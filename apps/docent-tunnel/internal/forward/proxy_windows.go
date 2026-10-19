//go:build windows

package forward

import (
	"os"
	"os/exec"
	"syscall"
)

// proxyShellCommand runs a ProxyCommand through cmd.exe. The command line is
// passed verbatim because cmd.exe does not follow Go's argument quoting rules.
func proxyShellCommand(command string) *exec.Cmd {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	cmd := exec.Command(shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /S /C "` + command + `"`}
	return cmd
}
