package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// TestCollectorErrorAlertsOncePerOutage checks that a failing unit fires a
// collector_error rule when it starts failing, stays quiet while it keeps
// failing, and fires again after it recovers and fails anew.
func TestCollectorErrorAlertsOncePerOutage(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{}
	e.reg.Register("fake", f)
	d := userdata.Directive{ID: "ci", Name: "CI runs", Collector: "fake", Enabled: true}
	u := e.newUnit(d, collectors.ModeEvents, nil, time.Now())

	// Record each collector_error the rule receives
	var mu sync.Mutex
	var got []string
	e.automations = automation.NewDispatcher([]automation.Rule{{
		ID: "notify", Enabled: true,
		Trigger: automation.Trigger{Type: "signal", Kind: []string{"collector_error"}},
		Actions: []automation.Action{{Type: "shell", Command: "true"}},
	}})
	e.automations.Registry.Register("shell", automation.RunnerFunc(func(_ context.Context, _ automation.Action, ev automation.Event) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, ev.Signal.Source+"|"+ev.Signal.Title+"|"+ev.Signal.Fields["directive_id"]+"|"+ev.Signal.Summary)
		return nil
	}))

	// Fail, fail again, recover, then fail with a new error
	for _, err := range []error{errors.New("token expired"), errors.New("token expired"), nil, errors.New("run az login")} {
		f.eventsErr = err
		e.collectUnit(context.Background(), u)
		time.Sleep(5 * time.Millisecond) // distinct outage timestamps
	}
	time.Sleep(50 * time.Millisecond)

	// Expect one alert per outage, carrying the directive and error
	mu.Lock()
	defer mu.Unlock()
	want := []string{"fake|CI runs|ci|token expired", "fake|CI runs|ci|run az login"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("alerts = %q, want %q", got, want)
	}
}
