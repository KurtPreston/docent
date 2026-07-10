package engine

import (
	"context"
	"fmt"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
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
}

func firstDirective(dirs []userdata.Directive, collector string) (userdata.Directive, bool) {
	for _, d := range dirs {
		if d.Enabled && d.Collector == collector {
			return d, true
		}
	}
	return userdata.Directive{}, false
}
