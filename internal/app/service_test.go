package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"maxwell/internal/config"
	"maxwell/internal/convert"
	"maxwell/internal/model"
	"maxwell/internal/queue"
	"maxwell/internal/storage"
	"maxwell/internal/torrent"
	"maxwell/internal/vpn"
)

type fakeGate struct {
	state model.VPNState
}

func (f fakeGate) Status(context.Context) (model.VPNState, vpn.Signals, error) {
	return f.state, vpn.Signals{}, nil
}

type fakeTorrent struct {
	list       []model.Torrent
	added      []string
	pauseCalls int
}

func (f *fakeTorrent) Name() string { return "fake" }
func (f *fakeTorrent) List(context.Context) ([]model.Torrent, error) {
	return f.list, nil
}
func (f *fakeTorrent) AddMagnet(_ context.Context, magnet string, _ string) (string, error) {
	f.added = append(f.added, magnet)
	return magnet, nil
}
func (f *fakeTorrent) PauseAll(context.Context) error {
	f.pauseCalls++
	return nil
}
func (f *fakeTorrent) ResumeAll(context.Context) error              { return nil }
func (f *fakeTorrent) PauseHashes(context.Context, []string) error  { return nil }
func (f *fakeTorrent) ResumeHashes(context.Context, []string) error { return nil }

type fakeUploader struct {
	urls map[string]string
}

func (f fakeUploader) Name() string { return "fake-storage" }
func (f fakeUploader) Upload(_ context.Context, localPath, objectKey string) (string, error) {
	if u, ok := f.urls[localPath]; ok {
		return u, nil
	}
	return "https://example.com/" + objectKey, nil
}

type flakyConverter struct {
	failUntil int
	calls     int
}

func (f *flakyConverter) Name() string { return "flaky" }

func (f *flakyConverter) Convert(_ context.Context, inputPath, outputPath, preset string) error {
	f.calls++
	if f.calls <= f.failUntil {
		return errors.New("simulated conversion failure")
	}
	in, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, in, 0o644)
}

func newService(t *testing.T, gate fakeGate, torrents torrent.Client, uploader storage.Uploader, cfg config.Config) *Service {
	t.Helper()
	if cfg.StateStore.DSN == "" {
		cfg.StateStore.Driver = "sqlite"
		cfg.StateStore.DSN = filepath.Join(t.TempDir(), "maxwell.db")
		cfg.StateStore.MaxOpenConns = 1
	}
	if cfg.Paths.ProcessedDir == "" {
		cfg.Paths.ProcessedDir = filepath.Join(t.TempDir(), "processed")
	}
	store, err := queue.Open(cfg.EffectiveStateStore())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	svc, err := NewService(Dependencies{
		Config:    cfg,
		Gate:      gate,
		Torrents:  torrents,
		Uploader:  uploader,
		Converter: convert.CopyConverter{},
		Store:     store,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

func TestAddMagnetBlockedWhenUnsafe(t *testing.T) {
	cfg := config.Default()
	cfg.VPN.RequireSafeForMagnetAdds = true
	cfg.StateStore.DSN = filepath.Join(t.TempDir(), "db.sqlite")
	cfg.Paths.ProcessedDir = filepath.Join(t.TempDir(), "processed")

	ft := &fakeTorrent{}
	svc := newService(t, fakeGate{state: model.VPNStateUnsafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	_, err := svc.AddMagnet(context.Background(), "magnet:?xt=urn:btih:test")
	if err == nil {
		t.Fatalf("expected unsafe vpn error")
	}
	if !errors.Is(err, ErrUnsafeVPN) {
		t.Fatalf("expected ErrUnsafeVPN, got %v", err)
	}
	if ft.pauseCalls == 0 {
		t.Fatalf("expected pause all on unsafe state")
	}
}

func TestPipelineEndToEndInService(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	processedDir := filepath.Join(dir, "processed")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	srcFile := filepath.Join(downloadDir, "video.mkv")
	if err := os.WriteFile(srcFile, []byte("video-content"), 0o644); err != nil {
		t.Fatal(err)
	}

	ft := &fakeTorrent{
		list: []model.Torrent{
			{ID: "hash1", Name: "video.mkv", SavePath: downloadDir, Completed: true, Progress: 1},
		},
	}
	cfg := config.Default()
	cfg.Paths.ProcessedDir = processedDir
	cfg.StateStore.DSN = filepath.Join(dir, "maxwell.db")
	cfg.FFmpeg.Preset = "h264_1080p_fast"
	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	ctx := context.Background()
	if err := svc.SyncCompletedDownloads(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := svc.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}

	links, err := svc.ListLinks(ctx, 10)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(links))
	}
	if links[0].FinalURL == "" {
		t.Fatalf("expected final URL")
	}
}

func TestConversionRetryAndRecovery(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	processedDir := filepath.Join(dir, "processed")
	_ = os.MkdirAll(downloadDir, 0o755)
	_ = os.MkdirAll(processedDir, 0o755)
	srcFile := filepath.Join(downloadDir, "clip.mkv")
	_ = os.WriteFile(srcFile, []byte("x"), 0o644)

	ft := &fakeTorrent{list: []model.Torrent{{ID: "h1", Name: "clip.mkv", SavePath: downloadDir, Completed: true, Progress: 1}}}
	cfg := config.Default()
	cfg.Paths.ProcessedDir = processedDir
	cfg.StateStore.DSN = filepath.Join(dir, "maxwell.db")
	cfg.Workers.MaxAttempts = 3
	cfg.Workers.BackoffSeconds = 1
	store, err := queue.Open(cfg.EffectiveStateStore())
	if err != nil {
		t.Fatal(err)
	}
	conv := &flakyConverter{failUntil: 1}
	svc, err := NewService(Dependencies{
		Config:    cfg,
		Gate:      fakeGate{state: model.VPNStateSafe},
		Torrents:  ft,
		Uploader:  fakeUploader{urls: map[string]string{}},
		Converter: conv,
		Store:     store,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	ctx := context.Background()
	if err := svc.SyncCompletedDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// first run fails conversion and requeues
	jobs, err := svc.ListConversionJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].Status != model.JobStatusQueued || jobs[0].Attempts != 1 {
		t.Fatalf("unexpected retry state: %+v", jobs)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := svc.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	links, err := svc.ListLinks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("expected successful retry to produce link")
	}
}
