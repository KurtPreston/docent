//go:build windows

package automation

import "os/exec"

// configureProcGroup is a no-op on Windows (no POSIX process groups); the
// default CommandContext behavior kills only the direct child.
func configureProcGroup(cmd *exec.Cmd) {}
