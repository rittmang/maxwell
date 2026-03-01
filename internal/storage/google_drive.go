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
	"path/filepath"
	"strings"
	"time"

	"maxwell/internal/config"
)

type GoogleDriveUploader struct {
	endpoint    string
	accessToken string
	httpClient  *http.Client
}

func NewGoogleDriveUploader(cfg config.StorageConfig) (*GoogleDriveUploader, error) {
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("storage.access_token is required for google_drive")
	}
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	if endpoint == "" {
		endpoint = "https://www.googleapis.com/upload/drive/v3/files"
	}
	return &GoogleDriveUploader{
		endpoint:    endpoint,
		accessToken: cfg.AccessToken,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}, nil
}

func (u *GoogleDriveUploader) Name() string { return "google_drive" }

func (u *GoogleDriveUploader) Upload(ctx context.Context, localPath, objectKey string) (string, error) {
	content, err := os.ReadFile(localPath)
	if err != nil {
		return "", err
	}

	name := filepath.Base(objectKey)
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = filepath.Base(localPath)
	}

	q := url.Values{}
	q.Set("uploadType", "media")
	q.Set("name", name)
	target := u.endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(content))
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
		return "", fmt.Errorf("google_drive upload failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed struct {
		ID          string `json:"id"`
		WebViewLink string `json:"webViewLink"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if parsed.WebViewLink != "" {
		return parsed.WebViewLink, nil
	}
	if parsed.ID == "" {
		return "", fmt.Errorf("google_drive upload missing id")
	}
	return "https://drive.google.com/file/d/" + parsed.ID + "/view", nil
}
