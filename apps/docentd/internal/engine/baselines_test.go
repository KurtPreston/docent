package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/model"
)

// headRule is a transition rule on any change to a PR's head_sha that records
// the head it fires for.
func headRule(e *Engine) func() []string {
	var mu sync.Mutex
	var fired []string
	e.automations = automation.NewDispatcher([]automation.Rule{{
		ID: "review", Enabled: true,
		Trigger: automation.Trigger{
			Type: "transition", Source: "github", Kind: []string{"pr_review_status"},
			When: automation.When{Field: "head_sha"},
		},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}})
	e.automations.Registry.Register("shell", automation.RunnerFunc(func(_ context.Context, _ automation.Action, ev automation.Event) error {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, ev.To)
		return nil
	}))
	return func() []string {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), fired...)
	}
}

func prSignal(head string) model.Signal {
	return model.Signal{
		StableID: "pr:my-org/my-repo#1", Source: "github", Kind: "pr_review_status", Title: "Add a thing",
		URL:    "https://github.com/my-org/my-repo/pull/1",
		Fields: map[string]string{"head_sha": head},
	}
}

// restartedEngine builds a fresh engine sharing dir's baselines, as docentd
// would after a restart, with one state unit fed by f.
func restartedEngine(t *testing.T, dir string, f *fakeCollector) (*Engine, *unit) {
	t.Helper()
	e := newTestEngine(t)
	e.baselines = &baselineStore{dir: dir}
	e.reg.Register("fake", f)
	u := e.newUnit(userdata.Directive{ID: "gh", Collector: "fake", Enabled: true}, collectors.ModeState, nil, time.Now())
	return e, u
}

// TestRestartFiresTransitionsSinceSavedBaseline checks that a push made while
// docentd was down fires on the first collect after it restarts.
func TestRestartFiresTransitionsSinceSavedBaseline(t *testing.T) {
	dir := t.TempDir()

	// The first run sees head aaa and saves it as the baseline
	e1, u1 := restartedEngine(t, dir, &fakeCollector{state: []model.Signal{prSignal("aaa")}})
	fired1 := headRule(e1)
	e1.collectUnit(context.Background(), u1)
	if got := fired1(); len(got) != 0 {
		t.Fatalf("first run fired %v, want nothing", got)
	}

	// After a restart the PR's head has moved to bbb
	f := &fakeCollector{state: []model.Signal{prSignal("bbb")}}
	e2, u2 := restartedEngine(t, dir, f)
	fired2 := headRule(e2)
	e2.collectUnit(context.Background(), u2)
	if got := fired2(); len(got) != 1 || got[0] != "bbb" {
		t.Fatalf("after restart fired %v, want [bbb]", got)
	}

	// An unchanged head on the next collect fires nothing more
	e2.collectUnit(context.Background(), u2)
	if got := fired2(); len(got) != 1 {
		t.Fatalf("unchanged head fired again: %v", got)
	}
}

// TestRestartWithUnchangedStateFiresNothing checks that restoring a baseline
// doesn't replay entities that didn't change while docentd was down.
func TestRestartWithUnchangedStateFiresNothing(t *testing.T) {
	dir := t.TempDir()
	f := &fakeCollector{state: []model.Signal{prSignal("aaa")}}

	// Collect once, restart, and collect the same state again
	e1, u1 := restartedEngine(t, dir, f)
	headRule(e1)
	e1.collectUnit(context.Background(), u1)
	e2, u2 := restartedEngine(t, dir, f)
	fired := headRule(e2)
	e2.collectUnit(context.Background(), u2)
	if got := fired(); len(got) != 0 {
		t.Fatalf("fired = %v, want nothing", got)
	}
}

// TestRestartDoesNotDoubleFireIncludeExisting checks that an include_existing
// rule fires once for an entity that changed while docentd was down, not once
// for the restored baseline and again as an existing entity.
func TestRestartDoesNotDoubleFireIncludeExisting(t *testing.T) {
	dir := t.TempDir()
	newTasks := func(queued string) (*Engine, *unit, func() []string) {
		e := newTestEngine(t)
		e.baselines = &baselineStore{dir: dir}
		e.reg.Register("fake", &fakeCollector{state: []model.Signal{taskSignal("TASK-1", queued)}})
		fired := queueRule(t, e, true)
		u := e.newUnit(userdata.Directive{ID: "tasks", Collector: "fake", Enabled: true}, collectors.ModeState, nil, time.Now())
		return e, u, fired
	}

	// Save an unqueued task, then restart with it queued
	e1, u1, _ := newTasks("false")
	e1.collectUnit(context.Background(), u1)
	e2, u2, fired := newTasks("true")
	e2.collectUnit(context.Background(), u2)
	if got := fired(); len(got) != 1 {
		t.Fatalf("fired = %v, want TASK-1 once", got)
	}
}

// TestBaselineStore covers the round trip, a missing snapshot, a corrupt one,
// and that a unit key can't escape the directory.
func TestBaselineStore(t *testing.T) {
	dir := t.TempDir()
	s := &baselineStore{dir: dir}
	if got := s.load("gh/state"); got != nil {
		t.Fatalf("missing snapshot = %v, want nil", got)
	}

	// Save and reload a snapshot
	want := map[string]model.Entity{"pr:1": {ID: "pr:1", State: map[string]string{"head_sha": "aaa"}}}
	if err := s.save("../gh/state", want); err != nil {
		t.Fatal(err)
	}
	if got := s.load("../gh/state"); got["pr:1"].State["head_sha"] != "aaa" {
		t.Fatalf("reloaded = %v", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("dir holds %d entries, want the one snapshot", len(entries))
	}

	// A corrupt snapshot reads as missing
	if err := os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := s.load("../gh/state"); got != nil {
		t.Fatalf("corrupt snapshot = %v, want nil", got)
	}
}
