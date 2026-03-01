package integration_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"maxwell/internal/config"
	"maxwell/internal/torrent"
)

func TestQBitIntegration(t *testing.T) {
	var sawAdd, sawPause bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"hash": "h1", "name": "movie.mkv", "progress": 0.5, "dlspeed": 10, "eta": 12, "state": "downloading", "save_path": "/d",
			}})
		case "/api/v2/torrents/add":
			sawAdd = true
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), "category=maxwell") {
				t.Fatalf("expected category in request body, got: %s", string(body))
			}
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/pause":
			sawPause = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	client, err := torrent.NewClient(config.TorrentConfig{Provider: "qbittorrent", BaseURL: srv.URL, Category: "maxwell"})
	if err != nil {
		t.Fatal(err)
	}

	list, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "h1" {
		t.Fatalf("unexpected list: %+v", list)
	}

	if _, err := client.AddMagnet(context.Background(), "magnet:?xt=urn:btih:abc", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := client.PauseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawAdd || !sawPause {
		t.Fatalf("expected add and pause calls")
	}
}

func TestTransmissionIntegration(t *testing.T) {
	sessionID := "abc-session"
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Transmission-Session-Id") == "" {
			w.Header().Set("X-Transmission-Session-Id", sessionID)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var body struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		methods = append(methods, body.Method)
		switch body.Method {
		case "torrent-get":
			_ = json.NewEncoder(w).Encode(map[string]any{"arguments": map[string]any{"torrents": []map[string]any{{
				"hashString": "h2", "name": "show.mkv", "percentDone": 1.0, "rateDownload": 0, "eta": 0, "status": 6, "downloadDir": "/tmp",
			}}}})
		case "torrent-add":
			_ = json.NewEncoder(w).Encode(map[string]any{"arguments": map[string]any{"torrent-added": map[string]any{"hashString": "h3"}}})
		case "torrent-stop":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": "success"})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	client, err := torrent.NewClient(config.TorrentConfig{Provider: "transmission", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "h2" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if _, err := client.AddMagnet(context.Background(), "magnet:?xt=urn:btih:def", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := client.PauseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(methods, ",")
	for _, method := range []string{"torrent-get", "torrent-add", "torrent-stop"} {
		if !strings.Contains(joined, method) {
			t.Fatalf("expected method %s in %s", method, joined)
		}
	}
}

func TestUTorrentIntegration(t *testing.T) {
	var sawAdd, sawPause bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/gui/token.html":
			_, _ = w.Write([]byte("<html><div id='token'>tok123</div></html>"))
		case r.URL.Path == "/gui/" && r.URL.Query().Get("list") == "1":
			_ = json.NewEncoder(w).Encode(map[string]any{"torrents": [][]any{{
				"h3", 0, "ep1.mkv", 0, 1000, 0, 0, 0, 100, 0, 120, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, "/downloads",
			}}})
		case r.URL.Path == "/gui/" && r.URL.Query().Get("action") == "add-url":
			sawAdd = true
			_, _ = w.Write([]byte("{}"))
		case r.URL.Path == "/gui/" && r.URL.Query().Get("action") == "pause":
			sawPause = true
			_, _ = w.Write([]byte("{}"))
		default:
			t.Fatalf("unexpected request: %s", r.URL.String())
		}
	}))
	defer srv.Close()

	client, err := torrent.NewClient(config.TorrentConfig{Provider: "utorrent", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	list, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "h3" {
		t.Fatalf("unexpected list: %+v", list)
	}
	if _, err := client.AddMagnet(context.Background(), "magnet:?xt=urn:btih:ghi", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if err := client.PauseAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !sawAdd || !sawPause {
		t.Fatalf("expected add and pause calls")
	}
}
