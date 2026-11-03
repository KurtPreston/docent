package selfupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientOnlyInstall(t *testing.T) {
	newReleaseServer(t, fullRelease("v0.2.0"))
	dir := installDir(t, "docent-tunnel")
	res, err := newTestUpdater(t, dir).Install("v0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(res.Updated, ",") != "docent,docent-tunnel" {
		t.Fatalf("updated %v", res.Updated)
	}
	if got := readFile(t, filepath.Join(dir, "docent")); got != "new-cli" {
		t.Fatalf("CLI received wrong binary: %q", got)
	}
	if _, err := os.Stat(res.DaemonBinary); !os.IsNotExist(err) {
		t.Fatalf("daemon was installed: %v", err)
	}
}

func TestRejectLegacyArchiveBeforeReplacingAnything(t *testing.T) {
	rel := fullRelease("v0.1.0")
	delete(rel.files, "docent")
	newReleaseServer(t, rel)
	dir := installDir(t, "docentd")
	_, err := newTestUpdater(t, dir).Install(rel.tag)
	if err == nil || !strings.Contains(err.Error(), "no docent binary") {
		t.Fatalf("legacy archive: %v", err)
	}
	for _, name := range []string{"docent", "docentd"} {
		if got := readFile(t, filepath.Join(dir, name)); got != "old" {
			t.Fatalf("%s changed: %q", name, got)
		}
	}
}

func TestResolveCLIAndDaemonThroughSymlink(t *testing.T) {
	dir := installDir(t, "docentd")
	link := filepath.Join(t.TempDir(), "docent")
	if err := os.Symlink(filepath.Join(dir, "docent"), link); err != nil {
		t.Fatal(err)
	}
	u, err := New(Options{Binary: link})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if u.Binary() != filepath.Join(resolved, "docent") || u.DaemonBinary() != filepath.Join(resolved, "docentd") {
		t.Fatalf("wrong installation: %s, %s", u.Binary(), u.DaemonBinary())
	}
}

func TestPairedInstallRollsBackCLI(t *testing.T) {
	dir := installDir(t, "docentd")
	targets := map[string]string{"docent": filepath.Join(dir, "docent"), "docentd": filepath.Join(dir, "docentd")}
	count := 0
	err := installFiles(targets, map[string][]byte{"docent": []byte("cli"), "docentd": []byte("daemon")}, func(from, to string) error {
		count++
		if count == 2 {
			return os.ErrPermission
		}
		return os.Rename(from, to)
	})
	if err == nil {
		t.Fatal("expected replacement failure")
	}
	for name, path := range targets {
		if readFile(t, path) != "old" {
			t.Fatalf("%s was not restored", name)
		}
	}
}
