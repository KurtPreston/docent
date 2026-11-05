package worktree

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// A configured cone limits docent's new worktree to those directories plus the
// repository root's own files, and leaves the tree clean.
func TestResolveSparseLimitsTheCheckout(t *testing.T) {
	requireGit(t)
	f := newForge(t)
	remote := f.repo("Chip/salsa")
	code := t.TempDir()
	local := f.clone(code, "salsa", remote)

	// Publish a few directories, only some of which the cone includes.
	for _, p := range []string{"apps/web/main.go", "apps/api/main.go", "ci/build.sh", "docs/readme.md"} {
		path := filepath.Join(local, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitAt(t, local, "add", ".")
	gitAt(t, local, "commit", "-qm", "layout")
	gitAt(t, local, "push", "-q", "origin", "HEAD")

	res, err := Resolve(context.Background(), Request{
		Repo: "Chip/salsa", Branch: "narrow", Roots: []string{code}, StateRoot: t.TempDir(),
		Sparse: []string{"apps/web", "ci"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Only the cone's directories and the root's files are checked out.
	for p, want := range map[string]bool{
		"apps/web/main.go": true, "ci/build.sh": true,
		"apps/api/main.go": false, "docs/readme.md": false,
	} {
		if got := exists(filepath.Join(res.Dir, filepath.FromSlash(p))); got != want {
			t.Errorf("%s present = %v; want %v", p, got, want)
		}
	}
	entries, _ := os.ReadDir(res.Dir)
	if len(entries) < 3 {
		t.Errorf("root has %d entries; want its own files kept alongside the cone", len(entries))
	}
	if dirty, err := IsDirty(context.Background(), res.Dir); err != nil || dirty {
		t.Errorf("IsDirty = %v, %v; a sparse tree must start clean", dirty, err)
	}
}

// SparseFor matches repositories case-insensitively and tidies the list.
func TestSparseFor(t *testing.T) {
	cfg := map[string][]string{"My-Org/My-Repo": {" apps/web/ ", "", "ci", "ci", "."}}
	if got, want := SparseFor(cfg, "my-org/my-repo"), []string{"apps/web", "ci"}; !reflect.DeepEqual(got, want) {
		t.Errorf("SparseFor = %v; want %v", got, want)
	}
	if got := SparseFor(cfg, "my-org/other"); got != nil {
		t.Errorf("SparseFor(unlisted) = %v; want nil", got)
	}
}
