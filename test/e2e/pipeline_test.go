package e2e_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	var addCalled bool
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/add":
			addCalled = true
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
	if !addCalled {
		t.Fatalf("expected qb add endpoint to be called")
	}

	stdout.Reset()
	stderr.Reset()
	code = r.Execute([]string{"--config", cfgPath, "run", "--cycles", "1"})
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

	svc, cfgLoaded, err := app.Build(cfgPath)
	if err != nil {
		t.Fatalf("build for web failed: %v", err)
	}
	defer svc.Close()
	ws := httptest.NewServer(web.NewServer(svc, cfgLoaded.Security.WebToken, cfgLoaded.Security.CSRF).Handler())
	defer ws.Close()

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
