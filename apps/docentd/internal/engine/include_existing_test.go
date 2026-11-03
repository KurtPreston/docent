package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/model"
)

// queueRule is a transition rule on a task's queued field that records the
// entity IDs it fires for.
func queueRule(t *testing.T, e *Engine, includeExisting bool) func() []string {
	t.Helper()
	var mu sync.Mutex
	var fired []string
	e.automations = automation.NewDispatcher([]automation.Rule{{
		ID: "queue", Enabled: true,
		Trigger: automation.Trigger{
			Type: "transition", Source: "backlog", Kind: []string{"task"},
			IncludeExisting: includeExisting,
			When:            automation.When{Field: "queued", To: "true"},
		},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}})
	e.automations.Registry.Register("shell", automation.RunnerFunc(func(_ context.Context, _ automation.Action, ev automation.Event) error {
		mu.Lock()
		defer mu.Unlock()
		fired = append(fired, ev.Entity.ID)
		return nil
	}))
	return func() []string {
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), fired...)
	}
}

func taskSignal(id, queued string) model.Signal {
	return model.Signal{
		StableID: "task:tasks:" + id, Source: "backlog", Kind: "task", Title: id,
		Fields: map[string]string{"id": id, "queued": queued},
	}
}

// TestIncludeExistingFiresOnFirstCollect checks that a rule with
// include_existing fires once for an entity already queued at startup, and
// not again while it stays queued.
func TestIncludeExistingFiresOnFirstCollect(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{state: []model.Signal{taskSignal("TASK-1", "true"), taskSignal("TASK-2", "false")}}
	e.reg.Register("fake", f)
	fired := queueRule(t, e, true)
	u := e.newUnit(userdata.Directive{ID: "tasks", Collector: "fake", Enabled: true}, collectors.ModeState, nil, time.Now())

	// First and second collect with the same state
	e.collectUnit(context.Background(), u)
	e.collectUnit(context.Background(), u)
	if got := fired(); len(got) != 1 || got[0] != "task:tasks:TASK-1" {
		t.Fatalf("fired = %v, want only TASK-1 once", got)
	}

	// A task queued later still fires as an ordinary transition
	f.state = []model.Signal{taskSignal("TASK-1", "true"), taskSignal("TASK-2", "true")}
	e.collectUnit(context.Background(), u)
	if got := fired(); len(got) != 2 || got[1] != "task:tasks:TASK-2" {
		t.Fatalf("fired = %v, want TASK-2 after TASK-1", got)
	}
}

// TestWithoutIncludeExistingFirstCollectOnlySeeds pins today's behavior: the
// first collect seeds the baseline and fires nothing.
func TestWithoutIncludeExistingFirstCollectOnlySeeds(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{state: []model.Signal{taskSignal("TASK-1", "true")}}
	e.reg.Register("fake", f)
	fired := queueRule(t, e, false)
	u := e.newUnit(userdata.Directive{ID: "tasks", Collector: "fake", Enabled: true}, collectors.ModeState, nil, time.Now())

	e.collectUnit(context.Background(), u)
	e.collectUnit(context.Background(), u)
	if got := fired(); len(got) != 0 {
		t.Fatalf("fired = %v, want nothing", got)
	}
}
