package storage

import (
	"context"
	"fmt"
	"strings"

	"maxwell/internal/config"
)

type Uploader interface {
	Name() string
	Upload(ctx context.Context, localPath, objectKey string) (string, error)
}

func NewUploader(cfg config.StorageConfig) (Uploader, error) {
	switch normalize(cfg.Provider) {
	case "backblaze_b2", "b2", "backblaze":
		return NewS3CompatibleUploader("backblaze_b2", cfg)
	case "aws_s3", "s3", "aws":
		return NewS3CompatibleUploader("aws_s3", cfg)
	case "google_drive", "gdrive", "drive":
		return NewGoogleDriveUploader(cfg)
	case "onedrive", "one_drive":
		return NewOneDriveUploader(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage provider: %s", cfg.Provider)
	}
}

func normalize(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}
