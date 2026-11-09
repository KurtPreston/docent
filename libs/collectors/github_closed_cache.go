package collectors

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Every state collect lists the user's recently-closed PRs and looks up each
// one's head branch and final state, which search can't return. Neither can
// change while the PR stays closed, so the collector keeps each answer and
// looks a PR up again only when it closes anew (its closedAt moves). Without
// this, a busy week costs one request per closed PR on every poll.

// closedCacheVersion changes whenever the file layout or the meaning of an
// entry changes. A file with any other version is ignored.
const closedCacheVersion = 1

// closedCache is one viewer's saved closed-PR lookups.
type closedCache struct {
	Version int `json:"version"`
	// PRs holds each recently-closed PR's lookup, keyed by URL.
	PRs map[string]closedCacheEntry `json:"prs"`
}

// closedCacheEntry is one closed PR's lookup, valid while ClosedAt matches.
type closedCacheEntry struct {
	ClosedAt   string `json:"closed_at"`
	HeadBranch string `json:"head_branch"`
	State      string `json:"state"`
}

// closedCachePath is where a viewer's closed-PR cache lives under the run's
// cache directory, or "" when there is none and caching is off.
func closedCachePath(opts *CollectOpts, v watchViewer) string {
	if opts == nil || strings.TrimSpace(opts.CacheDir) == "" {
		return ""
	}
	return filepath.Join(opts.CacheDir, "github-closed", watchCacheKey(v, "closed", nil)+".json")
}

// newClosedCache starts an empty cache.
func newClosedCache() *closedCache {
	return &closedCache{Version: closedCacheVersion, PRs: map[string]closedCacheEntry{}}
}

// save writes the cache atomically.
func (c *closedCache) save(path string) error {
	c.Version = closedCacheVersion
	return writeJSONAtomic(path, c)
}

// loadClosedCache reads a cache file. A missing or outdated file yields an
// empty cache; only a failure other than the file not existing is returned,
// alongside an empty cache, so the caller can log it and carry on.
func loadClosedCache(path string) (*closedCache, error) {
	empty := newClosedCache()
	if path == "" {
		return empty, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}

	// Ignore a file from another version or one that doesn't parse
	var c closedCache
	if err := json.Unmarshal(b, &c); err != nil {
		return empty, err
	}
	if c.Version != closedCacheVersion || c.PRs == nil {
		return empty, nil
	}
	return &c, nil
}

// lookup returns the saved lookup for a closed search row, if it was made
// since the PR last closed.
func (c *closedCache) lookup(row ghSearchActivityRow) (closedCacheEntry, bool) {
	closedAt := strings.TrimSpace(row.ClosedAt)
	e, ok := c.PRs[row.URL]
	if !ok || closedAt == "" || e.ClosedAt != closedAt {
		return closedCacheEntry{}, false
	}
	return e, true
}

// isTerminalPRState reports whether a lookup's state is final enough to keep:
// CLOSED or MERGED, not OPEN (search lagging a reopen) or empty (a failure).
func isTerminalPRState(state string) bool {
	return strings.EqualFold(state, "CLOSED") || strings.EqualFold(state, "MERGED")
}
