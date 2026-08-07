package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/agentsession"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/worktree"
)

// agentStartRequest is the POST /api/agents body.
type agentStartRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Title    string `json:"title"`
	Repo     string `json:"repo"`
	Branch   string `json:"branch"`
	Dir      string `json:"dir"`
	BaseRef  string `json:"baseRef"`
	OpenPath string `json:"openPath"`
	Prompt   string `json:"prompt"`
	// Force proceeds even when another agent appears to be working in the
	// worktree. It is the client's way of saying the user saw the warning.
	Force bool `json:"force"`
}

type agentTurnRequest struct {
	Prompt string `json:"prompt"`
	Force  bool   `json:"force"`
}

// provisionTimeout bounds the part of a start request the caller waits on:
// resolving the worktree, which the first time a repository is seen means
// cloning it. The turn itself
// runs in the background and is not covered by this.
const provisionTimeout = 5 * time.Minute

// agentsAPI handles /api/agents: GET lists sessions, POST starts one.
func (s *Server) agentsAPI(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		s.agentsUnavailable(w)
		return
	}
	switch r.Method {
	case http.MethodGet:
		sessions, err := s.agents.Sessions()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": agentViews(sessions)})
	case http.MethodPost:
		s.agentStart(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) agentStart(w http.ResponseWriter, r *http.Request) {
	var req agentStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch == "" && strings.TrimSpace(req.Dir) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "a branch (or an explicit dir) is required",
		})
		return
	}
	provider := agentsession.Provider(strings.ToLower(strings.TrimSpace(req.Provider)))
	if provider == "" {
		provider = agentsession.ProviderClaude
	}

	// Detached from the request: a client that gives up mid-fetch should not
	// abort a worktree checkout that is already underway.
	ctx, cancel := context.WithTimeout(context.Background(), provisionTimeout)
	defer cancel()

	sess, err := s.agents.Start(ctx, agentsession.StartRequest{
		Provider: provider,
		Model:    strings.TrimSpace(req.Model),
		Title:    strings.TrimSpace(req.Title),
		Repo:     strings.TrimSpace(req.Repo),
		Branch:   branch,
		Dir:      strings.TrimSpace(req.Dir),
		BaseRef:  strings.TrimSpace(req.BaseRef),
		OpenPath: strings.TrimSpace(req.OpenPath),
		Prompt:   req.Prompt,
		Force:    req.Force,
		// The lane's color is derived from the branch name, so a cockpit lane and
		// the editor title bar for the same branch agree.
		Color: model.ColorForName(branch),
	})
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "session": agentView(sess)})
}

// projectsAPI lists the repositories an agent can be started in. The cockpit
// needs it to offer "start work on this ticket", where there is a repository to
// choose but no branch yet.
func (s *Server) projectsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	type projectView struct {
		Repo string `json:"repo"`
		Dir  string `json:"dir"`
		Name string `json:"name"`
	}
	out := []projectView{}
	// One per repository, and the one provisioning would actually pick: a second
	// clone of the same repo (a ~/Code/salsa2) is not a different choice, just a
	// duplicate option that does the same thing.
	for _, p := range worktree.UniqueByRepo(s.engine.Projects()) {
		out = append(out, projectView{Repo: p.Repo, Dir: p.Dir, Name: filepath.Base(p.Dir)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": out})
}

// agentsSub handles /api/agents/{id} and its sub-resources.
func (s *Server) agentsSub(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		s.agentsUnavailable(w)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/agents/"), "/")
	id, action, _ := strings.Cut(rest, "/")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "session id required"})
		return
	}
	if strings.Contains(action, "/") {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such endpoint"})
		return
	}

	switch action {
	case "":
		switch r.Method {
		case http.MethodGet:
			sess, err := s.agents.Get(id)
			if err != nil {
				writeJSON(w, agentErrorStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session": agentView(sess)})
		case http.MethodDelete:
			if err := s.agents.Delete(id); err != nil {
				writeJSON(w, agentErrorStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	case "events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.agentStream(w, r, id)
	case "turn":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req agentTurnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
			return
		}
		turn := s.agents.Turn
		if req.Force {
			turn = s.agents.TurnForce
		}
		if err := turn(id, req.Prompt); err != nil {
			writeAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	case "stop":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := s.agents.Stop(id); err != nil {
			writeJSON(w, agentErrorStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case "promote":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.agentPromote(w, id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no such endpoint"})
	}
}

// agentPromote hands a session's worktree over to an editor window.
//
// The daemon does the half only it can do: stop the agent and write HANDOFF.md
// beside the code. Opening the window and pre-filling the chat box are the
// client's, because both happen on the machine the user is sitting at -- wsm
// listens on that box's loopback, and a cursor:// URL only means anything to the
// browser that navigates it.
func (s *Server) agentPromote(w http.ResponseWriter, id string) {
	sess, path, err := s.agents.Handoff(id)
	if err != nil {
		writeJSON(w, agentErrorStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"session":     agentView(sess),
		"handoffPath": path,
		"dir":         sess.Dir,
		// The worktree lives on the docentd box, so the link is to this host's
		// ssh alias -- the same one the dashboard's open action uses.
		"deepLink": s.engine.PathDeepLink(sess.Dir),
		"sshHost":  s.cfg.SSHHost,
		"prompt":   agentsession.PromptText(sess),
	})
}

// agentStream is the SSE transcript for one session.
//
// Unlike the report stream, this does not end at the first terminal event: a
// session outlives any single turn, so the stream stays open across turns and
// closes only when the client goes away. That is what lets a cockpit lane stay
// attached while you send a follow-up.
func (s *Server) agentStream(w http.ResponseWriter, r *http.Request, id string) {
	replay, ch, cancel, err := s.agents.Subscribe(id)
	if err != nil {
		writeJSON(w, agentErrorStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer cancel()

	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this an intervening proxy buffers the stream and the transcript
	// arrives in one lump at the end, which defeats the point.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	writeEvent := func(ev agentsession.Event) bool {
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

	for _, ev := range replay {
		if !writeEvent(ev) {
			return
		}
	}

	// A comment frame on an interval keeps idle connections from being reaped
	// by a proxy or a laptop sleeping. An agent session is idle most of the
	// time -- that is what "waiting for you" means -- so this is the common case.
	ticker := time.NewTicker(agentKeepalive)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			if !writeEvent(ev) {
				return
			}
		}
	}
}

const agentKeepalive = 25 * time.Second

func (s *Server) agentsUnavailable(w http.ResponseWriter) {
	msg := "agent sessions are unavailable"
	if s.agentsErr != nil {
		msg += ": " + s.agentsErr.Error()
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": msg})
}

// writeAgentError renders a failed agent request. The two refusals the user can
// overrule are named, so the client can offer the override rather than leaving a
// dead end: a foreign agent's claim on the worktree, which docent only inferred
// from an editor's own reporting, and a branch that has forked from the
// developer's copy, which docent declines to reconcile on its own.
func writeAgentError(w http.ResponseWriter, err error) {
	body := map[string]any{"ok": false, "error": err.Error()}
	switch {
	case errors.Is(err, agentsession.ErrForeignAgent):
		body["conflict"] = "foreign-agent"
	case errors.Is(err, agentsession.ErrDiverged):
		body["conflict"] = "diverged"
	}
	writeJSON(w, agentErrorStatus(err), body)
}

// agentErrorStatus maps the manager's sentinel errors onto status codes, so a
// client can tell "you asked for something that does not exist" from "the
// worktree is busy" from "this broke".
func agentErrorStatus(err error) int {
	switch {
	case errors.Is(err, agentsession.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, agentsession.ErrBusy),
		errors.Is(err, agentsession.ErrForeignAgent),
		errors.Is(err, agentsession.ErrDiverged):
		return http.StatusConflict
	case errors.Is(err, agentsession.ErrNoRunner):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

// agentSessionView is the JSON shape the dashboard reads. It mirrors the stored
// record minus anything only the daemon needs.
type agentSessionView struct {
	ID         string                   `json:"id"`
	Provider   string                   `json:"provider"`
	Model      string                   `json:"model,omitempty"`
	Title      string                   `json:"title,omitempty"`
	Repo       string                   `json:"repo,omitempty"`
	Branch     string                   `json:"branch,omitempty"`
	Dir        string                   `json:"dir,omitempty"`
	Project    string                   `json:"project,omitempty"`
	Color      string                   `json:"color,omitempty"`
	Status     string                   `json:"status"`
	Error      string                   `json:"error,omitempty"`
	Turns      int                      `json:"turns"`
	LastResult *agentsession.TurnResult `json:"lastResult,omitempty"`
	CreatedAt  string                   `json:"createdAt"`
	UpdatedAt  string                   `json:"updatedAt"`
}

func agentView(s agentsession.Session) agentSessionView {
	return agentSessionView{
		ID: s.ID, Provider: string(s.Provider), Model: s.Model, Title: s.Title,
		Repo: s.Repo, Branch: s.Branch, Dir: s.Dir, Project: s.Project,
		Color: s.Color, Status: string(s.Status), Error: s.Error, Turns: s.Turns,
		LastResult: s.LastResult,
		CreatedAt:  s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:  s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func agentViews(all []agentsession.Session) []agentSessionView {
	out := make([]agentSessionView, 0, len(all))
	for _, s := range all {
		out = append(out, agentView(s))
	}
	return out
}
