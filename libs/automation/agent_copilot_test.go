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
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	r := automation.AgentRunner{DefaultProvider: "copilot", CopilotCommand: stub}
	err := r.Run(context.Background(), automation.Action{
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
