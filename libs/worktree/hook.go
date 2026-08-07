package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// HookTimeout bounds one setup hook. Generous because installing dependencies
// into a fresh checkout is genuinely slow; bounded because a hook that prompts
// for something would otherwise hang the daemon forever.
const HookTimeout = 15 * time.Minute

// HookRequest is a freshly created working directory handed to the setup hook.
type HookRequest struct {
	// Script is the hook to run. Empty, or a path that does not exist, means no
	// setup: not every setup needs one, and a configured default that was never
	// written should not fail a checkout.
	Script string
	// Dir is the directory that was just created.
	Dir string
	// Branch is the branch checked out in it.
	Branch string
	// Repo is the host-relative repository identity.
	Repo string
	// ProjectDir is the root the directory was created under.
	ProjectDir string
	// BaseRef is the ref a brand-new branch was based on, empty otherwise.
	BaseRef string
	// Reference is an existing checkout of the same repository to copy ignored
	// files from, or "" when there is none.
	Reference string
	// Owned reports whether this is docent's own directory, so one script can
	// behave differently in a tree the developer also uses.
	Owned bool
}

// RunHook runs the per-worktree setup hook.
//
// This is the whole of docent's setup story, in every tree it creates. Making a
// fresh checkout usable -- the .env files, the installed dependencies, the editor
// colour, whatever else a particular repository needs -- is not something docent
// can know, and the previous attempt to know it consisted of delegating creation
// wholesale to a tool that did. A hook keeps placement in git, where every tool
// can see it, and puts the repository-specific part where the person who knows
// the repository can write it.
//
// It carries more weight in docent's own tree than in the developer's project,
// because a project's setup is often not in the repository at all: on a machine
// where the scripts that lay a project out are tracked by a different repository
// living inside the project root, a clone of the code inherits none of it.
func RunHook(ctx context.Context, req HookRequest) error {
	script := strings.TrimSpace(req.Script)
	if script == "" {
		return nil
	}
	info, err := os.Stat(script)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("worktree hook %s: %w", script, err)
	}
	if info.IsDir() {
		return fmt.Errorf("worktree hook %s is a directory", script)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("worktree hook %s is not executable", script)
	}

	cctx, cancel := context.WithTimeout(ctx, HookTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, script)
	cmd.Dir = req.Dir
	cmd.Env = append(os.Environ(), req.env()...)
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	// A hook that shells out to an installer must not survive its own timeout:
	// a killed parent leaving a package manager holding a lock is how the next
	// checkout fails for a reason nobody can trace back to here.
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second

	if err := cmd.Run(); err != nil {
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("worktree hook %s timed out after %s%s", script, HookTimeout, detail(out.String()))
		}
		return fmt.Errorf("worktree hook %s: %w%s", script, err, detail(out.String()))
	}
	return nil
}

func (r HookRequest) env() []string {
	owned := "0"
	if r.Owned {
		owned = "1"
	}
	pairs := []struct{ k, v string }{
		{"DOCENT_WORKTREE_DIR", r.Dir},
		{"DOCENT_BRANCH", r.Branch},
		{"DOCENT_REPO", r.Repo},
		{"DOCENT_PROJECT_DIR", r.ProjectDir},
		{"DOCENT_BASE_REF", r.BaseRef},
		{"DOCENT_REFERENCE_DIR", r.Reference},
		{"DOCENT_WORKTREE_OWNED", owned},
	}
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		if p.v == "" {
			continue
		}
		out = append(out, p.k+"="+p.v)
	}
	return out
}
