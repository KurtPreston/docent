package server

import (
	"net/http"
	"strconv"
	"strings"
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

// automationsSub handles /api/automations/{id} detail.
func (s *Server) automationsSub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/automations/"), "/")
	if id == "" || strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/automations/{id}"})
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
