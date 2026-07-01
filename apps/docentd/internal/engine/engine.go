package engine

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"github.com/kurt/slakkr-ai/libs/correlation"
	"github.com/kurt/slakkr-ai/libs/model"
)

// Dashboard matches the legacy docent /sessions payload for the web UI.
type Dashboard struct {
	GeneratedAt  string           `json:"generatedAt"`
	Backend      string           `json:"backend"`
	SessionCount int              `json:"sessionCount"`
	GroupCount   int              `json:"groupCount"`
	Groups       []DashboardGroup `json:"groups"`
}

type DashboardGroup struct {
	Key            string             `json:"key"`
	Ticket         string             `json:"ticket,omitempty"`
	Summary        string             `json:"summary,omitempty"`
	Repo           string             `json:"repo,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	OpenPath       string             `json:"openPath,omitempty"`
	LastActivity   string             `json:"lastActivity,omitempty"`
	JiraStatus     string             `json:"jiraStatus,omitempty"`
	JiraURL        string             `json:"jiraUrl,omitempty"`
	Color          string             `json:"color,omitempty"`
	FG             string             `json:"fg,omitempty"`
	NeedsFollowup  bool               `json:"needsFollowup"`
	Status         string             `json:"status,omitempty"`
	StatusRank     int                `json:"statusRank"`
	ActionRequired bool               `json:"actionRequired"`
	Sessions       []DashboardSession `json:"sessions"`
	PRs            []DashboardPR      `json:"prs"`
	Tickets        []DashboardTicket  `json:"tickets,omitempty"`
}

// Work-item status tiers, ordered by display priority (lower rank sorts
// first). A work-item takes the lowest-rank status it qualifies for; one
// with no qualifying status is hidden from the dashboard.
const (
	statusActive   = "active"
	statusApproved = "approved"
	statusStarted  = "started"
	statusAwaiting = "awaiting-response"
	statusAssigned = "assigned"

	rankActive   = 1
	rankApproved = 2
	rankStarted  = 3
	rankAwaiting = 4
	rankAssigned = 5
	rankHidden   = 99
)

type DashboardSession struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	Host          string `json:"host,omitempty"`
	Path          string `json:"path,omitempty"`
	Ticket        string `json:"ticket,omitempty"`
	Color         string `json:"color,omitempty"`
	FG            string `json:"fg,omitempty"`
	Live          bool   `json:"live"`
	Status        string `json:"status"`
	NeedsFollowup bool   `json:"needsFollowup"`
	LastActivity  string `json:"lastActivity,omitempty"`
}

type DashboardPR struct {
	PRNumber int    `json:"prNumber"`
	Title    string `json:"title"`
	URL      string `json:"url,omitempty"`
	Repo     string `json:"repo,omitempty"`
	State    string `json:"state,omitempty"`
	Draft    bool   `json:"draft"`
	Ticket   string `json:"ticket,omitempty"`
}

type DashboardTicket struct {
	Key    string `json:"key"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Status string `json:"status,omitempty"`
}

const (
	defaultPollInterval = 15 * time.Minute
	defaultLookback     = 7 * 24 * time.Hour
	onRequestTimeout    = 15 * time.Second
)

// unitKey identifies a collection unit: a (directive, mode) pair.
type unitKey struct {
	Directive string
	Mode      collectors.Mode
}

// unit is one schedulable, cacheable collection unit. Its mutable collection
// state is guarded by the engine mutex.
type unit struct {
	key       unitKey
	directive userdata.Directive
	collector string
	mode      collectors.Mode
	interval  time.Duration
	onRequest bool
	onLoad    bool
	lookback  time.Duration
	query     string

	signals   []model.Signal
	lastRun   time.Time
	lastErr   string
	nextDue   time.Time
	watermark time.Time
}

// Engine collects sources on a per-unit schedule and builds the dashboard
// model plus the retained signal -> entity -> work-item links.
type Engine struct {
	cfg     config.DaemonConfig
	store   *registry.Store
	reg     *collectors.Registry
	corrCfg correlation.Config

	mu             sync.Mutex
	units          []*unit
	collecting     map[unitKey]bool
	lastDashboard  Dashboard
	lastWorkItems  []model.WorkItem
	entityWorkItem map[string]string
	generatedAt    time.Time

	refreshing sync.Mutex // single-flight guard for on-request collection
}

func New(cfg config.DaemonConfig, store *registry.Store) *Engine {
	e := &Engine{
		cfg:            cfg,
		store:          store,
		reg:            collectors.NewRegistry(time.Now),
		corrCfg:        correlation.Config{TicketPattern: cfg.TicketPattern},
		collecting:     map[unitKey]bool{},
		entityWorkItem: map[string]string{},
	}
	e.units = e.buildUnits()
	return e
}

// buildUnits expands enabled directives into collection units using each
// collector's declared capabilities. A directive with explicit state/events
// blocks fans out into one unit per block; with neither, it gets a single
// default unit for whatever mode its collector supports (state wins for a
// dual-capable collector, matching the launcher's current-state focus).
func (e *Engine) buildUnits() []*unit {
	start := time.Now()
	var units []*unit
	for _, d := range e.cfg.Directives {
		if !d.Enabled {
			continue
		}
		// A jira directive with per-tier JQL fans out into one state unit
		// per configured tier (started / assigned) instead of the default
		// single involved-scope unit, so each tier's issues arrive tagged
		// with the dashboard status they satisfy.
		if tiered := expandJiraTierDirectives(d); len(tiered) > 0 {
			for _, td := range tiered {
				units = append(units, e.newUnit(td, collectors.ModeState, nil, start))
			}
			continue
		}
		stateCap, eventsCap := e.reg.Capabilities(d.Collector)
		type spec struct {
			mode      collectors.Mode
			mc        *userdata.ModeConfig
			supported bool
		}
		var specs []spec
		switch {
		case d.State != nil || d.Events != nil:
			if d.State != nil {
				specs = append(specs, spec{collectors.ModeState, d.State, stateCap})
			}
			if d.Events != nil {
				specs = append(specs, spec{collectors.ModeEvents, d.Events, eventsCap})
			}
		case stateCap:
			specs = append(specs, spec{collectors.ModeState, nil, true})
		case eventsCap:
			specs = append(specs, spec{collectors.ModeEvents, nil, true})
		}
		for _, sp := range specs {
			if !sp.supported {
				log.Printf("docentd: directive %q collector %q does not support %s mode; skipping that unit", d.ID, d.Collector, sp.mode)
				continue
			}
			units = append(units, e.newUnit(d, sp.mode, sp.mc, start))
		}
	}
	return units
}

// expandJiraTierDirectives returns one derived directive per configured
// dashboard tier query on a jira directive (config.started_query,
// config.assigned_query). Each derived directive carries a distinct ID (so
// its state unit gets a unique unitKey), config.query set to the tier JQL,
// and config.status_tier set to the tier label the jira collector stamps
// onto emitted issues. A non-jira directive, or a jira directive with no
// tier queries, returns nil so the caller falls back to default expansion.
func expandJiraTierDirectives(d userdata.Directive) []userdata.Directive {
	if d.Collector != "jira" {
		return nil
	}
	tiers := []struct{ label, query string }{
		{"started", strings.TrimSpace(d.Config["started_query"])},
		{"assigned", strings.TrimSpace(d.Config["assigned_query"])},
	}
	var out []userdata.Directive
	for _, t := range tiers {
		if t.query == "" {
			continue
		}
		cfg := make(map[string]string, len(d.Config)+2)
		for k, v := range d.Config {
			cfg[k] = v
		}
		cfg["query"] = t.query
		cfg["status_tier"] = t.label
		td := d
		td.ID = d.ID + "#" + t.label
		td.Config = cfg
		td.State = nil
		td.Events = nil
		out = append(out, td)
	}
	return out
}

func (e *Engine) newUnit(d userdata.Directive, mode collectors.Mode, mc *userdata.ModeConfig, start time.Time) *unit {
	u := &unit{
		key:       unitKey{Directive: d.ID, Mode: mode},
		directive: d,
		collector: d.Collector,
		mode:      mode,
		interval:  defaultPollInterval,
		onLoad:    true,
		lookback:  defaultLookback,
	}
	if mc != nil {
		if dur, err := userdata.ParseDuration(mc.Poll.Interval); err == nil && mc.Poll.Interval != "" {
			u.interval = dur
		}
		u.onRequest = mc.Poll.OnRequest
		u.onLoad = mc.Poll.OnLoad
		if dur, err := userdata.ParseDuration(mc.Lookback); err == nil && mc.Lookback != "" {
			u.lookback = dur
		}
		u.query = strings.TrimSpace(mc.Query)
	}
	if u.interval > 0 {
		// Background units don't collect at startup unless on_load is set;
		// their first scheduled poll lands one interval in.
		u.nextDue = start.Add(u.interval)
	}
	return u
}

// due reports whether the unit should be collected now: on the initial pass
// every on_load unit is due; afterwards a background unit is due once it
// reaches its nextDue.
func (u *unit) due(now time.Time, initial bool) bool {
	if initial && u.onLoad {
		return true
	}
	return u.interval > 0 && !u.nextDue.IsZero() && !now.Before(u.nextDue)
}

// StartScheduler runs the background poll loop until ctx is cancelled. It
// performs the initial on_load collection, then polls each unit on its own
// cadence, rebuilding the snapshot after any collection completes.
func (e *Engine) StartScheduler(ctx context.Context) {
	go func() {
		e.tick(ctx, true)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				e.tick(ctx, false)
			}
		}
	}()
}

// tick collects every unit that is due (on initial=true, every on_load unit;
// otherwise every background unit past its nextDue). Each due unit is
// collected in its own goroutine so a slow source can't stall the loop, and
// each rebuilds the snapshot on completion.
func (e *Engine) tick(ctx context.Context, initial bool) {
	now := time.Now()
	var due []*unit
	e.mu.Lock()
	for _, u := range e.units {
		if e.collecting[u.key] {
			continue
		}
		if u.due(now, initial) {
			e.collecting[u.key] = true
			due = append(due, u)
		}
	}
	e.mu.Unlock()
	for _, u := range due {
		go func(u *unit) {
			e.collectUnit(ctx, u)
			e.mu.Lock()
			delete(e.collecting, u.key)
			e.mu.Unlock()
			e.rebuild()
		}(u)
	}
}

// RefreshOnRequest collects all on_request units inline (bounded), rebuilds,
// and returns the current dashboard. A single-flight guard means concurrent
// requests (e.g. the 5s browser poll) don't stampede the collectors: only one
// inline refresh runs at a time; others return the latest snapshot.
func (e *Engine) RefreshOnRequest(ctx context.Context) Dashboard {
	if !e.refreshing.TryLock() {
		return e.Snapshot()
	}
	defer e.refreshing.Unlock()

	cctx, cancel := context.WithTimeout(ctx, onRequestTimeout)
	defer cancel()
	var due []*unit
	e.mu.Lock()
	for _, u := range e.units {
		if u.onRequest && !e.collecting[u.key] {
			e.collecting[u.key] = true
			due = append(due, u)
		}
	}
	e.mu.Unlock()

	var wg sync.WaitGroup
	for _, u := range due {
		wg.Add(1)
		go func(u *unit) {
			defer wg.Done()
			e.collectUnit(cctx, u)
			e.mu.Lock()
			delete(e.collecting, u.key)
			e.mu.Unlock()
		}(u)
	}
	wg.Wait()
	if len(due) > 0 {
		e.rebuild()
	}
	return e.Snapshot()
}

// collectUnit runs one unit and merges the result into its cache slice:
// state units replace; events units accumulate (append + dedup by stable id +
// age-out beyond the lookback) and advance the watermark.
func (e *Engine) collectUnit(ctx context.Context, u *unit) {
	now := time.Now()
	opts := &collectors.CollectOpts{
		UserdataDir: e.cfg.ConfigDir,
		Until:       now,
		Scope:       collectors.ScopeInvolved,
		Mode:        u.mode,
	}
	cutoff := now.Add(-u.lookback)
	if u.mode == collectors.ModeEvents {
		since := cutoff
		e.mu.Lock()
		if !u.watermark.IsZero() && u.watermark.After(since) {
			since = u.watermark
		}
		e.mu.Unlock()
		opts.Since = since
	} else {
		opts.Since = cutoff
	}

	d := u.directive
	if u.query != "" {
		d = withQuery(d, u.query)
	}
	signals, err := e.reg.CollectUnit(ctx, d, u.mode, opts)

	e.mu.Lock()
	defer e.mu.Unlock()
	u.lastRun = now
	if err != nil {
		u.lastErr = err.Error()
	} else {
		u.lastErr = ""
		if u.mode == collectors.ModeState {
			u.signals = signals
		} else {
			u.signals = mergeEvents(u.signals, signals, cutoff)
			u.watermark = now
		}
	}
	if u.interval > 0 {
		u.nextDue = now.Add(u.interval)
	}
}

// withQuery returns a shallow copy of d with config.query overridden, so a
// per-mode query (e.g. a state-view JQL) applies only to that unit.
func withQuery(d userdata.Directive, query string) userdata.Directive {
	cfg := make(map[string]string, len(d.Config)+1)
	for k, v := range d.Config {
		cfg[k] = v
	}
	cfg["query"] = query
	d.Config = cfg
	return d
}

// mergeEvents appends incoming events to existing ones, de-duplicates by
// stable id (incoming wins), and drops anything observed before cutoff.
func mergeEvents(existing, incoming []model.Signal, cutoff time.Time) []model.Signal {
	byID := make(map[string]model.Signal)
	order := make([]string, 0, len(existing)+len(incoming))
	add := func(s model.Signal) {
		if s.ObservedAt.Before(cutoff) {
			return
		}
		id := signalID(s)
		if _, ok := byID[id]; !ok {
			order = append(order, id)
		}
		byID[id] = s
	}
	for _, s := range existing {
		add(s)
	}
	for _, s := range incoming {
		add(s)
	}
	out := make([]model.Signal, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

// signalID mirrors correlation's entity id derivation closely enough to
// de-duplicate signals across polls.
func signalID(s model.Signal) string {
	if s.StableID != "" {
		return s.StableID
	}
	return s.Source + ":" + s.Kind + ":" + s.Title
}

// Snapshot returns the most recently rebuilt dashboard.
func (e *Engine) Snapshot() Dashboard {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastDashboard
}

// rebuild recomputes entities, work-items, the dashboard, and the
// signal -> entity -> work-item links from the union of all units' signals.
func (e *Engine) rebuild() {
	e.mu.Lock()
	signals := make([]model.Signal, 0)
	for _, u := range e.units {
		signals = append(signals, u.signals...)
	}
	e.mu.Unlock()

	entities := e.entitiesFrom(signals)
	workItems := correlation.BuildWorkItems(entities, e.corrCfg)
	dashboard := e.buildDashboard(workItems)
	entityWorkItem := make(map[string]string)
	for _, wi := range workItems {
		for _, ent := range wi.Entities {
			entityWorkItem[ent.ID] = wi.Key
		}
	}

	e.mu.Lock()
	e.lastDashboard = dashboard
	e.lastWorkItems = workItems
	e.entityWorkItem = entityWorkItem
	e.generatedAt = time.Now()
	e.mu.Unlock()
}

// entitiesFrom maps signals to entities, enriches session entities from the
// registry store, and injects registry-only sessions that still need
// follow-up (their live window has closed).
func (e *Engine) entitiesFrom(signals []model.Signal) []model.Entity {
	entities := correlation.SignalsToEntities(signals, e.corrCfg)
	for i := range entities {
		ent := &entities[i]
		if ent.Kind != "session" {
			continue
		}
		name := ent.Title
		if rec, ok := e.store.Get(name); ok {
			if rec.Color != "" {
				ent.State["color"] = rec.Color
			}
			if rec.Host != "" {
				ent.Coordinates["host"] = rec.Host
			}
			ent.State["attention"] = registry.SessionStatus(rec)
			ent.State["lastActivity"] = registry.LatestActivity(rec)
		}
		if ent.State["color"] == "" {
			c := model.ColorForName(name)
			ent.State["color"] = c
			ent.State["fg"] = model.ForegroundForHex(c)
		}
	}

	for name, rec := range e.store.All() {
		if registry.SessionStatus(rec) != "needs-followup" {
			continue
		}
		found := false
		for _, ent := range entities {
			if ent.Kind == "session" && ent.Title == name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		ent := model.Entity{
			ID:          "session:" + name,
			Kind:        "session",
			Title:       name,
			State:       map[string]string{"attention": "needs-followup", "live": "false", "lastActivity": registry.LatestActivity(rec)},
			Coordinates: map[string]string{},
		}
		if rec.Host != "" {
			ent.Coordinates["host"] = rec.Host
		}
		if rec.Color != "" {
			ent.State["color"] = rec.Color
			ent.State["fg"] = rec.FG
		}
		entities = append(entities, ent)
	}
	return entities
}

// groupFacts accumulates the entity-derived signals a work-item needs to be
// classified into a status + action_required.
type groupFacts struct {
	hasLiveSession       bool
	sessionNeedsFollowup bool
	authoredApproved     bool // authored, non-draft, approved, checks passing/none
	authoredDraft        bool // authored draft PR
	authoredAwaiting     bool // authored, non-draft, not approved
	authoredMyTurn       bool // authored, non-draft, changes-requested or failing checks
	reviewRequested      bool // someone else's PR awaiting my review
	jiraStarted          bool
	jiraAssigned         bool
	branchEvidence       bool // a local branch/commit/reflog/session ties work to the ticket
}

func (e *Engine) buildDashboard(workItems []model.WorkItem) Dashboard {
	groups := make([]DashboardGroup, 0, len(workItems))
	liveCount := 0
	for _, wi := range workItems {
		g := DashboardGroup{
			Key:      wi.Key,
			Ticket:   wi.Key,
			Summary:  wi.Title,
			Repo:     wi.Repo,
			Branch:   wi.Branch,
			OpenPath: wi.OpenPath,
			LastActivity: wi.LastActivity,
			Color:    wi.Color,
			FG:       wi.FG,
			Sessions: []DashboardSession{},
			PRs:      []DashboardPR{},
		}
		if strings.HasPrefix(wi.Key, "wb:") {
			g.Ticket = ""
			if len(wi.Tickets) > 0 {
				g.Ticket = wi.Tickets[0].Key
			}
			for _, tr := range wi.Tickets {
				g.Tickets = append(g.Tickets, DashboardTicket{
					Key:    tr.Key,
					Title:  tr.Title,
					URL:    tr.URL,
					Status: tr.Status,
				})
			}
		}
		if wi.Attention == "needs-followup" || wi.Attention == "working" {
			g.NeedsFollowup = wi.Attention == "needs-followup"
		}
		if g.Color == "" {
			g.Color = model.ColorForName(wi.Key)
			g.FG = model.ForegroundForHex(g.Color)
		}
		var facts groupFacts
		for _, ent := range wi.Entities {
			switch ent.Kind {
			case "session":
				live := ent.State["live"] == "true"
				if live {
					liveCount++
					facts.hasLiveSession = true
				}
				// A ticket-anchored session means a local checkout exists
				// for that ticket (branch evidence for "started"). A
				// ticketless session is still shown when live via
				// hasLiveSession, but doesn't imply a ticket branch.
				if ent.Coordinates["ticket"] != "" {
					facts.branchEvidence = true
				}
				status := ent.State["attention"]
				if status == "" {
					status = "idle"
				}
				if status == "needs-followup" {
					facts.sessionNeedsFollowup = true
				}
				ds := DashboardSession{
					Kind:          "session",
					Name:          ent.Title,
					Host:          ent.Coordinates["host"],
					Path:          ent.Coordinates["path"],
					Ticket:        correlation.ParseTicketKey(ent.Title, e.corrCfg),
					Color:         ent.State["color"],
					FG:            ent.State["fg"],
					Live:          live,
					Status:        status,
					NeedsFollowup: status == "needs-followup",
					LastActivity:  ent.State["lastActivity"],
				}
				if ds.Color == "" {
					ds.Color = model.ColorForName(ds.Name)
					ds.FG = model.ForegroundForHex(ds.Color)
				}
				g.Sessions = append(g.Sessions, ds)
				if ds.NeedsFollowup {
					g.NeedsFollowup = true
				}
			case "ticket", "issue_activity", "issue":
				g.Summary = ent.Title
				g.JiraURL = ent.URL
				if ent.State != nil {
					g.JiraStatus = ent.State["status"]
					switch ent.State["status_tier"] {
					case "started":
						facts.jiraStarted = true
					case "assigned":
						facts.jiraAssigned = true
					}
				}
			case "branch", "commit", "reflog":
				// Repo/branch units always have local git evidence.
				// Legacy ticket-keyed units only count when ticket-anchored.
				if strings.HasPrefix(wi.Key, "wb:") {
					facts.branchEvidence = true
				} else if ent.Coordinates["ticket"] != "" {
					facts.branchEvidence = true
				}
			default:
				if strings.Contains(ent.Kind, "pr") {
					draft := ent.State["is_draft"] == "true"
					g.PRs = append(g.PRs, DashboardPR{
						PRNumber: prNumberFromURL(ent.URL),
						Title:    ent.Title,
						URL:      ent.URL,
						Repo:     ent.Coordinates["repo"],
						State:    ent.State["state"],
						Draft:    draft,
						Ticket:   ent.Coordinates["ticket"],
					})
					// Only the state-mode review-status signal carries the
					// relation/checks/review_decision fields the status model
					// needs; events-mode activity PRs are shown as rows but
					// don't drive classification.
					if ent.Kind == "pr_review_status" {
						classifyPR(&facts, ent)
					}
				}
			}
		}
		g.Status, g.StatusRank, g.ActionRequired = classifyGroup(facts)
		if g.StatusRank >= rankHidden {
			continue
		}
		groups = append(groups, g)
	}

	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].StatusRank != groups[j].StatusRank {
			return groups[i].StatusRank < groups[j].StatusRank
		}
		if groups[i].ActionRequired != groups[j].ActionRequired {
			return groups[i].ActionRequired // action-required first
		}
		if groups[i].LastActivity != groups[j].LastActivity {
			return groups[i].LastActivity > groups[j].LastActivity
		}
		return groups[i].Key < groups[j].Key
	})

	return Dashboard{
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Backend:      "go",
		SessionCount: liveCount,
		GroupCount:   len(groups),
		Groups:       groups,
	}
}

// classifyPR folds one PR entity's state into the group facts. Only authored
// PRs (relation=authored) carry checks/review_decision; review_requested PRs
// mean my review is still pending on someone else's PR.
func classifyPR(facts *groupFacts, ent model.Entity) {
	relation := ent.State["relation"]
	if relation == "review_requested" {
		facts.reviewRequested = true
		return
	}
	// Treat anything else (authored, or legacy rows without a relation) as
	// my own PR.
	draft := ent.State["is_draft"] == "true"
	if draft {
		facts.authoredDraft = true
		return
	}
	decision := ent.State["review_decision"]
	checks := ent.State["checks"]
	if decision == "APPROVED" && (checks == "passing" || checks == "none") {
		facts.authoredApproved = true
		return
	}
	facts.authoredAwaiting = true
	if decision == "CHANGES_REQUESTED" || checks == "failing" {
		facts.authoredMyTurn = true
	}
}

// classifyGroup maps accumulated facts to (status, rank, action_required),
// choosing the lowest-rank status the work-item qualifies for. Returns
// rankHidden when nothing matches so the caller can drop the group.
func classifyGroup(f groupFacts) (string, int, bool) {
	switch {
	case f.hasLiveSession:
		return statusActive, rankActive, f.sessionNeedsFollowup
	case f.authoredApproved:
		return statusApproved, rankApproved, true // not merged yet
	case f.jiraStarted || f.authoredDraft || f.branchEvidence:
		return statusStarted, rankStarted, f.branchEvidence
	case f.authoredAwaiting || f.reviewRequested:
		return statusAwaiting, rankAwaiting, f.reviewRequested || f.authoredMyTurn
	case f.jiraAssigned:
		return statusAssigned, rankAssigned, false
	default:
		return "", rankHidden, false
	}
}

// prNumberFromURL extracts the PR number from a .../pull/<n> URL, or 0.
func prNumberFromURL(raw string) int {
	i := strings.LastIndex(raw, "/pull/")
	if i < 0 {
		return 0
	}
	rest := raw[i+len("/pull/"):]
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest))
	if err != nil {
		return 0
	}
	return n
}

// SignalView is one raw signal annotated with the entity and work-item it
// correlated into.
type SignalView struct {
	Kind        string            `json:"kind"`
	Title       string            `json:"title"`
	Summary     string            `json:"summary,omitempty"`
	URL         string            `json:"url,omitempty"`
	ObservedAt  string            `json:"observedAt,omitempty"`
	EntityID    string            `json:"entityId,omitempty"`
	WorkItemKey string            `json:"workItemKey,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// SignalUnit groups the raw signals produced by one collection unit.
type SignalUnit struct {
	DirectiveID string       `json:"directiveId"`
	Collector   string       `json:"collector"`
	Mode        string       `json:"mode"`
	LastRun     string       `json:"lastRun,omitempty"`
	LastErr     string       `json:"lastErr,omitempty"`
	Count       int          `json:"count"`
	Signals     []SignalView `json:"signals"`
}

// SignalsView is the payload for GET /api/signals.
type SignalsView struct {
	GeneratedAt string       `json:"generatedAt"`
	Units       []SignalUnit `json:"units"`
}

// UnitView summarizes one collection unit for GET /api/collectors.
type UnitView struct {
	DirectiveID string `json:"directiveId"`
	Collector   string `json:"collector"`
	Mode        string `json:"mode"`
	Interval    string `json:"interval,omitempty"`
	OnRequest   bool   `json:"onRequest"`
	OnLoad      bool   `json:"onLoad"`
	LastRun     string `json:"lastRun,omitempty"`
	NextDue     string `json:"nextDue,omitempty"`
	ItemCount   int    `json:"itemCount"`
	LastErr     string `json:"lastErr,omitempty"`
}

// CollectorsView is the payload for GET /api/collectors.
type CollectorsView struct {
	GeneratedAt string     `json:"generatedAt"`
	Units       []UnitView `json:"units"`
}

// WorkItemDetail is the payload for GET /api/workitems/{key}.
type WorkItemDetail struct {
	Key          string             `json:"key"`
	Title        string             `json:"title,omitempty"`
	Ticket       string             `json:"ticket,omitempty"`
	Summary      string             `json:"summary,omitempty"`
	Repo         string             `json:"repo,omitempty"`
	Branch       string             `json:"branch,omitempty"`
	OpenPath     string             `json:"openPath,omitempty"`
	LastActivity string             `json:"lastActivity,omitempty"`
	JiraURL      string             `json:"jiraUrl,omitempty"`
	Status       string             `json:"jiraStatus,omitempty"`
	Color        string             `json:"color,omitempty"`
	FG           string             `json:"fg,omitempty"`
	Sessions     []DashboardSession `json:"sessions"`
	PRs          []DashboardPR      `json:"prs"`
	Tickets      []DashboardTicket  `json:"tickets,omitempty"`
	Entities     []EntityView       `json:"entities"`
	Signals      []SignalView       `json:"signals"`
}

// EntityView is a correlated entity shown on a work-item detail page.
type EntityView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

func iso(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (e *Engine) signalView(s model.Signal) SignalView {
	entID := correlation.SignalToEntity(s, e.corrCfg).ID
	return SignalView{
		Kind:        s.Kind,
		Title:       s.Title,
		Summary:     s.Summary,
		URL:         s.URL,
		ObservedAt:  iso(s.ObservedAt),
		EntityID:    entID,
		WorkItemKey: e.entityWorkItem[entID],
		Fields:      s.Fields,
	}
}

// Signals returns the raw signals per collection unit with their correlation
// links. Safe for concurrent use.
func (e *Engine) Signals() SignalsView {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := SignalsView{GeneratedAt: iso(e.generatedAt)}
	for _, u := range e.units {
		su := SignalUnit{
			DirectiveID: u.directive.ID,
			Collector:   u.collector,
			Mode:        string(u.mode),
			LastRun:     iso(u.lastRun),
			LastErr:     u.lastErr,
			Count:       len(u.signals),
			Signals:     make([]SignalView, 0, len(u.signals)),
		}
		for _, s := range u.signals {
			su.Signals = append(su.Signals, e.signalView(s))
		}
		out.Units = append(out.Units, su)
	}
	return out
}

// Collectors returns one status row per collection unit.
func (e *Engine) Collectors() CollectorsView {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := CollectorsView{GeneratedAt: iso(e.generatedAt)}
	for _, u := range e.units {
		interval := ""
		if u.interval > 0 {
			interval = u.interval.String()
		}
		out.Units = append(out.Units, UnitView{
			DirectiveID: u.directive.ID,
			Collector:   u.collector,
			Mode:        string(u.mode),
			Interval:    interval,
			OnRequest:   u.onRequest,
			OnLoad:      u.onLoad,
			LastRun:     iso(u.lastRun),
			NextDue:     iso(u.nextDue),
			ItemCount:   len(u.signals),
			LastErr:     u.lastErr,
		})
	}
	return out
}

// WorkItem returns the detail payload for one work item, or ok=false when no
// work item with that key exists in the current snapshot.
func (e *Engine) WorkItem(key string) (WorkItemDetail, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var wi *model.WorkItem
	for i := range e.lastWorkItems {
		if e.lastWorkItems[i].Key == key {
			wi = &e.lastWorkItems[i]
			break
		}
	}
	if wi == nil {
		return WorkItemDetail{}, false
	}
	detail := WorkItemDetail{
		Key:          wi.Key,
		Title:        wi.Title,
		Ticket:       wi.Key,
		Repo:         wi.Repo,
		Branch:       wi.Branch,
		OpenPath:     wi.OpenPath,
		LastActivity: wi.LastActivity,
		Color:        wi.Color,
		FG:           wi.FG,
		Sessions:     []DashboardSession{},
		PRs:          []DashboardPR{},
		Entities:     make([]EntityView, 0, len(wi.Entities)),
		Signals:      []SignalView{},
	}
	if strings.HasPrefix(wi.Key, "wb:") {
		detail.Ticket = ""
		if len(wi.Tickets) > 0 {
			detail.Ticket = wi.Tickets[0].Key
		}
		for _, tr := range wi.Tickets {
			detail.Tickets = append(detail.Tickets, DashboardTicket{
				Key:    tr.Key,
				Title:  tr.Title,
				URL:    tr.URL,
				Status: tr.Status,
			})
		}
	}
	for _, g := range e.lastDashboard.Groups {
		if g.Key == key {
			detail.Summary = g.Summary
			detail.JiraURL = g.JiraURL
			detail.Status = g.JiraStatus
			detail.Sessions = g.Sessions
			detail.PRs = g.PRs
			if len(detail.Tickets) == 0 {
				detail.Tickets = g.Tickets
			}
			break
		}
	}
	for _, ent := range wi.Entities {
		detail.Entities = append(detail.Entities, EntityView{
			ID:    ent.ID,
			Kind:  ent.Kind,
			Title: ent.Title,
			URL:   ent.URL,
		})
	}
	for _, u := range e.units {
		for _, s := range u.signals {
			if e.entityWorkItem[correlation.SignalToEntity(s, e.corrCfg).ID] == key {
				detail.Signals = append(detail.Signals, e.signalView(s))
			}
		}
	}
	return detail, true
}

// CollectUnitNow forces an immediate collection of the (directive, mode) unit,
// ignoring its interval, then rebuilds. Returns ok=false when no such unit
// exists.
func (e *Engine) CollectUnitNow(ctx context.Context, directiveID string, mode collectors.Mode) bool {
	key := unitKey{Directive: directiveID, Mode: mode}
	var target *unit
	e.mu.Lock()
	for _, u := range e.units {
		if u.key == key {
			target = u
			break
		}
	}
	e.mu.Unlock()
	if target == nil {
		return false
	}
	e.collectUnit(ctx, target)
	e.mu.Lock()
	delete(e.collecting, key)
	e.mu.Unlock()
	e.rebuild()
	return true
}

// Ensure default directives include webhook collector.
func EnsureDirectives(d []userdata.Directive) []userdata.Directive {
	hasWebhook := false
	hasWM := false
	for _, dir := range d {
		if dir.Collector == "webhook" {
			hasWebhook = true
		}
		if dir.Collector == "docent-wm" {
			hasWM = true
		}
	}
	out := append([]userdata.Directive{}, d...)
	if !hasWebhook {
		out = append(out, userdata.Directive{
			ID: "webhook", Name: "Webhook inbox", Collector: "webhook", Enabled: true,
			// The inbox is drained on read, so collect on every request.
			Events: &userdata.ModeConfig{Poll: userdata.PollConfig{OnRequest: true}},
		})
	}
	if !hasWM {
		out = append(out, userdata.Directive{
			ID: "local-wm", Name: "Local docent-wm", Collector: "docent-wm", Enabled: true,
			Config: map[string]string{"base_url": "http://127.0.0.1:39788", "machine": "local"},
			// Live windows are real-time and cheap: collect on load and on
			// every request.
			State: &userdata.ModeConfig{Poll: userdata.PollConfig{OnRequest: true, OnLoad: true}},
		})
	}
	return out
}
