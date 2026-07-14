package ai

import (
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/model"
)

func TestFormatWorkItemsMarkdown(t *testing.T) {
	items := []model.WorkItem{{
		Key:    "wb:Chip/salsa@salsa-42-fix",
		Title:  "salsa-42-fix",
		Repo:   "Chip/salsa",
		Branch: "salsa-42-fix",
		Tickets: []model.TicketRef{{
			Key:    "SALSA-42",
			Title:  "SALSA-42 Fix the widget",
			URL:    "https://jira.example/browse/SALSA-42",
			Status: "In Development",
		}},
		Entities: []model.Entity{
			{Kind: "pr_review_status", Title: "[SALSA-42] Fix the widget", URL: "https://git.example/Chip/salsa/pull/7"},
			{Kind: "commit", Title: "wip"},
			{Kind: "commit", Title: "more"},
		},
		LastActivity: "2026-05-05T10:00:00Z",
	}}
	out, err := FormatWorkItems(items, RepoChronologicalFormatter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## salsa-42-fix") {
		t.Fatalf("missing heading:\n%s", out)
	}
	if !strings.Contains(out, "Chip/salsa @ salsa-42-fix") {
		t.Fatalf("missing location:\n%s", out)
	}
	if !strings.Contains(out, "[SALSA-42](https://jira.example/browse/SALSA-42) Fix the widget [In Development]") {
		t.Fatalf("missing ticket line:\n%s", out)
	}
	if !strings.Contains(out, "pr: [[SALSA-42] Fix the widget](https://git.example/Chip/salsa/pull/7)") {
		t.Fatalf("missing pr line:\n%s", out)
	}
	if !strings.Contains(out, "commit×2") {
		t.Fatalf("missing activity tally:\n%s", out)
	}
}

func TestFormatWorkItemsJSON(t *testing.T) {
	items := []model.WorkItem{{Key: "SALSA-1", Title: "SALSA-1 thing"}}
	out, err := FormatWorkItems(items, JSONSignalListFormatter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"key": "SALSA-1"`) {
		t.Fatalf("expected JSON work item:\n%s", out)
	}
}

func TestTicketDescriptionStripsKey(t *testing.T) {
	got := ticketDescription(model.TicketRef{Key: "SALSA-1", Title: "SALSA-1 Fix widget"})
	if got != "Fix widget" {
		t.Fatalf("got %q", got)
	}
}
