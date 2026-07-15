package collectors

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
)

type LocalGitCollector struct {
	Clock func() time.Time
}

// Collect emits commits and reflog rows since opts.Since across resolved repo directories.
//
// Scope semantics:
//   - ScopeSelf: only commits the per-repo matcher flags as the user's own
//     (matched by author email or USER name). Reflog rows always pass since
//     they record the user's local checkout actions.
//   - ScopeInvolved (default): commits reachable from local branches (which
//     the user has by definition created or checked out) UNION the matcher's
//     self commits. Covers detached-HEAD work that isn't on any local branch
//     yet.
//   - ScopeAll: every commit on every ref (`git log --all`), regardless of
//     author.
func (c LocalGitCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	expand := defaultExpandRepoPath(opts)
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	scope := opts.EffectiveScope()
	dirs, err := localGitRepoDirs(directive, opts, expand)
	if err != nil {
		return nil, err
	}
	sinceISO := since.UTC().Format(time.RFC3339)
	globalEmail := strings.ToLower(strings.TrimSpace(gitConfigValue(ctx, "", "--global", "user.email", opts, directive.ID)))
	currentUser := strings.ToLower(strings.TrimSpace(currentOSUsername()))
	var out []StatusItem
	commitTimes := map[string]time.Time{}
	// Tracks which shared git stores (keyed by common git dir) have already had
	// their commit history scanned, so the many worktrees of one repository
	// don't each re-emit the identical `git log --all` commit set. Reflogs are
	// still collected per worktree below (HEAD's reflog lives in each
	// worktree's own gitdir).
	scannedCommits := map[string]bool{}
	// One unit of progress per repo. This is by far the biggest
	// wall-clock contributor for users with sizable code_home
	// directories, so a steady "47/170" bar is much more useful than
	// the indefinite spinner we showed before.
	totalDirs := len(dirs)
	for i, abs := range dirs {
		reportProgress(opts, DirectiveProgress{
			DirectiveID: directive.ID,
			Description: directive.Name,
			Status:      "running",
			Detail:      fmt.Sprintf("scanning %s", filepath.Base(abs)),
			Completed:   i,
			Total:       totalDirs,
		})
		// A freshly-initialised repo (e.g. `git init` with no commits) makes
		// `git log --all` / `git reflog` exit 128 with "your current branch
		// '<name>' does not have any commits yet". Treat that as "nothing to
		// report" rather than failing the entire directive: one empty repo
		// under code_home shouldn't sabotage every other repo's collection.
		if !localGitRepoHasCommits(ctx, abs, opts, directive.ID) {
			continue
		}
		repoLabel := localGitRepositoryKey(ctx, abs, opts, directive.ID)
		matcher := newLocalGitSelfMatcher(ctx, abs, globalEmail, currentUser, opts, directive.ID)

		// A ticket derived from the checked-out branch (or the worktree
		// directory name for salsa-style worktrees) anchors commits and
		// reflog rows to the right work-item even when their own text
		// doesn't name the ticket. We deliberately do NOT emit a kind=branch
		// snapshot here: that is current state (every checkout under
		// code_home), not an in-window event, and would flood recent-activity
		// prompts with branch×1 noise for untouched repos.
		branch := localGitCurrentBranch(ctx, abs, opts, directive.ID)
		repoTicket := correlation.ScanTicketKey(branch, correlation.Config{})
		if repoTicket == "" {
			repoTicket = correlation.ScanTicketKey(filepath.Base(abs), correlation.Config{})
		}
		// When this repo has sibling worktrees sharing its refs (grove-style
		// layouts), a commit surfaced by `git log --all` may belong to a
		// branch checked out elsewhere. Map branch -> owning worktree so each
		// row is tagged with the directory that actually holds its branch,
		// not whichever worktree we happen to be scanning.
		worktrees := localGitWorktreeBranches(ctx, abs, opts, directive.ID)

		// Worktrees of one repository share a single object store and ref set,
		// so `git log --all` returns an identical commit set in every one of
		// them (grove-style layouts keep 15+ worktrees side by side). Scan the
		// history just once per shared store — keyed by the common git dir —
		// rather than re-emitting (and re-walking) the same commits per
		// worktree. Reflogs are handled per directory below, since HEAD's
		// reflog lives in each worktree's own gitdir.
		common := localGitCommonDir(ctx, abs, opts, directive.ID)
		if common == "" {
			common = abs
		}
		if !scannedCommits[common] {
			scannedCommits[common] = true

			commits, err := collectLocalGitCommits(ctx, abs, sinceISO, since, now, matcher, opts, directive.ID)
			if err != nil {
				return nil, err
			}

			// branchHashes is only populated for scope=involved (where we need
			// to know which commits sit on local branches). For self/all we
			// either don't care about it or just keep every commit anyway.
			var branchHashes map[string]struct{}
			if scope == ScopeInvolved {
				branchHashes, err = localGitBranchHashes(ctx, abs, sinceISO, opts, directive.ID)
				if err != nil {
					return nil, err
				}
			}

			for _, ci := range commits {
				keep := true
				switch scope {
				case ScopeSelf:
					keep = ci.isSelf
				case ScopeInvolved:
					if !ci.isSelf {
						if _, ok := branchHashes[ci.hash]; !ok {
							keep = false
						}
					}
				default: // ScopeAll
					keep = true
				}
				if !keep {
					continue
				}
				// Attribute the commit to the worktree that actually owns its
				// branch — for both the open path and the disambiguating title
				// prefix — instead of whichever worktree we scanned from.
				commitDir := abs
				if wt := worktrees[ci.branch]; ci.branch != "" && wt != "" {
					commitDir = wt
				}
				item := buildLocalGitCommitItem(directive.ID, repoLabel, commitDir, ci, dirs)
				if t := localGitTicket(ci.subject, repoTicket); t != "" {
					item.Fields["ticket"] = t
				}
				out = append(out, item)
				commitTimes[ci.hash] = ci.observed
			}
		}

		refOut, err := gitOutput(ctx, abs, opts, directive.ID, "reflog", "--since="+sinceISO, "--date=iso", "--pretty=format:%H%x09%gd%x09%gs")
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
		// A reflog row records an action the user took locally (checkout,
		// commit, reset, ...). Its activity time is when that action
		// happened — carried in the `%gd` selector because we ask for
		// --date=iso — not the referenced commit's author/committer date,
		// which can be far older (e.g. checking out a months-old branch).
		// Fall back to the commit date only when the selector lacks a
		// parseable timestamp.
		obs, ok := parseReflogTime(gd)
		if !ok {
			obs, ok = commitTimes[hash]
			if !ok {
				ci, err := gitOutput(ctx, abs, opts, directive.ID, "show", "-s", "--format=%cI", hash)
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
			fields := map[string]string{
				"path":       abs,
				"hash":       hash,
				"short_hash": short,
				"gd":         gd,
				"gs":         gs,
			}
			if b := localGitReflogBranch(gd, gs); b != "" {
				fields["branch"] = b
				if wt := worktrees[b]; wt != "" {
					fields["path"] = wt
				}
			}
			// Reflog subjects like "checkout: moving from main to salsa-123"
			// carry the branch (and thus ticket); fall back to the repo's
			// current-branch ticket otherwise.
			if t := localGitTicket(gs, repoTicket); t != "" {
				fields["ticket"] = t
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
				IsSelf:      true,
				Fields:      fields,
			})
		}
	}
	return out, nil
}

// localGitCommit is the parsed form of one `git log` row before it becomes a
// StatusItem. Splitting this out keeps Collect's scope branching readable.
type localGitCommit struct {
	hash     string
	ref      string
	branch   string
	iso      string
	author   string
	email    string
	subject  string
	observed time.Time
	isSelf   bool
}

func collectLocalGitCommits(ctx context.Context, repoDir, sinceISO string, since, now time.Time, matcher localGitSelfMatcher, opts *CollectOpts, directiveID string) ([]localGitCommit, error) {
	logOut, err := gitOutput(ctx, repoDir, opts, directiveID, "log", "--all", "--source", "--no-merges", "--since="+sinceISO, "--pretty=format:%H%x09%S%x09%aI%x09%an%x09%ae%x09%s")
	if err != nil {
		return nil, err
	}
	var out []localGitCommit
	for _, line := range strings.Split(logOut, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) < 6 {
			continue
		}
		hash := strings.TrimSpace(parts[0])
		ref := strings.TrimSpace(parts[1])
		iso := strings.TrimSpace(parts[2])
		author := strings.TrimSpace(parts[3])
		email := strings.TrimSpace(parts[4])
		subject := strings.TrimSpace(parts[5])
		obs, err := time.Parse(time.RFC3339, iso)
		if err != nil {
			if obs, err = time.Parse("2006-01-02 15:04:05 -0700", strings.ReplaceAll(iso, "T", " ")); err != nil {
				continue
			}
		}
		if obs.Before(since) || obs.After(now) {
			continue
		}
		out = append(out, localGitCommit{
			hash:     hash,
			ref:      ref,
			branch:   normalizeGitRef(ref),
			iso:      iso,
			author:   author,
			email:    email,
			subject:  subject,
			observed: obs,
			isSelf:   matcher.Match(author, email),
		})
	}
	return out, nil
}

// localGitBranchHashes returns the set of commit SHAs reachable from any
// local branch within the time window. Used for ScopeInvolved: a commit
// counts as "the user's involved work" when it sits on one of the branches
// they have created or checked out locally.
func localGitBranchHashes(ctx context.Context, repoDir, sinceISO string, opts *CollectOpts, directiveID string) (map[string]struct{}, error) {
	out, err := gitOutput(ctx, repoDir, opts, directiveID, "log", "--branches", "--no-merges", "--since="+sinceISO, "--pretty=format:%H")
	if err != nil {
		return nil, err
	}
	set := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		h := strings.TrimSpace(line)
		if h == "" {
			continue
		}
		set[h] = struct{}{}
	}
	return set, nil
}

func buildLocalGitCommitItem(directiveID, repoLabel, abs string, ci localGitCommit, allDirs []string) StatusItem {
	short := ci.hash
	if len(ci.hash) > 7 {
		short = ci.hash[:7]
	}
	title := ci.subject
	if len(allDirs) > 1 {
		title = fmt.Sprintf("(%s) %s", filepath.Base(abs), ci.subject)
	}
	authorIdentity := ci.author
	if ci.email != "" {
		if ci.author != "" {
			authorIdentity = fmt.Sprintf("%s <%s>", ci.author, ci.email)
		} else {
			authorIdentity = ci.email
		}
	}
	return StatusItem{
		DirectiveID: directiveID,
		Repository:  repoLabel,
		Source:      "local-git",
		Kind:        "commit",
		Title:       title,
		Summary:     fmt.Sprintf("%s %s — %s", short, ci.author, ci.iso),
		Severity:    "info",
		ObservedAt:  ci.observed.UTC(),
		Author:      authorIdentity,
		IsSelf:      ci.isSelf,
		Fields: func() map[string]string {
			fields := map[string]string{
				"path":         abs,
				"hash":         ci.hash,
				"short_hash":   short,
				"author":       ci.author,
				"author_email": ci.email,
				"iso":          ci.iso,
				"subject":      ci.subject,
			}
			if ci.branch != "" {
				fields["branch"] = ci.branch
			}
			return fields
		}(),
	}
}

// normalizeGitRef maps a git log --source ref to a local branch name, or ""
// when the ref is not a local branch (remote, tag, detached, etc.).
func normalizeGitRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	const heads = "refs/heads/"
	if strings.HasPrefix(ref, heads) {
		return strings.TrimPrefix(ref, heads)
	}
	return ""
}

// localGitReflogBranch derives a branch name from reflog gd/gs fields.
func localGitReflogBranch(gd, gs string) string {
	gd = strings.TrimSpace(gd)
	gs = strings.TrimSpace(gs)
	ref := gd
	if i := strings.Index(gd, "@{"); i >= 0 {
		ref = gd[:i]
	}
	ref = strings.TrimSpace(ref)
	if ref != "" && !strings.EqualFold(ref, "HEAD") {
		return ref
	}
	// HEAD@{n} checkout: moving from X to Y -> Y
	const prefix = "checkout: moving from "
	if !strings.HasPrefix(gs, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(gs, prefix)
	i := strings.LastIndex(rest, " to ")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(rest[i+len(" to "):])
}

// parseReflogTime extracts the reflog entry timestamp from a `%gd` selector
// captured with --date=iso, e.g. "HEAD@{2026-07-06 10:51:08 -0500}". This is
// when the reflog action happened — the true activity time — as opposed to the
// referenced commit's date. Returns ok=false when no timestamp is present.
func parseReflogTime(gd string) (time.Time, bool) {
	i := strings.Index(gd, "@{")
	if i < 0 {
		return time.Time{}, false
	}
	rest := gd[i+2:]
	j := strings.LastIndex(rest, "}")
	if j < 0 {
		return time.Time{}, false
	}
	inner := strings.TrimSpace(rest[:j])
	t, err := time.Parse("2006-01-02 15:04:05 -0700", inner)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// localGitWorktreeBranches maps each local branch name to the absolute path of
// the worktree that currently has it checked out, via `git worktree list
// --porcelain`. Grove-style setups keep many worktrees of one repository side
// by side under code_home, all sharing a single object store and refs, so `git
// log --all` run in ANY worktree lists commits from EVERY branch — tagged,
// misleadingly, with the scanned worktree's own path. Resolving a commit's
// branch back to the worktree that actually holds it keeps a work item's
// open-path pointed at the right directory instead of whichever sibling
// worktree happened to be scanned first (alphabetically). Bare and detached
// worktrees carry no branch line and are skipped; an ordinary single-worktree
// repo yields exactly one entry. Returns nil on error so callers fall back to
// the scanned path unchanged.
func localGitWorktreeBranches(ctx context.Context, abs string, opts *CollectOpts, directiveID string) map[string]string {
	out, err := gitOutput(ctx, abs, opts, directiveID, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	branches := map[string]string{}
	var current string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimSpace(strings.TrimPrefix(line, "branch "))
			if name := normalizeGitRef(ref); name != "" && current != "" {
				branches[name] = current
			}
		}
	}
	if len(branches) == 0 {
		return nil
	}
	return branches
}

// localGitCommonDir returns the absolute path of a repository's shared git
// directory (its "common dir"), which every linked worktree of that repository
// reports identically. It is the natural key for collapsing many worktrees of
// one repo down to a single commit scan: worktrees share one object store and
// ref set, so `git log --all` yields the same commits in each. Returns "" on
// error so callers can fall back to treating the directory as its own repo.
func localGitCommonDir(ctx context.Context, abs string, opts *CollectOpts, directiveID string) string {
	out, err := gitOutput(ctx, abs, opts, directiveID, "rev-parse", "--git-common-dir")
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return ""
	}
	// `--git-common-dir` is absolute for linked worktrees but relative (".git")
	// for an ordinary clone; resolve it against the scanned directory so the
	// key is stable and comparable across sibling worktrees.
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(abs, dir)
	}
	return filepath.Clean(dir)
}

// localGitCurrentBranch returns the checked-out branch name for a repo (or
// worktree), or "" when detached or on error. Cheap enough to call once per
// repo per collection.
func localGitCurrentBranch(ctx context.Context, abs string, opts *CollectOpts, directiveID string) string {
	out, err := gitOutput(ctx, abs, opts, directiveID, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" { // detached HEAD
		return ""
	}
	return b
}

// localGitTicket prefers a ticket scanned from the commit/reflog text and
// falls back to the repo's branch-derived ticket, so rows whose own text
// omits the key still correlate to the branch they were made on.
func localGitTicket(text, repoTicket string) string {
	if t := correlation.ScanTicketKey(text, correlation.Config{}); t != "" {
		return t
	}
	return repoTicket
}

// localGitRepositoryKey prefers remote.origin URL (owner/repo or nested path) so local-git
// aligns with GitHub / Gitea `repository`; falls back to the working tree directory name.
func localGitRepositoryKey(ctx context.Context, abs string, opts *CollectOpts, directiveID string) string {
	fallback := filepath.Base(abs)
	out, err := gitOutput(ctx, abs, opts, directiveID, "remote", "get-url", "origin")
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

// localGitSelfMatcher decides whether a commit's author belongs to the
// configured user. A commit matches when its author email equals either
// the per-repo or global `user.email`, or when the OS username appears
// (case-insensitive) anywhere in the author name. All comparisons are
// case-insensitive; empty matchers are simply skipped.
type localGitSelfMatcher struct {
	repoEmail   string
	globalEmail string
	user        string
}

func newLocalGitSelfMatcher(ctx context.Context, repoDir, globalEmail, currentUser string, opts *CollectOpts, directiveID string) localGitSelfMatcher {
	return localGitSelfMatcher{
		repoEmail:   strings.ToLower(strings.TrimSpace(gitConfigValue(ctx, repoDir, "--local", "user.email", opts, directiveID))),
		globalEmail: globalEmail,
		user:        currentUser,
	}
}

func (m localGitSelfMatcher) Match(name, email string) bool {
	e := strings.ToLower(strings.TrimSpace(email))
	if e != "" {
		if m.repoEmail != "" && e == m.repoEmail {
			return true
		}
		if m.globalEmail != "" && e == m.globalEmail {
			return true
		}
	}
	if m.user != "" {
		n := strings.ToLower(strings.TrimSpace(name))
		if n != "" && strings.Contains(n, m.user) {
			return true
		}
	}
	return false
}

// gitConfigValue runs `git config <scope> <key>` and returns the trimmed value.
// Errors (missing key, missing repo, no git binary) collapse to "" so callers
// can treat the absence the same as any other empty matcher.
func gitConfigValue(ctx context.Context, repoDir string, scope, key string, opts *CollectOpts, directiveID string) string {
	args := []string{}
	if repoDir != "" {
		args = append(args, "-C", repoDir)
	}
	args = append(args, "config")
	if scope != "" {
		args = append(args, scope)
	}
	args = append(args, "--get", key)
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := runAndLogExec(cmd, opts, directiveID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func currentOSUsername() string {
	if u := strings.TrimSpace(os.Getenv("USER")); u != "" {
		return u
	}
	if u := strings.TrimSpace(os.Getenv("USERNAME")); u != "" {
		return u
	}
	if cu, err := user.Current(); err == nil {
		return strings.TrimSpace(cu.Username)
	}
	return ""
}

// localGitRepoHasCommits reports whether the repo's HEAD resolves to a commit.
// Returns false only for the "unborn HEAD" case (post-`git init`, pre-first-
// commit) so callers can skip empty repos without swallowing real failures.
// `git rev-parse --verify --quiet HEAD` exits 1 for an unborn HEAD and 128 for
// genuine repo problems (corruption, safe.directory, missing .git) — only the
// exit-1 signal counts as empty; everything else returns true so the
// subsequent `git log` call in Collect can resurface the real error.
func localGitRepoHasCommits(ctx context.Context, repoDir string, opts *CollectOpts, directiveID string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--verify", "--quiet", "HEAD")
	_, err := runAndLogExec(cmd, opts, directiveID)
	if err == nil {
		return true
	}
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		return false
	}
	return true
}

func gitOutput(ctx context.Context, dir string, opts *CollectOpts, directiveID string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := runAndLogExec(cmd, opts, directiveID)
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
