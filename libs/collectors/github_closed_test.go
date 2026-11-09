package collectors

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// closedSearchJSON renders a closed-PR search reply, one row per URL and
// closedAt pair.
func closedSearchJSON(t *testing.T, pairs ...string) string {
	t.Helper()
	var rows []map[string]any
	for i := 0; i+1 < len(pairs); i += 2 {
		rows = append(rows, map[string]any{
			"title": "PR", "url": pairs[i], "state": "closed", "closedAt": pairs[i+1],
			"updatedAt": pairs[i+1], "createdAt": "2026-01-01T00:00:00Z",
			"repository": map[string]string{"nameWithOwner": "my-org/my-repo"},
		})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestClosedPRLookupsAreCached checks that a closed PR is looked up once and
// then served from the cache, that a failed lookup is retried, that closing
// anew forces a fresh lookup, and that PRs leaving the list are pruned.
func TestClosedPRLookupsAreCached(t *testing.T) {
	const a, b = "https://github.com/my-org/my-repo/pull/1", "https://github.com/my-org/my-repo/pull/2"
	d := userdata.Directive{ID: "gh", Name: "GitHub", Collector: "github", Enabled: true}
	opts := stateOpts()
	opts.CacheDir = t.TempDir()

	// Run one closed collect against a fresh fake gh and return the fake
	poll := func(search string, bFails bool) *fakeGH {
		f := newFakeGH(t)
		f.on(ghRule{Pattern: "--state closed", Out: search})
		f.on(ghRule{Pattern: "pull/1 --json headRefName,state", Out: `{"headRefName":"fix-a","state":"MERGED"}`})
		if bFails {
			f.on(ghRule{Pattern: "pull/2 --json", Out: "boom", Exit: 1})
		} else {
			f.on(ghRule{Pattern: "pull/2 --json", Out: `{"headRefName":"fix-b","state":"CLOSED"}`})
		}
		items, err := GitHubCollector{}.collectClosedPRs(context.Background(), os.Environ(), d, "my-user", "github.com", opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != strings.Count(search, `"url"`) {
			t.Fatalf("items = %d for search %s", len(items), search)
		}
		return f
	}

	// First poll looks both up; B's lookup fails and isn't kept
	f := poll(closedSearchJSON(t, a, "2026-01-02T00:00:00Z", b, "2026-01-02T00:00:00Z"), true)
	if f.countCalls("pr view") != 2 {
		t.Fatalf("first poll calls = %q", f.calls())
	}

	// Second poll serves A from the cache and retries B
	f = poll(closedSearchJSON(t, a, "2026-01-02T00:00:00Z", b, "2026-01-02T00:00:00Z"), false)
	if f.countCalls("pull/1") != 0 || f.countCalls("pull/2") != 1 {
		t.Fatalf("second poll calls = %q", f.calls())
	}

	// A closes anew and B leaves the list: A is looked up again, B is pruned
	f = poll(closedSearchJSON(t, a, "2026-01-02T12:00:00Z"), false)
	if f.countCalls("pull/1") != 1 {
		t.Fatalf("third poll calls = %q", f.calls())
	}
	cache, err := loadClosedCache(closedCachePath(opts, watchViewer{"github.com", "my-user", "gh"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.PRs[b]; ok || cache.PRs[a].ClosedAt != "2026-01-02T12:00:00Z" || len(cache.PRs) != 1 {
		t.Fatalf("cache = %+v", cache.PRs)
	}
}
