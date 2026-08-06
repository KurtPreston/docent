//go:build !windows

package agentsession

import (
	"os/exec"
	"syscall"
)

// configureProcGroup makes cmd the leader of its own process group and makes
// context cancellation SIGKILL the whole group (the negative PID), so a
// timed-out turn cannot leave orphaned grandchildren behind.
//
// This matters more for agents than for most subprocesses: an agent's whole job
// is to run build and test commands, so killing only the CLI would leave a yarn
// or vitest tree holding the worktree and burning CPU indefinitely.
func configureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
			return err
		}
		return nil
	}
}
