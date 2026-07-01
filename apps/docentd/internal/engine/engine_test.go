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

func TestClassifyGroup(t *testing.T) {
	cases := []struct {
		name       string
		facts      groupFacts
		wantStatus string
		wantRank   int
		wantAction bool
	}{
		{"live session needs followup", groupFacts{hasLiveSession: true, sessionNeedsFollowup: true, branchEvidence: true}, statusActive, rankActive, true},
		{"live session no followup", groupFacts{hasLiveSession: true}, statusActive, rankActive, false},
		{"approved beats started", groupFacts{authoredApproved: true, branchEvidence: true}, statusApproved, rankApproved, true},
		{"jira started no branch", groupFacts{jiraStarted: true}, statusStarted, rankStarted, false},
		{"draft pr is started", groupFacts{authoredDraft: true}, statusStarted, rankStarted, false},
		{"branch evidence is started with action", groupFacts{branchEvidence: true}, statusStarted, rankStarted, true},
		{"authored awaiting waits on others", groupFacts{authoredAwaiting: true}, statusAwaiting, rankAwaiting, false},
		{"authored my turn", groupFacts{authoredAwaiting: true, authoredMyTurn: true}, statusAwaiting, rankAwaiting, true},
		{"review requested needs action", groupFacts{reviewRequested: true}, statusAwaiting, rankAwaiting, true},
		{"assigned no action", groupFacts{jiraAssigned: true}, statusAssigned, rankAssigned, false},
		{"nothing hidden", groupFacts{}, "", rankHidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, r, a := classifyGroup(tc.facts)
			if s != tc.wantStatus || r != tc.wantRank || a != tc.wantAction {
				t.Errorf("classifyGroup = (%q,%d,%v), want (%q,%d,%v)", s, r, a, tc.wantStatus, tc.wantRank, tc.wantAction)
			}
		})
	}
}

func TestClassifyPR(t *testing.T) {
	mk := func(state map[string]string) model.Entity {
		return model.Entity{Kind: "pr_review_status", State: state}
	}
	var f groupFacts
	classifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "APPROVED", "checks": "passing"}))
	if !f.authoredApproved {
		t.Error("approved+passing authored PR should set authoredApproved")
	}
	f = groupFacts{}
	classifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "true"}))
	if !f.authoredDraft || f.authoredApproved {
		t.Errorf("draft PR facts wrong: %+v", f)
	}
	f = groupFacts{}
	classifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "CHANGES_REQUESTED", "checks": "passing"}))
	if !f.authoredAwaiting || !f.authoredMyTurn {
		t.Errorf("changes-requested should be awaiting+my-turn: %+v", f)
	}
	f = groupFacts{}
	classifyPR(&f, mk(map[string]string{"relation": "review_requested"}))
	if !f.reviewRequested || f.authoredAwaiting {
		t.Errorf("review_requested facts wrong: %+v", f)
	}
}

func TestPrNumberFromURL(t *testing.T) {
	tests := map[string]int{
		"https://github.com/o/r/pull/123":      123,
		"https://git.example/o/r/pull/7/files": 7,
		"https://github.com/o/r/issues/5":      0,
		"":                                     0,
	}
	for url, want := range tests {
		if got := prNumberFromURL(url); got != want {
			t.Errorf("prNumberFromURL(%q) = %d, want %d", url, got, want)
		}
	}
}

func TestExpandJiraTierDirectives(t *testing.T) {
	d := userdata.Directive{
		ID:        "jira",
		Collector: "jira",
		Config: map[string]string{
			"base_url":       "https://jira.example",
			"started_query":  `status = "In Development"`,
			"assigned_query": `status in ("To Do")`,
		},
	}
	got := expandJiraTierDirectives(d)
	if len(got) != 2 {
		t.Fatalf("expected 2 tier directives, got %d", len(got))
	}
	byTier := map[string]userdata.Directive{}
	for _, td := range got {
		byTier[td.Config["status_tier"]] = td
	}
	started, ok := byTier["started"]
	if !ok {
		t.Fatal("missing started tier directive")
	}
	if started.ID != "jira#started" {
		t.Errorf("started ID = %q, want jira#started", started.ID)
	}
	if started.Config["query"] != `status = "In Development"` {
		t.Errorf("started query = %q", started.Config["query"])
	}
	// Base config must be preserved (base_url) and the original untouched.
	if started.Config["base_url"] != "https://jira.example" {
		t.Errorf("base_url not carried over: %v", started.Config)
	}
	if _, ok := d.Config["query"]; ok {
		t.Error("expansion mutated the source directive config")
	}
	// Non-jira and jira-without-tier-queries return nil.
	if expandJiraTierDirectives(userdata.Directive{Collector: "github"}) != nil {
		t.Error("non-jira should return nil")
	}
	if expandJiraTierDirectives(userdata.Directive{Collector: "jira", Config: map[string]string{"base_url": "x"}}) != nil {
		t.Error("jira without tier queries should return nil")
	}
}

func TestBuildDashboardOrderingAndVisibility(t *testing.T) {
	e := newTestEngine(t)
	wi := func(key string, ents ...model.Entity) model.WorkItem {
		return model.WorkItem{Key: key, Title: key, Entities: ents}
	}
	sess := func(ticket string, live bool, attention string) model.Entity {
		return model.Entity{Kind: "session", Title: ticket, Coordinates: map[string]string{"ticket": ticket}, State: map[string]string{"live": boolStr(live), "attention": attention}}
	}
	pr := func(state map[string]string) model.Entity {
		return model.Entity{Kind: "pr_review_status", URL: "https://github.com/o/r/pull/1", State: state, Coordinates: map[string]string{}}
	}
	jira := func(tier, status string) model.Entity {
		return model.Entity{Kind: "issue", State: map[string]string{"status_tier": tier, "status": status}}
	}
	reflog := func() model.Entity {
		return model.Entity{Kind: "reflog", Coordinates: map[string]string{}, State: map[string]string{}}
	}

	items := []model.WorkItem{
		wi("SALSA-5", jira("assigned", "To Do")),
		wi("NOISE", reflog()),
		wi("SALSA-4", pr(map[string]string{"relation": "review_requested", "is_draft": "false"})),
		wi("SALSA-3", jira("started", "In Development")),
		wi("SALSA-2", pr(map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "APPROVED", "checks": "passing"})),
		wi("SALSA-1", sess("SALSA-1", true, "needs-followup")),
	}
	dash := e.buildDashboard(items)

	wantOrder := []string{"SALSA-1", "SALSA-2", "SALSA-3", "SALSA-4", "SALSA-5"}
	if dash.GroupCount != len(wantOrder) {
		t.Fatalf("group count = %d, want %d (NOISE should be hidden): %+v", dash.GroupCount, len(wantOrder), dash.Groups)
	}
	for i, key := range wantOrder {
		if dash.Groups[i].Key != key {
			t.Errorf("order[%d] = %q, want %q", i, dash.Groups[i].Key, key)
		}
	}
	byKey := map[string]DashboardGroup{}
	for _, g := range dash.Groups {
		byKey[g.Key] = g
	}
	if byKey["SALSA-1"].Status != statusActive || !byKey["SALSA-1"].ActionRequired {
		t.Errorf("SALSA-1 = %+v, want active + action", byKey["SALSA-1"])
	}
	if byKey["SALSA-3"].Status != statusStarted || byKey["SALSA-3"].ActionRequired {
		t.Errorf("SALSA-3 = %+v, want started + no action", byKey["SALSA-3"])
	}
	if byKey["SALSA-5"].Status != statusAssigned {
		t.Errorf("SALSA-5 status = %q, want assigned", byKey["SALSA-5"].Status)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
