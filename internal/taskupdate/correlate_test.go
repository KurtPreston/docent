package taskupdate

import (
	"testing"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestDeterministicTaskMatchURL(t *testing.T) {
	tasks := []userdata.Task{{
		ID:        "t1",
		ProjectID: "p1",
		Name:      "My task",
		Status:    userdata.TaskStatusInProgress,
		Priority:  userdata.PriorityHigh,
		Links: []userdata.Link{{
			Type: "jira",
			URL:  "https://jira.example/browse/ABC-1",
		}},
	}}
	n := NormalizedSignal{
		ID:        "sig-x",
		Source:    "jira",
		Kind:      "issue",
		URL:       "https://jira.example/browse/ABC-1",
		ProjectID: "p1",
	}
	if id := DeterministicTaskMatch(n, tasks); id != "t1" {
		t.Fatalf("got %q", id)
	}
}

func TestDoneTaskNotMatched(t *testing.T) {
	tasks := []userdata.Task{{
		ID:        "t1",
		ProjectID: "p1",
		Name:      "X",
		Status:    userdata.TaskStatusDone,
		Priority:  userdata.PriorityHigh,
		Links: []userdata.Link{{
			Type: "jira",
			URL:  "https://jira.example/browse/ABC-1",
		}},
	}}
	n := NormalizedSignal{URL: "https://jira.example/browse/ABC-1", ProjectID: "p1"}
	if id := DeterministicTaskMatch(n, tasks); id != "" {
		t.Fatalf("expected no match, got %q", id)
	}
}
