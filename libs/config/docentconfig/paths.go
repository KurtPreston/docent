package docentconfig

import (
	"os"
	"path/filepath"
)

// DefaultDir is the docent configuration root (~/.config/docent).
func DefaultDir() string {
	if v := os.Getenv("DOCENT_CONFIG_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "docent")
	}
	return filepath.Join(home, ".config", "docent")
}

func DaemonConfigPath() string {
	if v := os.Getenv("DOCENT_CONFIG"); v != "" {
		return v
	}
	return filepath.Join(DefaultDir(), "docentd.yaml")
}

func DirectivesConfigPath() string {
	return filepath.Join(DefaultDir(), "config.yaml")
}

func EnvPath() string {
	return filepath.Join(DefaultDir(), ".env")
}

// StateDir is the docent state root (logs, run history). Follows the XDG
// Base Directory spec: $XDG_STATE_HOME/docent, defaulting to
// ~/.local/state/docent. DOCENT_STATE_DIR overrides everything.
func StateDir() string {
	if v := os.Getenv("DOCENT_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "docent")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "state", "docent")
	}
	return filepath.Join(home, ".local", "state", "docent")
}

// DefaultOutputDir is where the reporter writes generated markdown when no
// output_dir is configured and no --out-dir flag is given. These are
// user-facing documents, so the default lives in a visible ~/docent rather
// than a hidden dir. DOCENT_OUTPUT_DIR overrides it.
func DefaultOutputDir() string {
	if v := os.Getenv("DOCENT_OUTPUT_DIR"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "docent"
	}
	return filepath.Join(home, "docent")
}
