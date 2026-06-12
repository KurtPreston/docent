package ai

import (
	"context"
	"strings"
	"testing"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

func TestSplitJiraKey(t *testing.T) {
	cases := []struct {
		title    string
		wantKey  string
		wantRest string
	}{
		{"[SALSA-12621] Adding shouldComponentUpdate", "SALSA-12621", "Adding shouldComponentUpdate"},
		{"SALSA-12483 Fix chart alignment", "SALSA-12483", "Fix chart alignment"},
		{"SALSA-12494: Jira transition comment", "SALSA-12494", "Jira transition comment"},
		{"TANGO-7", "TANGO-7", ""},
		{"Update README", "", "Update README"},
		{"bump deps (no ticket)", "", "bump deps (no ticket)"},
	}
	for _, tc := range cases {
		key, rest := splitJiraKey(tc.title)
		if key != tc.wantKey || rest != tc.wantRest {
			t.Errorf("splitJiraKey(%q) = (%q, %q), want (%q, %q)", tc.title, key, rest, tc.wantKey, tc.wantRest)
		}
	}
}

func prItem(repo, title, url string, ready bool) collectors.StatusItem {
	r := "false"
	if ready {
		r = "true"
	}
	return collectors.StatusItem{
		Repository: repo,
		Source:     "github",
		Kind:       "pr_review_status",
		Title:      title,
		URL:        url,
		Fields:     map[string]string{"ready": r},
	}
}

func TestRenderPRsMarkdownBucketsAndFormatting(t *testing.T) {
	in := RunInput{
		ModeID: "prs",
		Statuses: []collectors.StatusItem{
			prItem("Chip/salsa", "[SALSA-1] Ready one", "https://example.com/pull/1", true),
			prItem("Chip/salsa", "WIP no ticket", "https://example.com/pull/2", false),
			prItem("Chip/salsa", "SALSA-3: draft work", "https://example.com/pull/3", false),
		},
	}
	md := RenderPRsMarkdown(in)

	if !strings.HasPrefix(md, "# PRs\n") {
		t.Fatalf("expected H1 PRs:\n%s", md)
	}
	readyIdx := strings.Index(md, "Ready for review:")
	wipIdx := strings.Index(md, "Work in progress:")
	if readyIdx < 0 || wipIdx < 0 || readyIdx > wipIdx {
		t.Fatalf("expected both sections in order:\n%s", md)
	}

	readySection := md[readyIdx:wipIdx]
	if !strings.Contains(readySection, "- [SALSA-1](https://example.com/pull/1) Ready one") {
		t.Fatalf("ready bullet wrong:\n%s", readySection)
	}
	if strings.Contains(readySection, "pull/2") || strings.Contains(readySection, "pull/3") {
		t.Fatalf("non-ready PRs leaked into ready section:\n%s", readySection)
	}

	wipSection := md[wipIdx:]
	if !strings.Contains(wipSection, "- [WIP no ticket](https://example.com/pull/2)") {
		t.Fatalf("expected no-ticket bullet to use title as link text:\n%s", wipSection)
	}
	if !strings.Contains(wipSection, "- [SALSA-3](https://example.com/pull/3) draft work") {
		t.Fatalf("expected stripped ticket bullet:\n%s", wipSection)
	}
}

func TestRenderPRsMarkdownEmptySections(t *testing.T) {
	md := RenderPRsMarkdown(RunInput{ModeID: "prs"})
	if !strings.Contains(md, "Ready for review:\n- _none_") {
		t.Fatalf("expected empty ready placeholder:\n%s", md)
	}
	if !strings.Contains(md, "Work in progress:\n- _none_") {
		t.Fatalf("expected empty wip placeholder:\n%s", md)
	}
}

func TestRuleBasedRunModePRs(t *testing.T) {
	md, err := RuleBasedProvider{}.RunMode(context.Background(), RunInput{
		ModeID: "prs",
		Statuses: []collectors.StatusItem{
			prItem("o/r", "[ABC-9] thing", "https://example.com/pull/9", true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "- [ABC-9](https://example.com/pull/9) thing") {
		t.Fatalf("rule-based prs render wrong:\n%s", md)
	}
}
