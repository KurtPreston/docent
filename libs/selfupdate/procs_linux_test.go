package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestBackgroundDaemonHelper(t *testing.T) {
	if os.Getenv("DOCENT_TEST_DAEMON") != "1" {
		return
	}
	fmt.Println("ready")
	time.Sleep(time.Minute)
}

func TestReadProcPreservesBackgroundLaunch(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	want := Launch{
		Args: []string{binary, "-test.run=^TestBackgroundDaemonHelper$"},
		Dir:  dir, Env: append(os.Environ(), "DOCENT_TEST_DAEMON=1", "DOCENT_TEST_SPACES=two words"),
		LogPath: filepath.Join(dir, "daemon.log"),
	}
	s := realSystem{}
	pid, err := s.start(want)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.stop(pid); err != nil {
			t.Errorf("stop helper: %v", err)
		}
	})
	p, ok := readProc(filepath.Join("/proc", strconv.Itoa(pid)), pid)
	if !ok || p.LaunchError != nil {
		t.Fatalf("readProc = %+v, %v", p, ok)
	}
	if !reflect.DeepEqual(p.Args, want.Args) || !reflect.DeepEqual(p.Env, want.Env) ||
		p.Dir != want.Dir || p.Stdout != want.LogPath {
		t.Fatalf("launch metadata changed: %+v", p)
	}

	// Exercise detection and a real restart using only this test executable.
	t.Cleanup(func() {
		procs, err := s.processes()
		if err != nil {
			t.Error(err)
			return
		}
		for _, child := range procs {
			if child.Exe == binary && child.Dir == dir {
				if err := s.stop(child.PID); err != nil {
					t.Errorf("stop restarted helper: %v", err)
				}
			}
		}
	})
	d, err := Detect(binary)
	if err != nil || d.Manager != ManagerProcess {
		t.Fatalf("Detect = %+v, %v", d, err)
	}
	if err := d.Restart(io.Discard, false); err != nil {
		t.Fatal(err)
	}
	restarted, err := Detect(binary)
	if err != nil || len(restarted.PIDs) != 1 || restarted.PIDs[0] == pid ||
		!reflect.DeepEqual(restarted.Launch, want) {
		t.Fatalf("restart did not preserve launch settings: %+v, %v", restarted, err)
	}
}

func TestReadProcReportsMissingEnvironment(t *testing.T) {
	dir := t.TempDir()
	for name, target := range map[string]string{"exe": "/bin/docentd", "cwd": "/config"} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte("docentd\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, ok := readProc(dir, 42)
	if !ok || p.LaunchError == nil {
		t.Fatalf("missing environment was ignored: %+v, %v", p, ok)
	}
	if env := splitNul(nil); env == nil || len(env) != 0 {
		t.Fatalf("empty environment must not inherit the updater environment: %v", env)
	}
}
