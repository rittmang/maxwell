package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"maxwell/internal/config"
	"maxwell/internal/storage"
)

func TestS3CompatibleProviders(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "video.mp4")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, provider := range []string{"backblaze_b2", "aws_s3"} {
		t.Run(provider, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			uploader, err := storage.NewUploader(config.StorageConfig{
				Provider:      provider,
				Endpoint:      srv.URL,
				Bucket:        "bucket",
				PublicBaseURL: "https://cdn.example.com",
				KeyID:         "kid",
				AppKey:        "key",
			})
			if err != nil {
				t.Fatal(err)
			}

			url, err := uploader.Upload(context.Background(), file, "2026/02/28/video.mp4")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(gotPath, "/bucket/") {
				t.Fatalf("unexpected path: %s", gotPath)
			}
			if !strings.HasPrefix(url, "https://cdn.example.com/") {
				t.Fatalf("unexpected url: %s", url)
			}
		})
	}
}

func TestGoogleDriveProvider(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "video.mp4")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Bearer token") {
			t.Fatalf("missing token")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "drive-id", "webViewLink": "https://drive.google.com/file/d/drive-id/view"})
	}))
	defer srv.Close()

	uploader, err := storage.NewUploader(config.StorageConfig{
		Provider:    "google_drive",
		Endpoint:    srv.URL,
		AccessToken: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	url, err := uploader.Upload(context.Background(), file, "video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "drive.google.com") {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestOneDriveProvider(t *testing.T) {
	tmp := t.TempDir()
	file := filepath.Join(tmp, "video.mp4")
	if err := os.WriteFile(file, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Bearer token") {
			t.Fatalf("missing token")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "one-id", "webUrl": "https://onedrive.live.com/?id=one-id"})
	}))
	defer srv.Close()

	uploader, err := storage.NewUploader(config.StorageConfig{
		Provider:    "onedrive",
		Endpoint:    srv.URL,
		AccessToken: "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	url, err := uploader.Upload(context.Background(), file, "folder/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(url, "onedrive") {
		t.Fatalf("unexpected url: %s", url)
	}
}
