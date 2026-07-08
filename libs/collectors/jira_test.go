package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

func TestBuildJiraTierJQL(t *testing.T) {
	if got := buildJiraTierJQL(""); got != "" {
		t.Errorf("empty query should yield empty JQL, got %q", got)
	}
	got := buildJiraTierJQL(`assignee = currentUser() AND status = "In Development"`)
	if !strings.HasSuffix(got, "ORDER BY updated DESC") {
		t.Errorf("expected default ordering appended, got %q", got)
	}
	if strings.Contains(got, "watcher = currentUser()") {
		t.Errorf("tier JQL must be verbatim (no scope wrapping), got %q", got)
	}
	// An explicit ORDER BY is preserved as-is.
	custom := `status = "Done" ORDER BY created ASC`
	if got := buildJiraTierJQL(custom); got != custom {
		t.Errorf("explicit ORDER BY should be preserved, got %q", got)
	}
}

func TestBuildJiraItemStampsStatusTier(t *testing.T) {
	d := userdata.Directive{ID: "jira#started", Collector: "jira", Config: map[string]string{"status_tier": "started"}}
	var iss jiraIssue
	iss.Key = "SALSA-5"
	iss.Fields.Summary = "do the thing"
	iss.Fields.Status.Name = "In Development"
	item := buildJiraItem(d, "https://jira.example", iss, "issue", time.Now(), true)
	if item.Fields["status_tier"] != "started" {
		t.Errorf("status_tier = %q, want started", item.Fields["status_tier"])
	}
	// Without a status_tier config the field is absent.
	d2 := userdata.Directive{ID: "jira", Collector: "jira"}
	item2 := buildJiraItem(d2, "https://jira.example", iss, "issue", time.Now(), true)
	if _, ok := item2.Fields["status_tier"]; ok {
		t.Errorf("status_tier should be absent without config, got %v", item2.Fields)
	}
}

func TestBuildJiraKeyJQL(t *testing.T) {
	if got := buildJiraKeyJQL(nil); got != "" {
		t.Errorf("empty keys should yield empty JQL, got %q", got)
	}
	got := buildJiraKeyJQL([]string{"SALSA-1", "SALSA-2"})
	want := "issuekey in (SALSA-1, SALSA-2) ORDER BY updated DESC"
	if got != want {
		t.Errorf("buildJiraKeyJQL = %q, want %q", got, want)
	}
}

func TestNormalizeJiraKeys(t *testing.T) {
	got := normalizeJiraKeys([]string{" salsa-1 ", "SALSA-1", "", "salsa-2"})
	want := []string{"SALSA-1", "SALSA-2"}
	if len(got) != len(want) {
		t.Fatalf("normalizeJiraKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normalizeJiraKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// jiraTestState records the JQL and maxResults of each search the collector
// issued, so tests can assert on batching behavior.
type jiraTestState struct {
	mu    sync.Mutex
	jqls  []string
	maxes []string
}

func (s *jiraTestState) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jqls := append([]string(nil), s.jqls...)
	maxes := append([]string(nil), s.maxes...)
	return jqls, maxes
}

func newJiraSearchServer(t *testing.T, issuesFor func(jql string) []jiraIssue) (*httptest.Server, *jiraTestState) {
	t.Helper()
	st := &jiraTestState{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rest/api/2/search") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		q := r.URL.Query()
		st.mu.Lock()
		st.jqls = append(st.jqls, q.Get("jql"))
		st.maxes = append(st.maxes, q.Get("maxResults"))
		st.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jiraSearchResult{Issues: issuesFor(q.Get("jql"))})
	}))
	t.Cleanup(srv.Close)
	return srv, st
}

// jiraIssueRow builds a jiraIssue without naming its inline anonymous structs.
func jiraIssueRow(key, summary, status, updated string) jiraIssue {
	var iss jiraIssue
	iss.Key = key
	iss.Fields.Summary = summary
	iss.Fields.Status.Name = status
	iss.Fields.Updated = updated
	return iss
}

// keysInJQL extracts the comma-separated keys inside `issuekey in (...)`.
func keysInJQL(jql string) []string {
	openIdx := strings.Index(jql, "(")
	closeIdx := strings.Index(jql, ")")
	if openIdx < 0 || closeIdx < 0 || closeIdx < openIdx {
		return nil
	}
	var out []string
	for _, p := range strings.Split(jql[openIdx+1:closeIdx], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newJiraByKeyDirective(baseURL string) userdata.Directive {
	return userdata.Directive{
		ID:             "jira",
		Collector:      "jira",
		Enabled:        true,
		Config:         map[string]string{"base_url": baseURL},
		CredentialRefs: map[string]string{"pat": "DOCENT_JIRA_TEST_PAT"},
	}
}

func TestJiraResolveRefsMapsIssues(t *testing.T) {
	t.Setenv("DOCENT_JIRA_TEST_PAT", "fake-pat")
	srv, state := newJiraSearchServer(t, func(jql string) []jiraIssue {
		var out []jiraIssue
		for _, k := range keysInJQL(jql) {
			out = append(out, jiraIssueRow(k, "Publish thing", "Code Review", "2026-07-07T14:54:45.000-0500"))
		}
		return out
	})
	c := JiraCollector{Clock: time.Now}
	items, err := c.ResolveRefs(context.Background(), newJiraByKeyDirective(srv.URL), &CollectOpts{}, []string{"SALSA-12430"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Kind != "issue" {
		t.Errorf("kind = %q, want issue", it.Kind)
	}
	if it.Title != "SALSA-12430 Publish thing" {
		t.Errorf("title = %q, want %q", it.Title, "SALSA-12430 Publish thing")
	}
	if it.Fields["status"] != "Code Review" {
		t.Errorf("status field = %q, want Code Review", it.Fields["status"])
	}
	if _, ok := it.Fields["status_tier"]; ok {
		t.Errorf("annotation signals must omit status_tier, got %v", it.Fields)
	}
	if it.IsSelf {
		t.Error("annotation signals should be IsSelf=false")
	}
	if !strings.HasSuffix(it.URL, "/browse/SALSA-12430") {
		t.Errorf("url = %q, want a /browse link", it.URL)
	}
	jqls, _ := state.snapshot()
	if len(jqls) != 1 || !strings.Contains(jqls[0], "issuekey in (SALSA-12430)") {
		t.Errorf("jqls = %v, want a single issuekey-in query", jqls)
	}
}

func TestJiraResolveRefsChunks(t *testing.T) {
	t.Setenv("DOCENT_JIRA_TEST_PAT", "fake-pat")
	srv, state := newJiraSearchServer(t, func(jql string) []jiraIssue {
		var out []jiraIssue
		for _, k := range keysInJQL(jql) {
			out = append(out, jiraIssueRow(k, "s", "To Do", "2026-07-07T14:54:45.000-0500"))
		}
		return out
	})
	c := JiraCollector{Clock: time.Now}
	keys := make([]string, 0, 51)
	for i := 0; i < 51; i++ {
		keys = append(keys, fmt.Sprintf("SALSA-%d", i+1))
	}
	items, err := c.ResolveRefs(context.Background(), newJiraByKeyDirective(srv.URL), &CollectOpts{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 51 {
		t.Fatalf("expected 51 items, got %d", len(items))
	}
	jqls, maxes := state.snapshot()
	if len(jqls) != 2 {
		t.Fatalf("expected 2 batched requests, got %d: %v", len(jqls), jqls)
	}
	// Each request's maxResults matches its batch size, not the hardcoded 50.
	if maxes[0] != "50" || maxes[1] != "1" {
		t.Errorf("maxResults per batch = %v, want [50 1]", maxes)
	}
}

func TestBuildJiraActivityJQL(t *testing.T) {
	since := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	const dateStr = `2026-05-01`

	cases := []struct {
		name             string
		userQuery        string
		scope            Scope
		followedProjects []string
		wantContains     []string
		wantNotContains  []string
	}{
		{
			name:  "self drops watcher",
			scope: ScopeSelf,
			wantContains: []string{
				`(assignee = currentUser() OR reporter = currentUser())`,
				`updated >= "` + dateStr + `"`,
				`ORDER BY updated DESC`,
			},
			wantNotContains: []string{"watcher = currentUser()"},
		},
		{
			name:  "involved includes watcher",
			scope: ScopeInvolved,
			wantContains: []string{
				`(assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
				`updated >= "` + dateStr + `"`,
			},
		},
		{
			name:  "unset defaults to involved",
			scope: ScopeUnset,
			wantContains: []string{
				`(assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
			},
		},
		{
			name:             "all with followed projects",
			scope:            ScopeAll,
			followedProjects: []string{"PROJ", "OTHER"},
			wantContains: []string{
				`(project in ("PROJ", "OTHER") OR assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
				`updated >= "` + dateStr + `"`,
			},
		},
		{
			name:  "all without followed projects falls back to involved",
			scope: ScopeAll,
			wantContains: []string{
				`(assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
			},
			wantNotContains: []string{"project in"},
		},
		{
			name:      "wraps user-supplied query",
			scope:     ScopeInvolved,
			userQuery: "labels = team-foo",
			wantContains: []string{
				`(labels = team-foo)`,
				`AND (assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
				`AND updated >= "` + dateStr + `"`,
			},
		},
		{
			name:             "wraps user-supplied query with all+projects",
			scope:            ScopeAll,
			userQuery:        "priority = High",
			followedProjects: []string{"PROJ"},
			wantContains: []string{
				`(priority = High)`,
				`AND (project in ("PROJ") OR assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser())`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildJiraActivityJQL(tc.userQuery, since, tc.scope, tc.followedProjects)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("JQL missing %q\nfull: %s", want, got)
				}
			}
			for _, bad := range tc.wantNotContains {
				if strings.Contains(got, bad) {
					t.Errorf("JQL should not contain %q\nfull: %s", bad, got)
				}
			}
		})
	}
}
