package torrent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"maxwell/internal/config"
	"maxwell/internal/model"
)

type QBitClient struct {
	baseURL    string
	username   string
	password   string
	category   string
	httpClient *http.Client
	mu         sync.Mutex
	loggedIn   bool
}

func NewQBitClient(cfg config.TorrentConfig) (*QBitClient, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("qbittorrent base_url is required")
	}
	jar, _ := cookiejar.New(nil)
	return &QBitClient{
		baseURL:  base,
		username: cfg.Username,
		password: cfg.Password,
		category: cfg.Category,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
	}, nil
}

func (c *QBitClient) Name() string { return "qbittorrent" }

func (c *QBitClient) ensureLogin(ctx context.Context) error {
	c.mu.Lock()
	if c.loggedIn {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	form := url.Values{}
	form.Set("username", c.username)
	form.Set("password", c.password)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("qb login failed: %s", strings.TrimSpace(string(body)))
	}

	c.mu.Lock()
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

func (c *QBitClient) List(ctx context.Context) ([]model.Torrent, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v2/torrents/info", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qb list failed: status %d", resp.StatusCode)
	}

	var payload []struct {
		Hash        string  `json:"hash"`
		Name        string  `json:"name"`
		Progress    float64 `json:"progress"`
		DLSpeed     int64   `json:"dlspeed"`
		ETA         int64   `json:"eta"`
		State       string  `json:"state"`
		SavePath    string  `json:"save_path"`
		Completion  int64   `json:"completion_on"`
		AmountLeft  int64   `json:"amount_left"`
		Completed   int64   `json:"completed"`
		TotalSize   int64   `json:"total_size"`
		ContentPath string  `json:"content_path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}

	out := make([]model.Torrent, 0, len(payload))
	for _, t := range payload {
		completed := t.Progress >= 1 || t.State == "pausedUP" || t.AmountLeft == 0
		out = append(out, model.Torrent{
			ID:            t.Hash,
			Name:          t.Name,
			Progress:      t.Progress,
			DownloadSpeed: t.DLSpeed,
			ETASeconds:    t.ETA,
			State:         t.State,
			SavePath:      t.SavePath,
			Completed:     completed,
		})
	}
	return out, nil
}

func (c *QBitClient) AddMagnet(ctx context.Context, magnet string, downloadDir string) (string, error) {
	if err := c.ensureLogin(ctx); err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("urls", magnet)
	if downloadDir != "" {
		form.Set("savepath", downloadDir)
	}
	if strings.TrimSpace(c.category) != "" {
		form.Set("category", c.category)
	}
	form.Set("paused", "false")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/add", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qb add failed: status %d", resp.StatusCode)
	}
	return magnet, nil
}

func (c *QBitClient) PauseAll(ctx context.Context) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/pause", strings.NewReader("hashes=all"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qb pause failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *QBitClient) ResumeAll(ctx context.Context) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/torrents/resume", strings.NewReader("hashes=all"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qb resume failed: status %d", resp.StatusCode)
	}
	return nil
}

func (c *QBitClient) PauseHashes(ctx context.Context, hashes []string) error {
	return c.pauseResumeHashes(ctx, "/api/v2/torrents/pause", hashes)
}

func (c *QBitClient) ResumeHashes(ctx context.Context, hashes []string) error {
	return c.pauseResumeHashes(ctx, "/api/v2/torrents/resume", hashes)
}

func (c *QBitClient) pauseResumeHashes(ctx context.Context, endpoint string, hashes []string) error {
	if err := c.ensureLogin(ctx); err != nil {
		return err
	}
	if len(hashes) == 0 {
		return nil
	}
	form := url.Values{}
	form.Set("hashes", strings.Join(hashes, "|"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qb pause/resume hashes failed: status %d", resp.StatusCode)
	}
	return nil
}
