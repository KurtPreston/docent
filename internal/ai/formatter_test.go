package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

func TestRepoChronologicalFormatter(t *testing.T) {
	t1 := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	items := []collectors.StatusItem{
		{Repository: "repo-b", Source: "local-git", Kind: "commit", Title: "later", ObservedAt: t3, Fields: map[string]string{"short_hash": "bbb3333"}},
		{Repository: "repo-a", Source: "local-git", Kind: "commit", Title: "first", ObservedAt: t1, Fields: map[string]string{"short_hash": "aaa1111"}},
		{Repository: "repo-a", Source: "local-git", Kind: "commit", Title: "second", ObservedAt: t2, Fields: map[string]string{"short_hash": "aaa2222"}},
		{Source: "cal", Kind: "calendar_event", Title: "Dentist", ObservedAt: t2, Fields: map[string]string{"duration": "1h"}},
		{Kind: "collector_error", DirectiveID: "d-err", Title: "boom", Summary: "network", ObservedAt: t1},
	}
	f := RepoChronologicalFormatter{HeadingLevel: 2}
	out, err := f.Format(items)
	if err != nil {
		t.Fatal(err)
	}
	// repo-a block should list first before second (chronological)
	iFirst := strings.Index(out, "aaa1111:")
	iSecond := strings.Index(out, "aaa2222:")
	if iFirst <= 0 || iSecond <= 0 || iFirst >= iSecond {
		t.Fatalf("chronological order within repo-a:\n%s", out)
	}
	if !strings.Contains(out, "## (no repository)") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "## Collector errors") || !strings.Contains(out, "d-err") {
		t.Fatal(out)
	}
}

func TestJSONSignalListFormatter(t *testing.T) {
	items := []collectors.StatusItem{{
		Source: "s", Kind: "k", Title: "t", ObservedAt: time.Unix(1, 0).UTC(),
	}}
	raw, err := (JSONSignalListFormatter{}).Format(items)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"source"`) || !strings.Contains(raw, `"kind"`) {
		t.Fatal(raw)
	}
}

func TestSelectActivityFormatter(t *testing.T) {
	if got := SelectActivityFormatter(""); got.Name() != activityFormatterRepoChronological {
		t.Fatalf("got %q", got.Name())
	}
	if got := SelectActivityFormatter("repo_chronological"); got.Name() != activityFormatterRepoChronological {
		t.Fatalf("got %q", got.Name())
	}
	if got := SelectActivityFormatter("json-signal-list"); got.Name() != activityFormatterJSONSignalList {
		t.Fatalf("got %q", got.Name())
	}
}
