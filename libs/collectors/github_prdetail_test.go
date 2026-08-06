package collectors

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/prstatus"
)

// decodePRDetail is the parse-and-build path fetchPRDetail runs on a reply body.
func decodePRDetail(t *testing.T, raw, viewer string) prDetail {
	t.Helper()
	var resp ghPRDetailResponse
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Repository.PullRequest == nil {
		t.Fatal("pullRequest decoded as nil")
	}
	return buildPRDetail(*resp.Data.Repository.PullRequest, viewer)
}

// The shape of a real reply, exercising every branch buildPRDetail has to get
// right at once: a bot comment that must not count, a commit dated by
// committedDate, a thread-only review reply, and an unresolved thread.
func TestPRDetailDecodesAConsolidatedReply(t *testing.T) {
	raw := `{"data":{"repository":{"pullRequest":{
    "isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED",
    "updatedAt":"2026-08-05T18:00:00Z","headRefName":"SALSA-1/fix",
    "author":{"login":"alice","__typename":"User"},
    "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]},
    "reviewThreads":{"nodes":[
      {"id":"t1","isResolved":false,"isOutdated":false,"path":"a.ts","line":4,
       "comments":{"nodes":[{"author":{"login":"bob"},"body":"why?","url":"u1","createdAt":"2026-08-05T09:00:00Z"}]}}]},
    "reviews":{"nodes":[
      {"state":"COMMENTED","createdAt":"2026-08-05T09:00:00Z","submittedAt":"2026-08-05T09:00:00Z","body":"q","author":{"login":"bob","__typename":"User"}},
      {"state":"COMMENTED","createdAt":"2026-08-05T15:00:00Z","submittedAt":"2026-08-05T15:00:00Z","body":"answered","author":{"login":"alice","__typename":"User"}}]},
    "timelineItems":{"nodes":[
      {"__typename":"PullRequestCommit","commit":{"committedDate":"2026-08-05T08:00:00Z"}},
      {"__typename":"IssueComment","createdAt":"2026-08-05T17:00:00Z","body":"build finished","author":{"login":"ci-bot","__typename":"Bot"}},
      {"__typename":"PullRequestReview","submittedAt":"2026-08-05T09:00:00Z","state":"COMMENTED","author":{"login":"bob","__typename":"User"}}]}
  }}}}`

	got := decodePRDetail(t, raw, "alice")

	if got.Checks != "passing" {
		t.Errorf("checks = %q, want passing", got.Checks)
	}
	if got.ReviewDecision != "REVIEW_REQUIRED" {
		t.Errorf("reviewDecision = %q", got.ReviewDecision)
	}
	if got.HeadBranch != "SALSA-1/fix" {
		t.Errorf("headBranch = %q", got.HeadBranch)
	}
	if got.Mergeable != "mergeable" {
		t.Errorf("mergeable = %q", got.Mergeable)
	}
	if len(got.Threads) != 1 || got.Threads[0].ID != "t1" || got.Threads[0].Mine {
		t.Errorf("threads = %+v, want one thread awaiting me", got.Threads)
	}
	// The author's 15:00 thread reply is the newest real action: the 17:00 bot
	// comment does not count, so the ball is back with the reviewers.
	if got.Status.Bucket != prstatus.AwaitingReview {
		t.Errorf("bucket = %q, want %q", got.Status.Bucket, prstatus.AwaitingReview)
	}
	if got.Status.Side != prstatus.SideAuthor {
		t.Errorf("side = %q, want %q", got.Status.Side, prstatus.SideAuthor)
	}
	if want := time.Date(2026, 8, 5, 15, 0, 0, 0, time.UTC); !got.Status.At.Equal(want) {
		t.Errorf("last action at = %v, want %v", got.Status.At, want)
	}
}

// A null rollup means the head commit has no checks configured. Reading that as
// "unknown" would strand every PR in a repo without CI outside ready_to_merge.
func TestNullRollupIsNoneNotUnknown(t *testing.T) {
	raw := `{"data":{"repository":{"pullRequest":{
    "isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"APPROVED",
    "updatedAt":"2026-08-05T18:00:00Z","headRefName":"b",
    "author":{"login":"alice","__typename":"User"},
    "commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]},
    "reviewThreads":{"nodes":[]},"reviews":{"nodes":[]},"timelineItems":{"nodes":[]}
  }}}}`
	got := decodePRDetail(t, raw, "alice")
	if got.Checks != "none" {
		t.Fatalf("checks = %q, want none", got.Checks)
	}
	if got.Status.Bucket != prstatus.ReadyToMerge {
		t.Fatalf("bucket = %q, want %q", got.Status.Bucket, prstatus.ReadyToMerge)
	}
}

// The partial-data case this query is designed around: a token that cannot read
// checks gets a null rollup container alongside otherwise-complete data. The PR
// must still classify on what did arrive rather than being discarded.
func TestPartialReplyStillClassifies(t *testing.T) {
	// The errors array is what makes `gh api graphql` exit non-zero, which is why
	// fetchPRDetail parses the body regardless of exit code.
	raw := `{"data":{"repository":{"pullRequest":{
    "isDraft":false,"mergeable":"CONFLICTING","reviewDecision":"APPROVED",
    "updatedAt":"2026-08-05T18:00:00Z","headRefName":"b",
    "author":{"login":"alice","__typename":"User"},
    "commits":{"nodes":[]},
    "reviewThreads":{"nodes":[]},"reviews":{"nodes":[]},"timelineItems":{"nodes":[]}
  }}},"errors":[{"message":"Resource not accessible by integration"}]}`

	got := decodePRDetail(t, raw, "alice")
	if got.Checks != "unknown" {
		t.Errorf("checks = %q, want unknown (no commit node)", got.Checks)
	}
	if got.Mergeable != "conflicting" {
		t.Errorf("mergeable = %q, want conflicting: the readable fields must survive", got.Mergeable)
	}
	// An unreadable rollup must not hide an approved, mergeable PR.
	if got.Status.Bucket != prstatus.ReadyToMerge {
		t.Errorf("bucket = %q, want %q", got.Status.Bucket, prstatus.ReadyToMerge)
	}
}

// A deleted account decodes as a null author. That must not panic or turn every
// remaining comment into reviewer activity by way of an empty login matching.
func TestNullAuthorIsHandled(t *testing.T) {
	raw := `{"data":{"repository":{"pullRequest":{
    "isDraft":false,"mergeable":"MERGEABLE","reviewDecision":"REVIEW_REQUIRED",
    "updatedAt":"2026-08-05T18:00:00Z","headRefName":"b",
    "author":null,
    "commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"SUCCESS"}}}]},
    "reviewThreads":{"nodes":[]},"reviews":{"nodes":[]},
    "timelineItems":{"nodes":[
      {"__typename":"IssueComment","createdAt":"2026-08-05T10:00:00Z","body":"hi","author":null}]}
  }}}}`
	got := decodePRDetail(t, raw, "alice")
	// An unknown commenter on a PR with an unknown author is a reviewer, not the
	// author: two empty logins must not be treated as the same person.
	if got.Status.Side != prstatus.SideReviewer {
		t.Fatalf("side = %q, want %q", got.Status.Side, prstatus.SideReviewer)
	}
	if got.Status.Bucket != prstatus.AwaitingAuthor {
		t.Fatalf("bucket = %q, want %q", got.Status.Bucket, prstatus.AwaitingAuthor)
	}
}

func TestChecksFromRollupStates(t *testing.T) {
	withRollup := func(r *ghCheckRollup) ghPRDetail {
		node := ghCommitNode{}
		node.Commit.StatusCheckRollup = r
		pr := ghPRDetail{}
		pr.Commits.Nodes = []ghCommitNode{node}
		return pr
	}
	for state, want := range map[string]string{
		"SUCCESS": "passing", "FAILURE": "failing", "ERROR": "failing",
		"PENDING": "pending", "EXPECTED": "pending", "WEIRD": "unknown",
	} {
		if got := checksFromRollup(withRollup(&ghCheckRollup{State: state})); got != want {
			t.Errorf("rollup %q = %q, want %q", state, got, want)
		}
	}
	if got := checksFromRollup(withRollup(nil)); got != "none" {
		t.Errorf("null rollup = %q, want none", got)
	}
	// No commit node at all means the query could not reach the commit, which is
	// genuinely unknown rather than "no checks".
	if got := checksFromRollup(ghPRDetail{}); got != "unknown" {
		t.Errorf("no commit node = %q, want unknown", got)
	}
}
