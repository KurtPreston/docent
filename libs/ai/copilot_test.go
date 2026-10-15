package ai

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

func TestSelectProviderCopilot(t *testing.T) {
	p := SelectProvider(userdata.AIConfig{Provider: "Copilot"}, nil)
	cp, ok := p.(CopilotCLIProvider)
	if !ok {
		t.Fatalf("expected CopilotCLIProvider, got %T", p)
	}
	if cp.Command != "copilot" {
		t.Fatalf("expected default command copilot, got %q", cp.Command)
	}
	if _, ok := WithFormatter(p, RepoChronologicalFormatter{}).(CopilotCLIProvider); !ok {
		t.Fatal("WithFormatter should preserve CopilotCLIProvider")
	}
}

// fakeCopilot writes a script that records its argv (one per line) and echoes
// a fenced markdown body, and returns the script path and argv file.
func fakeCopilot(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stub requires a POSIX shell")
	}
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "copilot")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argvFile + "'; done\nprintf '```markdown\\n# Report\\n```\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func TestCopilotRunModePassesPromptAfterP(t *testing.T) {
	bin, argvFile := fakeCopilot(t)
	out, err := CopilotCLIProvider{Command: bin}.RunMode(context.Background(), RunInput{
		ModeID:      "custom-prompt",
		Instruction: "Summarize my day.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out != "# Report" {
		t.Fatalf("expected fence-stripped output, got %q", out)
	}
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	for _, want := range []string{"--silent", "--no-custom-instructions", "--deny-tool=shell,write"} {
		if !strings.Contains(argv, want+"\n") {
			t.Errorf("missing default arg %q in argv:\n%s", want, argv)
		}
	}
	idx := strings.Index(argv, "\n-p\n")
	if idx < 0 {
		t.Fatalf("expected -p flag in argv:\n%s", argv)
	}
	if !strings.Contains(argv[idx:], "Summarize my day.") {
		t.Fatalf("expected prompt payload after -p:\n%s", argv)
	}
}

func TestCopilotCustomArgsReplaceDefaults(t *testing.T) {
	bin, argvFile := fakeCopilot(t)
	_, err := CopilotCLIProvider{Command: bin, Args: []string{"--model", "gpt-5"}}.RunMode(context.Background(), RunInput{
		ModeID:      "custom-prompt",
		Instruction: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(argvFile)
	lines := strings.SplitN(string(raw), "\n", 4)
	if lines[0] != "--model" || lines[1] != "gpt-5" || lines[2] != "-p" {
		t.Fatalf("expected custom args followed by -p, got %q", lines[:3])
	}
}
