package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/report"
)

// reportGenerateTimeout bounds a single background generation independent of
// the request that started it. LLM providers can be slow, so it's generous.
const reportGenerateTimeout = 10 * time.Minute

type reportRequest struct {
	Mode   string `json:"mode"`
	Days   int    `json:"days"`
	Scope  string `json:"scope"`
	Prompt string `json:"prompt"`
}

// reportStart handles POST /api/report: it validates the request, kicks off
// generation in the background, and returns the job id immediately.
func (s *Server) reportStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req reportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	req.Mode = strings.TrimSpace(req.Mode)
	if req.Mode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "mode is required"})
		return
	}
	// Blank scope means "use the mode default"; anything else must be valid.
	scope := executionmode.Scope(strings.TrimSpace(req.Scope))
	if err := scope.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	cfg := userdata.ConfigFile{
		AI:             s.cfg.AI,
		Directives:     s.cfg.Directives,
		ExecutionModes: s.cfg.ExecutionModes,
	}
	opts := report.Options{
		ModeID:    req.Mode,
		Days:      req.Days,
		Prompt:    strings.TrimSpace(req.Prompt),
		Scope:     scope,
		ConfigDir: s.cfg.ConfigDir,
	}

	id := s.reports.start()
	go func() {
		s.reports.markRunning(id)
		// Detached from the request context so polling/navigation doesn't
		// cancel an in-flight report.
		ctx, cancel := context.WithTimeout(context.Background(), reportGenerateTimeout)
		defer cancel()
		res, err := report.Generate(ctx, cfg, opts)
		if err != nil {
			s.reports.fail(id, err)
			return
		}
		s.reports.finish(id, res)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": id})
}

// reportSub handles the /api/report/ subtree: the literal suffix "meta"
// returns the form metadata; any other suffix is treated as a job id to poll.
func (s *Server) reportSub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/report/"), "/")
	if rest == "meta" {
		s.reportMeta(w, r)
		return
	}
	if rest == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "report id required"})
		return
	}
	job, ok := s.reports.get(rest)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such report"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

type reportModeMeta struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PromptRequired bool   `json:"promptRequired"`
}

// reportMeta returns the modes, scopes, and AI identity the /report form needs
// to render itself.
func (s *Server) reportMeta(w http.ResponseWriter, _ *http.Request) {
	modes, err := executionmode.Load(executionmode.BuiltinModes(), s.cfg.ExecutionModes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]reportModeMeta, 0, len(modes))
	for _, m := range modes {
		// A mode with no declared Prompt must be given one at run time
		// (executionmode.Resolve errors otherwise for non-interactive callers).
		out = append(out, reportModeMeta{ID: m.ID, Name: m.Display(), PromptRequired: m.Prompt == nil})
	}
	provider, model := aiIdentity(s.cfg.AI)
	writeJSON(w, http.StatusOK, map[string]any{
		"modes": out,
		"scopes": []string{
			string(executionmode.ScopeSelf),
			string(executionmode.ScopeInvolved),
			string(executionmode.ScopeAll),
		},
		"provider": map[string]any{
			"label":    providerLabel(provider, model),
			"provider": provider,
			"model":    model,
		},
	})
}

// aiIdentity mirrors ai.SelectProvider's normalization + defaults to derive a
// stable (provider, model/command) pair for display.
func aiIdentity(cfg userdata.AIConfig) (provider, model string) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(cfg.Provider), "_", "-")) {
	case "ollama":
		m := strings.TrimSpace(cfg.Ollama.Model)
		if m == "" {
			m = "llama3"
		}
		return "ollama", m
	case "cursor":
		c := strings.TrimSpace(cfg.Cursor.Command)
		if c == "" {
			c = "cursor-agent"
		}
		return "cursor", c
	case "claude":
		c := strings.TrimSpace(cfg.Claude.Command)
		if c == "" {
			c = "claude"
		}
		return "claude", c
	default:
		return "rule-based", ""
	}
}

func providerLabel(provider, model string) string {
	switch provider {
	case "ollama":
		return "Ollama (" + model + ")"
	case "cursor":
		return "Cursor CLI (" + model + ")"
	case "claude":
		return "Claude CLI (" + model + ")"
	default:
		return "Rule-based (deterministic)"
	}
}
