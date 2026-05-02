package collectors

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

func (c LocalGitCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	projectID := strings.TrimSpace(directive.Target["project_id"])
	repoID := strings.TrimSpace(directive.Target["repo_id"])
	if opts == nil || opts.HostID == "" {
		return nil, fmt.Errorf("local-git requires host context (SLAKKR_HOST or hostname)")
	}
	dirs, err := resolveDirectiveRepoDirs(opts.Projects, opts.HostID, projectID, repoID, expand)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		if projectID != "" || repoID != "" {
			return nil, fmt.Errorf("no existing working tree on this host for project_id=%s repo_id=%s", projectID, repoID)
		}
		return nil, fmt.Errorf("no existing working trees on this host (define paths_by_host in userdata/projects.yaml)")
	}

	repoStatus, branchWip := parseLocalGitChecks(directive.Config)
	lookbackDays := 14
	if v := strings.TrimSpace(directive.Config["branch_lookback_days"]); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lookbackDays = n
		}
	}
	lookback := time.Duration(lookbackDays) * 24 * time.Hour
	now := c.Clock()

	var out []StatusItem
	for _, abs := range dirs {
		if repoStatus {
			item, err := c.collectOne(ctx, directive, abs)
			if err != nil {
				return nil, err
			}
			if len(dirs) > 1 {
				item.Title = fmt.Sprintf("%s (%s)", directive.Name, filepath.Base(abs))
			}
			out = append(out, item)
		}
		if branchWip {
			extra, err := c.collectBranchWIP(ctx, directive, abs, lookback, now, len(dirs) > 1)
			if err != nil {
				return nil, err
			}
			out = append(out, extra...)
		}
	}
	if len(out) == 0 {
		return []StatusItem{{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      "local-git",
			Kind:        "git_report",
			Title:       directive.Name,
			Summary:     "No local-git signals for this run (e.g. no WIP branches in lookback or no main/master to compare).",
			Severity:    "info",
			ObservedAt:  c.Clock(),
		}}, nil
	}
	return out, nil
}

// CollectActivity emits one StatusItem per commit and reflog row since `since` across resolved repo dirs (read-only).
func (c LocalGitCollector) CollectActivity(ctx context.Context, directive userdata.Directive, since time.Time, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	projectID := strings.TrimSpace(directive.Target["project_id"])
	repoID := strings.TrimSpace(directive.Target["repo_id"])
	if opts == nil || opts.HostID == "" {
		return nil, fmt.Errorf("local-git requires host context (SLAKKR_HOST or hostname)")
	}
	dirs, err := resolveDirectiveRepoDirs(opts.Projects, opts.HostID, projectID, repoID, expand)
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		if projectID != "" || repoID != "" {
			return nil, fmt.Errorf("no existing working tree on this host for project_id=%s repo_id=%s", projectID, repoID)
		}
		return nil, fmt.Errorf("no existing working trees on this host (define paths_by_host in userdata/projects.yaml)")
	}
	now := c.Clock()
	sinceISO := since.UTC().Format(time.RFC3339)
	var out []StatusItem
	commitTimes := map[string]time.Time{}
	for _, abs := range dirs {
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
				ProjectID:   directive.ProjectID,
				Source:      "local-git",
				Kind:        "commit",
				Title:       title,
				Summary:     fmt.Sprintf("%s %s — %s", short, author, iso),
				Severity:    "info",
				ObservedAt:  obs.UTC(),
				Fields: map[string]string{
					"path":        abs,
					"hash":        hash,
					"short_hash":  short,
					"author":      author,
					"iso":         iso,
					"subject":     subject,
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
				ProjectID:   directive.ProjectID,
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

func resolveDirectiveRepoDirs(projects userdata.ProjectsFile, hostID, projectID, repoID string, expand func(string) string) ([]string, error) {
	projectID = strings.TrimSpace(projectID)
	repoID = strings.TrimSpace(repoID)
	switch {
	case projectID == "" && repoID == "":
		return existingHostRepoDirs(projects, hostID, expand), nil
	case projectID == "" || repoID == "":
		return nil, fmt.Errorf("local-git target must include both project_id and repo_id when target is set")
	default:
		candidates, err := userdata.ExpandedRepoWorktrees(projects, projectID, repoID, hostID, expand)
		if err != nil {
			return nil, err
		}
		var dirs []string
		for _, dir := range candidates {
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				dirs = append(dirs, dir)
			}
		}
		return dirs, nil
	}
}

func existingHostRepoDirs(projects userdata.ProjectsFile, hostID string, expand func(string) string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range projects.Projects {
		for _, r := range p.Repos {
			for _, raw := range userdata.RepoWorktreePaths(r, hostID) {
				dir := expand(raw)
				if dir == "" || seen[dir] {
					continue
				}
				st, err := os.Stat(dir)
				if err != nil || !st.IsDir() {
					continue
				}
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	return out
}

// parseLocalGitChecks returns (repository_status, branch_wip). Empty checks enables both.
func parseLocalGitChecks(config map[string]string) (bool, bool) {
	raw := strings.TrimSpace(config["checks"])
	if raw == "" {
		return true, true
	}
	var repo, branch bool
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		switch p {
		case "repository_status", "repo", "status":
			repo = true
		case "branch_wip", "branches", "wip":
			branch = true
		}
	}
	return repo, branch
}

func defaultExpandRepoPath(opts *CollectOpts) func(string) string {
	if opts != nil && opts.ExpandRepoPath != nil {
		return opts.ExpandRepoPath
	}
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

func (c LocalGitCollector) collectOne(ctx context.Context, directive userdata.Directive, abs string) (StatusItem, error) {
	branch, _ := gitOutput(ctx, abs, "branch", "--show-current")
	status, err := gitOutput(ctx, abs, "status", "--porcelain=v1")
	if err != nil {
		return StatusItem{}, err
	}
	recent, _ := gitOutput(ctx, abs, "log", "-1", "--pretty=format:%h %s")
	remote, _ := gitOutput(ctx, abs, "remote", "get-url", "origin")
	upstream, upstreamErr := gitOutput(ctx, abs, "rev-parse", "--abbrev-ref", "@{u}")
	upstream = strings.TrimSpace(upstream)
	ahead, behind := "", ""
	if upstreamErr == nil && upstream != "" && !strings.HasPrefix(strings.ToLower(upstream), "fatal") {
		counts, err := gitOutput(ctx, abs, "rev-list", "--left-right", "--count", "@{u}...HEAD")
		if err == nil {
			parts := strings.Fields(strings.TrimSpace(counts))
			if len(parts) == 2 {
				behind = parts[0]
				ahead = parts[1]
			}
		}
	}
	stashN := "0"
	if sl, err := gitOutput(ctx, abs, "stash", "list"); err == nil && strings.TrimSpace(sl) != "" {
		stashN = strconv.Itoa(len(strings.Split(strings.TrimSpace(sl), "\n")))
	}
	summary := "Working tree clean"
	severity := "info"
	if strings.TrimSpace(status) != "" {
		lines := strings.Split(strings.TrimSpace(status), "\n")
		summary = fmt.Sprintf("Working tree has %d changed file(s)", len(lines))
		severity = "warning"
	}
	if ahead != "" && behind != "" && (ahead != "0" || behind != "0") {
		summary += fmt.Sprintf("; ahead %s, behind %s vs upstream", ahead, behind)
		if severity == "info" {
			severity = "warning"
		}
	}
	if stashN != "0" {
		summary += fmt.Sprintf("; %s stash(es)", stashN)
		if severity == "info" {
			severity = "warning"
		}
	}
	fields := map[string]string{
		"path":          abs,
		"branch":        strings.TrimSpace(branch),
		"latest_commit": strings.TrimSpace(recent),
		"remote":        strings.TrimSpace(remote),
		"upstream":      upstream,
		"ahead":         ahead,
		"behind":        behind,
		"stash_count":   stashN,
	}
	return StatusItem{
		DirectiveID: directive.ID,
		ProjectID:   directive.ProjectID,
		Source:      "local-git",
		Kind:        "repository_status",
		Title:       directive.Name,
		Summary:     summary,
		Severity:    severity,
		ObservedAt:  c.Clock(),
		Fields:      fields,
	}, nil
}

// collectBranchWIP lists local feature branches with recent tip commits and commits ahead of main/master. Read-only.
func (c LocalGitCollector) collectBranchWIP(ctx context.Context, d userdata.Directive, abs string, lookback time.Duration, now time.Time, multiPath bool) ([]StatusItem, error) {
	base, err := resolveBaseBranchName(ctx, abs)
	if err != nil || base == "" {
		return nil, nil
	}
	lines, err := gitOutput(ctx, abs, "for-each-ref", "refs/heads/", "--format=%(refname:short)\t%(committerdate:iso-strict)\t%(objectname:short)")
	if err != nil {
		return nil, err
	}
	var out []StatusItem
	prefix := d.Name
	if multiPath {
		prefix = fmt.Sprintf("%s (%s)", d.Name, filepath.Base(abs))
	}
	for _, line := range strings.Split(lines, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		bName := strings.TrimSpace(parts[0])
		iso := strings.TrimSpace(parts[1])
		obj := strings.TrimSpace(parts[2])
		if bName == "main" || bName == "master" {
			continue
		}
		tip, err := time.Parse(time.RFC3339, iso)
		if err != nil {
			// some git versions use slightly different format
			if tip, err = time.Parse("2006-01-02 15:04:05 -0700", strings.ReplaceAll(iso, "T", " ")); err != nil {
				continue
			}
		}
		if now.Sub(tip) > lookback {
			continue
		}
		// read-only: count commits on branch not reachable from base
		ahead, rerr := gitOutput(ctx, abs, "rev-list", "--count", base+".."+bName)
		if rerr != nil {
			continue
		}
		ahead = strings.TrimSpace(ahead)
		if ahead == "0" {
			continue
		}
		mb, _ := gitOutput(ctx, abs, "merge-base", base, bName)
		mb = strings.TrimSpace(mb)
		latest, _ := gitOutput(ctx, abs, "log", "-1", "--pretty=format:%h %s", bName)
		title := fmt.Sprintf("WIP branch %s (ahead of %s)", bName, base)
		if multiPath {
			title = fmt.Sprintf("%s — %s", prefix, title)
		} else {
			title = prefix + " — " + title
		}
		fields := map[string]string{
			"path":          abs,
			"branch":        bName,
			"base_branch":   base,
			"merge_base":    mb,
			"ahead":         ahead,
			"latest_commit": strings.TrimSpace(latest),
			"tip_object":    obj,
			"committer_utc": tip.UTC().Format(time.RFC3339),
		}
		summary := fmt.Sprintf("tip=%s, ahead of %s by %s commit(s), merge_base=%s", strings.TrimSpace(latest), base, ahead, mb)
		out = append(out, StatusItem{
			DirectiveID: d.ID,
			ProjectID:   d.ProjectID,
			Source:      "local-git",
			Kind:        "branch_wip",
			Title:       title,
			Summary:     summary,
			Severity:    "info",
			ObservedAt:  c.Clock(),
			Fields:      fields,
		})
	}
	return out, nil
}

func resolveBaseBranchName(ctx context.Context, abs string) (string, error) {
	for _, name := range []string{"main", "master"} {
		_, err := gitOutput(ctx, abs, "rev-parse", "--verify", name)
		if err == nil {
			return name, nil
		}
	}
	return "", nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
