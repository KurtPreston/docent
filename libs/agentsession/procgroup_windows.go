//go:build windows

package agentsession

import "os/exec"

// configureProcGroup is a no-op on Windows, where there is no process-group
// signal to send. exec.CommandContext still kills the immediate child on
// cancellation; grandchildren would need a Job Object, which docent does not
// need today because agents run on the Linux workstation.
func configureProcGroup(*exec.Cmd) {}
