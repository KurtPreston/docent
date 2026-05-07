package collectors

import (
	"context"
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
		Since:    time.Unix(0, 0).UTC(),
		Until:    time.Unix(1, 0).UTC(),
		CodeHome: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no items, got %d", len(items))
	}
}
