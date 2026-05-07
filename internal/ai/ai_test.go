package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

func TestBuildDailyPlanPromptBoundary(t *testing.T) {
	prompt, err := BuildDailyPlanPrompt("Plan.", DailyPlanInput{
		Now:     time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		Since:   time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC),
		Statuses: []collectors.StatusItem{{
			DirectiveID: "d1",
			Source:      "local-git",
			Kind:        "commit",
			Title:       "fix",
			Summary:     "abc",
			ObservedAt:  time.Unix(1, 0).UTC(),
		}},
	})
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

func TestRenderRecentActivityMarkdown(t *testing.T) {
	md := RenderRecentActivityMarkdown(RecentActivityInput{
		Now:          time.Unix(1000, 0).UTC(),
		Since:        time.Unix(0, 0).UTC(),
		LookbackDays: 7,
		Statuses: []collectors.StatusItem{{
			DirectiveID: "d",
			ProjectID:   "p1",
			Source:      "local-git",
			Kind:        "commit",
			Title:       "x",
			Summary:     "y",
			ObservedAt:  time.Unix(500, 0).UTC(),
		}},
	})
	if !strings.Contains(md, "p1") {
		t.Fatal(md)
	}
}

func TestStripMarkdownFence(t *testing.T) {
	in := "```markdown\n# Hi\n```"
	if StripMarkdownFence(in) != "# Hi" {
		t.Fatal(StripMarkdownFence(in))
	}
}
