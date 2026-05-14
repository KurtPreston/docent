package collectors

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type StatusItem struct {
	DirectiveID    string            `json:"directive_id"`
	Repository     string            `json:"repository,omitempty"` // grouping key when known (e.g. org/repo, local folder name)
	Source         string            `json:"source"`
	Kind           string            `json:"kind"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary"`
	URL            string            `json:"url,omitempty"`
	Severity       string            `json:"severity,omitempty"`
	ObservedAt     time.Time         `json:"observed_at"`
	Fields         map[string]string `json:"fields,omitempty"`
	StableID       string            `json:"stable_id,omitempty"`
	AttentionClass string            `json:"attention_class,omitempty"`
	ChangeState    string            `json:"change_state,omitempty"`
}

// CollectOpts carries env resolution and the collection time window.
type CollectOpts struct {
	UserdataDir       string
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(DirectiveProgress)
	Since             time.Time
	Until             time.Time // window end; if zero, collectors use their clock
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
}

// Collector gathers status items for events since opts.Since through window end.
type Collector interface {
	Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error)
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
	collectors map[string]Collector
	clock      func() time.Time
}

func NewRegistry(clock func() time.Time) *Registry {
	if clock == nil {
		clock = time.Now
	}
	registry := &Registry{collectors: map[string]Collector{}, clock: clock}
	registry.Register("local-git", LocalGitCollector{Clock: clock})
	registry.Register("github", GitHubCollector{Clock: clock})
	registry.Register("github-enterprise", GitHubCollector{Clock: clock})
	registry.Register("gitea", GiteaCollector{Clock: clock, HTTP: nil})
	registry.Register("jira", JiraCollector{Clock: clock, HTTP: nil})
	registry.Register("google-calendar", GoogleCalendarCollector{Clock: clock, HTTP: nil})
	return registry
}

func (r *Registry) Register(name string, collector Collector) {
	r.collectors[name] = collector
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
	collector := r.collectors[d.Collector]
	if opts != nil && opts.OnDirectiveUpdate != nil {
		opts.OnDirectiveUpdate(DirectiveProgress{
			DirectiveID: d.ID,
			Description: d.Name,
			Status:      "running",
			Detail:      "collecting",
		})
	}
	items, err := collector.Collect(ctx, d, opts)
	if err != nil {
		if opts != nil && opts.OnDirectiveUpdate != nil {
			opts.OnDirectiveUpdate(DirectiveProgress{
				DirectiveID: d.ID,
				Description: d.Name,
				Status:      "error",
				Detail:      err.Error(),
			})
		}
		return []StatusItem{{
			DirectiveID: d.ID,
			Source:      d.Collector,
			Kind:        "collector_error",
			Title:       d.Name,
			Summary:     err.Error(),
			Severity:    "error",
			ObservedAt:  r.clock(),
		}}
	}
	if opts != nil && opts.OnDirectiveUpdate != nil {
		opts.OnDirectiveUpdate(DirectiveProgress{
			DirectiveID: d.ID,
			Description: d.Name,
			Status:      "done",
			Detail:      fmt.Sprintf("%d item(s)", len(items)),
		})
	}
	return items
}
