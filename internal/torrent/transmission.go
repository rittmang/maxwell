package torrent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"maxwell/internal/config"
	"maxwell/internal/model"
)

type TransmissionClient struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	mu         sync.Mutex
	sessionID  string
}

func NewTransmissionClient(cfg config.TorrentConfig) (*TransmissionClient, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("transmission base_url is required")
	}
	return &TransmissionClient{
		baseURL:  base,
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}, nil
}

func (c *TransmissionClient) Name() string { return "transmission" }

func (c *TransmissionClient) rpc(ctx context.Context, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	attempt := func(sessionID string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/transmission/rpc", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		if sessionID != "" {
			req.Header.Set("X-Transmission-Session-Id", sessionID)
		}
		if c.username != "" || c.password != "" {
			token := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
			req.Header.Set("Authorization", "Basic "+token)
		}
		return c.httpClient.Do(req)
	}

	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()

	resp, err := attempt(sid)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		sid = resp.Header.Get("X-Transmission-Session-Id")
		if sid == "" {
			return fmt.Errorf("transmission session handshake failed")
		}
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()

		resp2, err := attempt(sid)
		if err != nil {
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("transmission rpc failed: %s", strings.TrimSpace(string(b)))
		}
		if out != nil {
			return json.NewDecoder(resp2.Body).Decode(out)
		}
		return nil
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transmission rpc failed: %s", strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *TransmissionClient) List(ctx context.Context) ([]model.Torrent, error) {
	payload := map[string]any{
		"method": "torrent-get",
		"arguments": map[string]any{
			"fields": []string{"id", "hashString", "name", "percentDone", "rateDownload", "eta", "status", "downloadDir"},
		},
	}
	var resp struct {
		Arguments struct {
			Torrents []struct {
				ID           int     `json:"id"`
				Hash         string  `json:"hashString"`
				Name         string  `json:"name"`
				PercentDone  float64 `json:"percentDone"`
				RateDownload int64   `json:"rateDownload"`
				ETA          int64   `json:"eta"`
				Status       int     `json:"status"`
				DownloadDir  string  `json:"downloadDir"`
			} `json:"torrents"`
		} `json:"arguments"`
	}
	if err := c.rpc(ctx, payload, &resp); err != nil {
		return nil, err
	}

	out := make([]model.Torrent, 0, len(resp.Arguments.Torrents))
	for _, t := range resp.Arguments.Torrents {
		completed := t.PercentDone >= 1 || t.Status == 6
		out = append(out, model.Torrent{
			ID:            t.Hash,
			Name:          t.Name,
			Progress:      t.PercentDone,
			DownloadSpeed: t.RateDownload,
			ETASeconds:    t.ETA,
			State:         fmt.Sprintf("%d", t.Status),
			SavePath:      t.DownloadDir,
			Completed:     completed,
		})
	}
	return out, nil
}

func (c *TransmissionClient) AddMagnet(ctx context.Context, magnet string, downloadDir string) (string, error) {
	payload := map[string]any{
		"method": "torrent-add",
		"arguments": map[string]any{
			"filename":     magnet,
			"download-dir": downloadDir,
		},
	}
	var resp struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := c.rpc(ctx, payload, &resp); err != nil {
		return "", err
	}
	if _, ok := resp.Arguments["torrent-added"]; !ok {
		if _, ok2 := resp.Arguments["torrent-duplicate"]; !ok2 {
			return "", fmt.Errorf("transmission did not report torrent-added")
		}
	}
	return magnet, nil
}

func (c *TransmissionClient) PauseAll(ctx context.Context) error {
	payload := map[string]any{
		"method": "torrent-stop",
		"arguments": map[string]any{
			"ids": "recently-active",
		},
	}
	return c.rpc(ctx, payload, nil)
}

func (c *TransmissionClient) ResumeAll(ctx context.Context) error {
	payload := map[string]any{
		"method": "torrent-start",
		"arguments": map[string]any{
			"ids": "recently-active",
		},
	}
	return c.rpc(ctx, payload, nil)
}

func (c *TransmissionClient) PauseHashes(ctx context.Context, hashes []string) error {
	payload := map[string]any{
		"method": "torrent-stop",
		"arguments": map[string]any{
			"ids": hashes,
		},
	}
	return c.rpc(ctx, payload, nil)
}

func (c *TransmissionClient) ResumeHashes(ctx context.Context, hashes []string) error {
	payload := map[string]any{
		"method": "torrent-start",
		"arguments": map[string]any{
			"ids": hashes,
		},
	}
	return c.rpc(ctx, payload, nil)
}
