package automation

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// gatedRunner blocks each job until the test releases it, recording the order
// jobs start in and how many run at once.
type gatedRunner struct {
	mu      sync.Mutex
	started []string
	active  int
	peak    int
	gates   map[string]chan struct{}
}

func newGatedRunner(ids ...string) *gatedRunner {
	g := &gatedRunner{gates: map[string]chan struct{}{}}
	for _, id := range ids {
		g.gates[id] = make(chan struct{})
	}
	return g
}

func (g *gatedRunner) Run(_ context.Context, _ Action, ev Event) error {
	// Record the start and the concurrency high-water mark
	id := ev.Signal.StableID
	g.mu.Lock()
	g.started = append(g.started, id)
	g.active++
	if g.active > g.peak {
		g.peak = g.active
	}
	gate := g.gates[id]
	g.mu.Unlock()

	// Hold the slot until released
	<-gate
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
	return nil
}

func (g *gatedRunner) snapshot() ([]string, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.started...), g.peak
}

// waitFor polls until cond holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func limitedDispatcher(max int, g *gatedRunner) (*Dispatcher, Rule) {
	rule := Rule{
		ID: "queue", Enabled: true,
		Trigger:    Trigger{Type: "signal"},
		Conditions: Conditions{MaxConcurrent: max},
		Actions:    []Action{{Type: "gated"}},
	}
	d := NewDispatcher([]Rule{rule})
	d.Registry.Register("gated", g)
	return d, rule
}

func eventFor(rule Rule, id string) Event {
	return Event{Rule: rule, Trigger: "signal", Signal: &model.Signal{StableID: id}}
}

func jobStatuses(d *Dispatcher) map[JobStatus]int {
	out := map[JobStatus]int{}
	for _, j := range d.Store.List(0) {
		out[j.Status]++
	}
	return out
}

func TestMaxConcurrentRunsJobsInOrder(t *testing.T) {
	g := newGatedRunner("a", "b", "c")
	d, rule := limitedDispatcher(1, g)
	d.HandleEvents(context.Background(), []Event{eventFor(rule, "a"), eventFor(rule, "b"), eventFor(rule, "c")})

	// Only the first runs; the others show as queued
	waitFor(t, "a to start", func() bool { s, _ := g.snapshot(); return len(s) == 1 })
	if st := jobStatuses(d); st[JobRunning] != 1 || st[JobQueued] != 2 {
		t.Fatalf("statuses = %v, want 1 running and 2 queued", st)
	}

	// Each finishing job hands its slot to the next in line
	close(g.gates["a"])
	waitFor(t, "b to start", func() bool { s, _ := g.snapshot(); return len(s) == 2 })
	close(g.gates["b"])
	waitFor(t, "c to start", func() bool { s, _ := g.snapshot(); return len(s) == 3 })
	close(g.gates["c"])
	waitFor(t, "all jobs done", func() bool { return jobStatuses(d)[JobDone] == 3 })

	started, peak := g.snapshot()
	if peak != 1 || started[0] != "a" || started[1] != "b" || started[2] != "c" {
		t.Fatalf("started %v with peak %d, want a,b,c one at a time", started, peak)
	}
}

func TestMaxConcurrentUnsetRunsInParallel(t *testing.T) {
	g := newGatedRunner("a", "b")
	d, rule := limitedDispatcher(0, g)
	d.HandleEvents(context.Background(), []Event{eventFor(rule, "a"), eventFor(rule, "b")})

	// Both start without waiting for each other
	waitFor(t, "both to start", func() bool { s, _ := g.snapshot(); return len(s) == 2 })
	close(g.gates["a"])
	close(g.gates["b"])
	waitFor(t, "both done", func() bool { return jobStatuses(d)[JobDone] == 2 })
	if _, peak := g.snapshot(); peak != 2 {
		t.Fatalf("peak = %d, want 2", peak)
	}
}

func TestMaxConcurrentSurvivesSetRules(t *testing.T) {
	g := newGatedRunner("a", "b")
	d, rule := limitedDispatcher(1, g)
	d.HandleEvents(context.Background(), []Event{eventFor(rule, "a")})
	waitFor(t, "a to start", func() bool { s, _ := g.snapshot(); return len(s) == 1 })

	// A reload doesn't reset the running count, so b still waits for a
	d.SetRules([]Rule{rule})
	d.HandleEvents(context.Background(), []Event{eventFor(rule, "b")})
	time.Sleep(30 * time.Millisecond)
	if s, _ := g.snapshot(); len(s) != 1 {
		t.Fatalf("b started while a was running: %v", s)
	}
	close(g.gates["a"])
	waitFor(t, "b to start", func() bool { s, _ := g.snapshot(); return len(s) == 2 })
	close(g.gates["b"])
	waitFor(t, "both done", func() bool { return jobStatuses(d)[JobDone] == 2 })
}

// A later action gets its own timeout budget rather than what remains of an
// earlier long action's.
func TestRunJobGivesEachActionItsOwnContext(t *testing.T) {
	var deadlines []time.Time
	var mu sync.Mutex
	rule := Rule{ID: "r", Enabled: true, Actions: []Action{{Type: "rec"}, {Type: "rec"}}}
	d := NewDispatcher([]Rule{rule})
	d.Registry.Register("rec", RunnerFunc(func(ctx context.Context, _ Action, _ Event) error {
		dl, _ := ctx.Deadline()
		mu.Lock()
		deadlines = append(deadlines, dl)
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return nil
	}))
	d.runJob("job", Event{Rule: rule})
	if len(deadlines) != 2 || !deadlines[1].After(deadlines[0]) {
		t.Fatalf("deadlines = %v, want the second later than the first", deadlines)
	}
}
