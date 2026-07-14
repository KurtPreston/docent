// Package report holds the reusable report pipeline shared by the
// docent-reporter CLI and the docentd daemon: resolve an execution mode,
// collect signals for its window/scope, and render the result to Markdown
// via the configured AI provider.
//
// It performs no file I/O, no stdout, and no interactive prompting. Callers
// keep those concerns (the CLI writes run logs / output files and drives an
// interactive picker; docentd wraps Generate in an async job). Overrides
// (days, scope, prompt) must therefore be supplied explicitly.
package report

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// Options carries everything Generate needs for one report run: the mode
// selector, the per-run overrides, and the optional hooks that let the CLI
// keep progress/streaming/run-log parity.
type Options struct {
	// ModeID selects the execution mode (built-in or user-declared).
	ModeID string
	// Days is the lookback override (--days). 0 uses the mode default.
	Days int
	// Prompt is the instruction override. Empty uses the mode default;
	// modes that require a prompt error when both are empty.
	Prompt string
	// Scope forces the collection scope. ScopeUnset ("") uses the mode
	// default (which is ScopeInvolved for non-interactive callers).
	Scope executionmode.Scope
	// Collect forces the collection capability (events / state / both).
	// CollectUnset ("") uses the mode default (events for most modes).
	Collect executionmode.Collect

	// Now is the clock anchor. Zero uses time.Now().
	Now time.Time
	// ConfigDir holds .env for credential_refs (collectors read it).
	ConfigDir string

	// Registry is the collector registry. Nil constructs a fresh one bound
	// to Now.
	Registry *collectors.Registry
	// Provider is the fallback AI provider used when cfg.AI selects the
	// default/unknown provider. Nil falls back to ai.RuleBasedProvider{}.
	Provider ai.Provider

	// Optional hooks — safe to leave nil.
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(collectors.DirectiveProgress)
	RunLog            collectors.RunLog
	DebugDir          string
	StreamOut         io.Writer
}

// CollectOptions are the collection-only knobs shared by Generate and the
// CLI (which resolves + stages its run log itself, then collects).
type CollectOptions struct {
	ConfigDir         string
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(collectors.DirectiveProgress)
	RunLog            collectors.RunLog
}

// RenderOptions are the render-only knobs shared by Generate and the CLI.
type RenderOptions struct {
	DebugDir  string
	StreamOut io.Writer
}

// Result is the outcome of a report run.
type Result struct {
	Markdown string
	Run      executionmode.ResolvedRun
	Statuses int
}

// Generate runs the full pipeline (resolve -> collect -> render) for the
// given config and options. docentd calls this directly; the CLI composes
// the same steps itself so it can interleave run-log staging.
func Generate(ctx context.Context, cfg userdata.ConfigFile, opts Options) (Result, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	modes, err := executionmode.Load(executionmode.BuiltinModes(), cfg.ExecutionModes)
	if err != nil {
		return Result{}, err
	}
	mode, ok := executionmode.Find(modes, strings.TrimSpace(opts.ModeID))
	if !ok {
		available := make([]string, len(modes))
		for i, m := range modes {
			available[i] = m.ID
		}
		return Result{}, fmt.Errorf("unknown mode %q (available: %s)", opts.ModeID, strings.Join(available, ", "))
	}

	resolved, err := executionmode.Resolve(mode, executionmode.ResolveOpts{
		Now:                     now,
		DaysOverride:            opts.Days,
		PromptOverride:          opts.Prompt,
		ScopeOverride:           opts.Scope,
		CollectOverride:         opts.Collect,
		ConfigActivityFormatter: cfg.AI.ActivityFormatter,
	})
	if err != nil {
		return Result{}, err
	}

	reg := opts.Registry
	if reg == nil {
		reg = collectors.NewRegistry(func() time.Time { return now })
	}

	statuses, err := Collect(ctx, reg, cfg, resolved, CollectOptions{
		ConfigDir:         opts.ConfigDir,
		ExpandRepoPath:    opts.ExpandRepoPath,
		OnDirectiveUpdate: opts.OnDirectiveUpdate,
		RunLog:            opts.RunLog,
	})
	if err != nil {
		return Result{}, err
	}

	provider := ai.SelectProvider(cfg.AI, opts.Provider)
	md, err := Render(ctx, resolved, statuses, provider, RenderOptions{
		DebugDir:  opts.DebugDir,
		StreamOut: opts.StreamOut,
	})
	if err != nil {
		return Result{}, err
	}

	return Result{Markdown: md, Run: resolved, Statuses: len(statuses)}, nil
}

// Collect runs the enabled directives for an already-resolved run, honoring
// resolved.Collect (events, state, or both). When CollectBoth, both passes
// run and their signals are concatenated. It assembles the CollectOpts both
// the CLI and Generate need.
func Collect(ctx context.Context, reg *collectors.Registry, cfg userdata.ConfigFile, resolved executionmode.ResolvedRun, opts CollectOptions) ([]collectors.StatusItem, error) {
	expand := opts.ExpandRepoPath
	if expand == nil {
		expand = func(s string) string { return s }
	}
	base := &collectors.CollectOpts{
		UserdataDir:        opts.ConfigDir,
		ExpandRepoPath:     expand,
		OnDirectiveUpdate:  opts.OnDirectiveUpdate,
		Since:              resolved.Since,
		Until:              resolved.Until,
		Scope:              collectors.Scope(resolved.Scope),
		OnlyCollectorTypes: resolved.Collectors,
		RunLog:             opts.RunLog,
	}

	collect := resolved.Collect
	if collect == executionmode.CollectUnset {
		collect = executionmode.CollectEvents
	}

	var all []collectors.StatusItem
	runPass := func(mode collectors.Mode) error {
		pass := *base
		pass.Mode = mode
		items, err := reg.Collect(ctx, cfg.Directives, &pass)
		if err != nil {
			return err
		}
		all = append(all, items...)
		return nil
	}

	switch collect {
	case executionmode.CollectState:
		if err := runPass(collectors.ModeState); err != nil {
			return nil, err
		}
	case executionmode.CollectBoth:
		if err := runPass(collectors.ModeEvents); err != nil {
			return nil, err
		}
		if err := runPass(collectors.ModeState); err != nil {
			return nil, err
		}
	default: // CollectEvents
		if err := runPass(collectors.ModeEvents); err != nil {
			return nil, err
		}
	}
	return all, nil
}

// Render turns a resolved run + collected statuses into a Markdown document
// via the given provider, applying the mode's per-run formatter override.
// The returned Markdown is trimmed and newline-terminated.
func Render(ctx context.Context, resolved executionmode.ResolvedRun, statuses []collectors.StatusItem, provider ai.Provider, opts RenderOptions) (string, error) {
	if resolved.Formatter != "" {
		provider = ai.WithFormatter(provider, ai.SelectActivityFormatter(resolved.Formatter))
	}
	md, err := provider.RunMode(ctx, ai.RunInput{
		ModeID:       resolved.ModeID,
		ModeName:     resolved.ModeName,
		Now:          resolved.Until,
		Since:        resolved.Since,
		LookbackDays: resolved.LookbackDays,
		Instruction:  resolved.Instruction,
		Statuses:     statuses,
		DebugDir:     opts.DebugDir,
		StreamOut:    opts.StreamOut,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(md) + "\n", nil
}
