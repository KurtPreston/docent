package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUniqueOutputPath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "2026-05-07-recent-activity.md")

	got, err := uniqueOutputPath(base)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatalf("first write path: got %q want %q", got, base)
	}

	if err := os.WriteFile(base, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = uniqueOutputPath(base)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "2026-05-07-recent-activity-2.md")
	if got != want {
		t.Fatalf("second path: got %q want %q", got, want)
	}

	if err := os.WriteFile(want, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = uniqueOutputPath(base)
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(dir, "2026-05-07-recent-activity-3.md")
	if got != want {
		t.Fatalf("third path: got %q want %q", got, want)
	}
}
