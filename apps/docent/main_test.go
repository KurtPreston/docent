package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/cursorhooks"
	"github.com/KurtPreston/docent/libs/daemoncontrol"
	"github.com/KurtPreston/docent/libs/selfupdate"
)

func TestCommandExitCodes(t *testing.T) {
	for _, tt := range []struct {
		args []string
		code int
	}{
		{nil, 0}, {[]string{"--help"}, 0}, {[]string{"version"}, 0},
		{[]string{"unknown"}, 2}, {[]string{"version", "extra"}, 2},
		{[]string{"doctor", "--help"}, 0}, {[]string{"doctor", "--bogus"}, 2},
		{[]string{"install-hooks", "--check", "extra"}, 2},
		{[]string{"update", "--restart", "always"}, 2},
		{[]string{"update", "extra"}, 2},
	} {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			var out, err bytes.Buffer
			a := app{out: &out, err: &err}
			if got := a.run(tt.args); got != tt.code {
				t.Fatalf("exit %d, want %d; %s %s", got, tt.code, &out, &err)
			}
		})
	}
}

func TestOfflineDoctorAndHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CURSOR_CONFIG_DIR", filepath.Join(home, ".cursor"))
	var out, stderr bytes.Buffer
	a := app{out: &out, err: &stderr}
	if code := a.run([]string{"doctor", "--config", filepath.Join(home, "missing.yaml")}); code != 0 {
		t.Fatalf("offline doctor: %d %s", code, &stderr)
	}
	if code := a.run([]string{"install-hooks", "--check"}); code != 1 {
		t.Fatalf("missing hooks: %d", code)
	}
	if code := a.run([]string{"install-hooks"}); code != 0 || !cursorhooks.Check().OK() {
		t.Fatalf("install: %d %s", code, &stderr)
	}
	if code := a.run([]string{"install-hooks", "--check"}); code != 0 {
		t.Fatalf("check installed: %d", code)
	}
	bad := filepath.Join(home, "invalid.yaml")
	if err := os.WriteFile(bad, []byte("port: [invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := a.run([]string{"doctor", "--config", bad}); code != 1 {
		t.Fatalf("invalid config: %d", code)
	}
}

func TestClientOnlyUpdateNeverPromptsOrRestarts(t *testing.T) {
	var out bytes.Buffer
	a := app{in: strings.NewReader("yes\n"), out: &out, terminal: true}
	err := a.restartAfterUpdate(updateOptions{restart: "auto"},
		daemoncontrol.Daemon{Manager: daemoncontrol.ManagerNone},
		selfupdate.Result{Updated: []string{"docent", "docent-tunnel"}})
	if err != nil || out.Len() != 0 {
		t.Fatalf("client-only update attempted startup: %v %s", err, &out)
	}
}

func TestStoppedUpdateRequiresConfirmation(t *testing.T) {
	var out bytes.Buffer
	a := app{in: strings.NewReader("no\n"), out: &out, terminal: true}
	res := selfupdate.Result{Updated: []string{"docent", "docentd"}}
	err := a.restartAfterUpdate(updateOptions{restart: "auto"}, daemoncontrol.Daemon{Manager: daemoncontrol.ManagerNone}, res)
	if err != nil || !strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("confirmation: %v %s", err, &out)
	}
}
