package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/KurtPreston/docent/libs/model"
)

// formatActivityBody prefers compact work-item output when WorkItems are
// present (the correlated report path); otherwise falls back to formatting
// the raw Statuses list. This is what LLM prompts and rule-based renderers
// share, and is the byte-reduction win for cursor-agent/claude/ollama.
func formatActivityBody(in RunInput, formatter ActivityFormatter) (string, error) {
	if len(in.WorkItems) > 0 {
		return FormatWorkItems(in.WorkItems, formatter)
	}
	return formatter.Format(in.Statuses)
}

// FormatWorkItems renders correlated work items using the activity
// formatter's style: JSON when the formatter is json-signal-list, otherwise
// compact Markdown grouped by work item.
func FormatWorkItems(items []model.WorkItem, formatter ActivityFormatter) (string, error) {
	if formatter != nil && formatter.Name() == activityFormatterJSONSignalList {
		return formatWorkItemsJSON(items)
	}
	return formatWorkItemsMarkdown(items, 2), nil
}

// NestWorkItemDepth bumps Markdown heading depth for nesting under a ##
// section (e.g. Activity / Yesterday).
func NestWorkItemDepth(headingLevel int) int {
	if headingLevel < 3 {
		return 3
	}
	return headingLevel
}

func formatWorkItemsMarkdown(items []model.WorkItem, headingLevel int) string {
	if headingLevel < 2 {
		headingLevel = 2
	}
	prefix := strings.Repeat("#", headingLevel) + " "
	if len(items) == 0 {
		return "_No work items in this window._"
	}

	// Stable order: last activity desc, then key.
	sorted := append([]model.WorkItem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].LastActivity != sorted[j].LastActivity {
			return sorted[i].LastActivity > sorted[j].LastActivity
		}
		return sorted[i].Key < sorted[j].Key
	})

	var b strings.Builder
	for _, wi := range sorted {
		title := strings.TrimSpace(wi.Title)
		if title == "" {
			title = wi.Key
		}
		fmt.Fprintf(&b, "%s%s\n\n", prefix, title)
		if wi.Repo != "" || wi.Branch != "" {
			loc := wi.Repo
			if wi.Branch != "" {
				if loc != "" {
					loc += " @ "
				}
				loc += wi.Branch
			}
			fmt.Fprintf(&b, "- location: %s\n", loc)
		}
		for _, tr := range wi.Tickets {
			desc := ticketDescription(tr)
			link := tr.URL
			if link == "" {
				link = tr.Key
			}
			line := fmt.Sprintf("- ticket: [%s](%s)", tr.Key, link)
			if desc != "" {
				line += " " + desc
			}
			if tr.Status != "" {
				line += fmt.Sprintf(" [%s]", tr.Status)
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
		for _, ent := range wi.Entities {
			if !strings.Contains(ent.Kind, "pr") {
				continue
			}
			label := strings.TrimSpace(ent.Title)
			if label == "" {
				label = ent.Kind
			}
			if ent.URL != "" {
				fmt.Fprintf(&b, "- pr: [%s](%s)\n", label, ent.URL)
			} else {
				fmt.Fprintf(&b, "- pr: %s\n", label)
			}
		}
		// Compact entity tally (exclude PRs already listed).
		counts := map[string]int{}
		for _, ent := range wi.Entities {
			if strings.Contains(ent.Kind, "pr") {
				continue
			}
			k := ent.Kind
			if k == "" {
				k = "other"
			}
			counts[k]++
		}
		if len(counts) > 0 {
			kinds := make([]string, 0, len(counts))
			for k := range counts {
				kinds = append(kinds, k)
			}
			sort.Strings(kinds)
			parts := make([]string, 0, len(kinds))
			for _, k := range kinds {
				parts = append(parts, fmt.Sprintf("%s×%d", k, counts[k]))
			}
			fmt.Fprintf(&b, "- activity: %s\n", strings.Join(parts, ", "))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatWorkItemsJSON(items []model.WorkItem) (string, error) {
	raw, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// ticketDescription returns the ticket title with a leading "KEY " stripped.
func ticketDescription(tr model.TicketRef) string {
	title := strings.TrimSpace(tr.Title)
	if title == "" {
		return ""
	}
	key := strings.TrimSpace(tr.Key)
	if key != "" && strings.HasPrefix(strings.ToUpper(title), strings.ToUpper(key)) {
		rest := strings.TrimSpace(title[len(key):])
		rest = strings.TrimLeft(rest, " \t:-–—|")
		return rest
	}
	return title
}
