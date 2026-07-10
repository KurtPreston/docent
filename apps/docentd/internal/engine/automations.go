package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/goals"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/report"
)

// Automations returns the dispatcher (may be nil when no rules are configured).
func (e *Engine) Automations() *automation.Dispatcher {
	return e.automations
}

// AutomationJobs returns recent automation job history.
func (e *Engine) AutomationJobs(limit int) []automation.Job {
	if e.automations == nil || e.automations.Store == nil {
		return nil
	}
	return e.automations.Store.List(limit)
}

// evaluateAutomations matches newly collected signals (and, when entity
// snapshots are available, state transitions) and dispatches actions.
// It must not run under e.mu.
func (e *Engine) evaluateAutomations(ctx context.Context, newSignals []model.Signal, prevEntities, nextEntities map[string]model.Entity) {
	if e.automations == nil {
		return
	}
	rules := e.automations.EnabledRules()
	if len(rules) == 0 {
		return
	}
	opts := automation.MatchOpts{CorrCfg: e.corrCfg}
	var events []automation.Event
	if len(newSignals) > 0 {
		events = append(events, automation.MatchSignals(rules, newSignals, opts)...)
	}
	if prevEntities != nil && nextEntities != nil {
		events = append(events, automation.MatchTransitions(rules, prevEntities, nextEntities, opts)...)
	}
	if len(events) > 0 {
		e.enrichEvents(events)
		e.automations.HandleEvents(ctx, events)
	}
}

func (e *Engine) enrichEvents(events []automation.Event) {
	e.mu.Lock()
	workItems := e.lastWorkItems
	entityWorkItem := e.entityWorkItem
	e.mu.Unlock()
	byKey := make(map[string]*model.WorkItem, len(workItems))
	for i := range workItems {
		byKey[workItems[i].Key] = &workItems[i]
	}
	for i := range events {
		entID := ""
		if events[i].Entity != nil {
			entID = events[i].Entity.ID
		} else if events[i].Signal != nil {
			entID = correlation.SignalToEntity(*events[i].Signal, e.corrCfg).ID
		}
		if entID == "" {
			continue
		}
		if key, ok := entityWorkItem[entID]; ok {
			if wi, ok := byKey[key]; ok {
				events[i].WorkItem = wi
			}
		}
	}
}

func (e *Engine) entitiesByID(signals []model.Signal) map[string]model.Entity {
	ents := correlation.SignalsToEntities(signals, e.corrCfg)
	out := make(map[string]model.Entity, len(ents))
	for _, ent := range ents {
		out[ent.ID] = ent
	}
	return out
}

func signalIDSet(signals []model.Signal) map[string]struct{} {
	out := make(map[string]struct{}, len(signals))
	for _, s := range signals {
		out[signalID(s)] = struct{}{}
	}
	return out
}

func filterNewSignals(signals []model.Signal, prev map[string]struct{}) []model.Signal {
	if len(signals) == 0 {
		return nil
	}
	var out []model.Signal
	for _, s := range signals {
		if _, ok := prev[signalID(s)]; ok {
			continue
		}
		out = append(out, s)
	}
	return out
}

// wireAutomationConnectors registers jira-comment and slack-post runners that
// dispatch through the collector registry using the first matching directive.
func (e *Engine) wireAutomationConnectors() {
	if e.automations == nil || e.automations.Registry == nil {
		return
	}
	opts := &collectors.CollectOpts{UserdataDir: e.cfg.ConfigDir}
	e.automations.Registry.Register("jira-comment", automation.JiraCommentRunner{
		Commenter: automation.IssueCommenterFunc(func(ctx context.Context, issueKey, body string) error {
			dir, ok := firstDirective(e.cfg.Directives, "jira")
			if !ok {
				return fmt.Errorf("no enabled jira directive configured")
			}
			return e.reg.PostComment(ctx, dir, opts, issueKey, body)
		}),
	})
	e.automations.Registry.Register("slack-post", automation.SlackPostRunner{
		Poster: automation.ChatPosterFunc(func(ctx context.Context, channel, body string) error {
			dir, ok := firstDirective(e.cfg.Directives, "slack")
			if !ok {
				return fmt.Errorf("no enabled slack directive configured")
			}
			return e.reg.PostMessage(ctx, dir, opts, channel, body)
		}),
	})
	// Agent actions are enqueued to the durable queue for apps/docent-automations.
	e.automations.Registry.Register("agent", automation.QueuingAgentRunner{})
	// Also register an in-process agent runner under "agent-inline" for tests /
	// environments without the worker.
	e.automations.Registry.Register("agent-inline", automation.AgentRunner{
		DefaultProvider: e.cfg.AI.Provider,
		CursorCommand:   e.cfg.AI.Cursor.Command,
		ClaudeCommand:   e.cfg.AI.Claude.Command,
		ResolveRemote:   automation.ResolveRemoteURL,
		Commenter: automation.IssueCommenterFunc(func(ctx context.Context, issueKey, body string) error {
			dir, ok := firstDirective(e.cfg.Directives, "jira")
			if !ok {
				return fmt.Errorf("no enabled jira directive configured")
			}
			return e.reg.PostComment(ctx, dir, opts, issueKey, body)
		}),
	})
	e.automations.Registry.Register("report", automation.ReportRunner{
		DefaultOutDir: e.cfg.OutputDir,
		SlackPoster: automation.ChatPosterFunc(func(ctx context.Context, channel, body string) error {
			dir, ok := firstDirective(e.cfg.Directives, "slack")
			if !ok {
				return fmt.Errorf("no enabled slack directive configured")
			}
			return e.reg.PostMessage(ctx, dir, opts, channel, body)
		}),
		Generator: automation.ReportGeneratorFunc(func(ctx context.Context, modeID string, days int) (string, error) {
			return e.generateReport(ctx, modeID, days)
		}),
	})
}

// tickSchedules evaluates schedule-type automation rules.
func (e *Engine) tickSchedules(ctx context.Context) {
	if e.automations == nil {
		return
	}
	now := time.Now()
	e.scheduleMu.Lock()
	events := automation.MatchSchedule(e.automations.EnabledRules(), now, e.scheduleLastFire)
	for _, ev := range events {
		e.scheduleLastFire[ev.Rule.ID] = now
	}
	e.scheduleMu.Unlock()
	if len(events) > 0 {
		e.automations.HandleEvents(ctx, events)
	}
}

func firstDirective(dirs []userdata.Directive, collector string) (userdata.Directive, bool) {
	for _, d := range dirs {
		if d.Enabled && d.Collector == collector {
			return d, true
		}
	}
	return userdata.Directive{}, false
}

func (e *Engine) generateReport(ctx context.Context, modeID string, days int) (string, error) {
	cfg := userdata.ConfigFile{
		AI:             e.cfg.AI,
		Directives:     e.cfg.Directives,
		ExecutionModes: e.cfg.ExecutionModes,
		OutputDir:      e.cfg.OutputDir,
	}
	opts := report.Options{
		ModeID:    modeID,
		Days:      days,
		ConfigDir: e.cfg.ConfigDir,
		Registry:  e.reg,
	}
	if modeID == "goal-alignment" {
		gf, err := goals.Load(goals.Path(e.cfg.ConfigDir))
		if err != nil {
			return "", fmt.Errorf("load goals: %w", err)
		}
		active := goals.ActiveGoals(gf)
		if len(active) == 0 {
			return "", fmt.Errorf("no active goals in goals.yaml")
		}
		opts.Prompt = goals.AlignmentPrompt(active)
		if days <= 0 {
			opts.Days = 7
		}
		// Fall back to recent-activity collection shape when no custom mode exists.
		opts.ModeID = "recent-activity"
	}
	res, err := report.Generate(ctx, cfg, opts)
	if err != nil {
		return "", err
	}
	return res.Markdown, nil
}
