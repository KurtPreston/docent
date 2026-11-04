package collectors

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCollectStateRunsSearchesTogether proves the open-PR searches overlap each
// other and the recently-closed lookups. Each rule below waits for a marker that
// a later call (in the old, one-at-a-time order) creates, so running them in
// turn times out instead of passing.
func TestCollectStateRunsSearchesTogether(t *testing.T) {
	gh := newFakeGH(t)
	watched := fixturePR{Number: 7, Author: "teammate", Paths: []string{"services/api/main.go"}}
	gh.on(ghRule{Pattern: "is:pr is:open repo:my-org/my-repo", Out: watchPageJSON(t, watched), WaitFor: "authored-started"})
	gh.on(ghRule{Pattern: "--author my-user --state open", Out: "[]", Touch: "authored-started", WaitFor: "closed-started"})
	gh.on(ghRule{Pattern: "--author my-user --state closed", Out: "[]", Touch: "closed-started"})
	gh.on(ghRule{Pattern: "--review-requested my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "--assignee my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "-F number=7", Out: prDetailJSON("teammate")})

	items, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}

	// The output is what the one-at-a-time collection produced
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d: %+v", len(items), items)
	}
	got := items[0]
	if got.Kind != "pr_review_status" || got.URL != watched.url() {
		t.Fatalf("unexpected item: %+v", got)
	}
	if got.Fields["relation"] != "api" || got.Fields["checks"] != "passing" || got.Fields["reviewable"] != "true" {
		t.Fatalf("unexpected fields: %+v", got.Fields)
	}
	if n := len(gh.calls()); n != 6 {
		t.Fatalf("want 6 gh calls, got %d: %v", n, gh.calls())
	}
}

// TestCollectStateReportsFirstFailingSearchInOrder keeps the error a failed
// collection reports the same as when searches ran in turn: the earliest spec's.
func TestCollectStateReportsFirstFailingSearchInOrder(t *testing.T) {
	gh := newFakeGH(t)
	gh.on(ghRule{Pattern: "is:pr is:open repo:my-org/my-repo", Out: "watch broke", Exit: 1, WaitFor: "requested-failed"})
	gh.on(ghRule{Pattern: "--review-requested my-user", Out: "requested broke", Exit: 1, Touch: "requested-failed"})
	gh.on(ghRule{Pattern: "--state", Out: "[]"})

	_, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err == nil {
		t.Fatal("want an error")
	}
	if want := "search \"is:pr is:open repo:my-org/my-repo"; !strings.Contains(err.Error(), want) {
		t.Fatalf("want the watch search's error first, got: %v", err)
	}
}
