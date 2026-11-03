package daemoncontrol

import (
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
)

var startupGrace = 1500 * time.Millisecond

func Start(log io.Writer, opts StartOptions) error {
	d, err := inspect(opts)
	if err != nil {
		return err
	}
	if opts.ConfigOverride && (d.Running || d.Manager != ManagerNone) {
		return fmt.Errorf("--config-dir is only valid for a new unmanaged daemon; installed services and running daemons keep their configuration")
	}
	if d.Running {
		fmt.Fprintln(log, "docentd is already "+d.Describe())
		return nil
	}
	if !d.Installed {
		return fmt.Errorf("local daemon is not installed beside this CLI; run the local/server installer first")
	}
	switch d.Manager {
	case ManagerSystemd:
		err = runLogged(log, "systemctl", "--user", "start", SystemdUnit)
	case ManagerLaunchd:
		if d.loaded {
			err = runLogged(log, "launchctl", "kickstart", launchdTarget(LaunchdLabel))
		} else {
			err = runLogged(log, "launchctl", "bootstrap", "gui/"+strconv.Itoa(sys.uid()), d.plist)
		}
	default:
		if sys.goos() != "linux" {
			return fmt.Errorf("install a LaunchAgent before starting docentd on macOS")
		}
		if opts.ConfigDir == "" {
			opts.ConfigDir = docentconfig.DefaultDir()
		}
		opts.ConfigDir, err = filepath.Abs(opts.ConfigDir)
		if err != nil {
			return err
		}
		return launch(log, Launch{
			Args: []string{opts.Binary, "-config", filepath.Join(opts.ConfigDir, "docentd.yaml")},
			Dir:  opts.ConfigDir, LogPath: filepath.Join(opts.ConfigDir, "docentd.log"),
		})
	}
	if err != nil {
		return err
	}
	return waitReady(log, opts.Binary)
}

func Stop(log io.Writer, binary string) error {
	d, err := Inspect(binary)
	if err != nil {
		return err
	}
	// Stop loaded managers even without a child, so KeepAlive/restart policies
	// cannot bring the daemon back between inspection and the stop request.
	switch d.Manager {
	case ManagerSystemd:
		err = runLogged(log, "systemctl", "--user", "stop", SystemdUnit)
	case ManagerLaunchd:
		if d.loaded {
			err = runLogged(log, "launchctl", "bootout", launchdTarget(LaunchdLabel))
		}
	case ManagerProcess:
		err = sys.stop(d.PIDs[0])
	}
	if err != nil {
		return err
	}
	var last error
	for i := 0; i < 50; i++ {
		now, err := Inspect(binary)
		last = err
		if err == nil && !now.Running {
			fmt.Fprintln(log, "docentd is stopped")
			return nil
		}
		sys.sleep(100 * time.Millisecond)
	}
	if last != nil {
		return fmt.Errorf("cannot confirm stopped daemon: %w", last)
	}
	return fmt.Errorf("docentd did not stop")
}

func Restart(log io.Writer, opts StartOptions) error {
	d, err := inspect(opts)
	if err != nil {
		return err
	}
	if !d.Running {
		return Start(log, opts)
	}
	if opts.ConfigOverride {
		return fmt.Errorf("--config-dir cannot override a running daemon; stop it first")
	}
	return d.Restart(log, false)
}

func (d Daemon) Restart(log io.Writer, tunnelUpdated bool) error {
	if d.Binary != "" {
		current, err := Inspect(d.Binary)
		if err != nil {
			return err
		}
		if !current.Running || current.Manager != d.Manager || joinPIDs(current.PIDs) != joinPIDs(d.PIDs) {
			return fmt.Errorf("daemon identity changed since inspection; refusing to restart")
		}
	}
	if err := d.restartable(); err != nil {
		return err
	}
	var err error
	switch d.Manager {
	case ManagerSystemd:
		err = runLogged(log, "systemctl", "--user", "restart", SystemdUnit)
	case ManagerLaunchd:
		err = runLogged(log, "launchctl", "kickstart", "-k", launchdTarget(LaunchdLabel))
	case ManagerProcess:
		for _, pid := range d.PIDs {
			if err := sys.stop(pid); err != nil {
				return fmt.Errorf("stop pid %d: %w", pid, err)
			}
		}
		return launch(log, d.Launch)
	default:
		return fmt.Errorf("docentd is not running")
	}
	if err != nil {
		return err
	}
	if err := waitReady(log, d.Binary); err != nil {
		return err
	}
	if d.TunnelAgent && tunnelUpdated {
		return runLogged(log, "launchctl", "kickstart", "-k", launchdTarget(TunnelLabel))
	}
	return nil
}

func launch(log io.Writer, l Launch) error {
	fmt.Fprintf(log, "starting %s (log: %s)\n", joinArgs(l.Args), l.LogPath)
	pid, err := sys.start(l)
	if err != nil {
		return fmt.Errorf("could not start docentd: %w", err)
	}
	if err := waitReady(log, l.Args[0]); err != nil {
		// A failed new launch must not leave a stray process to race a retry.
		if stopErr := sys.stop(pid); stopErr != nil {
			return fmt.Errorf("%w; cleanup pid %d: %v", err, pid, stopErr)
		}
		return err
	}
	return nil
}

func runLogged(log io.Writer, name string, args ...string) error {
	fmt.Fprintf(log, "%s %s\n", name, joinArgs(args))
	if out, err := sys.run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, joinArgs(args), err, trimmed(out))
	}
	return nil
}
