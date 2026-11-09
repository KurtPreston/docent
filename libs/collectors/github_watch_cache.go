package collectors

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// A path-filtered watch query has to page through every open PR its
// qualifiers match, because search can't filter by path. Most of those PRs
// haven't changed since the last poll, so the collector keeps what it learned
// about each one: whether it touches the watched paths, and for which head
// and base. A later run re-searches only what changed and reuses the rest.
//
// Nothing about checks, reviews, or mergeability is kept. Those are fetched
// fresh for every watched PR on every run, because they change without the
// PR's head or base moving.

// watchCacheVersion changes whenever the file layout or the meaning of an
// entry changes. A file with any other version is ignored.
const watchCacheVersion = 1

// watchCache is one watch query's saved scan state.
type watchCache struct {
	Version int `json:"version"`
	// Query and Paths describe the watch query, for anyone reading the file.
	Query string   `json:"query"`
	Paths []string `json:"paths"`
	// ScannedAt is when the last successful scan started. The next scan
	// searches PRs updated since shortly before it.
	ScannedAt time.Time `json:"scanned_at"`
	// Cursor is the creation time the next reconciliation slice starts at,
	// working back from the newest PRs. Empty starts again at the newest.
	Cursor string `json:"cursor,omitempty"`
	// Truncated is set when the scan that seeded the cache hit GitHub's
	// 1000-result cap; Covered once reconciliation has since reached the
	// oldest matching PR, so every PR has been checked at least once.
	Truncated bool `json:"truncated,omitempty"`
	Covered   bool `json:"covered,omitempty"`
	// PRs holds every open PR the query matched, keyed by URL.
	PRs map[string]watchCacheEntry `json:"prs"`
}

// watchCacheEntry is what the cache knows about one open PR.
type watchCacheEntry struct {
	Row         ghSearchActivityRow `json:"row"`
	HeadRefOid  string              `json:"head_ref_oid"`
	BaseRefName string              `json:"base_ref_name"`
	// Touches says whether the PR changes a file under a watched path, as of
	// HeadRefOid against BaseRefName.
	Touches bool `json:"touches"`
	// Unknown is set when the PR's files couldn't be listed. Membership is
	// retried on every run until a listing succeeds.
	Unknown bool `json:"unknown,omitempty"`
}

// watchViewer is whose view of GitHub a watch cache holds. What search
// returns depends on who is asking, and a directive may carry its own token
// while its user is the default "@me", so the directive is part of it too.
type watchViewer struct {
	host, user, directive string
}

// watchCacheKey identifies a watch query's cache file.
func watchCacheKey(v watchViewer, query string, paths []string) string {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, part := range []string{v.host, strings.ToLower(v.user), v.directive, query, strings.Join(sorted, "\n")} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// watchCachePath is where a watch query's cache lives under dir.
func watchCachePath(dir, key string) string {
	return filepath.Join(dir, "github-watch", key+".json")
}

// loadWatchCache reads a cache file. A missing, unreadable, or outdated file
// yields nil, which means scanning from scratch; only a failure other than the
// file not existing is returned as an error, so the caller can log it.
func loadWatchCache(path string) (*watchCache, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c watchCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Version != watchCacheVersion || c.PRs == nil {
		return nil, nil
	}
	return &c, nil
}

// save writes the cache atomically: to a temporary file beside it, then
// renamed over it, so a reader never sees half a file. Concurrent writers
// each leave a complete scan behind; the last one wins.
func (c *watchCache) save(path string) error {
	c.Version = watchCacheVersion
	return writeJSONAtomic(path, c)
}

// writeJSONAtomic writes v as JSON to a temporary file beside path, then
// renames it over path, so a reader never sees half a file.
func writeJSONAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	// Write beside the target so the rename stays on one filesystem
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// newWatchCache starts an empty cache for a query.
func newWatchCache(query string, paths []string) *watchCache {
	return &watchCache{Version: watchCacheVersion, Query: query, Paths: paths, PRs: map[string]watchCacheEntry{}}
}

// touchingRows returns the rows of cached PRs known to touch the watched
// paths, most recently updated first, as a full scan would list them.
func (c *watchCache) touchingRows() []ghSearchActivityRow {
	var rows []ghSearchActivityRow
	for _, e := range c.PRs {
		if e.Touches && !e.Unknown {
			rows = append(rows, e.Row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		ti, tj := parseGitHubTime(rows[i].UpdatedAt), parseGitHubTime(rows[j].UpdatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return rows[i].URL < rows[j].URL
	})
	return rows
}

// cacheEntry records a scanned node and its membership verdict.
func (n watchSearchNode) cacheEntry(v touchVerdict) watchCacheEntry {
	return watchCacheEntry{
		Row:         n.searchRow(),
		HeadRefOid:  n.HeadRefOid,
		BaseRefName: n.BaseRefName,
		Touches:     v.touches,
		Unknown:     v.err != nil,
	}
}

// watchCachePath returns where this run keeps a watch query's cache, or ""
// when caching is off.
func (r ghRun) watchCachePath(query string, paths []string) string {
	if r.opts == nil || strings.TrimSpace(r.opts.CacheDir) == "" {
		return ""
	}
	return watchCachePath(r.opts.CacheDir, watchCacheKey(watchViewer{r.host, r.user, r.directive.ID}, query, paths))
}

// saveWatchCache writes a watch query's cache. A failure only costs the next
// run its head start, so it is logged, not returned.
func (r ghRun) saveWatchCache(path string, c *watchCache) {
	if err := c.save(path); err != nil {
		loggerFor(r.opts, r.directive.ID).Note("github: could not save watch cache %s: %v", path, err)
	}
}

// clock reads the run's wall clock.
func (r ghRun) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// clockNow reads the collector's clock, falling back to the system's.
func (c GitHubCollector) clockNow() time.Time {
	if c.Clock != nil {
		return c.Clock()
	}
	return time.Now()
}
