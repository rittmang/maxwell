package storage

import (
	"bytes"
	"context"
	"encoding/json"
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

type OneDriveUploader struct {
	endpoint    string
	accessToken string
	httpClient  *http.Client
}

func NewOneDriveUploader(cfg config.StorageConfig) (*OneDriveUploader, error) {
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("storage.access_token is required for onedrive")
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = "https://graph.microsoft.com/v1.0/me"
	}
	return &OneDriveUploader{
		endpoint:    endpoint,
		accessToken: cfg.AccessToken,
		httpClient:  &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (u *OneDriveUploader) Name() string { return "onedrive" }

func (u *OneDriveUploader) Upload(ctx context.Context, localPath, objectKey string) (string, error) {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return "", err
	}
	escaped := strings.TrimPrefix(path.Clean("/"+objectKey), "/")
	target := fmt.Sprintf("%s/drive/root:/%s:/content", u.endpoint, url.PathEscape(escaped))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+u.accessToken)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := u.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("onedrive upload failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		WebURL string `json:"webUrl"`
		ID     string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.WebURL != "" {
		return parsed.WebURL, nil
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("onedrive upload missing id")
	}
	return fmt.Sprintf("https://onedrive.live.com/?id=%s", parsed.ID), nil
}
