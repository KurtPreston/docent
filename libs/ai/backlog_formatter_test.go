package ai

import (
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/model"
)

func TestBacklogFormattingPreservesEvidence(t *testing.T) {
	event := model.Entity{
		ID: "event", Kind: "task_activity", Title: "A task",
		Coordinates: map[string]string{"backlog_board": "board", "backlog_id": "TASK-1"},
		State: map[string]string{
			"commit": "abcdef1234", "observedAt": "2026-01-02T12:00:00Z",
			"changes": `{"status":{"before":"In Progress","after":"Done"},"section: final summary":{"before":"","after":"Delivered reports."}}`,
		},
	}
	state := model.Entity{ID: "state", Kind: "task", Coordinates: event.Coordinates, State: map[string]string{"status": "In Progress"}}
	items := []model.WorkItem{{Key: "backlog:board:TASK-1", Title: "A task", Entities: []model.Entity{event, state}}}
	for _, formatter := range []ActivityFormatter{RepoChronologicalFormatter{}, JSONSignalListFormatter{}} {
		body, err := FormatWorkItems(items, formatter)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Delivered reports.", "In Progress", "Done", "abcdef1234", "2026-01-02T12:00:00Z"} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q from %s", want, body)
			}
		}
		if formatter.Name() == "repo-chronological" && !strings.Contains(body, "current board state (not activity)") {
			t.Fatalf("state mislabeled as past work: %s", body)
		}
	}
	event.State["changes"] = "invalid"
	if _, err := formatBacklogEntity(event); err == nil {
		t.Fatal("invalid evidence silently rendered")
	}
}
