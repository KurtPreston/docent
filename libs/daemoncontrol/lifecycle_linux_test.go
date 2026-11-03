package daemoncontrol

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

var helperConfig = flag.String("config", "", "isolated lifecycle test configuration")

func TestMain(m *testing.M) {
	if os.Getenv("DOCENT_LIFECYCLE_HELPER") == "1" {
		flag.Parse()
		if err := serveLifecycleHelper(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func serveLifecycleHelper() error {
	data, err := os.ReadFile(*helperConfig)
	if err != nil {
		return err
	}
	var cfg struct {
		Port int `yaml:"port"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return err
	}
	fmt.Println("ready")
	return http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
}

// Keep real process and HTTP behavior, but never discover host service managers.
type unmanagedSystem struct{ realSystem }

func (s unmanagedSystem) exists(path string) bool {
	return path != systemdRunDir && s.realSystem.exists(path)
}

func useUnmanagedSystem(t *testing.T) {
	t.Helper()
	old := sys
	sys = unmanagedSystem{}
	t.Cleanup(func() { sys = old })
}

func TestUnprivilegedLifecycle(t *testing.T) {
	useUnmanagedSystem(t)
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "docentd")
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docentd.yaml"), []byte(fmt.Sprintf("port: %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCENT_LIFECYCLE_HELPER", "1")
	t.Cleanup(func() {
		if err := Stop(io.Discard, binary); err != nil {
			t.Errorf("cleanup daemon: %v", err)
		}
	})
	opts := StartOptions{Binary: binary, ConfigDir: dir}
	if err := Start(io.Discard, opts); err != nil {
		t.Fatal(err)
	}
	before, err := Inspect(binary)
	if err != nil || !before.Running {
		t.Fatalf("status: %+v %v", before, err)
	}
	waitForBackgroundHelper(t, before.PIDs[0], filepath.Join(dir, "docentd.log"))
	if err := Start(io.Discard, opts); err != nil {
		t.Fatal(err)
	}
	again, err := Inspect(binary)
	if err != nil || again.PIDs[0] != before.PIDs[0] {
		t.Fatalf("idempotent start changed pid: %v", err)
	}
	if err := Restart(io.Discard, opts); err != nil {
		t.Fatal(err)
	}
	after, err := Inspect(binary)
	if err != nil || !after.Running || after.PIDs[0] == before.PIDs[0] {
		t.Fatalf("restart: %+v %v", after, err)
	}
	if err := Stop(io.Discard, binary); err != nil {
		t.Fatal(err)
	}
	stopped, err := Inspect(binary)
	if err != nil || stopped.Running {
		t.Fatalf("stop: %+v %v", stopped, err)
	}
	if err := Stop(io.Discard, binary); err != nil {
		t.Fatal(err)
	}
}
