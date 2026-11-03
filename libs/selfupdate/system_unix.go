//go:build !windows

package selfupdate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// stopTimeout is how long a process gets to exit after SIGTERM before SIGKILL.
const stopTimeout = 10 * time.Second

func (s realSystem) stop(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}

	// Wait for it to exit, then force it
	deadline := time.Now().Add(stopTimeout)
	for time.Now().Before(deadline) {
		if !s.alive(pid) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (realSystem) alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// start launches l in its own session, detached from this terminal, with
// output appended to its log file.
func (realSystem) start(l Launch) (int, error) {
	out, err := openLog(l.LogPath)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	cmd := exec.Command(l.Args[0], l.Args[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = l.Env
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid

	// Reap it if it exits while this process is still around, so alive()
	// doesn't see a zombie
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

func openLog(path string) (*os.File, error) {
	if path == "" {
		return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}
