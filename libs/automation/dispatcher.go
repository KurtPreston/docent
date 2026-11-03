package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// maxActionErrorLen caps how much of a single action's error text is kept
// in the accumulated PriorActionError chain, so one verbose failure (e.g. a
// captured agent stdout/stderr blob) can't blow up the env var / template
// payload passed to later actions.
const maxActionErrorLen = 2000

// describeActionError formats one action's failure for the chain, truncating
// long error text (long stdout/stderr capture) to keep it notification-sized.
func describeActionError(actionType string, err error) string {
	msg := err.Error()
	if len(msg) > maxActionErrorLen {
		msg = msg[:maxActionErrorLen] + "… (truncated)"
	}
	return fmt.Sprintf("%s: %s", actionType, msg)
}

// Dispatcher matches events against cooldown and runs actions asynchronously.
type Dispatcher struct {
	Rules    []Rule
	Registry *Registry
	Store    *Store
	Log      *log.Logger

	mu     sync.Mutex
	limits ruleLimiter
}

// NewDispatcher builds a dispatcher with the given rules and a default registry/store.
func NewDispatcher(rules []Rule) *Dispatcher {
	return &Dispatcher{
		Rules:    rules,
		Registry: NewRegistry(),
		Store:    NewStore(),
	}
}

// EnabledRules returns a copy of enabled rules.
func (d *Dispatcher) EnabledRules() []Rule {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Rule, 0, len(d.Rules))
	for _, r := range d.Rules {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out
}

// SetRules replaces the rule list (e.g. after config reload).
func (d *Dispatcher) SetRules(rules []Rule) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Rules = rules
}

// HandleEvents applies cooldown and dispatches matched events.
func (d *Dispatcher) HandleEvents(ctx context.Context, events []Event) {
	if len(events) == 0 {
		return
	}
	now := time.Now()
	for _, ev := range events {
		d.handleOne(ctx, ev, now)
	}
}

func (d *Dispatcher) handleOne(ctx context.Context, ev Event, now time.Time) {
	key := DedupeKey(ev)
	cooldown := ""
	if ev.Rule.Conditions.Cooldown != "" {
		cooldown = ev.Rule.Conditions.Cooldown
	}
	id := newJobID()
	if d.Store.ShouldSkip(key, cooldown, now) {
		d.Store.Skip(id, ev.Rule.ID, key, "cooldown", now)
		d.logf("automation %s skipped (cooldown) key=%s", ev.Rule.ID, key)
		return
	}
	// Without a limit, start right away. Detach from the caller's context so
	// a short rebuild doesn't cancel work.
	max := ev.Rule.Conditions.MaxConcurrent
	if max <= 0 {
		d.Store.Start(id, ev.Rule.ID, key, now)
		go d.runJob(id, ev)
		return
	}

	// With max_concurrent, take a slot or wait in line for one
	wait := d.limits.acquire(ev.Rule.ID, max)
	if wait == nil {
		d.Store.Start(id, ev.Rule.ID, key, now)
	} else {
		d.Store.Queue(id, ev.Rule.ID, key, now)
		d.logf("automation %s queued (max_concurrent=%d) key=%s", ev.Rule.ID, max, key)
	}
	go func() {
		defer d.limits.release(ev.Rule.ID)
		if wait != nil {
			<-wait
			d.Store.Run(id, time.Now())
			d.logf("automation %s started after waiting key=%s", ev.Rule.ID, key)
		}
		d.runJob(id, ev)
	}()
}

// RunRuleNow runs a rule's actions immediately, bypassing cooldown/dedupe, and
// waits for completion. Intended for manual testing via the API. ev.Rule must
// be populated; ev's other fields (Signal, From/To, TicketKey, …) provide the
// template context actions render against.
func (d *Dispatcher) RunRuleNow(ctx context.Context, ev Event) Job {
	id := newJobID()
	now := time.Now()
	d.Store.Start(id, ev.Rule.ID, "manual:"+ev.Rule.ID, now)
	var lastErr error
	var priorErrs []string
	for _, action := range ev.Rule.Actions {
		ev.PriorActionError = strings.Join(priorErrs, "; ")
		out, err := d.Registry.RunOutput(ctx, action, ev)
		if out = strings.TrimSpace(out); out != "" {
			ev.PriorAgentOutput = out
		}
		if err != nil {
			lastErr = err
			priorErrs = append(priorErrs, describeActionError(action.Type, err))
			d.logf("automation %s (manual) action %s failed: %v", ev.Rule.ID, action.Type, err)
		}
	}
	now = time.Now()
	if lastErr != nil {
		d.Store.Fail(id, lastErr.Error(), now)
	} else {
		d.Store.Finish(id, "ok", now)
		d.logf("automation %s (manual) completed", ev.Rule.ID)
	}
	if out, ok := d.Store.Get(id); ok {
		return out
	}
	return Job{ID: id, RuleID: ev.Rule.ID}
}

// actionTimeout bounds each action of a dispatched job. It is per action, not
// per job: an agent-inline action can run longer than this on its own budget,
// and a notifier after it must still get its full time.
const actionTimeout = 15 * time.Minute

func (d *Dispatcher) runJob(id string, ev Event) {
	var lastErr error
	var priorErrs []string
	for _, action := range ev.Rule.Actions {
		ev.PriorActionError = strings.Join(priorErrs, "; ")
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		out, err := d.Registry.RunOutput(ctx, action, ev)
		cancel()
		if out = strings.TrimSpace(out); out != "" {
			ev.PriorAgentOutput = out
		}
		if err != nil {
			lastErr = err
			priorErrs = append(priorErrs, describeActionError(action.Type, err))
			d.logf("automation %s action %s failed: %v", ev.Rule.ID, action.Type, err)
			// Continue remaining actions; record overall failure at the end.
		}
	}
	now := time.Now()
	if lastErr != nil {
		d.Store.Fail(id, lastErr.Error(), now)
		return
	}
	d.Store.Finish(id, "ok", now)
	d.logf("automation %s completed", ev.Rule.ID)
}

func (d *Dispatcher) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
		return
	}
	log.Printf("docentd: "+format, args...)
}

func newJobID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
