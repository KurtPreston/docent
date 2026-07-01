package docentconfig

import (
	_ "embed"
	"os"
	"path/filepath"
)

//go:embed onclick.sh
var defaultOnClickScript []byte

// OnClickScriptPath is the default dashboard launch hook path.
func OnClickScriptPath() string {
	return filepath.Join(DefaultDir(), "onclick.sh")
}

// InstallDefaultOnClickScript writes the bundled default onclick.sh when the
// target path does not already exist. Returns installed=true when a new file
// was written.
func InstallDefaultOnClickScript() (installed bool, err error) {
	path := OnClickScriptPath()
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(path, defaultOnClickScript, 0o755); err != nil {
		return false, err
	}
	return true, nil
}
