package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

func TestGiteaCollectActivityFiltersRepos(t *testing.T) {
	t.Setenv("T", "secret-token")

	since := time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	repos := []giteaRepo{
		{FullName: "u/old", Name: "old", HTMLURL: "http://x/old", Updated: "2026-04-01T12:00:00Z", DefaultBranch: "main"},
		{FullName: "u/new", Name: "new", HTMLURL: "http://x/new", Updated: "2026-04-20T12:00:00Z", DefaultBranch: "main"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(repos)
	}))
	t.Cleanup(srv.Close)

	c := GiteaCollector{Clock: func() time.Time { return now }, HTTP: srv.Client()}
	directive := userdata.Directive{
		ID:        "g1",
		Name:      "Gitea",
		Collector: "gitea",
		Enabled:   true,
		Config: map[string]string{
			"base_url": srv.URL,
		},
		Target: map[string]string{
			"owner": "kurt",
		},
		CredentialRefs: map[string]string{"token": "T"},
	}
	opts := &CollectOpts{UserdataDir: t.TempDir()}
	items, err := c.CollectActivity(context.Background(), directive, since, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 repo in window, got %d %#v", len(items), items)
	}
	if items[0].Title != "u/new" {
		t.Fatalf("unexpected item: %#v", items[0])
	}
	if items[0].Kind != "repository_updated" {
		t.Fatalf("kind: %s", items[0].Kind)
	}
}
