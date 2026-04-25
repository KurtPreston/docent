package statuscache

import (
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

func TestAnnotateDetectsNewAndUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Unix(1700000000, 0)
	items := []collectors.StatusItem{{
		DirectiveID: "d1",
		Source:      "local-git",
		Kind:        "repository_status",
		Title:       "repo",
		Summary:     "Working tree clean",
		ObservedAt:  now,
		Fields:      map[string]string{"path": "/tmp/r1"},
	}}
	out, err := Annotate(dir, items, now)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].ChangeState != "new" {
		t.Fatalf("first run: change=%q", out[0].ChangeState)
	}
	id := out[0].StableID
	if id == "" {
		t.Fatal("empty stable id")
	}

	out2, err := Annotate(dir, append([]collectors.StatusItem(nil), items...), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if out2[0].ChangeState != "unchanged" {
		t.Fatalf("second run: change=%q", out2[0].ChangeState)
	}
	if out2[0].StableID != id {
		t.Fatalf("stable id drift: %s vs %s", out2[0].StableID, id)
	}

	items[0].Summary = "Working tree has 1 changed file(s)"
	out3, err := Annotate(dir, items, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if out3[0].ChangeState != "updated" {
		t.Fatalf("after edit: change=%q", out3[0].ChangeState)
	}
}
