package selfupdate

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// fakeSystem records what restarting would do instead of doing it.
type fakeSystem struct {
	os      string
	paths   map[string]bool
	outputs map[string]string // "name args..." -> output; missing means the command fails
	procs   []process
	self    int
	dead    bool // started processes exit immediately

	ran     []string
	stopped []int
	started []Launch
}

func (f *fakeSystem) goos() string                  { return f.os }
func (f *fakeSystem) uid() int                      { return 501 }
func (f *fakeSystem) pid() int                      { return f.self }
func (f *fakeSystem) exists(p string) bool          { return f.paths[p] }
func (f *fakeSystem) sleep(time.Duration)           {}
func (f *fakeSystem) alive(int) bool                { return !f.dead }
func (f *fakeSystem) stop(pid int) error            { f.stopped = append(f.stopped, pid); return nil }
func (f *fakeSystem) processes() ([]process, error) { return f.procs, nil }

func (f *fakeSystem) run(name string, args ...string) (string, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.ran = append(f.ran, cmd)
	out, ok := f.outputs[cmd]
	if !ok {
		return "", errors.New("exit status 1")
	}
	return out, nil
}

func (f *fakeSystem) start(l Launch) (int, error) {
	f.started = append(f.started, l)
	return 4242, nil
}

func useFake(t *testing.T, f *fakeSystem) {
	t.Helper()
	if f.paths == nil {
		f.paths = map[string]bool{}
	}
	if f.outputs == nil {
		f.outputs = map[string]string{}
	}
	old := sys
	sys = f
	t.Cleanup(func() { sys = old })
}

const bin = "/home/me/.local/bin/docentd"

func TestDetectActiveSystemdUnit(t *testing.T) {
	f := &fakeSystem{os: "linux"}
	useFake(t, f)
	f.paths[systemdRunDir] = true
	f.outputs["systemctl --user is-active docentd.service"] = "active\n"
	f.outputs["systemctl --user restart docentd.service"] = ""

	d := Detect(bin)
	if d.Manager != ManagerSystemd {
		t.Fatalf("Manager = %s, want systemd", d.Manager)
	}
	if err := d.Restart(io.Discard, false); err != nil {
		t.Fatal(err)
	}
	if last := f.ran[len(f.ran)-1]; last != "systemctl --user restart docentd.service" {
		t.Fatalf("ran %q", last)
	}
}

func TestDetectIgnoresSystemctlShimWithoutSystemd(t *testing.T) {
	// A container's systemctl shim may exit 0, but /run/systemd/system is absent
	f := &fakeSystem{os: "linux"}
	useFake(t, f)
	f.outputs["systemctl --user is-active docentd.service"] = "active\n"

	if d := Detect(bin); d.Manager != ManagerNone {
		t.Fatalf("Manager = %s, want none", d.Manager)
	}
}

func TestDetectLaunchAgentRestartsTunnelWhenUpdated(t *testing.T) {
	f := &fakeSystem{os: "darwin"}
	useFake(t, f)
	f.outputs["launchctl print gui/501/com.docent.docentd"] = "state = running"
	f.outputs["launchctl print gui/501/com.docent.docent-tunnel"] = "state = running"
	f.outputs["launchctl kickstart -k gui/501/com.docent.docentd"] = ""
	f.outputs["launchctl kickstart -k gui/501/com.docent.docent-tunnel"] = ""

	d := Detect(bin)
	if d.Manager != ManagerLaunchd || !d.TunnelAgent {
		t.Fatalf("Detect = %+v, want launchd with tunnel", d)
	}
	f.ran = nil
	if err := d.Restart(io.Discard, true); err != nil {
		t.Fatal(err)
	}
	want := "launchctl kickstart -k gui/501/com.docent.docentd|launchctl kickstart -k gui/501/com.docent.docent-tunnel"
	if got := strings.Join(f.ran, "|"); got != want {
		t.Fatalf("ran %q, want %q", got, want)
	}
}

func TestDetectBackgroundProcessAndRelaunch(t *testing.T) {
	f := &fakeSystem{os: "linux", self: 10}
	useFake(t, f)
	f.procs = []process{
		{PID: 10, Exe: bin, Args: []string{bin, "update"}},                 // this command
		{PID: 11, Exe: bin, Args: []string{bin, "doctor"}},                 // another one-shot command
		{PID: 12, Exe: "/usr/bin/other", Args: []string{"/usr/bin/other"}}, // unrelated
		{PID: 13, Exe: bin, Args: []string{"docentd", "-config", "/c/docentd.yaml", "-port", "39787"},
			Dir: "/c", Env: []string{"DOCENT_TOKEN=x"}, Stdout: "/c/docentd.log"},
	}

	d := Detect(bin)
	if d.Manager != ManagerProcess || len(d.PIDs) != 1 || d.PIDs[0] != 13 {
		t.Fatalf("Detect = %+v, want process pid 13", d)
	}
	if err := d.Restart(io.Discard, false); err != nil {
		t.Fatal(err)
	}

	// It stops the daemon and relaunches the same command line, dir, env, and log
	if len(f.stopped) != 1 || f.stopped[0] != 13 {
		t.Fatalf("stopped %v, want [13]", f.stopped)
	}
	l := f.started[0]
	if got := strings.Join(l.Args, " "); got != bin+" -config /c/docentd.yaml -port 39787" {
		t.Fatalf("relaunched %q", got)
	}
	if l.Dir != "/c" || l.LogPath != "/c/docentd.log" || len(l.Env) != 1 {
		t.Fatalf("relaunch = %+v", l)
	}
}

func TestRelaunchReportsAnImmediateExit(t *testing.T) {
	f := &fakeSystem{os: "linux", dead: true}
	useFake(t, f)
	d := Daemon{Manager: ManagerProcess, PIDs: []int{13}, Launch: Launch{Args: []string{bin}, LogPath: "/c/docentd.log"}}
	err := d.Restart(io.Discard, false)
	if err == nil || !strings.Contains(err.Error(), "exited right after starting") {
		t.Fatalf("Restart error = %v", err)
	}
}

func TestStartPrefersAnInstalledService(t *testing.T) {
	f := &fakeSystem{os: "linux"}
	useFake(t, f)
	f.paths[systemdRunDir] = true
	f.paths["/home/me/.config/systemd/user/docentd.service"] = true
	f.outputs["systemctl --user start docentd.service"] = ""

	if err := Start(io.Discard, StartOptions{Binary: bin, ConfigDir: "/c", Home: "/home/me"}); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 0 || f.ran[0] != "systemctl --user start docentd.service" {
		t.Fatalf("ran %v, started %v", f.ran, f.started)
	}
}

func TestStartFallsBackToABackgroundProcess(t *testing.T) {
	f := &fakeSystem{os: "linux"}
	useFake(t, f)

	if err := Start(io.Discard, StartOptions{Binary: bin, ConfigDir: "/c", Home: "/home/me"}); err != nil {
		t.Fatal(err)
	}
	l := f.started[0]
	if got := strings.Join(l.Args, " "); got != bin+" -config /c/docentd.yaml" || l.Dir != "/c" || l.LogPath != "/c/docentd.log" {
		t.Fatalf("started %+v", l)
	}
}
