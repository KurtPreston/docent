package worktree

import (
	"os/exec"
	"testing"
)

// Each name is checked against git itself, so the rules here cannot drift from
// the ones `git worktree add -b` applies.
func TestCheckBranchNameAgreesWithGit(t *testing.T) {
	requireGit(t)
	names := []string{
		// Accepted
		"main", "fix-login", "salsa-1/fix", "release/3.23.100", "a.b", "x@y", "feature_x",
		// Rejected
		"", "HEAD", "-x", "has space", "fix-login (existing PR branch)",
		"a..b", "a~1", "a^", "a:b", "a?", "a*", "a[b", `a\b`, "a@{1}", "a\tb",
		"/a", "a/", "a//b", "a.", ".a", "a/.b", "a.lock", "a.lock/b",
	}
	for _, name := range names {
		want := exec.Command("git", "check-ref-format", "--branch", name).Run() == nil
		err := CheckBranchName(name)
		if got := err == nil; got != want {
			t.Errorf("CheckBranchName(%q) = %v, but git accepts it: %t", name, err, want)
		}
	}
}

// The reason names the offending part, since it is what a user reads.
func TestCheckBranchNameExplains(t *testing.T) {
	err := CheckBranchName("fix-login (existing PR branch)")
	if err == nil || err.Error() != `branch name "fix-login (existing PR branch)" contains ' '` {
		t.Fatalf("err = %v", err)
	}
}
