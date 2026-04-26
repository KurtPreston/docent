package userdata

import "testing"

func TestLoadSignalsMissingFileIsEmpty(t *testing.T) {
	store := NewStore(t.TempDir())
	projects := ProjectsFile{Projects: []Project{{ID: "x", Name: "X"}}}
	tasks := TasksFile{}
	file, err := store.LoadSignals(projects, tasks)
	if err != nil {
		t.Fatalf("LoadSignals: %v", err)
	}
	if len(file.Signals) != 0 {
		t.Fatalf("expected empty, got %#v", file)
	}
}
