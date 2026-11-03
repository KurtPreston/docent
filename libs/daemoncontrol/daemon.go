// Package daemoncontrol inspects and controls the current user's local daemon.
// Callers serialize mutations with WithLock for the entire operation, including
// binary replacement when updating.
package daemoncontrol

import (
	"fmt"
	"path/filepath"
	"strconv"
)

type Manager string

const (
	ManagerSystemd Manager = "systemd"
	ManagerLaunchd Manager = "launchd"
	ManagerProcess Manager = "process"
	ManagerNone    Manager = "none"
	SystemdUnit            = "docentd.service"
	LaunchdLabel           = "com.docent.docentd"
	TunnelLabel            = "com.docent.docent-tunnel"
	systemdRunDir          = "/run/systemd/system"
)

type Daemon struct {
	Binary      string
	Installed   bool
	Running     bool
	Manager     Manager
	PIDs        []int
	Launch      Launch
	TunnelAgent bool
	launchError error
	loaded      bool
	plist       string
}

type Launch struct {
	Args    []string
	Dir     string
	Env     []string
	LogPath string
}

type StartOptions struct {
	Binary         string
	ConfigDir      string
	Home           string
	ConfigOverride bool // explicitly requested, not a default
}

func (d Daemon) Describe() string {
	state := "stopped"
	if d.Running {
		state = "running"
	} else if !d.Installed {
		state = "not installed"
	}
	return fmt.Sprintf("%s (manager: %s%s)", state, d.Manager, d.pidDescription())
}

func (d Daemon) pidDescription() string {
	if len(d.PIDs) == 0 {
		return ""
	}
	return "; " + joinPIDs(d.PIDs)
}

func (d Daemon) restartable() error {
	if d.Manager != ManagerProcess {
		return nil
	}
	if sys.goos() != "linux" {
		return fmt.Errorf("cannot safely relaunch an unmanaged daemon on %s; stop/start it manually or install a service; use --restart=never for updates", sys.goos())
	}
	if d.launchError != nil {
		return fmt.Errorf("cannot preserve daemon launch: %w; use --restart=never for updates", d.launchError)
	}
	return nil
}

func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	return a == b
}

func launchdTarget(label string) string {
	return "gui/" + strconv.Itoa(sys.uid()) + "/" + label
}

// Exclude legacy one-shot commands while an older daemon is being migrated.
func isSubcommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "update", "doctor", "install-hooks", "version", "-version", "--version", "-v", "-h", "--help":
		return true
	}
	return false
}
