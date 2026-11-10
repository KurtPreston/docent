package automation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/worktree"
)

// defaultAgentTimeout bounds an agent-inline run, agent and post-steps
// together, when its action sets no timeout.
const defaultAgentTimeout = 30 * time.Minute

// agentRunTimeout returns how long one run of action may take: its timeout
// field when set, else defaultAgentTimeout. ValidateRules rejects a value
// that doesn't parse, so falling back to the default here is only a guard.
func agentRunTimeout(action Action) time.Duration {
	if d, err := ParseDuration(action.Timeout); err == nil && d > 0 {
		return d
	}
	return defaultAgentTimeout
}

// worktreeAcquireTimeout bounds how long an agent action waits for a busy
// worktree lock (see worktreelock.go) before giving up. A waiter can't see
// the holder's budget, so it allows twice the longer of its own and the
// default: enough to outlast one full run of a typical holder.
func worktreeAcquireTimeout(runTimeout time.Duration) time.Duration {
	return 2 * max(runTimeout, defaultAgentTimeout)
}

// AgentRunner runs a write-capable coding agent in a provisioned workdir,
// then optionally runs post-steps (validate / commit / push).
type AgentRunner struct {
	// DefaultProvider is used when action.Provider is empty ("cursor",
	// "claude", or "copilot").
	DefaultProvider string
	// CursorCommand / ClaudeCommand / CopilotCommand override the CLI binaries.
	CursorCommand  string
	ClaudeCommand  string
	CopilotCommand string
	// Commenter is used when post.jira_comment is set.
	Commenter IssueCommenter
	// Roots are the directories searched for the developer's own copy of a
	// repository, when the event carries no local path pointing at one. These
	// are the roots local-git already scans.
	Roots []string
	// WorktreeHook is the per-worktree setup script run on a fresh checkout.
	WorktreeHook string
	// WorktreeSparse maps a repository to the directories its worktrees are
	// limited to; see worktree.SparseFor.
	WorktreeSparse map[string][]string
}

func (r AgentRunner) Run(ctx context.Context, action Action, ev Event) error {
	_, err := r.RunWithOutput(ctx, action, ev)
	return err
}

// RunWithOutput runs the agent and post-steps, returning the agent's final
// stdout so later actions in the chain can relay it (e.g. to Slack). The
// output is returned even when a post-step fails.
func (r AgentRunner) RunWithOutput(ctx context.Context, action Action, ev Event) (string, error) {
	actx := EventContext(ev)
	prompt, err := RenderTemplate(action.Prompt, actx)
	if err != nil {
		return "", err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("agent: prompt is empty")
	}

	mode := strings.TrimSpace(action.Workdir)
	if mode == "" {
		mode = WorkdirWorktree
	}
	baseRef, err := RenderTemplate(action.BaseRef, actx)
	if err != nil {
		return "", err
	}
	baseRef = strings.TrimSpace(baseRef)

	// Serialize with any other agent action targeting the same working
	// directory (e.g. a different rule matching the same PR) so they don't run
	// concurrently in it. Two agents sharing a git index corrupt each other, and
	// in open_path mode the damage would be to the developer's real work rather
	// than a scratch directory. Wait on a budget detached from the
	// incoming ctx so time spent blocked here isn't deducted from the run.
	lockKey := worktreeLockKey(mode, actx.Repo, actx.Branch, actx.OpenPath)
	timeout := agentRunTimeout(action)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), worktreeAcquireTimeout(timeout))
	release, err := worktreeLocks.acquire(waitCtx, lockKey)
	cancelWait()
	if err != nil {
		return "", fmt.Errorf("agent: gave up waiting for worktree %q: %w", lockKey, err)
	}
	defer release()

	// Give this run its own full timeout budget starting now, rather than
	// inheriting whatever remains of the caller's deadline (which may have
	// been set at dispatch time and already partly spent waiting for the
	// lock above).
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The worktree outlives the run. In docent's own tree that is what lets a
	// session resume into it later; in open_path mode it is the developer's own
	// checkout and was never docent's to tear down.
	wd, err := ProvisionWorkdir(runCtx, WorkdirRequest{
		Mode:     mode,
		Repo:     actx.Repo,
		Branch:   actx.Branch,
		From:     baseRef,
		OpenPath: actx.OpenPath,
		Roots:    r.Roots,
		Hook:     r.WorktreeHook,
		Sparse:   worktree.SparseFor(r.WorktreeSparse, actx.Repo),
	})
	if err != nil {
		return "", err
	}

	provider := strings.TrimSpace(action.Provider)
	if provider == "" {
		provider = strings.TrimSpace(r.DefaultProvider)
	}
	if provider == "" {
		provider = "cursor"
	}
	cmdName, args, err := r.agentCommand(provider, strings.TrimSpace(action.Model), prompt)
	if err != nil {
		return "", err
	}
	out, err := runAgent(runCtx, agentProcess{Provider: provider, Dir: wd.Path, Name: cmdName, Args: args, Timeout: timeout})
	if err != nil {
		return out, err
	}
	return out, r.runPost(runCtx, action, actx, wd.Path)
}

// agentCommand returns the CLI binary and arguments that run prompt
// unattended with provider. A non-empty model is passed as --model; an empty
// one leaves the arguments exactly as they were before models could be pinned.
func (r AgentRunner) agentCommand(provider, model, prompt string) (string, []string, error) {
	var cmdName string
	var args []string
	switch strings.ToLower(provider) {
	case "cursor":
		cmdName = r.CursorCommand
		if cmdName == "" {
			cmdName = "cursor-agent"
		}
		// Write mode: omit --mode=ask so the agent can edit and run shell.
		args = []string{"-p", "--output-format=text", "--trust", "--force"}
	case "claude":
		cmdName = r.ClaudeCommand
		if cmdName == "" {
			cmdName = "claude"
		}
		// Write mode: omit --disallowedTools so Edit/Write/Bash are available.
		args = []string{"--print", "--output-format=text", "--permission-mode", "acceptEdits"}
	case "copilot":
		cmdName = r.CopilotCommand
		if cmdName == "" {
			cmdName = "copilot"
		}
		// Write mode, unattended: every tool/path/URL is allowed and ask_user is
		// disabled since nobody is there to answer. Text mode prints every
		// assistant message, so read JSONL and keep only the final one.
		args = []string{"--allow-all", "--no-ask-user", "--output-format", "json", "--no-color"}
	default:
		return "", nil, fmt.Errorf("agent: unknown provider %q", provider)
	}

	// Pin the model, then end with the prompt (copilot takes it as -p's value)
	if model != "" {
		args = append(args, "--model", model)
	}
	if strings.EqualFold(provider, "copilot") {
		args = append(args, "-p")
	}
	return cmdName, append(args, prompt), nil
}

// agentProcess is one unattended agent CLI invocation.
type agentProcess struct {
	Provider string
	Dir      string
	Name     string
	Args     []string
	// Timeout is the run's whole budget, which ctx already enforces; it is
	// kept here so a timeout error can say what the limit was.
	Timeout time.Duration
}

// runAgent runs p until it exits or ctx ends, and returns the agent's final
// answer: stdout for providers run in text mode, or the last assistant
// message from copilot's JSONL stream.
func runAgent(ctx context.Context, p agentProcess) (string, error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	provider := p.Provider

	cmd := exec.CommandContext(cctx, p.Name, p.Args...)
	cmd.Dir = p.Dir
	configureProcGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	var combined, stdout, stderr bytes.Buffer
	cmd.Stdout = io.MultiWriter(&combined, &stdout)
	cmd.Stderr = io.MultiWriter(&combined, &stderr)
	runErr := cmd.Run()

	// Reduce copilot's event stream to its final answer, and describe a
	// failure with that answer and stderr rather than the raw stream
	out, detail := stdout.String(), combined.String()
	if strings.EqualFold(provider, "copilot") {
		out = finalCopilotMessage(out)
		detail = strings.TrimSpace(stderr.String()) + "\n" + out
	}
	if runErr == nil {
		return out, nil
	}

	// Report a timeout or a failed exit, keeping whatever the agent said
	detail = strings.TrimSpace(detail)
	if cctx.Err() == context.DeadlineExceeded {
		return out, fmt.Errorf("agent %s timed out after %s (raise the action's timeout to allow longer runs)", provider, p.Timeout)
	}
	if detail != "" {
		return out, fmt.Errorf("agent %s: %w\n%s", provider, runErr, detail)
	}
	return out, fmt.Errorf("agent %s: %w", provider, runErr)
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
