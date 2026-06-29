package collectors

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestRegistryUnknownCollector(t *testing.T) {
	r := NewRegistry(time.Now)
	_, err := r.Collect(context.Background(), []userdata.Directive{
		{ID: "x", Name: "X", Collector: "nonexistent", Enabled: true},
	}, &CollectOpts{
		Since: time.Now().Add(-time.Hour),
		Until: time.Now(),
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCollectOptsWindowEnd(t *testing.T) {
	now := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	o := &CollectOpts{Until: now}
	if !o.windowEnd(func() time.Time { return time.Unix(0, 0) }).Equal(now) {
		t.Fatal("expected Until when set")
	}
	o2 := &CollectOpts{}
	clock := func() time.Time { return now }
	if !o2.windowEnd(clock).Equal(now) {
		t.Fatal("expected clock when Until zero")
	}
}

func TestRegistrySkipsDisabled(t *testing.T) {
	r := NewRegistry(func() time.Time { return time.Unix(0, 0).UTC() })
	items, err := r.Collect(context.Background(), []userdata.Directive{
		{ID: "x", Name: "X", Collector: "local-git", Enabled: false},
	}, &CollectOpts{
		Since: time.Unix(0, 0).UTC(),
		Until: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items, got %d", len(items))
	}
}

// stubCollector records whether it was collected and emits one item so
// the filter test can assert which directives actually ran.
type stubCollector struct {
	id     string
	called *[]string
}

func (s stubCollector) Collect(_ context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	*s.called = append(*s.called, d.ID)
	return []StatusItem{{DirectiveID: d.ID, Source: d.Collector, Kind: "stub", Title: d.ID}}, nil
}

func TestRegistryOnlyCollectorTypes(t *testing.T) {
	var called []string
	r := NewRegistry(func() time.Time { return time.Unix(0, 0).UTC() })
	r.Register("alpha", stubCollector{id: "alpha", called: &called})
	r.Register("beta", stubCollector{id: "beta", called: &called})

	directives := []userdata.Directive{
		{ID: "a", Name: "A", Collector: "alpha", Enabled: true},
		{ID: "b", Name: "B", Collector: "beta", Enabled: true},
		{ID: "a2", Name: "A2", Collector: "alpha", Enabled: true},
	}
	items, err := r.Collect(context.Background(), directives, &CollectOpts{
		OnlyCollectorTypes: []string{"alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected only the 2 alpha directives, got %d: %#v", len(items), items)
	}
	for _, id := range called {
		if id == "b" {
			t.Fatalf("beta collector should have been skipped, called: %v", called)
		}
	}

	// Empty set collects everything (historical default).
	called = nil
	items, err = r.Collect(context.Background(), directives, &CollectOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("empty OnlyCollectorTypes should collect all 3, got %d", len(items))
	}
}

// abortStubCollector simulates a collector that gathered one item and
// then unwound with a context error because the run was aborted.
type abortStubCollector struct{}

func (abortStubCollector) Collect(ctx context.Context, d userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	return []StatusItem{{DirectiveID: d.ID, Source: d.Collector, Kind: "partial", Title: "partial"}}, ctx.Err()
}

func TestRegistryAbortKeepsPartialResults(t *testing.T) {
	r := NewRegistry(func() time.Time { return time.Unix(0, 0).UTC() })
	r.Register("aborty", abortStubCollector{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // user pressed the abort key before this directive finished

	items, err := r.Collect(ctx, []userdata.Directive{
		{ID: "a", Name: "A", Collector: "aborty", Enabled: true},
	}, &CollectOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != "partial" {
		t.Fatalf("expected the partial item to be kept, got %#v", items)
	}
	for _, it := range items {
		if it.Kind == "collector_error" {
			t.Fatalf("aborted collection must not emit a collector_error row: %#v", items)
		}
	}
}

type stubValidator struct {
	Issues []ValidationIssue
	Err    error
	Calls  int
}

func (s *stubValidator) Collect(_ context.Context, _ userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	return nil, nil
}

func (s *stubValidator) ValidateDirective(_ context.Context, d userdata.Directive, _ *ValidateOpts) []ValidationIssue {
	s.Calls++
	out := make([]ValidationIssue, len(s.Issues))
	copy(out, s.Issues)
	for i := range out {
		// Leave DirectiveID/Collector/Description blank to confirm Registry.Validate fills them in.
		out[i].Message = fmt.Sprintf("%s: %s", d.ID, out[i].Message)
	}
	return out
}

func TestRegistryValidateAggregates(t *testing.T) {
	r := NewRegistry(time.Now)
	stub := &stubValidator{Issues: []ValidationIssue{{Field: "x", Message: "boom", Remediation: "fix it"}}}
	r.Register("stubby", stub)

	issues := r.Validate(context.Background(), []userdata.Directive{
		{ID: "a", Name: "Alpha", Collector: "stubby", Enabled: true},
		{ID: "b", Name: "Beta", Collector: "stubby", Enabled: false},
		{ID: "c", Name: "Gamma", Collector: "stubby", Enabled: true},
		{ID: "d", Name: "Delta", Collector: "nope", Enabled: true},
	}, &ValidateOpts{})

	if stub.Calls != 2 {
		t.Fatalf("expected validator called twice (enabled directives only), got %d", stub.Calls)
	}
	if len(issues) != 3 {
		t.Fatalf("expected 3 issues (a, c, d), got %d: %#v", len(issues), issues)
	}
	wantOrder := []string{"a", "c", "d"}
	for i, want := range wantOrder {
		if issues[i].DirectiveID != want {
			t.Fatalf("issue %d: directive id = %q, want %q", i, issues[i].DirectiveID, want)
		}
		if issues[i].Description == "" {
			t.Fatalf("issue %d: description not populated", i)
		}
		if issues[i].Collector == "" {
			t.Fatalf("issue %d: collector not populated", i)
		}
	}
	if got := issues[2].Message; got == "" {
		t.Fatalf("expected unknown-collector message, got empty")
	}
}
