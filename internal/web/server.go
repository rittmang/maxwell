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
	if s.csrfEnabled {
		s.ensureCSRFCookie(w, r)
	}
	_ = s.tmpl.Execute(w, map[string]any{
		"VPN":   vpnState,
		"Stats": stats,
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
	if s.token != "" && r.Header.Get("X-API-Token") != s.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if s.csrfEnabled && !s.validCSRF(r) {
		http.Error(w, "csrf check failed", http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	magnet := r.Form.Get("magnet")
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

func (s *Server) ensureCSRFCookie(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("maxwell_csrf"); err == nil {
		return
	}
	token := randomHex(16)
	http.SetCookie(w, &http.Cookie{
		Name:     "maxwell_csrf",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
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
    }
    body {
      margin: 0;
      font-family: "Avenir Next", "Segoe UI", sans-serif;
      color: var(--ink);
      background: radial-gradient(circle at 20% 0%, #e7f3ea, var(--bg));
      min-height: 100vh;
    }
    .container {
      max-width: 980px;
      margin: 24px auto;
      padding: 0 16px;
    }
    .hero {
      background: var(--card);
      border-radius: 14px;
      padding: 20px;
      box-shadow: 0 8px 20px rgba(26,43,31,.08);
      animation: fade .5s ease;
    }
    .hero h1 { margin: 0; letter-spacing: .06em; text-transform: uppercase; }
    .grid {
      margin-top: 16px;
      display: grid;
      grid-template-columns: repeat(auto-fit,minmax(210px,1fr));
      gap: 12px;
    }
    .card {
      background: var(--card);
      border-radius: 12px;
      padding: 12px;
      box-shadow: 0 6px 14px rgba(26,43,31,.07);
    }
    .label { color: var(--muted); font-size: 12px; text-transform: uppercase; letter-spacing: .04em; }
    .value { font-size: 22px; margin-top: 8px; }
    #vpn-value[data-state="safe"], #vpn-value[data-state="SAFE"] { color: var(--accent); }
    #vpn-value[data-state="unsafe"], #vpn-value[data-state="UNSAFE"], #vpn-value[data-state="unknown"], #vpn-value[data-state="UNKNOWN"] { color: var(--warn); }
    .meta { margin-top: 10px; font-size: 12px; color: var(--muted); }
    @keyframes fade { from { opacity: 0; transform: translateY(8px); } to { opacity: 1; transform: translateY(0); } }
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
      <div class="meta">Live updates: <span id="live-status">connecting...</span> | Last update: <span id="updated-at">never</span></div>
    </div>
  </div>
  <script>
    (function () {
      const els = {
        vpn: document.getElementById('vpn-value'),
        downloads: document.getElementById('downloads-value'),
        conversion: document.getElementById('conversion-value'),
        upload: document.getElementById('upload-value'),
        links: document.getElementById('links-value'),
        live: document.getElementById('live-status'),
        updated: document.getElementById('updated-at')
      };

      function setText(el, value) {
        if (el) el.textContent = String(value ?? 0);
      }

      function applyOverview(payload) {
        if (!payload || typeof payload !== 'object') return;
        if (payload.vpn && els.vpn) {
          const vpn = String(payload.vpn);
          els.vpn.textContent = vpn;
          els.vpn.dataset.state = vpn.toLowerCase();
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

      async function fetchOverview() {
        try {
          const res = await fetch('/api/overview', { cache: 'no-store' });
          if (!res.ok) return;
          applyOverview(await res.json());
        } catch (_) {}
      }

      function startPolling() {
        els.live.textContent = 'polling';
        fetchOverview();
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

      startSSE();
    })();
  </script>
</body>
</html>`
