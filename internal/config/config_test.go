package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProviderMatrix(t *testing.T) {
	cfg := Default()
	cfg.Torrent.Provider = "transmission"
	cfg.Storage.Provider = "aws_s3"
	cfg.Storage.Endpoint = "http://localhost:9000"
	cfg.Storage.Bucket = "bucket"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got %v", err)
	}

	cfg.Torrent.Provider = "invalid"
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected error for invalid torrent provider")
	}
}

func TestLoadConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
torrent:
  provider: utorrent
  base_url: http://127.0.0.1:8080
  download_dir: ./downloads
storage:
  provider: onedrive
  access_token: test-token
ffmpeg:
  bin: copy
paths:
  processed_dir: ./processed
workers:
  conversion: 1
  upload: 1
database:
  path: ./maxwell.db
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if cfg.Torrent.Provider != "utorrent" {
		t.Fatalf("unexpected torrent provider: %s", cfg.Torrent.Provider)
	}
}

func TestResolvePath(t *testing.T) {
	base := "/tmp/example/config.yaml"
	if got := ResolvePath(base, "./db.sqlite"); got != "/tmp/example/db.sqlite" {
		t.Fatalf("unexpected path: %s", got)
	}
}

func TestEffectiveStateStoreBackwardCompatibility(t *testing.T) {
	cfg := Default()
	cfg.StateStore.DSN = ""
	cfg.Database.Path = "./legacy.db"
	ss := cfg.EffectiveStateStore()
	if ss.Driver != "sqlite" {
		t.Fatalf("unexpected driver: %s", ss.Driver)
	}
	if ss.DSN != "./legacy.db" {
		t.Fatalf("unexpected dsn: %s", ss.DSN)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := Default()
	cfg.Torrent.Provider = "qbittorrent"
	cfg.Torrent.BaseURL = "http://127.0.0.1:8090"
	if err := Save(path, cfg); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loaded.Torrent.BaseURL != "http://127.0.0.1:8090" {
		t.Fatalf("unexpected base_url: %s", loaded.Torrent.BaseURL)
	}
}
