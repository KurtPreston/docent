package ai

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// BuildRecentActivityPrompt serializes input for LLM providers using the configured formatter.
func BuildRecentActivityPrompt(instruction string, in RecentActivityInput, formatter ActivityFormatter) (string, error) {
	body, err := formatter.Format(in.Statuses)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	buf.WriteString(instruction)
	buf.WriteString("\n\n")
	buf.WriteString(groundTruthHint(formatter))
	buf.WriteString(" Return only a single Markdown document (no JSON). Do not wrap the entire answer in a code fence.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")
	appendRecentActivityWindowMeta(&buf, in)
	buf.WriteString(body)
	return buf.String(), nil
}

func appendRecentActivityWindowMeta(w *bytes.Buffer, in RecentActivityInput) {
	fmt.Fprintf(w, "Window: %s — %s\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	fmt.Fprintf(w, "Lookback: %d calendar day(s)\n\n", in.LookbackDays)
}

// RenderDailyPlanMarkdown renders a deterministic two-section document for rule-based mode.
func RenderDailyPlanMarkdown(in DailyPlanInput, formatter ActivityFormatter) string {
	nestedFmt := NestRepoChronologicalDepth(formatter)
	body, err := nestedFmt.Format(in.Statuses)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Daily plan\n\n")
	fmt.Fprintf(&b, "_Window: %s — %s_\n\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	if strings.TrimSpace(in.UserPriorities) != "" {
		fmt.Fprintf(&b, "**Your priorities:** %s\n\n", strings.TrimSpace(in.UserPriorities))
	}
	fmt.Fprintf(&b, "## Yesterday\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.TrimRight(body, "\n"))
	fmt.Fprintf(&b, "## Today\n\n")
	fmt.Fprintf(&b, "_Suggested next steps (configure `ai.provider` to `ollama` or `cursor` for model-generated planning):_\n\n")
	fmt.Fprintf(&b, "- Review the activity above and pick 1–3 focus items.\n")
	fmt.Fprintf(&b, "- Block time for the highest-signal work.\n")
	return b.String()
}

// RenderRecentActivityMarkdown deterministically renders statuses via the formatter.
func RenderRecentActivityMarkdown(in RecentActivityInput, formatter ActivityFormatter) string {
	body, err := formatter.Format(in.Statuses)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Recent activity (%s — %s)\n\n", in.Since.Format("2006-01-02"), in.Now.Format("2006-01-02"))
	fmt.Fprintf(&b, "_lookback: %d day(s)_\n\n", in.LookbackDays)
	fmt.Fprintf(&b, "%s\n", strings.TrimRight(body, "\n"))
	return b.String()
}

func groundTruthHint(formatter ActivityFormatter) string {
	if formatter.Name() == activityFormatterJSONSignalList {
		return "Use the structured JSON activity array below as ground truth."
	}
	return "Use the aggregated activity timeline below as ground truth (Markdown headings separate repositories; lines are chronological within each)."
}

// BuildDailyPlanPrompt builds the user message for LLM daily plan generation.
func BuildDailyPlanPrompt(instruction string, in DailyPlanInput, formatter ActivityFormatter) (string, error) {
	nestedFmt := NestRepoChronologicalDepth(formatter)
	body, err := nestedFmt.Format(in.Statuses)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	buf.WriteString(instruction)
	buf.WriteString("\n\n")
	buf.WriteString(groundTruthHint(formatter))
	buf.WriteString(" Return only a single Markdown document with exactly these sections: `## Yesterday` then `## Today`.\n")
	buf.WriteString("Do not invent work not present in the input.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")

	fmt.Fprintf(&buf, "Window: %s — %s\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	if strings.TrimSpace(in.UserPriorities) != "" {
		fmt.Fprintf(&buf, "Your priorities: %s\n\n", strings.TrimSpace(in.UserPriorities))
	}
	buf.WriteString(body)
	return buf.String(), nil
}

// BuildCustomPromptPayload builds the user message for custom prompt mode.
func BuildCustomPromptPayload(userPrompt string, in CustomPromptInput, formatter ActivityFormatter) (string, error) {
	nestedFmt := NestRepoChronologicalDepth(formatter)
	body, err := nestedFmt.Format(in.Statuses)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	buf.WriteString("The user request is below.\n")
	buf.WriteString("\nUser request:\n")
	buf.WriteString(strings.TrimRight(userPrompt, "\n"))
	buf.WriteString("\n\n")
	buf.WriteString(groundTruthHint(formatter))
	buf.WriteString(" Follow the user's instructions accordingly.\n\n")
	buf.WriteString("Return only a single Markdown document (no JSON). Do not wrap the entire answer in a code fence.\n")
	buf.WriteString("Never include credentials, secrets, or unrelated local data.\n\n")

	fmt.Fprintf(&buf, "Window: %s — %s\n", in.Since.Format(time.RFC3339), in.Now.Format(time.RFC3339))
	fmt.Fprintf(&buf, "Lookback: %d calendar day(s)\n\n", in.LookbackDays)

	buf.WriteString(body)
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
