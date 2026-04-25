package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestBuildPromptIncludesBoundedContextAndSecretWarning(t *testing.T) {
	prompt, err := BuildPrompt("Plan today.", PlanningInput{
		Date: time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		Tasks: []userdata.Task{{
			ID:        "task-one",
			ProjectID: "project-one",
			Name:      "Task One",
			Status:    userdata.TaskStatusReady,
			Priority:  userdata.PriorityHigh,
		}},
		Statuses: []collectors.StatusItem{{
			DirectiveID: "status",
			Source:      "manual",
			Title:       "Manual",
			Summary:     "Needs review",
		}},
	})
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if !strings.Contains(prompt, "Never include credentials") {
		t.Fatalf("prompt should include secret boundary: %s", prompt)
	}
	if !strings.Contains(prompt, "task-one") {
		t.Fatalf("prompt should include task context: %s", prompt)
	}
}

func TestParsePlanningOutputExtractsJSONObject(t *testing.T) {
	output, err := ParsePlanningOutput([]byte("text\n{\"summary\":\"ok\",\"questions\":[\"q\"]}\nmore"))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if output.Summary != "ok" || len(output.Questions) != 1 {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestParsePlanningOutputAcceptsStringPrimaryFocus(t *testing.T) {
	output, err := ParsePlanningOutput([]byte("{\"summary\":\"ok\",\"primary_focus\":\"Ship collector fixes\"}"))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if output.PrimaryFocus == nil || output.PrimaryFocus.Title != "Ship collector fixes" {
		t.Fatalf("unexpected primary focus: %#v", output.PrimaryFocus)
	}
}

func TestParsePlanningOutputAcceptsStringSecondaryFocusList(t *testing.T) {
	output, err := ParsePlanningOutput([]byte("{\"summary\":\"ok\",\"secondary_focus\":[\"Cleanup tasks\",{\"title\":\"Review PRs\",\"reason\":\"requested\"}]}"))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.SecondaryFocus) != 2 {
		t.Fatalf("unexpected secondary focus count: %#v", output.SecondaryFocus)
	}
	if output.SecondaryFocus[0].Title != "Cleanup tasks" {
		t.Fatalf("unexpected first secondary focus: %#v", output.SecondaryFocus[0])
	}
	if output.SecondaryFocus[1].Title != "Review PRs" {
		t.Fatalf("unexpected second secondary focus: %#v", output.SecondaryFocus[1])
	}
}
