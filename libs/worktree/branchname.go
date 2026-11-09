package worktree

import (
	"fmt"
	"strings"
)

// CheckBranchName reports why name is not a branch git would create, or nil if
// it is one. It follows `git check-ref-format --branch` without running git, so
// a collector can vet every name it reads, such as a branch named in a task's
// brief, before anything tries to check it out.
func CheckBranchName(name string) error {
	// Names git reserves, or would read as an option
	switch {
	case name == "":
		return fmt.Errorf("branch name is empty")
	case name == "HEAD":
		return fmt.Errorf("branch name %q is reserved", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("branch name %q starts with '-'", name)
	}

	// Characters and sequences git forbids anywhere in a ref
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("branch name %q contains a control character", name)
		}
		if strings.ContainsRune(" ~^:?*[\\", r) {
			return fmt.Errorf("branch name %q contains %q", name, r)
		}
	}
	for _, seq := range []string{"..", "@{", "//"} {
		if strings.Contains(name, seq) {
			return fmt.Errorf("branch name %q contains %q", name, seq)
		}
	}

	// Rules for how the name, and each slash-separated part, may begin and end
	if strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") {
		return fmt.Errorf("branch name %q starts or ends with '/'", name)
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf("branch name %q ends with '.'", name)
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return fmt.Errorf("branch name %q has a part starting with '.'", name)
		}
		if strings.HasSuffix(part, ".lock") {
			return fmt.Errorf("branch name %q has a part ending with \".lock\"", name)
		}
	}
	return nil
}
