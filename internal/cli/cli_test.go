package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maxwell/internal/config"
	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/vpn"
)

type fakeCLIService struct {
	added        string
	syncCalls    int
	processCalls int
}

func (f *fakeCLIService) Close() error { return nil }
func (f *fakeCLIService) VPNStatus(context.Context) (model.VPNState, vpn.Signals, error) {
	return model.VPNStateSafe, vpn.Signals{HasTunnelInterface: true}, nil
}
func (f *fakeCLIService) ListTorrents(context.Context) ([]model.Torrent, error) {
	return []model.Torrent{{ID: "h1", Name: "n1", Progress: 0.2, State: "downloading"}}, nil
}
func (f *fakeCLIService) AddMagnet(_ context.Context, magnet string) (string, error) {
	f.added = magnet
	return "id-1", nil
}
func (f *fakeCLIService) SyncCompletedDownloads(context.Context) error {
	f.syncCalls++
	return nil
}
func (f *fakeCLIService) ProcessOnce(context.Context) error {
	f.processCalls++
	return nil
}
func (f *fakeCLIService) ListConversionJobs(context.Context) ([]model.ConversionJob, error) {
	return []model.ConversionJob{{ID: 1}}, nil
}
func (f *fakeCLIService) ListUploadJobs(context.Context) ([]model.UploadJob, error) {
	return []model.UploadJob{{ID: 2}}, nil
}
func (f *fakeCLIService) ListLinks(context.Context, int) ([]model.LinkRecord, error) {
	return []model.LinkRecord{{ID: 1, FinalURL: "https://example.com"}}, nil
}
func (f *fakeCLIService) ListEvents(context.Context, int) ([]model.Event, error) {
	return []model.Event{{ID: 1}}, nil
}
func (f *fakeCLIService) Stats(context.Context) (map[string]int64, error) {
	return map[string]int64{"downloads": 1}, nil
}
func (f *fakeCLIService) EventBus() *events.Bus { return events.NewBus() }

func newTestRunner(svc *fakeCLIService) (*Runner, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	err := &bytes.Buffer{}
	r := NewRunner(out, err)
	r.Build = func(string) (ServiceAPI, error) { return svc, nil }
	return r, out, err
}

func TestUnknownCommand(t *testing.T) {
	r, _, errBuf := newTestRunner(&fakeCLIService{})
	code := r.Execute([]string{"nope"})
	if code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if !strings.Contains(errBuf.String(), "unknown command") {
		t.Fatalf("expected unknown command message")
	}
}

func TestTorrentsAdd(t *testing.T) {
	svc := &fakeCLIService{}
	r, out, _ := newTestRunner(svc)
	code := r.Execute([]string{"torrents", "add", "magnet:?xt=urn:btih:test"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if svc.added == "" {
		t.Fatalf("expected AddMagnet to be called")
	}
	if !strings.Contains(out.String(), "added=id-1") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestRunCycles(t *testing.T) {
	svc := &fakeCLIService{}
	r, out, _ := newTestRunner(svc)
	code := r.Execute([]string{"run", "--cycles", "3"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if svc.syncCalls != 3 || svc.processCalls != 3 {
		t.Fatalf("expected 3 calls each, sync=%d process=%d", svc.syncCalls, svc.processCalls)
	}
	if !strings.Contains(out.String(), "run complete") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestQueueListAlias(t *testing.T) {
	svc := &fakeCLIService{}
	r, out, _ := newTestRunner(svc)
	code := r.Execute([]string{"queue", "list"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d", code)
	}
	if !strings.Contains(out.String(), "conversion_jobs=") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestDoctorCommand(t *testing.T) {
	qb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer qb.Close()

	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.yaml")
	cfgYAML := `
torrent:
  provider: qbittorrent
  base_url: ` + qb.URL + `
  download_dir: ./downloads
storage:
  provider: backblaze_b2
  endpoint: http://127.0.0.1:9000
  bucket: bucket
ffmpeg:
  bin: copy
  ffprobe_bin: ""
paths:
  processed_dir: ./processed
workers:
  conversion: 1
  upload: 1
  max_attempts: 3
  backoff_seconds: 1
state_store:
  driver: sqlite
  dsn: ./maxwell.db
  max_open_conns: 1
database:
  path: ./maxwell.db
`
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	r, out, errBuf := newTestRunner(&fakeCLIService{})
	code := r.Execute([]string{"--config", cfgPath, "doctor"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "doctor: ok") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestTorrentsSetupQBitWritesINIAndConfig(t *testing.T) {
	tmp := t.TempDir()
	iniPath := filepath.Join(tmp, "qBittorrent.ini")
	cfgPath := filepath.Join(tmp, "config.yaml")
	initialINI := `[Preferences]
WebUI\Enabled=false
WebUI\Address=*
WebUI\Port=8090
WebUI\LocalHostAuth=false
`
	if err := os.WriteFile(iniPath, []byte(initialINI), 0o644); err != nil {
		t.Fatal(err)
	}
	initialCfg := `
torrent:
  provider: qbittorrent
  base_url: http://127.0.0.1:8080
  download_dir: ./downloads
storage:
  provider: backblaze_b2
  endpoint: http://127.0.0.1:9000
  bucket: media
paths:
  processed_dir: ./processed
workers:
  conversion: 1
  upload: 1
  max_attempts: 3
  backoff_seconds: 1
state_store:
  driver: sqlite
  dsn: ./maxwell.db
  max_open_conns: 1
`
	if err := os.WriteFile(cfgPath, []byte(initialCfg), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Getenv("MAXWELL_QBITTORRENT_INI")
	t.Cleanup(func() { _ = os.Setenv("MAXWELL_QBITTORRENT_INI", old) })
	if err := os.Setenv("MAXWELL_QBITTORRENT_INI", iniPath); err != nil {
		t.Fatal(err)
	}

	r, out, errBuf := newTestRunner(&fakeCLIService{})
	code := r.Execute([]string{"--config", cfgPath, "torrents", "setup-qbittorrent", "--verify=false", "--start=false"})
	if code != 0 {
		t.Fatalf("expected code 0, got %d: %s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "qbittorrent_setup=ok") {
		t.Fatalf("unexpected output: %s", out.String())
	}

	b, err := os.ReadFile(iniPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{
		`WebUI\Enabled=true`,
		`WebUI\Address=127.0.0.1`,
		`WebUI\Port=8090`,
		`WebUI\LocalHostAuth=false`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected ini to contain %q, got:\n%s", want, text)
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Torrent.BaseURL != "http://127.0.0.1:8090" {
		t.Fatalf("unexpected base_url: %s", cfg.Torrent.BaseURL)
	}
}
