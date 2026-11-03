package daemoncontrol

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Manager names what keeps docentd running.
type Manager string

const (
	ManagerSystemd Manager = "systemd" // a systemd user unit
	ManagerLaunchd Manager = "launchd" // a macOS LaunchAgent
	ManagerProcess Manager = "process" // a detached process, as the installer's nohup fallback starts
	ManagerNone    Manager = "none"    // docentd isn't running
)

// Service names the installers register.
const (
	SystemdUnit   = "docentd.service"
	LaunchdLabel  = "com.docent.docentd"
	TunnelLabel   = "com.docent.docent-tunnel"
	systemdRunDir = "/run/systemd/system"
)

// startupGrace is how long a relaunched process must survive to count as started.
var startupGrace = 1500 * time.Millisecond

// Daemon is how docentd was found running before an update.
type Daemon struct {
	Manager     Manager
	PIDs        []int  // processes to stop (ManagerProcess)
	Launch      Launch // how to relaunch it (ManagerProcess)
	TunnelAgent bool   // launchd also runs docent-tunnel
}

// Launch describes a detached docentd process.
type Launch struct {
	Args    []string // argv, starting with the binary
	Dir     string   // working directory
	Env     []string // environment; nil inherits this process's
	LogPath string   // file stdout and stderr append to; "" discards them
}

// Describe says how the daemon runs, for messages such as "restarting docentd
// (systemd)".
func (d Daemon) Describe() string {
	switch d.Manager {
	case ManagerSystemd:
		return "systemd unit " + SystemdUnit
	case ManagerLaunchd:
		return "LaunchAgent " + LaunchdLabel
	case ManagerProcess:
		return "background process " + joinPIDs(d.PIDs)
	}
	return "not running"
}

// Detect finds only the daemon using binary. Ambiguous or incomplete process
// metadata must not lead to stopping a daemon we cannot faithfully relaunch.
func Detect(binary string) (Daemon, error) {
	if sys.goos() == "darwin" {
		out, err := sys.run("launchctl", "print", launchdTarget(LaunchdLabel))
		if err == nil && sameFile(serviceValue(out, "program"), binary) && serviceValue(out, "state") == "running" {
			tunnel, terr := sys.run("launchctl", "print", launchdTarget(TunnelLabel))
			tunnelRunning := terr == nil && serviceValue(tunnel, "state") == "running" &&
				sameFile(serviceValue(tunnel, "program"), filepath.Join(filepath.Dir(binary), "docent-tunnel"))
			return Daemon{Manager: ManagerLaunchd, TunnelAgent: tunnelRunning}, nil
		}
	}

	procs, err := sys.processes()
	if err != nil {
		return Daemon{}, fmt.Errorf("cannot inspect running daemons: %w; use --restart=never to update without restarting", err)
	}
	var d Daemon
	var launchErr error
	for _, p := range procs {
		if p.PID == sys.pid() || !sameFile(p.Exe, binary) || len(p.Args) == 0 || isSubcommand(p.Args) {
			continue
		}
		if len(d.PIDs) == 0 {
			d.Launch = Launch{Args: append([]string{binary}, p.Args[1:]...), Dir: p.Dir, Env: p.Env, LogPath: p.Stdout}
			launchErr = p.LaunchError
		}
		d.PIDs = append(d.PIDs, p.PID)
	}
	if len(d.PIDs) == 0 {
		return Daemon{Manager: ManagerNone}, nil
	}
	if len(d.PIDs) > 1 {
		return Daemon{}, fmt.Errorf("multiple daemons use %s; use --restart=never and restart each instance manually", binary)
	}
	if sys.exists(systemdRunDir) {
		out, err := sys.run("systemctl", "--user", "show", "--property=MainPID", "--value", SystemdUnit)
		if err == nil && trimmed(out) == strconv.Itoa(d.PIDs[0]) {
			return Daemon{Manager: ManagerSystemd}, nil
		}
	}
	if sys.goos() != "linux" {
		return Daemon{}, fmt.Errorf("cannot preserve a hand-started daemon's arguments and environment on %s; use --restart=never and restart it manually", sys.goos())
	}
	if launchErr != nil {
		return Daemon{}, fmt.Errorf("cannot preserve daemon pid %d: %w; use --restart=never", d.PIDs[0], launchErr)
	}
	d.Manager = ManagerProcess
	return d, nil
}

// Restart restarts a running daemon the way it was running. tunnelUpdated
// also restarts the docent-tunnel LaunchAgent, when there is one.
func (d Daemon) Restart(log io.Writer, tunnelUpdated bool) error {
	switch d.Manager {
	case ManagerSystemd:
		return runLogged(log, "systemctl", "--user", "restart", SystemdUnit)
	case ManagerLaunchd:
		if err := runLogged(log, "launchctl", "kickstart", "-k", launchdTarget(LaunchdLabel)); err != nil {
			return err
		}
		if d.TunnelAgent && tunnelUpdated {
			return runLogged(log, "launchctl", "kickstart", "-k", launchdTarget(TunnelLabel))
		}
		return nil
	case ManagerProcess:
		return d.relaunch(log)
	}
	return fmt.Errorf("docentd isn't running")
}

// relaunch stops the running processes and starts one with the same
// arguments, directory, environment, and log file.
func (d Daemon) relaunch(log io.Writer) error {
	for _, pid := range d.PIDs {
		fmt.Fprintf(log, "stopping docentd (pid %d)\n", pid)
		if err := sys.stop(pid); err != nil {
			return fmt.Errorf("could not stop docentd (pid %d): %w", pid, err)
		}
	}
	return launch(log, d.Launch)
}

// StartOptions says where a newly started docentd finds its config.
type StartOptions struct {
	Binary    string // the docentd to run
	ConfigDir string // holds docentd.yaml; also the working directory
	Home      string // for service files and the macOS log dir; defaults to $HOME
}

// Start starts docentd when it wasn't running: through its systemd unit or
// LaunchAgent when one is installed, otherwise as a detached process like the
// Linux installer's background fallback.
func Start(log io.Writer, opts StartOptions) error {
	home := opts.Home
	if home == "" {
		home, _ = os.UserHomeDir()
	}

	// An installed but stopped systemd unit or LaunchAgent
	unit := filepath.Join(home, ".config", "systemd", "user", SystemdUnit)
	if sys.exists(systemdRunDir) && sys.exists(unit) {
		if err := checkSystemdBinary(opts.Binary); err != nil {
			return err
		}
		return runLogged(log, "systemctl", "--user", "start", SystemdUnit)
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", LaunchdLabel+".plist")
	if sys.goos() == "darwin" && sys.exists(plist) {
		program, err := sys.run("plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", plist)
		if err != nil || !sameFile(trimmed(program), opts.Binary) {
			return fmt.Errorf("cannot start %s: its executable does not match %s", plist, opts.Binary)
		}
		if _, err := sys.run("launchctl", "print", launchdTarget(LaunchdLabel)); err == nil {
			return runLogged(log, "launchctl", "kickstart", launchdTarget(LaunchdLabel))
		}
		return runLogged(log, "launchctl", "bootstrap", "gui/"+strconv.Itoa(sys.uid()), plist)
	}

	// Otherwise a detached process, logging where the installers do
	logPath := filepath.Join(opts.ConfigDir, "docentd.log")
	if sys.goos() == "darwin" {
		logPath = filepath.Join(home, "Library", "Logs", "docentd.log")
	}
	return launch(log, Launch{
		Args:    []string{opts.Binary, "-config", filepath.Join(opts.ConfigDir, "docentd.yaml")},
		Dir:     opts.ConfigDir,
		LogPath: logPath,
	})
}

// launch starts a detached docentd and checks that it stays up briefly.
func launch(log io.Writer, l Launch) error {
	dest := l.LogPath
	if dest == "" {
		dest = "/dev/null"
	}
	fmt.Fprintf(log, "starting %s (log: %s)\n", joinArgs(l.Args), dest)
	pid, err := sys.start(l)
	if err != nil {
		return fmt.Errorf("could not start docentd: %w", err)
	}
	sys.sleep(startupGrace)
	if !sys.alive(pid) {
		return fmt.Errorf("docentd (pid %d) exited right after starting; see %s", pid, dest)
	}
	fmt.Fprintf(log, "docentd is running (pid %d)\n", pid)
	return nil
}

// runLogged runs a service-manager command, echoing it first.
func runLogged(log io.Writer, name string, args ...string) error {
	fmt.Fprintf(log, "%s %s\n", name, joinArgs(args))
	if out, err := sys.run(name, args...); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, joinArgs(args), err, trimmed(out))
	}
	return nil
}

// isSubcommand reports whether argv runs a one-shot docentd command, such as
// `docentd update` or `docentd doctor`, rather than the daemon.
func isSubcommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "update", "doctor", "install-hooks", "version", "-version", "--version", "-v":
		return true
	}
	return false
}

// sameFile compares executable paths after resolving symlinks.
func sameFile(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if ra, err := filepath.EvalSymlinks(a); err == nil {
		a = ra
	}
	if rb, err := filepath.EvalSymlinks(b); err == nil {
		b = rb
	}
	return a == b
}

func launchdTarget(label string) string {
	return "gui/" + strconv.Itoa(sys.uid()) + "/" + label
}
