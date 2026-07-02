package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/webhook"
)

type Server struct {
	cfg      config.DaemonConfig
	engine   *engine.Engine
	registry *registry.Store
	webRoot  string
	web      fs.FS
	reports  *reportStore
}

// New builds the HTTP server. webRoot is the on-disk dashboard directory used
// in dev/disk mode; webFS, when non-nil (embed builds), takes precedence and
// serves the dashboard from assets baked into the binary.
func New(cfg config.DaemonConfig, eng *engine.Engine, reg *registry.Store, webRoot string, webFS fs.FS) *Server {
	return &Server{cfg: cfg, engine: eng, registry: reg, webRoot: webRoot, web: webFS, reports: newReportStore()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// /health and the static dashboard shell stay open (the shell carries no
	// secrets); every data endpoint is gated by requireAuth so that binding
	// docentd to a non-loopback interface only exposes data to bearers of the
	// configured token. With no token set, requireAuth is a pass-through.
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/sessions", s.requireAuth(s.sessions))
	mux.HandleFunc("/api/workitems", s.requireAuth(s.sessions))
	mux.HandleFunc("/api/workitems/", s.requireAuth(s.workItemDetail))
	mux.HandleFunc("/api/signals", s.requireAuth(s.signalsAPI))
	mux.HandleFunc("/api/collectors", s.requireAuth(s.collectorsAPI))
	mux.HandleFunc("/api/units/", s.requireAuth(s.collectUnit))
	mux.HandleFunc("/api/report", s.requireAuth(s.reportStart))
	mux.HandleFunc("/api/report/", s.requireAuth(s.reportSub))
	mux.HandleFunc("/ingest", s.requireAuth(s.ingest))
	mux.HandleFunc("/", s.staticOrIndex)
	return mux
}

// requireAuth gates a handler behind the shared-secret bearer check. When no
// token is configured, authOK returns true and this is a transparent
// pass-through (preserving the open, loopback-only default).
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authOK(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
			return
		}
		h(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.engine.RefreshOnRequest(ctx))
}

func (s *Server) signalsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.Signals())
}

func (s *Server) collectorsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.engine.Collectors())
}

func (s *Server) workItemDetail(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/workitems/"), "/")
	if strings.HasSuffix(rest, "/launch") {
		s.workItemLaunch(w, r, strings.TrimSuffix(rest, "/launch"))
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := rest
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "work item key required"})
		return
	}
	detail, ok := s.engine.WorkItem(key)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "work item not found"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) workItemLaunch(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key = strings.Trim(key, "/")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "work item key required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	result, ok := s.engine.LaunchWorkItem(ctx, key)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "work item not found"})
		return
	}
	status := http.StatusOK
	if !result.OK {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

// collectUnit force-collects one (directive, mode) unit now, ignoring its
// poll interval, then rebuilds. Path: /api/units/{directive}/{mode}/collect.
func (s *Server) collectUnit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/units/"), "/"), "/")
	if len(parts) != 3 || parts[2] != "collect" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/units/{directive}/{mode}/collect"})
		return
	}
	directiveID, mode := parts[0], collectors.Mode(parts[1])
	if mode != collectors.ModeState && mode != collectors.ModeEvents {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "mode must be state or events"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if !s.engine.CollectUnitNow(ctx, directiveID, mode) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such collection unit"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "directive": directiveID, "mode": string(mode)})
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var payload struct {
		Source         string `json:"source"`
		Kind           string `json:"kind"`
		Name           string `json:"name"`
		Path           string `json:"path"`
		Host           string `json:"host"`
		Color          string `json:"color"`
		ConversationID string `json:"conversationId"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	name := payload.Name
	if name == "" && payload.Path != "" {
		name = filepath.Base(strings.TrimRight(payload.Path, "/"))
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "name or path required"})
		return
	}
	source := payload.Source
	if source == "" {
		source = "cursor"
	}
	kind := payload.Kind
	if kind == "" {
		kind = "agent-stop"
	}
	webhook.Default.Push(webhook.Event{
		Source:         source,
		Kind:           kind,
		Name:           name,
		Path:           payload.Path,
		Host:           payload.Host,
		Color:          payload.Color,
		ConversationID: payload.ConversationID,
		ReceivedAt:     time.Now().UTC(),
	})
	_ = s.registry.ApplyEvent(name, kind, payload.Host, payload.Path, payload.Color)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": name, "kind": kind})
}

// staticOrIndex serves the single-page dashboard. It serves the requested
// asset when it exists; otherwise, for extensionless client-side routes
// (e.g. /signals, /collectors, /workitem, and any future route), it falls back
// to index.html so react-router can render them. Assets come from the embedded
// FS in embed builds, else from webRoot on disk.
func (s *Server) staticOrIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Normalize to a clean, slash-rooted asset name; path.Clean against a
	// leading "/" neutralizes any ".." traversal before we hit the FS/disk.
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if b, ctype, ok := s.readAsset(name); ok {
		writeAsset(w, b, ctype)
		return
	}
	// SPA fallback for client routes. Missing files that look like assets
	// (have an extension) and unmatched /api/* paths stay 404s.
	if path.Ext(name) == "" && !strings.HasPrefix(name, "api/") {
		if b, _, ok := s.readAsset("index.html"); ok {
			writeAsset(w, b, "text/html; charset=utf-8")
			return
		}
	}
	http.NotFound(w, r)
}

// readAsset returns the named dashboard asset from the embedded FS (embed
// builds) or from webRoot on disk, along with its content type.
func (s *Server) readAsset(name string) (data []byte, contentType string, ok bool) {
	var (
		b   []byte
		err error
	)
	if s.web != nil {
		b, err = fs.ReadFile(s.web, name)
	} else {
		full := filepath.Join(s.webRoot, filepath.FromSlash(name))
		if !strings.HasPrefix(full, s.webRoot) {
			return nil, "", false
		}
		b, err = os.ReadFile(full)
	}
	if err != nil {
		return nil, "", false
	}
	return b, contentTypeFor(name), true
}

func writeAsset(w http.ResponseWriter, b []byte, contentType string) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func contentTypeFor(name string) string {
	switch path.Ext(name) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return ""
}

func (s *Server) authOK(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return false
	}
	got := strings.TrimSpace(h[7:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
