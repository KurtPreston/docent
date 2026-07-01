package collectors

import (
	"reflect"
	"testing"
	"time"
)

func TestFilterToSelfKeepsSelfAndCollectorErrors(t *testing.T) {
	at := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	items := []StatusItem{
		{DirectiveID: "git", Source: "local-git", Kind: "commit", Title: "mine", ObservedAt: at, IsSelf: true},
		{DirectiveID: "git", Source: "local-git", Kind: "commit", Title: "someone else's", ObservedAt: at.Add(time.Minute), IsSelf: false},
		{DirectiveID: "git", Source: "local-git", Kind: "reflog", Title: "checkout", ObservedAt: at.Add(2 * time.Minute), IsSelf: true},
		{DirectiveID: "gh", Source: "github", Kind: "collector_error", Title: "boom", Summary: "rate limit", ObservedAt: at.Add(3 * time.Minute), IsSelf: false},
	}

	got := FilterToSelf(items)

	want := []StatusItem{items[0], items[2], items[3]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FilterToSelf mismatch:\n got = %#v\nwant = %#v", got, want)
	}
}

func TestFilterToSelfPreservesOrder(t *testing.T) {
	t0 := time.Unix(0, 0).UTC()
	items := []StatusItem{
		{Title: "first", ObservedAt: t0, IsSelf: true},
		{Title: "drop1", ObservedAt: t0.Add(time.Second), IsSelf: false},
		{Title: "second", ObservedAt: t0.Add(2 * time.Second), IsSelf: true},
		{Title: "drop2", ObservedAt: t0.Add(3 * time.Second), IsSelf: false},
		{Title: "third", ObservedAt: t0.Add(4 * time.Second), IsSelf: true},
	}
	got := FilterToSelf(items)
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
	for i, want := range []string{"first", "second", "third"} {
		if got[i].Title != want {
			t.Fatalf("index %d: got title %q want %q", i, got[i].Title, want)
		}
	}
}

func TestFilterToSelfEmpty(t *testing.T) {
	if got := FilterToSelf(nil); got != nil {
		t.Fatalf("nil input should return nil, got %#v", got)
	}
	if got := FilterToSelf([]StatusItem{}); len(got) != 0 {
		t.Fatalf("empty input should return empty slice, got %#v", got)
	}
}
