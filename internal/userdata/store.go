package userdata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/kurt/slakkr-ai/internal/configschema"
	"gopkg.in/yaml.v3"
)

type Store struct {
	Root string
}

func NewStore(root string) Store {
	return Store{Root: root}
}

// Ensure creates userdata/output and a default config.yaml if missing.
// The legacy `.cache/` directory (used for ai-debug payloads before
// run-log directories existed) is opportunistically removed so old
// installs don't carry it around.
func (s Store) Ensure(_ context.Context) error {
	if err := os.MkdirAll(filepath.Join(s.Root, "output"), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(s.Root, ".cache"))
	return writeDefaultYAML(filepath.Join(s.Root, "config.yaml"), ConfigFile{
		AI: AIConfig{Provider: "rule-based"},
	})
}

func (s Store) ConfigPath() string {
	return filepath.Join(s.Root, "config.yaml")
}

func (s Store) LoadConfig() (ConfigFile, error) {
	var file ConfigFile
	path := s.ConfigPath()
	content, err := os.ReadFile(path)
	if err != nil {
		return file, err
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return file, nil
	}
	if err := configschema.ValidateYAML(content); err != nil {
		return file, ValidationError{Problems: configschema.ValidationProblems(err)}
	}
	if err := yaml.Unmarshal(content, &file); err != nil {
		return file, err
	}
	if err := file.Validate(); err != nil {
		return file, err
	}
	return file, nil
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

func writeYAML(path string, value any) error {
	content, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	if err := configschema.ValidateYAML(content); err != nil {
		return ValidationError{Problems: configschema.ValidationProblems(err)}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
