package ai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// CursorCLIProvider shells out to cursor-agent (or Command) with a single prompt payload.
//
// Each invocation runs from a freshly created temp directory and (by default)
// uses cursor-agent's read-only `ask` mode plus `--sandbox enabled`, so the
// agent cannot edit files or run shell commands. The temp directory is
// removed after the call returns. This is defense in depth: slakkr only ever
// asks the model to convert structured `statuses` JSON into Markdown, so the
// agent should never need to touch the filesystem or execute anything.
type CursorCLIProvider struct {
	Command string
	// Args, when non-empty, replaces the default flag set passed to
	// cursor-agent. The prompt payload is always appended as the final
	// positional argument, so user-supplied Args should NOT include it.
	Args []string
}

func (p CursorCLIProvider) command() string {
	if strings.TrimSpace(p.Command) == "" {
		return "cursor-agent"
	}
	return p.Command
}

// defaultArgs returns the flag set passed to cursor-agent when Args is empty.
// `--mode=ask` is read-only, `--sandbox=enabled` blocks shell tools,
// `--trust` auto-approves the (empty, ephemeral) workspace, and `--force`
// prevents headless hangs on any approval prompt that does slip through.
func defaultCursorArgs() []string {
	return []string{
		"-p",
		"--output-format=text",
		"--mode=ask",
		"--sandbox=enabled",
		"--trust",
		"--force",
	}
}

func (p CursorCLIProvider) GenerateDailyPlan(ctx context.Context, in DailyPlanInput) (string, error) {
	instruction := "Create a practical daily plan as Markdown with sections `## Yesterday` and `## Today`, using `statuses` as ground truth."
	payload, err := BuildDailyPlanPrompt(instruction, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload, in.StreamOut)
}

func (p CursorCLIProvider) SummarizeRecentActivity(ctx context.Context, in RecentActivityInput) (string, error) {
	instruction := fmt.Sprintf(
		"Summarize the developer's recent activity over %d calendar day(s) (%s to %s UTC). Group by project when project_id is present. Use the structured `statuses` JSON below as ground truth. Return one Markdown document.",
		in.LookbackDays,
		in.Since.Format(time.RFC3339),
		in.Now.Format(time.RFC3339),
	)
	payload, err := BuildRecentActivityPrompt(instruction, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload, in.StreamOut)
}

func (p CursorCLIProvider) RunCustomPrompt(ctx context.Context, in CustomPromptInput) (string, error) {
	payload, err := BuildCustomPromptPayload(in.UserPrompt, in)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload, in.StreamOut)
}

func (p CursorCLIProvider) runMarkdown(ctx context.Context, payload string, streamOut io.Writer) (string, error) {
	dir, err := os.MkdirTemp("", "slakkr-cursor-")
	if err != nil {
		return "", fmt.Errorf("cursor-agent: create temp workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	args := append([]string{}, p.Args...)
	if len(args) == 0 {
		args = defaultCursorArgs()
	}
	args = append(args, payload)

	cmd := exec.CommandContext(ctx, p.command(), args...)
	cmd.Dir = dir

	var stdout, stderrBuf bytes.Buffer
	cmd.Stdout = &stdout
	if streamOut != nil {
		cmd.Stderr = io.MultiWriter(&stderrBuf, streamOut)
		fmt.Fprintf(streamOut, "$ %s %s  (cwd=%s)\n",
			p.command(),
			strings.Join(redactCursorArgs(args, payload), " "),
			dir,
		)
	} else {
		cmd.Stderr = &stderrBuf
	}

	runErr := cmd.Run()
	stderr := strings.TrimSpace(stderrBuf.String())
	if runErr != nil {
		if exit, ok := runErr.(*exec.ExitError); ok {
			return "", fmt.Errorf("cursor-agent exited with code %d: %w\nstderr:\n%s",
				exit.ExitCode(), runErr, stderr)
		}
		return "", fmt.Errorf("cursor-agent: %w\nstderr:\n%s", runErr, stderr)
	}
	if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		return "", fmt.Errorf("cursor-agent exited with code %d\nstderr:\n%s",
			cmd.ProcessState.ExitCode(), stderr)
	}

	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return "", fmt.Errorf("cursor-agent returned no output (exit 0)\nstderr:\n%s", stderr)
	}
	return StripMarkdownFence(out), nil
}

// redactCursorArgs replaces the prompt payload with a placeholder so the
// banner printed to stderr stays compact and doesn't echo the full statuses
// JSON (which can be large and may contain repo paths/usernames).
func redactCursorArgs(args []string, payload string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if a == payload {
			out[i] = fmt.Sprintf("<payload %d bytes>", len(payload))
			continue
		}
		out[i] = a
	}
	return out
}
