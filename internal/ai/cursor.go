package ai

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// CursorCLIProvider shells out to cursor-agent (or Command) with a single prompt payload.
type CursorCLIProvider struct {
	Command string
	Args    []string
}

func (p CursorCLIProvider) command() string {
	if strings.TrimSpace(p.Command) == "" {
		return "cursor-agent"
	}
	return p.Command
}

func (p CursorCLIProvider) GenerateDailyPlan(ctx context.Context, in DailyPlanInput) (string, error) {
	instruction := "Create a practical daily plan as Markdown with sections `## Yesterday` and `## Today`, using `statuses` as ground truth."
	payload, err := BuildDailyPlanPrompt(instruction, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload)
}

func (p CursorCLIProvider) SummarizeRecentActivity(ctx context.Context, in RecentActivityInput) (string, error) {
	instruction := fmt.Sprintf(
		"Summarize the developer's recent activity over %d calendar day(s) (%s to %s UTC). Group by project when project_id is present. Use the structured `statuses` JSON below as ground truth. Return one Markdown document.",
		in.LookbackDays,
		in.Since.Format(time.RFC3339),
		in.Now.Format(time.RFC3339),
	)
	payload, err := BuildRecentActivityPrompt(instruction, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload)
}

func (p CursorCLIProvider) RunCustomPrompt(ctx context.Context, in CustomPromptInput) (string, error) {
	payload, err := BuildCustomPromptPayload(in.UserPrompt, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload)
}

func (p CursorCLIProvider) runMarkdown(ctx context.Context, payload string) (string, error) {
	args := p.Args
	if len(args) == 0 {
		args = []string{"-p", payload}
	}
	cmd := exec.CommandContext(ctx, p.command(), args...)
	output, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s: %w\n%s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
	}
	return StripMarkdownFence(string(output)), nil
}
