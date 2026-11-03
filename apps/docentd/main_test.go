package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestRemovedCommandsRejectedBeforeConfig(t *testing.T) {
	if command := os.Getenv("DOCENT_TEST_COMMAND"); command != "" {
		serve([]string{"-config", "/missing/unreadable/docentd.yaml", command})
		return
	}
	for _, command := range []string{"version", "doctor", "install-hooks", "update", "unknown"} {
		t.Run(command, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRemovedCommandsRejectedBeforeConfig$")
			cmd.Env = append(os.Environ(), "DOCENT_TEST_COMMAND="+command)
			out, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "use `docent "+command+"`") {
				t.Fatalf("removed command wasn't rejected: %v %s", err, out)
			}
		})
	}
}
