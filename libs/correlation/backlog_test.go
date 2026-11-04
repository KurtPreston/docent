package correlation

import (
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

func TestBacklogGroupingKeepsBoardIdentityAndHistory(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	signals := []model.Signal{
		{Source: "backlog", DirectiveID: "one", Kind: "task_activity", StableID: "event-one",
			Title: "TASK-1 old title", ObservedAt: now, Fields: map[string]string{"id": "TASK-1", "status": "Done"}},
		{Source: "backlog", DirectiveID: "one", Kind: "task", StableID: "state-one",
			Title: "TASK-1 current title", Fields: map[string]string{"id": "TASK-1", "status": "In Progress", "repo": "my-org/repo", "branch": "TASK-1", "ticket": "TASK-1"}},
		{Source: "backlog", DirectiveID: "two", Kind: "task", StableID: "state-two",
			Title: "TASK-1 unrelated", Fields: map[string]string{"id": "TASK-1"}},
		{Source: "jira", Kind: "ticket", StableID: "jira-one", Title: "TASK-1 external ticket", Fields: map[string]string{"ticket": "TASK-1"}},
	}
	cfg := Config{AllowGeneric: true}
	items := BuildWorkItems(SignalsToEntities(signals, cfg), cfg)
	if len(items) != 3 {
		t.Fatalf("board IDs collided with another board or JIRA: %+v", items)
	}
	for _, item := range items {
		if item.Key != "backlog:one:TASK-1" {
			continue
		}
		if len(item.Entities) != 2 || len(item.Tickets) != 0 || item.OpenPath != "" || item.Repo != "" {
			t.Fatalf("incorrect anchors: %+v", item)
		}
		if item.Title != "TASK-1 current title" || item.LastActivity != now.Format(time.RFC3339Nano) {
			t.Fatalf("current state changed activity time or title: %+v", item)
		}
		return
	}
	t.Fatal("board task missing")
}
