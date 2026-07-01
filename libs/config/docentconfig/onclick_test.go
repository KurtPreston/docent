package docentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallDefaultOnClickScript(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCENT_CONFIG_DIR", dir)

	path := OnClickScriptPath()
	installed, err := InstallDefaultOnClickScript()
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("expected installed=true on first run")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatal("onclick.sh should be executable")
	}

	installed, err = InstallDefaultOnClickScript()
	if err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("expected installed=false when file already exists")
	}
}

func TestOnClickScriptPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCENT_CONFIG_DIR", dir)
	want := filepath.Join(dir, "onclick.sh")
	if got := OnClickScriptPath(); got != want {
		t.Fatalf("OnClickScriptPath() = %q, want %q", got, want)
	}
}
