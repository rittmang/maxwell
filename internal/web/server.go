package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
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
	SyncCompletedDownloads(context.Context) error
	ProcessOnce(context.Context) error
	ListConversionJobs(context.Context) ([]model.ConversionJob, error)
	ListUploadJobs(context.Context) ([]model.UploadJob, error)
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
	s.mux.HandleFunc("/api/run/once", s.handleRunOnce)
	s.mux.HandleFunc("/api/queue", s.handleQueue)
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
	writeJSON(w, list)
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
      grid-template-columns: repeat(auto-fit,minmax(420px,1fr));
      gap: 10px;
    }
    .panel-wide {
      grid-column: 1 / -1;
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
    .table-wrap {
      overflow: auto;
      max-height: 280px;
      border: 1px solid var(--border);
      border-radius: 8px;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 13px;
      min-width: 680px;
    }
    table.torrent-table {
      min-width: 980px;
      table-layout: fixed;
    }
    table.torrent-table th:nth-child(1), table.torrent-table td:nth-child(1) { width: 260px; }
    table.torrent-table th:nth-child(2), table.torrent-table td:nth-child(2) { width: 80px; }
    table.torrent-table th:nth-child(3), table.torrent-table td:nth-child(3) { width: 90px; }
    table.torrent-table th:nth-child(4), table.torrent-table td:nth-child(4) { width: 90px; }
    table.torrent-table th:nth-child(5), table.torrent-table td:nth-child(5) { width: 120px; }
    table.torrent-table th:nth-child(6), table.torrent-table td:nth-child(6) { width: 340px; }
    th, td {
      border-bottom: 1px solid var(--border);
      padding: 8px;
      text-align: left;
      white-space: nowrap;
    }
    th { background: #f0f6f2; position: sticky; top: 0; }
    td.wrap { white-space: normal; word-break: break-word; }
    .empty { color: var(--muted); padding: 10px; font-size: 13px; }
    table.torrent-table td.wrap {
      word-break: normal;
      overflow-wrap: anywhere;
    }
    @media (max-width: 980px) {
      .panels { grid-template-columns: 1fr; }
      .panel-wide { grid-column: auto; }
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
          <button type="submit">Add Magnet</button>
        </form>
      </div>

      <div class="panel panel-wide">
        <h3>Active Torrents</h3>
        <div class="table-wrap">
          <table class="torrent-table">
            <thead>
              <tr><th>Name</th><th>Progress</th><th>Speed</th><th>ETA</th><th>State</th><th>Path</th></tr>
            </thead>
            <tbody id="torrents-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel">
        <h3>Conversion Queue</h3>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>Status</th><th>Attempts</th><th>Input</th><th>Output</th></tr>
            </thead>
            <tbody id="conv-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel">
        <h3>Upload Queue</h3>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>Status</th><th>Attempts</th><th>Key</th><th>Final URL</th></tr>
            </thead>
            <tbody id="upload-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel">
        <h3>Links</h3>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>File</th><th>URL</th><th>Created</th></tr>
            </thead>
            <tbody id="links-body"></tbody>
          </table>
        </div>
      </div>

      <div class="panel">
        <h3>Events</h3>
        <div class="table-wrap">
          <table>
            <thead>
              <tr><th>ID</th><th>Level</th><th>Type</th><th>Message</th><th>At</th></tr>
            </thead>
            <tbody id="events-body"></tbody>
          </table>
        </div>
      </div>
    </div>
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
        runBtn: document.getElementById('run-once-btn'),
        refreshBtn: document.getElementById('refresh-btn'),
        torrentsBody: document.getElementById('torrents-body'),
        convBody: document.getElementById('conv-body'),
        uploadBody: document.getElementById('upload-body'),
        linksBody: document.getElementById('links-body'),
        eventsBody: document.getElementById('events-body')
      };

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

      function applyOverview(payload) {
        if (!payload || typeof payload !== 'object') return;
        if (payload.vpn && els.vpn) {
          const vpn = String(payload.vpn);
          els.vpn.textContent = vpn;
          els.vpn.dataset.state = vpn;
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

      function renderRows(target, rows, emptyMsg) {
        if (!target) return;
        if (!rows || rows.length === 0) {
          target.innerHTML = '<tr><td class="empty" colspan="8">' + escapeHtml(emptyMsg) + '</td></tr>';
          return;
        }
        target.innerHTML = rows.join('');
      }

      function renderTorrents(items) {
        const rows = (items || []).map(function (t) {
          return '<tr>' +
            '<td class="wrap">' + escapeHtml(t.name) + '</td>' +
            '<td>' + escapeHtml(fmtNum((t.progress || 0) * 100)) + '%</td>' +
            '<td>' + escapeHtml(t.download_speed) + '</td>' +
            '<td>' + escapeHtml(t.eta_seconds) + '</td>' +
            '<td>' + escapeHtml(t.state) + '</td>' +
            '<td class="wrap">' + escapeHtml(t.save_path) + '</td>' +
          '</tr>';
        });
        renderRows(els.torrentsBody, rows, 'No active torrents');
      }

      function renderConversion(items) {
        const rows = (items || []).map(function (j) {
          return '<tr>' +
            '<td>' + escapeHtml(j.id) + '</td>' +
            '<td>' + escapeHtml(j.status) + '</td>' +
            '<td>' + escapeHtml(j.attempts) + '</td>' +
            '<td class="wrap">' + escapeHtml(j.input_path) + '</td>' +
            '<td class="wrap">' + escapeHtml(j.output_path) + '</td>' +
          '</tr>';
        });
        renderRows(els.convBody, rows, 'No conversion jobs');
      }

      function renderUpload(items) {
        const rows = (items || []).map(function (j) {
          return '<tr>' +
            '<td>' + escapeHtml(j.id) + '</td>' +
            '<td>' + escapeHtml(j.status) + '</td>' +
            '<td>' + escapeHtml(j.attempts) + '</td>' +
            '<td class="wrap">' + escapeHtml(j.object_key) + '</td>' +
            '<td class="wrap">' + escapeHtml(j.final_url || '') + '</td>' +
          '</tr>';
        });
        renderRows(els.uploadBody, rows, 'No upload jobs');
      }

      function renderLinks(items) {
        const rows = (items || []).map(function (l) {
          return '<tr>' +
            '<td>' + escapeHtml(l.id) + '</td>' +
            '<td class="wrap">' + escapeHtml(l.file_path) + '</td>' +
            '<td class="wrap"><a href="' + escapeHtml(l.final_url) + '" target="_blank" rel="noreferrer">' + escapeHtml(l.final_url) + '</a></td>' +
            '<td>' + escapeHtml(l.created_at) + '</td>' +
          '</tr>';
        });
        renderRows(els.linksBody, rows, 'No links emitted');
      }

      function renderEvents(items) {
        const rows = (items || []).map(function (e) {
          return '<tr>' +
            '<td>' + escapeHtml(e.id) + '</td>' +
            '<td>' + escapeHtml(e.level) + '</td>' +
            '<td>' + escapeHtml(e.type) + '</td>' +
            '<td class="wrap">' + escapeHtml(e.message) + '</td>' +
            '<td>' + escapeHtml(e.created_at) + '</td>' +
          '</tr>';
        });
        renderRows(els.eventsBody, rows, 'No events yet');
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
          ['magnet_added', 'conversion_queued', 'conversion_done', 'upload_done'].forEach(function (name) {
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

      fetchOverview();
      refreshCollections();
      setInterval(refreshCollections, 5000);
      startSSE();
    })();
  </script>
</body>
</html>`
