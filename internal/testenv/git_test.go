package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureGitFixtures(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, inherited := range []bool{false, true} {
		name := "global config"
		if inherited {
			name = "inherited process config"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			link := filepath.Join(root, "alias")
			if err := os.Symlink(root, link); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", link)
			config := filepath.Join(root, "gitconfig")
			if err := os.WriteFile(config, []byte("[safe]\n\tbareRepository = explicit\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", config)
			t.Setenv("GIT_CONFIG_COUNT", "0")
			if inherited {
				t.Setenv("GIT_CONFIG_COUNT", "2")
				t.Setenv("GIT_CONFIG_KEY_0", "safe.bareRepository")
				t.Setenv("GIT_CONFIG_VALUE_0", "explicit")
				t.Setenv("GIT_CONFIG_KEY_1", "test.inherited")
				t.Setenv("GIT_CONFIG_VALUE_1", "preserved")
			}
			for _, key := range []string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_CONFIG_KEY_2", "GIT_CONFIG_VALUE_2"} {
				t.Setenv(key, os.Getenv(key))
			}
			if err := configure(); err != nil {
				t.Fatal(err)
			}

			// A fresh test gets a new temp root; this test already cached its own.
			t.Run("canonical temp paths", func(t *testing.T) {
				dir := t.TempDir()
				physical, err := filepath.EvalSymlinks(dir)
				if err != nil {
					t.Fatal(err)
				}
				if dir != physical {
					t.Fatalf("fixture path %q differs from physical path %q", dir, physical)
				}
			})
			dir := t.TempDir()
			cmd := exec.Command("git", "init", "--bare", "-q", dir)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("init bare fixture: %v\n%s", err, out)
			}
			cmd = exec.Command("git", "rev-parse", "--is-bare-repository")
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "true" {
				t.Fatalf("access bare fixture: %v\n%s", err, out)
			}
			if inherited {
				out, err := exec.Command("git", "config", "--get", "test.inherited").CombinedOutput()
				if err != nil || strings.TrimSpace(string(out)) != "preserved" {
					t.Fatalf("inherited config lost: %v\n%s", err, out)
				}
			}
			content, err := os.ReadFile(config)
			if err != nil || string(content) != "[safe]\n\tbareRepository = explicit\n" {
				t.Fatalf("host config changed: %v\n%s", err, content)
			}
		})
	}
}

func TestConfigureRejectsInvalidConfigCount(t *testing.T) {
	for _, count := range []string{"invalid", "-1"} {
		t.Run(count, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", count)
			if err := configure(); err == nil || !strings.Contains(err.Error(), "GIT_CONFIG_COUNT") {
				t.Fatalf("configure = %v, want invalid config count", err)
			}
		})
	}
}
