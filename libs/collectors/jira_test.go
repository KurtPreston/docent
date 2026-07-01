package collectors

import (
	"strings"
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
