package vpn

import (
	"context"
	"fmt"
	"testing"

	"maxwell/internal/config"
)

type fakeRunner struct {
	outputs map[string]string
	errs    map[string]error
}

func (f fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	k := name + " " + joinArgs(args)
	if err, ok := f.errs[k]; ok {
		return "", err
	}
	if out, ok := f.outputs[k]; ok {
		return out, nil
	}
	return "", fmt.Errorf("no fake output for %s", k)
}

type fakeHTTP struct {
	responses map[string][]byte
	errs      map[string]error
}

func (f fakeHTTP) Get(_ context.Context, url string) ([]byte, error) {
	if err, ok := f.errs[url]; ok {
		return nil, err
	}
	if out, ok := f.responses[url]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("no fake response for %s", url)
}

func TestParsePublicResponse(t *testing.T) {
	ip, asn, err := parsePublicResponse([]byte(`{"ip":"1.2.3.4","org":"AS13335 Cloudflare"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ip != "1.2.3.4" {
		t.Fatalf("ip=%s", ip)
	}
	if asn != "13335" {
		t.Fatalf("asn=%s", asn)
	}
}

func TestSystemDetectorMacSignals(t *testing.T) {
	cfg := config.Default().VPN
	cfg.HomeSSIDs = []string{"MyHomeWiFi"}
	cfg.HomeCIDRs = []string{"192.168.1.0/24"}
	cfg.PublicIPCheckURLs = []string{"https://api.ipify.org?format=json"}

	d := &SystemDetector{cfg: cfg,
		runner: fakeRunner{outputs: map[string]string{
			"route -n get default":                "interface: en0\n",
			"ifconfig -l":                         "lo0 en0 utun3",
			"networksetup -listallhardwareports":  "Hardware Port: Wi-Fi\nDevice: en0\n",
			"networksetup -getairportnetwork en0": "Current Wi-Fi Network: MyHomeWiFi\n",
			"ipconfig getifaddr en0":              "192.168.1.12\n",
		}, errs: map[string]error{}},
		http: fakeHTTP{responses: map[string][]byte{
			"https://api.ipify.org?format=json": []byte(`{"ip":"198.51.100.7"}`),
		}},
	}

	sig, err := d.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sig.HasDefaultRoute || sig.DefaultRouteInterface != "en0" {
		t.Fatalf("unexpected route signal: %+v", sig)
	}
	if sig.HasTunnelInterface {
		t.Fatalf("expected no tunnel on default route")
	}
	if !sig.OnHomeNetwork {
		t.Fatalf("expected home network true from SSID/CIDR")
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}
