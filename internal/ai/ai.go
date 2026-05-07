package ai

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

// Provider turns collected status rows into Markdown documents.
type Provider interface {
	GenerateDailyPlan(ctx context.Context, in DailyPlanInput) (string, error)
	SummarizeRecentActivity(ctx context.Context, in RecentActivityInput) (string, error)
	RunCustomPrompt(ctx context.Context, in CustomPromptInput) (string, error)
}

// DailyPlanInput is passed for the daily-plan mode (previous workday window).
type DailyPlanInput struct {
	Now            time.Time
	Since          time.Time
	UserPriorities string
	Statuses       []collectors.StatusItem
	DebugDir       string
	StreamOut      io.Writer
}

// RecentActivityInput is passed for recent-activity mode.
type RecentActivityInput struct {
	Now          time.Time
	Since        time.Time
	LookbackDays int
	Statuses     []collectors.StatusItem
	DebugDir     string
	StreamOut    io.Writer
}

// CustomPromptInput is passed for custom-prompt mode.
type CustomPromptInput struct {
	Now          time.Time
	Since        time.Time
	LookbackDays int
	UserPrompt   string
	Statuses     []collectors.StatusItem
	DebugDir     string
	StreamOut    io.Writer
}

// RuleBasedProvider is deterministic (no network); used for tests and offline runs.
type RuleBasedProvider struct {
	Formatter ActivityFormatter // if nil, uses repo-chronological (##)
}

func (p RuleBasedProvider) formatterOrDefault() ActivityFormatter {
	if p.Formatter != nil {
		return p.Formatter
	}
	return RepoChronologicalFormatter{HeadingLevel: 2}
}

func (p RuleBasedProvider) GenerateDailyPlan(_ context.Context, in DailyPlanInput) (string, error) {
	return RenderDailyPlanMarkdown(in, p.formatterOrDefault()), nil
}

func (p RuleBasedProvider) SummarizeRecentActivity(_ context.Context, in RecentActivityInput) (string, error) {
	return RenderRecentActivityMarkdown(in, p.formatterOrDefault()), nil
}

func (p RuleBasedProvider) RunCustomPrompt(_ context.Context, in CustomPromptInput) (string, error) {
	nestedFmt := NestRepoChronologicalDepth(p.formatterOrDefault())
	body, err := nestedFmt.Format(in.Statuses)
	if err != nil {
		body = fmt.Sprintf("_formatter error: %v_", err)
	}
	var b strings.Builder
	b.WriteString("# Custom report\n\n")
	b.WriteString(in.UserPrompt)
	b.WriteString("\n\n## Activity (ground truth)\n\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	return b.String(), nil
}
