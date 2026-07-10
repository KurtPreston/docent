package goals_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/KurtPreston/docent/libs/goals"
)

func TestLoadValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "goals.yaml")
	content := `
goals:
  - id: feature-x
    title: Ship feature X
    repos: ["Chip/salsa"]
    ticket_keys: ["SALSA-100"]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := goals.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Goals) != 1 || f.Goals[0].ID != "feature-x" {
		t.Fatalf("%+v", f)
	}
	active := goals.ActiveGoals(f)
	if len(active) != 1 {
		t.Fatalf("active=%d", len(active))
	}
	prompt := goals.AlignmentPrompt(active)
	if prompt == "" {
		t.Fatal("empty prompt")
	}
}

func TestValidateDuplicate(t *testing.T) {
	err := goals.Validate(goals.File{Goals: []goals.Goal{
		{ID: "a", Title: "A"},
		{ID: "a", Title: "B"},
	}})
	if err == nil {
		t.Fatal("expected error")
	}
}
