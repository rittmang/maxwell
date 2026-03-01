package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"maxwell/internal/config"
	"maxwell/internal/convert"
	"maxwell/internal/model"
	"maxwell/internal/queue"
	"maxwell/internal/storage"
	"maxwell/internal/torrent"
	"maxwell/internal/vpn"
)

func Build(configPath string) (*Service, config.Config, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, cfg, err
	}

	cfg.Torrent.DownloadDir = config.ResolvePath(configPath, cfg.Torrent.DownloadDir)
	cfg.Paths.DownloadsDir = config.ResolvePath(configPath, cfg.Paths.DownloadsDir)
	cfg.Paths.ProcessedDir = config.ResolvePath(configPath, cfg.Paths.ProcessedDir)
	ss := cfg.EffectiveStateStore()
	if strings.EqualFold(ss.Driver, "sqlite") {
		ss.DSN = config.ResolvePath(configPath, ss.DSN)
	}
	cfg.StateStore = ss
	cfg.Database.Path = ss.DSN

	if err := os.MkdirAll(cfg.Torrent.DownloadDir, 0o755); err != nil {
		return nil, cfg, fmt.Errorf("create torrent.download_dir: %w", err)
	}
	if err := os.MkdirAll(cfg.Paths.ProcessedDir, 0o755); err != nil {
		return nil, cfg, fmt.Errorf("create paths.processed_dir: %w", err)
	}
	if strings.EqualFold(ss.Driver, "sqlite") {
		if err := os.MkdirAll(filepath.Dir(ss.DSN), 0o755); err != nil {
			return nil, cfg, fmt.Errorf("create database dir: %w", err)
		}
	}

	torrentClient, err := torrent.NewClient(cfg.Torrent)
	if err != nil {
		return nil, cfg, err
	}
	uploader, err := storage.NewUploader(cfg.Storage)
	if err != nil {
		return nil, cfg, err
	}
	store, err := queue.Open(cfg.StateStore)
	if err != nil {
		return nil, cfg, err
	}

	conv := convert.New(cfg.FFmpeg)
	gate := newDefaultGate(cfg.VPN)

	svc, err := NewService(Dependencies{
		Config:    cfg,
		Gate:      gate,
		Torrents:  torrentClient,
		Uploader:  uploader,
		Converter: conv,
		Store:     store,
	})
	if err != nil {
		_ = store.Close()
		return nil, cfg, err
	}
	return svc, cfg, nil
}

func newDefaultGate(vpnCfg config.VPNConfig) VPNGate {
	forced := strings.ToUpper(strings.TrimSpace(os.Getenv("MAXWELL_VPN_FORCE_STATE")))
	switch model.VPNState(forced) {
	case model.VPNStateSafe:
		return vpn.NewGate(vpn.StaticDetector{Signals: vpn.Signals{HasTunnelInterface: true, HasDefaultRoute: true}})
	case model.VPNStateUnsafe:
		return vpn.NewGate(vpn.StaticDetector{Signals: vpn.Signals{HasTunnelInterface: false, HasDefaultRoute: true, OnHomeNetwork: true, PublicIPLooksHome: true}})
	default:
		return vpn.NewGate(vpn.NewSystemDetector(vpnCfg))
	}
}
