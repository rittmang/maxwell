package torrent

import (
	"context"
	"fmt"
	"strings"

	"maxwell/internal/config"
	"maxwell/internal/model"
)

type Client interface {
	Name() string
	List(context.Context) ([]model.Torrent, error)
	AddMagnet(ctx context.Context, magnet string, downloadDir string) (string, error)
	PauseAll(context.Context) error
	ResumeAll(context.Context) error
	PauseHashes(context.Context, []string) error
	ResumeHashes(context.Context, []string) error
}

func NewClient(cfg config.TorrentConfig) (Client, error) {
	switch normalize(cfg.Provider) {
	case "qbittorrent", "qb", "qbit":
		return NewQBitClient(cfg)
	case "transmission":
		return NewTransmissionClient(cfg)
	case "utorrent", "u-torrent":
		return NewUTorrentClient(cfg)
	default:
		return nil, fmt.Errorf("unsupported torrent provider: %s", cfg.Provider)
	}
}

func normalize(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
