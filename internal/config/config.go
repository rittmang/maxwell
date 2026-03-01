package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	VPN        VPNConfig        `yaml:"vpn"`
	StateStore StateStoreConfig `yaml:"state_store"`
	Torrent    TorrentConfig    `yaml:"torrent"`
	Storage    StorageConfig    `yaml:"storage"`
	FFmpeg     FFmpegConfig     `yaml:"ffmpeg"`
	Paths      PathsConfig      `yaml:"paths"`
	Security   SecurityConfig   `yaml:"security"`
	Workers    WorkersConfig    `yaml:"workers"`
	Database   DatabaseConfig   `yaml:"database"` // backward compatibility
}

type VPNConfig struct {
	Mode                        string   `yaml:"mode"`
	AllowedTunnelIfPrefixes     []string `yaml:"allowed_tunnel_if_prefixes"`
	HomeSSIDs                   []string `yaml:"home_ssids"`
	HomeCIDRs                   []string `yaml:"home_cidrs"`
	HomePublicIPs               []string `yaml:"home_public_ips"`
	HomeASNs                    []string `yaml:"home_asns"`
	AllowedVPNASNs              []string `yaml:"allowed_vpn_asns"`
	CheckIntervalSeconds        int      `yaml:"check_interval_seconds"`
	PublicIPCheckURLs           []string `yaml:"public_ip_check_urls"`
	UnsafeDebounceMilliseconds  int      `yaml:"unsafe_debounce_milliseconds"`
	RequireSafeForMagnetAdds    bool     `yaml:"require_safe_for_magnet_adds"`
	RequireSafeOnStartupDefault bool     `yaml:"require_safe_on_startup_default"`
}

type TorrentConfig struct {
	Provider    string `yaml:"provider"`
	BaseURL     string `yaml:"base_url"`
	Username    string `yaml:"username"`
	Password    string `yaml:"password"`
	DownloadDir string `yaml:"download_dir"`
	Category    string `yaml:"category"`
}

type StorageConfig struct {
	Provider      string `yaml:"provider"`
	Endpoint      string `yaml:"endpoint"`
	Bucket        string `yaml:"bucket"`
	Region        string `yaml:"region"`
	KeyID         string `yaml:"key_id"`
	AppKey        string `yaml:"app_key"`
	PublicBaseURL string `yaml:"public_base_url"`
	AccessToken   string `yaml:"access_token"`
	DriveID       string `yaml:"drive_id"`
}

type FFmpegConfig struct {
	Bin        string `yaml:"bin"`
	FFProbeBin string `yaml:"ffprobe_bin"`
	Preset     string `yaml:"preset"`
	OutputDir  string `yaml:"output_dir"`
}

type PathsConfig struct {
	DownloadsDir string `yaml:"downloads_dir"`
	ProcessedDir string `yaml:"processed_dir"`
}

type SecurityConfig struct {
	WebBind  string `yaml:"web_bind"`
	WebToken string `yaml:"web_token"`
	CSRF     bool   `yaml:"csrf_enabled"`
}

type WorkersConfig struct {
	Conversion     int `yaml:"conversion"`
	Upload         int `yaml:"upload"`
	MaxAttempts    int `yaml:"max_attempts"`
	BackoffSeconds int `yaml:"backoff_seconds"`
}

type StateStoreConfig struct {
	Driver       string `yaml:"driver"`
	DSN          string `yaml:"dsn"`
	MaxOpenConns int    `yaml:"max_open_conns"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

func Default() Config {
	return Config{
		VPN: VPNConfig{
			Mode:                        "enforce",
			AllowedTunnelIfPrefixes:     []string{"utun", "tun", "wg", "ppp"},
			CheckIntervalSeconds:        8,
			UnsafeDebounceMilliseconds:  1000,
			RequireSafeForMagnetAdds:    true,
			RequireSafeOnStartupDefault: true,
			PublicIPCheckURLs:           []string{"https://api.ipify.org?format=json", "https://ipinfo.io/json"},
		},
		StateStore: StateStoreConfig{
			Driver:       "sqlite",
			DSN:          "./maxwell.db",
			MaxOpenConns: 1,
		},
		Torrent: TorrentConfig{
			Provider:    "qbittorrent",
			BaseURL:     "http://127.0.0.1:8080",
			DownloadDir: "./downloads",
			Category:    "maxwell",
		},
		Storage: StorageConfig{
			Provider: "backblaze_b2",
			Endpoint: "http://127.0.0.1:9000",
			Bucket:   "maxwell",
			Region:   "us-west-000",
		},
		FFmpeg: FFmpegConfig{
			Bin:        "copy",
			FFProbeBin: "ffprobe",
			Preset:     "h264_1080p_fast",
			OutputDir:  "./processed",
		},
		Paths: PathsConfig{
			DownloadsDir: "./downloads",
			ProcessedDir: "./processed",
		},
		Security: SecurityConfig{
			WebBind: "127.0.0.1:7777",
			CSRF:    true,
		},
		Workers:  WorkersConfig{Conversion: 1, Upload: 1, MaxAttempts: 5, BackoffSeconds: 5},
		Database: DatabaseConfig{Path: "./maxwell.db"},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, cfg.Validate()
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config yaml: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var errs []string

	// Backward compatibility with older schema using database.path.
	if strings.TrimSpace(c.StateStore.DSN) == "" && strings.TrimSpace(c.Database.Path) != "" {
		c.StateStore.DSN = c.Database.Path
	}
	if strings.TrimSpace(c.StateStore.Driver) == "" {
		c.StateStore.Driver = "sqlite"
	}

	if c.Torrent.Provider == "" {
		errs = append(errs, "torrent.provider is required")
	}
	if c.Torrent.BaseURL == "" {
		errs = append(errs, "torrent.base_url is required")
	}
	if c.Torrent.DownloadDir == "" {
		errs = append(errs, "torrent.download_dir is required")
	}

	torrentProvider := normalizeProvider(c.Torrent.Provider)
	switch torrentProvider {
	case "qbittorrent", "transmission", "utorrent":
	default:
		errs = append(errs, "torrent.provider must be one of qbittorrent|transmission|utorrent")
	}

	storageProvider := normalizeProvider(c.Storage.Provider)
	switch storageProvider {
	case "backblaze_b2", "aws_s3", "google_drive", "onedrive":
	default:
		errs = append(errs, "storage.provider must be one of backblaze_b2|aws_s3|google_drive|onedrive")
	}

	if c.Paths.ProcessedDir == "" {
		errs = append(errs, "paths.processed_dir is required")
	}
	if c.StateStore.DSN == "" {
		errs = append(errs, "state_store.dsn is required")
	}
	switch normalizeProvider(c.StateStore.Driver) {
	case "sqlite", "postgres", "mysql":
	default:
		errs = append(errs, "state_store.driver must be one of sqlite|postgres|mysql")
	}
	if c.StateStore.MaxOpenConns <= 0 {
		errs = append(errs, "state_store.max_open_conns must be > 0")
	}
	if c.Workers.Conversion <= 0 {
		errs = append(errs, "workers.conversion must be > 0")
	}
	if c.Workers.Upload <= 0 {
		errs = append(errs, "workers.upload must be > 0")
	}
	if c.Workers.MaxAttempts <= 0 {
		errs = append(errs, "workers.max_attempts must be > 0")
	}
	if c.Workers.BackoffSeconds < 0 {
		errs = append(errs, "workers.backoff_seconds must be >= 0")
	}

	if storageProvider == "backblaze_b2" || storageProvider == "aws_s3" {
		if c.Storage.Endpoint == "" {
			errs = append(errs, "storage.endpoint is required for s3-compatible providers")
		}
		if c.Storage.Bucket == "" {
			errs = append(errs, "storage.bucket is required for s3-compatible providers")
		}
	}

	if storageProvider == "google_drive" || storageProvider == "onedrive" {
		if c.Storage.AccessToken == "" {
			errs = append(errs, "storage.access_token is required for google_drive/onedrive")
		}
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c Config) EffectiveStateStore() StateStoreConfig {
	ss := c.StateStore
	if strings.TrimSpace(ss.Driver) == "" {
		ss.Driver = "sqlite"
	}
	if strings.TrimSpace(ss.DSN) == "" && strings.TrimSpace(c.Database.Path) != "" {
		ss.DSN = c.Database.Path
	}
	if ss.MaxOpenConns <= 0 {
		ss.MaxOpenConns = 1
	}
	return ss
}

func normalizeProvider(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func ResolvePath(baseFile, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	if baseFile == "" {
		return p
	}
	return filepath.Join(filepath.Dir(baseFile), p)
}
