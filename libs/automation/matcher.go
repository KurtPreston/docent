package automation

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
)

// MatchOpts carries optional correlation config for ticket-key extraction.
type MatchOpts struct {
	CorrCfg correlation.Config
	Now     time.Time
}

// MatchSignals evaluates signal-type rules against newly observed signals.
// Only rules with Trigger.Type == "signal" (or empty, treated as signal) are
// considered. Returns one Event per (rule, signal) match.
func MatchSignals(rules []Rule, signals []model.Signal, opts MatchOpts) []Event {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	var out []Event
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		t := strings.TrimSpace(rule.Trigger.Type)
		if t != "" && t != "signal" {
			continue
		}
		for i := range signals {
			sig := &signals[i]
			if ev, ok := matchSignalRule(rule, sig, opts, now); ok {
				out = append(out, ev)
			}
		}
	}
	return out
}

// MatchTransitions evaluates transition-type rules against state changes.
// prev and next are keyed by entity StableID / entity ID. Entities carry an
// optional State["is_self"]="true" marker (stamped by the engine from the
// originating signal) so the "me" sentinel and the self condition can be
// evaluated without an IsSelf field on model.Entity.
func MatchTransitions(rules []Rule, prev, next map[string]model.Entity, opts MatchOpts) []Event {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	var out []Event
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		if strings.TrimSpace(rule.Trigger.Type) != "transition" {
			continue
		}
		field := strings.TrimSpace(rule.Trigger.When.Field)
		if field == "" {
			continue
		}
		wantFrom := strings.TrimSpace(rule.Trigger.When.From)
		wantTo := strings.TrimSpace(rule.Trigger.When.To)
		for id, ent := range next {
			if !sourceKindMatch(rule.Trigger, ent) {
				continue
			}
			newVal := ""
			newSelf := false
			if ent.State != nil {
				newVal = ent.State[field]
				newSelf = ent.State["is_self"] == "true"
			}
			oldVal := ""
			oldSelf := false
			if old, ok := prev[id]; ok && old.State != nil {
				oldVal = old.State[field]
				oldSelf = old.State["is_self"] == "true"
			}
			if oldVal == newVal {
				continue
			}
			if !transitionValueMatch(wantFrom, oldVal, oldSelf) {
				continue
			}
			if !transitionValueMatch(wantTo, newVal, newSelf) {
				continue
			}
			if !conditionsOK(rule.Conditions, nil, &ent) {
				continue
			}
			entCopy := ent
			out = append(out, Event{
				Rule:      rule,
				Trigger:   "transition",
				Entity:    &entCopy,
				From:      oldVal,
				To:        newVal,
				FiredAt:   now,
				TicketKey: ticketFromEntity(ent, opts.CorrCfg),
			})
		}
	}
	return out
}

func matchSignalRule(rule Rule, sig *model.Signal, opts MatchOpts, now time.Time) (Event, bool) {
	tr := rule.Trigger
	if src := strings.TrimSpace(tr.Source); src != "" && !strings.EqualFold(src, sig.Source) {
		return Event{}, false
	}
	if len(tr.Kind) > 0 && !kindIn(tr.Kind, sig.Kind) {
		return Event{}, false
	}
	for k, v := range tr.Match.Fields {
		if sig.Fields == nil || sig.Fields[k] != v {
			return Event{}, false
		}
	}
	var captures []string
	if text := strings.TrimSpace(tr.Match.Text); text != "" {
		re, err := regexp.Compile(text)
		if err != nil {
			return Event{}, false
		}
		hay := sig.Title + "\n" + sig.Summary
		m := re.FindStringSubmatch(hay)
		if m == nil {
			return Event{}, false
		}
		captures = m
	}
	ticketKey := ""
	if tr.Match.TicketKey {
		hay := sig.Title + " " + sig.Summary
		if sig.Fields != nil {
			if k := sig.Fields["key"]; k != "" {
				hay = k + " " + hay
			}
		}
		ticketKey = extractTicketKey(hay, opts.CorrCfg)
		if ticketKey == "" {
			return Event{}, false
		}
	}
	if !conditionsOK(rule.Conditions, sig, nil) {
		return Event{}, false
	}
	sigCopy := *sig
	return Event{
		Rule:      rule,
		Trigger:   "signal",
		Signal:    &sigCopy,
		TicketKey: ticketKey,
		Match:     captures,
		FiredAt:   now,
	}, true
}

// sourceKinds maps a trigger `source` to the entity kinds its collector
// actually produces. An entity's Kind equals its originating signal Kind
// (see correlation.SignalToEntity), so these are signal kinds. Sources not
// listed here are not gated on kind — any entity kind is accepted.
var sourceKinds = map[string]map[string]bool{
	"jira":              {"issue": true, "issue_activity": true, "ticket": true},
	"github":            {"pr_review_status": true, "pr": true, "pr_activity": true, "issue": true, "issue_activity": true},
	"github-enterprise": {"pr_review_status": true, "pr": true, "pr_activity": true, "issue": true, "issue_activity": true},
	"gitea":             {"pr_review_status": true, "pr": true, "pr_activity": true, "issue": true, "issue_activity": true},
}

// kindAliases lets a rule use a friendly `kind` and still match the concrete
// entity kind (e.g. `kind: pr` matches `pr_review_status`, and vice versa).
var kindAliases = map[string][]string{
	"pr":               {"pr_review_status", "pr_activity"},
	"pr_review_status": {"pr"},
	"pr_activity":      {"pr"},
	"ticket":           {"issue", "issue_activity"},
	"issue":            {"issue_activity", "ticket"},
	"issue_activity":   {"issue", "ticket"},
}

func sourceKindMatch(tr Trigger, ent model.Entity) bool {
	if src := strings.ToLower(strings.TrimSpace(tr.Source)); src != "" {
		if allowed, known := sourceKinds[src]; known && !allowed[ent.Kind] {
			return false
		}
	}
	if len(tr.Kind) == 0 {
		return true
	}
	if kindIn(tr.Kind, ent.Kind) {
		return true
	}
	if ent.State != nil && kindIn(tr.Kind, ent.State["kind"]) {
		return true
	}
	for _, k := range tr.Kind {
		for _, alias := range kindAliases[strings.ToLower(strings.TrimSpace(k))] {
			if strings.EqualFold(alias, ent.Kind) {
				return true
			}
		}
	}
	return false
}

func kindIn(kinds KindSpec, kind string) bool {
	for _, k := range kinds {
		if strings.EqualFold(strings.TrimSpace(k), kind) {
			return true
		}
	}
	return false
}

// valueMatch compares a wanted value against an observed one case-insensitively.
// Empty want always matches.
func valueMatch(want, got string) bool {
	if want == "" {
		return true
	}
	return strings.EqualFold(want, got)
}

// transitionValueMatch matches a When.from/to spec against a field value for a
// transition. The sentinel "me" matches when the entity belongs to the current
// user (is_self, stamped by the engine from the originating signal's IsSelf)
// and the value is non-empty — i.e. a field that transitioned *to* the user
// (e.g. assignee -> me), not one that was cleared. Empty want always matches.
func transitionValueMatch(want, got string, isSelf bool) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return true
	}
	if strings.EqualFold(want, "me") {
		return isSelf && strings.TrimSpace(got) != ""
	}
	return strings.EqualFold(want, got)
}

func conditionsOK(c Conditions, sig *model.Signal, ent *model.Entity) bool {
	if c.Self != nil {
		want := *c.Self
		got := false
		if sig != nil {
			got = sig.IsSelf
		} else if ent != nil && ent.State != nil {
			// Transitions have no signal; the engine stamps is_self onto the
			// entity so self conditions still work.
			got = ent.State["is_self"] == "true"
		}
		if want != got {
			return false
		}
	}
	if len(c.Repos) > 0 {
		repo := ""
		if sig != nil {
			repo = sig.Repository
			if repo == "" && sig.Fields != nil {
				repo = sig.Fields["repo"]
			}
		}
		if ent != nil && repo == "" {
			repo = ent.Coordinates["repo"]
		}
		if !repoIn(c.Repos, repo) {
			return false
		}
	}
	return true
}

func repoIn(repos []string, repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	for _, r := range repos {
		if strings.EqualFold(strings.TrimSpace(r), repo) {
			return true
		}
	}
	return false
}

func extractTicketKey(hay string, cfg correlation.Config) string {
	if k := correlation.ScanTicketKey(hay, cfg); k != "" {
		return k
	}
	return correlation.ParseTicketKey(hay, cfg)
}

func ticketFromEntity(ent model.Entity, cfg correlation.Config) string {
	if ent.Kind == "ticket" {
		if k := correlation.ParseTicketKey(ent.Title, cfg); k != "" {
			return k
		}
		if ent.Coordinates != nil {
			if k := correlation.ParseTicketKey(ent.Coordinates["key"], cfg); k != "" {
				return k
			}
		}
	}
	if ent.Coordinates != nil {
		if k := correlation.ParseTicketKey(ent.Coordinates["ticket"], cfg); k != "" {
			return k
		}
	}
	return ""
}

// EventContext builds a Context from an Event for templating / env.
func EventContext(ev Event) Context {
	ctx := Context{
		RuleID:  ev.Rule.ID,
		Match:   ev.Match,
		From:    ev.From,
		To:      ev.To,
		FiredAt: ev.FiredAt,
		Ticket:  TicketRef{Key: ev.TicketKey},
		Fields:  map[string]string{},
	}
	if ev.Signal != nil {
		ctx.Source = ev.Signal.Source
		ctx.Kind = ev.Signal.Kind
		ctx.Title = ev.Signal.Title
		ctx.Summary = ev.Signal.Summary
		ctx.URL = ev.Signal.URL
		ctx.Repo = ev.Signal.Repository
		ctx.StableID = ev.Signal.StableID
		ctx.IsSelf = ev.Signal.IsSelf
		if ev.Signal.Fields != nil {
			for k, v := range ev.Signal.Fields {
				ctx.Fields[k] = v
			}
			if ctx.Repo == "" {
				ctx.Repo = ev.Signal.Fields["repo"]
			}
			if ctx.Branch == "" {
				ctx.Branch = ev.Signal.Fields["head_branch"]
				if ctx.Branch == "" {
					ctx.Branch = ev.Signal.Fields["branch"]
				}
			}
			if ctx.Ticket.Key == "" {
				ctx.Ticket.Key = ev.Signal.Fields["key"]
			}
		}
	}
	if ev.Entity != nil {
		if ctx.Title == "" {
			ctx.Title = ev.Entity.Title
		}
		if ctx.URL == "" {
			ctx.URL = ev.Entity.URL
		}
		if ctx.StableID == "" {
			ctx.StableID = ev.Entity.ID
		}
		if ev.Entity.Coordinates != nil {
			if ctx.Repo == "" {
				ctx.Repo = ev.Entity.Coordinates["repo"]
			}
			if ctx.Branch == "" {
				ctx.Branch = ev.Entity.Coordinates["head_branch"]
				if ctx.Branch == "" {
					ctx.Branch = ev.Entity.Coordinates["branch"]
				}
			}
			if ctx.OpenPath == "" {
				ctx.OpenPath = ev.Entity.Coordinates["path"]
			}
		}
		if ev.Entity.State != nil {
			for k, v := range ev.Entity.State {
				if _, ok := ctx.Fields[k]; !ok {
					ctx.Fields[k] = v
				}
			}
		}
	}
	if ev.WorkItem != nil {
		if ctx.Repo == "" {
			ctx.Repo = ev.WorkItem.Repo
		}
		if ctx.Branch == "" {
			ctx.Branch = ev.WorkItem.Branch
		}
		if ctx.OpenPath == "" {
			ctx.OpenPath = ev.WorkItem.OpenPath
		}
		if ctx.Title == "" {
			ctx.Title = ev.WorkItem.Title
		}
		if len(ev.WorkItem.Tickets) > 0 && ctx.Ticket.Key == "" {
			ctx.Ticket.Key = ev.WorkItem.Tickets[0].Key
			ctx.Ticket.Title = ev.WorkItem.Tickets[0].Title
			ctx.Ticket.URL = ev.WorkItem.Tickets[0].URL
		}
	}
	return ctx
}

// DedupeKey returns the cooldown/idempotency key for an event.
func DedupeKey(ev Event) string {
	if k := strings.TrimSpace(ev.Rule.Conditions.DedupeKey); k != "" {
		return fmt.Sprintf("%s:%s", ev.Rule.ID, k)
	}
	stable := ""
	if ev.Signal != nil && ev.Signal.StableID != "" {
		stable = ev.Signal.StableID
	} else if ev.Entity != nil {
		stable = ev.Entity.ID
	}
	if ev.Trigger == "transition" {
		return fmt.Sprintf("%s:%s:%s->%s", ev.Rule.ID, stable, ev.From, ev.To)
	}
	return fmt.Sprintf("%s:%s", ev.Rule.ID, stable)
}
