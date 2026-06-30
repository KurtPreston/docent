package collectors

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"github.com/kurt/slakkr-ai/libs/model"
)

// StatusItem is the historical name for model.Signal.
type StatusItem = model.Signal

// parseFollowedList splits a directive config string (used for
// `followed_repos` / `followed_projects`) into trimmed, non-empty entries.
// Accepts commas, semicolons, and any whitespace as separators so users can
// write `org/a, org/b` or `org/a org/b` or one-per-line.
func parseFollowedList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
	if len(fields) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FilterToSelf keeps only items the collector flagged as the configured
// user's own activity. collector_error rows always pass through so failures
// stay visible in self-only modes (daily-plan, recent-activity); the input
// order is preserved for callers that already sorted the slice.
func FilterToSelf(items []StatusItem) []StatusItem {
	if len(items) == 0 {
		return items
	}
	out := make([]StatusItem, 0, len(items))
	for _, item := range items {
		if item.IsSelf || item.Kind == "collector_error" {
			out = append(out, item)
		}
	}
	return out
}

// CollectOpts carries env resolution and the collection time window.
type CollectOpts struct {
	UserdataDir       string
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(DirectiveProgress)
	Since             time.Time
	Until             time.Time // window end; if zero, collectors use their clock
	// Scope controls how broadly each collector pulls data. Collectors
	// branch on this directly; the CLI no longer post-filters by self.
	// Mirrors executionmode.Scope without importing that package to keep
	// the dependency direction.
	Scope Scope
	// OnlyCollectorTypes, when non-empty, restricts collection to
	// directives whose `collector` value is in this set. Empty collects
	// every enabled directive (the historical default). Used by modes
	// that only need a subset of sources (e.g. `prs` → GitHub only).
	OnlyCollectorTypes []string
	// Mode selects the collection capability to invoke when a collector
	// supports both. ModeEvents (the default) runs the activity/timeline
	// path; ModeState runs the current-state snapshot path. The aggregate
	// Collect honors this; docentd's per-unit path passes the mode
	// explicitly via CollectUnit.
	Mode Mode
	// RunLog routes per-directive HTTP and subprocess activity into
	// the per-run log directory. Nil disables logging (the default for
	// tests). The runlog.Run type satisfies this interface; collectors
	// only see the small interfaces below to keep their dependency
	// surface narrow.
	RunLog RunLog
}

// DirectiveLogger captures per-directive HTTP and subprocess
// activity. The CLI builds a concrete logger that appends to
// userdata/logs/<run>/<directive-id>.log; tests can pass nil
// (collectors fall back to a noop logger).
type DirectiveLogger interface {
	LogHTTP(method, url string, reqBytes int, status int, resBytes int64, duration time.Duration, err error)
	LogExec(name string, args []string, exitCode int, stdoutBytes, stderrBytes int, duration time.Duration, err error)
	Note(format string, args ...any)
}

// RunLog is the per-run logger registry: one DirectiveLogger per
// configured directive ID. Returning nil from Directive is allowed and
// equivalent to a no-op logger.
type RunLog interface {
	Directive(id string) DirectiveLogger
}

// loggerFor returns the DirectiveLogger configured for directiveID,
// or a no-op when no run-log is wired in. Callers should always go
// through this helper so they don't have to nil-check at every call.
func loggerFor(opts *CollectOpts, directiveID string) DirectiveLogger {
	if opts == nil || opts.RunLog == nil {
		return nopDirectiveLogger{}
	}
	l := opts.RunLog.Directive(directiveID)
	if l == nil {
		return nopDirectiveLogger{}
	}
	return l
}

// nopDirectiveLogger is the fallback when no logger is wired up.
type nopDirectiveLogger struct{}

func (nopDirectiveLogger) LogHTTP(method, url string, reqBytes int, status int, resBytes int64, duration time.Duration, err error) {
}
func (nopDirectiveLogger) LogExec(name string, args []string, exitCode int, stdoutBytes, stderrBytes int, duration time.Duration, err error) {
}
func (nopDirectiveLogger) Note(format string, args ...any) {}

// Scope mirrors executionmode.Scope; defined here so the collectors package
// has no upward dependency on executionmode. See executionmode.Scope for
// the canonical doc comment.
type Scope string

const (
	ScopeUnset    Scope = ""
	ScopeSelf     Scope = "self"
	ScopeInvolved Scope = "involved"
	ScopeAll      Scope = "all"
)

// EffectiveScope returns the scope to honor when collecting. An empty/unset
// Scope resolves to ScopeInvolved (matches the default built-in modes).
func (o *CollectOpts) EffectiveScope() Scope {
	if o == nil || o.Scope == ScopeUnset {
		return ScopeInvolved
	}
	return o.Scope
}

// collectorAllowed reports whether a directive using the named collector
// should run under the current options. An empty OnlyCollectorTypes set
// allows everything (the historical default).
func (o *CollectOpts) collectorAllowed(name string) bool {
	if o == nil || len(o.OnlyCollectorTypes) == 0 {
		return true
	}
	for _, allowed := range o.OnlyCollectorTypes {
		if allowed == name {
			return true
		}
	}
	return false
}

func (o *CollectOpts) windowEnd(clock func() time.Time) time.Time {
	if o != nil && !o.Until.IsZero() {
		return o.Until
	}
	if clock != nil {
		return clock()
	}
	return time.Now()
}

type DirectiveProgress struct {
	DirectiveID string
	Description string
	Status      string
	Detail      string
	// Completed and Total describe optional per-collector progress. When
	// Total > 0 the CLI renders a progress bar (Completed / Total);
	// when zero, only Status and Detail are surfaced. Collectors are
	// free to revise Total upward mid-run (e.g. when a new work phase
	// adds units the collector didn't know about up front).
	Completed int
	Total     int
}

// reportProgress is the package-internal helper collectors call to push
// a DirectiveProgress update through opts.OnDirectiveUpdate. It is a
// no-op when no callback is wired in, so collectors don't have to
// nil-check at every emission site.
func reportProgress(opts *CollectOpts, p DirectiveProgress) {
	if opts == nil || opts.OnDirectiveUpdate == nil {
		return
	}
	opts.OnDirectiveUpdate(p)
}

// Mode selects which collection capability a directive exercises.
type Mode string

const (
	// ModeEvents collects activity within [opts.Since, window end]; callers
	// accumulate and age these out incrementally.
	ModeEvents Mode = "events"
	// ModeState collects the complete current set, independent of time;
	// callers replace prior signals on each collection.
	ModeState Mode = "state"
)

// EffectiveMode resolves an unset mode to ModeEvents (the historical default).
func (o *CollectOpts) EffectiveMode() Mode {
	if o == nil || o.Mode == "" {
		return ModeEvents
	}
	return o.Mode
}

// EventCollector answers "what happened recently": it returns events within
// [opts.Since, window end]. Callers accumulate/age-out these incrementally.
type EventCollector interface {
	CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error)
}

// StateCollector answers "what is true right now": it returns the complete
// current set, independent of any time window. Callers replace prior signals.
type StateCollector interface {
	CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error)
}

// ValidationIssue describes a single problem with a directive's configuration
// or runtime environment that would prevent (or degrade) collection. Validators
// return zero or more issues; an empty slice means the directive looks ready.
type ValidationIssue struct {
	DirectiveID string
	Description string // directive Name, populated by Registry.Validate when blank
	Collector   string // collector name, populated by Registry.Validate when blank
	Field       string // optional pointer to the offending config field
	Message     string
	Remediation string
}

// ValidateOpts mirrors the parts of CollectOpts validators need (env lookup
// against userdata/.env and the same path expansion as collection).
type ValidateOpts struct {
	UserdataDir    string
	ExpandRepoPath func(string) string
}

// Validator is optionally implemented by Collectors. ValidateDirective inspects
// a directive's environment (config, credentials, on-disk paths, network auth)
// and reports user-facing issues with remediation hints. It must not write to
// stdout/stderr; surface findings through the returned slice instead.
type Validator interface {
	ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue
}

type Registry struct {
	collectors map[string]any
	clock      func() time.Time
}

func NewRegistry(clock func() time.Time) *Registry {
	if clock == nil {
		clock = time.Now
	}
	registry := &Registry{collectors: map[string]any{}, clock: clock}
	registry.Register("local-git", LocalGitCollector{Clock: clock})
	registry.Register("github", GitHubCollector{Clock: clock})
	registry.Register("github-enterprise", GitHubCollector{Clock: clock})
	registry.Register("gitea", GiteaCollector{Clock: clock, HTTP: nil})
	registry.Register("jira", JiraCollector{Clock: clock, HTTP: nil})
	registry.Register("google-calendar", GoogleCalendarCollector{Clock: clock, HTTP: nil})
	registry.Register("slack", SlackCollector{Clock: clock, HTTP: nil})
	registry.Register("docent-wm", DocentWMCollector{Clock: clock})
	registry.Register("webhook", WebhookCollector{Clock: clock})
	return registry
}

// Register adds a collector under name. A collector may implement
// StateCollector, EventCollector, or both; capability is resolved at
// collection time via type assertion.
func (r *Registry) Register(name string, collector any) {
	r.collectors[name] = collector
}

// Capabilities reports which modes the named collector supports.
func (r *Registry) Capabilities(name string) (state bool, events bool) {
	c, ok := r.collectors[name]
	if !ok {
		return false, false
	}
	_, state = c.(StateCollector)
	_, events = c.(EventCollector)
	return state, events
}

// CollectUnit runs a single directive in the given mode, dispatching to the
// collector's StateCollector or EventCollector capability. It is the entry
// point docentd uses for per-(directive, mode) collection units; a collector
// that lacks the requested capability is a hard error here.
func (r *Registry) CollectUnit(ctx context.Context, d userdata.Directive, mode Mode, opts *CollectOpts) ([]StatusItem, error) {
	c, ok := r.collectors[d.Collector]
	if !ok {
		return nil, fmt.Errorf("directive %s uses unknown collector %q", d.ID, d.Collector)
	}
	switch mode {
	case ModeState:
		sc, ok := c.(StateCollector)
		if !ok {
			return nil, fmt.Errorf("collector %q does not support state mode", d.Collector)
		}
		return sc.CollectState(ctx, d, opts)
	default:
		ec, ok := c.(EventCollector)
		if !ok {
			return nil, fmt.Errorf("collector %q does not support events mode", d.Collector)
		}
		return ec.CollectEvents(ctx, d, opts)
	}
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.collectors))
	for name := range r.collectors {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Collect runs enabled directives in parallel. Each directive must use CollectOpts.Since/Until.
func (r *Registry) Collect(ctx context.Context, directives []userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	enabled := make([]userdata.Directive, 0, len(directives))
	for _, directive := range directives {
		if !directive.Enabled {
			continue
		}
		if !opts.collectorAllowed(directive.Collector) {
			continue
		}
		enabled = append(enabled, directive)
	}
	for _, directive := range enabled {
		if _, ok := r.collectors[directive.Collector]; !ok {
			return nil, fmt.Errorf("directive %s uses unknown collector %q", directive.ID, directive.Collector)
		}
	}
	type directiveResult struct {
		items []StatusItem
	}
	results := make([]directiveResult, len(enabled))
	var wg sync.WaitGroup
	for i, directive := range enabled {
		wg.Add(1)
		go func(index int, d userdata.Directive) {
			defer wg.Done()
			results[index].items = r.collectDirective(ctx, d, opts)
		}(i, directive)
	}
	wg.Wait()
	var all []StatusItem
	for i := range results {
		all = append(all, results[i].items...)
	}
	return all, nil
}

// Validate runs every enabled directive's Validator (if its collector
// implements one) in parallel and returns the aggregated issues, sorted to
// match the directive order in the input slice for stable output.
func (r *Registry) Validate(ctx context.Context, directives []userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	type bucket struct {
		index int
		items []ValidationIssue
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		buckets []bucket
	)
	for i, d := range directives {
		if !d.Enabled {
			continue
		}
		index := i
		directive := d
		collector, ok := r.collectors[directive.Collector]
		if !ok {
			mu.Lock()
			buckets = append(buckets, bucket{index: index, items: []ValidationIssue{{
				DirectiveID: directive.ID,
				Description: directive.Name,
				Collector:   directive.Collector,
				Message:     fmt.Sprintf("unknown collector %q", directive.Collector),
				Remediation: "fix the directive's `collector` field or register a custom collector",
			}}})
			mu.Unlock()
			continue
		}
		validator, ok := collector.(Validator)
		if !ok {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := validator.ValidateDirective(ctx, directive, opts)
			if len(items) == 0 {
				return
			}
			for j := range items {
				if items[j].DirectiveID == "" {
					items[j].DirectiveID = directive.ID
				}
				if items[j].Description == "" {
					items[j].Description = directive.Name
				}
				if items[j].Collector == "" {
					items[j].Collector = directive.Collector
				}
			}
			mu.Lock()
			buckets = append(buckets, bucket{index: index, items: items})
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].index < buckets[j].index })
	var all []ValidationIssue
	for _, b := range buckets {
		all = append(all, b.items...)
	}
	return all
}

func (r *Registry) collectDirective(ctx context.Context, d userdata.Directive, opts *CollectOpts) []StatusItem {
	mode := opts.EffectiveMode()
	if state, events := r.Capabilities(d.Collector); (mode == ModeState && !state) || (mode == ModeEvents && !events) {
		// The directive's collector doesn't participate in this mode
		// (e.g. an event-only collector during a state run). Skip it
		// silently rather than emitting a collector_error row.
		return nil
	}
	reportProgress(opts, DirectiveProgress{
		DirectiveID: d.ID,
		Description: d.Name,
		Status:      "running",
		Detail:      "starting",
	})
	items, err := r.CollectUnit(ctx, d, mode, opts)
	if err != nil {
		// When the run was aborted (the user pressed the abort key, which
		// cancels the collection context), don't surface a collector_error
		// row. Treat it as a clean stop and keep whatever partial items the
		// collector managed to return before unwinding.
		if ctx.Err() != nil {
			reportProgress(opts, DirectiveProgress{
				DirectiveID: d.ID,
				Description: d.Name,
				Status:      "done",
				Detail:      fmt.Sprintf("aborted, %d item(s)", len(items)),
				Completed:   1,
				Total:       1,
			})
			return items
		}
		reportProgress(opts, DirectiveProgress{
			DirectiveID: d.ID,
			Description: d.Name,
			Status:      "error",
			Detail:      err.Error(),
		})
		return []StatusItem{{
			DirectiveID: d.ID,
			Source:      d.Collector,
			Kind:        "collector_error",
			Title:       d.Name,
			Summary:     err.Error(),
			Severity:    "error",
			ObservedAt:  r.clock(),
			IsSelf:      true,
		}}
	}
	// On success, fill the progress bar (Completed == Total) so the
	// final render shows a full bar even for collectors that never
	// emitted intermediate progress with a denominator. The item count
	// remains the user-visible detail.
	reportProgress(opts, DirectiveProgress{
		DirectiveID: d.ID,
		Description: d.Name,
		Status:      "done",
		Detail:      fmt.Sprintf("%d item(s)", len(items)),
		Completed:   1,
		Total:       1,
	})
	return items
}
