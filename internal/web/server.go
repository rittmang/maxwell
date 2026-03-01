package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"maxwell/internal/events"
	"maxwell/internal/model"
	"maxwell/internal/vpn"
)

const (
	streamOverviewInterval = 3 * time.Second
	streamKeepalivePeriod  = 25 * time.Second
)

type Service interface {
	VPNStatus(context.Context) (model.VPNState, vpn.Signals, error)
	Stats(context.Context) (map[string]int64, error)
	ListTorrents(context.Context) ([]model.Torrent, error)
	AddMagnet(context.Context, string) (string, error)
	PauseTorrent(context.Context, string) error
	ResumeTorrent(context.Context, string) error
	OpenTorrentFolder(context.Context, string) error
	SyncCompletedDownloads(context.Context) error
	ProcessOnce(context.Context) error
	ListConversionJobs(context.Context) ([]model.ConversionJob, error)
	ListUploadJobs(context.Context) ([]model.UploadJob, error)
	PauseConversionJob(context.Context, int64) error
	ResumeConversionJob(context.Context, int64) error
	PauseUploadJob(context.Context, int64) error
	ResumeUploadJob(context.Context, int64) error
	ListLinks(context.Context, int) ([]model.LinkRecord, error)
	ListEvents(context.Context, int) ([]model.Event, error)
	EventBus() *events.Bus
}

type Server struct {
	svc         Service
	token       string
	csrfEnabled bool
	tmpl        *template.Template
	mux         *http.ServeMux
}

func NewServer(svc Service, token string, csrfEnabled bool) *Server {
	tmpl := template.Must(template.New("index").Parse(indexHTML))
	s := &Server{
		svc:         svc,
		token:       token,
		csrfEnabled: csrfEnabled,
		tmpl:        tmpl,
		mux:         http.NewServeMux(),
	}
	s.routes()
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/api/overview", s.handleOverview)
	s.mux.HandleFunc("/api/torrents", s.handleTorrents)
	s.mux.HandleFunc("/api/torrents/add", s.handleAddTorrent)
	s.mux.HandleFunc("/api/torrents/action", s.handleTorrentAction)
	s.mux.HandleFunc("/api/torrents/open-folder", s.handleTorrentOpenFolder)
	s.mux.HandleFunc("/api/run/once", s.handleRunOnce)
	s.mux.HandleFunc("/api/queue", s.handleQueue)
	s.mux.HandleFunc("/api/conversion/action", s.handleConversionAction)
	s.mux.HandleFunc("/api/upload/action", s.handleUploadAction)
	s.mux.HandleFunc("/api/links", s.handleLinks)
	s.mux.HandleFunc("/api/events", s.handleEvents)
	s.mux.HandleFunc("/api/stream", s.handleStream)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	vpnState, _, _ := s.svc.VPNStatus(ctx)
	stats, _ := s.svc.Stats(ctx)
	csrfToken := ""
	if s.csrfEnabled {
		csrfToken = s.ensureCSRFCookie(w, r)
	}
	_ = s.tmpl.Execute(w, map[string]any{
		"VPN":       vpnState,
		"Stats":     stats,
		"APIToken":  s.token,
		"CSRFToken": csrfToken,
	})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	overview, err := s.currentOverview(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, overview)
}

func (s *Server) handleTorrents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	list, err := s.svc.ListTorrents(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sortTorrentsForLane(list)
	writeJSON(w, list)
}

func sortTorrentsForLane(list []model.Torrent) {
	sort.SliceStable(list, func(i, j int) bool {
		ri := torrentLaneRank(list[i])
		rj := torrentLaneRank(list[j])
		if ri != rj {
			return ri < rj
		}
		ni := strings.ToLower(strings.TrimSpace(list[i].Name))
		nj := strings.ToLower(strings.TrimSpace(list[j].Name))
		if ni != nj {
			return ni < nj
		}
		return list[i].ID < list[j].ID
	})
}

func torrentLaneRank(t model.Torrent) int {
	state := strings.ToLower(strings.TrimSpace(t.State))
	if strings.Contains(state, "missing") {
		return 3
	}
	if strings.Contains(state, "pause") || strings.Contains(state, "stopped") {
		return 1
	}
	if strings.Contains(state, "complete") || t.Progress >= 0.999 {
		return 2
	}
	return 0
}

func (s *Server) handleAddTorrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	magnet := strings.TrimSpace(r.Form.Get("magnet"))
	if magnet == "" {
		http.Error(w, "magnet is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id, err := s.svc.AddMagnet(ctx, magnet)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"id": id})
}

func (s *Server) handleTorrentAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	hash := strings.TrimSpace(r.Form.Get("hash"))
	action := strings.ToLower(strings.TrimSpace(r.Form.Get("action")))
	if hash == "" {
		http.Error(w, "hash is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var err error
	switch action {
	case "pause":
		err = s.svc.PauseTorrent(ctx, hash)
	case "resume":
		err = s.svc.ResumeTorrent(ctx, hash)
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleTorrentOpenFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	hash := strings.TrimSpace(r.Form.Get("hash"))
	if hash == "" {
		http.Error(w, "hash is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.svc.OpenTorrentFolder(ctx, hash); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleConversionAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id, err := parseIDFormField(r.Form.Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.Form.Get("action")))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch action {
	case "pause":
		err = s.svc.PauseConversionJob(ctx, id)
	case "resume":
		err = s.svc.ResumeConversionJob(ctx, id)
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleUploadAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id, err := parseIDFormField(r.Form.Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	action := strings.ToLower(strings.TrimSpace(r.Form.Get("action")))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	switch action {
	case "pause":
		err = s.svc.PauseUploadJob(ctx, id)
	case "resume":
		err = s.svc.ResumeUploadJob(ctx, id)
	default:
		http.Error(w, "invalid action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleRunOnce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authorizeMutation(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.svc.SyncCompletedDownloads(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.svc.ProcessOnce(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func parseIDFormField(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("id is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	conv, err := s.svc.ListConversionJobs(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	upl, err := s.svc.ListUploadJobs(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"conversion": conv, "upload": upl})
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	links, err := s.svc.ListLinks(ctx, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, links)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	events, err := s.svc.ListEvents(ctx, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, events)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}

	sub := s.svc.EventBus().Subscribe(32)
	defer s.svc.EventBus().Unsubscribe(sub)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	if err := s.sendSSE(w, flusher, "ready", map[string]any{"ok": true}); err != nil {
		return
	}
	if err := s.sendOverviewEvent(r.Context(), w, flusher); err != nil {
		return
	}

	overviewTicker := time.NewTicker(streamOverviewInterval)
	defer overviewTicker.Stop()
	keepaliveTicker := time.NewTicker(streamKeepalivePeriod)
	defer keepaliveTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-sub:
			if err := s.sendSSE(w, flusher, msg.Type, msg.Body); err != nil {
				return
			}
		case <-overviewTicker.C:
			if err := s.sendOverviewEvent(r.Context(), w, flusher); err != nil {
				return
			}
		case <-keepaliveTicker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	stats, err := s.svc.Stats(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for k, v := range stats {
		fmt.Fprintf(w, "maxwell_%s %d\n", sanitizeMetricName(k), v)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) sendSSE(w http.ResponseWriter, flusher http.Flusher, event string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func (s *Server) sendOverviewEvent(ctx context.Context, w http.ResponseWriter, flusher http.Flusher) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	overview, err := s.currentOverview(cctx)
	if err != nil {
		return s.sendSSE(w, flusher, "overview_error", map[string]any{"error": err.Error()})
	}
	return s.sendSSE(w, flusher, "overview", overview)
}

func (s *Server) currentOverview(ctx context.Context) (map[string]any, error) {
	state, signals, err := s.svc.VPNStatus(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.svc.Stats(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"vpn":       state,
		"signals":   signals,
		"stats":     stats,
		"updatedAt": time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *Server) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("maxwell_csrf"); err == nil && strings.TrimSpace(c.Value) != "" {
		return c.Value
	}
	token := randomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name:     "maxwell_csrf",
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	return token
}

func (s *Server) validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("maxwell_csrf")
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
	if header == "" {
		_ = r.ParseForm()
		header = strings.TrimSpace(r.Form.Get("csrf_token"))
	}
	return header != "" && header == cookie.Value
}

func (s *Server) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	if s.token != "" && r.Header.Get("X-API-Token") != s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if s.csrfEnabled && !s.validCSRF(r) {
		http.Error(w, "csrf check failed", http.StatusForbidden)
		return false
	}
	return true
}

func randomHex(n int) string {
	if n <= 0 {
		n = 16
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func sanitizeMetricName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	return strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(s)
}

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>Maxwell</title>
  <style>
    :root {
      --bg: #f6f8f3;
      --card: #ffffff;
      --ink: #1a2b1f;
      --accent: #1f7a4d;
      --warn: #b03a2e;
      --muted: #4a5a4e;
      --border: #d8e4dc;
    }
    body {
      margin: 0;
      font-family: "Avenir Next", "Segoe UI", sans-serif;
      color: var(--ink);
      background: radial-gradient(circle at 20% 0%, #e7f3ea, var(--bg));
      min-height: 100vh;
    }
    .container {
      max-width: 1180px;
      margin: 24px auto;
      padding: 0 16px 24px;
    }
    .hero, .panel {
      background: var(--card);
      border-radius: 14px;
      padding: 16px;
      box-shadow: 0 8px 20px rgba(26,43,31,.08);
      animation: fade .35s ease;
    }
    .hero h1 { margin: 0; letter-spacing: .06em; text-transform: uppercase; }
    .grid {
      margin-top: 14px;
      display: grid;
      grid-template-columns: repeat(auto-fit,minmax(190px,1fr));
      gap: 10px;
    }
    .card {
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 10px;
    }
    .label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
    .value { font-size: 22px; margin-top: 8px; }
    #vpn-value[data-state="safe"], #vpn-value[data-state="SAFE"] { color: var(--accent); }
    #vpn-value[data-state="unsafe"], #vpn-value[data-state="UNSAFE"], #vpn-value[data-state="unknown"], #vpn-value[data-state="UNKNOWN"] { color: var(--warn); }
    .meta { margin-top: 10px; font-size: 12px; color: var(--muted); }
    .actions {
      margin-top: 14px;
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
      align-items: center;
    }
    button {
      background: #1f7a4d;
      color: #fff;
      border: 0;
      padding: 8px 14px;
      border-radius: 8px;
      font-weight: 600;
      cursor: pointer;
    }
    button.secondary { background: #29493a; }
    button:disabled { opacity: .7; cursor: not-allowed; }
    .status {
      font-size: 13px;
      color: var(--muted);
    }
    .panels {
      margin-top: 14px;
      display: grid;
      gap: 10px;
    }
    .panel h3 {
      margin: 0 0 10px;
      font-size: 16px;
      letter-spacing: .01em;
    }
    form {
      display: grid;
      gap: 8px;
    }
    textarea, input {
      width: 100%;
      border: 1px solid var(--border);
      border-radius: 8px;
      padding: 9px;
      box-sizing: border-box;
      font-family: inherit;
      font-size: 14px;
    }
    .board {
      overflow-x: auto;
      padding-bottom: 6px;
    }
    .board-track {
      display: grid;
      grid-template-columns: repeat(4, minmax(260px, 1fr));
      gap: 12px;
      min-width: 1080px;
    }
    .lane {
      border: 1px solid var(--border);
      border-radius: 12px;
      padding: 10px;
      background: linear-gradient(180deg, #f9fcfa 0%, #f4f8f5 100%);
      min-height: 320px;
      display: grid;
      grid-template-rows: auto 1fr;
      gap: 10px;
    }
    .lane-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 10px;
      font-size: 13px;
      font-weight: 700;
      letter-spacing: .02em;
      text-transform: uppercase;
      color: #2b4135;
    }
    .lane-count {
      min-width: 24px;
      text-align: center;
      border-radius: 999px;
      padding: 3px 8px;
      background: #e5efe9;
      border: 1px solid #d2e2d9;
      font-size: 12px;
    }
    .lane-list {
      display: grid;
      align-content: start;
      gap: 8px;
      min-height: 44px;
    }
    .empty-lane {
      border: 1px dashed #c8d9ce;
      border-radius: 10px;
      padding: 10px;
      font-size: 12px;
      color: var(--muted);
      background: rgba(255,255,255,.6);
    }
    .item-card {
      border: 1px solid var(--border);
      border-radius: 10px;
      padding: 9px;
      --fill-pct: 0%;
      position: relative;
      overflow: hidden;
      background: #ffffff;
      box-shadow: 0 3px 10px rgba(26,43,31,.08);
      display: grid;
      gap: 6px;
      isolation: isolate;
    }
    .item-card::before {
      content: "";
      position: absolute;
      left: 0;
      top: 0;
      bottom: 0;
      width: var(--fill-pct);
      max-width: 100%;
      background: linear-gradient(90deg, rgba(76, 175, 80, .48), rgba(31, 122, 77, .36));
      z-index: 0;
      pointer-events: none;
    }
    .item-card > * {
      position: relative;
      z-index: 1;
    }
    .item-card[data-row-kind] { cursor: context-menu; }
    .item-head {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      gap: 8px;
    }
    .item-title {
      font-size: 13px;
      font-weight: 700;
      line-height: 1.35;
      word-break: break-word;
    }
    .item-badge {
      border-radius: 999px;
      border: 1px solid #d1e2d8;
      background: #eff7f2;
      color: #2d4f3c;
      padding: 2px 8px;
      font-size: 11px;
      font-weight: 700;
      white-space: nowrap;
    }
    .item-meta {
      display: grid;
      gap: 3px;
      font-size: 12px;
      color: #334c3e;
    }
    .item-meta-line {
      display: grid;
      grid-template-columns: 70px 1fr;
      gap: 6px;
      align-items: baseline;
    }
    .item-meta-label {
      color: #617464;
      text-transform: uppercase;
      font-size: 10px;
      letter-spacing: .03em;
      font-weight: 700;
    }
    .item-meta-value {
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
    }
    .events-list {
      display: grid;
      gap: 8px;
      max-height: 300px;
      overflow: auto;
      padding-right: 4px;
    }
    .context-menu {
      position: fixed;
      z-index: 999;
      min-width: 150px;
      background: #ffffff;
      border: 1px solid var(--border);
      border-radius: 10px;
      box-shadow: 0 12px 28px rgba(26,43,31,.2);
      padding: 6px;
      display: grid;
      gap: 4px;
    }
    .context-menu[hidden] { display: none; }
    .context-menu button {
      text-align: left;
      background: #ffffff;
      color: var(--ink);
      border: 1px solid transparent;
      border-radius: 8px;
      padding: 8px 10px;
      font-weight: 600;
    }
    .context-menu button:hover:not(:disabled) {
      background: #eef5f0;
      border-color: var(--border);
    }
    .context-menu button:disabled {
      opacity: .45;
      cursor: not-allowed;
      background: #f7faf8;
    }
    @media (max-width: 980px) {
      .board-track { min-width: 100%; grid-template-columns: 1fr; }
    }
    @keyframes fade { from { opacity: 0; transform: translateY(6px); } to { opacity: 1; transform: translateY(0); } }
  </style>
</head>
<body>
  <div class="container">
    <div class="hero">
      <h1>Maxwell</h1>
      <p>Pluggable torrent and storage orchestration.</p>
      <div class="grid">
        <div class="card">
          <div class="label">VPN</div>
          <div class="value" id="vpn-value" data-state="{{.VPN}}">{{.VPN}}</div>
        </div>
        <div class="card">
          <div class="label">Downloads</div>
          <div class="value" id="downloads-value">{{index .Stats "downloads"}}</div>
        </div>
        <div class="card">
          <div class="label">Conversion Jobs</div>
          <div class="value" id="conversion-value">{{index .Stats "conversion"}}</div>
        </div>
        <div class="card">
          <div class="label">Upload Jobs</div>
          <div class="value" id="upload-value">{{index .Stats "upload"}}</div>
        </div>
        <div class="card">
          <div class="label">Links</div>
          <div class="value" id="links-value">{{index .Stats "links"}}</div>
        </div>
      </div>
      <div class="actions">
        <button id="run-once-btn" class="secondary">Run One Cycle</button>
        <button id="refresh-btn" class="secondary">Refresh Now</button>
        <span class="status" id="action-status">ready</span>
      </div>
      <div class="meta">Live updates: <span id="live-status">connecting...</span> | Last update: <span id="updated-at">never</span></div>
    </div>

    <div class="panels">
      <div class="panel">
        <h3>Add Magnet</h3>
        <form id="add-magnet-form">
          <textarea id="magnet-input" rows="4" placeholder="magnet:?xt=urn:btih:..."></textarea>
          <button id="add-magnet-btn" type="submit">Add Magnet</button>
        </form>
      </div>

      <div class="panel">
        <h3>Pipeline Board</h3>
        <div class="board">
          <div class="board-track" id="pipeline-board">
            <section class="lane" id="lane-torrents">
              <div class="lane-head">Active Torrents <span class="lane-count" id="torrents-count">0</span></div>
              <div class="lane-list" id="torrents-lane"></div>
            </section>
            <section class="lane" id="lane-conversion">
              <div class="lane-head">Conversion Queue <span class="lane-count" id="conversion-count">0</span></div>
              <div class="lane-list" id="conv-lane"></div>
            </section>
            <section class="lane" id="lane-upload">
              <div class="lane-head">Upload Queue <span class="lane-count" id="upload-count">0</span></div>
              <div class="lane-list" id="upload-lane"></div>
            </section>
            <section class="lane" id="lane-links">
              <div class="lane-head">Links <span class="lane-count" id="links-count">0</span></div>
              <div class="lane-list" id="links-lane"></div>
            </section>
          </div>
        </div>
      </div>

      <div class="panel">
        <h3>Events</h3>
        <div class="events-list" id="events-list"></div>
      </div>
    </div>
  </div>
  <div id="row-context-menu" class="context-menu" hidden>
    <button type="button" data-action="open-folder">Open Folder</button>
    <button type="button" data-action="pause">Pause</button>
    <button type="button" data-action="resume">Continue</button>
  </div>
  <script>
    (function () {
      const apiToken = {{.APIToken}};
      const initialCSRFToken = {{.CSRFToken}};

      const els = {
        vpn: document.getElementById('vpn-value'),
        downloads: document.getElementById('downloads-value'),
        conversion: document.getElementById('conversion-value'),
        upload: document.getElementById('upload-value'),
        links: document.getElementById('links-value'),
        live: document.getElementById('live-status'),
        updated: document.getElementById('updated-at'),
        status: document.getElementById('action-status'),
        addForm: document.getElementById('add-magnet-form'),
        magnetInput: document.getElementById('magnet-input'),
        addMagnetBtn: document.getElementById('add-magnet-btn'),
        runBtn: document.getElementById('run-once-btn'),
        refreshBtn: document.getElementById('refresh-btn'),
        rowMenu: document.getElementById('row-context-menu'),
        torrentsLane: document.getElementById('torrents-lane'),
        convLane: document.getElementById('conv-lane'),
        uploadLane: document.getElementById('upload-lane'),
        linksLane: document.getElementById('links-lane'),
        eventsList: document.getElementById('events-list'),
        torrentsCount: document.getElementById('torrents-count'),
        conversionCount: document.getElementById('conversion-count'),
        uploadCount: document.getElementById('upload-count'),
        linksCount: document.getElementById('links-count')
      };

      let rowActionContext = null;

      function setText(el, value) {
        if (el) el.textContent = String(value ?? 0);
      }

      function escapeHtml(v) {
        return String(v ?? '').replace(/[&<>"']/g, function (ch) {
          return ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;', '\'':'&#39;'})[ch];
        });
      }

      function fmtNum(n) {
        return Number.isFinite(Number(n)) ? Number(n).toFixed(2) : '0.00';
      }

      function clampRatio(n) {
        if (!Number.isFinite(Number(n))) return 0;
        const v = Number(n);
        if (v < 0) return 0;
        if (v > 1) return 1;
        return v;
      }

      function readCookie(name) {
        const prefix = name + '=';
        const parts = String(document.cookie || '').split(';');
        for (let i = 0; i < parts.length; i++) {
          const part = parts[i].trim();
          if (part.indexOf(prefix) === 0) {
            try { return decodeURIComponent(part.slice(prefix.length)); } catch (_) { return part.slice(prefix.length); }
          }
        }
        return '';
      }

      function currentCSRFToken() {
        return readCookie('maxwell_csrf') || initialCSRFToken || '';
      }

      function authHeaders() {
        const h = {};
        if (apiToken) h['X-API-Token'] = apiToken;
        const csrf = currentCSRFToken();
        if (csrf) h['X-CSRF-Token'] = csrf;
        return h;
      }

      function setStatus(msg, isErr) {
        if (!els.status) return;
        els.status.textContent = msg;
        els.status.style.color = isErr ? '#b03a2e' : '#4a5a4e';
      }

      function isVPNStateSafe(v) {
        return String(v == null ? '' : v).trim().toUpperCase() === 'SAFE';
      }

      function syncMagnetControls(vpnState) {
        const enabled = isVPNStateSafe(vpnState);
        if (els.magnetInput) {
          els.magnetInput.disabled = !enabled;
          els.magnetInput.placeholder = enabled ? 'magnet:?xt=urn:btih:...' : 'VPN not SAFE; magnet add disabled';
        }
        if (els.addMagnetBtn) {
          els.addMagnetBtn.disabled = !enabled;
          els.addMagnetBtn.title = enabled ? '' : 'VPN must be SAFE to add magnets';
        }
      }

      function applyOverview(payload) {
        if (!payload || typeof payload !== 'object') return;
        if (payload.vpn && els.vpn) {
          const vpn = String(payload.vpn);
          els.vpn.textContent = vpn;
          els.vpn.dataset.state = vpn;
          syncMagnetControls(vpn);
        }
        const stats = payload.stats || {};
        setText(els.downloads, stats.downloads || 0);
        setText(els.conversion, stats.conversion || 0);
        setText(els.upload, stats.upload || 0);
        setText(els.links, stats.links || 0);
        if (payload.updatedAt && els.updated) {
          els.updated.textContent = payload.updatedAt;
        }
      }

      function renderLane(target, items, emptyMsg) {
        if (!target) return;
        if (!items || items.length === 0) {
          target.innerHTML = '<div class="empty-lane">' + escapeHtml(emptyMsg) + '</div>';
          return;
        }
        target.innerHTML = items.join('');
      }

      function laneMetaLine(label, value) {
        const l = escapeHtml(label);
        const v = escapeHtml(value == null ? '' : value);
        return '<div class="item-meta-line"><span class="item-meta-label">' + l + '</span><span class="item-meta-value" title="' + v + '">' + v + '</span></div>';
      }

      function progressLabel(ratio) {
        return fmtNum(clampRatio(ratio) * 100) + '%';
      }

      function visibleFillRatio(ratio) {
        const clamped = clampRatio(ratio);
        if (clamped <= 0 || clamped >= 1) return clamped;
        // Keep low, non-zero progress clearly visible on small cards.
        return Math.max(clamped, 0.025);
      }

      function torrentProgressRatio(state, progress) {
        const s = normalizeStatus(state);
        if (s === 'pausedup') return 1;
        return clampRatio(progress);
      }

      function queueProgressRatio(status) {
        const s = normalizeStatus(status);
        if (s === 'done' || s === 'failed') return 1;
        if (s === 'running' || s === 'paused') return 0.5;
        return 0;
      }

      function itemCard(kind, id, status, title, badge, metaLines, fillRatio) {
        const titleHTML = escapeHtml(title == null ? '' : title);
        const badgeHTML = escapeHtml(badge == null ? '' : badge);
        const fillPct = progressLabel(visibleFillRatio(fillRatio));
        const styleAttr = ' style="--fill-pct:' + escapeHtml(fillPct) + ';"';
        const attrs = kind && id
          ? ' data-row-kind="' + escapeHtml(kind) + '" data-row-id="' + escapeHtml(id) + '" data-row-status="' + escapeHtml(status || '') + '"'
          : '';
        return '<article class="item-card"' + attrs + styleAttr + '>' +
          '<div class="item-head">' +
            '<div class="item-title" title="' + titleHTML + '">' + titleHTML + '</div>' +
            '<span class="item-badge">' + badgeHTML + '</span>' +
          '</div>' +
          '<div class="item-meta">' + metaLines.join('') + '</div>' +
        '</article>';
      }

      function normalizeStatus(v) {
        return String(v == null ? '' : v).trim().toLowerCase();
      }

      function isTorrentPausedState(status) {
        const s = normalizeStatus(status);
        return s.indexOf('pause') >= 0 || s.indexOf('stopped') >= 0;
      }

      function torrentLaneRankFromState(state, progress) {
        const s = normalizeStatus(state);
        const p = Number(progress || 0);
        if (s.indexOf('missing') >= 0) return 3;
        if (s.indexOf('pause') >= 0 || s.indexOf('stopped') >= 0) return 1;
        if (s.indexOf('complete') >= 0 || p >= 0.999) return 2;
        return 0;
      }

      function canPauseRow(ctx) {
        if (!ctx) return false;
        if (ctx.kind === 'torrent') return !isTorrentPausedState(ctx.status);
        return ctx.status === 'queued';
      }

      function canResumeRow(ctx) {
        if (!ctx) return false;
        if (ctx.kind === 'torrent') return isTorrentPausedState(ctx.status);
        return ctx.status === 'paused';
      }

      function canOpenFolder(ctx) {
        return !!ctx && ctx.kind === 'torrent';
      }

      function hideContextMenu() {
        if (!els.rowMenu) return;
        els.rowMenu.hidden = true;
        rowActionContext = null;
      }

      function showContextMenu(x, y, ctx) {
        if (!els.rowMenu) return;
        rowActionContext = ctx;
        const openBtn = els.rowMenu.querySelector('button[data-action="open-folder"]');
        const pauseBtn = els.rowMenu.querySelector('button[data-action="pause"]');
        const resumeBtn = els.rowMenu.querySelector('button[data-action="resume"]');
        if (openBtn) openBtn.disabled = !canOpenFolder(ctx);
        if (pauseBtn) pauseBtn.disabled = !canPauseRow(ctx);
        if (resumeBtn) resumeBtn.disabled = !canResumeRow(ctx);
        els.rowMenu.hidden = false;
        const rect = els.rowMenu.getBoundingClientRect();
        let left = x;
        let top = y;
        if (left + rect.width > window.innerWidth - 8) left = Math.max(8, window.innerWidth - rect.width - 8);
        if (top + rect.height > window.innerHeight - 8) top = Math.max(8, window.innerHeight - rect.height - 8);
        els.rowMenu.style.left = String(left) + 'px';
        els.rowMenu.style.top = String(top) + 'px';
      }

      function rowContextFromTarget(target) {
        const row = target && target.closest ? target.closest('[data-row-kind][data-row-id]') : null;
        if (!row) return null;
        const kind = String(row.dataset.rowKind || '').trim();
        const id = String(row.dataset.rowId || '').trim();
        const status = normalizeStatus(row.dataset.rowStatus || '');
        if (!kind || !id) return null;
        return { kind: kind, id: id, status: status };
      }

      function actionRequestForRow(ctx, action) {
        if (!ctx) return null;
        if (ctx.kind === 'torrent') {
          if (action === 'open-folder') return { url: '/api/torrents/open-folder', body: { hash: ctx.id } };
          return { url: '/api/torrents/action', body: { hash: ctx.id, action: action } };
        }
        if (ctx.kind === 'conversion') {
          return { url: '/api/conversion/action', body: { id: ctx.id, action: action } };
        }
        if (ctx.kind === 'upload') {
          return { url: '/api/upload/action', body: { id: ctx.id, action: action } };
        }
        return null;
      }

      async function runRowAction(action, ctx) {
        const req = actionRequestForRow(ctx, action);
        if (!req) return;
        const stateWord = action === 'pause' ? 'paused' : action === 'resume' ? 'continued' : 'opened';
        setStatus(ctx.kind + ' ' + stateWord + '...', false);
        try {
          await postForm(req.url, req.body);
          await refreshCollections();
          setStatus(ctx.kind + ' ' + stateWord, false);
        } catch (err) {
          setStatus(err.message || String(err), true);
        }
      }

      function renderTorrents(items) {
        const list = (items || []).slice().sort(function (a, b) {
          const ra = torrentLaneRankFromState(a.state, a.progress);
          const rb = torrentLaneRankFromState(b.state, b.progress);
          if (ra !== rb) return ra - rb;
          const na = String(a.name || '').toLowerCase();
          const nb = String(b.name || '').toLowerCase();
          if (na < nb) return -1;
          if (na > nb) return 1;
          return String(a.id || '').localeCompare(String(b.id || ''));
        });
        setText(els.torrentsCount, list.length);
        const cards = list.map(function (t) {
          const id = String(t.id || '');
          const state = String(t.state || '');
          const progress = torrentProgressRatio(state, t.progress);
          const pct = progressLabel(progress);
          return itemCard(
            'torrent',
            id,
            normalizeStatus(state),
            t.name || id,
            pct,
            [
              laneMetaLine('State', state || '-'),
              laneMetaLine('Speed', t.download_speed),
              laneMetaLine('ETA', t.eta_seconds)
            ],
            progress
          );
        });
        renderLane(els.torrentsLane, cards, 'No active torrents');
      }

      function renderConversion(items) {
        const list = items || [];
        setText(els.conversionCount, list.length);
        const cards = list.map(function (j) {
          const id = String(j.id || '');
          const status = normalizeStatus(j.status);
          const progress = queueProgressRatio(status);
          return itemCard(
            'conversion',
            id,
            status,
            'Job #' + id,
            j.status || '-',
            [
              laneMetaLine('Progress', progressLabel(progress)),
              laneMetaLine('Attempts', j.attempts),
              laneMetaLine('Input', j.input_path || '-'),
              laneMetaLine('Output', j.output_path || '-')
            ],
            progress
          );
        });
        renderLane(els.convLane, cards, 'No conversion jobs');
      }

      function renderUpload(items) {
        const list = items || [];
        setText(els.uploadCount, list.length);
        const cards = list.map(function (j) {
          const id = String(j.id || '');
          const status = normalizeStatus(j.status);
          const progress = queueProgressRatio(status);
          return itemCard(
            'upload',
            id,
            status,
            'Job #' + id,
            j.status || '-',
            [
              laneMetaLine('Progress', progressLabel(progress)),
              laneMetaLine('Attempts', j.attempts),
              laneMetaLine('Key', j.object_key || '-'),
              laneMetaLine('URL', j.final_url || '-')
            ],
            progress
          );
        });
        renderLane(els.uploadLane, cards, 'No upload jobs');
      }

      function renderLinks(items) {
        const list = items || [];
        setText(els.linksCount, list.length);
        const cards = list.map(function (l) {
          return itemCard(
            '',
            '',
            '',
            l.file_path || ('Link #' + (l.id == null ? '' : l.id)),
            'Ready',
            [
              laneMetaLine('URL', l.final_url || '-'),
              laneMetaLine('Created', l.created_at || '-')
            ],
            0
          );
        });
        renderLane(els.linksLane, cards, 'No links emitted');
      }

      function renderEvents(items) {
        const list = (items || []).slice(0, 80);
        const cards = list.map(function (e) {
          return itemCard(
            '',
            '',
            '',
            (e.type || 'event') + ' #' + escapeHtml(e.id),
            e.level || 'info',
            [
              laneMetaLine('Message', e.message || '-'),
              laneMetaLine('At', e.created_at || '-')
            ],
            0
          );
        });
        renderLane(els.eventsList, cards, 'No events yet');
      }

      async function fetchJSON(url) {
        const res = await fetch(url, { cache: 'no-store' });
        const text = await res.text();
        if (!res.ok) {
          const msg = String(text || '').trim();
          if (msg) throw new Error(msg);
          throw new Error('HTTP ' + res.status + ' for ' + url);
        }
        if (!text) return {};
        return JSON.parse(text);
      }

      async function fetchOverview() {
        try {
          applyOverview(await fetchJSON('/api/overview'));
        } catch (_) {}
      }

      async function refreshCollections() {
        try {
          const [torrents, queue, links, events] = await Promise.all([
            fetchJSON('/api/torrents'),
            fetchJSON('/api/queue'),
            fetchJSON('/api/links'),
            fetchJSON('/api/events')
          ]);
          renderTorrents(torrents);
          renderConversion(queue.conversion || []);
          renderUpload(queue.upload || []);
          renderLinks(links);
          renderEvents(events);
        } catch (err) {
          setStatus(err.message || String(err), true);
        }
      }

      async function postForm(url, bodyObj) {
        const body = new URLSearchParams(bodyObj || {});
        if (!body.has('csrf_token')) {
          const csrf = currentCSRFToken();
          if (csrf) body.set('csrf_token', csrf);
        }
        const headers = Object.assign({'Content-Type': 'application/x-www-form-urlencoded'}, authHeaders());
        const res = await fetch(url, { method: 'POST', headers: headers, body: body.toString() });
        if (!res.ok) {
          throw new Error(await res.text() || ('HTTP ' + res.status));
        }
        return res.json();
      }

      async function runOnce() {
        els.runBtn.disabled = true;
        setStatus('running one cycle...', false);
        try {
          await postForm('/api/run/once', {});
          await Promise.all([fetchOverview(), refreshCollections()]);
          setStatus('run cycle complete', false);
        } catch (err) {
          setStatus(err.message || String(err), true);
        } finally {
          els.runBtn.disabled = false;
        }
      }

      async function addMagnet() {
        if (els.addMagnetBtn && els.addMagnetBtn.disabled) {
          setStatus('magnet add is disabled while VPN is not SAFE', true);
          return;
        }
        const magnet = (els.magnetInput.value || '').trim();
        if (!magnet) {
          setStatus('magnet is required', true);
          return;
        }
        setStatus('adding magnet...', false);
        try {
          await postForm('/api/torrents/add', { magnet: magnet });
          els.magnetInput.value = '';
          await refreshCollections();
          setStatus('magnet added', false);
        } catch (err) {
          setStatus(err.message || String(err), true);
        }
      }

      function startPolling() {
        els.live.textContent = 'polling';
        fetchOverview();
        refreshCollections();
        setInterval(fetchOverview, 5000);
      }

      function startSSE() {
        if (!window.EventSource) {
          startPolling();
          return;
        }
        let retryMs = 1000;
        const connect = () => {
          const es = new EventSource('/api/stream');
          let closed = false;
          es.addEventListener('ready', function () {
            els.live.textContent = 'connected';
            retryMs = 1000;
          });
          es.addEventListener('overview', function (evt) {
            try { applyOverview(JSON.parse(evt.data)); } catch (_) {}
            els.live.textContent = 'connected';
          });
          es.addEventListener('overview_error', function () {
            els.live.textContent = 'degraded (fallback)';
            fetchOverview();
          });
          ['magnet_added', 'conversion_queued', 'conversion_done', 'upload_done', 'torrent_paused', 'torrent_resumed', 'conversion_paused', 'conversion_resumed', 'upload_paused', 'upload_resumed'].forEach(function (name) {
            es.addEventListener(name, function () { refreshCollections(); });
          });
          es.onerror = function () {
            es.close();
            if (closed) return;
            els.live.textContent = 'reconnecting...';
            setTimeout(connect, retryMs);
            retryMs = Math.min(retryMs * 2, 15000);
          };
          window.addEventListener('beforeunload', function () {
            closed = true;
            es.close();
          }, { once: true });
        };
        connect();
      }

      if (els.addForm) {
        els.addForm.addEventListener('submit', function (e) {
          e.preventDefault();
          addMagnet();
        });
      }
      if (els.runBtn) {
        els.runBtn.addEventListener('click', function () { runOnce(); });
      }
      if (els.refreshBtn) {
        els.refreshBtn.addEventListener('click', function () { Promise.all([fetchOverview(), refreshCollections()]); });
      }
      if (els.rowMenu) {
        document.addEventListener('contextmenu', function (e) {
          const ctx = rowContextFromTarget(e.target);
          if (!ctx) return;
          e.preventDefault();
          showContextMenu(e.clientX, e.clientY, ctx);
        });
        document.addEventListener('click', function (e) {
          if (!els.rowMenu || els.rowMenu.hidden) return;
          const btn = e.target.closest('#row-context-menu button[data-action]');
          if (btn) {
            e.preventDefault();
            if (!btn.disabled) {
              const action = String(btn.getAttribute('data-action') || '').trim();
              const ctx = rowActionContext;
              hideContextMenu();
              if (action) runRowAction(action, ctx);
            }
            return;
          }
          if (!e.target.closest('#row-context-menu')) {
            hideContextMenu();
          }
        });
        document.addEventListener('keydown', function (e) {
          if (e.key === 'Escape') hideContextMenu();
        });
        window.addEventListener('scroll', function () { hideContextMenu(); }, { passive: true });
      }

      fetchOverview();
      syncMagnetControls(els.vpn ? els.vpn.textContent : '');
      refreshCollections();
      setInterval(refreshCollections, 5000);
      startSSE();
    })();
  </script>
</body>
</html>`
