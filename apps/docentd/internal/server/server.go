package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/engine"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/webhook"
)

type Server struct {
	cfg      config.DaemonConfig
	engine   *engine.Engine
	registry *registry.Store
	webRoot  string
}

func New(cfg config.DaemonConfig, eng *engine.Engine, reg *registry.Store, webRoot string) *Server {
	return &Server{cfg: cfg, engine: eng, registry: reg, webRoot: webRoot}
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
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/workitems/"), "/")
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

func (s *Server) staticOrIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" || path == "dashboard" {
		path = "index.html"
	}
	// Map extensionless app routes (e.g. /signals, /collectors, /workitem)
	// to their backing HTML page.
	if path != "" && filepath.Ext(path) == "" {
		path += ".html"
	}
	full := filepath.Join(s.webRoot, filepath.Clean(path))
	if !strings.HasPrefix(full, s.webRoot) {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	switch filepath.Ext(full) {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
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

// WebFS returns an fs.FS for embedded web assets (optional).
var WebFS fs.FS
