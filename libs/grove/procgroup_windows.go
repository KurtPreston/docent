//go:build windows

package grove

import "os/exec"

// configureProcGroup is a no-op on Windows, which has no process-group signal.
// grove runs on the Linux workstation; this exists so the package still builds
// for the Windows launcher tooling.
func configureProcGroup(*exec.Cmd) {}
