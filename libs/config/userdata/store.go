package userdata

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/kurt/slakkr-ai/libs/config/configschema"
	"gopkg.in/yaml.v3"
)

type Store struct {
	Root string
}

func NewStore(root string) Store {
	return Store{Root: root}
}

// Ensure creates userdata/output and a default config.yaml if missing.
//
// The `.cache/` directory is intentionally left in place: collectors now
// use it for durable, cross-run caches (e.g. the Slack collector caches
// resolved user identities under `.cache/slack/<team>/` to avoid
// re-issuing users.info every run). Wiping it here would defeat that, so
// we no longer remove it. Stale legacy ai-debug payloads from old
// installs are harmless leftovers and can be deleted manually.
func (s Store) Ensure(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Join(s.Root, "output"), 0o755); err != nil {
		return err
	}
	return s.EnsureConfig(ctx)
}

// EnsureConfig writes a default config.yaml under Root if one does not
// already exist, without creating an output directory. Callers that
// manage their own output location (e.g. docent-reporter's XDG layout)
// use this instead of Ensure.
func (s Store) EnsureConfig(_ context.Context) error {
	return writeDefaultYAML(s.ConfigPath(), ConfigFile{
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
