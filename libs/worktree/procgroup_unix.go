//go:build !windows

package worktree

import (
	"os/exec"
	"syscall"
)

// configureProcGroup makes cmd the leader of its own process group so a timed-out
// hook takes its children with it. Without this a setup script killed mid-install
// leaves the package manager or git process holding a lock, and every later
// attempt fails on it until someone notices.
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
