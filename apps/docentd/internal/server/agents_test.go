package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/agentsession"
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

// A start into a worktree an editor's agent is already working in is refused,
// and the refusal says so in a way the client can act on: the cockpit offers
// "start anyway" off the conflict marker, not off the message text.
func TestStartIsRefusedWhileAnEditorAgentHoldsTheWorktree(t *testing.T) {
	h := newTestServer(t, "")
	const dir = "/home/k/Code/salsa/SALSA-1"
	rr := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","path":"`+dir+`","event":"agent_request_sent"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("session event: %d\n%s", rr.Code, rr.Body.String())
	}

	rr = doJSON(t, h, http.MethodPost, "/api/agents", "", `{"dir":"`+dir+`","prompt":"hi"}`)
	if rr.Code != http.StatusConflict {
		t.Fatalf("start = %d, want 409\n%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Error    string `json:"error"`
		Conflict string `json:"conflict"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Conflict != "foreign-agent" {
		t.Errorf("conflict = %q, want foreign-agent", got.Conflict)
	}
	if !strings.Contains(got.Error, "cursor") {
		t.Errorf("error should name what is running there, got %q", got.Error)
	}

	// Once that agent reports it is done, the worktree is available again.
	rr = doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","path":"`+dir+`","event":"agent_response_received"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("session event: %d\n%s", rr.Code, rr.Body.String())
	}
	rr = doJSON(t, h, http.MethodPost, "/api/agents", "", `{"dir":"`+dir+`"}`)
	if rr.Code == http.StatusConflict {
		t.Fatalf("still refused after the agent stopped\n%s", rr.Body.String())
	}
}

// With no local-git roots configured there are no projects, but the
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

// The picker binds straight to this, and an unknown repository is the normal
// state while somebody is still typing -- so it answers with the one placement
// that never depends on anything being on disk rather than with an error.
func TestWorktreeTargetsAlwaysOffersTheIsolatedOne(t *testing.T) {
	h := newTestServer(t, "")
	rr := doJSON(t, h, http.MethodGet, "/api/worktree-targets?repo=Chip%2Fsalsa&branch=salsa-1%2Ffix", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/worktree-targets: %d\n%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Targets []struct {
			Kind    string `json:"kind"`
			Dir     string `json:"dir"`
			Label   string `json:"label"`
			Owned   bool   `json:"owned"`
			Default bool   `json:"default"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("targets = %+v, want just the isolated one", got.Targets)
	}
	only := got.Targets[0]
	if only.Kind != "isolated" || !only.Default || !only.Owned || only.Dir == "" || only.Label == "" {
		t.Errorf("target = %+v", only)
	}
}

// An incomplete question gets an empty list rather than null: the picker maps
// over it directly.
func TestWorktreeTargetsIsAlwaysAList(t *testing.T) {
	h := newTestServer(t, "")
	rr := doJSON(t, h, http.MethodGet, "/api/worktree-targets?repo=Chip%2Fsalsa", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/worktree-targets: %d", rr.Code)
	}
	var got struct {
		Targets []struct{} `json:"targets"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Targets == nil {
		t.Fatal("targets is null, want an empty list")
	}
}

// The two refusals a user can overrule have to reach the client as a marker
// rather than as prose, since that is what the "start anyway" button keys off.
func TestOverridableRefusalsCarryAConflictMarker(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"foreign agent", fmt.Errorf("%w: cursor", agentsession.ErrForeignAgent), "foreign-agent"},
		{"diverged", fmt.Errorf("%w: 1 ahead", agentsession.ErrDiverged), "diverged"},
		{"busy", fmt.Errorf("%w (session x)", agentsession.ErrBusy), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			writeAgentError(rr, tc.err)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rr.Code)
			}
			var got struct {
				Conflict string `json:"conflict"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Conflict != tc.want {
				t.Errorf("conflict = %q, want %q", got.Conflict, tc.want)
			}
		})
	}
}
