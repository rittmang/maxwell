package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/vpn"
)

type fakeService struct {
	bus         *events.Bus
	addedMagnet string
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

func (f *fakeService) ListConversionJobs(context.Context) ([]model.ConversionJob, error) {
	return []model.ConversionJob{{ID: 1}}, nil
}

func (f *fakeService) ListUploadJobs(context.Context) ([]model.UploadJob, error) {
	return []model.UploadJob{{ID: 1}}, nil
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
