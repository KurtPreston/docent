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
	mux.HandleFunc("/api/workitems", s.requireAuth(s.workItems))
	mux.HandleFunc("/api/workitems/", s.requireAuth(s.workItemDetail))
	mux.HandleFunc("/api/signals", s.requireAuth(s.signalsAPI))
	mux.HandleFunc("/api/collectors", s.requireAuth(s.collectorsAPI))
	mux.HandleFunc("/api/units/", s.requireAuth(s.collectUnit))
	mux.HandleFunc("/api/config", s.requireAuth(s.configAPI))
	mux.HandleFunc("/api/config/", s.requireAuth(s.configItemAPI))
	mux.HandleFunc("/api/report", s.requireAuth(s.reportStart))
	mux.HandleFunc("/api/report/", s.requireAuth(s.reportSub))
	mux.HandleFunc("/api/hooks/", s.hooksAPI) // auth via bearer or hook secret
	mux.HandleFunc("/api/automations", s.requireAuth(s.automationsAPI))
	mux.HandleFunc("/api/automations/", s.requireAuth(s.automationsSub))
	mux.HandleFunc("/api/sessions", s.requireAuth(s.sessionsList))
	mux.HandleFunc("/api/sessions/events", s.requireAuth(s.sessionEvents))
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

// workItems serves the dashboard payload (work-item groups) and triggers an
// on-request refresh of any collectors flagged onRequest.
func (s *Server) workItems(w http.ResponseWriter, r *http.Request) {
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
	if strings.HasSuffix(rest, "/open") {
		s.workItemOpen(w, r, strings.TrimSuffix(rest, "/open"))
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

// workItemOpen prepares a work item to be opened in the editor: for the cursor
// provider (with color-writing enabled) it syncs the work item's color into its
// .vscode/settings.json, then returns the provider deep link for the client to
// navigate. Path: /api/workitems/{key}/open.
func (s *Server) workItemOpen(w http.ResponseWriter, r *http.Request, key string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key = strings.Trim(key, "/")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "work item key required"})
		return
	}
	result, ok := s.engine.OpenWorkItem(key)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "work item not found"})
		return
	}
	status := http.StatusOK
	if !result.OK {
		status = http.StatusInternalServerError
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

// SessionEventRequest is the payload accepted by POST /api/sessions/events. It
// is the canonical contract shared by the Cursor hooks and the IDE extension.
// A session's identity is the composite of ide, ideHost, targetHost, and path.
type SessionEventRequest struct {
	// IDE is the editor reporting the event ("cursor", "vscode", "vi", ...).
	IDE string `json:"ide"`
	// IDEHost is the host of the machine the IDE runs on.
	IDEHost string `json:"ideHost"`
	// TargetHost is the remote server the IDE is editing, when applicable.
	TargetHost string `json:"targetHost,omitempty"`
	// Path is the workspace path being edited.
	Path string `json:"path,omitempty"`
	// Event is one of: open, close, agent_request_sent,
	// agent_response_received, heartbeat.
	Event string `json:"event"`
	// Name overrides the display leaf name (defaults to basename of path).
	Name string `json:"name,omitempty"`
	// Color optionally carries the workspace title-bar color.
	Color string `json:"color,omitempty"`
}

// SessionEventResponse is returned by POST /api/sessions/events.
type SessionEventResponse struct {
	OK    bool   `json:"ok"`
	Key   string `json:"key,omitempty"`
	Event string `json:"event,omitempty"`
	Error string `json:"error,omitempty"`
}

// validSessionEvents is the accepted event vocabulary.
var validSessionEvents = map[string]bool{
	"open":                    true,
	"close":                   true,
	"agent_request_sent":      true,
	"agent_response_received": true,
	"heartbeat":               true,
}

// sessionEvents ingests a single session lifecycle/activity event, updating the
// composite-keyed session registry (and the webhook inbox timeline for
// non-heartbeat events).
func (s *Server) sessionEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var payload SessionEventRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, SessionEventResponse{OK: false, Error: "invalid json"})
		return
	}
	payload.IDE = strings.TrimSpace(payload.IDE)
	if payload.IDE == "" {
		payload.IDE = "cursor"
	}
	event := strings.TrimSpace(payload.Event)
	if !validSessionEvents[event] {
		writeJSON(w, http.StatusBadRequest, SessionEventResponse{OK: false, Error: "invalid or missing event"})
		return
	}
	if strings.TrimSpace(payload.IDEHost) == "" && strings.TrimSpace(payload.Path) == "" {
		writeJSON(w, http.StatusBadRequest, SessionEventResponse{OK: false, Error: "ideHost or path required"})
		return
	}
	id := registry.Identity{
		IDE:        payload.IDE,
		IDEHost:    payload.IDEHost,
		TargetHost: payload.TargetHost,
		Path:       payload.Path,
	}
	name := strings.TrimSpace(payload.Name)
	if name == "" {
		name = id.Name()
	}
	// Heartbeats are high-frequency liveness pings and would flood the
	// timeline; only push meaningful lifecycle/activity events to the inbox.
	if event != "heartbeat" {
		host := payload.TargetHost
		if host == "" {
			host = payload.IDEHost
		}
		webhook.Default.Push(webhook.Event{
			Source:     payload.IDE,
			Kind:       event,
			Name:       name,
			Path:       payload.Path,
			Host:       host,
			Color:      payload.Color,
			ReceivedAt: time.Now().UTC(),
		})
	}
	_ = s.registry.ApplyEvent(id, event, name, payload.Color)
	writeJSON(w, http.StatusOK, SessionEventResponse{OK: true, Key: id.Key(), Event: event})
}

// sessionsList returns the current session registry records (the ingest view of
// what is open), keyed by composite key.
func (s *Server) sessionsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ttl := s.cfg.Sessions.TTL()
	now := time.Now()
	// provider/workItemKey/deepLink let the Sessions page mirror the dashboard's
	// launch action (open/focus the IDE) and cross-link a session to its work
	// item, without the page needing to fetch and correlate /api/workitems.
	type sessionView struct {
		Key          string `json:"key"`
		IDE          string `json:"ide,omitempty"`
		IDEHost      string `json:"ideHost,omitempty"`
		TargetHost   string `json:"targetHost,omitempty"`
		Path         string `json:"path,omitempty"`
		Name         string `json:"name,omitempty"`
		Live         bool   `json:"live"`
		Status       string `json:"status"`
		LastActivity string `json:"lastActivity,omitempty"`
		Provider     string `json:"provider,omitempty"`
		WorkItemKey  string `json:"workItemKey,omitempty"`
		DeepLink     string `json:"deepLink,omitempty"`
	}
	provider := s.engine.Provider()
	all := s.registry.All()
	out := make([]sessionView, 0, len(all))
	for key, rec := range all {
		out = append(out, sessionView{
			Key:          key,
			IDE:          rec.IDE,
			IDEHost:      rec.IDEHost,
			TargetHost:   rec.TargetHost,
			Path:         rec.Path,
			Name:         rec.Name,
			Live:         registry.IsFresh(rec, ttl, now),
			Status:       registry.SessionStatus(rec),
			LastActivity: registry.LatestActivity(rec),
			Provider:     provider,
			WorkItemKey:  s.engine.WorkItemKeyForSession(key, rec.Name, rec.Path),
			DeepLink:     s.engine.SessionDeepLink(rec.Path, rec.TargetHost),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
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
