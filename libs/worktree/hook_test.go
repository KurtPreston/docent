package worktree

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// recordingHook writes its environment and working directory to a file, then
// exits with the given code.
func recordingHook(t *testing.T, exitCode string) (script, record string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture hook is a shell script")
	}
	dir := t.TempDir()
	script = filepath.Join(dir, "worktree.sh")
	record = filepath.Join(dir, "record")
	body := "#!/bin/sh\n" +
		"{ echo \"PWD=$PWD\"; env | grep '^DOCENT_'; } > " + record + "\n" +
		"echo 'setup said something' >&2\n" +
		"exit " + exitCode + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, record
}

// writeHook makes an executable hook out of a shell fragment.
func writeHook(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture hook is a shell script")
	}
	script := filepath.Join(t.TempDir(), "worktree.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func recorded(t *testing.T, record string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("hook left no record: %v", err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			out[k] = v
		}
	}
	return out
}

// The hook is the whole of docent's setup story, so what it is told has to be
// enough to do the job: where the checkout is, what it holds, and an existing
// checkout to copy the ignored files from that make it habitable.
func TestResolveRunsTheHookOnCreation(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)
	state := t.TempDir()
	script, record := recordingHook(t, "0")

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "salsa-1/fix", BaseRef: "main",
		Roots: []string{code}, StateRoot: state, Hook: script,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.SetupErr != nil {
		t.Fatalf("SetupErr = %v", res.SetupErr)
	}
	env := recorded(t, record)
	if !SamePath(env["PWD"], res.Dir) {
		t.Errorf("hook ran in %q, want the new worktree %q", env["PWD"], res.Dir)
	}
	if env["DOCENT_WORKTREE_DIR"] != res.Dir {
		t.Errorf("DOCENT_WORKTREE_DIR = %q", env["DOCENT_WORKTREE_DIR"])
	}
	if env["DOCENT_BRANCH"] != "salsa-1/fix" {
		t.Errorf("DOCENT_BRANCH = %q, want the real branch name", env["DOCENT_BRANCH"])
	}
	if env["DOCENT_REPO"] != "Chip/salsa" {
		t.Errorf("DOCENT_REPO = %q", env["DOCENT_REPO"])
	}
	if env["DOCENT_BASE_REF"] != "main" {
		t.Errorf("DOCENT_BASE_REF = %q", env["DOCENT_BASE_REF"])
	}
	if env["DOCENT_WORKTREE_OWNED"] != "1" {
		t.Errorf("DOCENT_WORKTREE_OWNED = %q, want 1 in docent's own tree", env["DOCENT_WORKTREE_OWNED"])
	}
	// The developer's own checkout is where the .env files actually are, which
	// is what makes an isolated clone habitable rather than a bare tree.
	if !SamePath(env["DOCENT_REFERENCE_DIR"], local) {
		t.Errorf("DOCENT_REFERENCE_DIR = %q, want the developer's checkout %q", env["DOCENT_REFERENCE_DIR"], local)
	}
}

// Setup runs once, when the directory is made. A resumed session re-entering its
// worktree must not have its dependencies reinstalled underneath it.
func TestResolveRunsTheHookOnlyOnCreation(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()
	script, record := recordingHook(t, "0")

	req := Request{Repo: "Chip/salsa", Branch: "feature", Roots: []string{code}, StateRoot: state, Hook: script}
	if _, err := Resolve(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the hook ran again for a worktree that already existed")
	}
}

// A checkout with failed setup is still a checkout, and stranding it is worse
// than reporting it -- but the failure has to be visible, with the hook's own
// explanation, or an agent starts in a tree that quietly cannot build.
func TestResolveReportsAFailedHookWithoutFailing(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	f.clone(code, "salsa", remote)
	state := t.TempDir()
	script, _ := recordingHook(t, "3")

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "feature", Roots: []string{code}, StateRoot: state, Hook: script,
	})
	if err != nil {
		t.Fatalf("a failed hook failed the whole provision: %v", err)
	}
	if res.Dir == "" || res.SetupErr == nil {
		t.Fatalf("res = %+v, want a usable directory and a reported setup failure", res)
	}
	if !strings.Contains(res.SetupErr.Error(), "setup said something") {
		t.Errorf("SetupErr = %v, want it to carry the hook's own output", res.SetupErr)
	}
}

func TestRunHookSkipsWhatIsNotThere(t *testing.T) {
	if err := RunHook(context.Background(), HookRequest{Script: ""}); err != nil {
		t.Errorf("no hook configured should be no error, got %v", err)
	}
	missing := filepath.Join(t.TempDir(), "worktree.sh")
	if err := RunHook(context.Background(), HookRequest{Script: missing}); err != nil {
		t.Errorf("a default path nobody wrote should be no error, got %v", err)
	}
}

// A hook that exists but cannot run is a misconfiguration the user needs told
// about, not a silent skip: the checkout would come out unusable either way, and
// only one of those explains itself.
func TestRunHookReportsAnUnusableScript(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "worktree.sh")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RunHook(context.Background(), HookRequest{Script: notExec, Dir: dir}); err == nil ||
		!strings.Contains(err.Error(), "not executable") {
		t.Errorf("err = %v, want it to name the problem", err)
	}
	if err := RunHook(context.Background(), HookRequest{Script: dir, Dir: dir}); err == nil ||
		!strings.Contains(err.Error(), "directory") {
		t.Errorf("err = %v, want it to name the problem", err)
	}
}

// One script has to serve both trees, so it needs to be able to tell them apart.
func TestRunHookMarksWhoOwnsTheDirectory(t *testing.T) {
	script, record := recordingHook(t, "0")
	dir := t.TempDir()
	if err := RunHook(context.Background(), HookRequest{Script: script, Dir: dir, Owned: false}); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, record)["DOCENT_WORKTREE_OWNED"]; got != "0" {
		t.Errorf("DOCENT_WORKTREE_OWNED = %q, want 0 in the developer's own project", got)
	}
}
