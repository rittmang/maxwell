package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/vpn"
)

type fakeService struct {
	bus                *events.Bus
	addedMagnet        string
	syncCalls          int
	processCalls       int
	pausedTorrentHash  string
	resumedTorrentHash string
	pausedConvID       int64
	resumedConvID      int64
	pausedUploadID     int64
	resumedUploadID    int64
}

func (f *fakeService) VPNStatus(context.Context) (model.VPNState, vpn.Signals, error) {
	return model.VPNStateSafe, vpn.Signals{HasTunnelInterface: true}, nil
}

func (f *fakeService) Stats(context.Context) (map[string]int64, error) {
	return map[string]int64{"downloads": 2, "conversion": 1, "upload": 1, "links": 1}, nil
}

func (f *fakeService) ListTorrents(context.Context) ([]model.Torrent, error) {
	return []model.Torrent{{ID: "h1", Name: "movie.mkv", Progress: 0.9}}, nil
}

func (f *fakeService) AddMagnet(_ context.Context, magnet string) (string, error) {
	f.addedMagnet = magnet
	return "added-id", nil
}

func (f *fakeService) PauseTorrent(_ context.Context, hash string) error {
	f.pausedTorrentHash = hash
	return nil
}

func (f *fakeService) ResumeTorrent(_ context.Context, hash string) error {
	f.resumedTorrentHash = hash
	return nil
}

func (f *fakeService) SyncCompletedDownloads(context.Context) error {
	f.syncCalls++
	return nil
}

func (f *fakeService) ProcessOnce(context.Context) error {
	f.processCalls++
	return nil
}

func (f *fakeService) ListConversionJobs(context.Context) ([]model.ConversionJob, error) {
	return []model.ConversionJob{{ID: 1}}, nil
}

func (f *fakeService) ListUploadJobs(context.Context) ([]model.UploadJob, error) {
	return []model.UploadJob{{ID: 1}}, nil
}

func (f *fakeService) PauseConversionJob(_ context.Context, id int64) error {
	f.pausedConvID = id
	return nil
}

func (f *fakeService) ResumeConversionJob(_ context.Context, id int64) error {
	f.resumedConvID = id
	return nil
}

func (f *fakeService) PauseUploadJob(_ context.Context, id int64) error {
	f.pausedUploadID = id
	return nil
}

func (f *fakeService) ResumeUploadJob(_ context.Context, id int64) error {
	f.resumedUploadID = id
	return nil
}

func (f *fakeService) ListLinks(context.Context, int) ([]model.LinkRecord, error) {
	return []model.LinkRecord{{ID: 1, FinalURL: "https://example.com/a.mp4"}}, nil
}

func (f *fakeService) ListEvents(context.Context, int) ([]model.Event, error) {
	return []model.Event{{ID: 1, Type: "upload_done"}}, nil
}

func (f *fakeService) EventBus() *events.Bus {
	if f.bus == nil {
		f.bus = events.NewBus()
	}
	return f.bus
}

func TestOverviewAPI(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "", false)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/overview")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["vpn"] != string(model.VPNStateSafe) {
		t.Fatalf("unexpected vpn state: %v", body["vpn"])
	}
}

func TestIndexContainsParityActions(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "token", true)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, mustContain := range []string{"add-magnet-form", "run-once-btn", "/api/torrents/add", "/api/run/once", "/api/torrents/action", "/api/conversion/action", "/api/upload/action", "row-context-menu", "pipeline-board", "lane-torrents", "lane-conversion", "lane-upload", "lane-links", "item-card", "function itemCard(", "function renderLane("} {
		if !strings.Contains(text, mustContain) {
			t.Fatalf("index missing %q", mustContain)
		}
	}
}

func TestAddTorrentRequiresToken(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "secret", true)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	form := url.Values{"magnet": []string{"magnet:?xt=urn:btih:abc"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/torrents/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	reqForbidden, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/torrents/add", strings.NewReader(form.Encode()))
	reqForbidden.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqForbidden.Header.Set("X-API-Token", "secret")
	respForbidden, err := http.DefaultClient.Do(reqForbidden)
	if err != nil {
		t.Fatal(err)
	}
	defer respForbidden.Body.Close()
	if respForbidden.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for csrf, got %d", respForbidden.StatusCode)
	}

	req2, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/torrents/add", strings.NewReader(form.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req2.Header.Set("X-API-Token", "secret")
	req2.AddCookie(&http.Cookie{Name: "maxwell_csrf", Value: "csrf-token"})
	req2.Header.Set("X-CSRF-Token", "csrf-token")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	if svc.addedMagnet == "" {
		t.Fatalf("expected magnet to be added")
	}
}

func TestAddTorrentWithRenderedCSRFToken(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "secret", true)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	getResp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	body, err := io.ReadAll(getResp.Body)
	if err != nil {
		t.Fatal(err)
	}

	var csrfCookie *http.Cookie
	for _, c := range getResp.Cookies() {
		if c.Name == "maxwell_csrf" {
			csrfCookie = c
			break
		}
	}
	if csrfCookie == nil || strings.TrimSpace(csrfCookie.Value) == "" {
		t.Fatalf("expected maxwell_csrf cookie on index response")
	}

	apiToken := extractJSConstString(t, string(body), "apiToken")
	if apiToken != "secret" {
		t.Fatalf("expected apiToken to be rendered, got %q", apiToken)
	}
	csrfToken := extractJSConstString(t, string(body), "initialCSRFToken")
	if csrfToken != csrfCookie.Value {
		t.Fatalf("rendered csrf token mismatch: token=%q cookie=%q", csrfToken, csrfCookie.Value)
	}

	form := url.Values{"magnet": []string{"magnet:?xt=urn:btih:abc"}}
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/torrents/add", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-API-Token", apiToken)
	req.Header.Set("X-CSRF-Token", csrfToken)
	req.AddCookie(csrfCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d body=%s", resp.StatusCode, string(b))
	}
}

func TestSSEStreamReady(t *testing.T) {
	svc := &fakeService{bus: events.NewBus()}
	server := NewServer(svc, "", false)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	evt, _, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if evt != "ready" {
		t.Fatalf("unexpected first event: %q", evt)
	}
}

func TestSSEOverviewEvent(t *testing.T) {
	svc := &fakeService{bus: events.NewBus()}
	server := NewServer(svc, "", false)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(ts.URL + "/api/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	evt, _, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if evt != "ready" {
		t.Fatalf("expected ready event, got %q", evt)
	}
	evt, data, err := readSSEEvent(reader)
	if err != nil {
		t.Fatal(err)
	}
	if evt != "overview" {
		t.Fatalf("expected overview event, got %q", evt)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		t.Fatalf("invalid overview payload: %v", err)
	}
	if payload["vpn"] != string(model.VPNStateSafe) {
		t.Fatalf("unexpected vpn in overview: %v", payload["vpn"])
	}
}

func TestMetricsEndpoint(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "", false)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "maxwell_downloads") {
		t.Fatalf("unexpected metrics body: %s", string(body))
	}
}

func TestRunOnceEndpoint(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "secret", true)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/run/once", strings.NewReader("csrf_token=csrf-token"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-API-Token", "secret")
	req.Header.Set("X-CSRF-Token", "csrf-token")
	req.AddCookie(&http.Cookie{Name: "maxwell_csrf", Value: "csrf-token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if svc.syncCalls != 1 || svc.processCalls != 1 {
		t.Fatalf("expected one sync and process call, got sync=%d process=%d", svc.syncCalls, svc.processCalls)
	}
}

func TestRowActionEndpoints(t *testing.T) {
	svc := &fakeService{}
	server := NewServer(svc, "secret", true)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	post := func(path string, vals url.Values) {
		t.Helper()
		cloned := url.Values{}
		for k, v := range vals {
			cloned[k] = append([]string(nil), v...)
		}
		vals = cloned
		vals.Set("csrf_token", "csrf-token")
		req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(vals.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-API-Token", "secret")
		req.Header.Set("X-CSRF-Token", "csrf-token")
		req.AddCookie(&http.Cookie{Name: "maxwell_csrf", Value: "csrf-token"})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post %s: %v", path, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("post %s expected 200, got %d body=%s", path, resp.StatusCode, string(b))
		}
	}

	post("/api/torrents/action", url.Values{"hash": []string{"hash-1"}, "action": []string{"pause"}})
	post("/api/torrents/action", url.Values{"hash": []string{"hash-1"}, "action": []string{"resume"}})
	post("/api/conversion/action", url.Values{"id": []string{"7"}, "action": []string{"pause"}})
	post("/api/conversion/action", url.Values{"id": []string{"7"}, "action": []string{"resume"}})
	post("/api/upload/action", url.Values{"id": []string{"9"}, "action": []string{"pause"}})
	post("/api/upload/action", url.Values{"id": []string{"9"}, "action": []string{"resume"}})

	if svc.pausedTorrentHash != "hash-1" || svc.resumedTorrentHash != "hash-1" {
		t.Fatalf("torrent action calls not captured: pause=%q resume=%q", svc.pausedTorrentHash, svc.resumedTorrentHash)
	}
	if svc.pausedConvID != 7 || svc.resumedConvID != 7 {
		t.Fatalf("conversion action calls not captured: pause=%d resume=%d", svc.pausedConvID, svc.resumedConvID)
	}
	if svc.pausedUploadID != 9 || svc.resumedUploadID != 9 {
		t.Fatalf("upload action calls not captured: pause=%d resume=%d", svc.pausedUploadID, svc.resumedUploadID)
	}
}

func readSSEEvent(r *bufio.Reader) (string, string, error) {
	event := ""
	data := ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return "", "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if event != "" {
				return event, data, nil
			}
			continue
		}
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimSpace(strings.TrimPrefix(line, "data: "))
			continue
		}
	}
}

func extractJSConstString(t *testing.T, html, name string) string {
	t.Helper()
	re := regexp.MustCompile(`const\s+` + regexp.QuoteMeta(name) + `\s*=\s*("(?:[^"\\]|\\.)*");`)
	match := re.FindStringSubmatch(html)
	if len(match) < 2 {
		t.Fatalf("missing JS const %q", name)
	}
	value, err := strconv.Unquote(match[1])
	if err != nil {
		t.Fatalf("invalid quoted const %q: %v", name, err)
	}
	return value
}
