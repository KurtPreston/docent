//go:build !windows

package grove

import (
	"os/exec"
	"syscall"
)

// configureProcGroup makes cmd the leader of its own process group so a timed-out
// grove invocation takes its git children with it. Without this a `grove path`
// killed while fetching leaves the git process holding the repository lock, and
// every later invocation fails on index.lock until someone notices.
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
