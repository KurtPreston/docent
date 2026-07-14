package engine

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
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
	dash := e.buildDashboard(items, e.corrCfg)

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

func TestBuildDashboardBranchUnit(t *testing.T) {
	e := newTestEngine(t)
	wi := model.WorkItem{
		Key:          "wb:org/repo@salsa-1-fix",
		Title:        "salsa-1-fix",
		Repo:         "org/repo",
		Branch:       "salsa-1-fix",
		OpenPath:     "/code/repo",
		LastActivity: "2026-06-01T12:00:00Z",
		Tickets: []model.TicketRef{
			{Key: "SALSA-1", Title: "SALSA-1 Fix widget", URL: "https://jira/SALSA-1", Status: "In Progress"},
		},
		Entities: []model.Entity{
			{Kind: "branch", Coordinates: map[string]string{"repo": "org/repo", "branch": "salsa-1-fix", "path": "/code/repo", "ticket": "SALSA-1"}},
			{Kind: "commit", Coordinates: map[string]string{"repo": "org/repo", "branch": "salsa-1-fix", "ticket": "SALSA-1"}, State: map[string]string{"observedAt": "2026-06-01T12:00:00Z"}},
			{Kind: "issue", Title: "SALSA-1 Fix widget", URL: "https://jira/SALSA-1", Coordinates: map[string]string{"ticket": "SALSA-1"}, State: map[string]string{"status": "In Progress", "status_tier": "started"}},
			{Kind: "pr_review_status", Title: "salsa-1 fix", URL: "https://github.com/org/repo/pull/5", Coordinates: map[string]string{"repo": "org/repo", "head_branch": "salsa-1-fix", "ticket": "SALSA-1"}, State: map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "APPROVED", "checks": "passing"}},
		},
	}
	dash := e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	if dash.GroupCount != 1 {
		t.Fatalf("expected 1 group, got %d", dash.GroupCount)
	}
	g := dash.Groups[0]
	if g.Branch != "salsa-1-fix" || g.Repo != "org/repo" || g.OpenPath != "/code/repo" {
		t.Errorf("branch unit fields = repo:%q branch:%q path:%q", g.Repo, g.Branch, g.OpenPath)
	}
	if len(g.Tickets) != 1 || g.Tickets[0].Key != "SALSA-1" {
		t.Errorf("tickets = %+v", g.Tickets)
	}
	if len(g.PRs) != 1 || g.PRs[0].PRNumber != 5 {
		t.Errorf("prs = %+v", g.PRs)
	}
	if g.Status != statusApproved {
		t.Errorf("status = %q, want approved", g.Status)
	}
}

func TestBuildDashboardJiraBrowseFallback(t *testing.T) {
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(config.DaemonConfig{
		Directives: []userdata.Directive{
			{ID: "jira", Collector: "jira", Enabled: true, Config: map[string]string{"base_url": "https://jira.example.com/"}},
		},
	}, store)

	// A branch unit whose second ticket has no collected URL should still get
	// a synthesized /browse link, while a ticket that already has a URL keeps
	// it verbatim.
	branch := model.WorkItem{
		Key:    "wb:org/repo@salsa-1-fix",
		Repo:   "org/repo",
		Branch: "salsa-1-fix",
		Tickets: []model.TicketRef{
			{Key: "SALSA-1", URL: "https://jira.example.com/browse/SALSA-1"},
			{Key: "SALSA-2"},
		},
		Entities: []model.Entity{
			{Kind: "branch", Coordinates: map[string]string{"repo": "org/repo", "branch": "salsa-1-fix"}},
		},
	}
	// A ticket-anchored unit with no collected JIRA entity should still get a
	// clickable JiraURL.
	orphan := model.WorkItem{
		Key:   "SALSA-3",
		Title: "SALSA-3",
		Entities: []model.Entity{
			{Kind: "session", Title: "SALSA-3-thing", Coordinates: map[string]string{"ticket": "SALSA-3"}, State: map[string]string{"live": "true", "attention": "idle"}},
		},
	}

	dash := e.buildDashboard([]model.WorkItem{branch, orphan}, e.corrCfg)
	byKey := map[string]DashboardGroup{}
	for _, g := range dash.Groups {
		byKey[g.Key] = g
	}

	b := byKey["wb:org/repo@salsa-1-fix"]
	if len(b.Tickets) != 2 {
		t.Fatalf("branch tickets = %+v", b.Tickets)
	}
	if b.Tickets[0].URL != "https://jira.example.com/browse/SALSA-1" {
		t.Errorf("collected ticket URL changed: %q", b.Tickets[0].URL)
	}
	if b.Tickets[1].URL != "https://jira.example.com/browse/SALSA-2" {
		t.Errorf("uncollected ticket URL = %q, want synthesized browse link", b.Tickets[1].URL)
	}

	o := byKey["SALSA-3"]
	if o.JiraURL != "https://jira.example.com/browse/SALSA-3" {
		t.Errorf("orphan ticket JiraURL = %q, want synthesized browse link", o.JiraURL)
	}
}

func TestBuildDashboardReviewRequestedBranchUnit(t *testing.T) {
	e := newTestEngine(t)
	wi := model.WorkItem{
		Key:    "wb:org/repo@feature-x",
		Title:  "feature-x",
		Repo:   "org/repo",
		Branch: "feature-x",
		Entities: []model.Entity{
			{Kind: "pr_review_status", Title: "their PR", URL: "https://github.com/org/repo/pull/9", Coordinates: map[string]string{"repo": "org/repo", "head_branch": "feature-x"}, State: map[string]string{"relation": "review_requested", "is_draft": "false"}},
		},
	}
	dash := e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	if dash.GroupCount != 1 {
		t.Fatalf("expected 1 group, got %d", dash.GroupCount)
	}
	g := dash.Groups[0]
	if g.Key != "wb:org/repo@feature-x" {
		t.Errorf("key = %q", g.Key)
	}
	if g.Status != statusAwaiting || !g.ActionRequired {
		t.Errorf("review-requested branch unit = status:%q action:%v", g.Status, g.ActionRequired)
	}
}

func TestBuildDashboardOrphanTicket(t *testing.T) {
	e := newTestEngine(t)
	wi := model.WorkItem{
		Key:   "SALSA-99",
		Title: "SALSA-99 Unstarted",
		Entities: []model.Entity{
			{Kind: "issue", Title: "SALSA-99 Unstarted", Coordinates: map[string]string{"ticket": "SALSA-99"}, State: map[string]string{"status_tier": "assigned", "status": "To Do"}},
		},
	}
	dash := e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	if dash.GroupCount != 1 || dash.Groups[0].Key != "SALSA-99" {
		t.Fatalf("orphan ticket group: %+v", dash.Groups)
	}
	if dash.Groups[0].Status != statusAssigned {
		t.Errorf("status = %q", dash.Groups[0].Status)
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func TestDanglingTicketKeys(t *testing.T) {
	cfg := correlation.Config{Projects: []string{"SALSA"}}
	items := []model.WorkItem{
		{Key: "wb:o/r@SALSA-1", Tickets: []model.TicketRef{{Key: "SALSA-1"}}},                        // dangling
		{Key: "wb:o/r@SALSA-2", Tickets: []model.TicketRef{{Key: "SALSA-2", Title: "SALSA-2 done"}}}, // has metadata
		{Key: "SALSA-3", Tickets: []model.TicketRef{{Key: "SALSA-3"}}},                               // dangling
		{Key: "wb:o/r@foo", Tickets: []model.TicketRef{{Key: "PR-7"}}},                               // not a JIRA project key
		{Key: "wb:o/r@dup", Tickets: []model.TicketRef{{Key: "SALSA-1"}}},                            // duplicate of SALSA-1
	}
	got := danglingTicketKeys(items, cfg)
	want := []string{"SALSA-1", "SALSA-3"}
	if len(got) != len(want) {
		t.Fatalf("danglingTicketKeys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("danglingTicketKeys[%d] = %q, want %q (%v)", i, got[i], want[i], got)
		}
	}
}

// fakeResolver implements collectors.ReferenceResolver, returning a canned set
// of signals and recording the refs it was asked to resolve.
type fakeResolver struct {
	mu     sync.Mutex
	calls  [][]string
	result []collectors.StatusItem
}

func (f *fakeResolver) ResolveRefs(_ context.Context, _ userdata.Directive, _ *collectors.CollectOpts, refs []string) ([]collectors.StatusItem, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), refs...))
	f.mu.Unlock()
	return f.result, nil
}

func (f *fakeResolver) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestAnnotationBackfillsBranchSummary(t *testing.T) {
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(config.DaemonConfig{
		TicketProjects: []string{"salsa"},
		Directives: []userdata.Directive{
			{ID: "jira", Collector: "jira", Enabled: true, Config: map[string]string{"base_url": "https://jira.example.com/"}},
		},
	}, store)

	fake := &fakeResolver{result: []collectors.StatusItem{{
		Source:     "jira",
		Kind:       "issue",
		Title:      "SALSA-12430 Publish type_generator for external use",
		URL:        "https://jira.example.com/browse/SALSA-12430",
		ObservedAt: time.Now(),
		Fields:     map[string]string{"key": "SALSA-12430", "status": "Code Review"},
	}}}
	e.reg.Register("jira", fake)

	// A branch unit anchored purely by an authored PR that references
	// SALSA-12430; no JIRA entity was collected, so the summary is missing.
	u := &unit{
		key:       unitKey{Directive: "gh", Mode: collectors.ModeState},
		directive: userdata.Directive{ID: "gh", Collector: "github-enterprise"},
		collector: "github-enterprise",
		mode:      collectors.ModeState,
		signals: []model.Signal{{
			StableID:   "pr:7190",
			Source:     "github-enterprise",
			Kind:       "pr_review_status",
			Title:      "[SALSA-12430] Publishing @tango/type-generator from monorepo",
			URL:        "https://git.drwholdings.com/Chip/salsa/pull/7190",
			ObservedAt: time.Now(),
			Fields:     map[string]string{"repo": "Chip/salsa", "head_branch": "SALSA-12430", "relation": "authored", "is_draft": "false"},
		}},
	}
	e.units = []*unit{u}

	const wantSummary = "SALSA-12430 Publish type_generator for external use"

	// First rebuild finds the dangling key and kicks the async fetch, which
	// caches the result and triggers a follow-up rebuild.
	e.rebuild()

	deadline := time.Now().Add(2 * time.Second)
	var g DashboardGroup
	for time.Now().Before(deadline) {
		snap := e.Snapshot()
		if len(snap.Groups) == 1 {
			g = snap.Groups[0]
			if g.Summary == wantSummary {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	if g.Key != "wb:Chip/salsa@SALSA-12430" {
		t.Fatalf("group key = %q, want the branch unit", g.Key)
	}
	if g.Summary != wantSummary {
		t.Fatalf("summary not backfilled: %q, want %q", g.Summary, wantSummary)
	}
	if len(g.Tickets) == 0 || g.Tickets[0].Status != "Code Review" {
		t.Errorf("ticket status not backfilled: %+v", g.Tickets)
	}
	if fake.callCount() == 0 {
		t.Error("expected ResolveRefs to be called")
	}
}

func TestAnnotationSkipsWhenNoJiraDirective(t *testing.T) {
	// With no JIRA directive configured, a dangling key must not panic or
	// attempt a fetch; the row simply stays without a summary.
	e := newTestEngine(t)
	fake := &fakeResolver{}
	e.reg.Register("jira", fake)
	e.maybeAnnotate([]string{"SALSA-1"})
	// Give any (erroneously spawned) goroutine a chance to run.
	time.Sleep(50 * time.Millisecond)
	if fake.callCount() != 0 {
		t.Errorf("ResolveRefs should not be called without a jira directive, got %d calls", fake.callCount())
	}
}

func newCursorTestEngine(t *testing.T, sshHost string, writeColor *bool) *Engine {
	t.Helper()
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DaemonConfig{
		SSHHost: sshHost,
		SessionManager: userdata.SessionManagerConfig{
			Provider: "cursor",
			Cursor:   userdata.SessionManagerCursor{WriteColor: writeColor},
		},
	}
	return New(cfg, store)
}

func TestBuildDashboardCursorDeepLink(t *testing.T) {
	e := newCursorTestEngine(t, "devbox", nil)
	wi := model.WorkItem{
		Key:      "wb:org/repo@feature-x",
		Title:    "feature-x",
		Repo:     "org/repo",
		Branch:   "feature-x",
		OpenPath: "/code/repo",
		Entities: []model.Entity{
			{Kind: "session", Title: "feature-x", State: map[string]string{"live": "true"}, Coordinates: map[string]string{}},
		},
	}
	dash := e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	if dash.Provider != "cursor" || dash.SSHHost != "devbox" {
		t.Fatalf("dashboard provider/sshHost = %q/%q", dash.Provider, dash.SSHHost)
	}
	if dash.GroupCount != 1 {
		t.Fatalf("group count = %d", dash.GroupCount)
	}
	want := "cursor://vscode-remote/ssh-remote+devbox/code/repo"
	if dash.Groups[0].DeepLink != want {
		t.Errorf("deepLink = %q, want %q", dash.Groups[0].DeepLink, want)
	}
}

func TestOpenWorkItemSyncsColor(t *testing.T) {
	e := newCursorTestEngine(t, "devbox", nil)
	dir := t.TempDir()
	wi := model.WorkItem{
		Key:      "wb:org/repo@feature-x",
		Title:    "feature-x",
		Repo:     "org/repo",
		Branch:   "feature-x",
		OpenPath: dir,
		Color:    "#123456",
		FG:       "#ffffff",
		Entities: []model.Entity{
			{Kind: "session", Title: "feature-x", State: map[string]string{"live": "true"}, Coordinates: map[string]string{}},
		},
	}
	// Seed the engine's snapshot so WorkItem(key) resolves.
	e.mu.Lock()
	e.lastWorkItems = []model.WorkItem{wi}
	e.lastDashboard = e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	e.mu.Unlock()

	res, ok := e.OpenWorkItem("wb:org/repo@feature-x")
	if !ok || !res.OK {
		t.Fatalf("OpenWorkItem = %+v ok=%v", res, ok)
	}
	if !res.ColorSynced {
		t.Error("expected color synced for cursor provider")
	}
	if _, err := os.Stat(filepath.Join(dir, ".vscode", "settings.json")); err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if res.DeepLink == "" {
		t.Error("expected a deep link in open result")
	}
}

func TestOpenWorkItemWriteColorDisabled(t *testing.T) {
	no := false
	e := newCursorTestEngine(t, "devbox", &no)
	dir := t.TempDir()
	wi := model.WorkItem{Key: "SALSA-1", Title: "t", OpenPath: dir, Color: "#123456", FG: "#ffffff",
		Entities: []model.Entity{{Kind: "session", Title: "salsa-1", State: map[string]string{"live": "true"}, Coordinates: map[string]string{}}}}
	e.mu.Lock()
	e.lastWorkItems = []model.WorkItem{wi}
	e.lastDashboard = e.buildDashboard([]model.WorkItem{wi}, e.corrCfg)
	e.mu.Unlock()

	res, ok := e.OpenWorkItem("SALSA-1")
	if !ok || !res.OK {
		t.Fatalf("OpenWorkItem = %+v ok=%v", res, ok)
	}
	if res.ColorSynced {
		t.Error("write_color=false should skip the color sync")
	}
	if _, err := os.Stat(filepath.Join(dir, ".vscode", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("settings.json should not be written when write_color=false (err=%v)", err)
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

func TestDeriveTicketProjects(t *testing.T) {
	cfg := config.DaemonConfig{
		TicketProjects: []string{"salsa"},
		Directives: []userdata.Directive{
			{Collector: "jira", Config: map[string]string{"followed_projects": "JASPER, salsa"}},
			{Collector: "github", Config: map[string]string{"followed_projects": "ignored"}},
		},
	}
	got := deriveTicketProjects(cfg)
	want := []string{"SALSA", "JASPER"}
	if len(got) != len(want) {
		t.Fatalf("deriveTicketProjects = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("deriveTicketProjects[%d] = %q, want %q (%v)", i, got[i], w, got)
		}
	}
}

func TestMergeObservedProjects(t *testing.T) {
	base := correlation.Config{Projects: []string{"SALSA"}}
	signals := []model.Signal{
		{Source: "jira", Fields: map[string]string{"key": "JASPER-3300"}},
		{Source: "jira", Fields: map[string]string{"key": "SALSA-1"}},
		{Source: "local-git", Fields: map[string]string{"key": "NOTJIRA-1"}},
	}
	merged := mergeObservedProjects(signals, base)
	if len(merged.Projects) != 2 {
		t.Fatalf("merged projects = %v, want 2 entries", merged.Projects)
	}
	seen := map[string]bool{}
	for _, p := range merged.Projects {
		seen[p] = true
	}
	if !seen["SALSA"] || !seen["JASPER"] {
		t.Errorf("merged projects = %v, want SALSA and JASPER", merged.Projects)
	}

	// An explicit TicketPattern must short-circuit merging (Projects is
	// ignored by ticketRegexp in that case).
	overridden := correlation.Config{TicketPattern: `^([A-Z]+-\d+)`}
	if got := mergeObservedProjects(signals, overridden); len(got.Projects) != 0 {
		t.Errorf("mergeObservedProjects should no-op when TicketPattern is set, got %v", got.Projects)
	}
}

func TestEngineNewDerivesProjectsFromJiraDirective(t *testing.T) {
	store, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	e := New(config.DaemonConfig{
		Directives: []userdata.Directive{
			{Collector: "jira", Enabled: true, Config: map[string]string{"followed_projects": "SALSA"}},
		},
	}, store)
	if len(e.corrCfg.Projects) != 1 || e.corrCfg.Projects[0] != "SALSA" {
		t.Errorf("corrCfg.Projects = %v, want [SALSA]", e.corrCfg.Projects)
	}
}
