package daemoncontrol

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectDoesNotRequireRelaunchMetadata(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		f := &fakeSystem{os: platform, procs: []process{{PID: 13, Exe: bin, Args: []string{bin}, LaunchError: errors.New("unreadable environment")}}}
		useFake(t, f)
		d, err := Inspect(bin)
		if err != nil || !d.Running || d.Manager != ManagerProcess {
			t.Fatalf("inspect: %+v %v", d, err)
		}
		if err := Stop(io.Discard, bin); err != nil {
			t.Fatal(err)
		}
		if len(f.stopped) != 1 {
			t.Fatal("stop required relaunch metadata")
		}
	}
}

func TestIdempotentStartStop(t *testing.T) {
	f := &fakeSystem{os: "linux"}
	useFake(t, f)
	opts := StartOptions{Binary: bin, ConfigDir: "/c"}
	for i := 0; i < 2; i++ {
		if err := Start(io.Discard, opts); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.started) != 1 {
		t.Fatal("duplicate daemon started")
	}
	for i := 0; i < 2; i++ {
		if err := Stop(io.Discard, bin); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.stopped) != 1 {
		t.Fatal("stopped the process more than once")
	}
	if err := Restart(io.Discard, opts); err != nil || len(f.started) != 2 {
		t.Fatalf("restart of stopped daemon: %v", err)
	}
}

func TestStopLoadedLaunchAgentWithoutAChild(t *testing.T) {
	f := &fakeSystem{os: "darwin"}
	useFake(t, f)
	f.outputs["launchctl print gui/501/com.docent.docentd"] = "program = " + bin + "\nstate = not running"
	f.outputs["launchctl bootout gui/501/com.docent.docentd"] = ""
	if err := Stop(io.Discard, bin); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.ran, "|"), "launchctl bootout") || len(f.stopped) != 0 {
		t.Fatalf("KeepAlive was not safely unloaded: %v", f.ran)
	}
}

func TestLifecycleFailures(t *testing.T) {
	t.Run("missing installation", func(t *testing.T) {
		f := &fakeSystem{os: "linux"}
		useFake(t, f)
		f.paths[bin] = false
		if err := Start(io.Discard, StartOptions{Binary: bin}); err == nil || len(f.started) != 0 {
			t.Fatalf("missing daemon start: %v", err)
		}
	})
	t.Run("startup health failure", func(t *testing.T) {
		f := &fakeSystem{os: "linux", probeErr: errors.New("unhealthy")}
		useFake(t, f)
		if err := Start(io.Discard, StartOptions{Binary: bin, ConfigDir: "/c"}); err == nil || !strings.Contains(err.Error(), "unhealthy") {
			t.Fatalf("health failure: %v", err)
		}
		if len(f.stopped) != 1 {
			t.Fatal("failed launch leaked a process")
		}
	})
	t.Run("config override", func(t *testing.T) {
		f := &fakeSystem{os: "linux", procs: []process{{PID: 13, Exe: bin, Args: []string{bin}}}}
		useFake(t, f)
		if err := Start(io.Discard, StartOptions{Binary: bin, ConfigOverride: true}); err == nil {
			t.Fatal("accepted conflicting override")
		}
	})
	t.Run("inspection failure", func(t *testing.T) {
		f := &fakeSystem{os: "darwin"}
		useFake(t, f)
		f.outputs["launchctl print gui/501/com.docent.docentd"] = "invalid output"
		if _, err := Inspect(bin); err == nil {
			t.Fatal("invalid manager output accepted")
		}
	})
}

func TestHealthUsesEffectiveConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "docentd.yaml")
	port := strings.Split(server.URL, ":")[2]
	if err := os.WriteFile(path, []byte("port: 1\nbindHost: 0.0.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := Launch{Args: []string{bin, "-config", "docentd.yaml", "-port", port}, Dir: dir}
	if err := (realSystem{}).probe(Daemon{Launch: l}); err != nil {
		t.Fatal(err)
	}
	l.Args = []string{bin}
	l.Env = []string{"DOCENT_CONFIG=" + path}
	url, err := healthURL(l)
	if err != nil || url != "http://127.0.0.1:1/health" {
		t.Fatalf("environment config: %s %v", url, err)
	}
}

func TestRestartDoesNotStopDaemonWithMissingExecutable(t *testing.T) {
	f := &fakeSystem{os: "linux", procs: []process{{PID: 13, Exe: bin, Args: []string{bin}}}}
	useFake(t, f)
	f.paths[bin] = false
	if err := Restart(io.Discard, StartOptions{Binary: bin}); err == nil {
		t.Fatal("restart accepted a missing executable")
	}
	if len(f.stopped) != 0 {
		t.Fatal("stopped the only running copy of a missing binary")
	}
}
