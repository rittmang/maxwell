package vpn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"maxwell/internal/config"
)

var defaultRouteInterfaceRegex = regexp.MustCompile(`(?m)^\s*interface:\s+(\S+)\s*$`)
var wifiDeviceRegex = regexp.MustCompile(`(?ms)Hardware Port:\s*Wi-Fi\s*\nDevice:\s*(\S+)`)
var airportSSIDRegex = regexp.MustCompile(`(?m)^Current Wi-Fi Network:\s*(.+)\s*$`)

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %v: %w (%s)", name, args, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type httpGetter interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

type clientGetter struct {
	client *http.Client
}

func (g clientGetter) Get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// SystemDetector evaluates real OS and network signals.
type SystemDetector struct {
	cfg    config.VPNConfig
	runner commandRunner
	http   httpGetter
	mu     sync.RWMutex
	cache  publicSignalCache
}

type publicSignalCache struct {
	IP       string
	ASN      string
	ExpireAt time.Time
	HasValue bool
}

func NewSystemDetector(cfg config.VPNConfig) *SystemDetector {
	return &SystemDetector{
		cfg:    cfg,
		runner: execRunner{},
		http: clientGetter{client: &http.Client{
			Timeout: 4 * time.Second,
		}},
	}
}

func (d *SystemDetector) Check(ctx context.Context) (Signals, error) {
	s := Signals{}
	var hardErrs []string

	defaultIface, hasDefaultRoute, err := d.defaultRouteInterface(ctx)
	if err == nil {
		s.DefaultRouteInterface = defaultIface
		s.HasDefaultRoute = hasDefaultRoute
	} else {
		hardErrs = append(hardErrs, "default-route:"+err.Error())
	}

	ifaces, err := d.listInterfaces(ctx)
	if err == nil {
		s.TunnelInterfaces = tunnelInterfaces(ifaces, d.allowedPrefixes())
	} else {
		hardErrs = append(hardErrs, "ifaces:"+err.Error())
	}

	if hasDefaultRoute && hasPrefix(defaultIface, d.allowedPrefixes()) {
		s.HasTunnelInterface = true
	}

	if ssid, err := d.currentSSID(ctx); err == nil {
		s.ActiveSSID = ssid
		if containsFold(d.cfg.HomeSSIDs, ssid) {
			s.OnHomeNetwork = true
		}
	}

	if hasDefaultRoute {
		if ip, err := d.ifaceIP(ctx, defaultIface); err == nil {
			s.DefaultInterfaceIP = ip
			if ipInAnyCIDR(ip, d.cfg.HomeCIDRs) {
				s.OnHomeNetwork = true
			}
		}
	}

	publicIP, asn, err := d.publicIPAndASNMaybeCached(ctx)
	if err == nil {
		s.PublicIP = publicIP
		s.PublicASN = asn
		if containsFold(d.cfg.HomePublicIPs, publicIP) {
			s.PublicIPLooksHome = true
			s.OnHomeNetwork = true
		}
		if containsFold(d.cfg.HomeASNs, asn) {
			s.OnHomeNetwork = true
		}
		if containsFold(d.cfg.AllowedVPNASNs, asn) {
			s.ASNLooksVPN = true
		}
	}

	if len(hardErrs) > 0 {
		return s, errors.New(strings.Join(hardErrs, "; "))
	}
	return s, nil
}

func (d *SystemDetector) allowedPrefixes() []string {
	if len(d.cfg.AllowedTunnelIfPrefixes) == 0 {
		return []string{"utun", "tun", "wg", "ppp", "ipsec"}
	}
	return d.cfg.AllowedTunnelIfPrefixes
}

func (d *SystemDetector) defaultRouteInterface(ctx context.Context) (string, bool, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		out, err := d.runner.Run(ctx, "route", "-n", "get", "default")
		if err != nil {
			return "", false, err
		}
		m := defaultRouteInterfaceRegex.FindStringSubmatch(out)
		if len(m) < 2 {
			return "", false, fmt.Errorf("default interface not found")
		}
		return strings.TrimSpace(m[1]), true, nil
	default:
		return "", false, fmt.Errorf("unsupported os: %s", runtime.GOOS)
	}
}

func (d *SystemDetector) listInterfaces(ctx context.Context) ([]string, error) {
	switch runtime.GOOS {
	case "darwin", "linux":
		out, err := d.runner.Run(ctx, "ifconfig", "-l")
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(out)
		if len(fields) == 0 {
			return nil, fmt.Errorf("no interfaces")
		}
		return fields, nil
	default:
		return nil, fmt.Errorf("unsupported os: %s", runtime.GOOS)
	}
}

func (d *SystemDetector) currentSSID(ctx context.Context) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("ssid lookup only implemented on darwin")
	}
	ports, err := d.runner.Run(ctx, "networksetup", "-listallhardwareports")
	if err != nil {
		return "", err
	}
	m := wifiDeviceRegex.FindStringSubmatch(ports)
	if len(m) < 2 {
		return "", fmt.Errorf("wifi device not found")
	}
	dev := strings.TrimSpace(m[1])
	out, err := d.runner.Run(ctx, "networksetup", "-getairportnetwork", dev)
	if err != nil {
		return "", err
	}
	sm := airportSSIDRegex.FindStringSubmatch(out)
	if len(sm) < 2 {
		return "", fmt.Errorf("ssid not found")
	}
	ssid := strings.TrimSpace(sm[1])
	if strings.Contains(strings.ToLower(ssid), "not associated") {
		return "", fmt.Errorf("not associated")
	}
	return ssid, nil
}

func (d *SystemDetector) ifaceIP(ctx context.Context, iface string) (string, error) {
	if strings.TrimSpace(iface) == "" {
		return "", fmt.Errorf("iface empty")
	}
	switch runtime.GOOS {
	case "darwin":
		out, err := d.runner.Run(ctx, "ipconfig", "getifaddr", iface)
		if err != nil {
			return "", err
		}
		ip := strings.TrimSpace(out)
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("invalid ip: %s", ip)
		}
		return ip, nil
	case "linux":
		out, err := d.runner.Run(ctx, "sh", "-c", "ip -4 addr show dev "+shellQuote(iface)+" | awk '/inet / {print $2}' | head -n1")
		if err != nil {
			return "", err
		}
		cidr := strings.TrimSpace(out)
		ip := strings.SplitN(cidr, "/", 2)[0]
		if net.ParseIP(ip) == nil {
			return "", fmt.Errorf("invalid ip: %s", ip)
		}
		return ip, nil
	default:
		return "", fmt.Errorf("unsupported os: %s", runtime.GOOS)
	}
}

func (d *SystemDetector) publicIPAndASN(ctx context.Context) (string, string, error) {
	urls := d.cfg.PublicIPCheckURLs
	if len(urls) == 0 {
		urls = []string{"https://api.ipify.org?format=json", "https://ipinfo.io/json"}
	}
	var lastErr error
	for _, u := range urls {
		reqCtx := ctx
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 1200*time.Millisecond {
			var cancel context.CancelFunc
			reqCtx, cancel = context.WithTimeout(ctx, 1200*time.Millisecond)
			b, err := d.http.Get(reqCtx, u)
			cancel()
			if err != nil {
				lastErr = err
				continue
			}
			ip, asn, err := parsePublicResponse(b)
			if err != nil {
				lastErr = err
				continue
			}
			if ip != "" {
				return ip, asn, nil
			}
			continue
		}
		b, err := d.http.Get(reqCtx, u)
		if err != nil {
			lastErr = err
			continue
		}
		ip, asn, err := parsePublicResponse(b)
		if err != nil {
			lastErr = err
			continue
		}
		if ip != "" {
			return ip, asn, nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no response")
	}
	return "", "", lastErr
}

func (d *SystemDetector) publicIPAndASNMaybeCached(ctx context.Context) (string, string, error) {
	now := time.Now()
	d.mu.RLock()
	c := d.cache
	d.mu.RUnlock()
	if c.HasValue && now.Before(c.ExpireAt) {
		return c.IP, c.ASN, nil
	}

	ip, asn, err := d.publicIPAndASN(ctx)
	if err != nil {
		// If fresh lookup fails, use stale cached value instead of blocking status updates.
		if c.HasValue {
			return c.IP, c.ASN, nil
		}
		return "", "", err
	}
	ttl := 30 * time.Second
	if d.cfg.CheckIntervalSeconds > 0 {
		ttl = time.Duration(d.cfg.CheckIntervalSeconds*3) * time.Second
		if ttl < 15*time.Second {
			ttl = 15 * time.Second
		}
		if ttl > 2*time.Minute {
			ttl = 2 * time.Minute
		}
	}
	d.mu.Lock()
	d.cache = publicSignalCache{
		IP:       ip,
		ASN:      asn,
		ExpireAt: now.Add(ttl),
		HasValue: true,
	}
	d.mu.Unlock()
	return ip, asn, nil
}

func parsePublicResponse(b []byte) (string, string, error) {
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		txt := strings.TrimSpace(string(b))
		if net.ParseIP(txt) != nil {
			return txt, "", nil
		}
		return "", "", err
	}

	ip := ""
	for _, k := range []string{"ip", "query", "address"} {
		if v, ok := obj[k].(string); ok && net.ParseIP(strings.TrimSpace(v)) != nil {
			ip = strings.TrimSpace(v)
			break
		}
	}

	asn := ""
	if v, ok := obj["asn"]; ok {
		switch t := v.(type) {
		case string:
			asn = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(t), "AS"))
		case float64:
			asn = strings.TrimSpace(fmt.Sprintf("%.0f", t))
		}
	}
	if asn == "" {
		if org, ok := obj["org"].(string); ok {
			up := strings.ToUpper(org)
			if strings.HasPrefix(up, "AS") {
				parts := strings.Fields(up)
				if len(parts) > 0 {
					asn = strings.TrimPrefix(parts[0], "AS")
				}
			}
		}
	}

	if ip == "" {
		return "", "", fmt.Errorf("ip not found in response")
	}
	return ip, asn, nil
}

func tunnelInterfaces(ifaces []string, prefixes []string) []string {
	out := make([]string, 0)
	for _, iface := range ifaces {
		if hasPrefix(iface, prefixes) {
			out = append(out, iface)
		}
	}
	return out
}

func hasPrefix(v string, prefixes []string) bool {
	vl := strings.ToLower(strings.TrimSpace(v))
	for _, p := range prefixes {
		if strings.HasPrefix(vl, strings.ToLower(strings.TrimSpace(p))) {
			return true
		}
	}
	return false
}

func containsFold(list []string, value string) bool {
	v := strings.TrimSpace(value)
	if v == "" {
		return false
	}
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), v) {
			return true
		}
	}
	return false
}

func ipInAnyCIDR(ip string, cidrs []string) bool {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return false
	}
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(strings.TrimSpace(c))
		if err != nil {
			continue
		}
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
