package ai

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kurt/slakkr-ai/libs/collectors"
)

// prsModeID is the built-in execution-mode id that this renderer serves.
// It mirrors executionmode.BuiltinPRs without importing that package, the
// same way the rule-based dispatch elsewhere in this file matches mode ids
// by string literal.
const prsModeID = "prs"

// jiraKeyPattern matches Jira-style ticket keys like SALSA-12483 or
// TANGO-7. Project keys are uppercase letters/digits starting with a
// letter, followed by a hyphen and the issue number.
var jiraKeyPattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)

// RenderPRsMarkdown deterministically renders the `prs` report: open PRs
// split into "Ready for review:" (not draft, checks passing) and "Work in
// progress:" sections. Each PR is a bullet linking its Jira ticket key
// (when present in the title) to the PR URL, followed by the title with
// the ticket stripped. The rendering is identical regardless of the
// configured AI provider — there is nothing for a model to infer here.
func RenderPRsMarkdown(in RunInput) string {
	var ready, wip, errs []collectors.StatusItem
	for _, s := range in.Statuses {
		switch s.Kind {
		case "pr_review_status":
			if s.Fields != nil && s.Fields["ready"] == "true" {
				ready = append(ready, s)
			} else {
				wip = append(wip, s)
			}
		case "collector_error":
			errs = append(errs, s)
		}
	}
	sortPRItems(ready)
	sortPRItems(wip)

	var b strings.Builder
	b.WriteString("# PRs\n\n")
	writePRSection(&b, "Ready for review:", ready)
	b.WriteString("\n")
	writePRSection(&b, "Work in progress:", wip)

	if len(errs) > 0 {
		b.WriteString("\nCollector errors:\n")
		for _, e := range errs {
			id := strings.TrimSpace(e.DirectiveID)
			if id == "" {
				id = strings.TrimSpace(e.Title)
			}
			b.WriteString(fmt.Sprintf("- **%s**: %s\n", id, strings.TrimSpace(e.Summary)))
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

func writePRSection(b *strings.Builder, label string, items []collectors.StatusItem) {
	b.WriteString(label)
	b.WriteString("\n")
	if len(items) == 0 {
		b.WriteString("- _none_\n")
		return
	}
	for _, s := range items {
		b.WriteString(prBullet(s))
		b.WriteString("\n")
	}
}

// sortPRItems orders PRs by repository then title for stable output.
func sortPRItems(items []collectors.StatusItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Repository != items[j].Repository {
			return items[i].Repository < items[j].Repository
		}
		return items[i].Title < items[j].Title
	})
}

// prBullet formats a single PR as `- [link text](url) trailing`. The link
// text is the Jira ticket key when the title contains one; otherwise the
// full title is used as the link text and there is no trailing text.
func prBullet(s collectors.StatusItem) string {
	title := strings.TrimSpace(s.Title)
	key, rest := splitJiraKey(title)
	if key == "" {
		return fmt.Sprintf("- [%s](%s)", title, s.URL)
	}
	if rest == "" {
		return fmt.Sprintf("- [%s](%s)", key, s.URL)
	}
	return fmt.Sprintf("- [%s](%s) %s", key, s.URL, rest)
}

// splitJiraKey extracts the first Jira ticket key from a PR title and
// returns the key plus the title with that key (and any surrounding
// brackets / separators) removed. When no key is present it returns an
// empty key and the original title.
func splitJiraKey(title string) (key, rest string) {
	loc := jiraKeyPattern.FindStringIndex(title)
	if loc == nil {
		return "", title
	}
	key = title[loc[0]:loc[1]]
	rest = title[:loc[0]] + title[loc[1]:]
	// Clean up the junction left behind, e.g. "[SALSA-1] " -> "" and
	// "SALSA-1: " -> "". Trim brackets, separators, and whitespace from
	// both ends.
	rest = strings.Trim(rest, " \t[](){}:#-")
	rest = strings.TrimSpace(rest)
	return key, rest
}
