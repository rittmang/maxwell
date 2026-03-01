package e2e_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"maxwell/internal/app"
	"maxwell/internal/cli"
	"maxwell/internal/web"
)

func TestPipelineSmokeCLIAndWeb(t *testing.T) {
	tmp := t.TempDir()
	downloads := filepath.Join(tmp, "downloads")
	processed := filepath.Join(tmp, "processed")
	if err := os.MkdirAll(downloads, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloads, "movie.mkv"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	var addCalls int
	var lastAddedMagnet string
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			addCalls++
			lastAddedMagnet = form.Get("urls")
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/pause":
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash": "h1", "name": "movie.mkv", "progress": 1.0, "state": "pausedUP", "save_path": downloads,
			}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer qb.Close()

	storageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Fatalf("expected PUT upload, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer storageServer.Close()

	cfgPath := filepath.Join(tmp, "config.yaml")
	cfg := `
vpn:
  require_safe_for_magnet_adds: true
torrent:
  provider: qbittorrent
  base_url: ` + qb.URL + `
  download_dir: ` + downloads + `
storage:
  provider: backblaze_b2
  endpoint: ` + storageServer.URL + `
  bucket: media
  public_base_url: https://cdn.example.com
ffmpeg:
  bin: copy
  preset: h264_1080p_fast
paths:
  downloads_dir: ` + downloads + `
  processed_dir: ` + processed + `
workers:
  conversion: 1
  upload: 1
security:
  web_bind: 127.0.0.1:7777
  web_token: secret
database:
  path: ` + filepath.Join(tmp, "maxwell.db") + `
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Getenv("MAXWELL_VPN_FORCE_STATE")
	defer os.Setenv("MAXWELL_VPN_FORCE_STATE", old)
	if err := os.Setenv("MAXWELL_VPN_FORCE_STATE", "SAFE"); err != nil {
		t.Fatal(err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	r := cli.NewRunner(stdout, stderr)

	code := r.Execute([]string{"--config", cfgPath, "torrents", "add", "magnet:?xt=urn:btih:test"})
	if code != 0 {
		t.Fatalf("torrents add failed (%d): %s", code, stderr.String())
	}
	if addCalls == 0 {
		t.Fatalf("expected qb add endpoint to be called")
	}

	if err := os.Setenv("MAXWELL_VPN_FORCE_STATE", "UNSAFE"); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code = r.Execute([]string{"--config", cfgPath, "run", "--cycles", "1", "--require-safe-vpn=false"})
	if code != 0 {
		t.Fatalf("run failed (%d): %s", code, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = r.Execute([]string{"--config", cfgPath, "links", "list"})
	if code != 0 {
		t.Fatalf("links list failed (%d): %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "https://cdn.example.com") {
		t.Fatalf("expected final link output, got %s", stdout.String())
	}

	if err := os.Setenv("MAXWELL_VPN_FORCE_STATE", "SAFE"); err != nil {
		t.Fatal(err)
	}

	svc, cfgLoaded, err := app.Build(cfgPath)
	if err != nil {
		t.Fatalf("build for web failed: %v", err)
	}
	defer svc.Close()
	ws := httptest.NewServer(web.NewServer(svc, cfgLoaded.Security.WebToken, cfgLoaded.Security.CSRF).Handler())
	defer ws.Close()

	longMagnet := "magnet:?xt=urn:btih:06E93E1901A0F73F48CC499C2EAF076A04D37153&dn=In+the+Blink+of+an+Eye+2026+1080p+WEB-DL+HEVC+x265+5.1+BONE&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce&tr=udp%3A%2F%2Fopentracker.io%3A6969%2Fannounce&tr=udp%3A%2F%2Fopen.stealth.si%3A80%2Fannounce&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2F&tr=udp%3A%2F%2Ftracker.torrent.eu.org%3A451%2Fannounce&tr=udp%3A%2F%2Fbandito.byterunner.io%3A6969%2Fannounce&tr=udp%3A%2F%2Ftracker.qu.ax%3A6969%2Fannounce&tr=http%3A%2F%2Ftracker.renfei.net%3A8080%2Fannounce&tr=udp%3A%2F%2Fopen.free-tracker.ga%3A6969%2Fannounce&tr=http%3A%2F%2Ftracker.ipv6tracker.org%2Fannounce&tr=udp%3A%2F%2Ftracker2.dler.org%3A80%2Fannounce&tr=udp%3A%2F%2Fexodus.desync.com%3A6969%2Fannounce&tr=udp%3A%2F%2Fopen.tracker.cl%3A1337%2Fannounce&tr=udp%3A%2F%2Ftracker.qu.ax%3A6969%2Fannounce&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce&tr=http%3A%2F%2Ftracker.openbittorrent.com%3A80%2Fannounce&tr=udp%3A%2F%2Fopentracker.i2p.rocks%3A6969%2Fannounce&tr=udp%3A%2F%2Ftracker.internetwarriors.net%3A1337%2Fannounce&tr=udp%3A%2F%2Ftracker.leechers-paradise.org%3A6969%2Fannounce&tr=udp%3A%2F%2Fcoppersurfer.tk%3A6969%2Fannounce&tr=udp%3A%2F%2Ftracker.zer0day.to%3A1337%2Fannounce"
	indexResp, err := http.Get(ws.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := io.ReadAll(indexResp.Body)
	indexResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var csrfCookie *http.Cookie
	for _, c := range indexResp.Cookies() {
		if c.Name == "maxwell_csrf" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil || strings.TrimSpace(csrfCookie.Value) == "" {
		t.Fatalf("expected csrf cookie from index")
	}
	csrfToken := extractJSConstString(t, string(indexBody), "initialCSRFToken")
	if csrfToken != csrfCookie.Value {
		t.Fatalf("csrf token mismatch: token=%q cookie=%q", csrfToken, csrfCookie.Value)
	}
	addForm := url.Values{"magnet": []string{longMagnet}}
	addReq, _ := http.NewRequest(http.MethodPost, ws.URL+"/api/torrents/add", strings.NewReader(addForm.Encode()))
	addReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addReq.Header.Set("X-API-Token", "secret")
	addReq.Header.Set("X-CSRF-Token", csrfToken)
	addReq.AddCookie(csrfCookie)
	addResp, err := http.DefaultClient.Do(addReq)
	if err != nil {
		t.Fatal(err)
	}
	if addResp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(addResp.Body)
		addResp.Body.Close()
		t.Fatalf("web add magnet status=%d body=%s", addResp.StatusCode, string(msg))
	}
	addResp.Body.Close()
	if addCalls < 2 {
		t.Fatalf("expected both CLI and web add to call qb add endpoint, got calls=%d", addCalls)
	}
	if lastAddedMagnet != longMagnet {
		t.Fatalf("web add magnet mismatch")
	}

	resp, err := http.Get(ws.URL + "/api/links")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("web links status: %d", resp.StatusCode)
	}
	var links []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&links); err != nil {
		t.Fatal(err)
	}
	if len(links) == 0 {
		t.Fatalf("expected web links to contain uploaded item")
	}
}

func extractJSConstString(t *testing.T, html, name string) string {
	t.Helper()
	re := regexp.MustCompile(`const\s+` + regexp.QuoteMeta(name) + `\s*=\s*("(?:[^"\\]|\\.)*");`)
	match := re.FindStringSubmatch(html)
	if len(match) < 2 {
		t.Fatalf("missing JS const %q", name)
	}
	value, err := strconv.Unquote(match[1])
	if err != nil {
		t.Fatalf("invalid quoted const %q: %v", name, err)
	}
	return value
}
