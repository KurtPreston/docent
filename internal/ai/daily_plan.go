package ai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

// BuildRecentActivityPrompt serializes input for LLM providers.
func BuildRecentActivityPrompt(instruction string, in RecentActivityInput) (string, error) {
	wire := struct {
		Now          string                   `json:"now"`
		Since        string                   `json:"since"`
		LookbackDays int                      `json:"lookback_days"`
		Statuses     []collectors.StatusItem  `json:"statuses"`
	}{
		Now:          in.Now.Format(time.RFC3339),
		Since:        in.Since.Format(time.RFC3339),
		LookbackDays: in.LookbackDays,
		Statuses:     in.Statuses,
	}
	payload, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(instruction)
	buf.WriteString("\n\nReturn only a single Markdown document (no JSON). Do not wrap the entire answer in a code fence.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	buf.Write(payload)
	return buf.String(), nil
}

// RenderDailyPlanMarkdown renders a deterministic two-section document for rule-based mode.
func RenderDailyPlanMarkdown(in DailyPlanInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Daily plan\n\n")
	fmt.Fprintf(&b, "_Window: %s — %s_\n\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	if strings.TrimSpace(in.UserPriorities) != "" {
		fmt.Fprintf(&b, "**Your priorities:** %s\n\n", strings.TrimSpace(in.UserPriorities))
	}
	fmt.Fprintf(&b, "## Yesterday\n\n")
	renderActivityBySourceLegacy(&b, in.Statuses)
	fmt.Fprintf(&b, "\n## Today\n\n")
	fmt.Fprintf(&b, "_Suggested next steps (configure `ai.provider` to `ollama` or `cursor` for model-generated planning):_\n\n")
	fmt.Fprintf(&b, "- Review the activity above and pick 1–3 focus items.\n")
	fmt.Fprintf(&b, "- Block time for the highest-signal work.\n")
	return b.String()
}

// RenderRecentActivityMarkdown deterministically renders statuses as Markdown.
func RenderRecentActivityMarkdown(in RecentActivityInput) string {
	projName := map[string]string{}
	for _, s := range in.Statuses {
		pid := strings.TrimSpace(s.ProjectID)
		if pid == "" {
			continue
		}
		if _, ok := projName[pid]; !ok {
			projName[pid] = pid
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Recent activity (%s — %s)\n\n", in.Since.Format("2006-01-02"), in.Now.Format("2006-01-02"))
	fmt.Fprintf(&b, "_lookback: %d day(s)_\n\n", in.LookbackDays)
	byProject := map[string][]collectors.StatusItem{}
	var cross []collectors.StatusItem
	for _, s := range in.Statuses {
		if s.Kind == "collector_error" {
			continue
		}
		pid := strings.TrimSpace(s.ProjectID)
		if pid == "" {
			cross = append(cross, s)
			continue
		}
		byProject[pid] = append(byProject[pid], s)
	}
	allPids := make([]string, 0, len(byProject))
	for pid := range byProject {
		allPids = append(allPids, pid)
	}
	sort.Strings(allPids)
	for _, pid := range allPids {
		name := projName[pid]
		if name == "" {
			name = pid
		}
		fmt.Fprintf(&b, "## %s (`%s`)\n\n", name, pid)
		items := byProject[pid]
		if len(items) == 0 {
			b.WriteString("_No activity in this window._\n\n")
			continue
		}
		renderActivityBySourceLegacy(&b, items)
	}
	if len(cross) > 0 {
		b.WriteString("## Cross-cutting\n\n")
		renderActivityBySourceLegacy(&b, cross)
	}
	var errs []collectors.StatusItem
	for _, s := range in.Statuses {
		if s.Kind == "collector_error" {
			errs = append(errs, s)
		}
	}
	if len(errs) > 0 {
		b.WriteString("## Collector errors\n\n")
		for _, e := range errs {
			fmt.Fprintf(&b, "- **%s**: %s\n", e.Title, e.Summary)
		}
	}
	return b.String()
}

func renderActivityBySourceLegacy(b *strings.Builder, items []collectors.StatusItem) {
	bySource := map[string][]collectors.StatusItem{}
	var order []string
	for _, it := range items {
		src := it.Source
		if src == "" {
			src = "(unknown)"
		}
		if _, ok := bySource[src]; !ok {
			order = append(order, src)
		}
		bySource[src] = append(bySource[src], it)
	}
	sort.Strings(order)
	for _, src := range order {
		list := bySource[src]
		sort.Slice(list, func(i, j int) bool {
			return list[i].ObservedAt.After(list[j].ObservedAt)
		})
		fmt.Fprintf(b, "### %s\n\n", src)
		for _, s := range list {
			line := formatActivityLine(s)
			if line != "" {
				fmt.Fprintf(b, "- %s\n", line)
			}
		}
		b.WriteString("\n")
	}
}

func formatActivityLine(s collectors.StatusItem) string {
	iso := s.ObservedAt.UTC().Format(time.RFC3339)
	switch s.Kind {
	case "commit":
		sh := ""
		if s.Fields != nil {
			sh = s.Fields["short_hash"]
		}
		author := ""
		if s.Fields != nil {
			author = s.Fields["author"]
		}
		if sh == "" && s.Fields != nil {
			sh = s.Fields["hash"]
			if len(sh) > 7 {
				sh = sh[:7]
			}
		}
		return fmt.Sprintf("%s %s %s — %s", iso, sh, author, strings.TrimSpace(s.Title))
	case "reflog":
		return fmt.Sprintf("%s %s", iso, strings.TrimSpace(s.Title))
	case "pull_request_activity", "issue_activity":
		st := ""
		if s.Fields != nil {
			st = s.Fields["state"]
		}
		url := s.URL
		if url != "" {
			return fmt.Sprintf("%s [%s] %s (%s)", iso, st, s.Title, url)
		}
		return fmt.Sprintf("%s [%s] %s", iso, st, s.Title)
	case "authored_pr", "reviewed_pr", "involved_issue":
		st := ""
		if s.Fields != nil {
			st = s.Fields["state"]
		}
		if s.URL != "" {
			return fmt.Sprintf("%s [%s] %s (%s)", iso, st, s.Title, s.URL)
		}
		return fmt.Sprintf("%s [%s] %s", iso, st, s.Title)
	case "repository_updated":
		if s.URL != "" {
			return fmt.Sprintf("%s %s (%s)", iso, s.Title, s.URL)
		}
		return fmt.Sprintf("%s %s", iso, s.Title)
	case "calendar_event":
		dur := ""
		if s.Fields != nil {
			dur = s.Fields["duration"]
		}
		if dur != "" {
			return fmt.Sprintf("%s %s (%s)", iso, s.Title, dur)
		}
		return fmt.Sprintf("%s %s", iso, s.Title)
	default:
		if s.URL != "" {
			return fmt.Sprintf("%s [%s] %s (%s)", iso, s.Kind, s.Title, s.URL)
		}
		return fmt.Sprintf("%s [%s] %s", iso, s.Kind, s.Title)
	}
}

// BuildDailyPlanPrompt builds the user message for LLM daily plan generation.
func BuildDailyPlanPrompt(instruction string, in DailyPlanInput) (string, error) {
	wire := struct {
		Now            string                  `json:"now"`
		Since          string                  `json:"since"`
		UserPriorities string                  `json:"user_priorities,omitempty"`
		Statuses       []collectors.StatusItem `json:"statuses"`
	}{
		Now:            in.Now.Format(time.RFC3339),
		Since:          in.Since.Format(time.RFC3339),
		UserPriorities: in.UserPriorities,
		Statuses:       in.Statuses,
	}
	payload, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(instruction)
	buf.WriteString("\n\nReturn only a single Markdown document with exactly these sections: `## Yesterday` then `## Today`.\n")
	buf.WriteString("Use the `statuses` JSON as ground truth. Do not invent work not present in the input.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	buf.Write(payload)
	return buf.String(), nil
}

// BuildCustomPromptPayload builds the user message for custom prompt mode.
func BuildCustomPromptPayload(userPrompt string, in CustomPromptInput) (string, error) {
	wire := struct {
		Now          string                  `json:"now"`
		Since        string                  `json:"since"`
		LookbackDays int                     `json:"lookback_days"`
		UserPrompt   string                  `json:"user_prompt"`
		Statuses     []collectors.StatusItem `json:"statuses"`
	}{
		Now:          in.Now.Format(time.RFC3339),
		Since:        in.Since.Format(time.RFC3339),
		LookbackDays: in.LookbackDays,
		UserPrompt:   userPrompt,
		Statuses:     in.Statuses,
	}
	payload, err := json.MarshalIndent(wire, "", "  ")
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString("The user request and collected activity are below. Follow the user's instructions using `statuses` as ground truth.\n\n")
	buf.WriteString("Return only a single Markdown document (no JSON). Do not wrap the entire answer in a code fence.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	buf.Write(payload)
	return buf.String(), nil
}

// StripMarkdownFence removes a leading ```markdown / ``` wrapper if the model added one.
func StripMarkdownFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) < 2 {
		return s
	}
	end := len(lines) - 1
	for end > 0 && strings.TrimSpace(lines[end]) != "```" {
		end--
	}
	if end <= 0 {
		return s
	}
	inner := strings.Join(lines[1:end], "\n")
	return strings.TrimSpace(inner)
}
