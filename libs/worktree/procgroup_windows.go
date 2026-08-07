//go:build windows

package worktree

import "os/exec"

// configureProcGroup is a no-op on Windows, which has no process-group signal.
// The daemon runs on Linux and macOS; this exists so the package still builds
// for the Windows launcher tooling.
func configureProcGroup(*exec.Cmd) {}
