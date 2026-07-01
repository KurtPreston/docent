package ai

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/libs/collectors"
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
		Statuses: []collectors.StatusItem{{
			DirectiveID: "d",
			Repository:  "p1",
			Source:      "local-git",
			Kind:        "commit",
			Title:       "x",
			Summary:     "y",
			ObservedAt:  time.Unix(500, 0).UTC(),
			Fields: map[string]string{
				"short_hash": "abcd123",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "p1") {
		t.Fatal(md)
	}
	if !strings.Contains(md, "local-git commit abcd123:") {
		t.Fatal(md)
	}
}

func TestRuleBasedRunModeDailyPlan(t *testing.T) {
	md, err := RuleBasedProvider{}.RunMode(context.Background(), RunInput{
		ModeID: "daily-plan",
		Now:    time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since:  time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "## Yesterday") || !strings.Contains(md, "## Today") {
		t.Fatalf("expected Yesterday/Today sections:\n%s", md)
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
