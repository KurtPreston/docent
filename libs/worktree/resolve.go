package worktree

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
)

// projectsDir is where docent keeps its own clones, under the state root.
const projectsDir = "projects"

// baseDirName is what docent calls the bare repository it creates. Detection
// never uses this name -- a project is recognized by having a bare child,
// whatever it is called -- but creating one means choosing a name, and a
// dot-prefixed one keeps it out of the directory scans that look for working
// trees.
const baseDirName = ".base"

// cloneTimeout bounds the initial bare clone. Generous because it is a full
// repository over the network the first time a repo is seen (and seconds after
// that, when a local copy can be referenced); bounded because a credential
// prompt with no terminal to answer it would otherwise hang the daemon forever.
const cloneTimeout = 15 * time.Minute

// fetchTimeout bounds a fetch of a single branch.
const fetchTimeout = 5 * time.Minute

// gitTimeout bounds the local git commands: config writes, ref lookups, adding
// a worktree. Adding a worktree writes out a full checkout, hence minutes.
const gitTimeout = 5 * time.Minute

// Request describes the working directory an agent needs.
type Request struct {
	// Repo is the host-relative repository identity ("Chip/salsa"). Required.
	Repo string
	// Branch is the branch to work on. Required.
	Branch string
	// BaseRef is the ref a brand-new branch is created from. Empty means the
	// remote's default branch, which is right for fresh work and wrong for a
	// backport.
	BaseRef string
	// OpenPath is a checkout this work item is already known to live in. It is
	// evidence about this specific item rather than an inference from a name, so
	// it wins over Roots when locating the developer's own copy.
	OpenPath string
	// Roots are the directories to search for the developer's copy of Repo --
	// the same roots local-git scans. Used to learn the clone URL, to reference
	// its objects, and to register remote.local.
	Roots []string
	// RemoteURL overrides where the bare clone is made from. Needed only for a
	// repository with no local copy for docent to read an origin off.
	RemoteURL string
	// Hook is the per-worktree setup script, run once when a directory is
	// created. Empty or missing means no setup.
	Hook string
	// Target names which placement the caller chose, for ResolveDeveloper. One
	// of TargetExisting, TargetCreate or TargetInPlace; Resolve ignores it,
	// being the isolated placement itself.
	Target string
	// StateRoot overrides the docent state directory, for tests.
	StateRoot string
}

// Result is a provisioned working directory.
type Result struct {
	// Dir is the directory the agent runs in.
	Dir string
	// Project is the root that owns Dir.
	Project string
	// Created reports that Dir did not exist before this call, which is also
	// when the setup hook ran.
	Created bool
	// Owned reports that this is docent's own directory: safe to commit into,
	// to sync, and to rebuild when it breaks. False for anywhere the developer
	// might have open in an editor.
	Owned bool
	// PreviousBranch is what an in-place checkout switched away from, so the
	// user can be told which branch their editor is no longer on. Empty for
	// every other placement, none of which move anything.
	PreviousBranch string
	// SetupErr is the setup hook's failure, if it had one. Not fatal: a checkout
	// with failed setup is still a checkout, and stranding it is worse than
	// reporting it.
	SetupErr error
}

// Resolve provisions a branch's worktree in docent's own tree, cloning the
// repository the first time it is asked for.
//
// # Why docent keeps its own copy
//
// docent used to do exactly this, gave it up, and has come back to it. The
// version that was given up put the agent in a checkout the developer had never
// opened, with none of the per-worktree setup that makes one usable, so a lane in
// the cockpit pointed at a directory nobody recognized -- a parallel universe.
// Running the agent in the developer's own worktree fixed that and introduced the
// opposite problem: one ref store, one directory, and git's one-worktree-per-branch
// rule meant an agent and a human could not hold the same branch, and an agent
// could rewrite a tree with unsaved editor state in it.
//
// This is not a straight revert, because the parallel universe is addressed
// rather than accepted. The developer's commits reach docent through remote.local
// before every turn; docent's reach the developer over the filesystem when they
// ask to open one; every turn ends in a commit, so nothing is invisible; and a
// fork of the two is refused rather than silently merged. Per-worktree setup is
// the configured hook's job, which is also how it stops being docent's problem to
// know what a usable checkout looks like in someone else's repository.
//
// The result is always Owned. Nothing infers ownership from the shape of a path.
func Resolve(ctx context.Context, req Request) (Result, error) {
	repo := strings.Trim(strings.TrimSpace(req.Repo), "/")
	branch := strings.Trim(strings.TrimSpace(req.Branch), "/")
	if repo == "" {
		return Result{}, fmt.Errorf("worktree: a repository is required")
	}
	if branch == "" {
		return Result{}, fmt.Errorf("worktree: a branch is required")
	}

	root := filepath.Join(stateRoot(req.StateRoot), projectsDir, SanitizePath(repo))
	local, hasLocal := DeveloperProject(req.OpenPath, req.Roots, repo)
	base, err := ensureBase(ctx, root, repo, local, hasLocal, req.RemoteURL)
	if err != nil {
		return Result{}, err
	}

	res := Result{Project: root, Owned: true}
	dir, created, err := ensureWorktree(ctx, base, root, branch, req.BaseRef)
	if err != nil {
		return Result{}, err
	}
	res.Dir, res.Created = dir, created
	if created {
		res.SetupErr = RunHook(ctx, HookRequest{
			Script:     req.Hook,
			Dir:        dir,
			Branch:     branch,
			Repo:       repo,
			ProjectDir: root,
			BaseRef:    req.BaseRef,
			Reference:  referenceFor(ctx, local, hasLocal, base, dir),
			Owned:      true,
		})
	}
	return res, nil
}

// DeveloperProject finds the developer's own copy of a repository.
//
// openPath wins when it resolves, because it is evidence about this specific
// work item rather than an inference from a repository name: it is a directory
// the developer's own activity happened in. Only when there is none -- the common
// case for a PR event with no local commits -- does it fall back to matching the
// repository against the configured roots.
func DeveloperProject(openPath string, roots []string, repo string) (Project, bool) {
	if p, ok := FindProject(openPath); ok {
		return p, true
	}
	return ProjectForRepo(roots, repo)
}

// stateRoot resolves docent's state directory, honouring a test override.
func stateRoot(override string) string {
	if s := strings.TrimSpace(override); s != "" {
		return s
	}
	return docentconfig.StateDir()
}

// ensureBase returns docent's bare repository for repo, cloning it on first use.
func ensureBase(ctx context.Context, root, repo string, local Project, hasLocal bool, remoteURL string) (string, error) {
	base := filepath.Join(root, baseDirName)
	if isBareRepo(base) {
		// A repository cloned locally after docent made its base should still
		// become reachable, so the local remote is reconciled every time rather
		// than only at creation.
		if hasLocal {
			if err := setRemote(ctx, base, localRemote, local.GitDir); err != nil {
				return "", err
			}
		}
		return base, nil
	}

	url, err := cloneURL(repo, local, hasLocal, remoteURL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	args := []string{"clone", "--bare", "--quiet"}
	if hasLocal {
		// Copying the objects off local disk turns a repository-sized network
		// fetch into a few seconds, and --dissociate then makes the copy docent's
		// own, so deleting the developer's clone later cannot break it. The cost
		// is a second copy of the object store, which is the trade this whole
		// design is making anyway.
		args = append(args, "--reference-if-able", local.GitDir, "--dissociate")
	}
	args = append(args, url, base)
	if err := gitRun(ctx, root, cloneTimeout, args...); err != nil {
		// A half-written clone would be mistaken for a usable one next time.
		os.RemoveAll(base)
		return "", err
	}

	// A bare clone has no fetch refspec, so without this `git fetch origin`
	// updates FETCH_HEAD and no remote-tracking branch, and every lookup of
	// origin/<branch> comes up empty.
	if err := setRemote(ctx, base, "origin", url); err != nil {
		return "", err
	}
	if err := gitRun(ctx, base, fetchTimeout, "fetch", "--quiet", "origin"); err != nil {
		return "", err
	}
	if err := adoptRemoteHead(ctx, base); err != nil {
		return "", err
	}
	if err := clearHeads(ctx, base); err != nil {
		return "", err
	}
	if hasLocal {
		if err := setRemote(ctx, base, localRemote, local.GitDir); err != nil {
			return "", err
		}
	}
	return base, nil
}

// adoptRemoteHead records the remote's default branch as origin/HEAD.
//
// A bare clone does not create one, and there is no need to ask the remote: the
// clone already set the repository's own HEAD from it. Reading it here rather
// than running `remote set-head --auto` keeps provisioning to a single network
// round trip.
func adoptRemoteHead(ctx context.Context, base string) error {
	out, err := gitOutput(ctx, base, gitTimeout, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return nil
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return nil
	}
	return gitRun(ctx, base, gitTimeout, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+name)
}

// clearHeads removes the local branches a bare clone starts with.
//
// `git clone --bare` copies every remote branch into refs/heads, and those refs
// then never move again -- the fetch refspec set above only updates
// refs/remotes/origin/*. Left in place they would make every branch resolve to
// its tip at the moment docent first saw the repository, months stale, because
// "this branch already exists locally" is the first thing worktree creation
// checks. Cleared, a local branch means what it should: one docent has worked on.
func clearHeads(ctx context.Context, base string) error {
	out, err := gitOutput(ctx, base, gitTimeout, "for-each-ref", "--format=%(refname)", "refs/heads/")
	if err != nil {
		return err
	}
	for _, ref := range strings.Split(out, "\n") {
		if ref = strings.TrimSpace(ref); ref == "" {
			continue
		}
		if err := gitRun(ctx, base, gitTimeout, "update-ref", "-d", ref); err != nil {
			return err
		}
	}
	return nil
}

// localRemote is the developer's own git directory, registered as a remote so
// commits they have made and pushed nowhere can still reach docent.
const localRemote = "local"

// setRemote points a remote at url with the standard fetch refspec, whether or
// not it already exists. Writing the config keys directly rather than choosing
// between `remote add` and `remote set-url` makes it idempotent.
func setRemote(ctx context.Context, base, name, url string) error {
	if err := gitRun(ctx, base, gitTimeout, "config", "remote."+name+".url", url); err != nil {
		return err
	}
	refspec := fmt.Sprintf("+refs/heads/*:refs/remotes/%s/*", name)
	return gitRun(ctx, base, gitTimeout, "config", "remote."+name+".fetch", refspec)
}

// cloneURL decides where docent's copy comes from.
//
// A local copy's origin is exact -- it is the URL the developer actually uses,
// including whichever host alias and protocol their credentials are set up for --
// so it beats anything configured. An explicit override is the answer for a
// repository with no local copy at all.
//
// Nothing is guessed from the repository name. A constructed git@host:owner/repo
// that is subtly wrong does not fail cleanly; it fails as an authentication
// prompt against a host nobody meant to contact, which a daemon experiences as a
// timeout several minutes later.
func cloneURL(repo string, local Project, hasLocal bool, override string) (string, error) {
	if hasLocal {
		if u := OriginURL(local.GitDir); u != "" {
			return u, nil
		}
	}
	if u := strings.TrimSpace(override); u != "" {
		return u, nil
	}
	if hasLocal {
		return "", fmt.Errorf("worktree: %s is checked out at %s but that copy has no origin remote to clone from",
			repo, local.Dir)
	}
	return "", fmt.Errorf("worktree: no local copy of %s to learn a clone URL from "+
		"(clone it under a directory local-git scans, or set a remote URL for it)", repo)
}

// ensureWorktree returns branch's directory under root, creating it if needed.
func ensureWorktree(ctx context.Context, base, root, branch, baseRef string) (dir string, created bool, err error) {
	layout, err := List(ctx, base)
	if err != nil {
		return "", false, err
	}
	if existing, ok := layout.ByBranch[branch]; ok {
		if isWorktreeOf(ctx, existing, base) {
			// Returned as it stands, with its working state neither inspected
			// nor altered. Sessions resume across turns, so resetting a reused
			// worktree would discard the previous turn's work.
			return existing, false, nil
		}
		// git still lists a directory that is gone or no longer a checkout --
		// deleted by hand, or a clone interrupted midway. Only docent's own tree
		// reaches this code, so healing it is safe in a way it would never be in
		// the developer's project.
		if err := gitRun(ctx, base, gitTimeout, "worktree", "prune"); err != nil {
			return "", false, err
		}
		os.RemoveAll(existing)
	}

	dir = filepath.Join(root, SanitizePath(branch))
	if _, statErr := os.Stat(dir); statErr == nil && !isWorktreeOf(ctx, dir, base) {
		os.RemoveAll(dir)
	}
	if err := addWorktree(ctx, base, dir, branch, baseRef); err != nil {
		return "", false, err
	}
	return dir, true, nil
}

// isWorktreeOf reports whether dir is a live worktree of the repository at base.
func isWorktreeOf(ctx context.Context, dir, base string) bool {
	out, err := gitOutput(ctx, dir, gitTimeout, "rev-parse", "--git-common-dir")
	if err != nil {
		return false
	}
	common := strings.TrimSpace(out)
	if common == "" {
		return false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return SamePath(common, base)
}

// addWorktree checks branch out at dir, creating the branch when it does not
// exist yet.
//
// The ladder matters for what an agent ends up working on. An existing local
// branch is continued; a branch that exists only on the remote is tracked, so a
// later push is a fast-forward rather than a surprise; a branch that exists
// nowhere is created from the ref the caller asked for, which is the difference
// between a backport landing on the release line and on the default branch.
func addWorktree(ctx context.Context, base, dir, branch, baseRef string) error {
	if hasRef(ctx, base, "refs/heads/"+branch) {
		// A local branch here is one docent has worked on before, carrying
		// earlier turns' commits, so it is continued rather than reset.
		return gitRun(ctx, base, gitTimeout, "worktree", "add", "--quiet", dir, branch)
	}
	remoteRef := "refs/remotes/origin/" + branch
	// Ask the remote before deciding anything, rather than only when the
	// tracking ref is missing. It may predate the branch -- but it may equally
	// predate the branch's current tip, and starting an agent on a months-old
	// snapshot of someone else's branch is the more confusing of the two
	// failures. Ignored on error: the branch may genuinely not exist there.
	_ = gitRun(ctx, base, fetchTimeout, "fetch", "--quiet", "origin",
		"+refs/heads/"+branch+":"+remoteRef)
	if hasRef(ctx, base, remoteRef) {
		return gitRun(ctx, base, gitTimeout, "worktree", "add", "--quiet",
			"--track", "-b", branch, dir, "origin/"+branch)
	}
	start, err := startPoint(ctx, base, baseRef)
	if err != nil {
		return err
	}
	return gitRun(ctx, base, gitTimeout, "worktree", "add", "--quiet", "-b", branch, dir, start)
}

// startPoint resolves what a brand-new branch is based on: the caller's ref when
// given, otherwise the remote's default branch.
func startPoint(ctx context.Context, base, baseRef string) (string, error) {
	if ref := strings.TrimSpace(baseRef); ref != "" {
		if hasRef(ctx, base, "refs/heads/"+ref) {
			return ref, nil
		}
		if hasRef(ctx, base, "refs/remotes/origin/"+ref) {
			return "origin/" + ref, nil
		}
		// Silently defaulting here is the difference between a backport landing
		// on the release line and landing on the default branch.
		return "", fmt.Errorf("worktree: base ref %q is not in %s", ref, base)
	}
	out, err := gitOutput(ctx, base, gitTimeout, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if head := strings.TrimSpace(out); err == nil && head != "" {
		return head, nil
	}
	// Not every repository has origin/HEAD -- git only writes it on a clone that
	// asked for it, and docent reads other people's repositories as well as its
	// own. The repository's own HEAD is the same answer from the other side, and
	// only a repository with no commits at all has neither.
	if err := gitRun(ctx, base, gitTimeout, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		return "HEAD", nil
	}
	return "", fmt.Errorf("worktree: cannot tell what %s's default branch is", base)
}

func hasRef(ctx context.Context, base, ref string) bool {
	err := gitRun(ctx, base, gitTimeout, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// referenceFor picks an existing checkout of the same repository for the setup
// hook to copy ignored files from -- the .env files and local configuration that
// are the difference between a habitable checkout and a bare tree.
//
// The developer's own copy is preferred, since that is where those files
// actually are. A sibling in docent's tree is the fallback for a repository with
// no local copy, which at least carries whatever the hook put there last time.
func referenceFor(ctx context.Context, local Project, hasLocal bool, base, exclude string) string {
	if hasLocal {
		if layout, err := List(ctx, local.GitDir); err == nil {
			if dir := someWorktree(layout, exclude); dir != "" {
				return dir
			}
		}
	}
	if layout, err := List(ctx, base); err == nil {
		return someWorktree(layout, exclude)
	}
	return ""
}

// someWorktree picks one working tree from a layout, deterministically so the
// hook is not handed a different reference on every run. The repository's own
// main tree is the most likely to be fully set up; otherwise the first worktree
// in path order.
func someWorktree(l Layout, exclude string) string {
	if home := l.Home(); home != "" && !SamePath(home, exclude) {
		return home
	}
	best := ""
	for _, dir := range l.ByBranch {
		if SamePath(dir, exclude) {
			continue
		}
		if best == "" || dir < best {
			best = dir
		}
	}
	return best
}

// SanitizePath maps a repository or branch name to a token safe to use as a path
// segment: slashes become dashes, anything else unusual becomes an underscore.
//
// docent names the directories it creates, in its own tree and in the
// developer's. A branch's real name is what git records, and this is only how it
// is spelled on disk.
func SanitizePath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '/' || r == ':' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
