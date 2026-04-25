package collectors

import (
	"context"
	"fmt"
	"sort"
	"strings"
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
	// ManualAnswer, when set, is called for each manual collector directive so the user can reply (e.g. start_day on a TTY).
	// Collect runs those directives sequentially after other collectors finish.
	ManualAnswer func(ctx context.Context, d userdata.Directive, question string) (answer string, err error)
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
	var parallel []userdata.Directive
	var manualAfter []userdata.Directive
	if opts != nil && opts.ManualAnswer != nil {
		for _, directive := range enabled {
			if directive.Collector == "manual" {
				manualAfter = append(manualAfter, directive)
			} else {
				parallel = append(parallel, directive)
			}
		}
	} else {
		parallel = enabled
	}
	type directiveResult struct {
		items []StatusItem
	}
	results := make([]directiveResult, len(parallel))
	var wg sync.WaitGroup
	for i, directive := range parallel {
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
	for _, d := range manualAfter {
		all = append(all, r.collectDirective(ctx, d, opts)...)
	}
	return all, nil
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
			ProjectID:   d.ProjectID,
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

type ManualCollector struct {
	Clock func() time.Time
}

func (c ManualCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	prompt := directive.Target["prompt"]
	if prompt == "" {
		prompt = "Manual status requested"
	}
	var summary string
	kind := "manual_prompt"
	if opts != nil && opts.ManualAnswer != nil {
		answer, err := opts.ManualAnswer(ctx, directive, prompt)
		if err != nil {
			return nil, err
		}
		answer = strings.TrimSpace(answer)
		if answer == "" {
			answer = "(no answer)"
		}
		summary = "Q: " + prompt + " | A: " + answer
		kind = "manual_response"
	} else {
		summary = prompt
	}
	return []StatusItem{{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      "manual",
		Kind:        kind,
		Title:       directive.Name,
		Summary:     summary,
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
