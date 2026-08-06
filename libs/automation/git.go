package automation

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runGit runs a git command in dir, folding its output into the error so a
// failure explains itself. Used by the post-steps (commit, push) that follow an
// agent turn; provisioning the worktree itself is grove's job.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sanitizePath maps a repo or branch name to a token safe to use as a map key or
// a path segment.
func sanitizePath(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == '/' || r == ':' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
