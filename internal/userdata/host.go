package userdata

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	// EnvHostID is the stable machine key used for host-scoped paths and code_home in
	// shared userdata. When unset, a sanitized os.Hostname() is used.
	EnvHostID = "SLAKKR_HOST"
	// EnvCodeHome overrides the configured code_home for the current host (see EffectiveCodeHome).
	EnvCodeHome = "CODE_HOME"
)

var (
	hostKeyPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	nonHostKeyRunes  = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)
	leadingHostTrim  = regexp.MustCompile(`^[._-]+`)
	trailingHostTrim = regexp.MustCompile(`[._-]+$`)
)

// CurrentHostID returns SLAKKR_HOST if set and valid, otherwise a sanitized hostname.
func CurrentHostID() (string, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvHostID)); raw != "" {
		return SanitizeHostKey(raw)
	}
	h, err := os.Hostname()
	if err != nil {
		return "", err
	}
	return SanitizeHostKey(h)
}

// SanitizeHostKey normalizes a user- or OS-provided host label to match hostKeyPattern.
func SanitizeHostKey(raw string) (string, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = nonHostKeyRunes.ReplaceAllString(s, "-")
	s = leadingHostTrim.ReplaceAllString(s, "")
	s = trailingHostTrim.ReplaceAllString(s, "")
	if s == "" {
		return "", fmt.Errorf("host id is empty after sanitizing %q", raw)
	}
	if hostKeyPattern.MatchString(s) {
		return s, nil
	}
	for len(s) > 0 && !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= '0' && s[0] <= '9')) {
		s = s[1:]
	}
	if !hostKeyPattern.MatchString(s) {
		return "", fmt.Errorf("host id %q could not be sanitized to a valid key", raw)
	}
	return s, nil
}

// ValidHostKey reports whether id is an acceptable host map key in config.yaml / paths_by_host.
func ValidHostKey(id string) bool {
	return hostKeyPattern.MatchString(strings.TrimSpace(id))
}

// EffectiveCodeHome returns CODE_HOME from the environment when set, otherwise the
// code_home value for this host in config (may be empty).
func EffectiveCodeHome(cfg ConfigFile, hostID string) string {
	if v := strings.TrimSpace(os.Getenv(EnvCodeHome)); v != "" {
		return v
	}
	if cfg.Hosts == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Hosts[hostID].CodeHome)
}

// RepoWorktreePaths returns paths for this repo on the given host (paths_by_host[hostID]).
func RepoWorktreePaths(r Repo, hostID string) []string {
	if r.PathsByHost == nil {
		return nil
	}
	p, ok := r.PathsByHost[hostID]
	if !ok {
		return nil
	}
	out := make([]string, 0, len(p))
	for _, s := range p {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ExpandedRepoWorktrees resolves projectID/repoID to expanded filesystem paths for hostID
// (no existence check; callers typically skip paths that are not directories).
func ExpandedRepoWorktrees(projects ProjectsFile, projectID, repoID, hostID string, expand func(string) string) ([]string, error) {
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(repoID) == "" {
		return nil, fmt.Errorf("project_id and repo_id are required")
	}
	for _, p := range projects.Projects {
		if p.ID != projectID {
			continue
		}
		for _, r := range p.Repos {
			if r.ID != repoID {
				continue
			}
			var out []string
			for _, raw := range RepoWorktreePaths(r, hostID) {
				dir := expand(raw)
				if dir != "" {
					out = append(out, dir)
				}
			}
			return out, nil
		}
		return nil, fmt.Errorf("unknown repo_id %q for project %q", repoID, projectID)
	}
	return nil, fmt.Errorf("unknown project_id %q", projectID)
}
