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

// CopilotCLIProvider shells out to the GitHub Copilot CLI (`copilot`, or
// Command) with a single prompt payload.
//
// Each invocation runs from a freshly created temp directory and uses
// copilot's non-interactive `-p` mode with the shell and file-write tools
// denied, so the agent cannot edit files or run commands. The temp directory
// is removed after the call returns. docent only ever asks the model to
// convert the structured activity below into Markdown, so the agent should
// never need to touch the filesystem or execute anything.
type CopilotCLIProvider struct {
	Command string
	// Formatter shapes how statuses are appended to prompts (defaults to repo-chronological).
	Formatter ActivityFormatter
	// Args, when non-empty, replaces the default flag set passed to copilot.
	// `-p <payload>` is always appended last, so user-supplied Args should NOT
	// include `-p`/`--prompt`.
	Args []string
}

func (p CopilotCLIProvider) formatterOrDefault() ActivityFormatter {
	if p.Formatter != nil {
		return p.Formatter
	}
	return SelectActivityFormatter("")
}

func (p CopilotCLIProvider) command() string {
	if strings.TrimSpace(p.Command) == "" {
		return "copilot"
	}
	return p.Command
}

// defaultCopilotArgs returns the flag set passed to copilot when Args is empty.
// `--silent` prints only the agent response (no usage stats), and
// `--deny-tool=shell,write` makes the run effectively read-only, mirroring the
// cursor and claude providers. Repo instructions, the ask_user tool, and the
// built-in MCP servers are disabled because the run happens in an empty temp
// directory with no human attached. Users who need different behavior can
// override the whole set via `ai.copilot.args` in userdata/config.yaml.
func defaultCopilotArgs() []string {
	return []string{
		"--silent",
		"--no-color",
		"--no-custom-instructions",
		"--no-ask-user",
		"--disable-builtin-mcps",
		"--deny-tool=shell,write",
	}
}

// RunMode builds the prompt payload for the resolved mode and shells out to
// copilot.
func (p CopilotCLIProvider) RunMode(ctx context.Context, in RunInput) (string, error) {
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

func (p CopilotCLIProvider) runMarkdown(ctx context.Context, payload, debugDir string, streamOut io.Writer, onContent, onThinking func(string)) (string, error) {
	dir, err := os.MkdirTemp("", "docent-copilot-")
	if err != nil {
		return "", fmt.Errorf("copilot: create temp workspace: %w", err)
	}
	defer os.RemoveAll(dir)

	args := append([]string{}, p.Args...)
	if len(args) == 0 {
		args = defaultCopilotArgs()
	}
	args = append(args, "-p", payload)

	redactedArgs := redactCursorArgs(args, payload)
	writeAIDebugLog(debugDir, "copilot", "request", map[string]any{
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
		writeAIDebugLog(debugDir, "copilot", "error", map[string]any{
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"command":   p.command(),
			"args":      redactedArgs,
			"exit_code": exitCode,
			"error":     runErr.Error(),
			"stdout":    stdoutStr,
			"stderr":    stderr,
		})
		if exit, ok := runErr.(*exec.ExitError); ok {
			return "", fmt.Errorf("copilot exited with code %d: %w\nstderr:\n%s",
				exit.ExitCode(), runErr, stderr)
		}
		return "", fmt.Errorf("copilot: %w\nstderr:\n%s", runErr, stderr)
	}

	out := strings.TrimSpace(stdoutStr)
	writeAIDebugLog(debugDir, "copilot", "response", map[string]any{
		"timestamp":       time.Now().UTC().Format(time.RFC3339Nano),
		"command":         p.command(),
		"args":            redactedArgs,
		"exit_code":       exitCode,
		"stdout":          stdoutStr,
		"stderr":          stderr,
		"message_content": out,
	})
	if out == "" {
		return "", fmt.Errorf("copilot returned no output (exit 0)\nstderr:\n%s", stderr)
	}
	return StripMarkdownFence(out), nil
}
