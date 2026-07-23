package engine

import (
	"context"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/sessionmanager"
	"github.com/KurtPreston/docent/libs/workitem"
)

// Dashboard is the dashboard payload for the web UI, served by GET /api/workitems.
type Dashboard struct {
	GeneratedAt  string `json:"generatedAt"`
	Backend      string `json:"backend"`
	SessionCount int    `json:"sessionCount"`
	GroupCount   int    `json:"groupCount"`
	// Provider is the configured open_trigger provider ("cursor", "wsm",
	// or "" when none). The web app uses it to decide whether to render the
	// session column and which action a session/path click triggers.
	Provider string `json:"provider,omitempty"`
	// SSHHost is docentd's ssh alias (DOCENT_HOST), used by providers that
	// build remote deep links.
	SSHHost string           `json:"sshHost,omitempty"`
	Groups  []DashboardGroup `json:"groups"`
}

type DashboardGroup struct {
	Key      string `json:"key"`
	Ticket   string `json:"ticket,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Branch   string `json:"branch,omitempty"`
	OpenPath string `json:"openPath,omitempty"`
	// DeepLink is the provider-supplied clickable link that opens/focuses this
	// work item's window (e.g. a cursor:// URI). Empty when the provider has no
	// deep link (e.g. wsm) or the work item has no path.
	DeepLink       string             `json:"deepLink,omitempty"`
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

// Work-item status tiers are defined in libs/workitem. Re-exported here as
// package-local aliases so existing dashboard tests and string comparisons
// keep working without a large churn.
const (
	statusActive   = workitem.StatusActive
	statusApproved = workitem.StatusApproved
	statusStarted  = workitem.StatusStarted
	statusAwaiting = workitem.StatusAwaiting
	statusAssigned = workitem.StatusAssigned

	rankActive   = workitem.RankActive
	rankApproved = workitem.RankApproved
	rankStarted  = workitem.RankStarted
	rankAwaiting = workitem.RankAwaiting
	rankAssigned = workitem.RankAssigned
	rankHidden   = workitem.RankHidden
)

type DashboardSession struct {
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	IDE           string `json:"ide,omitempty"`
	Host          string `json:"host,omitempty"`
	TargetHost    string `json:"targetHost,omitempty"`
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
	// backgroundCollectTimeout bounds a single scheduled collection so a wedged
	// git subprocess (e.g. one hung fetching from a broken partial clone) cannot
	// run forever and pile up across ticks.
	backgroundCollectTimeout = 60 * time.Second
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

// Annotation-pass tuning. Referenced-but-uncollected tickets are backfilled
// on a TTL so a rebuild (which fires after every collection) doesn't re-hit
// JIRA constantly. Negative results (nothing returned / fetch error) are
// cached for a shorter window so a transient failure recovers quickly.
const (
	annotationPositiveTTL  = 10 * time.Minute
	annotationNegativeTTL  = 2 * time.Minute
	annotationFetchTimeout = 20 * time.Second
)

// annotationEntry is one cached annotation-pass result. A nil Signal is a
// negative cache entry (the key was fetched but JIRA returned nothing, or the
// fetch failed); FetchedAt drives TTL expiry.
type annotationEntry struct {
	Signal    *model.Signal
	FetchedAt time.Time
}

// Engine collects sources on a per-unit schedule and builds the dashboard
// model plus the retained signal -> entity -> work-item links.
type Engine struct {
	cfg     config.DaemonConfig
	store   *registry.Store
	reg     *collectors.Registry
	corrCfg correlation.Config

	// sessionMgr is the configured session provider (nil when none). sessionLinker
	// is set when that provider can produce clickable deep links (cursor, not wsm).
	sessionMgr    sessionmanager.SessionManager
	sessionLinker sessionmanager.DeepLinker

	// jiraBaseURL is the configured jira collector's base_url (trailing slash
	// trimmed), used to synthesize /browse/<key> links for ticket keys that
	// resolved without a collected JIRA entity. Empty when no jira directive
	// with a base_url is configured.
	jiraBaseURL string

	mu             sync.Mutex
	units          []*unit
	collecting     map[unitKey]bool
	lastDashboard  Dashboard
	lastWorkItems  []model.WorkItem
	entityWorkItem map[string]string
	generatedAt    time.Time

	// annotations caches synthetic JIRA signals for tickets that were
	// referenced (by a PR/branch/commit) but never collected because they
	// fell outside the scope JQL. Keyed by upper-cased ticket key; a nil
	// Signal is a negative entry. Guarded by mu; unioned into the signal set
	// on each rebuild.
	annotations map[string]annotationEntry

	// automations evaluates rules after per-unit collection. Nil when no
	// rules are configured.
	automations *automation.Dispatcher

	// entitySnapshots holds the previous entity state per unit key, used for
	// transition detection. Keyed by unitKey string (directive/mode). Guarded
	// by mu; updated only from collectUnit after a successful collect.
	entitySnapshots map[string]map[string]model.Entity

	// automationSeeded marks unit keys whose first successful collect has
	// completed. The first collect only seeds the baseline (snapshot +
	// watermark) without firing automations, so a daemon start/restart does
	// not replay the whole lookback window as "new". Guarded by mu.
	automationSeeded map[string]bool

	// scheduleLastFire tracks the last fire time per schedule rule ID so we
	// don't re-fire within the same minute. Guarded by scheduleMu.
	scheduleLastFire map[string]time.Time
	scheduleMu       sync.Mutex

	refreshing sync.Mutex // single-flight guard for on-request collection
	annotating sync.Mutex // single-flight guard for the annotation fetch
}

func New(cfg config.DaemonConfig, store *registry.Store) *Engine {
	e := &Engine{
		cfg:   cfg,
		store: store,
		reg:   collectors.NewRegistry(time.Now),
		corrCfg: correlation.Config{
			TicketPattern: cfg.TicketPattern,
			Projects:      deriveTicketProjects(cfg),
			AllowGeneric:  hasJiraCollector(cfg),
		},
		collecting:       map[unitKey]bool{},
		entityWorkItem:   map[string]string{},
		annotations:      map[string]annotationEntry{},
		entitySnapshots:  map[string]map[string]model.Entity{},
		automationSeeded: map[string]bool{},
		scheduleLastFire: map[string]time.Time{},
	}
	e.sessionMgr = sessionmanager.Select(cfg.OpenTrigger)
	e.sessionLinker, _ = e.sessionMgr.(sessionmanager.DeepLinker)
	e.jiraBaseURL = jiraBaseURL(cfg)
	e.units = e.buildUnits()
	if len(cfg.Automations) > 0 {
		e.automations = automation.NewDispatcher(cfg.Automations)
		e.wireAutomationConnectors()
	}
	return e
}

// jiraBaseURL returns the first configured jira directive's base_url (trailing
// slash trimmed), or "" when none is configured. Used to build /browse/<key>
// links for resolved ticket keys that lack a collected JIRA entity.
func jiraBaseURL(cfg config.DaemonConfig) string {
	for _, d := range cfg.Directives {
		if d.Collector != "jira" {
			continue
		}
		if base := strings.TrimRight(strings.TrimSpace(d.Config["base_url"]), "/"); base != "" {
			return base
		}
	}
	return ""
}

// hasJiraCollector reports whether any enabled jira directive is configured.
// Generic ticket-key scanning (matching any WORD-digits token) is only
// enabled when this is true, so Dependabot / package-version branches don't
// invent phantom tickets for dashboards that never talk to JIRA.
func hasJiraCollector(cfg config.DaemonConfig) bool {
	for _, d := range cfg.Directives {
		if d.Collector == "jira" && d.Enabled {
			return true
		}
	}
	return false
}

// ticketBrowseURL builds a JIRA browse URL for a ticket key, or "" when no jira
// base_url is configured (or the key is empty).
func (e *Engine) ticketBrowseURL(key string) string {
	if e.jiraBaseURL == "" || key == "" {
		return ""
	}
	return e.jiraBaseURL + "/browse/" + key
}

// ticketURL prefers a ticket's collected URL, falling back to a synthesized
// browse URL so a resolved key still links out even when its JIRA entity was
// never collected (e.g. the ticket is outside the configured JQL scope). This
// is what keeps JIRA links consistently clickable across work items.
func (e *Engine) ticketURL(collected, key string) string {
	if collected != "" {
		return collected
	}
	return e.ticketBrowseURL(key)
}

// providerKey returns the normalized open_trigger provider ("cursor",
// "wsm", or "" when none).
func (e *Engine) providerKey() string {
	return normalizeSessionProvider(e.cfg.OpenTrigger.Provider)
}

// deepLinkFor returns the provider deep link for a work-item path, or "" when
// the provider has no deep link or the path is empty.
func (e *Engine) deepLinkFor(openPath string) string {
	if e.sessionLinker == nil || openPath == "" {
		return ""
	}
	return e.sessionLinker.DeepLink(openPath, e.cfg.SSHHost)
}

// Provider returns the normalized open_trigger provider ("cursor", "wsm", or
// "" when none). Exposed so the sessions API can tell the dashboard how a
// launch action should behave for a raw registry session.
func (e *Engine) Provider() string {
	return e.providerKey()
}

// SessionDeepLink returns the provider deep link that opens a session's own
// workspace path on its own host, or "" when the provider has no deep link or
// the path is empty. Unlike deepLinkFor it keys off the session's exact
// path+targetHost (rather than a work item's openPath and the daemon's ssh
// alias), so a launch focuses that specific window: a remote session yields an
// ssh-remote link to its targetHost, while a local session (no targetHost)
// yields a local file link. This is what makes the Sessions-page launch reveal
// the existing window instead of opening a mismatched new one.
func (e *Engine) SessionDeepLink(path, targetHost string) string {
	if e.sessionLinker == nil || path == "" {
		return ""
	}
	return e.sessionLinker.DeepLink(path, targetHost)
}

// WorkItemKeyForSession resolves the key of the work item that surfaces the
// given session, or "" when the session is not part of any work item. It
// prefers an exact match on the registry entity id ("session:<key>"), then
// falls back to matching a session entity by workspace path, then by display
// name (covering sessions a collector surfaced under a different entity id).
func (e *Engine) WorkItemKeyForSession(sessionKey, name, path string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	entID := "session:" + sessionKey
	var byPath, byName string
	for i := range e.lastWorkItems {
		for _, ent := range e.lastWorkItems[i].Entities {
			if ent.Kind != "session" {
				continue
			}
			if ent.ID == entID {
				return e.lastWorkItems[i].Key
			}
			if byPath == "" && path != "" && ent.Coordinates["path"] == path {
				byPath = e.lastWorkItems[i].Key
			}
			if byName == "" && name != "" && ent.Title == name {
				byName = e.lastWorkItems[i].Key
			}
		}
	}
	if byPath != "" {
		return byPath
	}
	return byName
}

// deriveTicketProjects seeds correlation.Config.Projects from the daemon's
// explicit ticketProjects plus each jira directive's config.followed_projects,
// so ticket-key matching is restricted to real project keys out of the box
// whenever a jira directive is configured. rebuild further widens this at
// runtime with any project key actually observed on collected jira issues
// (see mergeObservedProjects), covering the zero-config case too.
func deriveTicketProjects(cfg config.DaemonConfig) []string {
	seen := map[string]bool{}
	var projects []string
	add := func(raw string) {
		for _, p := range splitProjectList(raw) {
			p = strings.ToUpper(strings.TrimSpace(p))
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			projects = append(projects, p)
		}
	}
	for _, p := range cfg.TicketProjects {
		add(p)
	}
	for _, d := range cfg.Directives {
		if d.Collector != "jira" {
			continue
		}
		add(d.Config["followed_projects"])
	}
	return projects
}

// splitProjectList mirrors collectors.parseFollowedList's separators
// (comma, semicolon, or any whitespace) so followed_projects parses the
// same way here without importing that unexported helper.
func splitProjectList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}

// mergeObservedProjects returns a copy of cfg whose Projects also includes
// any JIRA project key observed on collected jira signals (parsed from
// Fields["key"], e.g. "SALSA-1234" -> "SALSA"). This narrows ticket-key
// matching to real, known projects even with no ticketProjects configured,
// as soon as the jira collector has returned any issues. It's a no-op when
// cfg.TicketPattern is set, since Projects is ignored in that case.
func mergeObservedProjects(signals []model.Signal, cfg correlation.Config) correlation.Config {
	if cfg.TicketPattern != "" {
		return cfg
	}
	seen := make(map[string]bool, len(cfg.Projects))
	projects := make([]string, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		projects = append(projects, p)
	}
	for _, s := range signals {
		if s.Source != "jira" {
			continue
		}
		key := strings.TrimSpace(s.Fields["key"])
		idx := strings.Index(key, "-")
		if idx <= 0 {
			continue
		}
		p := strings.ToUpper(key[:idx])
		if seen[p] {
			continue
		}
		seen[p] = true
		projects = append(projects, p)
	}
	cfg.Projects = projects
	return cfg
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
				e.tickSchedules(ctx)
				e.sweepSessions()
			}
		}
	}()
}

// sweepSessions prunes sessions whose heartbeat has gone stale (silent for
// longer than the configured TTL) and rebuilds the snapshot if any were
// removed. A non-positive TTL disables sweeping.
func (e *Engine) sweepSessions() {
	ttl := e.cfg.Sessions.TTL()
	if ttl <= 0 {
		return
	}
	if removed := e.store.Sweep(ttl, time.Now()); len(removed) > 0 {
		e.rebuild()
	}
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
			cctx, cancel := context.WithTimeout(ctx, backgroundCollectTimeout)
			defer cancel()
			e.collectUnit(cctx, u)
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
// After a successful collect it evaluates automations against newly observed
// signals and (for state units) entity state transitions.
func (e *Engine) collectUnit(ctx context.Context, u *unit) {
	now := time.Now()
	opts := &collectors.CollectOpts{
		UserdataDir: e.cfg.ConfigDir,
		Until:       now,
		Scope:       collectors.ScopeInvolved,
		Mode:        u.mode,
		CorrCfg:     e.corrCfg,
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

	e.mu.Lock()
	prevIDs := signalIDSet(u.signals)
	snapKey := u.key.Directive + "/" + string(u.mode)
	prevEntities := e.entitySnapshots[snapKey]
	seeded := e.automationSeeded[snapKey]
	e.mu.Unlock()

	signals, err := e.reg.CollectUnit(ctx, d, u.mode, opts)

	var newSignals []model.Signal
	var nextEntities map[string]model.Entity

	e.mu.Lock()
	u.lastRun = now
	if err != nil {
		u.lastErr = err.Error()
	} else {
		u.lastErr = ""
		if u.mode == collectors.ModeState {
			u.signals = signals
			// State units: fire signal rules for newly appearing StableIDs,
			// and transition rules against the previous entity snapshot.
			newSignals = filterNewSignals(signals, prevIDs)
			nextEntities = e.entitiesByID(signals)
			e.entitySnapshots[snapKey] = nextEntities
		} else {
			// Events units: the collector already returns the window since
			// watermark; treat the whole batch as new for automation matching
			// (dedupe/cooldown in the dispatcher prevents re-fires).
			newSignals = signals
			u.signals = mergeEvents(u.signals, signals, cutoff)
			u.watermark = now
		}
		// The first successful collect only establishes the baseline; mark
		// it seeded so subsequent collects fire against genuinely new state.
		e.automationSeeded[snapKey] = true
	}
	if u.interval > 0 {
		u.nextDue = now.Add(u.interval)
	}
	e.mu.Unlock()

	// Skip firing on the first successful collect of a unit (seeded==false
	// here): startup/restart would otherwise replay the whole lookback
	// window (events) or every current item (state) as "new".
	if err == nil && seeded && e.automations != nil {
		e.evaluateAutomations(ctx, newSignals, prevEntities, nextEntities)
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
	signals = append(signals, e.freshAnnotationSignalsLocked()...)
	e.mu.Unlock()

	corrCfg := mergeObservedProjects(signals, e.corrCfg)
	entities := e.entitiesFrom(signals, corrCfg)
	workItems := correlation.BuildWorkItems(entities, corrCfg)
	// Annotation pass: backfill JIRA data for tickets that were referenced
	// (by a PR/branch/commit) but never collected. This is async and
	// TTL-cached; results land in e.annotations and trigger a follow-up
	// rebuild, so this rebuild proceeds with whatever is cached now.
	e.maybeAnnotate(correlation.DanglingTicketKeys(workItems, corrCfg))
	dashboard := e.buildDashboard(workItems, corrCfg)
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

// freshAnnotationSignalsLocked returns the non-expired positive annotation
// signals to union into the signal set. The caller must hold e.mu.
func (e *Engine) freshAnnotationSignalsLocked() []model.Signal {
	now := time.Now()
	out := make([]model.Signal, 0, len(e.annotations))
	for _, ent := range e.annotations {
		if ent.Signal == nil {
			continue
		}
		if now.Sub(ent.FetchedAt) > annotationPositiveTTL {
			continue
		}
		out = append(out, *ent.Signal)
	}
	return out
}

// maybeAnnotate kicks a background batched fetch for any dangling ticket keys
// that aren't already cached-and-fresh. It is single-flighted: while one fetch
// is in flight, subsequent calls no-op and rely on the next rebuild to retry.
func (e *Engine) maybeAnnotate(keys []string) {
	if len(keys) == 0 {
		return
	}
	now := time.Now()
	e.mu.Lock()
	var todo []string
	for _, k := range keys {
		if ent, ok := e.annotations[k]; ok && annotationEntryFresh(ent, now) {
			continue
		}
		todo = append(todo, k)
	}
	e.mu.Unlock()
	if len(todo) == 0 {
		return
	}
	dir, ok := e.jiraAnnotationDirective()
	if !ok {
		return
	}
	if !e.annotating.TryLock() {
		return
	}
	go func() {
		defer e.annotating.Unlock()
		e.runAnnotation(dir, todo)
	}()
}

// annotationEntryFresh reports whether a cached entry is still within its TTL
// (positive and negative entries age out on different schedules).
func annotationEntryFresh(ent annotationEntry, now time.Time) bool {
	ttl := annotationPositiveTTL
	if ent.Signal == nil {
		ttl = annotationNegativeTTL
	}
	return now.Sub(ent.FetchedAt) <= ttl
}

// runAnnotation performs the batched fetch, updates the cache (positive for
// keys JIRA returned, negative for the rest), and triggers a follow-up rebuild
// so the freshly fetched summaries appear. A fetch failure degrades
// gracefully: keys are cached negative and the row keeps its key + browse URL.
func (e *Engine) runAnnotation(dir userdata.Directive, keys []string) {
	ctx, cancel := context.WithTimeout(context.Background(), annotationFetchTimeout)
	defer cancel()
	opts := &collectors.CollectOpts{UserdataDir: e.cfg.ConfigDir}
	items, err := e.reg.ResolveRefs(ctx, dir, opts, keys)
	if err != nil {
		log.Printf("docentd: annotation fetch for %v failed: %v", keys, err)
	}
	byKey := make(map[string]model.Signal, len(items))
	for i := range items {
		if k := annotationKeyFromSignal(items[i]); k != "" {
			byKey[k] = items[i]
		}
	}
	now := time.Now()
	e.mu.Lock()
	for _, k := range keys {
		if sig, ok := byKey[k]; ok {
			s := sig
			e.annotations[k] = annotationEntry{Signal: &s, FetchedAt: now}
		} else {
			e.annotations[k] = annotationEntry{FetchedAt: now}
		}
	}
	e.mu.Unlock()
	e.rebuild()
}

// annotationKeyFromSignal extracts the upper-cased JIRA key a synthetic
// annotation signal carries (buildJiraItem stamps Fields["key"]).
func annotationKeyFromSignal(s model.Signal) string {
	if s.Fields == nil {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(s.Fields["key"]))
}

// jiraAnnotationDirective returns the base JIRA directive to resolve tickets
// through (the first enabled jira directive with a base_url). It deliberately
// uses the un-expanded directive so no status_tier is stamped on the fetched
// issues. Multi-instance routing by project key is a future refinement.
func (e *Engine) jiraAnnotationDirective() (userdata.Directive, bool) {
	for _, d := range e.cfg.Directives {
		if d.Collector != "jira" || !d.Enabled {
			continue
		}
		if strings.TrimSpace(d.Config["base_url"]) == "" {
			continue
		}
		return d, true
	}
	return userdata.Directive{}, false
}

// entitiesFrom maps signals to entities, enriches collector-provided session
// entities from the registry store, and injects registry-tracked sessions
// (ingest pipeline) that no collector surfaced — live ones (fresh heartbeat)
// and closed ones that still need follow-up.
func (e *Engine) entitiesFrom(signals []model.Signal, corrCfg correlation.Config) []model.Entity {
	entities := correlation.SignalsToEntities(signals, corrCfg)
	for i := range entities {
		ent := &entities[i]
		if ent.Kind != "session" {
			continue
		}
		name := ent.Title
		// Collector sessions carry only a leaf name; enrich from the registry
		// by name (the ingest pipeline holds the real identity + activity).
		if rec, ok := e.store.GetByName(name); ok {
			if rec.Color != "" {
				ent.State["color"] = rec.Color
			}
			if rec.TargetHost != "" {
				ent.Coordinates["host"] = rec.TargetHost
				ent.Coordinates["targetHost"] = rec.TargetHost
			}
			if rec.IDE != "" {
				ent.State["ide"] = rec.IDE
			}
			ent.State["attention"] = registry.SessionStatus(rec)
			la := registry.LatestActivity(rec)
			ent.State["lastActivity"] = la
			// Drive the work-item "last activity" from real event timestamps
			// (open / agent request / agent response / heartbeat), not the
			// collector poll time — the collectors deliberately leave
			// observedAt unset. correlation aggregates observedAt across a work
			// item's entities into its LastActivity.
			if la != "" {
				ent.State["observedAt"] = la
			}
		}
		if ent.State["color"] == "" {
			c := model.ColorForName(name)
			ent.State["color"] = c
			ent.State["fg"] = model.ForegroundForHex(c)
		}
	}

	ttl := e.cfg.Sessions.TTL()
	now := time.Now()
	for key, rec := range e.store.All() {
		status := registry.SessionStatus(rec)
		live := registry.IsFresh(rec, ttl, now)
		// Only surface sessions that are alive or still need a follow-up.
		if !live && status != "needs-followup" {
			continue
		}
		// Skip sessions a collector already surfaced (matched by leaf name).
		found := false
		for _, ent := range entities {
			if ent.Kind == "session" && ent.Title == rec.Name {
				found = true
				break
			}
		}
		if found {
			continue
		}
		title := rec.Name
		if title == "" {
			title = rec.Path
		}
		la := registry.LatestActivity(rec)
		ent := model.Entity{
			ID:          "session:" + key,
			Kind:        "session",
			Title:       title,
			State:       map[string]string{"attention": status, "lastActivity": la},
			Coordinates: map[string]string{},
		}
		if live {
			ent.State["live"] = "true"
		} else {
			ent.State["live"] = "false"
		}
		if la != "" {
			ent.State["observedAt"] = la
		}
		if rec.IDE != "" {
			ent.State["ide"] = rec.IDE
		}
		if rec.TargetHost != "" {
			ent.Coordinates["host"] = rec.TargetHost
			ent.Coordinates["targetHost"] = rec.TargetHost
		}
		if rec.IDEHost != "" {
			ent.Coordinates["ideHost"] = rec.IDEHost
		}
		if rec.Path != "" {
			ent.Coordinates["path"] = rec.Path
		}
		if rec.Ticket != "" {
			ent.Coordinates["ticket"] = rec.Ticket
		}
		if rec.Color != "" {
			ent.State["color"] = rec.Color
			ent.State["fg"] = rec.FG
		}
		entities = append(entities, ent)
	}
	return entities
}

func (e *Engine) buildDashboard(workItems []model.WorkItem, corrCfg correlation.Config) Dashboard {
	groups := make([]DashboardGroup, 0, len(workItems))
	liveCount := 0
	for _, wi := range workItems {
		g := DashboardGroup{
			Key:          wi.Key,
			Ticket:       wi.Key,
			Summary:      wi.Title,
			Repo:         wi.Repo,
			Branch:       wi.Branch,
			OpenPath:     wi.OpenPath,
			DeepLink:     e.deepLinkFor(wi.OpenPath),
			LastActivity: wi.LastActivity,
			Color:        wi.Color,
			FG:           wi.FG,
			Sessions:     []DashboardSession{},
			PRs:          []DashboardPR{},
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
					URL:    e.ticketURL(tr.URL, tr.Key),
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
		var facts workitem.Facts
		for _, ent := range wi.Entities {
			switch ent.Kind {
			case "session":
				live := ent.State["live"] == "true"
				// Any attached session means an IDE window is registered/open
				// for this work item, so it pins to the top even when idle
				// (no fresh heartbeat). A fresh heartbeat additionally marks it
				// as live for the "N live" count and green status.
				facts.HasOpenSession = true
				if live {
					liveCount++
					facts.HasLiveSession = true
				}
				// A ticket-anchored session means a local checkout exists
				// for that ticket (branch evidence for "started"). A
				// ticketless session is still shown when live via
				// HasLiveSession, but doesn't imply a ticket branch.
				if ent.Coordinates["ticket"] != "" {
					facts.BranchEvidence = true
				}
				status := ent.State["attention"]
				if status == "" {
					status = "idle"
				}
				if status == "needs-followup" {
					facts.SessionNeedsFollowup = true
				}
				ds := DashboardSession{
					Kind:          "session",
					Name:          ent.Title,
					IDE:           ent.State["ide"],
					Host:          ent.Coordinates["host"],
					TargetHost:    ent.Coordinates["targetHost"],
					Path:          ent.Coordinates["path"],
					Ticket:        correlation.ParseTicketKey(ent.Title, corrCfg),
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
						facts.JiraStarted = true
					case "assigned":
						facts.JiraAssigned = true
					}
				}
			case "branch", "commit", "reflog":
				// Repo/branch units always have local git evidence.
				// Legacy ticket-keyed units only count when ticket-anchored.
				if strings.HasPrefix(wi.Key, "wb:") {
					facts.BranchEvidence = true
				} else if ent.Coordinates["ticket"] != "" {
					facts.BranchEvidence = true
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
						workitem.ClassifyPR(&facts, ent)
					}
				}
			}
		}
		// A resolved ticket key without a collected JIRA entity still gets a
		// synthesized browse link so its dashboard link is clickable.
		if g.JiraURL == "" && correlation.ParseTicketKey(g.Ticket, corrCfg) != "" {
			g.JiraURL = e.ticketBrowseURL(g.Ticket)
		}
		g.Status, g.StatusRank, g.ActionRequired = workitem.Classify(facts)
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
		Provider:     e.providerKey(),
		SSHHost:      e.cfg.SSHHost,
		Groups:       groups,
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
	DeepLink     string             `json:"deepLink,omitempty"`
	Provider     string             `json:"provider,omitempty"`
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
		DeepLink:     e.deepLinkFor(wi.OpenPath),
		Provider:     e.providerKey(),
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
				URL:    e.ticketURL(tr.URL, tr.Key),
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

// OpenResult is the payload for POST /api/workitems/{key}/open.
type OpenResult struct {
	OK          bool   `json:"ok"`
	Provider    string `json:"provider,omitempty"`
	DeepLink    string `json:"deepLink,omitempty"`
	ColorSynced bool   `json:"colorSynced"`
	Message     string `json:"message,omitempty"`
	Error       string `json:"error,omitempty"`
}

// OpenWorkItem prepares a work item to be opened in the editor. For the cursor
// provider with color-writing enabled (the default), it syncs the work item's
// current color into its repo's .vscode/settings.json so the title-bar color is
// in sync before the client navigates the deep link. The write runs on the
// docentd box (which holds the repo files); actually opening/focusing the window
// is the client's job via the returned deep link. ok=false when key is unknown.
func (e *Engine) OpenWorkItem(key string) (OpenResult, bool) {
	detail, ok := e.WorkItem(key)
	if !ok {
		return OpenResult{}, false
	}
	res := OpenResult{OK: true, Provider: e.providerKey(), DeepLink: detail.DeepLink}
	if e.providerKey() == "cursor" && e.cfg.OpenTrigger.Cursor.WriteColorEnabled() && detail.OpenPath != "" {
		color, fg := detail.Color, detail.FG
		if color == "" {
			color = model.ColorForName(detail.Key)
			fg = model.ForegroundForHex(color)
		}
		if err := model.SyncVSCodeColor(detail.OpenPath, color, fg); err != nil {
			res.OK = false
			res.Error = err.Error()
			return res, true
		}
		res.ColorSynced = true
		res.Message = "synced color"
	}
	return res, true
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

// EnsureDirectives injects the always-on webhook inbox. Live-window polling is
// no longer coupled to the open_trigger provider: to list live windows, declare
// an explicit "cursor" or "wsm" collector directive in config.yaml. Session
// activity also arrives via the ingest API (POST /api/sessions/events).
func EnsureDirectives(d []userdata.Directive) []userdata.Directive {
	hasWebhook := false
	for _, dir := range d {
		if dir.Collector == "webhook" {
			hasWebhook = true
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
	return out
}

func normalizeSessionProvider(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
}
