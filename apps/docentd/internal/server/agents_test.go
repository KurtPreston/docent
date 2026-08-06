package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestAgentsListIsEmptyBeforeAnyRun(t *testing.T) {
	h := newTestServer(t, "")
	rr := doJSON(t, h, http.MethodGet, "/api/agents", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/agents: %d\n%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Sessions []agentSessionView `json:"sessions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Sessions) != 0 {
		t.Fatalf("sessions = %+v, want none", got.Sessions)
	}
}

// The sentinel errors have to survive the trip through HTTP as distinguishable
// statuses, because the cockpit treats "gone" and "busy" very differently.
func TestAgentSubRoutesReportWhatWentWrong(t *testing.T) {
	h := newTestServer(t, "")
	for _, tc := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"unknown session", http.MethodGet, "/api/agents/nope", "", http.StatusNotFound},
		{"unknown session turn", http.MethodPost, "/api/agents/nope/turn", `{"prompt":"hi"}`, http.StatusNotFound},
		{"unknown session promote", http.MethodPost, "/api/agents/nope/promote", "", http.StatusNotFound},
		{"stopping an idle session", http.MethodPost, "/api/agents/nope/stop", "", http.StatusInternalServerError},
		{"no session id", http.MethodGet, "/api/agents/", "", http.StatusBadRequest},
		{"unknown sub-resource", http.MethodGet, "/api/agents/nope/wat", "", http.StatusNotFound},
		{"nested path", http.MethodGet, "/api/agents/nope/turn/again", "", http.StatusNotFound},
		{"wrong method on turn", http.MethodGet, "/api/agents/nope/turn", "", http.StatusMethodNotAllowed},
		{"a start needs a branch", http.MethodPost, "/api/agents", `{"prompt":"hi"}`, http.StatusBadRequest},
		{"a start needs valid json", http.MethodPost, "/api/agents", `{`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doJSON(t, h, tc.method, tc.path, "", tc.body)
			if rr.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d\n%s", tc.method, tc.path, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// With no local-git roots configured there are no grove projects, but the
// endpoint must still answer with a list rather than null: the picker binds to
// it directly.
func TestProjectsAlwaysReturnsAList(t *testing.T) {
	h := newTestServer(t, "")
	rr := doJSON(t, h, http.MethodGet, "/api/projects", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/projects: %d\n%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Projects []struct{ Repo, Dir, Name string } `json:"projects"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Projects == nil {
		t.Fatal("projects is null, want an empty list")
	}
}
