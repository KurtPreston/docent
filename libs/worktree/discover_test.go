package worktree

import (
	"os"
	"path/filepath"
	"testing"
)

// initBare makes project a worktree project cheaply: project/<name> is an empty
// bare repository. Cheap enough to build a dozen of, for the tests that only
// care about discovery and not about checking anything out.
func initBare(t *testing.T, project, name, origin string) string {
	t.Helper()
	requireGit(t)
	bare := filepath.Join(project, name)
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, bare, "init", "--bare", "-q")
	if origin != "" {
		gitAt(t, bare, "remote", "add", "origin", origin)
	}
	return bare
}

// initSingle makes dir an ordinary clone: one working tree, git directory at
// .git.
func initSingle(t *testing.T, dir, origin string) string {
	t.Helper()
	requireGit(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, dir, "init", "--initial-branch=main", "-q", ".")
	if origin != "" {
		gitAt(t, dir, "remote", "add", "origin", origin)
	}
	return dir
}

func TestIdentifyWorktreeProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "salsa")
	bare := initBare(t, project, ".base", "git@host:Chip/salsa.git")

	got, ok := Identify(project)
	if !ok {
		t.Fatal("a directory with a bare child was not identified as a project")
	}
	if got.Kind != KindWorktrees {
		t.Errorf("Kind = %q, want %q", got.Kind, KindWorktrees)
	}
	if !SamePath(got.GitDir, bare) {
		t.Errorf("GitDir = %q, want the bare child %q", got.GitDir, bare)
	}
	if got.Repo != "Chip/salsa" {
		t.Errorf("Repo = %q, want Chip/salsa read from the bare child's config", got.Repo)
	}
}

// The bare directory's name is not part of the test docent applies, so a project
// laid out by a different tool -- or by hand -- is recognized just the same.
func TestIdentifyIgnoresTheBareDirectoryName(t *testing.T) {
	for _, name := range []string{".base", ".bare", "repo.git", "_git"} {
		project := filepath.Join(t.TempDir(), "proj")
		initBare(t, project, name, "git@host:Some/repo.git")
		got, ok := Identify(project)
		if !ok || got.Kind != KindWorktrees {
			t.Errorf("bare child named %q: Identify = %+v, %v", name, got, ok)
		}
	}
}

// The landmine this ordering exists for: a worktree project's root can itself be
// an unrelated repository -- a checkout of the scripts that set the project up,
// tracked separately from the code. Judging the root by its own .git resolves the
// wrong git directory, reports the wrong origin, and loses the real project.
func TestIdentifyPrefersBareChildOverTheRootsOwnRepo(t *testing.T) {
	project := filepath.Join(t.TempDir(), "salsa")
	bare := initBare(t, project, ".base", "git@host:Chip/salsa.git")
	// The project root is also its own non-bare repo, with a different origin.
	initSingle(t, project, "git@host:Chip/salsa-project-scripts.git")
	if !isDir(filepath.Join(project, ".git")) {
		t.Fatal("fixture did not create the root's own .git directory")
	}

	got, ok := Identify(project)
	if !ok {
		t.Fatal("project not identified")
	}
	if got.Kind != KindWorktrees {
		t.Errorf("Kind = %q, want %q: the bare child must win", got.Kind, KindWorktrees)
	}
	if !SamePath(got.GitDir, bare) {
		t.Errorf("GitDir = %q, want the bare child %q, not the root's own .git", got.GitDir, bare)
	}
	if got.Repo != "Chip/salsa" {
		t.Errorf("Repo = %q, want the bare child's origin", got.Repo)
	}
}

func TestIdentifySingleCheckout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "docent")
	initSingle(t, dir, "git@github.com:KurtPreston/docent.git")

	got, ok := Identify(dir)
	if !ok {
		t.Fatal("an ordinary clone was not identified")
	}
	if got.Kind != KindSingle {
		t.Errorf("Kind = %q, want %q", got.Kind, KindSingle)
	}
	if !SamePath(got.GitDir, filepath.Join(dir, ".git")) {
		t.Errorf("GitDir = %q", got.GitDir)
	}
	if got.Repo != "KurtPreston/docent" {
		t.Errorf("Repo = %q", got.Repo)
	}
}

// A .git *directory* is the repository of the directory holding it, never a bare
// sibling, so it must not make an ordinary clone look like a worktree project.
func TestIdentifyDoesNotTreatDotGitAsABareChild(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "plain")
	initSingle(t, dir, "git@host:Some/plain.git")
	got, _ := Identify(dir)
	if got.Kind != KindSingle {
		t.Errorf("Kind = %q, want %q; .git was mistaken for a bare child", got.Kind, KindSingle)
	}
}

// A linked worktree is not a project; the repository it belongs to is. Without
// this, every worktree of a project becomes its own project.
func TestIdentifyNormalizesALinkedWorktree(t *testing.T) {
	project := filepath.Join(t.TempDir(), "salsa")
	bare := bareCloneInto(t, project, ".base", "git@host:Chip/salsa.git")
	wt := filepath.Join(project, "feature-x")
	gitAt(t, bare, "worktree", "add", "-q", "-b", "feature/x", wt)

	got, ok := Identify(wt)
	if !ok {
		t.Fatal("a linked worktree did not resolve to its project")
	}
	if !SamePath(got.Dir, project) {
		t.Errorf("Dir = %q, want the project root %q", got.Dir, project)
	}
	if got.Kind != KindWorktrees || !SamePath(got.GitDir, bare) {
		t.Errorf("resolved to %+v, want the project's bare repository", got)
	}
	if got.Repo != "Chip/salsa" {
		t.Errorf("Repo = %q", got.Repo)
	}
}

// A linked worktree of an ordinary clone resolves to that clone, so adding a
// second worktree to a single checkout does not spawn a phantom project.
func TestIdentifyNormalizesAWorktreeOfASingleCheckout(t *testing.T) {
	requireGit(t)
	main := filepath.Join(t.TempDir(), "docent")
	initSingle(t, main, "git@github.com:KurtPreston/docent.git")
	gitAt(t, main, "commit", "--allow-empty", "-q", "-m", "seed")
	wt := filepath.Join(t.TempDir(), "elsewhere")
	gitAt(t, main, "worktree", "add", "-q", "-b", "side", wt)

	got, ok := Identify(wt)
	if !ok {
		t.Fatal("a worktree of an ordinary clone did not resolve")
	}
	if !SamePath(got.Dir, main) {
		t.Errorf("Dir = %q, want %q", got.Dir, main)
	}
	if got.Kind != KindSingle {
		t.Errorf("Kind = %q, want %q", got.Kind, KindSingle)
	}
}

// A submodule's .git file points under modules/, not worktrees/. It belongs to no
// worktree, and saying so keeps submodules out of the project list.
func TestIdentifyRejectsASubmodulePointer(t *testing.T) {
	dir := t.TempDir()
	gitFile := filepath.Join(dir, ".git")
	content := "gitdir: " + filepath.Join(t.TempDir(), ".git", "modules", "sub") + "\n"
	if err := os.WriteFile(gitFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if p, ok := Identify(dir); ok {
		t.Errorf("a submodule pointer was identified as %+v", p)
	}
}

func TestIdentifyRejectsNonRepositories(t *testing.T) {
	if p, ok := Identify(t.TempDir()); ok {
		t.Errorf("an empty directory was identified as %+v", p)
	}
	if p, ok := Identify(""); ok {
		t.Errorf("an empty path was identified as %+v", p)
	}
	if p, ok := Identify(filepath.Join(t.TempDir(), "nope")); ok {
		t.Errorf("a missing directory was identified as %+v", p)
	}
}

func TestFindProjectWalksUp(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "salsa")
	initBare(t, project, ".base", "git@host:Chip/salsa.git")
	deep := filepath.Join(project, "some-branch", "libs", "ui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, ok := FindProject(deep)
	if !ok {
		t.Fatal("FindProject did not find the project from inside a worktree")
	}
	if !SamePath(got.Dir, project) {
		t.Errorf("Dir = %q, want %q", got.Dir, project)
	}
	if got, ok := FindProject(project); !ok || !SamePath(got.Dir, project) {
		t.Errorf("FindProject on the root itself = %+v, %v", got, ok)
	}
	// Terminating at / rather than looping forever is the thing worth pinning.
	if _, ok := FindProject(t.TempDir()); ok {
		t.Error("found a project where there is none")
	}
	if _, ok := FindProject(""); ok {
		t.Error("empty path resolved to a project")
	}
}

// Both shapes appear in one setup: ~/Code is a directory of projects, and
// ~/Code/salsa is itself a project whose children are worktrees. Discovery has to
// handle a root that is either, and an ordinary clone is now a project too.
func TestDiscoverProjectsFindsRootAndChildren(t *testing.T) {
	code := t.TempDir()
	initBare(t, filepath.Join(code, "salsa"), ".base", "git@host:Chip/salsa.git")
	initBare(t, filepath.Join(code, "gui"), ".base", "https://host/Tango/tango_gui.git")
	initSingle(t, filepath.Join(code, "docent"), "git@github.com:KurtPreston/docent.git")

	got := DiscoverProjects([]string{code})
	if len(got) != 3 {
		t.Fatalf("found %d projects, want 3: %+v", len(got), got)
	}
	byRepo := map[string]Project{}
	for _, p := range got {
		byRepo[p.Repo] = p
	}
	for _, want := range []string{"Chip/salsa", "Tango/tango_gui", "KurtPreston/docent"} {
		if byRepo[want].Dir == "" {
			t.Errorf("repo %q not resolved from origin: %+v", want, got)
		}
	}
	if byRepo["KurtPreston/docent"].Kind != KindSingle {
		t.Errorf("an ordinary clone should be KindSingle: %+v", byRepo["KurtPreston/docent"])
	}

	// Overlapping roots must not double up, and order must be stable.
	twice := DiscoverProjects([]string{code, code, filepath.Join(code, "salsa")})
	if len(twice) != 3 {
		t.Errorf("overlapping roots produced %d projects, want 3", len(twice))
	}
	for i := 1; i < len(twice); i++ {
		if twice[i-1].Dir > twice[i].Dir {
			t.Errorf("results are not sorted: %+v", twice)
		}
	}
	// A project passed directly as a root is found even when its parent is not
	// scanned, which is the ~/Code/salsa code_home shape.
	if got := DiscoverProjects([]string{filepath.Join(code, "salsa")}); len(got) != 1 {
		t.Errorf("project-as-root found %d, want 1", len(got))
	}
	if got := DiscoverProjects([]string{filepath.Join(code, "nope"), ""}); len(got) != 0 {
		t.Errorf("missing roots produced %+v", got)
	}
}

// The regression that comes with accepting any repository as a project: every
// worktree of a project holds a .git file, so a scan of the project directory
// would list each of them beside the project. Normalization has to collapse them.
func TestDiscoverProjectsCollapsesWorktreesIntoTheirProject(t *testing.T) {
	project := filepath.Join(t.TempDir(), "salsa")
	bare := bareCloneInto(t, project, ".base", "git@host:Chip/salsa.git")
	for _, b := range []string{"release-next", "feature-a", "feature-b"} {
		gitAt(t, bare, "worktree", "add", "-q", "-b", b, filepath.Join(project, b))
	}

	// Scanning the project itself as a root is the code_home-per-project shape,
	// and it is where the duplicates would appear.
	got := DiscoverProjects([]string{project})
	if len(got) != 1 {
		t.Fatalf("found %d projects, want just the one: %+v", len(got), got)
	}
	if !SamePath(got[0].Dir, project) {
		t.Errorf("Dir = %q, want %q", got[0].Dir, project)
	}
	if got[0].Repo != "Chip/salsa" {
		t.Errorf("Repo = %q", got[0].Repo)
	}
}

// A project with no origin still exists; it just cannot be matched by repo.
// Dropping it silently would be worse than reporting it with an empty Repo, since
// a caller walking up from a path still wants it.
func TestDiscoverProjectsToleratesMissingOrigin(t *testing.T) {
	code := t.TempDir()
	initBare(t, filepath.Join(code, "orphan"), ".base", "")
	got := DiscoverProjects([]string{code})
	if len(got) != 1 {
		t.Fatalf("found %d, want 1: %+v", len(got), got)
	}
	if got[0].Repo != "" {
		t.Errorf("Repo = %q, want empty", got[0].Repo)
	}
	if p, ok := ProjectForRepo([]string{code}, "Chip/salsa"); ok {
		t.Errorf("an origin-less project matched a repo lookup: %+v", p)
	}
}

// One repo cloned twice is normal (a salsa and a salsa2 for a long experiment).
// Which one an agent lands in must not depend on directory iteration order.
func TestProjectForRepoIsDeterministic(t *testing.T) {
	code := t.TempDir()
	origin := "git@git.drwholdings.com:Chip/salsa.git"
	want := filepath.Join(code, "salsa")
	initBare(t, want, ".base", origin)
	initBare(t, filepath.Join(code, "salsa2"), ".base", origin)
	initBare(t, filepath.Join(code, "aaa-salsa-experiment"), ".base", origin)

	for i := 0; i < 5; i++ {
		got, ok := ProjectForRepo([]string{code}, "Chip/salsa")
		if !ok {
			t.Fatal("no project found")
		}
		if !SamePath(got.Dir, want) {
			t.Fatalf("picked %q, want the name-matching %q", got.Dir, want)
		}
	}
	if _, ok := ProjectForRepo([]string{code}, "chip/SALSA"); !ok {
		t.Error("repo matching is case sensitive")
	}
	if _, ok := ProjectForRepo([]string{code}, "Other/repo"); ok {
		t.Error("matched a repo that is not cloned here")
	}
	if _, ok := ProjectForRepo([]string{code}, ""); ok {
		t.Error("empty repo matched something")
	}
	if _, ok := ProjectForRepo(nil, "Chip/salsa"); ok {
		t.Error("matched with no roots to search")
	}
}

func TestProjectForRepoFallsBackToShortestThenLexical(t *testing.T) {
	code := t.TempDir()
	origin := "git@host:Chip/salsa.git"
	initBare(t, filepath.Join(code, "zzz"), ".base", origin)
	initBare(t, filepath.Join(code, "bbb"), ".base", origin)
	short := filepath.Join(code, "aa")
	initBare(t, short, ".base", origin)

	got, ok := ProjectForRepo([]string{code}, "Chip/salsa")
	if !ok {
		t.Fatal("no project found")
	}
	if !SamePath(got.Dir, short) {
		t.Errorf("picked %q, want the shortest path %q", got.Dir, short)
	}
}

// A picker built from this list must offer one entry per repository, and that
// entry must be the clone an agent would actually land in -- otherwise the two
// options mean the same thing and one of them lies about where work will happen.
func TestUniqueByRepoAgreesWithProjectForRepo(t *testing.T) {
	code := t.TempDir()
	salsa := "git@git.drwholdings.com:Chip/salsa.git"
	want := filepath.Join(code, "salsa")
	initBare(t, want, ".base", salsa)
	initBare(t, filepath.Join(code, "salsa2"), ".base", salsa)
	merlion := filepath.Join(code, "merlion_gui")
	initBare(t, merlion, ".base", "git@host:MerlionJasper/jasper_merlion_gui.git")
	initBare(t, filepath.Join(code, "no-origin"), ".base", "")

	got := UniqueByRepo(DiscoverProjects([]string{code}))
	if len(got) != 2 {
		t.Fatalf("got %d projects, want one per repo: %+v", len(got), got)
	}
	dirs := map[string]string{}
	for _, p := range got {
		if _, dup := dirs[p.Repo]; dup {
			t.Fatalf("repo %s appears twice: %+v", p.Repo, got)
		}
		dirs[p.Repo] = p.Dir
	}
	resolved, ok := ProjectForRepo([]string{code}, "Chip/salsa")
	if !ok {
		t.Fatal("ProjectForRepo found nothing")
	}
	if !SamePath(dirs["Chip/salsa"], resolved.Dir) {
		t.Errorf("picker offers %q but resolution picks %q", dirs["Chip/salsa"], resolved.Dir)
	}
	if !SamePath(dirs["Chip/salsa"], want) {
		t.Errorf("picked %q, want the name-matching %q", dirs["Chip/salsa"], want)
	}
	if !SamePath(dirs["MerlionJasper/jasper_merlion_gui"], merlion) {
		t.Errorf("merlion project = %q, want %q", dirs["MerlionJasper/jasper_merlion_gui"], merlion)
	}
}

func TestUniqueByRepoOfNothing(t *testing.T) {
	if got := UniqueByRepo(nil); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// Origin is read out of the config file rather than by shelling out, which is
// what makes discovery affordable over every repository under a code_home. The
// parser has to survive the shapes git actually writes.
func TestGitConfigAndOriginURL(t *testing.T) {
	dir := t.TempDir()
	cfg := "" +
		"[core]\n" +
		"\trepositoryformatversion = 0\n" +
		"\tbare = true\n" +
		"; a comment line\n" +
		"# another comment\n" +
		"[remote \"origin\"]\n" +
		"\turl = git@git.drwholdings.com:Chip/salsa.git\n" +
		"\tfetch = +refs/heads/*:refs/remotes/origin/*\n" +
		"[branch \"release/next\"]\n" +
		"\tremote = origin\n" +
		"[submodule \"submodules/thing/1.2\"]\n" +
		"\turl = git@host:Other/thing.git\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	got := gitConfig(dir)
	if got["core.bare"] != "true" {
		t.Errorf("core.bare = %q", got["core.bare"])
	}
	if got["remote.origin.url"] != "git@git.drwholdings.com:Chip/salsa.git" {
		t.Errorf("remote.origin.url = %q", got["remote.origin.url"])
	}
	// A submodule's url must not be mistaken for origin's.
	if got["submodule.submodules/thing/1.2.url"] != "git@host:Other/thing.git" {
		t.Errorf("submodule url = %q", got["submodule.submodules/thing/1.2.url"])
	}
	if u := OriginURL(dir); u != "git@git.drwholdings.com:Chip/salsa.git" {
		t.Errorf("OriginURL = %q", u)
	}
	if u := OriginURL(t.TempDir()); u != "" {
		t.Errorf("OriginURL with no config = %q", u)
	}
}

// core.bare is what separates a bare repository from the .git directory of an
// ordinary clone, which has the same HEAD, objects and refs.
func TestIsBareRepo(t *testing.T) {
	project := t.TempDir()
	bare := initBare(t, project, "b", "")
	if !isBareRepo(bare) {
		t.Error("a bare repository was not recognized")
	}
	single := initSingle(t, filepath.Join(t.TempDir(), "s"), "")
	if isBareRepo(filepath.Join(single, ".git")) {
		t.Error("an ordinary clone's .git directory was reported bare")
	}
	if isBareRepo(t.TempDir()) {
		t.Error("an empty directory was reported bare")
	}
}
