package daemoncontrol

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// system is what restarting touches outside the process, so tests can stub it.
type system interface {
	goos() string
	uid() int
	pid() int
	exists(path string) bool
	run(name string, args ...string) (string, error)
	processes() ([]process, error)
	stop(pid int) error
	start(l Launch) (int, error)
	alive(pid int) bool
	sleep(d time.Duration)
}

// process is a running program as seen by Detect.
type process struct {
	PID         int
	Exe         string   // resolved executable path
	Args        []string // argv
	Dir         string   // working directory, when known
	Env         []string // environment, when readable
	Stdout      string   // file stdout writes to, when it's a regular file
	LaunchError error    // essential relaunch metadata could not be read
}

var sys system = realSystem{}

// commandTimeout bounds each service-manager command.
const commandTimeout = 30 * time.Second

type realSystem struct{}

func (realSystem) goos() string          { return runtime.GOOS }
func (realSystem) uid() int              { return os.Getuid() }
func (realSystem) pid() int              { return os.Getpid() }
func (realSystem) sleep(d time.Duration) { time.Sleep(d) }

func (realSystem) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (realSystem) run(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(out), err
}

func trimmed(s string) string { return strings.TrimSpace(s) }

func joinArgs(args []string) string { return strings.Join(args, " ") }

func joinPIDs(pids []int) string {
	parts := make([]string, len(pids))
	for i, p := range pids {
		parts[i] = "pid " + strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}
