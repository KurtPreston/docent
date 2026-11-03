package daemoncontrol

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Detect preflights an update restart without requiring one for a stopped daemon.
func Detect(binary string) (Daemon, error) {
	d, err := Inspect(binary)
	if err != nil {
		return d, err
	}
	if !d.Running {
		d.Manager = ManagerNone
		return d, nil
	}
	return d, d.restartable()
}

// Inspect is read-only and does not require recoverable relaunch metadata.
func Inspect(binary string) (Daemon, error) {
	return inspect(StartOptions{Binary: binary})
}

func inspect(opts StartOptions) (Daemon, error) {
	d := Daemon{Binary: opts.Binary, Manager: ManagerNone}
	if sys.goos() != "linux" && sys.goos() != "darwin" {
		return d, fmt.Errorf("local daemon lifecycle is unsupported on %s", sys.goos())
	}
	var err error
	d.Installed, err = sys.installed(opts.Binary)
	if err != nil {
		return d, fmt.Errorf("inspect daemon executable: %w", err)
	}
	procs, err := sys.processes()
	if err != nil {
		return d, fmt.Errorf("cannot inspect running daemons: %w; use --restart=never for updates", err)
	}
	for _, p := range procs {
		if p.PID == sys.pid() || !sameFile(p.Exe, opts.Binary) || len(p.Args) == 0 || isSubcommand(p.Args) {
			continue
		}
		d.PIDs = append(d.PIDs, p.PID)
		d.Launch = Launch{Args: append([]string{opts.Binary}, p.Args[1:]...), Dir: p.Dir, Env: p.Env, LogPath: p.Stdout}
		d.launchError = p.LaunchError
	}
	if len(d.PIDs) > 1 {
		return d, fmt.Errorf("multiple daemons use %s; refuse ambiguous control; use --restart=never for updates", opts.Binary)
	}
	if len(d.PIDs) == 1 {
		d.Running, d.Manager = true, ManagerProcess
	}
	if sys.goos() == "linux" && sys.exists(systemdRunDir) {
		return inspectSystemd(d)
	}
	if sys.goos() == "darwin" {
		return inspectLaunchd(d, opts.Home)
	}
	return d, nil
}

func inspectSystemd(d Daemon) (Daemon, error) {
	state, err := sys.run("systemctl", "--user", "show", "--property=LoadState", "--value", SystemdUnit)
	if trimmed(state) == "not-found" {
		return d, nil
	}
	if err != nil {
		return d, fmt.Errorf("cannot inspect user service: %w: %s", err, trimmed(state))
	}
	if err := checkSystemdBinary(d.Binary); err != nil {
		return d, err
	}
	pidText, err := sys.run("systemctl", "--user", "show", "--property=MainPID", "--value", SystemdUnit)
	if err != nil {
		return d, fmt.Errorf("cannot inspect service pid: %w", err)
	}
	pid, err := strconv.Atoi(trimmed(pidText))
	if err != nil || pid < 0 {
		return d, fmt.Errorf("invalid service pid %q", trimmed(pidText))
	}
	if d.Running && d.PIDs[0] != pid {
		return d, fmt.Errorf("matching daemon is not owned by %s; refuse ambiguous control", SystemdUnit)
	}
	if pid > 0 && !d.Running {
		return d, fmt.Errorf("service pid %d does not identify this installation", pid)
	}
	d.Manager, d.loaded = ManagerSystemd, true
	return d, nil
}

func inspectLaunchd(d Daemon, home string) (Daemon, error) {
	out, err := sys.run("launchctl", "print", launchdTarget(LaunchdLabel))
	if err == nil {
		if !sameFile(serviceValue(out, "program"), d.Binary) {
			return d, fmt.Errorf("LaunchAgent %s belongs to a different installation", LaunchdLabel)
		}
		pid, _ := strconv.Atoi(serviceValue(out, "pid"))
		if d.Running && d.PIDs[0] != pid {
			return d, fmt.Errorf("matching daemon is not owned by %s; refuse ambiguous control", LaunchdLabel)
		}
		if serviceValue(out, "state") == "running" && (!d.Running || pid <= 0) {
			return d, fmt.Errorf("LaunchAgent running pid does not identify this installation")
		}
		d.Manager, d.loaded = ManagerLaunchd, true
		tunnel, terr := sys.run("launchctl", "print", launchdTarget(TunnelLabel))
		d.TunnelAgent = terr == nil && serviceValue(tunnel, "state") == "running" &&
			sameFile(serviceValue(tunnel, "program"), filepath.Join(filepath.Dir(d.Binary), "docent-tunnel"))
	} else if !strings.Contains(strings.ToLower(out), "could not find service") {
		return d, fmt.Errorf("cannot inspect LaunchAgent: %w: %s", err, trimmed(out))
	}
	if home == "" {
		home, err = os.UserHomeDir()
		if err != nil {
			return d, err
		}
	}
	d.plist = filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	if sys.exists(d.plist) {
		program, err := sys.run("plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", d.plist)
		if err != nil || !sameFile(trimmed(program), d.Binary) {
			return d, fmt.Errorf("cannot use %s: executable does not match %s", d.plist, d.Binary)
		}
		if d.Manager == ManagerProcess {
			return d, fmt.Errorf("unmanaged daemon and installed LaunchAgent conflict")
		}
		d.Manager = ManagerLaunchd
	}
	return d, nil
}
