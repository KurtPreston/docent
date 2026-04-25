package taskupdate

import (
	"testing"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

func TestDeriveSignalIDStable(t *testing.T) {
	a := collectors.StatusItem{
		Source:     "jira",
		Kind:       "issue",
		URL:        "https://x.atlassian.net/browse/ABC-1",
		ProjectID:  "p1",
		DirectiveID: "d1",
		Title:      "old title", // should not affect id
		Fields:     map[string]string{"key": "ABC-1"},
		ObservedAt: time.Now(),
	}
	b := a
	b.Title = "new title"
	if DeriveSignalID(a) != DeriveSignalID(b) {
		t.Fatalf("id should not depend on title")
	}
}
