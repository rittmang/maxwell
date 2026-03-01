package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"maxwell/internal/config"
)

func TestTryAutoSetupQBitUpdatesLocalConfigWhenEndpointDown(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	iniPath := filepath.Join(tmp, "qBittorrent.ini")

	cfg := config.Default()
	cfg.Torrent.Provider = "qbittorrent"
	cfg.Torrent.BaseURL = "http://127.0.0.1:65531"
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	initialINI := `[Preferences]
WebUI\Enabled=false
WebUI\Port=8099
WebUI\Address=*
WebUI\LocalHostAuth=false
`
	if err := os.WriteFile(iniPath, []byte(initialINI), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Getenv("MAXWELL_QBITTORRENT_INI")
	t.Cleanup(func() { _ = os.Setenv("MAXWELL_QBITTORRENT_INI", old) })
	if err := os.Setenv("MAXWELL_QBITTORRENT_INI", iniPath); err != nil {
		t.Fatal(err)
	}

	tryAutoSetupQBit(cfgPath)

	updated, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Torrent.BaseURL != "http://127.0.0.1:8099" {
		t.Fatalf("unexpected base_url after auto setup: %s", updated.Torrent.BaseURL)
	}
	b, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		`WebUI\Enabled=true`,
		`WebUI\Address=127.0.0.1`,
		`WebUI\Port=8099`,
		`WebUI\LocalHostAuth=false`,
	} {
		if !containsLine(text, want) {
			t.Fatalf("expected %q in ini, got:\n%s", want, text)
		}
	}
}

func TestIsLocalBaseURL(t *testing.T) {
	for _, tc := range []struct {
		url   string
		local bool
	}{
		{url: "http://127.0.0.1:8080", local: true},
		{url: "http://localhost:8080", local: true},
		{url: "http://[::1]:8080", local: true},
		{url: "http://10.0.0.5:8080", local: false},
		{url: "https://example.com", local: false},
	} {
		if got := isLocalBaseURL(tc.url); got != tc.local {
			t.Fatalf("isLocalBaseURL(%q)=%t want %t", tc.url, got, tc.local)
		}
	}
}

func TestIsQBitAuthErr(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{err: fmt.Errorf("qb list failed: status 403"), want: true},
		{err: fmt.Errorf("qb login failed: invalid credentials"), want: true},
		{err: fmt.Errorf("Your IP address has been banned after too many failed authentication attempts."), want: true},
		{err: fmt.Errorf("dial tcp 127.0.0.1:8080: connect: connection refused"), want: false},
	}
	for _, tc := range tests {
		if got := isQBitAuthErr(tc.err); got != tc.want {
			t.Fatalf("isQBitAuthErr(%q)=%t want %t", tc.err, got, tc.want)
		}
	}
}

func containsLine(text, want string) bool {
	for _, line := range splitLines(text) {
		if line == want {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	out := []string{}
	cur := ""
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch == '\r' {
			continue
		}
		if ch == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(ch)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
