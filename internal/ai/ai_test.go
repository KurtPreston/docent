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
