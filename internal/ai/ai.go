package ai

import (
	"context"
	"io"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

// Provider turns a single resolved execution-mode run into a Markdown document.
type Provider interface {
	RunMode(ctx context.Context, in RunInput) (string, error)
}

// RunInput is everything a provider needs to produce one Markdown document:
// the LLM instruction text (already resolved by executionmode.Resolve), the
// time window, the collected statuses, and where to stream/debug.
//
// ModeID is provided so the deterministic RuleBasedProvider can preserve the
// historical per-mode output shape; LLM providers ignore it.
type RunInput struct {
	ModeID       string
	ModeName     string
	Now          time.Time
	Since        time.Time
	LookbackDays int // 0 when the lookback is not days-based (e.g. previous-weekday)
	Instruction  string
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

// RunMode dispatches to per-builtin renderers when ModeID matches a known
// built-in, and falls back to a generic "heading + instruction + activity"
// layout for user-defined modes.
func (p RuleBasedProvider) RunMode(_ context.Context, in RunInput) (string, error) {
	switch in.ModeID {
	case "daily-plan":
		return RenderDailyPlanMarkdown(in, p.formatterOrDefault()), nil
	case "recent-activity":
		return RenderRecentActivityMarkdown(in, p.formatterOrDefault()), nil
	case "custom-prompt":
		return RenderCustomPromptMarkdown(in, p.formatterOrDefault()), nil
	default:
		return RenderGenericMarkdown(in, p.formatterOrDefault()), nil
	}
}
