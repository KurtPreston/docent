package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/model"
)

func TestRenderDailyPlanStandup(t *testing.T) {
	in := RunInput{
		ModeID:       "daily-plan",
		Since:        time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		Now:          time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
		PrevDayLabel: "Monday",
		NextDayLabel: "Tuesday",
		IsMorning:    true,
		WorkItems: []model.WorkItem{
			{
				Key:    "wb:Chip/salsa@salsa-1",
				Title:  "salsa-1",
				Repo:   "Chip/salsa",
				Branch: "salsa-1",
				Tickets: []model.TicketRef{{
					Key:    "SALSA-1",
					Title:  "SALSA-1 Collections refactor",
					URL:    "https://jira.example/browse/SALSA-1",
					Status: "In Development",
				}},
				Entities: []model.Entity{
					{
						Kind:  "pr_review_status",
						Title: "[SALSA-1] Collections refactor",
						URL:   "https://git.example/Chip/salsa/pull/10",
						Coordinates: map[string]string{
							"ticket": "SALSA-1",
							"repo":   "Chip/salsa",
						},
						State: map[string]string{
							"relation":        "authored",
							"is_draft":        "false",
							"checks":          "passing",
							"review_decision": "REVIEW_REQUIRED",
							"ready":           "true",
							"state":           "open",
						},
					},
					{Kind: "commit", Title: "wip", Coordinates: map[string]string{"ticket": "SALSA-1", "branch": "salsa-1"}},
					{Kind: "issue", Title: "SALSA-1 Collections refactor", URL: "https://jira.example/browse/SALSA-1",
						Coordinates: map[string]string{"ticket": "SALSA-1"},
						State:       map[string]string{"status": "In Development", "status_tier": "started", "status_category": "indeterminate"}},
				},
			},
			{
				Key:   "wb:Chip/salsa@salsa-2",
				Title: "salsa-2",
				Tickets: []model.TicketRef{{
					Key:    "SALSA-2",
					Title:  "SALSA-2 Skip readonly validation",
					URL:    "https://jira.example/browse/SALSA-2",
					Status: "Done",
				}},
				Entities: []model.Entity{
					{
						Kind:  "authored_pr",
						Title: "[SALSA-2] Skip readonly validation",
						URL:   "https://git.example/Chip/salsa/pull/11",
						Coordinates: map[string]string{"ticket": "SALSA-2"},
						State: map[string]string{
							"state":     "closed",
							"closed_at": "2026-05-04T18:00:00Z",
						},
					},
					{Kind: "issue", Title: "SALSA-2 Skip readonly validation",
						Coordinates: map[string]string{"ticket": "SALSA-2"},
						State:       map[string]string{"status": "Done", "status_category": "done"}},
				},
			},
			{
				// Assigned-only — should be skipped.
				Key: "SALSA-99",
				Tickets: []model.TicketRef{{
					Key: "SALSA-99", Title: "SALSA-99 Backlog item", URL: "https://jira.example/browse/SALSA-99",
				}},
				Entities: []model.Entity{{
					Kind: "issue", Title: "SALSA-99 Backlog item",
					Coordinates: map[string]string{"ticket": "SALSA-99"},
					State:       map[string]string{"status_tier": "assigned", "status": "To Do"},
				}},
			},
		},
		Statuses: []collectors.StatusItem{
			{
				Kind:  "pr_review_status",
				Title: "[SALSA-1] Collections refactor",
				URL:   "https://git.example/Chip/salsa/pull/10",
				Fields: map[string]string{
					"ready":           "true",
					"review_decision": "REVIEW_REQUIRED",
					"relation":        "authored",
				},
			},
			{
				Kind:  "pr_review_status",
				Title: "[SALSA-3] Already approved",
				URL:   "https://git.example/Chip/salsa/pull/12",
				Fields: map[string]string{
					"ready":           "true",
					"review_decision": "APPROVED",
					"relation":        "authored",
				},
			},
		},
	}

	md := RenderDailyPlanMarkdown(in, nil)

	if !strings.Contains(md, "**Monday**\n") {
		t.Fatalf("missing Monday header:\n%s", md)
	}
	if !strings.Contains(md, "- Started [SALSA-1](https://git.example/Chip/salsa/pull/10) Collections refactor") {
		t.Fatalf("missing Started line:\n%s", md)
	}
	if !strings.Contains(md, "- Finished [SALSA-2](https://git.example/Chip/salsa/pull/11) Skip readonly validation") {
		t.Fatalf("missing Finished line:\n%s", md)
	}
	if strings.Contains(md, "SALSA-99") {
		t.Fatalf("assigned-only ticket should be skipped:\n%s", md)
	}
	if !strings.Contains(md, "**Tuesday**\n") {
		t.Fatalf("missing Tuesday header:\n%s", md)
	}
	if !strings.Contains(md, "- Continue on [SALSA-1](https://git.example/Chip/salsa/pull/10) Collections refactor") {
		t.Fatalf("missing Continue on line:\n%s", md)
	}
	if strings.Contains(md, "Continue on [SALSA-2]") {
		t.Fatalf("finished ticket should not appear under Continue on:\n%s", md)
	}
	readyIdx := strings.Index(md, "PRs ready for review:")
	if readyIdx < 0 {
		t.Fatalf("missing ready section:\n%s", md)
	}
	readySection := md[readyIdx:]
	if !strings.Contains(readySection, "- [SALSA-1](https://git.example/Chip/salsa/pull/10) Collections refactor") {
		t.Fatalf("ready PR missing:\n%s", readySection)
	}
	if strings.Contains(readySection, "pull/12") {
		t.Fatalf("approved PR should not be in ready list:\n%s", readySection)
	}
}

func TestRenderDailyPlanStandupEmpty(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Friday",
		NextDayLabel: "Monday",
	}, nil)
	if !strings.Contains(md, "**Friday**\n- _none_") {
		t.Fatalf("expected empty prev placeholder:\n%s", md)
	}
	if !strings.Contains(md, "**Monday**\n- _none_") {
		t.Fatalf("expected empty next placeholder:\n%s", md)
	}
	if !strings.Contains(md, "PRs ready for review:\n- _none_") {
		t.Fatalf("expected empty ready placeholder:\n%s", md)
	}
}

func TestRenderDailyPlanSkipsUnticketed(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Monday",
		NextDayLabel: "Tuesday",
		Since:        time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		Now:          time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
		WorkItems: []model.WorkItem{
			{
				Key:   "commit:local-git:commit:abc123",
				Title: "wip without ticket",
				Entities: []model.Entity{
					{Kind: "commit", Title: "wip", Coordinates: map[string]string{"branch": "feature"}},
				},
			},
		},
	}, nil)
	if strings.Contains(md, "commit:") || strings.Contains(md, "Started") || strings.Contains(md, "Finished") {
		t.Fatalf("unticketed work item should not produce standup lines:\n%s", md)
	}
	if !strings.Contains(md, "**Monday**\n- _none_") {
		t.Fatalf("expected empty prev section:\n%s", md)
	}
}

func TestRenderDailyPlanSkipsBareBranch(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Monday",
		NextDayLabel: "Tuesday",
		Since:        time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC),
		Now:          time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC),
		WorkItems: []model.WorkItem{
			{
				Key: "wb:Chip/salsa@salsa-3",
				Tickets: []model.TicketRef{{
					Key: "SALSA-3", Title: "SALSA-3 Old ticket", URL: "https://jira.example/browse/SALSA-3",
				}},
				Entities: []model.Entity{
					{Kind: "branch", Title: "salsa-3", Coordinates: map[string]string{"ticket": "SALSA-3", "branch": "salsa-3"}},
					{Kind: "issue", Title: "SALSA-3 Old ticket", URL: "https://jira.example/browse/SALSA-3",
						Coordinates: map[string]string{"ticket": "SALSA-3"},
						State: map[string]string{
							"status_tier":     "started",
							"status_category": "indeterminate",
							"updated":         "2020-01-01T00:00:00.000+0000",
							"observedAt":      "2020-01-01T00:00:00Z",
						}},
					{Kind: "pr_review_status", Title: "[SALSA-3] Old ticket", URL: "https://git.example/pull/1",
						Coordinates: map[string]string{"ticket": "SALSA-3"},
						State: map[string]string{
							"relation":        "authored",
							"ready":           "true",
							"review_decision": "REVIEW_REQUIRED",
							"state":           "open",
							"observedAt":      "2020-01-01T00:00:00Z",
						}},
				},
			},
		},
	}, nil)
	if strings.Contains(md, "SALSA-3") && (strings.Contains(md, "Started") || strings.Contains(md, "Continue")) {
		t.Fatalf("stale branch/PR/JIRA without in-window evidence should be skipped:\n%s", md)
	}
}

func TestAuthoredPRClosedInWindow(t *testing.T) {
	since := time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	ent := model.Entity{
		Kind: "authored_pr",
		State: map[string]string{
			"state":     "closed",
			"closed_at": "2026-05-04T12:00:00Z",
		},
	}
	if !authoredPRClosedInWindow(ent, since, until) {
		t.Fatal("expected closed PR in window")
	}
	ent.State["closed_at"] = "2026-05-03T12:00:00Z"
	if authoredPRClosedInWindow(ent, since, until) {
		t.Fatal("expected closed PR outside window to be false")
	}
}
