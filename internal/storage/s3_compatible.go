package storage

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"maxwell/internal/config"
)

type S3CompatibleUploader struct {
	provider      string
	endpoint      string
	bucket        string
	keyID         string
	appKey        string
	publicBaseURL string
	httpClient    *http.Client
}

func NewS3CompatibleUploader(provider string, cfg config.StorageConfig) (*S3CompatibleUploader, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		return nil, fmt.Errorf("storage.endpoint is required for %s", provider)
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("storage.bucket is required for %s", provider)
	}
	return &S3CompatibleUploader{
		provider:      provider,
		endpoint:      endpoint,
		bucket:        cfg.Bucket,
		keyID:         cfg.KeyID,
		appKey:        cfg.AppKey,
		publicBaseURL: strings.TrimRight(cfg.PublicBaseURL, "/"),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}, nil
}

func (u *S3CompatibleUploader) Name() string {
	return u.provider
}

func (u *S3CompatibleUploader) Upload(ctx context.Context, localPath, objectKey string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	escapedKey := path.Clean(strings.TrimPrefix(objectKey, "/"))
	target := fmt.Sprintf("%s/%s/%s", u.endpoint, u.bucket, escapedKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, f)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if u.keyID != "" {
		req.Header.Set("X-MAXWELL-KEY-ID", u.keyID)
	}
	if u.appKey != "" {
		req.Header.Set("X-MAXWELL-APP-KEY", u.appKey)
	}

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("%s upload failed: status=%d body=%s", u.provider, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	if u.publicBaseURL != "" {
		return u.publicBaseURL + "/" + url.PathEscape(escapedKey), nil
	}
	return target, nil
}
