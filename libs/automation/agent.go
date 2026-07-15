package automation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const agentTimeout = 30 * time.Minute

// worktreeAcquireTimeout bounds how long an agent action waits for a busy
// worktree lock (see worktreelock.go) before giving up. Sized to comfortably
// outlast one full run of whatever job is currently holding the lock (at
// most agentTimeout for the agent itself, plus room for post-steps).
const worktreeAcquireTimeout = 2 * agentTimeout

// AgentRunner runs a write-capable coding agent in a provisioned workdir,
// then optionally runs post-steps (validate / commit / push).
type AgentRunner struct {
	// DefaultProvider is used when action.Provider is empty ("cursor" or "claude").
	DefaultProvider string
	// CursorCommand / ClaudeCommand override the CLI binaries.
	CursorCommand string
	ClaudeCommand string
	// ResolveRemote looks up a git remote URL when EventContext has OpenPath
	// but no RemoteURL. Optional.
	ResolveRemote func(ctx context.Context, openPath string) (string, error)
	// Commenter is used when post.jira_comment is set.
	Commenter IssueCommenter
	// StateDir roots the docent-owned clones/worktrees (worktree mode). Empty
	// falls back to docentconfig.StateDir(); set it so the worktree location
	// matches the queue's state dir.
	StateDir string
}

func (r AgentRunner) Run(ctx context.Context, action Action, ev Event) error {
	actx := EventContext(ev)
	prompt, err := RenderTemplate(action.Prompt, actx)
	if err != nil {
		return err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return fmt.Errorf("agent: prompt is empty")
	}

	mode := strings.TrimSpace(action.Workdir)
	if mode == "" {
		mode = WorkdirWorktree
	}
	remote := ""
	if mode == WorkdirWorktree {
		if actx.OpenPath != "" && r.ResolveRemote != nil {
			remote, _ = r.ResolveRemote(ctx, actx.OpenPath)
		}
		if remote == "" && actx.Repo != "" {
			// Build an HTTPS clone URL from the PR's host (carried in the
			// entity/signal fields) so enterprise repos resolve correctly.
			// HTTPS lets `gh` act as the git credential helper, avoiding a
			// dependency on SSH keys in the daemon's environment. Defaults to
			// github.com when no host is present.
			host := strings.TrimSpace(actx.Fields["host"])
			if host == "" {
				host = "github.com"
			}
			remote = "https://" + host + "/" + actx.Repo + ".git"
		}
		if remote == "" {
			return fmt.Errorf("agent: cannot resolve remote URL for worktree (need OpenPath or Repo)")
		}
	}

	// Serialize with any other agent action targeting the same working
	// directory (e.g. a different rule matching the same PR) so they don't
	// provision/reset/clean up the same worktree concurrently. Wait on a
	// budget detached from the incoming ctx so time spent blocked here isn't
	// deducted from the run itself.
	lockKey := worktreeLockKey(mode, actx.Repo, actx.Branch, actx.OpenPath)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), worktreeAcquireTimeout)
	release, err := worktreeLocks.acquire(waitCtx, lockKey)
	cancelWait()
	if err != nil {
		return fmt.Errorf("agent: gave up waiting for worktree %q: %w", lockKey, err)
	}
	defer release()

	// Give this run its own full timeout budget starting now, rather than
	// inheriting whatever remains of the caller's deadline (which may have
	// been set at dispatch time and already partly spent waiting for the
	// lock above).
	runCtx, cancel := context.WithTimeout(context.Background(), agentTimeout)
	defer cancel()

	wd, err := ProvisionWorkdir(runCtx, WorkdirRequest{
		Mode:      mode,
		Repo:      actx.Repo,
		Branch:    actx.Branch,
		RemoteURL: remote,
		OpenPath:  actx.OpenPath,
		StateDir:  r.StateDir,
	})
	if err != nil {
		return err
	}
	defer func() {
		if wd.Cleanup != nil && action.Post["keep_workdir"] != "true" {
			_ = wd.Cleanup()
		}
	}()

	provider := strings.TrimSpace(action.Provider)
	if provider == "" {
		provider = strings.TrimSpace(r.DefaultProvider)
	}
	if provider == "" {
		provider = "cursor"
	}
	if err := r.runAgent(runCtx, provider, wd.Path, prompt); err != nil {
		return err
	}
	return r.runPost(runCtx, action, actx, wd.Path)
}

func (r AgentRunner) runAgent(ctx context.Context, provider, cwd, prompt string) error {
	cctx, cancel := context.WithTimeout(ctx, agentTimeout)
	defer cancel()

	var cmdName string
	var args []string
	switch strings.ToLower(provider) {
	case "cursor":
		cmdName = r.CursorCommand
		if cmdName == "" {
			cmdName = "cursor-agent"
		}
		// Write mode: omit --mode=ask so the agent can edit and run shell.
		args = []string{"-p", "--output-format=text", "--trust", "--force", prompt}
	case "claude":
		cmdName = r.ClaudeCommand
		if cmdName == "" {
			cmdName = "claude"
		}
		// Write mode: omit --disallowedTools so Edit/Write/Bash are available.
		args = []string{"--print", "--output-format=text", "--permission-mode", "acceptEdits", prompt}
	default:
		return fmt.Errorf("agent: unknown provider %q", provider)
	}

	cmd := exec.CommandContext(cctx, cmdName, args...)
	cmd.Dir = cwd
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		out := strings.TrimSpace(combined.String())
		if cctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("agent %s timed out", provider)
		}
		if out != "" {
			return fmt.Errorf("agent %s: %w\n%s", provider, err, out)
		}
		return fmt.Errorf("agent %s: %w", provider, err)
	}
	return nil
}

func (r AgentRunner) runPost(ctx context.Context, action Action, actx Context, cwd string) error {
	post := action.Post
	if len(post) == 0 {
		return nil
	}
	if v := post["validate"]; v != "" {
		// Comma-separated commands, or a single command string.
		for _, raw := range splitCommands(v) {
			if err := runInDir(ctx, cwd, raw); err != nil {
				return fmt.Errorf("post validate %q: %w", raw, err)
			}
		}
	}
	if post["commit"] == "true" || post["commit"] == "1" {
		msg := post["commit_message"]
		if msg == "" {
			msg = fmt.Sprintf("docent-automation: %s", actx.RuleID)
		}
		_ = runGit(ctx, cwd, "add", "-A")
		if err := runGit(ctx, cwd, "commit", "-m", msg); err != nil {
			// Nothing to commit is OK.
			if !strings.Contains(err.Error(), "nothing to commit") {
				return fmt.Errorf("post commit: %w", err)
			}
		}
	}
	if post["push"] == "true" || post["push"] == "1" {
		branch := actx.Branch
		if branch == "" {
			branch = "HEAD"
		}
		if err := runGit(ctx, cwd, "push", "-u", "origin", "HEAD:"+branch); err != nil {
			return fmt.Errorf("post push: %w", err)
		}
	}
	if post["jira_comment"] == "true" || post["jira_comment"] == "1" {
		if r.Commenter == nil {
			return fmt.Errorf("post jira_comment: no commenter configured")
		}
		body := post["jira_comment_body"]
		if body == "" {
			body = fmt.Sprintf("Docent automation %s completed for %s.", actx.RuleID, actx.Title)
		}
		rendered, err := RenderTemplate(body, actx)
		if err != nil {
			return err
		}
		issue := actx.Ticket.Key
		if issue == "" {
			return fmt.Errorf("post jira_comment: no ticket key")
		}
		if err := r.Commenter.PostComment(ctx, issue, rendered); err != nil {
			return fmt.Errorf("post jira_comment: %w", err)
		}
	}
	return nil
}

func splitCommands(v string) []string {
	// Prefer JSON-ish list via | separator for simplicity in map[string]string post.
	parts := strings.Split(v, "|")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func runInDir(ctx context.Context, cwd, cmdline string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
