package collectors

import (
	"testing"
	"time"
)

func TestBuildJiraActivityJQL(t *testing.T) {
	since := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	if got := buildJiraActivityJQL("", since); got != `updated >= "2026-04-01" ORDER BY updated DESC` {
		t.Fatalf("empty base: %q", got)
	}
	if got := buildJiraActivityJQL(`assignee = currentUser()`, since); got != `(assignee = currentUser()) AND updated >= "2026-04-01" ORDER BY updated DESC` {
		t.Fatalf("wrapped: %q", got)
	}
}
