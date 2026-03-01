package torrent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"maxwell/internal/config"
	"maxwell/internal/model"
)

var tokenPattern = regexp.MustCompile(`(?i)<div[^>]*id=['\"]token['\"][^>]*>([^<]+)</div>`)

type UTorrentClient struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	mu         sync.Mutex
	token      string
}

func NewUTorrentClient(cfg config.TorrentConfig) (*UTorrentClient, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		return nil, fmt.Errorf("utorrent base_url is required")
	}
	jar, _ := cookiejar.New(nil)
	return &UTorrentClient{
		baseURL:  base,
		username: cfg.Username,
		password: cfg.Password,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
			Jar:     jar,
		},
	}, nil
}

func (c *UTorrentClient) Name() string { return "utorrent" }

func (c *UTorrentClient) authHeader() string {
	if c.username == "" && c.password == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.username+":"+c.password))
}

func (c *UTorrentClient) ensureToken(ctx context.Context) error {
	c.mu.Lock()
	if c.token != "" {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/gui/token.html", nil)
	if err != nil {
		return err
	}
	if auth := c.authHeader(); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("utorrent token fetch failed: %s", strings.TrimSpace(string(b)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	m := tokenPattern.FindSubmatch(body)
	if len(m) < 2 {
		return fmt.Errorf("utorrent token not found")
	}
	c.mu.Lock()
	c.token = string(m[1])
	c.mu.Unlock()
	return nil
}

func (c *UTorrentClient) call(ctx context.Context, query url.Values, out any) error {
	if err := c.ensureToken(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	query.Set("token", token)

	u := c.baseURL + "/gui/?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if auth := c.authHeader(); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("utorrent api failed: %s", strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *UTorrentClient) List(ctx context.Context) ([]model.Torrent, error) {
	query := url.Values{}
	query.Set("list", "1")
	var resp struct {
		Torrents [][]any `json:"torrents"`
	}
	if err := c.call(ctx, query, &resp); err != nil {
		return nil, err
	}
	out := make([]model.Torrent, 0, len(resp.Torrents))
	for _, row := range resp.Torrents {
		if len(row) < 27 {
			continue
		}
		hash, _ := row[0].(string)
		name, _ := row[2].(string)
		progressRaw := toFloat(row[4])
		ratio := progressRaw / 1000.0
		state := fmt.Sprintf("%v", row[1])
		eta := int64(toFloat(row[10]))
		dlSpeed := int64(toFloat(row[8]))
		savePath, _ := row[26].(string)
		completed := ratio >= 1
		out = append(out, model.Torrent{
			ID:            hash,
			Name:          name,
			Progress:      ratio,
			DownloadSpeed: dlSpeed,
			ETASeconds:    eta,
			State:         state,
			SavePath:      savePath,
			Completed:     completed,
		})
	}
	return out, nil
}

func (c *UTorrentClient) AddMagnet(ctx context.Context, magnet string, _ string) (string, error) {
	query := url.Values{}
	query.Set("action", "add-url")
	query.Set("s", magnet)
	if err := c.call(ctx, query, nil); err != nil {
		return "", err
	}
	return magnet, nil
}

func (c *UTorrentClient) PauseAll(ctx context.Context) error {
	query := url.Values{}
	query.Set("action", "pause")
	query.Set("hash", "all")
	return c.call(ctx, query, nil)
}

func (c *UTorrentClient) ResumeAll(ctx context.Context) error {
	query := url.Values{}
	query.Set("action", "start")
	query.Set("hash", "all")
	return c.call(ctx, query, nil)
}

func (c *UTorrentClient) PauseHashes(ctx context.Context, hashes []string) error {
	for _, h := range hashes {
		query := url.Values{}
		query.Set("action", "pause")
		query.Set("hash", h)
		if err := c.call(ctx, query, nil); err != nil {
			return err
		}
	}
	return nil
}

func (c *UTorrentClient) ResumeHashes(ctx context.Context, hashes []string) error {
	for _, h := range hashes {
		query := url.Values{}
		query.Set("action", "start")
		query.Set("hash", h)
		if err := c.call(ctx, query, nil); err != nil {
			return err
		}
	}
	return nil
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	default:
		return 0
	}
}
