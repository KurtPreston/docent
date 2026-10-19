//go:build !windows

package forward

import "os/exec"

// proxyShellCommand runs a ProxyCommand through /bin/sh like OpenSSH; exec
// replaces the shell so killing the process stops the proxy itself.
func proxyShellCommand(command string) *exec.Cmd {
	return exec.Command("/bin/sh", "-c", "exec "+command)
}
