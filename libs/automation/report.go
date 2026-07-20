package automation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ReportRequest carries the per-run report parameters a ReportGenerator needs.
type ReportRequest struct {
	// ModeID selects the execution mode (or the special "goal-alignment").
	ModeID string
	// Days is the lookback override; <=0 uses the mode/default.
	Days int
	// Prompt fully overrides the mode's instruction when non-empty.
	Prompt string
	// Context is appended to the resolved instruction when non-empty,
	// layering extra guidance onto a built-in prompt without replacing it.
	Context string
}

// ReportGenerator produces markdown for a report request.
type ReportGenerator interface {
	Generate(ctx context.Context, req ReportRequest) (markdown string, err error)
}

// ReportGeneratorFunc adapts a function to ReportGenerator.
type ReportGeneratorFunc func(ctx context.Context, req ReportRequest) (string, error)

func (f ReportGeneratorFunc) Generate(ctx context.Context, req ReportRequest) (string, error) {
	return f(ctx, req)
}

// ReportRunner runs an execution mode and delivers the markdown.
type ReportRunner struct {
	Generator ReportGenerator
	// DefaultOutDir is used when deliver=file and out_path is empty.
	DefaultOutDir string
	// SlackPoster delivers when deliver=slack.
	SlackPoster ChatPoster
	// DefaultSlackChannel used when action.Channel is empty.
	DefaultSlackChannel string
}

func (r ReportRunner) Run(ctx context.Context, action Action, ev Event) error {
	if r.Generator == nil {
		return fmt.Errorf("report: no generator configured")
	}
	mode := strings.TrimSpace(action.Mode)
	if mode == "" {
		return fmt.Errorf("report: mode is required")
	}
	md, err := r.Generator.Generate(ctx, ReportRequest{
		ModeID:  mode,
		Days:    action.Days,
		Prompt:  strings.TrimSpace(action.Prompt),
		Context: strings.TrimSpace(action.Context),
	})
	if err != nil {
		return fmt.Errorf("report generate: %w", err)
	}
	actx := EventContext(ev)
	deliver := strings.TrimSpace(action.Deliver)
	if deliver == "" {
		deliver = "file"
	}
	switch deliver {
	case "file":
		path := strings.TrimSpace(action.OutPath)
		if path == "" {
			dir := r.DefaultOutDir
			if dir == "" {
				dir = "."
			}
			path = filepath.Join(dir, fmt.Sprintf("standup-%s.md", time.Now().Format("2006-01-02")))
		}
		path, err = RenderTemplate(path, actx)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(md), 0o644)
	case "slack":
		if r.SlackPoster == nil {
			return fmt.Errorf("report: slack delivery requires a Slack poster")
		}
		channel := action.Channel
		if channel == "" {
			channel = r.DefaultSlackChannel
		}
		channel, err = RenderTemplate(channel, actx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(channel) == "" {
			return fmt.Errorf("report: slack channel is empty")
		}
		// Slack has a message size limit; truncate with a note if needed.
		body := md
		if len(body) > 3500 {
			body = body[:3500] + "\n\n_(truncated)_"
		}
		return r.SlackPoster.PostMessage(ctx, channel, body)
	case "webhook":
		return WebhookRunner{}.Run(ctx, Action{
			Type: "webhook",
			URL:  action.URL,
			Body: md,
		}, ev)
	default:
		return fmt.Errorf("report: unknown deliver %q", deliver)
	}
}
