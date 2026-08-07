package automation_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/worktree"
)

func gitAt(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
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

// remoteRepo is a bare repository with one commit, standing in for the origin
// docent clones from. Real git rather than a stub: provisioning is a sequence of
// git commands, and a stub would let a wrong one pass.
func remoteRepo(t *testing.T) string {
	t.Helper()
	seed := t.TempDir()
	gitAt(t, seed, "init", "--initial-branch=main", "-q", ".")
	gitAt(t, seed, "commit", "--allow-empty", "-q", "-m", "seed")
	parent := t.TempDir()
	remote := filepath.Join(parent, "origin.git")
	gitAt(t, parent, "clone", "--bare", "-q", seed, remote)
	return remote
}

// Worktree mode is the default, since it is what an automation almost always
// wants and the alternative edits a directory the developer may have open.
func TestWorktreeModeProvisionsDocentsOwnDirectory(t *testing.T) {
	state := t.TempDir()
	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		RemoteURL: remoteRepo(t), StateRoot: state,
	})
	if err != nil {
		t.Fatalf("ProvisionWorkdir: %v", err)
	}
	if !res.Owned {
		t.Error("Owned = false; docent's own tree is what the turn-boundary guards key on")
	}
	if !strings.HasPrefix(res.Path, state) {
		t.Errorf("path = %q, want it under the state root %q", res.Path, state)
	}
	if res.ProjectDir == "" {
		t.Error("no project dir reported")
	}
}

// An existing checkout is the developer's. Nothing that keys off Owned -- the
// turn-end commit, the divergence guard -- may touch it.
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
	if res.Owned {
		t.Error("Owned = true for the developer's own checkout")
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

func TestWorktreeModeRequiresABranch(t *testing.T) {
	_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Mode: automation.WorkdirWorktree, Repo: "Chip/salsa",
	})
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("err = %v, want a missing-branch error", err)
	}
}

func TestEmptyModeDefaultsToWorktree(t *testing.T) {
	_, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{})
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("err = %v, want the worktree path's own validation", err)
	}
}

// developerProject is a worktree project the discovery pass can find: a bare
// clone under code/<name>, whose origin is a forge-shaped URL. Repository
// identity is derived from that URL, so a fixture whose origin is a bare
// filesystem path would have no identity and never match a lookup; git's
// insteadOf rewriting keeps the objects on local disk regardless.
func developerProject(t *testing.T, code, name, repo string) string {
	t.Helper()
	seed := remoteRepo(t)
	forge := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	body := "[url \"" + forge + string(filepath.Separator) + "\"]\n\tinsteadOf = git@forge.test:\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)

	published := filepath.Join(forge, filepath.FromSlash(repo)+".git")
	if err := os.MkdirAll(filepath.Dir(published), 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, filepath.Dir(published), "clone", "--bare", "-q", seed, published)

	project := filepath.Join(code, name)
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	gitAt(t, project, "clone", "--bare", "-q", "git@forge.test:"+repo+".git", filepath.Join(project, ".base"))
	return project
}

// A target routes provisioning into the developer's own project, and the result
// says it is not docent's -- which is what keeps the turn-boundary commit and
// the divergence guard out of a directory somebody may have open.
func TestATargetRoutesIntoTheDevelopersProject(t *testing.T) {
	code := t.TempDir()
	project := developerProject(t, code, "salsa", "Chip/salsa")

	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Target: worktree.TargetCreate,
		Roots:  []string{code}, StateRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ProvisionWorkdir: %v", err)
	}
	if res.Owned {
		t.Error("Owned = true for the developer's own project")
	}
	if !strings.HasPrefix(res.Path, project) {
		t.Errorf("path = %q, want it under %q", res.Path, project)
	}
}

// An automation names no target, so it lands in docent's own tree. A rule fires
// unattended, and the placements that touch the developer's repository are only
// ever arrived at by someone choosing them.
func TestNoTargetMeansDocentsOwnTree(t *testing.T) {
	state := t.TempDir()
	code := t.TempDir()
	project := developerProject(t, code, "salsa", "Chip/salsa")

	res, err := automation.ProvisionWorkdir(context.Background(), automation.WorkdirRequest{
		Repo: "Chip/salsa", Branch: "salsa-1/fix",
		Roots: []string{code}, StateRoot: state,
	})
	if err != nil {
		t.Fatalf("ProvisionWorkdir: %v", err)
	}
	if !res.Owned || !strings.HasPrefix(res.Path, state) {
		t.Errorf("path = %q owned = %v, want docent's own tree under %q", res.Path, res.Owned, state)
	}
	if strings.HasPrefix(res.Path, project) {
		t.Error("an automation landed in the developer's project")
	}
}
