package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"github.com/kurt/slakkr-ai/libs/model"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(config.DaemonConfig{}, store)
}

// fakeCollector is registered into the engine's registry to drive collection
// deterministically. It returns whatever it is told for each mode.
type fakeCollector struct {
	state       []model.Signal
	events      []model.Signal
	stateCalls  int
	eventsCalls int
}

func (f *fakeCollector) CollectState(_ context.Context, _ userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	f.stateCalls++
	return f.state, nil
}

func (f *fakeCollector) CollectEvents(_ context.Context, _ userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	f.eventsCalls++
	return f.events, nil
}

func TestMergeEventsAccumulateDedupAgeout(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-time.Hour)
	existing := []model.Signal{
		{StableID: "a", Title: "A", ObservedAt: now.Add(-30 * time.Minute)},
		{StableID: "old", Title: "Old", ObservedAt: now.Add(-2 * time.Hour)}, // before cutoff -> dropped
	}
	incoming := []model.Signal{
		{StableID: "a", Title: "A-updated", ObservedAt: now.Add(-10 * time.Minute)}, // dedup, newer wins
		{StableID: "b", Title: "B", ObservedAt: now.Add(-5 * time.Minute)},
	}
	got := mergeEvents(existing, incoming, cutoff)
	if len(got) != 2 {
		t.Fatalf("expected 2 merged signals (a,b), got %d: %+v", len(got), got)
	}
	byID := map[string]model.Signal{}
	for _, s := range got {
		byID[s.StableID] = s
	}
	if _, ok := byID["old"]; ok {
		t.Error("aged-out signal should have been dropped")
	}
	if byID["a"].Title != "A-updated" {
		t.Errorf("incoming should win on dedup, got title %q", byID["a"].Title)
	}
	if _, ok := byID["b"]; !ok {
		t.Error("new signal b should be present")
	}
}

func TestCollectUnitStateReplacesEventsAccumulate(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{}
	e.reg.Register("fake", f)
	d := userdata.Directive{ID: "fake", Collector: "fake", Enabled: true}

	// State mode replaces.
	stateUnit := e.newUnit(d, collectors.ModeState, nil, time.Now())
	f.state = []model.Signal{{StableID: "s1", Title: "one", ObservedAt: time.Now()}}
	e.collectUnit(context.Background(), stateUnit)
	f.state = []model.Signal{{StableID: "s2", Title: "two", ObservedAt: time.Now()}}
	e.collectUnit(context.Background(), stateUnit)
	if len(stateUnit.signals) != 1 || stateUnit.signals[0].StableID != "s2" {
		t.Fatalf("state mode should replace; got %+v", stateUnit.signals)
	}

	// Events mode accumulates.
	eventsUnit := e.newUnit(d, collectors.ModeEvents, nil, time.Now())
	f.events = []model.Signal{{StableID: "e1", Title: "one", ObservedAt: time.Now()}}
	e.collectUnit(context.Background(), eventsUnit)
	f.events = []model.Signal{{StableID: "e2", Title: "two", ObservedAt: time.Now()}}
	e.collectUnit(context.Background(), eventsUnit)
	if len(eventsUnit.signals) != 2 {
		t.Fatalf("events mode should accumulate; got %+v", eventsUnit.signals)
	}
}

func TestUnitDueLogic(t *testing.T) {
	now := time.Now()
	onLoad := &unit{onLoad: true}
	if !onLoad.due(now, true) {
		t.Error("on_load unit should be due on the initial pass")
	}
	if onLoad.due(now, false) {
		t.Error("on_load unit without interval should not be due after initial pass")
	}
	background := &unit{interval: 5 * time.Minute, nextDue: now.Add(-time.Second)}
	if !background.due(now, false) {
		t.Error("background unit past nextDue should be due")
	}
	future := &unit{interval: 5 * time.Minute, nextDue: now.Add(time.Hour)}
	if future.due(now, false) {
		t.Error("background unit before nextDue should not be due")
	}
	manual := &unit{interval: 0}
	if manual.due(now, false) || manual.due(now, true) {
		t.Error("manual unit (no interval, no on_load) should never be due")
	}
}

func TestRefreshOnRequestCollectsInline(t *testing.T) {
	e := newTestEngine(t)
	f := &fakeCollector{events: []model.Signal{{StableID: "x", Source: "fake", Kind: "fake", Title: "hi", ObservedAt: time.Now()}}}
	e.reg.Register("fake", f)
	d := userdata.Directive{ID: "fake", Collector: "fake", Enabled: true}
	u := e.newUnit(d, collectors.ModeEvents, nil, time.Now())
	u.onRequest = true
	e.units = []*unit{u}

	dash := e.RefreshOnRequest(context.Background())
	if f.eventsCalls != 1 {
		t.Fatalf("on_request unit should be collected once, got %d calls", f.eventsCalls)
	}
	if len(u.signals) != 1 {
		t.Fatalf("expected the collected signal to be cached, got %d", len(u.signals))
	}
	if dash.Backend != "go" {
		t.Fatalf("expected a rebuilt dashboard, got %+v", dash)
	}
}

func TestSignalEntityWorkItemLinks(t *testing.T) {
	e := newTestEngine(t)
	u := &unit{
		key:       unitKey{Directive: "jira", Mode: collectors.ModeState},
		directive: userdata.Directive{ID: "jira", Collector: "jira"},
		collector: "jira",
		mode:      collectors.ModeState,
		signals: []model.Signal{{
			StableID:   "jira:SALSA-123",
			Source:     "jira",
			Kind:       "issue",
			Title:      "SALSA-123 make it work",
			ObservedAt: time.Now(),
		}},
	}
	e.units = []*unit{u}
	e.rebuild()

	sv := e.Signals()
	if len(sv.Units) != 1 || len(sv.Units[0].Signals) != 1 {
		t.Fatalf("expected one unit with one signal, got %+v", sv)
	}
	if got := sv.Units[0].Signals[0].WorkItemKey; got != "SALSA-123" {
		t.Fatalf("signal should link to work item SALSA-123, got %q", got)
	}

	detail, ok := e.WorkItem("SALSA-123")
	if !ok {
		t.Fatal("work item SALSA-123 should exist")
	}
	if len(detail.Signals) != 1 {
		t.Fatalf("work item detail should list its contributing signal, got %d", len(detail.Signals))
	}
}

func TestCollectorsViewReflectsUnits(t *testing.T) {
	e := newTestEngine(t)
	d := userdata.Directive{ID: "fake", Collector: "fake", Enabled: true}
	e.reg.Register("fake", &fakeCollector{})
	u := e.newUnit(d, collectors.ModeEvents, &userdata.ModeConfig{Poll: userdata.PollConfig{Interval: "15m", OnRequest: true}}, time.Now())
	e.units = []*unit{u}

	cv := e.Collectors()
	if len(cv.Units) != 1 {
		t.Fatalf("expected one unit row, got %d", len(cv.Units))
	}
	row := cv.Units[0]
	if row.DirectiveID != "fake" || row.Mode != "events" || row.Interval != "15m0s" || !row.OnRequest {
		t.Fatalf("unexpected collector row: %+v", row)
	}
}
