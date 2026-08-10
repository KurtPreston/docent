package report

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
)

type stubJiraResolver struct {
	byKey map[string]collectors.StatusItem
}

func (s stubJiraResolver) CollectEvents(context.Context, userdata.Directive, *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	return nil, nil
}

func (s stubJiraResolver) CollectState(context.Context, userdata.Directive, *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	return nil, nil
}

func (s stubJiraResolver) ResolveRefs(_ context.Context, _ userdata.Directive, _ *collectors.CollectOpts, refs []string) ([]collectors.StatusItem, error) {
	var out []collectors.StatusItem
	for _, k := range refs {
		if item, ok := s.byKey[strings.ToUpper(k)]; ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func TestCorrelateBuildsWorkItemsAndBackfills(t *testing.T) {
	reg := collectors.NewRegistry(time.Now)
	reg.Register("jira", stubJiraResolver{
		byKey: map[string]collectors.StatusItem{
			"SALSA-42": {
				Source: "jira",
				Kind:   "issue",
				Title:  "SALSA-42 Fix the widget",
				URL:    "https://jira.example/browse/SALSA-42",
				Fields: map[string]string{
					"key":              "SALSA-42",
					"status":           "In Development",
					"status_category":  "indeterminate",
					"status_tier":      "", // annotation path omits tier
				},
				ObservedAt: time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC),
			},
		},
	})

	cfg := userdata.ConfigFile{
		Directives: []userdata.Directive{{
			ID:        "jira",
			Collector: "jira",
			Enabled:   true,
			Config:    map[string]string{"base_url": "https://jira.example"},
		}},
	}

	// A PR whose title carries the ticket but no JIRA signal was collected —
	// correlation should create a dangling ticket, and Annotate should fill it.
	signals := []collectors.StatusItem{{
		Source:     "github",
		Kind:       "authored_pr",
		Title:      "[SALSA-42] Fix the widget",
		URL:        "https://git.example/o/r/pull/7",
		Repository: "o/r",
		ObservedAt: time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC),
		Fields: map[string]string{
			"state":       "open",
			"head_branch": "salsa-42-fix",
		},
		IsSelf: true,
	}}

	workItems, enriched, err := Correlate(context.Background(), reg, cfg, signals, CorrelateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(enriched) != 2 {
		t.Fatalf("expected original + annotation signal, got %d", len(enriched))
	}
	if len(workItems) == 0 {
		t.Fatal("expected at least one work item")
	}

	found := false
	for _, wi := range workItems {
		for _, tr := range wi.Tickets {
			if tr.Key == "SALSA-42" {
				found = true
				if tr.Title == "" {
					t.Fatalf("expected backfilled title on SALSA-42, got %+v", tr)
				}
				if !strings.Contains(tr.Title, "Fix the widget") && tr.Title != "SALSA-42 Fix the widget" {
					// Title may be the full "KEY summary" from buildJiraItem.
					if tr.URL == "" {
						t.Fatalf("expected URL on backfilled ticket: %+v", tr)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("SALSA-42 not found on any work item: %+v", workItems)
	}
}

func TestCorrelateNoJiraDirectiveSkipsBackfill(t *testing.T) {
	reg := collectors.NewRegistry(time.Now)
	signals := []model.Signal{{
		Source:     "github",
		Kind:       "authored_pr",
		Title:      "[SALSA-1] thing",
		URL:        "https://git.example/o/r/pull/1",
		Repository: "o/r",
		ObservedAt: time.Now(),
		Fields:     map[string]string{"head_branch": "salsa-1"},
	}}
	workItems, enriched, err := Correlate(context.Background(), reg, userdata.ConfigFile{}, signals, CorrelateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(enriched) != 1 {
		t.Fatalf("expected no annotation without jira directive, got %d signals", len(enriched))
	}
	_ = workItems
}

// corrCfgSpy records the CorrCfg the collector was handed.
type corrCfgSpy struct {
	got correlation.Config
}

func (s *corrCfgSpy) CollectEvents(_ context.Context, _ userdata.Directive, opts *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	s.got = opts.CorrCfg
	return nil, nil
}

func (s *corrCfgSpy) CollectState(context.Context, userdata.Directive, *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	return nil, nil
}

// Collect must hand collectors the directive-declared ticket-matching config.
// A zero correlation.Config disables matching outright, which silently stops
// local-git from stamping the branch-derived ticket that anchors commits and
// reflog rows whose own text never names one.
func TestCollectPassesTicketMatchingConfigToCollectors(t *testing.T) {
	spy := &corrCfgSpy{}
	reg := collectors.NewRegistry(time.Now)
	reg.Register("spy", spy)

	cfg := userdata.ConfigFile{
		Directives: []userdata.Directive{
			{
				ID:        "jira",
				Collector: "jira",
				Enabled:   true,
				Config: map[string]string{
					"base_url":          "https://jira.example",
					"followed_projects": "JASPER, TANGO",
				},
			},
			{ID: "spy", Collector: "spy", Enabled: true},
		},
	}

	if _, err := Collect(context.Background(), reg, cfg, executionmode.ResolvedRun{
		Collect: executionmode.CollectEvents,
	}, CollectOptions{}); err != nil {
		t.Fatal(err)
	}

	if !spy.got.AllowGeneric {
		t.Error("AllowGeneric = false, want true (an enabled jira directive is configured)")
	}
	want := []string{"JASPER", "TANGO"}
	if !slices.Equal(spy.got.Projects, want) {
		t.Errorf("Projects = %v, want %v (from the jira directive's followed_projects)", spy.got.Projects, want)
	}
	// The config must actually match the keys those projects imply.
	if got := correlation.ScanTicketKey("jasper-4034-ioc-details", spy.got); got != "JASPER-4034" {
		t.Errorf("ScanTicketKey = %q, want JASPER-4034", got)
	}
}
