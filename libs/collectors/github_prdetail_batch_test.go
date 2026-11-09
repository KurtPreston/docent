package collectors

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// detailNodeJSON is prDetailJSON's pullRequest object with its head branch
// set, so a test can tell which reply landed on which row.
func detailNodeJSON(head string) string {
	full := strings.Replace(prDetailJSON("teammate"), `"headRefName":"fix"`, `"headRefName":"`+head+`"`, 1)
	start := strings.Index(full, `{"state"`)
	return full[start : len(full)-3]
}

// TestBatchedPRDetailFallsBackPerPR checks that one request answers several
// PRs, each reply lands on its own row, and a PR the batch couldn't read is
// looked up on its own.
func TestBatchedPRDetailFallsBackPerPR(t *testing.T) {
	gh := newFakeGH(t)
	gh.on(ghRule{Pattern: "-F k0=", Out: `{"data":{"p0":{"pullRequest":` + detailNodeJSON("one") + `},"p1":{"pullRequest":null},"p2":{"pullRequest":` + detailNodeJSON("three") + `}}}`})
	gh.on(ghRule{Pattern: "-F number=2", Out: strings.Replace(prDetailJSON("teammate"), `"headRefName":"fix"`, `"headRefName":"two"`, 1)})

	// Three of the user's own PRs, resolved together
	var rows []prReviewRow
	for i := 1; i <= 3; i++ {
		rows = append(rows, prReviewRow{spec: prReviewSpec{mine: true}, row: ghSearchActivityRow{URL: fmt.Sprintf("https://github.com/my-org/my-repo/pull/%d", i)}})
	}
	run := ghRun{env: os.Environ(), directive: watchDirective(), opts: stateOpts(), user: "my-user"}
	GitHubCollector{}.resolveReviewRows(context.Background(), run, reviewBatch{rows: rows})

	// Expect one batch, one fallback, and every row with its own answer
	for i, want := range []string{"one", "two", "three"} {
		if got := rows[i].detail.HeadBranch; got != want {
			t.Fatalf("row %d head = %q, want %q (calls %q)", i, got, want, gh.calls())
		}
	}
	if gh.countCalls("-F k0=") != 1 || gh.countCalls("-F number=") != 1 {
		t.Fatalf("calls = %q", gh.calls())
	}
}

// TestPlanReviewLookupsBatchesFullDetailOnly checks that rows needing full
// detail are grouped up to prDetailBatchSize, and other rows go alone.
func TestPlanReviewLookupsBatchesFullDetailOnly(t *testing.T) {
	var rows []prReviewRow
	var indices []int
	for i := 0; i < 13; i++ {
		spec := prReviewSpec{reviewable: true}
		if i == 4 {
			spec = prReviewSpec{} // needs only its head branch
		}
		rows = append(rows, prReviewRow{spec: spec, row: ghSearchActivityRow{URL: fmt.Sprintf("https://github.com/my-org/my-repo/pull/%d", i+1)}})
		indices = append(indices, i)
	}
	var sizes []int
	for _, job := range planReviewLookups(rows, indices) {
		sizes = append(sizes, len(job))
	}
	if fmt.Sprint(sizes) != "[1 10 2]" {
		t.Fatalf("job sizes = %v, want [1 10 2]", sizes)
	}
}
