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
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/sessions", s.sessions)
	mux.HandleFunc("/api/workitems", s.sessions)
	mux.HandleFunc("/ingest", s.ingest)
	mux.HandleFunc("/", s.staticOrIndex)
	return mux
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
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	d, err := s.engine.Refresh(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.authOK(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
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
