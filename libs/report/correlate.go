package report

import (
	"context"
	"strings"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
)

// CorrelateOptions carries the knobs Correlate needs beyond the signal list.
type CorrelateOptions struct {
	ConfigDir string
}

// Correlate turns collected signals into annotated work items: seed ticket
// projects, build work items, backfill dangling JIRA tickets via ResolveRefs,
// and re-correlate. Returns the enriched signal list (original + annotation
// signals) and the final work items. Annotation failures degrade gracefully
// (work items keep key-only ticket refs).
func Correlate(ctx context.Context, reg *collectors.Registry, cfg userdata.ConfigFile, signals []collectors.StatusItem, opts CorrelateOptions) ([]model.WorkItem, []collectors.StatusItem, error) {
	corrCfg := correlationConfigFrom(cfg, signals)
	entities := correlation.SignalsToEntities(signals, corrCfg)
	workItems := correlation.BuildWorkItems(entities, corrCfg)

	keys := correlation.DanglingTicketKeys(workItems, corrCfg)
	if len(keys) == 0 {
		return workItems, signals, nil
	}
	dir, ok := jiraAnnotationDirective(cfg.Directives)
	if !ok || reg == nil {
		return workItems, signals, nil
	}

	annotated, err := reg.ResolveRefs(ctx, dir, &collectors.CollectOpts{
		UserdataDir: opts.ConfigDir,
	}, keys)
	if err != nil {
		// Degrade: keep what we have; dangling keys stay title-empty.
		return workItems, signals, nil
	}
	if len(annotated) == 0 {
		return workItems, signals, nil
	}

	enriched := append(append([]collectors.StatusItem{}, signals...), annotated...)
	corrCfg = correlationConfigFrom(cfg, enriched)
	entities = correlation.SignalsToEntities(enriched, corrCfg)
	workItems = correlation.BuildWorkItems(entities, corrCfg)
	return workItems, enriched, nil
}

func correlationConfigFrom(cfg userdata.ConfigFile, signals []collectors.StatusItem) correlation.Config {
	var followed []string
	for _, d := range cfg.Directives {
		if d.Collector != "jira" || !d.Enabled {
			continue
		}
		if v := strings.TrimSpace(d.Config["followed_projects"]); v != "" {
			followed = append(followed, v)
		}
	}
	seed := correlation.FollowedProjectsFromDirectives(followed)
	return correlation.SeedProjectsFromSignals(signals, correlation.Config{}, seed)
}

// jiraAnnotationDirective returns the first enabled jira directive with a
// base_url, deliberately without status_tier so ResolveRefs stamps display
// metadata only.
func jiraAnnotationDirective(directives []userdata.Directive) (userdata.Directive, bool) {
	for _, d := range directives {
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
