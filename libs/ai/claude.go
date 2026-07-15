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

// ClaudeCLIProvider shells out to the Claude Code CLI (`claude`, or Command)
// with a single prompt payload.
//
// Each invocation runs from a freshly created temp directory and uses
// claude's non-interactive `--print` mode with the file-mutating and shell
// tools disabled, so the agent cannot edit files or run commands. The temp
// directory is removed after the call returns. docent only ever asks the
// model to convert the structured activity below into Markdown, so the
// agent should never need to touch the filesystem or execute anything.
type ClaudeCLIProvider struct {
	Command string
	// Formatter shapes how statuses are appended to prompts (defaults to repo-chronological).
	Formatter ActivityFormatter
	// Args, when non-empty, replaces the default flag set passed to
	// claude. The prompt payload is always appended as the final
	// positional argument, so user-supplied Args should NOT include it.
	Args []string
}

func (p ClaudeCLIProvider) formatterOrDefault() ActivityFormatter {
	if p.Formatter != nil {
		return p.Formatter
	}
	return SelectActivityFormatter("")
}

func (p ClaudeCLIProvider) command() string {
	if strings.TrimSpace(p.Command) == "" {
		return "claude"
	}
	return p.Command
}

// defaultClaudeArgs returns the flag set passed to claude when Args is empty.
// `--print` runs non-interactively (the workspace trust dialog is skipped in
// this mode) and `--output-format=text` returns the raw Markdown body.
//
// `--disallowedTools` denies the file-mutating and shell tools so the run is
// effectively read-only, mirroring the safety posture of the cursor provider.
// The values use the `name=value` form (comma-separated) so the variadic flag
// consumes exactly one token and never swallows the trailing prompt argument.
// Users who need different behavior can override the whole set via
// `ai.claude.args` in userdata/config.yaml.
func defaultClaudeArgs() []string {
	return []string{
		"--print",
		"--output-format=text",
		"--disallowedTools=Bash,Edit,Write,MultiEdit,NotebookEdit",
	}
}

// RunMode builds the prompt payload for the resolved mode and shells out to
// claude.
func (p ClaudeCLIProvider) RunMode(ctx context.Context, in RunInput) (string, error) {
	// The `prs` and `daily-plan` reports are fully deterministic; never
	// send them to the model.
	if in.ModeID == prsModeID {
		return RenderPRsMarkdown(in), nil
	}
	if in.ModeID == dailyPlanModeID {
		return RenderDailyPlanMarkdown(in, p.formatterOrDefault()), nil
	}
	formatter := p.formatterOrDefault()
	if needsNested(in.ModeID) {
		formatter = NestRepoChronologicalDepth(formatter)
	}
	payload, err := BuildPrompt(in.Instruction, in, formatter)
	if err != nil {
		return "", err
	}
	return p.runMarkdown(ctx, payload, in.DebugDir, in.StreamOut, in.OnContent, in.OnThinking)
}

func (p ClaudeCLIProvider) runMarkdown(ctx context.Context, payload, debugDir string, streamOut io.Writer, onContent, onThinking func(string)) (string, error) {
	dir, err := os.MkdirTemp("", "docent-claude-")
	if err != nil {
		return "", fmt.Errorf("claude: create temp workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	args := append([]string{}, p.Args...)
	if len(args) == 0 {
		args = defaultClaudeArgs()
	}
	args = append(args, payload)

	redactedArgs := redactCursorArgs(args, payload)
	writeAIDebugLog(debugDir, "claude", "request", map[string]any{
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"command":   p.command(),
		"args":      redactedArgs,
		"cwd":       dir,
		"prompt":    payload,
	})

	cmd := exec.CommandContext(ctx, p.command(), args...)
	cmd.Dir = dir

	var stdout, stderrBuf bytes.Buffer
	cmd.Stdout = teeCallback(&stdout, onContent)
	cmd.Stderr = teeThinking(&stderrBuf, streamOut, onThinking)
	if streamOut != nil {
		fmt.Fprintf(streamOut, "$ %s %s  (cwd=%s)\n",
			p.command(),
			strings.Join(redactedArgs, " "),
			dir,
		)
	}

	runErr := cmd.Run()
	stderr := strings.TrimSpace(stderrBuf.String())
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	stdoutStr := stdout.String()
	if runErr != nil {
		writeAIDebugLog(debugDir, "claude", "error", map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"command":   p.command(),
			"args":      redactedArgs,
			"exit_code": exitCode,
			"error":     runErr.Error(),
			"stdout":    stdoutStr,
			"stderr":    stderr,
		})
		if exit, ok := runErr.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude exited with code %d: %w\nstderr:\n%s",
				exit.ExitCode(), runErr, stderr)
		}
		return "", fmt.Errorf("claude: %w\nstderr:\n%s", runErr, stderr)
	}
	if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
		writeAIDebugLog(debugDir, "claude", "error", map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"command":   p.command(),
			"args":      redactedArgs,
			"exit_code": exitCode,
			"stdout":    stdoutStr,
			"stderr":    stderr,
		})
		return "", fmt.Errorf("claude exited with code %d\nstderr:\n%s",
			cmd.ProcessState.ExitCode(), stderr)
	}

	out := strings.TrimSpace(stdoutStr)
	writeAIDebugLog(debugDir, "claude", "response", map[string]any{
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
		"command":         p.command(),
		"args":            redactedArgs,
		"exit_code":       exitCode,
		"stdout":          stdoutStr,
		"stderr":          stderr,
		"message_content": out,
	})
	if out == "" {
		return "", fmt.Errorf("claude returned no output (exit 0)\nstderr:\n%s", stderr)
	}
	return StripMarkdownFence(out), nil
}
