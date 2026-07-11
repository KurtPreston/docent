package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"sync"
	"time"
)

// Dispatcher matches events against cooldown and runs actions asynchronously.
type Dispatcher struct {
	Rules    []Rule
	Registry *Registry
	Store    *Store
	Log      *log.Logger

	mu sync.Mutex
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
	d.Store.Start(id, ev.Rule.ID, key, now)
	// Detach from the caller's context so a short rebuild doesn't cancel work.
	go d.runJob(id, ev)
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
	for _, action := range ev.Rule.Actions {
		if err := d.Registry.Run(ctx, action, ev); err != nil {
			lastErr = err
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

func (d *Dispatcher) runJob(id string, ev Event) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	var lastErr error
	for _, action := range ev.Rule.Actions {
		if err := d.Registry.Run(ctx, action, ev); err != nil {
			lastErr = err
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
