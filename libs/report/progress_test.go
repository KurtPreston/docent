package report

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
)

// overlapCollector's event collection waits for its state collection to start,
// so a run that does the passes one after the other times out instead.
type overlapCollector struct {
	stateStarted chan struct{}
	once         *sync.Once
	eventsErr    error
}

func (c overlapCollector) CollectEvents(ctx context.Context, d userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	select {
	case <-c.stateStarted:
	case <-time.After(5 * time.Second):
		return nil, errors.New("state pass never started while events ran")
	}
	if c.eventsErr != nil {
		return nil, c.eventsErr
	}
	return []collectors.StatusItem{{DirectiveID: d.ID, Kind: "event", IsSelf: true}}, nil
}

func (c overlapCollector) CollectState(_ context.Context, d userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	c.once.Do(func() { close(c.stateStarted) })
	return []collectors.StatusItem{{DirectiveID: d.ID, Kind: "state", IsSelf: true}}, nil
}

// eventsOnly is a collector with no state capability.
type eventsOnly struct{}

func (eventsOnly) CollectEvents(_ context.Context, d userdata.Directive, _ *collectors.CollectOpts) ([]collectors.StatusItem, error) {
	return []collectors.StatusItem{{DirectiveID: d.ID, Kind: "other-event", IsSelf: true}}, nil
}

// overlapSetup registers a both-mode "github" fake and an events-only fake.
func overlapSetup(eventsErr error) (*collectors.Registry, userdata.ConfigFile) {
	reg := collectors.NewRegistry(nil)
	reg.Register("github", overlapCollector{stateStarted: make(chan struct{}), once: &sync.Once{}, eventsErr: eventsErr})
	reg.Register("other", eventsOnly{})
	cfg := userdata.ConfigFile{Directives: []userdata.Directive{
		{ID: "gh", Name: "GitHub", Collector: "github", Enabled: true},
		{ID: "other", Name: "Other", Collector: "other", Enabled: true},
	}}
	return reg, cfg
}

// progressLog records forwarded progress in order.
type progressLog struct {
	mu  sync.Mutex
	all []collectors.DirectiveProgress
}

func (l *progressLog) add(p collectors.DirectiveProgress) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.all = append(l.all, p)
}

// forDirective returns one directive's updates in order.
func (l *progressLog) forDirective(id string) []collectors.DirectiveProgress {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []collectors.DirectiveProgress
	for _, p := range l.all {
		if p.DirectiveID == id {
			out = append(out, p)
		}
	}
	return out
}

func TestCollectBothRunsPassesTogether(t *testing.T) {
	reg, cfg := overlapSetup(nil)
	run := executionmode.ResolvedRun{Collect: executionmode.CollectBoth, Until: time.Now()}
	var log progressLog

	items, err := Collect(context.Background(), reg, cfg, run, CollectOptions{OnDirectiveUpdate: log.add})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Events come first, then state, in directive order within each pass
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Kind)
	}
	if got := strings.Join(kinds, ","); got != "event,other-event,state" {
		t.Fatalf("item order: %s", got)
	}

	// The two-pass directive stays running until both passes finish
	gh := log.forDirective("gh")
	for _, p := range gh[:len(gh)-1] {
		if p.Status != "running" {
			t.Fatalf("intermediate update ended the row early: %+v (all: %+v)", p, gh)
		}
	}
	last := gh[len(gh)-1]
	if last.Status != "done" || !strings.Contains(last.Detail, "events: 1 item(s)") || !strings.Contains(last.Detail, "state: 1 item(s)") {
		t.Fatalf("final update: %+v", last)
	}

	// A one-pass directive's progress is untouched
	other := log.forDirective("other")
	if final := other[len(other)-1]; final.Status != "done" || final.Detail != "1 item(s)" {
		t.Fatalf("single-pass directive progress changed: %+v", other)
	}
}

func TestCollectBothKeepsPassErrorVisible(t *testing.T) {
	reg, cfg := overlapSetup(errors.New("events broke"))
	run := executionmode.ResolvedRun{Collect: executionmode.CollectBoth, Until: time.Now()}
	var log progressLog

	items, err := Collect(context.Background(), reg, cfg, run, CollectOptions{OnDirectiveUpdate: log.add})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	gh := log.forDirective("gh")
	last := gh[len(gh)-1]
	if last.Status != "error" || !strings.Contains(last.Detail, "events: events broke") {
		t.Fatalf("final update lost the events error: %+v", last)
	}
	found := false
	for _, it := range items {
		found = found || (it.Kind == "collector_error" && it.DirectiveID == "gh")
	}
	if !found {
		t.Fatalf("missing collector_error row: %+v", items)
	}
}

func TestCollectSinglePassForwardsProgressUnchanged(t *testing.T) {
	reg, cfg := overlapSetup(nil)
	cfg.Directives = cfg.Directives[1:]
	run := executionmode.ResolvedRun{Collect: executionmode.CollectEvents, Until: time.Now()}
	var log progressLog

	if _, err := Collect(context.Background(), reg, cfg, run, CollectOptions{OnDirectiveUpdate: log.add}); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for _, p := range log.forDirective("other") {
		if strings.Contains(p.Detail, "events: ") {
			t.Fatalf("single-pass run labeled its progress: %+v", p)
		}
	}
}
