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
	list          []model.Torrent
	added         []string
	pauseCalls    int
	pausedHashes  [][]string
	resumedHashes [][]string
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
func (f *fakeTorrent) ResumeAll(context.Context) error { return nil }
func (f *fakeTorrent) PauseHashes(_ context.Context, hashes []string) error {
	f.pausedHashes = append(f.pausedHashes, append([]string(nil), hashes...))
	return nil
}
func (f *fakeTorrent) ResumeHashes(_ context.Context, hashes []string) error {
	f.resumedHashes = append(f.resumedHashes, append([]string(nil), hashes...))
	return nil
}

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
	svc := newService(t, fakeGate{state: model.VPNStateUnsafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
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
		Gate:      fakeGate{state: model.VPNStateUnsafe},
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

func TestConversionContinuesWhileUploadHeldOnSafeVPN(t *testing.T) {
	dir := t.TempDir()
	downloadDir := filepath.Join(dir, "downloads")
	processedDir := filepath.Join(dir, "processed")
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(downloadDir, "clip.mkv")
	if err := os.WriteFile(srcFile, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}

	ft := &fakeTorrent{
		list: []model.Torrent{
			{ID: "h-safe", Name: "clip.mkv", SavePath: downloadDir, Completed: true, Progress: 1},
		},
	}
	cfg := config.Default()
	cfg.Paths.ProcessedDir = processedDir
	cfg.StateStore.DSN = filepath.Join(dir, "maxwell.db")

	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	ctx := context.Background()
	if err := svc.SyncCompletedDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	if err := svc.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}

	convJobs, err := svc.ListConversionJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(convJobs) != 1 || convJobs[0].Status != model.JobStatusDone {
		t.Fatalf("expected conversion done while vpn safe, got %+v", convJobs)
	}
	uplJobs, err := svc.ListUploadJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(uplJobs) != 1 || uplJobs[0].Status != model.JobStatusQueued {
		t.Fatalf("expected queued upload while vpn safe, got %+v", uplJobs)
	}
	links, err := svc.ListLinks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("expected no uploaded links while vpn safe, got %d", len(links))
	}
}

func TestPauseResumeItemControls(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Paths.ProcessedDir = filepath.Join(dir, "processed")
	cfg.StateStore.DSN = filepath.Join(dir, "maxwell.db")
	cfg.Workers.MaxAttempts = 3
	_ = os.MkdirAll(cfg.Paths.ProcessedDir, 0o755)

	ft := &fakeTorrent{}
	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()
	ctx := context.Background()

	if err := svc.PauseTorrent(ctx, "hash-1"); err != nil {
		t.Fatalf("pause torrent: %v", err)
	}
	if len(ft.pausedHashes) != 1 || len(ft.pausedHashes[0]) != 1 || ft.pausedHashes[0][0] != "hash-1" {
		t.Fatalf("unexpected paused hashes: %+v", ft.pausedHashes)
	}
	if err := svc.ResumeTorrent(ctx, "hash-1"); err != nil {
		t.Fatalf("resume torrent: %v", err)
	}
	if len(ft.resumedHashes) != 1 || len(ft.resumedHashes[0]) != 1 || ft.resumedHashes[0][0] != "hash-1" {
		t.Fatalf("unexpected resumed hashes: %+v", ft.resumedHashes)
	}

	inserted, err := svc.store.EnqueueConversion(ctx, "hash-1", "/in/a.mkv", "/out/a.mp4", "h264")
	if err != nil || !inserted {
		t.Fatalf("enqueue conversion: inserted=%v err=%v", inserted, err)
	}
	convJobs, err := svc.ListConversionJobs(ctx)
	if err != nil || len(convJobs) != 1 {
		t.Fatalf("list conversion jobs: len=%d err=%v", len(convJobs), err)
	}
	if err := svc.PauseConversionJob(ctx, convJobs[0].ID); err != nil {
		t.Fatalf("pause conversion: %v", err)
	}
	convJobs, err = svc.ListConversionJobs(ctx)
	if err != nil {
		t.Fatalf("list conversion jobs after pause: %v", err)
	}
	if convJobs[0].Status != model.JobStatusPaused {
		t.Fatalf("expected paused conversion status, got %s", convJobs[0].Status)
	}
	if err := svc.ResumeConversionJob(ctx, convJobs[0].ID); err != nil {
		t.Fatalf("resume conversion: %v", err)
	}
	convJobs, err = svc.ListConversionJobs(ctx)
	if err != nil {
		t.Fatalf("list conversion jobs after resume: %v", err)
	}
	if convJobs[0].Status != model.JobStatusQueued {
		t.Fatalf("expected queued conversion status, got %s", convJobs[0].Status)
	}

	inserted, err = svc.store.EnqueueUpload(ctx, "/tmp/a.mp4", "2026/03/01/a.mp4")
	if err != nil || !inserted {
		t.Fatalf("enqueue upload: inserted=%v err=%v", inserted, err)
	}
	uploadJobs, err := svc.ListUploadJobs(ctx)
	if err != nil || len(uploadJobs) != 1 {
		t.Fatalf("list upload jobs: len=%d err=%v", len(uploadJobs), err)
	}
	if err := svc.PauseUploadJob(ctx, uploadJobs[0].ID); err != nil {
		t.Fatalf("pause upload: %v", err)
	}
	uploadJobs, err = svc.ListUploadJobs(ctx)
	if err != nil {
		t.Fatalf("list upload jobs after pause: %v", err)
	}
	if uploadJobs[0].Status != model.JobStatusPaused {
		t.Fatalf("expected paused upload status, got %s", uploadJobs[0].Status)
	}
	if err := svc.ResumeUploadJob(ctx, uploadJobs[0].ID); err != nil {
		t.Fatalf("resume upload: %v", err)
	}
	uploadJobs, err = svc.ListUploadJobs(ctx)
	if err != nil {
		t.Fatalf("list upload jobs after resume: %v", err)
	}
	if uploadJobs[0].Status != model.JobStatusQueued {
		t.Fatalf("expected queued upload status, got %s", uploadJobs[0].Status)
	}
}

func TestOpenTorrentFolder(t *testing.T) {
	cfg := config.Default()
	tmp := t.TempDir()
	cfg.StateStore.DSN = filepath.Join(tmp, "maxwell.db")
	cfg.Paths.ProcessedDir = filepath.Join(tmp, "processed")
	_ = os.MkdirAll(cfg.Paths.ProcessedDir, 0o755)
	saveDir := filepath.Join(tmp, "downloads")
	torrentDir := filepath.Join(saveDir, "MoviePack")
	if err := os.MkdirAll(torrentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ft := &fakeTorrent{
		list: []model.Torrent{
			{ID: "h-open", Name: "MoviePack", SavePath: saveDir},
		},
	}
	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	var opened string
	svc.openDir = func(_ context.Context, path string) error {
		opened = path
		return nil
	}
	if err := svc.OpenTorrentFolder(context.Background(), "h-open"); err != nil {
		t.Fatalf("open torrent folder: %v", err)
	}
	if opened != torrentDir {
		t.Fatalf("expected open path %q, got %q", torrentDir, opened)
	}
}

func TestOpenTorrentFolderSingleFileFallsBackToSavePath(t *testing.T) {
	cfg := config.Default()
	tmp := t.TempDir()
	cfg.StateStore.DSN = filepath.Join(tmp, "maxwell.db")
	cfg.Paths.ProcessedDir = filepath.Join(tmp, "processed")
	_ = os.MkdirAll(cfg.Paths.ProcessedDir, 0o755)
	saveDir := filepath.Join(tmp, "downloads")
	if err := os.MkdirAll(saveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(saveDir, "movie.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ft := &fakeTorrent{
		list: []model.Torrent{
			{ID: "h-open-file", Name: "movie.mkv", SavePath: saveDir, ContentPath: filepath.Join(saveDir, "movie.mkv")},
		},
	}
	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	var opened string
	svc.openDir = func(_ context.Context, path string) error {
		opened = path
		return nil
	}
	if err := svc.OpenTorrentFolder(context.Background(), "h-open-file"); err != nil {
		t.Fatalf("open torrent folder: %v", err)
	}
	if opened != saveDir {
		t.Fatalf("expected open path %q, got %q", saveDir, opened)
	}
}

func TestOpenTorrentFolderPrefersExistingContentPathDirectory(t *testing.T) {
	cfg := config.Default()
	tmp := t.TempDir()
	cfg.StateStore.DSN = filepath.Join(tmp, "maxwell.db")
	cfg.Paths.ProcessedDir = filepath.Join(tmp, "processed")
	_ = os.MkdirAll(cfg.Paths.ProcessedDir, 0o755)

	saveDir := filepath.Join(tmp, "Movie-GG")
	contentDir := filepath.Join(saveDir, "Salaam Bombay!.1988.1080p.MUBI.WEB-DL.AAC.2.0.x264-Telly")
	if err := os.MkdirAll(contentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTorrent{
		list: []model.Torrent{
			{
				ID:          "h-salaam",
				Name:        "Salaam Bombay!.1988.1080p.MUBI.WEB-DL.AAC.2.0.x264-Telly",
				SavePath:    saveDir,
				ContentPath: contentDir,
			},
		},
	}
	svc := newService(t, fakeGate{state: model.VPNStateSafe}, ft, fakeUploader{urls: map[string]string{}}, cfg)
	defer svc.Close()

	var opened string
	svc.openDir = func(_ context.Context, path string) error {
		opened = path
		return nil
	}
	if err := svc.OpenTorrentFolder(context.Background(), "h-salaam"); err != nil {
		t.Fatalf("open torrent folder: %v", err)
	}
	if opened != contentDir {
		t.Fatalf("expected open path %q, got %q", contentDir, opened)
	}
}
