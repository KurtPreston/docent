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
	ProjectID      string            `json:"project_id,omitempty"`
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

// CollectOpts carries host-scoped resolution data for collectors that need it (for example local-git).
type CollectOpts struct {
	HostID         string
	UserdataDir    string
	Projects       userdata.ProjectsFile
	Config         userdata.ConfigFile
	ExpandRepoPath func(string) string
	OnDirectiveUpdate func(DirectiveProgress)
}

type DirectiveProgress struct {
	DirectiveID string
	Description string
	Status      string
	Detail      string
}

type Collector interface {
	Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error)
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
	registry.Register("manual", ManualCollector{Clock: clock})
	registry.Register("local-git", LocalGitCollector{Clock: clock})
	registry.Register("github", GitHubCollector{Clock: clock})
	registry.Register("github-activity", GitHubActivityCollector{Clock: clock})
	registry.Register("github-enterprise", GitHubCollector{Clock: clock})
	registry.Register("gitea", GiteaCollector{Clock: clock})
	registry.Register("jira", JiraCollector{Clock: clock, HTTP: nil})
	registry.Register("google-calendar", GoogleCalendarCollector{Clock: clock, HTTP: nil})
	registry.Register("slack", PlaceholderCollector{Clock: clock, Source: "slack"})
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

func (r *Registry) Collect(ctx context.Context, directives []userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	type directiveResult struct {
		items []StatusItem
	}
	enabled := make([]userdata.Directive, 0, len(directives))
	for _, directive := range directives {
		if !directive.Enabled {
			continue
		}
		enabled = append(enabled, directive)
	}
	results := make([]directiveResult, len(enabled))
	var wg sync.WaitGroup
	for i, directive := range enabled {
		collector, ok := r.collectors[directive.Collector]
		if !ok {
			return nil, fmt.Errorf("directive %s uses unknown collector %q", directive.ID, directive.Collector)
		}
		if opts != nil && opts.OnDirectiveUpdate != nil {
			opts.OnDirectiveUpdate(DirectiveProgress{
				DirectiveID: directive.ID,
				Description: directive.Name,
				Status:      "running",
				Detail:      "collecting",
			})
		}
		wg.Add(1)
		go func(index int, d userdata.Directive, c Collector) {
			defer wg.Done()
			items, err := c.Collect(ctx, d, opts)
			if err != nil {
				if opts != nil && opts.OnDirectiveUpdate != nil {
					opts.OnDirectiveUpdate(DirectiveProgress{
						DirectiveID: d.ID,
						Description: d.Name,
						Status:      "error",
						Detail:      err.Error(),
					})
				}
				items = []StatusItem{{
					DirectiveID: d.ID,
					ProjectID:   d.ProjectID,
					Source:      d.Collector,
					Kind:        "collector_error",
					Title:       d.Name,
					Summary:     err.Error(),
					Severity:    "error",
					ObservedAt:  r.clock(),
				}}
				results[index] = directiveResult{items: items}
				return
			}
			if opts != nil && opts.OnDirectiveUpdate != nil {
				opts.OnDirectiveUpdate(DirectiveProgress{
					DirectiveID: d.ID,
					Description: d.Name,
					Status:      "done",
					Detail:      fmt.Sprintf("%d item(s)", len(items)),
				})
			}
			results[index] = directiveResult{items: items}
		}(i, directive, collector)
	}
	wg.Wait()
	var all []StatusItem
	for i := range results {
		all = append(all, results[i].items...)
	}
	return all, nil
}

type ManualCollector struct {
	Clock func() time.Time
}

func (c ManualCollector) Collect(_ context.Context, directive userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	prompt := directive.Target["prompt"]
	if prompt == "" {
		prompt = "Manual status requested"
	}
	return []StatusItem{{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      "manual",
		Kind:        "manual_prompt",
		Title:       directive.Name,
		Summary:     prompt,
		ObservedAt:  c.Clock(),
	}}, nil
}

type PlaceholderCollector struct {
	Clock  func() time.Time
	Source string
}

func (c PlaceholderCollector) Collect(_ context.Context, directive userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	return []StatusItem{{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      c.Source,
		Kind:        "not_configured",
		Title:       directive.Name,
		Summary:     "Collector interface is present, but this provider is not implemented yet.",
		Severity:    "info",
		ObservedAt:  c.Clock(),
	}}, nil
}
