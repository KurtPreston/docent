package daemoncontrol

import (
	"io"
	"strings"
	"testing"
)

func TestStopSystemdThroughManager(t *testing.T) {
	f := &fakeSystem{os: "linux", paths: map[string]bool{systemdRunDir: true},
		procs: []process{{PID: 13, Exe: bin, Args: []string{bin}}}}
	useFake(t, f)
	f.outputs["systemctl --user show --property=MainPID --value docentd.service"] = "13"
	f.outputs["systemctl --user stop docentd.service"] = ""
	if err := Stop(io.Discard, bin); err != nil {
		t.Fatal(err)
	}
	if len(f.stopped) != 0 || !strings.Contains(strings.Join(f.ran, "|"), "systemctl --user stop") {
		t.Fatalf("bypassed manager: %v", f.ran)
	}
}

func TestStartUnloadedLaunchAgent(t *testing.T) {
	f := &fakeSystem{os: "darwin"}
	useFake(t, f)
	plist := "/home/me/Library/LaunchAgents/com.docent.docentd.plist"
	f.paths[plist] = true
	f.outputs["plutil -extract ProgramArguments.0 raw -o - "+plist] = bin
	f.outputs["launchctl bootstrap gui/501 "+plist] = ""
	if err := Start(io.Discard, StartOptions{Binary: bin, Home: "/home/me"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.ran, "|"), "launchctl bootstrap gui/501 "+plist) {
		t.Fatal("retained plist was not bootstrapped")
	}
}

func TestManagerFailureNeverFallsBackToAProcess(t *testing.T) {
	f := &fakeSystem{os: "linux", paths: map[string]bool{systemdRunDir: true}}
	useFake(t, f)
	f.outputs["systemctl --user show --property=MainPID --value docentd.service"] = "0"
	if err := Start(io.Discard, StartOptions{Binary: bin}); err == nil {
		t.Fatal("failed service command reported success")
	}
	if len(f.started) != 0 {
		t.Fatal("started a duplicate unmanaged daemon")
	}
}

func TestManagedOverrideRejectedBeforeStart(t *testing.T) {
	f := &fakeSystem{os: "linux", paths: map[string]bool{systemdRunDir: true}}
	useFake(t, f)
	f.outputs["systemctl --user show --property=MainPID --value docentd.service"] = "0"
	if err := Start(io.Discard, StartOptions{Binary: bin, ConfigOverride: true}); err == nil {
		t.Fatal("managed config override accepted")
	}
	if strings.Contains(strings.Join(f.ran, "|"), "systemctl --user start ") {
		t.Fatal("started before validating override")
	}
}
