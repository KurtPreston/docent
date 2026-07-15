package automation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/automation"
)

// TestProvisionWorkdirHealsCorruptedWorktree reproduces the SALSA-12529
// incident shape: a worktree directory that still exists on disk but is no
// longer a valid git worktree (its .git is gone, and stray files were left
// behind). The previous behavior took the reuse path and failed the run with
// "fatal: not a git repository". ProvisionWorkdir must now discard the
// corrupted leftover and rebuild it instead.
func TestProvisionWorkdirHealsCorruptedWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	root := t.TempDir()

	// A "remote" repo with a single commit on test-branch. file:// forces the
	// smart transport so the blob:none partial clone in ensureBareClone is
	// honored (allowFilter avoids a fallback warning).
	remote := filepath.Join(root, "remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitT(t, remote, "init", "-q")
	runGitT(t, remote, "config", "uploadpack.allowFilter", "true")
	runGitT(t, remote, "checkout", "-q", "-b", "test-branch")
	if err := os.WriteFile(filepath.Join(remote, "hello.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitT(t, remote, "add", "-A")
	runGitT(t, remote,
		"-c", "user.email=test@example.com",
		"-c", "user.name=test",
		"-c", "commit.gpgsign=false",
		"commit", "-q", "-m", "initial")

	req := automation.WorkdirRequest{
		Mode:      automation.WorkdirWorktree,
		Repo:      "owner/repo",
		Branch:    "test-branch",
		RemoteURL: "file://" + remote,
		StateDir:  filepath.Join(root, "state"),
	}

	// First provision: produces a valid worktree.
	res, err := automation.ProvisionWorkdir(ctx, req)
	if err != nil {
		t.Fatalf("first ProvisionWorkdir: %v", err)
	}
	if !isInsideWorkTree(res.Path) {
		t.Fatalf("first provision did not produce a valid worktree at %s", res.Path)
	}

	// Corrupt it: remove .git and leave stray files behind, mirroring the
	// orphaned vitest cache that survived a prior run's cleanup.
	if err := os.RemoveAll(filepath.Join(res.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(res.Path, "libs", "node_modules", "stray.txt")
	if err := os.MkdirAll(filepath.Dir(stray), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isInsideWorkTree(res.Path) {
		t.Fatal("expected corrupted dir to be an invalid worktree before re-provision")
	}

	// Second provision with the same request must self-heal, not fail.
	res2, err := automation.ProvisionWorkdir(ctx, req)
	if err != nil {
		t.Fatalf("second ProvisionWorkdir should self-heal, got: %v", err)
	}
	if !isInsideWorkTree(res2.Path) {
		t.Fatalf("healed worktree is not valid at %s", res2.Path)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("stray file should have been removed during heal (stat err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(res2.Path, "hello.txt")); err != nil {
		t.Fatalf("healed worktree missing the committed file: %v", err)
	}
}

func runGitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func isInsideWorkTree(path string) bool {
	out, err := exec.Command("git", "-C", path, "rev-parse", "--is-inside-work-tree").Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}
