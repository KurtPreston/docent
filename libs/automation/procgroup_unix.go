//go:build !windows

package automation

import (
	"os/exec"
	"syscall"
)

// configureProcGroup starts cmd as the leader of its own process group and
// makes context cancellation SIGKILL the entire group (negative PID), so a
// timed-out agent can't leave orphaned grandchildren (yarn/vitest) running.
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
