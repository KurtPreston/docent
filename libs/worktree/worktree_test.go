package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// salsaPorcelain is the real shape of a worktree project: a bare main entry with
// no branch line, then one entry per worktree. Branch names keep their slashes
// even though the directories holding them do not, which is exactly the
// confusion a lookup by branch has to survive.
const salsaPorcelain = `worktree /home/k/Code/salsa/.base
bare

worktree /home/k/Code/salsa/release-next
HEAD 1a8cbfba41adb358d5a2d51e5f8f8f0da9494fe3
branch refs/heads/release/next

worktree /home/k/Code/salsa/salsa-12830-market_grid_config
HEAD 77c38faecf4fd0fde677f621bc22320851a94787
branch refs/heads/salsa-12830/market_grid_config

worktree /home/k/Code/salsa/detached-poke
HEAD e39f96d2feb6f6636236cf89ae0e8bb3a40b262e
detached

`

func TestParseWorktreeProject(t *testing.T) {
	l := Parse(salsaPorcelain)
	if l.MainDir != "/home/k/Code/salsa/.base" {
		t.Errorf("MainDir = %q, want the first entry", l.MainDir)
	}
	if !l.Bare {
		t.Error("Bare = false; the first entry carried a bare line")
	}
	if got := l.ByBranch["release/next"]; got != "/home/k/Code/salsa/release-next" {
		t.Errorf("release/next -> %q", got)
	}
	// The branch keeps its slash while its directory does not; conflating the
	// two is how a lookup by branch silently misses.
	if got := l.ByBranch["salsa-12830/market_grid_config"]; got != "/home/k/Code/salsa/salsa-12830-market_grid_config" {
		t.Errorf("slashed branch -> %q", got)
	}
	if len(l.ByBranch) != 2 {
		t.Errorf("ByBranch = %v; bare and detached entries carry no branch", l.ByBranch)
	}
}

func TestParseSingleCheckout(t *testing.T) {
	l := Parse("worktree /home/k/Code/docent\nHEAD abc123\nbranch refs/heads/main\n")
	if l.MainDir != "/home/k/Code/docent" || l.Bare {
		t.Errorf("MainDir = %q, Bare = %v", l.MainDir, l.Bare)
	}
	if got := l.ByBranch["main"]; got != "/home/k/Code/docent" {
		t.Errorf("main -> %q", got)
	}
}

// A `worktree` line ends the previous entry, so output whose last entry has no
// trailing blank line parses identically. Losing the final entry would silently
// drop whichever worktree git happened to list last.
func TestParseWithoutTrailingSeparator(t *testing.T) {
	l := Parse("worktree /a\nbare\n\nworktree /b\nbranch refs/heads/feature")
	if l.MainDir != "/a" || !l.Bare {
		t.Errorf("MainDir = %q, Bare = %v", l.MainDir, l.Bare)
	}
	if got := l.ByBranch["feature"]; got != "/b" {
		t.Errorf("feature -> %q, want /b from the unterminated final entry", got)
	}
}

func TestParseEmpty(t *testing.T) {
	l := Parse("")
	if l.MainDir != "" || l.Bare || len(l.ByBranch) != 0 {
		t.Errorf("empty output produced %+v", l)
	}
	// ByBranch must be usable without a nil check.
	if _, ok := l.ByBranch["anything"]; ok {
		t.Error("empty layout claims to know a branch")
	}
}

// Home is the whole attribution rule: a branch with no worktree of its own
// belongs in an ordinary clone's one working tree, and belongs nowhere in a
// project whose repository is bare.
func TestLayoutHome(t *testing.T) {
	single := Parse("worktree /home/k/Code/docent\nbranch refs/heads/main\n")
	if got := single.Home(); got != "/home/k/Code/docent" {
		t.Errorf("Home = %q, want an ordinary clone's own working tree", got)
	}
	// Adding a linked worktree beside an ordinary clone does not move its home,
	// and neither does which directory a scan happened to start from.
	withLinked := Parse("worktree /home/k/Code/docent\nbranch refs/heads/main\n\nworktree /tmp/side\nbranch refs/heads/side\n")
	if got := withLinked.Home(); got != "/home/k/Code/docent" {
		t.Errorf("Home = %q, want the main working tree", got)
	}

	if got := Parse(salsaPorcelain).Home(); got != "" {
		t.Errorf("Home = %q, want none: a bare repository has no working tree to absorb a branch", got)
	}
	if got := Parse("").Home(); got != "" {
		t.Errorf("Home = %q, want empty", got)
	}
}

func TestBranchName(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"refs/heads/main", "main"},
		{"refs/heads/salsa-1/fix", "salsa-1/fix"},
		{"refs/remotes/origin/main", ""},
		{"refs/tags/v1", ""},
		{"", ""},
	} {
		if got := BranchName(tc.in); got != tc.want {
			t.Errorf("BranchName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if !SamePath(dir, dir+"/") {
		t.Error("a trailing slash made a path different from itself")
	}
	// A code_home reached through a symlink and the real path git prints are the
	// same directory; a comparison that says otherwise drops every attribution
	// in that tree.
	if !SamePath(link, dir) {
		t.Errorf("SamePath(%q, %q) = false through a symlink", link, dir)
	}
	if SamePath("", dir) || SamePath(dir, "") {
		t.Error("an empty path matched something")
	}
	if SamePath(dir, filepath.Join(dir, "child")) {
		t.Error("a child matched its parent")
	}
}

// List has to agree with Parse against real git, which is the only way to catch
// a porcelain format assumption that is merely plausible.
func TestListAgainstRealGit(t *testing.T) {
	requireGit(t)
	project := t.TempDir()
	bare := bareCloneInto(t, project, ".base", "git@host:Chip/salsa.git")
	wt := filepath.Join(project, "feature-x")
	gitAt(t, bare, "worktree", "add", "-b", "feature/x", wt)

	l, err := List(context.Background(), bare)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !l.Bare {
		t.Errorf("a bare repository's main entry reported non-bare: %+v", l)
	}
	if !SamePath(l.MainDir, bare) {
		t.Errorf("MainDir = %q, want %q", l.MainDir, bare)
	}
	if got := l.ByBranch["feature/x"]; !SamePath(got, wt) {
		t.Errorf("feature/x -> %q, want %q", got, wt)
	}

	// Asked from inside the worktree, git reports the same repository, so the
	// layout must match.
	fromWorktree, err := List(context.Background(), wt)
	if err != nil {
		t.Fatalf("List from worktree: %v", err)
	}
	if !SamePath(fromWorktree.MainDir, l.MainDir) || len(fromWorktree.ByBranch) != len(l.ByBranch) {
		t.Errorf("layout differs by where it was asked: %+v vs %+v", fromWorktree, l)
	}
}

func TestListOnANonRepo(t *testing.T) {
	requireGit(t)
	if _, err := List(context.Background(), t.TempDir()); err == nil {
		t.Error("listing a directory that is not a repository succeeded")
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// seedRepo is an ordinary repo with one commit, to clone from.
func seedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitAt(t, dir, "init", "--initial-branch=main", "-q", ".")
	gitAt(t, dir, "commit", "--allow-empty", "-q", "-m", "seed")
	return dir
}

// bareCloneInto makes project a worktree project by cloning a seed repo into
// project/<name> as a bare repository with the given origin. A real clone rather
// than `init --bare` because worktrees need a commit to check out.
func bareCloneInto(t *testing.T, project, name, origin string) string {
	t.Helper()
	seed := seedRepo(t)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	bare := filepath.Join(project, name)
	gitAt(t, project, "clone", "--bare", "-q", seed, bare)
	if origin != "" {
		gitAt(t, bare, "remote", "set-url", "origin", origin)
	}
	return bare
}
