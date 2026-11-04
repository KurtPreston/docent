package collectors

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestWatchCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "github-watch", "k.json")
	c := newWatchCache("is:pr is:open repo:my-org/my-repo", []string{"services/api"})
	c.ScannedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c.Cursor = "2025-12-01T00:00:00Z"
	c.Truncated = true
	c.PRs["u"] = watchCacheEntry{
		Row:        ghSearchActivityRow{URL: "u", Title: "t", UpdatedAt: "2026-01-02T00:00:00Z"},
		HeadRefOid: "abc", BaseRefName: "main", Touches: true,
	}
	if err := c.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadWatchCache(path)
	if err != nil || got == nil {
		t.Fatalf("load: %v, %v", got, err)
	}
	if !reflect.DeepEqual(got, c) {
		t.Fatalf("round trip changed the cache:\n got %+v\nwant %+v", got, c)
	}

	// The temporary file used for the atomic write is gone
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("want only the cache file, got %v", entries)
	}
}

func TestWatchCacheLoadIgnoresMissingAndOutdatedFiles(t *testing.T) {
	dir := t.TempDir()

	// A missing file is a cold start, not an error
	if c, err := loadWatchCache(filepath.Join(dir, "missing.json")); c != nil || err != nil {
		t.Fatalf("missing file: got %v, %v", c, err)
	}

	// Another version is ignored
	old := filepath.Join(dir, "old.json")
	if err := os.WriteFile(old, []byte(`{"version":999,"prs":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := loadWatchCache(old); c != nil || err != nil {
		t.Fatalf("other version: got %v, %v", c, err)
	}

	// A corrupt file is reported so the caller can log it, and not used
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"version":1,"prs":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := loadWatchCache(bad); c != nil || err == nil {
		t.Fatalf("corrupt file: got %v, %v", c, err)
	}
}

func TestWatchCacheKeySeparatesViewers(t *testing.T) {
	me := watchViewer{"github.com", "me", "gh"}
	base := watchCacheKey(me, "q", []string{"a", "b"})
	if got := watchCacheKey(watchViewer{"github.com", "ME", "gh"}, "q", []string{"b", "a"}); got != base {
		t.Fatalf("login case and path order should not matter: %s != %s", got, base)
	}
	for name, other := range map[string]string{
		"host":      watchCacheKey(watchViewer{"ghe.example.com", "me", "gh"}, "q", []string{"a", "b"}),
		"user":      watchCacheKey(watchViewer{"github.com", "you", "gh"}, "q", []string{"a", "b"}),
		"directive": watchCacheKey(watchViewer{"github.com", "me", "gh-work"}, "q", []string{"a", "b"}),
		"query":     watchCacheKey(me, "q2", []string{"a", "b"}),
		"paths":     watchCacheKey(me, "q", []string{"a"}),
		// Joining fields must not let one field's text pass for another's
		"boundary": watchCacheKey(watchViewer{"github.com", "me", "ghq"}, "", []string{"a", "b"}),
	} {
		if other == base {
			t.Errorf("a different %s shares the key", name)
		}
	}
}

func TestCollectStateSavesWatchScan(t *testing.T) {
	small := fixturePR{Number: 1, Author: "teammate", Paths: []string{"services/api/main.go"}, Head: "h1", Base: "release"}
	elsewhere := fixturePR{Number: 2, Author: "teammate", Paths: []string{"docs/readme.md"}}
	large := fixturePR{Number: 3, Author: "teammate", Paths: []string{"docs/a.md"}, TotalFiles: 400}
	gh := newFakeGH(t)
	gh.on(ghRule{Pattern: "is:pr is:open repo:my-org/my-repo", Out: withIssueCount(watchPageJSON(t, small, elsewhere, large), 1500)})
	gh.on(ghRule{Pattern: "pulls/3/files", Exit: 1})
	gh.on(ghRule{Pattern: "--state", Out: "[]"})
	gh.on(ghRule{Pattern: "--review-requested my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "--assignee my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "-F number=", Out: prDetailJSON("teammate")})

	// Collect with a cache directory and a fixed clock
	scannedAt := time.Date(2026, 1, 3, 1, 2, 3, 0, time.UTC)
	opts := stateOpts()
	opts.CacheDir = t.TempDir()
	items, err := GitHubCollector{Clock: func() time.Time { return scannedAt }}.CollectState(context.Background(), watchDirective(), opts)
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}
	if prs := withoutErrors(items); len(prs) != 1 || prs[0].URL != small.url() {
		t.Fatalf("want only the touching PR, got %+v", prs)
	}

	// The cache file holds every scanned PR with its verdict
	q := "is:pr is:open repo:my-org/my-repo draft:false sort:updated-desc"
	c, err := loadWatchCache(watchCachePath(opts.CacheDir, watchCacheKey(watchViewer{"github.com", "my-user", "gh"}, q, []string{"services/api"})))
	if err != nil || c == nil {
		t.Fatalf("load cache: %v, %v", c, err)
	}
	if !c.ScannedAt.Equal(scannedAt) || !c.Truncated || c.Covered {
		t.Fatalf("scan metadata: %+v", c)
	}
	want := map[string]watchCacheEntry{
		small.url():     {HeadRefOid: "h1", BaseRefName: "release", Touches: true},
		elsewhere.url(): {HeadRefOid: "sha-2", BaseRefName: "main"},
		large.url():     {HeadRefOid: "sha-3", BaseRefName: "main", Unknown: true},
	}
	if len(c.PRs) != len(want) {
		t.Fatalf("cached %d PRs, want %d: %+v", len(c.PRs), len(want), c.PRs)
	}
	for url, w := range want {
		got := c.PRs[url]
		if got.Row.URL != url || got.HeadRefOid != w.HeadRefOid || got.BaseRefName != w.BaseRefName || got.Touches != w.Touches || got.Unknown != w.Unknown {
			t.Errorf("%s: got %+v, want %+v", url, got, w)
		}
	}
}

func TestWatchCachePathIsOffWithoutCacheDir(t *testing.T) {
	for _, opts := range []*CollectOpts{nil, {}, {CacheDir: "  "}} {
		if p := (ghRun{opts: opts}).watchCachePath("q", nil); p != "" {
			t.Errorf("opts %+v: want no cache path, got %q", opts, p)
		}
	}
}

// withoutErrors drops collector_error rows.
func withoutErrors(items []StatusItem) []StatusItem {
	var out []StatusItem
	for _, it := range items {
		if it.Kind != "collector_error" {
			out = append(out, it)
		}
	}
	return out
}
