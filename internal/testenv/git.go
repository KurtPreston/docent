// Package testenv configures process-local environments for Git fixture tests.
package testenv

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// Run prepares Git fixtures before tests (including parallel tests) start.
// Only the test binary and its children inherit these settings.
func Run(m *testing.M) int {
	if err := configure(); err != nil {
		fmt.Fprintf(os.Stderr, "configure Git test environment: %v\n", err)
		return 1
	}
	return m.Run()
}

func configure() error {
	// Git reports physical paths, so fixture paths must use the same temp root.
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return fmt.Errorf("resolve temporary directory: %w", err)
	}

	// Append rather than discard config inherited from the test runner.
	count := 0
	if raw := os.Getenv("GIT_CONFIG_COUNT"); raw != "" {
		count, err = strconv.Atoi(raw)
		if err != nil || count < 0 || count == math.MaxInt {
			return fmt.Errorf("invalid GIT_CONFIG_COUNT %q", raw)
		}
	}
	index := strconv.Itoa(count)
	settings := []struct{ key, value string }{
		{"TMPDIR", root},
		{"GIT_CONFIG_KEY_" + index, "safe.bareRepository"},
		{"GIT_CONFIG_VALUE_" + index, "all"},
		{"GIT_CONFIG_COUNT", strconv.Itoa(count + 1)},
	}
	for _, setting := range settings {
		if err := os.Setenv(setting.key, setting.value); err != nil {
			return fmt.Errorf("set %s: %w", setting.key, err)
		}
	}
	return nil
}
