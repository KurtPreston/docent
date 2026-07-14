package ai

import (
	"fmt"
	"strings"
	"time"
)

// BuildPrompt assembles the message body sent to an LLM provider for one
// run: the mode-supplied instruction, ground-truth hint, the time window,
// and the formatted activity body. Prefers correlated WorkItems when
// present (compact); otherwise formats the raw Statuses list.
//
// LLM providers append no per-mode framing of their own; everything the
// model sees comes from `instruction` plus the activity body that follows.
func BuildPrompt(instruction string, in RunInput, formatter ActivityFormatter) (string, error) {
	body, err := formatActivityBody(in, formatter)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	buf.WriteString(strings.TrimRight(instruction, "\n"))
	buf.WriteString("\n\n")
	buf.WriteString(groundTruthHint(in, formatter))
	buf.WriteString(" Return only a single Markdown document (no JSON). Do not wrap the entire answer in a code fence.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	appendWindowMeta(&buf, in)
	buf.WriteString(body)
	return buf.String(), nil
}

func appendWindowMeta(buf *strings.Builder, in RunInput) {
	fmt.Fprintf(buf, "Window: %s — %s\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	if in.LookbackDays > 0 {
		fmt.Fprintf(buf, "Lookback: %d calendar day(s)\n", in.LookbackDays)
	}
	buf.WriteString("\n")
}

// RenderDailyPlanMarkdown renders the deterministic two-section document
// previously produced by the rule-based daily-plan path. Prefer work items
// when present.
func RenderDailyPlanMarkdown(in RunInput, formatter ActivityFormatter) string {
	var body string
	var err error
	if len(in.WorkItems) > 0 {
		body, err = formatActivityBody(in, formatter)
	} else {
		nestedFmt := NestRepoChronologicalDepth(formatter)
		body, err = nestedFmt.Format(in.Statuses)
	}
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Daily plan\n\n")
	fmt.Fprintf(&b, "_Window: %s — %s_\n\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	fmt.Fprintf(&b, "## Yesterday\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(body, "\n"))
	fmt.Fprintf(&b, "## Today\n\n")
	fmt.Fprintf(&b, "_Suggested next steps (configure `ai.provider` to `ollama`, `cursor`, or `claude` for model-generated planning):_\n\n")
	fmt.Fprintf(&b, "- Review the activity above and pick 1–3 focus items.\n")
	fmt.Fprintf(&b, "- Block time for the highest-signal work.\n")
	return b.String()
}

// RenderRecentActivityMarkdown deterministically renders work items (or
// statuses) under a single `# Recent activity` heading.
func RenderRecentActivityMarkdown(in RunInput, formatter ActivityFormatter) string {
	body, err := formatActivityBody(in, formatter)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Recent activity (%s — %s)\n\n", in.Since.Format("2006-01-02"), in.Now.Format("2006-01-02"))
	if in.LookbackDays > 0 {
		fmt.Fprintf(&b, "_lookback: %d day(s)_\n\n", in.LookbackDays)
	}
	fmt.Fprintf(&b, "%s\n", strings.TrimRight(body, "\n"))
	return b.String()
}

// RenderCustomPromptMarkdown deterministically renders the user-prompt mode
// (instruction at the top, activity nested under `## Activity (ground truth)`).
func RenderCustomPromptMarkdown(in RunInput, formatter ActivityFormatter) string {
	body, err := formatActivityBody(in, formatter)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	b.WriteString("# Custom report\n\n")
	b.WriteString(strings.TrimRight(in.Instruction, "\n"))
	b.WriteString("\n\n## Activity (ground truth)\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	return b.String()
}

// RenderGenericMarkdown is the fallback rule-based renderer used for
// user-declared execution modes: the mode's display name as the H1, the
// instruction (when present) as a blockquote-style preamble, and the
// activity body nested under `## Activity`.
func RenderGenericMarkdown(in RunInput, formatter ActivityFormatter) string {
	body, err := formatActivityBody(in, formatter)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	title := strings.TrimSpace(in.ModeName)
	if title == "" {
		title = strings.TrimSpace(in.ModeID)
	}
	if title == "" {
		title = "Report"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "_Window: %s — %s_\n\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	if s := strings.TrimSpace(in.Instruction); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString("## Activity\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	return b.String()
}

func groundTruthHint(in RunInput, formatter ActivityFormatter) string {
	if len(in.WorkItems) > 0 {
		if formatter != nil && formatter.Name() == activityFormatterJSONSignalList {
			return "Use the structured JSON work-item array below as ground truth."
		}
		return "Use the correlated work-item list below as ground truth (each item groups a ticket/branch with its PRs and activity)."
	}
	if formatter != nil && formatter.Name() == activityFormatterJSONSignalList {
		return "Use the structured JSON activity array below as ground truth."
	}
	return "Use the aggregated activity timeline below as ground truth (Markdown headings separate repositories; lines are chronological within each)."
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
