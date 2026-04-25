package collectors

import (
	"context"
	"fmt"
	"sort"
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
	registry.Register("github-activity", PlaceholderCollector{Clock: clock, Source: "github-activity"})
	registry.Register("github-enterprise", GitHubCollector{Clock: clock})
	registry.Register("gitea", PlaceholderCollector{Clock: clock, Source: "gitea"})
	registry.Register("jira", PlaceholderCollector{Clock: clock, Source: "jira"})
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
	var all []StatusItem
	for _, directive := range directives {
		if !directive.Enabled {
			continue
		}
		collector, ok := r.collectors[directive.Collector]
		if !ok {
			return all, fmt.Errorf("directive %s uses unknown collector %q", directive.ID, directive.Collector)
		}
		items, err := collector.Collect(ctx, directive, opts)
		if err != nil {
			all = append(all, StatusItem{
				DirectiveID: directive.ID,
				ProjectID:   directive.ProjectID,
				Source:      directive.Collector,
				Kind:        "collector_error",
				Title:       directive.Name,
				Summary:     err.Error(),
				Severity:    "error",
				ObservedAt:  r.clock(),
			})
			continue
		}
		all = append(all, items...)
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
