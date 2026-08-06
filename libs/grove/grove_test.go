package grove

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mkProject builds a directory that looks to grove like a project: a .base bare
// repo with the given origin. Real git, because that is what repoForProject
// reads, and a stub would let a wrong argument order pass.
func mkProject(t *testing.T, dir, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := filepath.Join(dir, BaseDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = base
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--bare", "-q")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
	return dir
}

// fakeGrove writes a stand-in for the grove binary that records its argv and cwd
// and prints the given stdout. The real grove needs a real repository and a
// remote; what needs testing here is the wiring around it.
func fakeGrove(t *testing.T, stdout, stderr string, exitCode int) (bin, argvFile, cwdFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake grove is a shell script")
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-grove")
	argvFile = filepath.Join(dir, "argv")
	cwdFile = filepath.Join(dir, "cwd")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + argvFile + "\n" +
		"pwd > " + cwdFile + "\n"
	if stdout != "" {
		script += "printf '%s' '" + stdout + "'\n"
	}
	if stderr != "" {
		script += "printf '%s' '" + stderr + "' >&2\n"
	}
	script += "exit " + itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile, cwdFile
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

func TestIsProjectAndFindProject(t *testing.T) {
	root := t.TempDir()
	proj := mkProject(t, filepath.Join(root, "salsa"), "git@host:Chip/salsa.git")
	deep := filepath.Join(proj, "some-branch", "libs", "ui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	if !IsProject(proj) {
		t.Error("project root not recognized")
	}
	if IsProject(deep) {
		t.Error("a worktree subdirectory is not a project root")
	}
	if IsProject("") {
		t.Error("empty path reported as a project")
	}

	// The walk up from deep inside a worktree is the common case: docent knows a
	// file path from local-git, and needs the project that owns it.
	got, ok := FindProject(deep)
	if !ok {
		t.Fatal("FindProject did not find the project from inside a worktree")
	}
	if resolve(t, got) != resolve(t, proj) {
		t.Errorf("FindProject = %q, want %q", got, proj)
	}
	if got, ok := FindProject(proj); !ok || resolve(t, got) != resolve(t, proj) {
		t.Errorf("FindProject on the root itself = %q, %v", got, ok)
	}
	// Terminating at / rather than looping forever is the thing worth pinning.
	if _, ok := FindProject(t.TempDir()); ok {
		t.Error("found a project where there is none")
	}
	if _, ok := FindProject(""); ok {
		t.Error("empty path resolved to a project")
	}
}

// TempDir can live under a symlinked /tmp, so compare resolved paths.
func resolve(t *testing.T, p string) string {
	t.Helper()
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// Both layouts appear in one setup: ~/Code is a directory of projects, and
// ~/Code/salsa is itself a project whose children are worktrees. Discovery has
// to handle a root that is either.
func TestDiscoverProjectsFindsRootAndChildren(t *testing.T) {
	code := t.TempDir()
	mkProject(t, filepath.Join(code, "salsa"), "git@host:Chip/salsa.git")
	mkProject(t, filepath.Join(code, "gui"), "https://host/Tango/tango_gui.git")
	// A plain clone with no .base is not a grove project.
	if err := os.MkdirAll(filepath.Join(code, "notgrove", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverProjects([]string{code})
	if len(got) != 2 {
		t.Fatalf("found %d projects, want 2: %+v", len(got), got)
	}
	byRepo := map[string]string{}
	for _, p := range got {
		byRepo[p.Repo] = p.Dir
	}
	if byRepo["Chip/salsa"] == "" || byRepo["Tango/tango_gui"] == "" {
		t.Errorf("repos not resolved from origin: %+v", got)
	}
	// Sorted and deduplicated, so two roots that overlap do not double up.
	twice := DiscoverProjects([]string{code, code, filepath.Join(code, "salsa")})
	if len(twice) != 2 {
		t.Errorf("overlapping roots produced %d projects, want 2", len(twice))
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

// A project with an unreadable or missing origin still exists; it just cannot be
// matched by repo. Dropping it silently would be worse than reporting it with an
// empty Repo, since a caller walking up from a path still wants it.
func TestDiscoverProjectsToleratesMissingOrigin(t *testing.T) {
	code := t.TempDir()
	mkProject(t, filepath.Join(code, "orphan"), "")
	got := DiscoverProjects([]string{code})
	if len(got) != 1 {
		t.Fatalf("found %d, want 1", len(got))
	}
	if got[0].Repo != "" {
		t.Errorf("Repo = %q, want empty", got[0].Repo)
	}
	// And it must not then match every lookup.
	if dir, ok := ProjectForRepo([]string{code}, "Chip/salsa"); ok {
		t.Errorf("an origin-less project matched a repo lookup: %s", dir)
	}
}

// One repo cloned twice is normal (a salsa and a salsa2 for a long experiment).
// Which one an agent lands in must not depend on directory iteration order.
func TestProjectForRepoIsDeterministic(t *testing.T) {
	code := t.TempDir()
	origin := "git@git.drwholdings.com:Chip/salsa.git"
	want := mkProject(t, filepath.Join(code, "salsa"), origin)
	mkProject(t, filepath.Join(code, "salsa2"), origin)
	mkProject(t, filepath.Join(code, "aaa-salsa-experiment"), origin)

	for i := 0; i < 5; i++ {
		got, ok := ProjectForRepo([]string{code}, "Chip/salsa")
		if !ok {
			t.Fatal("no project found")
		}
		if resolve(t, got) != resolve(t, want) {
			t.Fatalf("picked %q, want the name-matching %q", got, want)
		}
	}
	// Case differences in the configured repo key must not lose the match.
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

// When no directory name matches, the choice still has to be stable.
func TestProjectForRepoFallsBackToShortestThenLexical(t *testing.T) {
	code := t.TempDir()
	origin := "git@host:Chip/salsa.git"
	mkProject(t, filepath.Join(code, "zzz"), origin)
	mkProject(t, filepath.Join(code, "bbb"), origin)
	short := mkProject(t, filepath.Join(code, "aa"), origin)
	got, ok := ProjectForRepo([]string{code}, "Chip/salsa")
	if !ok {
		t.Fatal("no project found")
	}
	if resolve(t, got) != resolve(t, short) {
		t.Errorf("picked %q, want the shortest path %q", got, short)
	}
}

// A picker built from this list must offer one entry per repository, and that
// entry must be the clone an agent would actually land in -- otherwise the two
// options mean the same thing and one of them lies about where work will happen.
func TestUniqueByRepoAgreesWithProjectForRepo(t *testing.T) {
	code := t.TempDir()
	salsa := "git@git.drwholdings.com:Chip/salsa.git"
	want := mkProject(t, filepath.Join(code, "salsa"), salsa)
	mkProject(t, filepath.Join(code, "salsa2"), salsa)
	merlion := mkProject(t, filepath.Join(code, "merlion_gui"), "git@host:MerlionJasper/jasper_merlion_gui.git")
	mkProject(t, filepath.Join(code, "no-origin"), "")

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
	if resolve(t, dirs["Chip/salsa"]) != resolve(t, resolved) {
		t.Errorf("picker offers %q but provisioning resolves %q", dirs["Chip/salsa"], resolved)
	}
	if resolve(t, dirs["Chip/salsa"]) != resolve(t, want) {
		t.Errorf("picked %q, want the name-matching %q", dirs["Chip/salsa"], want)
	}
	if resolve(t, dirs["MerlionJasper/jasper_merlion_gui"]) != resolve(t, merlion) {
		t.Errorf("merlion project = %q, want %q", dirs["MerlionJasper/jasper_merlion_gui"], merlion)
	}
}

func TestUniqueByRepoOfNothing(t *testing.T) {
	if got := UniqueByRepo(nil); len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestPathReturnsTheWorktree(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	wt := filepath.Join(proj, "SALSA-1-fix")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	// Real grove writes progress to stderr in color and the path to stdout.
	bin, argvFile, cwdFile := fakeGrove(t, wt+"\n", "\x1b[0;32mFetching latest...\x1b[0m\n", 0)

	got, err := CLI{Command: bin}.Path(context.Background(), proj, "SALSA-1/fix", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != wt {
		t.Errorf("path = %q, want %q", got, wt)
	}
	if argv := read(t, argvFile); argv != "path\nSALSA-1/fix" {
		t.Errorf("argv = %q", argv)
	}
	// grove infers its project from the working directory, so getting cwd wrong
	// silently operates on a different repository.
	if cwd := resolve(t, read(t, cwdFile)); cwd != resolve(t, proj) {
		t.Errorf("cwd = %q, want the project root %q", cwd, proj)
	}
}

// Without --from, and with no TTY to ask, grove bases a new branch on the
// default branch -- right for fresh work, wrong for a backport.
func TestPathPassesFromWhenGiven(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	bin, argvFile, _ := fakeGrove(t, proj+"\n", "", 0)
	if _, err := (CLI{Command: bin}).Path(context.Background(), proj, "backport/x", "release/4.1"); err != nil {
		t.Fatal(err)
	}
	if argv := read(t, argvFile); argv != "path\nbackport/x\n--from\nrelease/4.1" {
		t.Errorf("argv = %q", argv)
	}

	bin2, argvFile2, _ := fakeGrove(t, proj+"\n", "", 0)
	if _, err := (CLI{Command: bin2}).Path(context.Background(), proj, "feature/x", "  "); err != nil {
		t.Fatal(err)
	}
	if argv := read(t, argvFile2); strings.Contains(argv, "--from") {
		t.Errorf("a blank base ref became a --from: %q", argv)
	}
}

// Refusing before spawning matters: run from a non-project directory, grove
// walks up and can find a *different* project, so the agent would silently land
// in the wrong repository.
func TestPathRefusesANonProjectDirectory(t *testing.T) {
	bin, argvFile, _ := fakeGrove(t, "/tmp\n", "", 0)
	dir := t.TempDir()
	_, err := (CLI{Command: bin}).Path(context.Background(), dir, "branch", "")
	if !errors.Is(err, ErrNotAProject) {
		t.Fatalf("err = %v, want ErrNotAProject", err)
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("grove was invoked despite the directory not being a project")
	}
}

func TestPathRefusesAnEmptyBranch(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	bin, argvFile, _ := fakeGrove(t, proj+"\n", "", 0)
	for _, branch := range []string{"", "   ", "/"} {
		if _, err := (CLI{Command: bin}).Path(context.Background(), proj, branch, ""); err == nil {
			t.Errorf("branch %q was accepted", branch)
		}
	}
	if _, statErr := os.Stat(argvFile); statErr == nil {
		t.Error("grove was invoked with no branch")
	}
}

// grove explains itself on stderr and nowhere else, so a failure that drops it
// leaves only an exit status. The color escapes have to go, though: they are
// noise in a log and in a JSON error field.
func TestPathFailureCarriesStderrWithoutEscapes(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	bin, _, _ := fakeGrove(t, "", "\x1b[0;31merror:\x1b[0m /x already exists but is not a worktree\n", 1)
	_, err := (CLI{Command: bin}).Path(context.Background(), proj, "branch", "")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "already exists but is not a worktree") {
		t.Errorf("err = %v, want it to carry grove's explanation", err)
	}
	if strings.Contains(err.Error(), "\x1b[") {
		t.Errorf("err still carries ANSI escapes: %q", err.Error())
	}
}

// A path grove prints but that is not on disk means something went wrong that
// grove did not report as a failure. Returning it would send the agent to a
// directory that does not exist.
func TestPathRejectsAPathThatIsNotThere(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	bin, _, _ := fakeGrove(t, filepath.Join(proj, "never-created")+"\n", "", 0)
	if _, err := (CLI{Command: bin}).Path(context.Background(), proj, "branch", ""); err == nil {
		t.Fatal("want an error for a path that does not exist")
	}

	empty, _, _ := fakeGrove(t, "", "", 0)
	if _, err := (CLI{Command: empty}).Path(context.Background(), proj, "branch", ""); err == nil {
		t.Fatal("want an error when grove prints nothing")
	}
}

// A credential prompt with no TTY is the realistic hang, and a daemon that waits
// forever on it is worse than one that fails.
func TestPathTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	dir := t.TempDir()
	bin := filepath.Join(dir, "hang")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := (CLI{Command: bin, Timeout: 300 * time.Millisecond}).Path(context.Background(), proj, "b", "")
	if err == nil {
		t.Fatal("want a timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want it to name the timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("took %v: the process was not killed", elapsed)
	}
}

func TestWorktreesParsesPorcelain(t *testing.T) {
	proj := mkProject(t, t.TempDir(), "git@host:Chip/salsa.git")
	out := "backport/pr-1-to-4.0\t/home/k/Code/salsa/backport-pr-1-to-4.0\n" +
		"SALSA-2/fix\t/home/k/Code/salsa/SALSA-2-fix\n" +
		"\n" +
		"a line with no tab\n"
	bin, argvFile, _ := fakeGrove(t, out, "", 0)

	wts, err := CLI{Command: bin}.Worktrees(context.Background(), proj)
	if err != nil {
		t.Fatal(err)
	}
	if len(wts) != 2 {
		t.Fatalf("got %d worktrees, want 2: %+v", len(wts), wts)
	}
	if wts[0].Branch != "backport/pr-1-to-4.0" || wts[0].Path != "/home/k/Code/salsa/backport-pr-1-to-4.0" {
		t.Errorf("first = %+v", wts[0])
	}
	// The branch keeps its slashes while the directory has grove's sanitized
	// name; conflating the two is how a lookup by branch silently misses.
	if wts[1].Branch != "SALSA-2/fix" {
		t.Errorf("second branch = %q, want the unsanitized name", wts[1].Branch)
	}
	if argv := read(t, argvFile); argv != "list\n--porcelain" {
		t.Errorf("argv = %q", argv)
	}
}

func TestStripANSI(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"\x1b[0;32mFetching latest...\x1b[0m", "Fetching latest..."},
		{"plain text", "plain text"},
		{"", ""},
		{"\x1b[1mbold\x1b[0m and \x1b[31mred\x1b[0m", "bold and red"},
		// A truncated escape at the end must not spin or panic.
		{"trailing \x1b[", "trailing "},
	} {
		if got := stripANSI(tc.in); got != tc.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLastLine(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"/a/b\n", "/a/b"},
		{"noise\n/a/b\n", "/a/b"},
		{"  /a/b  ", "/a/b"},
		{"\n\n", ""},
		{"", ""},
	} {
		if got := lastLine(tc.in); got != tc.want {
			t.Errorf("lastLine(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
