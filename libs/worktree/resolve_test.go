package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forgePrefix is the SCP-style prefix the fixture forge answers on. Repository
// identity is derived from a remote URL, so a fixture whose origin is a bare
// filesystem path would have no identity at all and never match a lookup.
const forgePrefix = "git@forge.test:"

// forge is a stand-in remote host. Origins carry real forge URLs, and git's
// insteadOf rewriting maps them to directories, so the fixtures exercise the
// same URL handling as a real clone while still running off local disk.
//
// Real git throughout: what is being tested is which git commands docent runs
// and with what arguments, and a stub would let a wrong one pass.
type forge struct {
	t    *testing.T
	root string
}

func newForge(t *testing.T) *forge {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	// The identity is part of the fixture because GIT_CONFIG_GLOBAL replaces the
	// machine's own, and docent commits as whoever runs it -- there is no
	// docent identity to fall back on.
	body := "[url \"" + root + string(filepath.Separator) + "\"]\n\tinsteadOf = " + forgePrefix + "\n" +
		"[user]\n\tname = Test\n\temail = test@example\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	return &forge{t: t, root: root}
}

// repo publishes owner/name with one commit on main and returns its forge URL.
func (f *forge) repo(name string) string {
	f.t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(name)+".git")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	gitAt(f.t, filepath.Dir(path), "clone", "--bare", "-q", seedRepo(f.t), path)
	return forgePrefix + name + ".git"
}

// clone makes a working copy of a forge URL, standing in either for the
// developer's own checkout or for a scratch directory used to publish branches.
func (f *forge) clone(parent, name, url string) string {
	f.t.Helper()
	if err := os.MkdirAll(parent, 0o755); err != nil {
		f.t.Fatal(err)
	}
	dir := filepath.Join(parent, name)
	gitAt(f.t, parent, "clone", "-q", url, dir)
	return dir
}

func headSubject(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitOutput(context.Background(), dir, gitTimeout, "log", "-1", "--format=%s")
	if err != nil {
		t.Fatalf("log in %s: %v", dir, err)
	}
	return strings.TrimSpace(out)
}

func currentBranch(t *testing.T, dir string) string {
	t.Helper()
	out, err := gitOutput(context.Background(), dir, gitTimeout, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse in %s: %v", dir, err)
	}
	return strings.TrimSpace(out)
}

func TestResolveClonesAndCreatesTheWorktree(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)
	state := t.TempDir()

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !res.Owned {
		t.Error("Owned = false; docent's own tree is always owned")
	}
	if !res.Created {
		t.Error("Created = false on a first resolve")
	}
	// Slashes become dashes on disk while the branch keeps its real name.
	wantDir := filepath.Join(state, "projects", "Chip-salsa", "salsa-1-fix")
	if !SamePath(res.Dir, wantDir) {
		t.Errorf("Dir = %q, want %q", res.Dir, wantDir)
	}
	if got := currentBranch(t, res.Dir); got != "salsa-1/fix" {
		t.Errorf("checked out %q, want the unsanitized branch name", got)
	}

	base := filepath.Join(state, "projects", "Chip-salsa", ".base")
	if !isBareRepo(base) {
		t.Fatalf("no bare repository at %s", base)
	}
	cfg := gitConfig(base)
	// Without an explicit refspec a bare clone's fetch updates FETCH_HEAD and no
	// remote-tracking branch, so origin/<branch> never resolves.
	if got := cfg["remote.origin.fetch"]; got != "+refs/heads/*:refs/remotes/origin/*" {
		t.Errorf("remote.origin.fetch = %q", got)
	}
	if got := cfg["remote.origin.url"]; got != remote {
		t.Errorf("remote.origin.url = %q, want the developer's origin %q", got, remote)
	}
	// The developer's own git dir, so commits they made and pushed nowhere can
	// still reach docent before a turn.
	if got := cfg["remote.local.url"]; got != filepath.Join(local, ".git") {
		t.Errorf("remote.local.url = %q, want the developer's git dir", got)
	}
	if got := cfg["remote.local.fetch"]; got != "+refs/heads/*:refs/remotes/local/*" {
		t.Errorf("remote.local.fetch = %q", got)
	}
	// --dissociate means docent's objects are its own, so losing the developer's
	// clone cannot break it.
	if _, err := os.Stat(filepath.Join(base, "objects", "info", "alternates")); err == nil {
		t.Error("the clone still borrows objects through alternates; --dissociate did not take effect")
	}
}

// A second resolve of the same branch returns the same directory untouched.
// Sessions resume across turns, so resetting a reused worktree would throw away
// the previous turn's work.
func TestResolveReusesAWorktreeWithoutTouchingIt(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	req := Request{Repo: "Chip/salsa", Branch: "feature", Roots: []string{code}, StateRoot: state}
	first, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(first.Dir, "uncommitted.txt")
	if err := os.WriteFile(scratch, []byte("mid-turn work"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitAt(t, first.Dir, "commit", "--allow-empty", "-q", "-m", "turn 1")

	second, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if second.Dir != first.Dir {
		t.Errorf("second resolve = %q, want %q", second.Dir, first.Dir)
	}
	if second.Created {
		t.Error("Created = true for a directory that already existed")
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Errorf("uncommitted work was discarded: %v", err)
	}
	if got := headSubject(t, second.Dir); got != "turn 1" {
		t.Errorf("HEAD = %q; the previous turn's commit was reset away", got)
	}
}

// A directory git still lists but that is gone or no longer a checkout is
// rebuilt rather than reported. Only docent's own tree reaches this, which is
// what makes healing it safe.
func TestResolveRebuildsABrokenWorktree(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	req := Request{Repo: "Chip/salsa", Branch: "feature", Roots: []string{code}, StateRoot: state}
	first, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// Deleted behind git's back, then replaced with something that is not a
	// checkout at all -- what a clone interrupted midway leaves.
	if err := os.RemoveAll(first.Dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(first.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Dir, "debris"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	second, err := Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("Resolve did not heal a broken worktree: %v", err)
	}
	if !second.Created {
		t.Error("Created = false; the worktree was rebuilt and setup should have run")
	}
	if got := currentBranch(t, second.Dir); got != "feature" {
		t.Errorf("rebuilt worktree is on %q", got)
	}
}

// A branch that exists only on the remote must be tracked, not forked: a later
// push has to be a fast-forward rather than a surprise.
func TestResolveTracksARemoteBranch(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	// Create the branch on the remote only, with a commit of its own.
	scratch := f.clone(t.TempDir(), "scratch", remote)
	gitAt(t, scratch, "checkout", "-q", "-b", "already-remote")
	gitAt(t, scratch, "commit", "--allow-empty", "-q", "-m", "remote work")
	gitAt(t, scratch, "push", "-q", "origin", "already-remote")

	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "already-remote", Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headSubject(t, res.Dir); got != "remote work" {
		t.Errorf("HEAD = %q, want the remote branch's own tip", got)
	}
	out, err := gitOutput(context.Background(), res.Dir, gitTimeout, "rev-parse", "--abbrev-ref", "already-remote@{upstream}")
	if err != nil {
		t.Fatalf("branch has no upstream: %v", err)
	}
	if got := strings.TrimSpace(out); got != "origin/already-remote" {
		t.Errorf("upstream = %q, want origin/already-remote", got)
	}
}

// Without a base ref a new branch comes off the default branch, which is right
// for fresh work and wrong for a backport.
func TestResolveBasesANewBranchOnBaseRef(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	scratch := f.clone(t.TempDir(), "scratch", remote)
	gitAt(t, scratch, "checkout", "-q", "-b", "release/4.1")
	gitAt(t, scratch, "commit", "--allow-empty", "-q", "-m", "on the release line")
	gitAt(t, scratch, "push", "-q", "origin", "release/4.1")
	gitAt(t, scratch, "checkout", "-q", "main")
	gitAt(t, scratch, "commit", "--allow-empty", "-q", "-m", "on main")
	gitAt(t, scratch, "push", "-q", "origin", "main")

	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "backport/x", BaseRef: "release/4.1",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headSubject(t, res.Dir); got != "on the release line" {
		t.Errorf("HEAD = %q, want the branch to start from the base ref", got)
	}

	// A base ref that is nowhere is an error rather than a silent default: it is
	// the difference between a backport landing on the release line and on main.
	if _, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "backport/y", BaseRef: "release/nope",
		Roots: []string{code}, StateRoot: state,
	}); err == nil {
		t.Error("a missing base ref was silently ignored")
	}
}

func TestResolveDefaultsToTheRemoteDefaultBranch(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "brand-new", Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headSubject(t, res.Dir); got != "seed" {
		t.Errorf("HEAD = %q, want the default branch's tip", got)
	}
}

// A bare clone copies every remote branch into refs/heads, and those refs never
// move again -- the fetch refspec only updates refs/remotes/origin/*. Left in
// place they make every branch resolve to its tip at the moment docent first saw
// the repository, which for a long-lived install is months stale and looks like
// an agent inexplicably working on old code.
func TestResolveGetsTheCurrentTipOfABranchItHasNotWorkedOn(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	scratch := f.clone(t.TempDir(), "scratch", remote)
	gitAt(t, scratch, "checkout", "-q", "-b", "long-lived")
	gitAt(t, scratch, "commit", "--allow-empty", "-q", "-m", "tip at clone time")
	gitAt(t, scratch, "push", "-q", "origin", "long-lived")

	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()

	// Provisioning an unrelated branch is what makes docent's clone, fixing the
	// moment "clone time" refers to.
	if _, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "unrelated", Roots: []string{code}, StateRoot: state,
	}); err != nil {
		t.Fatal(err)
	}

	gitAt(t, scratch, "commit", "--allow-empty", "-q", "-m", "moved on since")
	gitAt(t, scratch, "push", "-q", "origin", "long-lived")

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "long-lived", Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headSubject(t, res.Dir); got != "moved on since" {
		t.Errorf("HEAD = %q, want the branch's current tip", got)
	}
}

// The open path is a checkout of this specific work item, so it beats matching
// the repository name against the roots -- which is a guess among clones.
func TestResolvePrefersTheOpenPathsCopy(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	byName := f.repo("Chip/salsa")
	elsewhere := f.repo("Chip/salsa-fork")
	code := t.TempDir()
	f.clone(code, "salsa", byName)
	openPath := f.clone(t.TempDir(), "salsa-fork", elsewhere)
	state := t.TempDir()

	if _, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", OpenPath: openPath,
		Roots: []string{code}, StateRoot: state,
	}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(state, "projects", "Chip-salsa", ".base")
	if got := gitConfig(base)["remote.origin.url"]; got != elsewhere {
		t.Errorf("cloned from %q, want the open path's origin %q", got, elsewhere)
	}
}

// An explicit URL is the answer for a repository with no local copy. Nothing is
// guessed from the repository name: a constructed URL that is subtly wrong fails
// as an authentication prompt against a host nobody meant to contact.
func TestResolveUsesAnExplicitRemoteURL(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	state := t.TempDir()

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", RemoteURL: remote,
		Roots: []string{t.TempDir()}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("Resolve with an explicit URL: %v", err)
	}
	if got := currentBranch(t, res.Dir); got != "feature" {
		t.Errorf("checked out %q", got)
	}
	// No local copy means no local remote to register.
	if got := gitConfig(filepath.Join(state, "projects", "Chip-salsa", ".base"))["remote.local.url"]; got != "" {
		t.Errorf("remote.local.url = %q with no developer copy", got)
	}
}

func TestResolveWithNothingToCloneFromExplainsItself(t *testing.T) {
	requireGit(t)
	_, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", Roots: []string{t.TempDir()}, StateRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"Chip/salsa", "local-git"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
}

// A clone made before the developer had a local copy should still gain the local
// remote once they do, since that is the only way their unpushed commits reach
// docent.
func TestResolveAddsTheLocalRemoteLater(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	state := t.TempDir()
	base := filepath.Join(state, "projects", "Chip-salsa", ".base")

	if _, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", RemoteURL: remote,
		Roots: []string{code}, StateRoot: state,
	}); err != nil {
		t.Fatal(err)
	}
	if got := gitConfig(base)["remote.local.url"]; got != "" {
		t.Fatalf("remote.local.url = %q before the developer cloned anything", got)
	}

	local := f.clone(code, "salsa", remote)
	if _, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", Roots: []string{code}, StateRoot: state,
	}); err != nil {
		t.Fatal(err)
	}
	if got := gitConfig(base)["remote.local.url"]; got != filepath.Join(local, ".git") {
		t.Errorf("remote.local.url = %q, want the newly cloned copy's git dir", got)
	}
}

func TestResolveRequiresRepoAndBranch(t *testing.T) {
	for name, req := range map[string]Request{
		"no repo":   {Branch: "feature"},
		"no branch": {Repo: "Chip/salsa"},
		"blank":     {Repo: "  ", Branch: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Resolve(context.Background(), req); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestSanitizePath(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Chip/salsa", "Chip-salsa"},
		{"salsa-12830/market_grid_config", "salsa-12830-market_grid_config"},
		{"backport/pr-1-to-4.0", "backport-pr-1-to-4.0"},
		{"feature with spaces", "feature-with-spaces"},
		{"weird*chars?", "weird_chars_"},
		{"", ""},
		{"   ", ""},
	} {
		if got := SanitizePath(tc.in); got != tc.want {
			t.Errorf("SanitizePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
