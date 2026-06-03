package collectors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// slackCache is a best-effort, per-workspace cache the Slack collector
// uses to reuse resolved user identities (users.info) across runs so
// author names are read from disk on repeat runs instead of re-fetched.
//
// It lives under <userdata>/.cache/slack/<teamID>/cache.json and is
// deliberately fail-open: any load/save/parse error degrades to "no
// cache" rather than failing collection. When userdataDir or teamID is
// empty (e.g. unit tests) the cache is disabled and every method is a
// no-op, so request counts match the no-cache behavior.
type slackCache struct {
	dir   string // "" => disabled (no on-disk persistence)
	mu    sync.Mutex
	data  slackCacheData
	dirty bool
}

// slackCacheData is the serialized portion of the cache.
type slackCacheData struct {
	Users map[string]slackCachedUser `json:"users"`
}

type slackCachedUser struct {
	User       slackUser `json:"user"`
	CachedUnix int64     `json:"cached_unix"`
}

// slackUserCacheTTL bounds how long a resolved users.info record is
// trusted. Display names change rarely, so a generous TTL still
// eliminates almost all repeat lookups while picking up renames within a
// month.
const slackUserCacheTTL = 30 * 24 * time.Hour

// loadSlackCache reads the on-disk cache for a workspace. It never
// returns nil; on any error it returns an empty (but functional) cache.
// An empty userdataDir or teamID disables persistence entirely.
func loadSlackCache(userdataDir, teamID string) *slackCache {
	c := &slackCache{data: slackCacheData{Users: map[string]slackCachedUser{}}}
	userdataDir = strings.TrimSpace(userdataDir)
	teamID = strings.TrimSpace(teamID)
	if userdataDir == "" || teamID == "" {
		return c
	}
	c.dir = filepath.Join(userdataDir, ".cache", "slack", sanitizeSlackTeamID(teamID))
	raw, err := os.ReadFile(filepath.Join(c.dir, "cache.json"))
	if err != nil {
		return c
	}
	var onDisk slackCacheData
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		return c
	}
	if onDisk.Users != nil {
		c.data.Users = onDisk.Users
	}
	return c
}

// cachedUser returns a non-expired cached user record, if present.
func (c *slackCache) cachedUser(id string, now time.Time) (slackUser, bool) {
	if c == nil || c.dir == "" || id == "" {
		return slackUser{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	rec, ok := c.data.Users[id]
	if !ok {
		return slackUser{}, false
	}
	if now.Sub(time.Unix(rec.CachedUnix, 0)) > slackUserCacheTTL {
		return slackUser{}, false
	}
	return rec.User, true
}

// putUser caches a resolved user identity. Records with an empty ID (or a
// failed lookup we don't want to persist) are ignored.
func (c *slackCache) putUser(u slackUser, now time.Time) {
	if c == nil || c.dir == "" || u.ID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data.Users[u.ID] = slackCachedUser{User: u, CachedUnix: now.Unix()}
	c.dirty = true
}

// save writes the cache to disk when persistence is enabled and something
// changed. Errors are returned so the caller can log them, but they are
// never fatal to collection.
func (c *slackCache) save() error {
	if c == nil || c.dir == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c.data, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(c.dir, "cache.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// sanitizeSlackTeamID keeps cache directory names to a safe charset so a
// surprising team identifier can't escape the cache root.
func sanitizeSlackTeamID(teamID string) string {
	var b strings.Builder
	for _, r := range teamID {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "default"
	}
	return out
}
