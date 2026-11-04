package collectors

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// gapFakeGH answers every state search except the watch scan with no PRs, and
// the watched PR's detail lookup with a passing PR.
func gapFakeGH(t *testing.T, watchPage string, extra ...ghRule) *fakeGH {
	t.Helper()
	gh := newFakeGH(t)
	gh.on(ghRule{Pattern: "is:pr is:open repo:my-org/my-repo", Out: watchPage})
	for _, r := range extra {
		gh.on(r)
	}
	gh.on(ghRule{Pattern: "--state", Out: "[]"})
	gh.on(ghRule{Pattern: "--review-requested my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "--assignee my-user", Out: "[]"})
	gh.on(ghRule{Pattern: "-F number=7", Out: prDetailJSON("teammate")})
	return gh
}

// withIssueCount sets the search total a watch page reports.
func withIssueCount(page string, n int) string {
	return strings.Replace(page, `"search":{`, fmt.Sprintf(`"search":{"issueCount":%d,`, n), 1)
}

// collectorErrors returns the collector_error rows among items.
func collectorErrors(items []StatusItem) []StatusItem {
	var out []StatusItem
	for _, it := range items {
		if it.Kind == "collector_error" {
			out = append(out, it)
		}
	}
	return out
}

// TestWatchScanReportsTruncation checks that a watch search GitHub cut short
// at its result cap still returns the PRs it found, plus a warning saying how
// many went unchecked.
func TestWatchScanReportsTruncation(t *testing.T) {
	watched := fixturePR{Number: 7, Author: "teammate", Paths: []string{"services/api/main.go"}}
	gapFakeGH(t, withIssueCount(watchPageJSON(t, watched), 1200))

	items, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}

	// The found PR comes first, then one warning naming the shortfall
	if len(items) != 2 || items[0].URL != watched.url() {
		t.Fatalf("want the watched PR then a gap row, got %+v", items)
	}
	errs := collectorErrors(items)
	if len(errs) != 1 {
		t.Fatalf("want 1 collector_error, got %+v", errs)
	}
	got := errs[0]
	if got.Title != `GitHub: watch "api" scan truncated` || got.Severity != "warning" || got.DirectiveID != "gh" {
		t.Fatalf("unexpected gap row: %+v", got)
	}
	if !strings.Contains(got.Summary, "matched 1200 open PRs") || !strings.Contains(got.Summary, "the last 1199 ") {
		t.Fatalf("summary should give the counts: %q", got.Summary)
	}
}

// TestWatchScanReportsFailedFileListing checks that a large PR whose file list
// can't be read is left out of the watched set and named in an error row,
// rather than vanishing as though it touched nothing watched.
func TestWatchScanReportsFailedFileListing(t *testing.T) {
	watched := fixturePR{Number: 7, Author: "teammate", Paths: []string{"services/api/main.go"}}
	large := fixturePR{Number: 8, Author: "teammate", Paths: []string{"web/app.ts"}, TotalFiles: 500}
	gh := gapFakeGH(t, watchPageJSON(t, watched, large),
		ghRule{Pattern: "pulls/8/files", Out: "HTTP 502: Bad Gateway", Exit: 1})

	items, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}

	// Only the PR known to touch the watched path is listed
	if len(items) != 2 || items[0].URL != watched.url() {
		t.Fatalf("want the watched PR then a gap row, got %+v", items)
	}
	if n := gh.countCalls("-F number=8"); n != 0 {
		t.Fatalf("the unlisted PR must not be resolved, got %d detail calls", n)
	}

	// The error row names the PR it could not check
	errs := collectorErrors(items)
	if len(errs) != 1 {
		t.Fatalf("want 1 collector_error, got %+v", errs)
	}
	got := errs[0]
	if got.Title != `GitHub: watch "api" file listings failed` || got.Severity != "error" {
		t.Fatalf("unexpected gap row: %+v", got)
	}
	if !strings.Contains(got.Summary, large.url()) {
		t.Fatalf("summary should name %s: %q", large.url(), got.Summary)
	}
}

// TestWatchScanCompleteAddsNoGapRows checks that a scan that saw every match
// and listed every large PR reports nothing extra.
func TestWatchScanCompleteAddsNoGapRows(t *testing.T) {
	watched := fixturePR{Number: 7, Author: "teammate", Paths: []string{"web/app.ts"}, TotalFiles: 500}
	gapFakeGH(t, withIssueCount(watchPageJSON(t, watched), 1),
		ghRule{Pattern: "pulls/7/files", Out: "web/app.ts\nservices/api/main.go\n"})

	items, err := GitHubCollector{Clock: time.Now}.CollectState(context.Background(), watchDirective(), stateOpts())
	if err != nil {
		t.Fatalf("CollectState: %v", err)
	}
	if len(items) != 1 || items[0].URL != watched.url() || len(collectorErrors(items)) != 0 {
		t.Fatalf("want only the watched PR, got %+v", items)
	}
}
