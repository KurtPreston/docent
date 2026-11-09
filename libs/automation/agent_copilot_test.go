package automation_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/model"
)

func TestAgentRunnerCopilot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "copilot")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'final answer'\necho 'noise' >&2\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := automation.AgentRunner{DefaultProvider: "copilot", CopilotCommand: stub}
	out, err := r.RunWithOutput(context.Background(), automation.Action{
		Type:    "agent-inline",
		Workdir: "open_path",
		Prompt:  "do {{.Title}}",
	}, automation.Event{
		Rule:    automation.Rule{ID: "r"},
		Signal:  &model.Signal{Title: "the thing", Fields: map[string]string{"path": dir}},
		FiredAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "final answer" {
		t.Fatalf("output = %q, want stdout only", out)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	if args[len(args)-2] != "-p" || args[len(args)-1] != "do the thing" {
		t.Fatalf("args = %q", args)
	}
	for _, want := range []string{"--allow-all", "--no-ask-user", "--silent"} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("args missing %s: %q", want, args)
		}
	}
}

// An action's timeout replaces the default budget: the agent is killed when
// it runs out, the error names the limit, and output so far is kept.
func TestAgentRunnerActionTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stub")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "copilot")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho 'pushed, waiting on CI'\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Run with a timeout far shorter than the stub's sleep
	r := automation.AgentRunner{DefaultProvider: "copilot", CopilotCommand: stub}
	start := time.Now()
	out, err := r.RunWithOutput(context.Background(), automation.Action{
		Type:    "agent-inline",
		Workdir: "open_path",
		Prompt:  "wait",
		Timeout: "300ms",
	}, automation.Event{
		Rule:    automation.Rule{ID: "r"},
		Signal:  &model.Signal{Fields: map[string]string{"path": dir}},
		FiredAt: time.Now(),
	})

	// It stops at the limit, says so, and keeps what the agent printed
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Fatalf("run took %s, timeout not enforced", elapsed)
	}
	if err == nil || !strings.Contains(err.Error(), "timed out after 300ms") {
		t.Fatalf("err = %v, want a timeout naming the limit", err)
	}
	if !strings.Contains(out, "pushed, waiting on CI") {
		t.Fatalf("output = %q, want the agent's output so far", out)
	}
}
