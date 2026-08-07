package collectors

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/worktree"
)

// gitSem bounds concurrent git subprocesses across all collector goroutines, so
// many repos (or a pathological partial clone that lazy-fetches) cannot stampede
// the remote at once. Overridable via DOCENT_GIT_CONCURRENCY (default 4).
var gitSem = make(chan struct{}, gitConcurrencyLimit())

func gitConcurrencyLimit() int {
	if v := strings.TrimSpace(os.Getenv("DOCENT_GIT_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 4
}

// runGit runs a git subprocess through the shared exec logger with the two
// safety rails every collector git call needs: GIT_NO_LAZY_FETCH=1 so a
// read-only command never triggers an implicit promisor fetch (docent must never
// fetch), and the global concurrency cap above. The semaphore acquire honors
// ctx so a caller whose deadline fires while queued behind busy slots returns
// promptly instead of waiting for a slot it will never use.
func runGit(ctx context.Context, cmd *exec.Cmd, opts *CollectOpts, directiveID string) ([]byte, error) {
	prepareGitCmd(cmd)
	if err := acquireGitSlot(ctx); err != nil {
		return nil, err
	}
	defer func() { <-gitSem }()
	return runAndLogExec(cmd, opts, directiveID)
}

// acquireGitSlot blocks until a git concurrency slot is free or ctx is done.
func acquireGitSlot(ctx context.Context) error {
	select {
	case gitSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// prepareGitCmd sets GIT_NO_LAZY_FETCH=1 on a git command's environment.
func prepareGitCmd(cmd *exec.Cmd) {
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
}

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
		corrCfg := collectCorrCfg(opts)
		repoTicket := correlation.ScanTicketKey(branch, corrCfg)
		if repoTicket == "" {
			repoTicket = correlation.ScanTicketKey(filepath.Base(abs), corrCfg)
		}
		// When this repo has sibling worktrees sharing its refs, a commit
		// surfaced by `git log --all` may belong to a branch checked out
		// elsewhere. The layout maps branch -> owning worktree so each row is
		// tagged with the directory that actually holds its branch, not
		// whichever worktree we happen to be scanning.
		layout := localGitWorktreeLayout(ctx, abs, opts, directive.ID)

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
				// `git log --all` surfaces commits from every branch, including
				// ones checked out in a sibling worktree or on no worktree at
				// all (a merged/backport branch, a squash-merge reachable only
				// from a release ref). In a project whose children are worktrees
				// the scanned dir is just the alphabetically-first sibling, so
				// tagging those commits with it points the dashboard's "Open" at
				// an unrelated worktree and makes every worktreeless branch look
				// like it lives there. Fall back to the repository's own primary
				// working tree instead — an ordinary clone's one checkout is the
				// home for all its branches — which is nothing at all when the
				// repository is bare, and in either case does not depend on which
				// sibling the scan started from.
				commitDir := layout.ByBranch[ci.branch]
				if commitDir == "" {
					commitDir = layout.Home()
				}
				item := buildLocalGitCommitItem(directive.ID, repoLabel, commitDir, ci, dirs)
				if t := localGitTicket(ci.subject, repoTicket, corrCfg); t != "" {
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
			// A reflog row's own action happened in the scanned directory, but
			// the branch it names may live somewhere else entirely: `checkout:
			// moving from release/next to salsa-12761-x` is recorded in the
			// worktree you left, and correlation takes the FIRST path among a
			// work item's commit and reflog entities. So a row left tagged with
			// the scanned directory re-supplies the wrong path and undoes the
			// commit-side attribution above.
			if b := localGitReflogBranch(gd, gs); b != "" {
				fields["branch"] = b
				if wt := layout.ByBranch[b]; wt != "" {
					fields["path"] = wt
				} else if home := layout.Home(); home != "" {
					fields["path"] = home
				} else {
					delete(fields, "path")
				}
			}
			// Reflog subjects like "checkout: moving from main to salsa-123"
			// carry the branch (and thus ticket); fall back to the repo's
			// current-branch ticket otherwise.
			if t := localGitTicket(gs, repoTicket, corrCfg); t != "" {
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

func buildLocalGitCommitItem(directiveID, repoLabel, commitDir string, ci localGitCommit, allDirs []string) StatusItem {
	short := ci.hash
	if len(ci.hash) > 7 {
		short = ci.hash[:7]
	}
	title := ci.subject
	if len(allDirs) > 1 {
		title = fmt.Sprintf("(%s) %s", commitDisplayLabel(commitDir, ci.branch, repoLabel), ci.subject)
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
				"hash":         ci.hash,
				"short_hash":   short,
				"author":       ci.author,
				"author_email": ci.email,
				"iso":          ci.iso,
				"subject":      ci.subject,
				// is_self distinguishes the user's own commits from others'
				// (e.g. CI bots) that land on branches they have checked out,
				// so the report only credits genuinely-authored work.
				"is_self": strconv.FormatBool(ci.isSelf),
			}
			// Only carry a path when the commit resolves to a real worktree;
			// an empty commitDir means the branch has no working directory to
			// open, so the work item is left without an (incorrect) open path.
			if commitDir != "" {
				fields["path"] = commitDir
			}
			if ci.branch != "" {
				fields["branch"] = ci.branch
			}
			return fields
		}(),
	}
}

// commitDisplayLabel picks the parenthetical disambiguator shown before a
// commit subject when more than one repo/worktree is scanned together. It names
// the owning worktree directory when known; for a commit whose branch has no
// worktree it falls back to the branch name, then the repo label, rather than
// borrowing an unrelated scanned worktree's name.
func commitDisplayLabel(commitDir, branch, repoLabel string) string {
	if commitDir != "" {
		return filepath.Base(commitDir)
	}
	if branch != "" {
		return branch
	}
	return filepath.Base(repoLabel)
}

// normalizeGitRef maps a git log --source ref to a local branch name, or ""
// when the ref is not a local branch (remote, tag, detached, etc.). Commit
// sources and worktree listings have to agree on what a branch is called, or a
// commit's branch never matches the worktree holding it.
func normalizeGitRef(ref string) string {
	return worktree.BranchName(ref)
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

// localGitWorktreeLayout asks git where each of a repository's branches is
// checked out, and which directory is its own primary working tree.
//
// A project whose children are worktrees keeps many working trees of one
// repository side by side under code_home, all sharing a single object store and
// refs, so `git log --all` run in ANY worktree lists commits from EVERY branch —
// tagged, misleadingly, with the scanned worktree's own path. Resolving a
// commit's branch back to the worktree that actually holds it keeps a work
// item's open-path pointed at the right directory instead of whichever sibling
// happened to be scanned first (alphabetically).
//
// The command runs through the collector's own git wrapper — logging, the
// concurrency cap, GIT_NO_LAZY_FETCH — and only the parsing is shared, so there
// is one understanding of git's output without two understandings of how to run
// git. On error the layout degrades to "an ordinary clone rooted here", which is
// the pre-existing behaviour of trusting the scanned path.
func localGitWorktreeLayout(ctx context.Context, abs string, opts *CollectOpts, directiveID string) worktree.Layout {
	out, err := gitOutput(ctx, abs, opts, directiveID, "worktree", "list", "--porcelain")
	if err != nil {
		return worktree.Layout{MainDir: abs}
	}
	return worktree.Parse(out)
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
func localGitTicket(text, repoTicket string, cfg correlation.Config) string {
	if t := correlation.ScanTicketKey(text, cfg); t != "" {
		return t
	}
	return repoTicket
}

func collectCorrCfg(opts *CollectOpts) correlation.Config {
	if opts == nil {
		return correlation.Config{}
	}
	return opts.CorrCfg
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

// parseGitRemoteToRepositoryKey returns the path portion of a remote URL as
// host-relative repo identity (e.g. "org/repo"), or "" if the URL does not look
// like a standard forge URL. It delegates to model so local-git, the forge
// collectors, and grove project discovery all derive the same key from the same
// remote.
func parseGitRemoteToRepositoryKey(raw string) string {
	return model.RepoKeyFromRemote(raw)
}

// LocalGitRoots returns the directories the enabled local-git directives scan,
// as absolute paths, in configuration order and without duplicates.
//
// This is the answer to "where does this developer keep code", which docent
// needs beyond collection: provisioning an agent worktree means finding the
// grove project for a repository, and these roots are where to look. It reads
// the configured values rather than the scan results, so a root that currently
// holds nothing is still reported -- the caller is looking for projects, not for
// the repositories local-git happened to find.
func LocalGitRoots(directives []userdata.Directive) []string {
	expand := fallbackExpandRepoPath()
	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		if p = expand(strings.TrimSpace(p)); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, d := range directives {
		if d.Collector != "local-git" || !d.Enabled {
			continue
		}
		add(d.CodeHome)
		for _, p := range d.Paths {
			add(p)
		}
	}
	return out
}

// localGitDefaultScanDepth is the historical reach of a scan: only the
// immediate children of code_home, and the `paths` entries themselves.
const localGitDefaultScanDepth = 1

// localGitMaxScanDepth caps config.scan_depth. Two levels covers the layout
// that motivated the option — a grove project root holding a bare `.base`
// clone plus one worktree directory per branch — and the cap keeps a typo from
// turning collection into a walk of the whole home directory.
const localGitMaxScanDepth = 3

// localGitScanDepth reads Config["scan_depth"]: how many directory levels a
// scan may examine below each root. Missing, unparseable, or out-of-range
// values fall back to the single level local-git has always scanned.
func localGitScanDepth(directive userdata.Directive) int {
	raw := strings.TrimSpace(directive.Config["scan_depth"])
	if raw == "" {
		return localGitDefaultScanDepth
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < localGitDefaultScanDepth {
		return localGitDefaultScanDepth
	}
	if n > localGitMaxScanDepth {
		return localGitMaxScanDepth
	}
	return n
}

// localGitDepthSuffix annotates an empty-scan message with the depth actually
// searched, so "no repositories" is not read as a deeper search than it was.
func localGitDepthSuffix(depth int) string {
	if depth <= localGitDefaultScanDepth {
		return ""
	}
	return fmt.Sprintf(" (searched %d levels deep)", depth)
}

// localGitMissingRepoRemediation extends a "found nothing" remediation with the
// scan_depth hint, but only while the scan is still at its default reach: a
// grove project root looks empty to a one-level scan even though every worktree
// under it is a repo, and that is the most likely reason to be reading this.
func localGitMissingRepoRemediation(depth int, base string) string {
	if depth > localGitDefaultScanDepth {
		return base
	}
	return base + `; if the repos sit one level deeper (e.g. grove worktrees), set config.scan_depth: "2"`
}

// appendLocalGitRepos collects the git working trees at or below dir, appending
// each expanded, de-duplicated path to dirs, and reports whether it found any.
//
// A directory that is itself a working tree ends the descent: everything under
// it is its own source tree, not separate repositories. Only a directory that
// is NOT a repo gets opened up, which is exactly the shape of a grove project
// root — a bare `.base` clone beside one worktree directory per branch, with no
// `.git` of its own. Dot-directories are skipped while recursing, which is what
// keeps `.base` out of the results.
//
// depth is how many levels this call may examine: 1 means "dir or nothing", 2
// means "dir, else its children", and so on.
func appendLocalGitRepos(dir string, depth int, expand func(string) string, seen map[string]bool, dirs *[]string) bool {
	if depth < 1 {
		return false
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		abs := expand(dir)
		if !seen[abs] {
			seen[abs] = true
			*dirs = append(*dirs, abs)
		}
		return true
	}
	if depth == 1 {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	found := false
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if appendLocalGitRepos(filepath.Join(dir, e.Name()), depth-1, expand, seen, dirs) {
			found = true
		}
	}
	return found
}

// localGitRepoDirs resolves the directive to the working trees to scan: the
// `paths` entries when set, otherwise the children of code_home. Each candidate
// is walked to config.scan_depth levels, so a root that is not a repo itself
// can still yield the repos nested inside it.
func localGitRepoDirs(directive userdata.Directive, opts *CollectOpts, expand func(string) string) ([]string, error) {
	depth := localGitScanDepth(directive)
	var dirs []string
	seen := map[string]bool{}
	for _, p := range directive.Paths {
		p = expand(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		appendLocalGitRepos(p, depth, expand, seen, &dirs)
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
		appendLocalGitRepos(filepath.Join(codeHome, e.Name()), depth, expand, seen, &dirs)
	}
	if len(dirs) == 0 {
		return nil, fmt.Errorf("local-git: no git repositories under %s%s", codeHome, localGitDepthSuffix(depth))
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
	depth := localGitScanDepth(directive)
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
		if !appendLocalGitRepos(p, depth, expand, seen, &resolved) {
			issues = append(issues, ValidationIssue{
				Field:       "paths",
				Message:     fmt.Sprintf("%s is not a git working tree (missing .git)%s", p, localGitDepthSuffix(depth)),
				Remediation: localGitMissingRepoRemediation(depth, "point to a directory containing .git, or drop this entry"),
			})
			continue
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
			appendLocalGitRepos(filepath.Join(codeHome, e.Name()), depth, expand, seen, &resolved)
		}
		if len(resolved) == 0 {
			return append(issues, ValidationIssue{
				Field:       "code_home",
				Message:     fmt.Sprintf("no git repositories found under %s%s", codeHome, localGitDepthSuffix(depth)),
				Remediation: localGitMissingRepoRemediation(depth, "clone repos into code_home or point it at a directory of repos"),
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
		prepareGitCmd(cmd)
		// CombinedOutput (not runGit) so stderr is folded into the diagnostic
		// below; still take a concurrency slot, releasing it panic-safely.
		out, err := func() ([]byte, error) {
			if err := acquireGitSlot(probeCtx); err != nil {
				return nil, err
			}
			defer func() { <-gitSem }()
			return cmd.CombinedOutput()
		}()
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
	out, err := runGit(ctx, cmd, opts, directiveID)
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
	_, err := runGit(ctx, cmd, opts, directiveID)
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
	out, err := runGit(ctx, cmd, opts, directiveID)
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
