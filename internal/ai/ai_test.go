package ai

import (
	"context"
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

func TestParsePlanningOutputAcceptsStringFocusBlocksList(t *testing.T) {
	output, err := ParsePlanningOutput([]byte("{\"summary\":\"ok\",\"focus_blocks\":[\"Cleanup tasks\",{\"title\":\"Review PRs\",\"reason\":\"requested\"}]}"))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.FocusBlocks) != 2 {
		t.Fatalf("unexpected focus blocks count: %#v", output.FocusBlocks)
	}
	if output.FocusBlocks[0].Title != "Cleanup tasks" {
		t.Fatalf("unexpected first focus block: %#v", output.FocusBlocks[0])
	}
	if output.FocusBlocks[1].Title != "Review PRs" {
		t.Fatalf("unexpected second focus block: %#v", output.FocusBlocks[1])
	}
}

func TestParsePlanningOutputAcceptsStringDelegationCandidatesList(t *testing.T) {
	raw := `{"summary":"ok","delegation_candidates":["Triage CI",` +
		`{"task_id":"t1","title":"Fix build","reason":"blocked","suggested_prompt":"Investigate"}]}`
	output, err := ParsePlanningOutput([]byte(raw))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.DelegationCandidates) != 2 {
		t.Fatalf("unexpected delegation_candidates count: %#v", output.DelegationCandidates)
	}
	if output.DelegationCandidates[0].Title != "Triage CI" {
		t.Fatalf("unexpected first delegation: %#v", output.DelegationCandidates[0])
	}
	if output.DelegationCandidates[1].Title != "Fix build" || output.DelegationCandidates[1].TaskID != "t1" {
		t.Fatalf("unexpected second delegation: %#v", output.DelegationCandidates[1])
	}
}

func TestParsePlanningOutputAcceptsStringAndAlternateProposedTaskChanges(t *testing.T) {
	raw := `{"summary":"ok","proposed_task_changes":[` +
		`"Add sync step to task-1",` +
		`{"task_id":"t1","field":"status","value":"in_progress","reason":"active"},` +
		`{"task_id":"t2","change":"Update title","rationale":"clarity"}` +
		`]}`
	output, err := ParsePlanningOutput([]byte(raw))
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.ProposedTaskChanges) != 3 {
		t.Fatalf("count = %d, want 3: %#v", len(output.ProposedTaskChanges), output.ProposedTaskChanges)
	}
	if output.ProposedTaskChanges[0].Value != "Add sync step to task-1" {
		t.Fatalf("first: %#v", output.ProposedTaskChanges[0])
	}
	if output.ProposedTaskChanges[1].Field != "status" || output.ProposedTaskChanges[1].Value != "in_progress" {
		t.Fatalf("second: %#v", output.ProposedTaskChanges[1])
	}
	if output.ProposedTaskChanges[2].Value != "Update title" || output.ProposedTaskChanges[2].Reason != "clarity" {
		t.Fatalf("third: %#v", output.ProposedTaskChanges[2])
	}
}

func TestRuleBasedSummarizeRecentActivityMatchesRender(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	in := RecentActivityInput{
		Now:          now,
		Since:        since,
		LookbackDays: 7,
		HostID:       "h1",
		Projects: []userdata.Project{{ID: "p1", Name: "One"}},
		Statuses: []collectors.StatusItem{
			{
				DirectiveID: "d1",
				ProjectID:   "p1",
				Source:      "local-git",
				Kind:        "commit",
				Title:       "fix stuff",
				ObservedAt:  now.Add(-time.Hour),
				Fields: map[string]string{"short_hash": "abc1234", "author": "dev"},
			},
		},
	}
	rule := RenderRecentActivityMarkdown(in)
	p := RuleBasedProvider{}
	out, err := p.SummarizeRecentActivity(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out != rule {
		t.Fatalf("mismatch\nrule:\n%s\n---\nout:\n%s", rule, out)
	}
	if !strings.Contains(out, "abc1234") || !strings.Contains(out, "p1") {
		t.Fatalf("unexpected markdown: %s", out)
	}
}
