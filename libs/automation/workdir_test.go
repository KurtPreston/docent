package automation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/grove"
)

// mkGroveProject builds a directory grove would recognize as a project: a .base
// bare repo carrying the given origin.
func mkGroveProject(t *testing.T, dir, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	base := filepath.Join(dir, ".base")
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

// fakeGrove stands in for the grove binary, recording the directory it was run
// in and printing a path back.
func fakeGrove(t *testing.T, printPath string) (cli grove.CLI, cwdFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake grove is a shell script")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-grove")
	cwdFile = filepath.Join(dir, "cwd")
	script := "#!/bin/sh\npwd > " + cwdFile + "\necho '" + printPath + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return grove.CLI{Command: bin}, cwdFile
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func TestOpenPathModeUsesTheDirectoryAsIs(t *testing.T) {
	dir := t.TempDir()
	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Mode: automation.WorkdirOpenPath, OpenPath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != dir {
		t.Errorf("path = %q, want %q", res.Path, dir)
	}
}

func TestOpenPathModeRejectsBadPaths(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"empty":   "",
		"missing": filepath.Join(t.TempDir(), "nope"),
		"a file":  file,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
				Mode: automation.WorkdirOpenPath, OpenPath: path,
			})
			if err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestUnknownModeIsRejected(t *testing.T) {
	_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{Mode: "sandbox"})
	if err == nil || !strings.Contains(err.Error(), "unknown workdir mode") {
		t.Fatalf("err = %v", err)
	}
}

// The open path is a worktree of the project, so walking up from it lands in the
// project the developer's own activity happened in.
func TestWorktreeModeUsesTheProjectContainingOpenPath(t *testing.T) {
	proj := mkGroveProject(t, filepath.Join(t.TempDir(), "salsa"), "git@host:Chip/salsa.git")
	openPath := filepath.Join(proj, "some-branch")
	wt := filepath.Join(proj, "SALSA-1-fix")
	for _, d := range []string{openPath, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cli, cwdFile := fakeGrove(t, wt)

	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Mode: automation.WorkdirWorktree, Repo: "Chip/salsa", Branch: "SALSA-1/fix",
		OpenPath: openPath, Grove: cli,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != wt {
		t.Errorf("path = %q, want %q", res.Path, wt)
	}
	if resolved(t, res.ProjectDir) != resolved(t, proj) {
		t.Errorf("projectDir = %q, want %q", res.ProjectDir, proj)
	}
	b, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved(t, strings.TrimSpace(string(b))); got != resolved(t, proj) {
		t.Errorf("grove ran in %q, want the project root %q", got, proj)
	}
}

// A PR event usually carries no local path, which is exactly when the repo has
// to be matched against the configured roots.
func TestWorktreeModeFindsTheProjectByRepo(t *testing.T) {
	code := t.TempDir()
	proj := mkGroveProject(t, filepath.Join(code, "salsa"), "git@git.drwholdings.com:Chip/salsa.git")
	mkGroveProject(t, filepath.Join(code, "gui"), "git@git.drwholdings.com:Tango/tango_gui.git")
	wt := filepath.Join(proj, "SALSA-2-fix")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	cli, cwdFile := fakeGrove(t, wt)

	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Mode: automation.WorkdirWorktree, Repo: "Chip/salsa", Branch: "SALSA-2/fix",
		GroveRoots: []string{code}, Grove: cli,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != wt {
		t.Errorf("path = %q, want %q", res.Path, wt)
	}
	b, _ := os.ReadFile(cwdFile)
	if got := resolved(t, strings.TrimSpace(string(b))); got != resolved(t, proj) {
		t.Errorf("grove ran in %q, want salsa's project %q", got, proj)
	}
}

// Evidence beats inference: the open path says where this work item actually
// happened, while the repo lookup is a guess among clones of the same repo.
func TestOpenPathWinsOverTheRepoLookup(t *testing.T) {
	code := t.TempDir()
	byRepo := mkGroveProject(t, filepath.Join(code, "salsa"), "git@host:Chip/salsa.git")
	elsewhere := mkGroveProject(t, filepath.Join(t.TempDir(), "salsa-fork"), "git@host:Chip/salsa.git")
	openPath := filepath.Join(elsewhere, "a-worktree")
	if err := os.MkdirAll(openPath, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := automation.ResolveGroveProject(openPath, []string{code}, "Chip/salsa")
	if err != nil {
		t.Fatal(err)
	}
	if resolved(t, got) != resolved(t, elsewhere) {
		t.Errorf("resolved to %q, want the open path's project %q (not %q)", got, elsewhere, byRepo)
	}
}

// Cloning the repo behind the developer's back is what produced the parallel
// universe this replaced, so a missing project is an error that says what to do.
func TestMissingProjectExplainsItself(t *testing.T) {
	code := t.TempDir()
	mkGroveProject(t, filepath.Join(code, "gui"), "git@host:Tango/tango_gui.git")

	_, err := automation.ResolveGroveProject("", []string{code}, "Chip/salsa")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"Chip/salsa", "grove clone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}

	// With no roots configured at all, the fix is a config change, not a clone.
	_, err = automation.ResolveGroveProject("", nil, "Chip/salsa")
	if err == nil || !strings.Contains(err.Error(), "code_home") {
		t.Errorf("err = %v, want it to point at the local-git config", err)
	}

	// With nothing to look up by, say that rather than blaming the config.
	_, err = automation.ResolveGroveProject("", []string{code}, "")
	if err == nil || !strings.Contains(err.Error(), "no repo") {
		t.Errorf("err = %v, want it to name the missing repo", err)
	}
}

func TestWorktreeModeRequiresABranch(t *testing.T) {
	_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Mode: automation.WorkdirWorktree, Repo: "Chip/salsa",
	})
	if err == nil || !strings.Contains(err.Error(), "Branch") {
		t.Fatalf("err = %v, want a missing-branch error", err)
	}
}

// Worktree mode is the default, since it is what an automation almost always
// wants and the alternative edits a directory the developer may have open.
func TestEmptyModeDefaultsToWorktree(t *testing.T) {
	_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{})
	if err == nil || !strings.Contains(err.Error(), "Branch") {
		t.Fatalf("err = %v, want the worktree path's missing-branch error", err)
	}
}
