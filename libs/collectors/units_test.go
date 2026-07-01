package collectors

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

type stateOnlyCollector struct{ calls *int }

func (s stateOnlyCollector) CollectState(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*s.calls++
	return []StatusItem{{DirectiveID: d.ID, Source: d.Collector, Kind: "state", Title: "s"}}, nil
}

type eventOnlyCollector struct{ calls *int }

func (e eventOnlyCollector) CollectEvents(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*e.calls++
	return []StatusItem{{DirectiveID: d.ID, Source: d.Collector, Kind: "event", Title: "e"}}, nil
}

type dualCollector struct {
	state  *int
	events *int
}

func (c dualCollector) CollectState(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*c.state++
	return []StatusItem{{DirectiveID: d.ID, Kind: "state"}}, nil
}

func (c dualCollector) CollectEvents(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*c.events++
	return []StatusItem{{DirectiveID: d.ID, Kind: "event"}}, nil
}

func TestRegistryCapabilities(t *testing.T) {
	r := NewRegistry(time.Now)
	cases := []struct {
		name        string
		wantState   bool
		wantEvents  bool
	}{
		{"wsm", true, false},
		{"slack", false, true},
		{"local-git", false, true},
		{"google-calendar", false, true},
		{"webhook", false, true},
		{"jira", true, true},
		{"github", true, true},
		{"gitea", true, true},
	}
	for _, tc := range cases {
		state, events := r.Capabilities(tc.name)
		if state != tc.wantState || events != tc.wantEvents {
			t.Errorf("%s: state=%v events=%v, want state=%v events=%v", tc.name, state, events, tc.wantState, tc.wantEvents)
		}
	}
	if state, events := r.Capabilities("nope"); state || events {
		t.Errorf("unknown collector should report no capabilities")
	}
}

func TestCollectUnitDispatch(t *testing.T) {
	var stateCalls, eventCalls int
	r := NewRegistry(time.Now)
	r.Register("dual", dualCollector{state: &stateCalls, events: &eventCalls})

	d := userdata.Directive{ID: "d", Collector: "dual", Enabled: true}
	if _, err := r.CollectUnit(context.Background(), d, ModeState, &CollectOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CollectUnit(context.Background(), d, ModeEvents, &CollectOpts{}); err != nil {
		t.Fatal(err)
	}
	if stateCalls != 1 || eventCalls != 1 {
		t.Fatalf("dispatch counts: state=%d events=%d, want 1/1", stateCalls, eventCalls)
	}
}

func TestCollectUnitCapabilityMismatch(t *testing.T) {
	var sc, ec int
	r := NewRegistry(time.Now)
	r.Register("state-only", stateOnlyCollector{calls: &sc})
	r.Register("event-only", eventOnlyCollector{calls: &ec})

	if _, err := r.CollectUnit(context.Background(), userdata.Directive{ID: "a", Collector: "state-only"}, ModeEvents, &CollectOpts{}); err == nil {
		t.Fatal("expected error: state-only collector cannot do events")
	}
	if _, err := r.CollectUnit(context.Background(), userdata.Directive{ID: "b", Collector: "event-only"}, ModeState, &CollectOpts{}); err == nil {
		t.Fatal("expected error: event-only collector cannot do state")
	}
	if _, err := r.CollectUnit(context.Background(), userdata.Directive{ID: "c", Collector: "ghost"}, ModeState, &CollectOpts{}); err == nil {
		t.Fatal("expected error: unknown collector")
	}
}

// TestJiraStateJQLOmitsUpdatedFilter is the key state-vs-events distinction:
// the events JQL pins a `updated >=` window; the state JQL must not, so
// unchanged-but-current issues still appear.
func TestJiraStateJQLOmitsUpdatedFilter(t *testing.T) {
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := buildJiraActivityJQL("project = FOO", since, ScopeInvolved, nil)
	state := buildJiraStateJQL("project = FOO", ScopeInvolved, nil)

	if !strings.Contains(events, "updated >=") {
		t.Fatalf("events JQL should carry an updated window: %q", events)
	}
	if strings.Contains(state, "updated >=") {
		t.Fatalf("state JQL must not carry an updated window: %q", state)
	}
	if !strings.Contains(state, "project = FOO") {
		t.Fatalf("state JQL should preserve the user query: %q", state)
	}
}
