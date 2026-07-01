package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/libs/config/userdata"
)

// giteaTestRequest captures one inbound request the collector made so the
// test can assert what was queried.
type giteaTestRequest struct {
	Path  string
	Query url.Values
	Token string
}

// giteaTestState holds the captured request log and dispatches to the
// per-test handler.
type giteaTestState struct {
	mu       sync.Mutex
	requests []giteaTestRequest
	handler  func(req giteaTestRequest) (int, interface{})
}

func (s *giteaTestState) snapshot() []giteaTestRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]giteaTestRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func newGiteaServer(t *testing.T, handler func(req giteaTestRequest) (int, interface{})) (*httptest.Server, *giteaTestState) {
	t.Helper()
	state := &giteaTestState{handler: handler}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "token ")
		req := giteaTestRequest{Path: r.URL.Path, Query: r.URL.Query(), Token: auth}
		state.mu.Lock()
		state.requests = append(state.requests, req)
		state.mu.Unlock()
		status, body := state.handler(req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body == nil {
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

func newGiteaDirective(baseURL string, configExtras map[string]string) userdata.Directive {
	cfg := map[string]string{"base_url": baseURL}
	for k, v := range configExtras {
		cfg[k] = v
	}
	// Set the env var the credential ref points at via the directive's
	// userdataDir — but the directive uses userdata.ResolveEnv which
	// reads process env, so we set the env var directly in each test.
	return userdata.Directive{
		ID:        "gitea",
		Name:      "Gitea",
		Collector: "gitea",
		Enabled:   true,
		Target:    map[string]string{"owner": "alice"},
		Config:    cfg,
		CredentialRefs: map[string]string{
			"token": "SLAKKR_GITEA_TEST_TOKEN",
		},
	}
}

// authoredIssueRow returns a literal-built giteaIssue without referencing
// the inline anonymous structs by name.
func authoredIssueRow(number int, title, repo, updated, login string, isPR bool) giteaIssue {
	row := giteaIssue{
		Number:  number,
		Title:   title,
		State:   "open",
		HTMLURL: "https://gitea.example/" + repo + "/issues/" + fmtInt(number),
		Updated: updated,
	}
	row.User.Login = login
	parts := strings.SplitN(repo, "/", 2)
	row.Repository.FullName = repo
	if len(parts) == 2 {
		row.Repository.Owner = parts[0]
		row.Repository.Name = parts[1]
	} else {
		row.Repository.Name = repo
	}
	if isPR {
		row.PullRequest = &struct{}{}
	}
	return row
}

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func TestGiteaCollectScopeSelfQueriesAuthoredOnly(t *testing.T) {
	t.Setenv("SLAKKR_GITEA_TEST_TOKEN", "fake-token")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	srv, state := newGiteaServer(t, func(req giteaTestRequest) (int, interface{}) {
		switch {
		case strings.HasSuffix(req.Path, "/api/v1/users/alice/repos"):
			return http.StatusOK, []giteaRepo{{
				Name: "demo", FullName: "alice/demo",
				Updated: "2026-05-13T12:00:00Z", DefaultBranch: "main",
			}}
		case strings.HasSuffix(req.Path, "/api/v1/orgs/alice/repos"):
			return http.StatusOK, []giteaRepo{}
		case strings.HasSuffix(req.Path, "/api/v1/repos/issues/search"):
			if req.Query.Get("created_by") == "alice" && req.Query.Get("type") == "issues" {
				return http.StatusOK, []giteaIssue{authoredIssueRow(1, "My bug", "alice/demo", "2026-05-13T12:00:00Z", "alice", false)}
			}
			if req.Query.Get("created_by") == "alice" && req.Query.Get("type") == "pulls" {
				return http.StatusOK, []giteaIssue{authoredIssueRow(2, "My PR", "alice/demo", "2026-05-13T13:00:00Z", "alice", true)}
			}
			return http.StatusOK, []giteaIssue{}
		}
		return http.StatusNotFound, map[string]string{"error": "unknown path " + req.Path}
	})

	c := GiteaCollector{Clock: func() time.Time { return now }}
	directive := newGiteaDirective(srv.URL, nil)
	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: now.Add(-7 * 24 * time.Hour),
		Until: now,
		Scope: ScopeSelf,
	})
	if err != nil {
		t.Fatal(err)
	}

	gotIssueQueries := issueSearchQueriesFor(state.snapshot())
	wantSelfOnly := []string{
		"created_by=alice&state=all&type=issues",
		"created_by=alice&state=all&type=pulls",
	}
	if !sameStringSets(gotIssueQueries, wantSelfOnly) {
		t.Errorf("scope=self should only query created_by; got %v want %v", gotIssueQueries, wantSelfOnly)
	}

	if len(items) == 0 {
		t.Fatal("expected items, got none")
	}
	var issues, prs, repos int
	for _, it := range items {
		switch it.Kind {
		case "gitea_issue":
			issues++
		case "gitea_pr":
			prs++
		case "repository_updated":
			repos++
		}
		if !it.IsSelf {
			t.Errorf("scope=self items should all be IsSelf=true, got %+v", it)
		}
	}
	if issues != 1 || prs != 1 || repos != 1 {
		t.Fatalf("counts wrong: issues=%d prs=%d repos=%d items=%#v", issues, prs, repos, items)
	}
}

func TestGiteaCollectScopeInvolvedAddsAssignedAndMentioned(t *testing.T) {
	t.Setenv("SLAKKR_GITEA_TEST_TOKEN", "fake-token")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	srv, state := newGiteaServer(t, func(req giteaTestRequest) (int, interface{}) {
		switch {
		case strings.HasSuffix(req.Path, "/api/v1/users/alice/repos"),
			strings.HasSuffix(req.Path, "/api/v1/orgs/alice/repos"):
			return http.StatusOK, []giteaRepo{}
		case strings.HasSuffix(req.Path, "/api/v1/repos/issues/search"):
			return http.StatusOK, []giteaIssue{}
		}
		return http.StatusNotFound, nil
	})
	c := GiteaCollector{Clock: func() time.Time { return now }}
	directive := newGiteaDirective(srv.URL, nil)
	_, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: now.Add(-7 * 24 * time.Hour),
		Until: now,
		Scope: ScopeInvolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := issueSearchQueriesFor(state.snapshot())
	want := []string{
		"assigned_by=alice&state=all&type=issues",
		"assigned_by=alice&state=all&type=pulls",
		"created_by=alice&state=all&type=issues",
		"created_by=alice&state=all&type=pulls",
		"mentioned_by=alice&state=all&type=issues",
		"mentioned_by=alice&state=all&type=pulls",
	}
	if !sameStringSets(got, want) {
		t.Errorf("involved queries mismatch:\n got = %v\nwant = %v", got, want)
	}
}

func TestGiteaCollectScopeAllAddsFollowedRepos(t *testing.T) {
	t.Setenv("SLAKKR_GITEA_TEST_TOKEN", "fake-token")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	srv, state := newGiteaServer(t, func(req giteaTestRequest) (int, interface{}) {
		switch {
		case strings.HasSuffix(req.Path, "/api/v1/users/alice/repos"),
			strings.HasSuffix(req.Path, "/api/v1/orgs/alice/repos"):
			return http.StatusOK, []giteaRepo{}
		case strings.HasSuffix(req.Path, "/api/v1/repos/issues/search"):
			if req.Query.Get("owner") == "some-org" && req.Query.Get("repo") == "some-repo" && req.Query.Get("type") == "issues" {
				return http.StatusOK, []giteaIssue{authoredIssueRow(42, "Bug from elsewhere", "some-org/some-repo", "2026-05-13T08:00:00Z", "bob", false)}
			}
			return http.StatusOK, []giteaIssue{}
		case strings.HasSuffix(req.Path, "/api/v1/repos/some-org/some-repo"):
			return http.StatusOK, giteaRepo{
				Name: "some-repo", FullName: "some-org/some-repo",
				Updated: "2026-05-13T07:00:00Z", DefaultBranch: "main",
			}
		}
		return http.StatusNotFound, nil
	})
	c := GiteaCollector{Clock: func() time.Time { return now }}
	directive := newGiteaDirective(srv.URL, map[string]string{
		"followed_repos": "some-org/some-repo",
	})
	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: now.Add(-7 * 24 * time.Hour),
		Until: now,
		Scope: ScopeAll,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := issueSearchQueriesFor(state.snapshot())
	wantSubset := []string{
		"created_by=alice&state=all&type=issues",
		"owner=some-org&repo=some-repo&state=all&type=issues",
		"owner=some-org&repo=some-repo&state=all&type=pulls",
	}
	for _, w := range wantSubset {
		if !containsString(got, w) {
			t.Errorf("missing expected query %q in %v", w, got)
		}
	}

	var bobItem *StatusItem
	var followedRepoItem *StatusItem
	for i := range items {
		if strings.Contains(items[i].Title, "Bug from elsewhere") {
			bobItem = &items[i]
		}
		if items[i].Kind == "repository_updated" && items[i].Repository == "some-org/some-repo" {
			followedRepoItem = &items[i]
		}
	}
	if bobItem == nil {
		t.Fatal("expected bob's repo-scoped issue to surface as item")
	}
	if bobItem.IsSelf {
		t.Errorf("repo-scoped issue authored by bob should be IsSelf=false, got %+v", bobItem)
	}
	if followedRepoItem == nil {
		t.Fatal("expected repository_updated item for followed repo")
	}
}

func TestGiteaCollectDedupesAcrossUserQueries(t *testing.T) {
	t.Setenv("SLAKKR_GITEA_TEST_TOKEN", "fake-token")
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	row := authoredIssueRow(7, "Recurring item", "alice/demo", "2026-05-13T12:00:00Z", "alice", false)
	srv, _ := newGiteaServer(t, func(req giteaTestRequest) (int, interface{}) {
		switch {
		case strings.HasSuffix(req.Path, "/api/v1/users/alice/repos"),
			strings.HasSuffix(req.Path, "/api/v1/orgs/alice/repos"):
			return http.StatusOK, []giteaRepo{}
		case strings.HasSuffix(req.Path, "/api/v1/repos/issues/search"):
			if req.Query.Get("type") == "issues" {
				return http.StatusOK, []giteaIssue{row}
			}
			return http.StatusOK, []giteaIssue{}
		}
		return http.StatusNotFound, nil
	})
	c := GiteaCollector{Clock: func() time.Time { return now }}
	directive := newGiteaDirective(srv.URL, nil)
	items, err := c.CollectEvents(context.Background(), directive, &CollectOpts{
		Since: now.Add(-7 * 24 * time.Hour),
		Until: now,
		Scope: ScopeInvolved,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, it := range items {
		if it.Kind == "gitea_issue" && it.Fields["number"] == "7" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("expected exactly one deduped issue row, got %d", found)
	}
}

// issueSearchQueriesFor returns sorted, normalized query strings issued
// against the /repos/issues/search endpoint. limit and since are stripped
// so tests assert on the meaningful filters.
func issueSearchQueriesFor(reqs []giteaTestRequest) []string {
	var out []string
	for _, r := range reqs {
		if !strings.HasSuffix(r.Path, "/api/v1/repos/issues/search") {
			continue
		}
		filtered := url.Values{}
		for k, vs := range r.Query {
			if k == "limit" || k == "since" {
				continue
			}
			for _, v := range vs {
				filtered.Add(k, v)
			}
		}
		out = append(out, filtered.Encode())
	}
	sort.Strings(out)
	return out
}

func sameStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	want := map[string]struct{}{}
	for _, s := range b {
		want[s] = struct{}{}
	}
	for _, s := range a {
		if _, ok := want[s]; !ok {
			return false
		}
	}
	return true
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
