package model

import (
	"net/url"
	"strings"
)

// RepoKeyFromRemote reduces a git remote URL to the host-relative repository
// identity, e.g. "Chip/salsa". Returns "" when the URL does not look like a
// forge URL.
//
// This is the key everything in docent joins on: the GitHub and Gitea
// collectors report it directly, local-git derives it from origin, and grove
// project discovery matches on it. All three must agree on the shape or the same
// repository appears as two, so the parsing lives here rather than in each.
//
// Both remote spellings are accepted, since one machine routinely has both: SCP
// style (git@host:owner/repo.git) and URL style (https://host/owner/repo.git,
// ssh://git@host/owner/repo).
func RepoKeyFromRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// SCP-like remotes are not URLs -- url.Parse reads "git@host" as a scheme --
	// so they are split by hand. The "://" test is what distinguishes them.
	if !strings.Contains(raw, "://") {
		if path, ok := splitSCPLikeRemote(raw); ok {
			return normalizeRepoPath(path)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return normalizeRepoPath(strings.TrimPrefix(u.Path, "/"))
}

func splitSCPLikeRemote(raw string) (path string, ok bool) {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return "", false
	}
	rest := raw[at+1:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	host, path := rest[:colon], rest[colon+1:]
	if host == "" || path == "" {
		return "", false
	}
	return path, true
}

func normalizeRepoPath(path string) string {
	path = strings.Trim(strings.TrimSuffix(strings.TrimSpace(path), ".git"), "/")
	// A single segment is a bare path, not an owner/repo identity; returning it
	// would collide with any other forge repo of the same name.
	if path == "" || !strings.Contains(path, "/") {
		return ""
	}
	return path
}
