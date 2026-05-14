package collectors

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

// Collect emits commits and reflog rows since opts.Since across resolved repo directories.
func (c LocalGitCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	dirs, err := localGitRepoDirs(directive, opts, expand)
	if err != nil {
		return nil, err
	}
	sinceISO := since.UTC().Format(time.RFC3339)
	var out []StatusItem
	commitTimes := map[string]time.Time{}
	for _, abs := range dirs {
		repoLabel := localGitRepositoryKey(ctx, abs)
		logOut, err := gitOutput(ctx, abs, "log", "--all", "--no-merges", "--since="+sinceISO, "--pretty=format:%H%x09%aI%x09%an%x09%s")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(logOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 4 {
				continue
			}
			hash := strings.TrimSpace(parts[0])
			iso := strings.TrimSpace(parts[1])
			author := strings.TrimSpace(parts[2])
			subject := strings.TrimSpace(parts[3])
			obs, err := time.Parse(time.RFC3339, iso)
			if err != nil {
				if obs, err = time.Parse("2006-01-02 15:04:05 -0700", strings.ReplaceAll(iso, "T", " ")); err != nil {
					continue
				}
			}
			if obs.Before(since) || obs.After(now) {
				continue
			}
			short := hash
			if len(hash) > 7 {
				short = hash[:7]
			}
			title := subject
			if len(dirs) > 1 {
				title = fmt.Sprintf("(%s) %s", filepath.Base(abs), subject)
			}
			out = append(out, StatusItem{
				DirectiveID: directive.ID,
				Repository:  repoLabel,
				Source:      "local-git",
				Kind:        "commit",
				Title:       title,
				Summary:     fmt.Sprintf("%s %s — %s", short, author, iso),
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"path":       abs,
					"hash":       hash,
					"short_hash": short,
					"author":     author,
					"iso":        iso,
					"subject":    subject,
				},
			})
		}
		refOut, err := gitOutput(ctx, abs, "reflog", "--since="+sinceISO, "--date=iso", "--pretty=format:%H%x09%gd%x09%gs")
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(refOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 3 {
				continue
			}
			hash := strings.TrimSpace(parts[0])
			gd := strings.TrimSpace(parts[1])
			gs := strings.TrimSpace(parts[2])
			obs, ok := commitTimes[hash]
			if !ok {
				ci, err := gitOutput(ctx, abs, "show", "-s", "--format=%cI", hash)
				if err != nil {
					continue
				}
				ci = strings.TrimSpace(ci)
				var perr error
				obs, perr = time.Parse(time.RFC3339, ci)
				if perr != nil {
					continue
				}
				commitTimes[hash] = obs
			}
			if obs.Before(since) || obs.After(now) {
				continue
			}
			short := hash
			if len(hash) > 7 {
				short = hash[:7]
			}
			title := gd + " " + gs
			if len(dirs) > 1 {
				title = fmt.Sprintf("(%s) %s %s", filepath.Base(abs), gd, gs)
			}
			out = append(out, StatusItem{
				DirectiveID: directive.ID,
				Repository:  repoLabel,
				Source:      "local-git",
				Kind:        "reflog",
				Title:       title,
				Summary:     short,
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"path":       abs,
					"hash":       hash,
					"short_hash": short,
					"gd":         gd,
					"gs":         gs,
				},
			})
		}
	}
	return out, nil
}

// localGitRepositoryKey prefers remote.origin URL (owner/repo or nested path) so local-git
// aligns with GitHub / Gitea `repository`; falls back to the working tree directory name.
func localGitRepositoryKey(ctx context.Context, abs string) string {
	fallback := filepath.Base(abs)
	out, err := gitOutput(ctx, abs, "remote", "get-url", "origin")
	if err != nil {
		return fallback
	}
	if key := parseGitRemoteToRepositoryKey(strings.TrimSpace(out)); key != "" {
		return key
	}
	return fallback
}

// parseGitRemoteToRepositoryKey returns the path portion of a remote URL as host-relative
// repo identity (e.g. "org/repo"), or "" if the URL does not look like a standard forge URL.
func parseGitRemoteToRepositoryKey(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if path, ok := splitSCPLikeGitRemote(raw); ok {
			return normalizeRepositoryPath(path)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	path := strings.TrimPrefix(u.Path, "/")
	return normalizeRepositoryPath(path)
}

func splitSCPLikeGitRemote(raw string) (path string, ok bool) {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return "", false
	}
	rest := raw[at+1:]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	host := rest[:colon]
	path = rest[colon+1:]
	if host == "" || path == "" {
		return "", false
	}
	return path, true
}

func normalizeRepositoryPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return ""
	}
	if strings.Count(path, "/") < 1 {
		return ""
	}
	return path
}

func localGitRepoDirs(directive userdata.Directive, opts *CollectOpts, expand func(string) string) ([]string, error) {
	var dirs []string
	seen := map[string]bool{}
	for _, p := range directive.Paths {
		p = expand(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
				continue
			}
			if !seen[p] {
				seen[p] = true
				dirs = append(dirs, p)
			}
		}
	}
	if len(dirs) > 0 {
		return dirs, nil
	}
	codeHome := expand(strings.TrimSpace(directive.CodeHome))
	if codeHome == "" {
		return nil, fmt.Errorf("local-git: set code_home or paths on the directive")
	}
	entries, err := os.ReadDir(codeHome)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cand := filepath.Join(codeHome, e.Name())
		if _, err := os.Stat(filepath.Join(cand, ".git")); err != nil {
			continue
		}
		abs := expand(cand)
		if !seen[abs] {
			seen[abs] = true
			dirs = append(dirs, abs)
		}
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("local-git: no git repositories under %s", codeHome)
	}
	return dirs, nil
}

func defaultExpandRepoPath(opts *CollectOpts) func(string) string {
	if opts != nil && opts.ExpandRepoPath != nil {
		return opts.ExpandRepoPath
	}
	return fallbackExpandRepoPath()
}

func fallbackExpandRepoPath() func(string) string {
	return func(s string) string {
		s = strings.TrimSpace(s)
		if s == "" {
			return ""
		}
		abs, err := filepath.Abs(s)
		if err != nil {
			return filepath.Clean(s)
		}
		return abs
	}
}

// ValidateDirective checks the `git` binary is present, that at least one
// repository (via `paths` or `code_home`) resolves on disk, and that `git`
// itself accepts each repository. The git probe catches the failure modes that
// would otherwise surface as the opaque "exit status 128" during Collect:
// safe.directory ownership errors, permission denials, and corrupt repos.
func (c LocalGitCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	if _, err := exec.LookPath("git"); err != nil {
		return []ValidationIssue{{
			Field:       "git",
			Message:     "`git` binary not found on PATH",
			Remediation: "install git (e.g. `apt install git`, `brew install git`)",
		}}
	}
	expand := fallbackExpandRepoPath()
	if opts != nil && opts.ExpandRepoPath != nil {
		expand = opts.ExpandRepoPath
	}
	var (
		issues   []ValidationIssue
		resolved []string
		seen     = map[string]bool{}
	)
	for _, raw := range directive.Paths {
		p := expand(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			issues = append(issues, ValidationIssue{
				Field:       "paths",
				Message:     fmt.Sprintf("path %s does not exist or is not a directory", p),
				Remediation: "remove the entry or correct the path",
			})
			continue
		}
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			issues = append(issues, ValidationIssue{
				Field:       "paths",
				Message:     fmt.Sprintf("%s is not a git working tree (missing .git)", p),
				Remediation: "point to a directory containing .git, or drop this entry",
			})
			continue
		}
		if !seen[p] {
			seen[p] = true
			resolved = append(resolved, p)
		}
	}
	if len(resolved) == 0 {
		codeHome := expand(strings.TrimSpace(directive.CodeHome))
		if codeHome == "" {
			return append(issues, ValidationIssue{
				Field:       "code_home",
				Message:     "neither `paths` nor `code_home` is set",
				Remediation: "set `code_home` to a parent of your repo clones, or list `paths` explicitly",
			})
		}
		st, err := os.Stat(codeHome)
		if err != nil || !st.IsDir() {
			return append(issues, ValidationIssue{
				Field:       "code_home",
				Message:     fmt.Sprintf("code_home %s does not exist or is not a directory", codeHome),
				Remediation: "create the directory or update code_home to a real path",
			})
		}
		entries, err := os.ReadDir(codeHome)
		if err != nil {
			return append(issues, ValidationIssue{
				Field:       "code_home",
				Message:     fmt.Sprintf("cannot read code_home %s: %v", codeHome, err),
				Remediation: "ensure the directory is readable",
			})
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			cand := filepath.Join(codeHome, e.Name())
			if _, err := os.Stat(filepath.Join(cand, ".git")); err != nil {
				continue
			}
			abs := expand(cand)
			if !seen[abs] {
				seen[abs] = true
				resolved = append(resolved, abs)
			}
		}
		if len(resolved) == 0 {
			return append(issues, ValidationIssue{
				Field:       "code_home",
				Message:     fmt.Sprintf("no git repositories found under %s", codeHome),
				Remediation: "clone repos into code_home or point it at a directory of repos",
			})
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, dir := range resolved {
		// Mirror the command shape Collect uses so failures here line up with
		// what would have surfaced as `exit status 128` during collection
		// (safe.directory ownership errors, empty repos with no commits,
		// corrupt refs, permission denials).
		cmd := exec.CommandContext(probeCtx, "git", "-C", dir, "log", "--all", "--max-count=1", "--format=%H")
		out, err := cmd.CombinedOutput()
		if err == nil {
			continue
		}
		stderr := strings.TrimSpace(string(out))
		rem := fmt.Sprintf("run `git -C %s log --all --max-count=1` to see the underlying error", dir)
		switch {
		case strings.Contains(stderr, "safe.directory"):
			rem = fmt.Sprintf("run `git config --global --add safe.directory %s` (or fix ownership of %s)", dir, dir)
		case strings.Contains(stderr, "not a git repository"):
			rem = fmt.Sprintf("remove %s from paths or delete its .git folder if no longer needed", dir)
		case strings.Contains(stderr, "does not have any commits yet"), strings.Contains(stderr, "bad default revision"):
			rem = fmt.Sprintf("repo %s has no commits yet; ignore it or make an initial commit", dir)
		case strings.Contains(stderr, "Permission denied"):
			rem = fmt.Sprintf("ensure the current user can read %s/.git", dir)
		}
		msg := fmt.Sprintf("git rejected %s", dir)
		if stderr != "" {
			msg = fmt.Sprintf("%s: %s", msg, stderr)
		}
		issues = append(issues, ValidationIssue{
			Field:       "git",
			Message:     msg,
			Remediation: rem,
		})
	}
	return issues
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// Surface git's stderr so callers (and the user) don't see an opaque
		// `exit status 128`; the stderr typically explains the actual cause
		// (safe.directory, missing commits, bad refs, etc).
		if exit, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exit.Stderr))
			if stderr != "" {
				return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), dir, err, stderr)
			}
		}
		return "", fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), dir, err)
	}
	return string(out), nil
}
