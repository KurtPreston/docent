package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/model"
)

func TestBuildPromptIncludesGuardrailsAndActivity(t *testing.T) {
	prompt, err := BuildPrompt("Plan.", RunInput{
		Now:   time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since: time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		Statuses: []collectors.StatusItem{{
			DirectiveID: "d1",
			Source:      "local-git",
			Kind:        "commit",
			Title:       "fix",
			Summary:     "abc",
			ObservedAt:  time.Unix(1, 0).UTC(),
		}},
	}, RepoChronologicalFormatter{HeadingLevel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Never include credentials") {
		t.Fatal(prompt)
	}
	if !strings.Contains(prompt, "fix") {
		t.Fatal(prompt)
	}
}

func TestBuildPromptOmitsLookbackWhenZero(t *testing.T) {
	prompt, err := BuildPrompt("Plan.", RunInput{
		Now:   time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since: time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
	}, RepoChronologicalFormatter{HeadingLevel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, "Lookback:") {
		t.Fatalf("expected no lookback line for non-days window:\n%s", prompt)
	}
}

func TestBuildPromptIncludesLookbackWhenSet(t *testing.T) {
	prompt, err := BuildPrompt("Plan.", RunInput{
		Now:          time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since:        time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		LookbackDays: 7,
	}, RepoChronologicalFormatter{HeadingLevel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "Lookback: 7 calendar day(s)") {
		t.Fatalf("expected lookback line:\n%s", prompt)
	}
}

func TestRuleBasedRunModeRecentActivity(t *testing.T) {
	md, err := RuleBasedProvider{}.RunMode(context.Background(), RunInput{
		ModeID:       "recent-activity",
		Now:          time.Unix(1000, 0).UTC(),
		Since:        time.Unix(0, 0).UTC(),
		LookbackDays: 7,
		WorkItems: []model.WorkItem{{
			Key:          "wb:p1@main",
			Title:        "main",
			Repo:         "p1",
			Branch:       "main",
			LastActivity: "2026-05-05T12:00:00Z",
			Entities: []model.Entity{
				{Kind: "commit", Title: "x"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "p1") {
		t.Fatal(md)
	}
	if !strings.Contains(md, "## main") {
		t.Fatalf("expected work-item heading:\n%s", md)
	}
}

func TestRuleBasedRunModeDailyPlan(t *testing.T) {
	md, err := RuleBasedProvider{}.RunMode(context.Background(), RunInput{
		ModeID:       "daily-plan",
		Now:          time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since:        time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		PrevDayLabel: "Thursday",
		NextDayLabel: "Friday",
		WorkItems: []model.WorkItem{{
			Key: "wb:o/r@salsa-1",
			Tickets: []model.TicketRef{{
				Key: "SALSA-1", Title: "SALSA-1 Thing", URL: "https://jira.example/browse/SALSA-1",
			}},
			Entities: []model.Entity{
				{Kind: "commit", Coordinates: map[string]string{"ticket": "SALSA-1", "branch": "salsa-1"},
					State: map[string]string{"observedAt": "2026-04-23T12:00:00Z"}},
				{Kind: "issue", Coordinates: map[string]string{"ticket": "SALSA-1"},
					State: map[string]string{"status_tier": "started", "updated": "2026-04-23T12:00:00Z"}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "*Thursday*") || !strings.Contains(md, "*Friday*") {
		t.Fatalf("expected day labels:\n%s", md)
	}
	if !strings.Contains(md, "Started [SALSA-1]") {
		t.Fatalf("expected Started line:\n%s", md)
	}
	if !strings.Contains(md, "PRs ready for review:") {
		t.Fatalf("expected ready section:\n%s", md)
	}
}

func TestRuleBasedRunModeGenericFallback(t *testing.T) {
	md, err := RuleBasedProvider{}.RunMode(context.Background(), RunInput{
		ModeID:      "repo-activity",
		ModeName:    "Repo activity",
		Now:         time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since:       time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC),
		Instruction: "Summarize repo activity.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "# Repo activity") {
		t.Fatalf("expected H1 from ModeName:\n%s", md)
	}
	if !strings.Contains(md, "## Activity") {
		t.Fatalf("expected Activity section:\n%s", md)
	}
	if !strings.Contains(md, "Summarize repo activity.") {
		t.Fatalf("expected instruction to be included:\n%s", md)
	}
}

func TestStripMarkdownFence(t *testing.T) {
	in := "```markdown\n# Hi\n```"
	if StripMarkdownFence(in) != "# Hi" {
		t.Fatal(StripMarkdownFence(in))
	}
}
