package collectors

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestCollectStateDropsPRsClosedSinceSearch checks that a PR search still
// lists but whose detail lookup reports merged is left out, while open PRs and
// their base branch come through.
func TestCollectStateDropsPRsClosedSinceSearch(t *testing.T) {
	open := fixturePR{Number: 7, Author: "teammate", Paths: []string{"services/api/main.go"}}
	merged := fixturePR{Number: 8, Author: "teammate", Paths: []string{"services/api/old.go"}}
	gh := newFakeGH(t)
	gh.on(ghRule{Pattern: "is:pr is:open repo:my-org/my-repo", Out: watchPageJSON(t, open, merged)})
	gh.on(ghRule{Pattern: "--state", Out: "[]"})
	gh.on(ghRule{Pattern: "--review-requested my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "--assignee my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "-F number=7", Out: prDetailJSON("teammate")})
	gh.on(ghRule{Pattern: "-F number=8", Out: strings.Replace(prDetailJSON("teammate"), `"state":"OPEN"`, `"state":"MERGED"`, 1)})

	items, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}
	if len(items) != 1 || items[0].URL != open.url() {
		t.Fatalf("want only the open PR, got %+v", items)
	}
}

// TestPRDetailClosedNeedsAKnownState checks that only a state the lookup
// actually reported can rule a PR out.
func TestPRDetailClosedNeedsAKnownState(t *testing.T) {
	for state, want := range map[string]bool{"": false, "OPEN": false, "CLOSED": true, "MERGED": true} {
		if got := (prDetail{State: state}).closed(); got != want {
			t.Errorf("closed() with state %q = %v, want %v", state, got, want)
		}
	}
}
