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
