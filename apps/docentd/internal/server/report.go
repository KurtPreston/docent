package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/report"
)

// reportGenerateTimeout bounds a single background generation independent of
// the request that started it. LLM providers can be slow, so it's generous.
const reportGenerateTimeout = 10 * time.Minute

type reportRequest struct {
	Mode    string `json:"mode"`
	Days    int    `json:"days"`
	Scope   string `json:"scope"`
	Prompt  string `json:"prompt"`
	Collect string `json:"collect"`
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
	// Blank scope/collect means "use the mode default"; anything else must be valid.
	scope := executionmode.Scope(strings.TrimSpace(req.Scope))
	if err := scope.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	collect := executionmode.Collect(strings.TrimSpace(req.Collect))
	if err := collect.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	cfg := userdata.ConfigFile{
		AI:             s.cfg.AI,
		Directives:     s.cfg.Directives,
		ExecutionModes: s.cfg.ExecutionModes,
	}
	id := s.reports.start()
	opts := report.Options{
		ModeID:    req.Mode,
		Days:      req.Days,
		Prompt:    strings.TrimSpace(req.Prompt),
		Scope:     scope,
		Collect:   collect,
		ConfigDir: s.cfg.ConfigDir,
		OnPhase: func(phase string) {
			s.reports.emit(id, reportEvent{Type: "phase", Phase: phase})
		},
		OnDirectiveUpdate: func(p collectors.DirectiveProgress) {
			s.reports.emit(id, reportEvent{
				Type: "collector",
				Collector: &reportCollectorView{
					ID:          p.DirectiveID,
					Description: p.Description,
					Status:      p.Status,
					Detail:      p.Detail,
					Completed:   p.Completed,
					Total:       p.Total,
				},
			})
		},
		OnContent: func(text string) {
			if text == "" {
				return
			}
			s.reports.emit(id, reportEvent{Type: "token", Text: text})
		},
		OnThinking: func(text string) {
			if text == "" {
				return
			}
			s.reports.emit(id, reportEvent{Type: "thinking", Text: text})
		},
	}

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
// returns the form metadata; "{id}/stream" is the SSE progress feed; any
// other single-segment suffix is treated as a job id to poll.
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
	if id, ok := strings.CutSuffix(rest, "/stream"); ok && id != "" && !strings.Contains(id, "/") {
		s.reportStream(w, r, id)
		return
	}
	if strings.Contains(rest, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such report"})
		return
	}
	job, ok := s.reports.get(rest)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such report"})
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// reportStream is the SSE progress feed for one report job. Generation is
// detached from this request: closing the stream (or navigating away) does
// not cancel the run. Late subscribers replay buffered events first.
func (s *Server) reportStream(w http.ResponseWriter, r *http.Request, id string) {
	replay, ch, cancel, ok := s.reports.subscribe(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such report"})
		return
	}
	if cancel != nil {
		defer cancel()
	}

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeSSE := func(ev reportEvent) bool {
		payload, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	terminal := false
	for _, ev := range replay {
		if !writeSSE(ev) {
			return
		}
		if ev.Type == "done" || ev.Type == "error" {
			terminal = true
		}
	}
	if terminal || ch == nil {
		return
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, open := <-ch:
			if !open {
				// Generation finished but the terminal event may have been
				// dropped on a full buffer; synthesize from the job snapshot.
				if job, ok := s.reports.get(id); ok {
					switch job.Status {
					case string(reportDone):
						_ = writeSSE(reportEvent{Type: "done", Markdown: job.Markdown, Meta: job.Meta})
					case string(reportError):
						_ = writeSSE(reportEvent{Type: "error", Error: job.Error})
					}
				}
				return
			}
			if !writeSSE(ev) {
				return
			}
			if ev.Type == "done" || ev.Type == "error" {
				return
			}
		}
	}
}

type reportModeMeta struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	PromptRequired bool   `json:"promptRequired"`
	// Defaults mirror what executionmode.Resolve would use for a
	// non-interactive caller so the /report form can prefill itself.
	LookbackKind string `json:"lookbackKind"` // "days" | "previous-weekday"
	LookbackDays int    `json:"lookbackDays,omitempty"`
	Scope        string `json:"scope"`
	Prompt       string `json:"prompt,omitempty"`
	Collect      string `json:"collect"`
}

// reportModeDefaults projects an ExecutionMode into the fields the Report
// form shows, applying the same non-interactive fallbacks Resolve uses
// (7-day lookback, involved scope, events collect) when a property is unset.
func reportModeDefaults(m executionmode.ExecutionMode) reportModeMeta {
	out := reportModeMeta{
		ID:             m.ID,
		Name:           m.Display(),
		PromptRequired: m.Prompt == nil,
	}
	switch {
	case m.Lookback == nil:
		out.LookbackKind = executionmode.LookbackKindDays
		out.LookbackDays = 7
	default:
		out.LookbackKind = m.Lookback.Kind
		out.LookbackDays = m.Lookback.Days
	}
	if m.Scope == executionmode.ScopeUnset {
		out.Scope = string(executionmode.ScopeInvolved)
	} else {
		out.Scope = string(m.Scope)
	}
	if m.Prompt != nil {
		out.Prompt = m.Prompt.Instruction
	}
	if m.Collect == executionmode.CollectUnset {
		out.Collect = string(executionmode.CollectEvents)
	} else {
		out.Collect = string(m.Collect)
	}
	return out
}

// reportMeta returns the modes, scopes, collects, and AI identity the /report
// form needs to render and prefill itself.
func (s *Server) reportMeta(w http.ResponseWriter, _ *http.Request) {
	modes, err := executionmode.Load(executionmode.BuiltinModes(), s.cfg.ExecutionModes)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	out := make([]reportModeMeta, 0, len(modes))
	for _, m := range modes {
		out = append(out, reportModeDefaults(m))
	}
	provider, model := aiIdentity(s.cfg.AI)
	writeJSON(w, http.StatusOK, map[string]any{
		"modes": out,
		"scopes": []string{
			string(executionmode.ScopeSelf),
			string(executionmode.ScopeInvolved),
			string(executionmode.ScopeAll),
		},
		"collects": []string{
			string(executionmode.CollectEvents),
			string(executionmode.CollectState),
			string(executionmode.CollectBoth),
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
