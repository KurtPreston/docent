package selfupdate

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
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
	waitForBackgroundHelper(t, pid, want.LogPath)
	p, ok := readProc(filepath.Join("/proc", strconv.Itoa(pid)), pid)
	if !ok || p.LaunchError != nil {
		t.Fatalf("readProc failed: found=%v, launch error=%v; %s", ok, p.LaunchError, procDiagnostics(pid))
	}
	if !reflect.DeepEqual(p.Args, want.Args) || !reflect.DeepEqual(p.Env, want.Env) ||
		p.Dir != want.Dir || p.Stdout != want.LogPath {
		t.Fatal("launch arguments, environment, working directory, or log destination changed")
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
		t.Fatalf("restart did not preserve launch settings: pids=%v, error=%v", restarted.PIDs, err)
	}
}

// Starting a process does not guarantee its runtime has finished initializing.
// Wait for the child itself before asserting its /proc metadata.
func waitForBackgroundHelper(t *testing.T, pid int, logPath string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		output, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read helper log: %v", err)
		}
		if strings.Contains(string(output), "ready\n") {
			return
		}
		if !(realSystem{}).alive(pid) || time.Now().After(deadline) {
			t.Fatalf("helper did not become ready: log=%q; %s", output, procDiagnostics(pid))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func procDiagnostics(pid int) string {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	exe, exeErr := os.Readlink(filepath.Join(dir, "exe"))
	cmdline, cmdErr := os.ReadFile(filepath.Join(dir, "cmdline"))
	return fmt.Sprintf("exe=%q, exe error=%v, cmdline bytes=%d, cmdline error=%v",
		exe, exeErr, len(cmdline), cmdErr)
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
