package engine

import (
	"context"
	"errors"
	"strings"
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

// TestCollectorErrorRateLimitAlertsWhenItPersists checks that a rate-limit
// failure that clears by the next poll stays quiet, while one that persists
// alerts once.
func TestCollectorErrorRateLimitAlertsWhenItPersists(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{}
	e.reg.Register("fake", f)
	d := userdata.Directive{ID: "gh", Name: "GitHub", Collector: "fake", Enabled: true}
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
		got = append(got, ev.Signal.Summary)
		return nil
	}))

	// A blip that recovers, then a limit that lasts three polls
	limited := errors.New("HTTP 403: You have exceeded a secondary rate limit")
	for _, err := range []error{limited, nil, limited, limited, limited} {
		f.eventsErr = err
		e.collectUnit(context.Background(), u)
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)

	// Expect a single alert, for the limit that persisted
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != limited.Error() {
		t.Fatalf("alerts = %q, want one rate-limit alert", got)
	}
}

// blockingCollector stands in for a collector whose searches outlast the unit's
// deadline: it waits for the context to end and fails the way a killed
// subprocess does.
type blockingCollector struct{}

func (blockingCollector) CollectState(ctx context.Context, _ userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	<-ctx.Done()
	return nil, errors.New("gh api graphql: signal: killed")
}

// TestCollectorTimeoutAlerts checks that a collect cut off by its own deadline
// is reported as a collector_error naming the timeout, while one cancelled by
// its caller (such as a daemon shutdown) is not.
func TestCollectorTimeoutAlerts(t *testing.T) {
	e := newTestEngine(t)
	e.reg.Register("slow", blockingCollector{})

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
		got = append(got, ev.Signal.Fields["directive_id"]+"|"+ev.Signal.Summary)
		return nil
	}))

	// One unit runs out of time; another is cancelled by its caller
	timedOut := e.newUnit(userdata.Directive{ID: "gh", Collector: "slow", Enabled: true}, collectors.ModeState, nil, time.Now())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	e.collectUnit(ctx, timedOut)
	cancel()
	stopped := e.newUnit(userdata.Directive{ID: "gh2", Collector: "slow", Enabled: true}, collectors.ModeState, nil, time.Now())
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	e.collectUnit(ctx, stopped)
	time.Sleep(50 * time.Millisecond)

	// Expect a single alert, for the timeout, that says what happened
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || !strings.HasPrefix(got[0], "gh|collection did not finish within") || !strings.Contains(got[0], "poll.timeout") {
		t.Fatalf("alerts = %q, want one timeout alert for gh", got)
	}
	if e.automationSeeded["gh/state"] {
		t.Fatal("a timed-out collect must not seed the transition baseline")
	}
}

// TestNewUnitPollTimeout checks that poll.timeout overrides the default bound
// on a scheduled collection.
func TestNewUnitPollTimeout(t *testing.T) {
	e := newTestEngine(t)
	d := userdata.Directive{ID: "gh", Collector: "github", Enabled: true}
	if u := e.newUnit(d, collectors.ModeState, nil, time.Now()); u.timeout != backgroundCollectTimeout {
		t.Fatalf("default timeout = %s, want %s", u.timeout, backgroundCollectTimeout)
	}
	mc := &userdata.ModeConfig{Poll: userdata.PollConfig{Timeout: "5m"}}
	if u := e.newUnit(d, collectors.ModeState, mc, time.Now()); u.timeout != 5*time.Minute {
		t.Fatalf("timeout = %s, want 5m", u.timeout)
	}
}
