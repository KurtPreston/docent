package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/model"
)

// automationsAPI handles GET /api/automations — list configured rules + recent jobs.
func (s *Server) automationsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rules := s.cfg.Automations
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	jobs := s.engine.AutomationJobs(limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"rules": rules,
		"jobs":  jobs,
	})
}

// automationsSub handles /api/automations/{id} (GET detail) and
// /api/automations/{id}/run (POST manual trigger).
func (s *Server) automationsSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/automations/"), "/")
	if rest == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/automations/{id}"})
		return
	}
	parts := strings.Split(rest, "/")
	id := parts[0]
	if len(parts) == 2 && parts[1] == "run" {
		s.automationRun(w, r, id)
		return
	}
	if len(parts) != 1 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/automations/{id} or /api/automations/{id}/run"})
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	for _, rule := range s.cfg.Automations {
		if rule.ID == id {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "rule not found"})
}

// automationRun handles POST /api/automations/{id}/run — fire a rule's actions
// immediately (bypassing schedule/cooldown) for testing. An optional JSON body
// supplies synthetic event context (title, url, repo, ticket, from/to, …) so
// signal/transition rules can be exercised without a real event.
func (s *Server) automationRun(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rule, ok := s.engine.FindAutomation(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "rule not found"})
		return
	}
	var body struct {
		Title    string            `json:"title"`
		URL      string            `json:"url"`
		Repo     string            `json:"repo"`
		Branch   string            `json:"branch"`
		Ticket   string            `json:"ticket"`
		OpenPath string            `json:"openPath"`
		From     string            `json:"from"`
		To       string            `json:"to"`
		Source   string            `json:"source"`
		Kind     string            `json:"kind"`
		Fields   map[string]string `json:"fields"`
	}
	if r.Body != nil {
		// Body is optional; ignore decode errors (e.g. empty body).
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	source := body.Source
	if source == "" {
		source = rule.Trigger.Source
	}
	kind := body.Kind
	if kind == "" && len(rule.Trigger.Kind) > 0 {
		kind = rule.Trigger.Kind[0]
	}
	fields := map[string]string{}
	for k, v := range body.Fields {
		fields[k] = v
	}
	if body.OpenPath != "" {
		fields["path"] = body.OpenPath
	}
	if body.Ticket != "" {
		fields["ticket"] = body.Ticket
	}
	if body.Branch != "" {
		fields["branch"] = body.Branch
	}
	ev := automation.Event{
		Rule:    rule,
		Trigger: "manual",
		Signal: &model.Signal{
			Source:     source,
			Kind:       kind,
			Title:      body.Title,
			URL:        body.URL,
			Repository: body.Repo,
			IsSelf:     true,
			Fields:     fields,
		},
		From:      body.From,
		To:        body.To,
		FiredAt:   time.Now(),
		TicketKey: body.Ticket,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	job, ok := s.engine.RunAutomationNow(ctx, ev)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "automations not configured"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": job.Status != automation.JobError, "job": job})
}
