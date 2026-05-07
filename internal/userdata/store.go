package userdata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Store struct {
	Root string
}

func NewStore(root string) Store {
	return Store{Root: root}
}

// Ensure creates userdata/output and a default config.yaml if missing.
func (s Store) Ensure(_ context.Context) error {
	if err := os.MkdirAll(filepath.Join(s.Root, "output"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.Root, ".cache"), 0o755); err != nil {
		return err
	}
	return writeDefaultYAML(filepath.Join(s.Root, "config.yaml"), ConfigFile{
		AI: AIConfig{Provider: "rule-based"},
	})
}

func (s Store) ConfigPath() string {
	return filepath.Join(s.Root, "config.yaml")
}

func (s Store) LoadConfig() (ConfigFile, error) {
	var file ConfigFile
	err := readYAML(s.ConfigPath(), &file)
	return file, err
}

func (s Store) SaveConfig(file ConfigFile) error {
	if err := file.Validate(); err != nil {
		return err
	}
	return writeYAML(s.ConfigPath(), file)
}

func writeDefaultYAML(path string, value any) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeYAML(path, value)
}

func readYAML(path string, out any) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	return yaml.Unmarshal(content, out)
}

func writeYAML(path string, value any) error {
	content, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
