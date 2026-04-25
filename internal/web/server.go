package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
	"github.com/kurt/slakkr-ai/internal/workflow"
)

// Server exposes read-only JSON endpoints over local userdata.
type Server struct {
	UserdataDir string
	Deps        workflow.Deps
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/planning-input", s.handlePlanningInput)
	mux.HandleFunc("GET /api/plan", s.handlePlan)
	mux.HandleFunc("GET /api/delegations", s.handleDelegations)
	mux.HandleFunc("GET /", s.handleIndex)
	return mux
}

// ListenAndServe starts the HTTP server on addr (e.g. 127.0.0.1:8765).
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.handler())
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>slakkr</title></head><body>
<h1>slakkr local UI</h1>
<ul>
<li><a href="/api/planning-input?date=%s">Today planning input (JSON)</a></li>
<li><a href="/api/plan?date=%s">Today structured plan (JSON)</a></li>
<li><a href="/api/delegations">Delegations (JSON)</a></li>
</ul>
</body></html>`, time.Now().Format("2006-01-02"), time.Now().Format("2006-01-02"))
}

func (s *Server) handlePlanningInput(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	if s.Deps.Now == nil {
		s.Deps.Now = time.Now
	}
	input, _, err := workflow.BuildPlanningInput(ctx, s.Deps, s.UserdataDir, date, "web")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(input)
}

func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = time.Now().Format("2006-01-02")
	}
	date, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		http.Error(w, "invalid date", http.StatusBadRequest)
		return
	}
	store := userdata.NewStore(s.UserdataDir)
	plan, err := store.LoadDailyPlan(date)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "plan not found for date (run start_day first)", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if plan.Date == "" {
		http.Error(w, "plan not found for date (run start_day first)", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(plan)
}

func (s *Server) handleDelegations(w http.ResponseWriter, r *http.Request) {
	store := userdata.NewStore(s.UserdataDir)
	projects, err := store.LoadProjects()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	del, err := store.LoadDelegations(tasks)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(del)
}
