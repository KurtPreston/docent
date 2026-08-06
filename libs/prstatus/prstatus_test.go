package prstatus

import (
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func commit(at time.Time) Event {
	return Event{Kind: "PullRequestCommit", At: at}
}

func comment(login string, bot bool, body string, at time.Time) Event {
	return Event{Kind: "IssueComment", Author: Actor{Login: login, Bot: bot}, Body: body, At: at}
}

func reviewEvent(login string, at time.Time) Event {
	return Event{Kind: "PullRequestReview", Author: Actor{Login: login}, At: at}
}

// threadReply is a review that only replies inside an existing review thread.
// GitHub leaves these out of timelineItems, so they arrive via Reviews alone.
func threadReply(login, state string, at time.Time) Review {
	return Review{State: state, At: at, Author: Actor{Login: login}}
}

func TestBucketPrecedence(t *testing.T) {
	alice := Actor{Login: "alice"}
	cases := []struct {
		name string
		pr   PR
		want Bucket
	}{
		{
			name: "draft beats failing checks",
			pr:   PR{IsDraft: true, Checks: "failing", Author: alice},
			want: Draft,
		},
		{
			name: "draft beats approval",
			pr:   PR{IsDraft: true, Checks: "passing", ReviewDecision: "APPROVED", Author: alice},
			want: Draft,
		},
		{
			name: "failing checks beat approval",
			pr:   PR{Checks: "failing", ReviewDecision: "APPROVED", Author: alice},
			want: FailingValidation,
		},
		{
			name: "pending checks beat approval",
			pr:   PR{Checks: "pending", ReviewDecision: "APPROVED", Author: alice},
			want: PendingValidation,
		},
		{
			name: "approved and green is ready to merge",
			pr:   PR{Checks: "passing", ReviewDecision: "APPROVED", Author: alice},
			want: ReadyToMerge,
		},
		{
			name: "changes requested is not ready even when green",
			pr:   PR{Checks: "passing", ReviewDecision: "CHANGES_REQUESTED", Author: alice},
			want: AwaitingReview,
		},
		{
			name: "unreviewed and green awaits review",
			pr:   PR{Checks: "passing", ReviewDecision: "REVIEW_REQUIRED", Author: alice},
			want: AwaitingReview,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Classify(tc.pr).Bucket; got != tc.want {
				t.Fatalf("bucket = %q, want %q", got, tc.want)
			}
		})
	}
}

// "none" means the head commit has no checks configured, so there is nothing to
// wait for. Treating it as pending would park every PR in a repo without CI in
// "checks running" forever.
func TestNoChecksCountsAsPassingNotPending(t *testing.T) {
	pr := PR{Checks: "none", ReviewDecision: "APPROVED", Author: Actor{Login: "alice"}}
	if got := Classify(pr).Bucket; got != ReadyToMerge {
		t.Fatalf("bucket = %q, want %q", got, ReadyToMerge)
	}
}

// An unreadable rollup (a token without the checks scope) must not hide an
// otherwise-mergeable PR behind a fake CI state.
func TestUnknownChecksDoNotBlockClassification(t *testing.T) {
	pr := PR{Checks: "unknown", ReviewDecision: "APPROVED", Author: Actor{Login: "alice"}}
	if got := Classify(pr).Bucket; got != ReadyToMerge {
		t.Fatalf("bucket = %q, want %q", got, ReadyToMerge)
	}
}

// Raw GraphQL rollup states classify the same as docent's reduced labels, so a
// caller holding an unreduced value is not silently misclassified.
func TestRawGitHubCheckStatesAreAccepted(t *testing.T) {
	alice := Actor{Login: "alice"}
	for raw, want := range map[string]Bucket{
		"FAILURE":  FailingValidation,
		"ERROR":    FailingValidation,
		"PENDING":  PendingValidation,
		"EXPECTED": PendingValidation,
		"SUCCESS":  AwaitingReview,
		"NONE":     AwaitingReview,
	} {
		if got := Classify(PR{Checks: raw, Author: alice}).Bucket; got != want {
			t.Fatalf("checks %q: bucket = %q, want %q", raw, got, want)
		}
	}
}

// Repos with no review policy leave reviewDecision empty forever, so approval
// has to come from the raw verdicts or nothing there is ever ready to merge.
func TestApprovalFallbackWhenNoReviewPolicy(t *testing.T) {
	alice := Actor{Login: "alice"}
	cases := []struct {
		name    string
		reviews []Review
		want    bool
	}{
		{"approved by a human", []Review{{State: "APPROVED", Author: Actor{Login: "bob"}}}, true},
		{"no reviews at all", nil, false},
		{
			name: "a later approval does not clear an outstanding changes-requested",
			reviews: []Review{
				{State: "APPROVED", Author: Actor{Login: "bob"}},
				{State: "CHANGES_REQUESTED", Author: Actor{Login: "carol"}},
			},
			want: false,
		},
		{"bot approval does not count", []Review{{State: "APPROVED", Author: Actor{Login: "ci", Bot: true}}}, false},
		{"comment-only review is not approval", []Review{{State: "COMMENTED", Author: Actor{Login: "bob"}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := PR{Checks: "passing", Author: alice, Reviews: tc.reviews}
			if got := IsApproved(pr); got != tc.want {
				t.Fatalf("IsApproved = %v, want %v", got, tc.want)
			}
		})
	}
}

// With a decision present, the raw verdicts are ignored: GitHub already applied
// the repo's policy, which may require more approvals than we can count.
func TestReviewDecisionOverridesRawVerdicts(t *testing.T) {
	pr := PR{
		Checks:         "passing",
		ReviewDecision: "REVIEW_REQUIRED",
		Author:         Actor{Login: "alice"},
		Reviews:        []Review{{State: "APPROVED", Author: Actor{Login: "bob"}}},
	}
	if IsApproved(pr) {
		t.Fatal("IsApproved = true, want false: REVIEW_REQUIRED must win over a raw approval")
	}
}

func TestWhoActedLast(t *testing.T) {
	alice := Actor{Login: "alice"}
	cases := []struct {
		name     string
		pr       PR
		wantSide Side
		wantAt   time.Time
	}{
		{
			name:     "nothing in the timeline falls back to author at updatedAt",
			pr:       PR{Author: alice, UpdatedAt: base},
			wantSide: SideAuthor,
			wantAt:   base,
		},
		{
			name: "a reviewer comment after a push puts the ball with the author",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				commit(base.Add(-2 * time.Hour)),
				comment("bob", false, "please rename this", base.Add(-time.Hour)),
			}},
			wantSide: SideReviewer,
			wantAt:   base.Add(-time.Hour),
		},
		{
			name: "a push after a reviewer comment hands it back to the reviewers",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				comment("bob", false, "please rename this", base.Add(-2*time.Hour)),
				commit(base.Add(-time.Hour)),
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-time.Hour),
		},
		{
			name: "the author's own comment is author-side",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				comment("bob", false, "question", base.Add(-2*time.Hour)),
				comment("alice", false, "answered", base.Add(-time.Hour)),
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-time.Hour),
		},
		{
			name: "bot chatter never counts as reviewer activity",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				commit(base.Add(-2 * time.Hour)),
				comment("build-bot", true, "pipeline finished", base.Add(-time.Hour)),
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-2 * time.Hour),
		},
		{
			name: "docent autofix comments never count as reviewer activity",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				commit(base.Add(-2 * time.Hour)),
				comment("kpreston", false, "docent autofix: repaired the lint failure", base.Add(-time.Hour)),
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-2 * time.Hour),
		},
		{
			name: "a ready-for-review event is author-side",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				comment("bob", false, "still a draft?", base.Add(-2*time.Hour)),
				{Kind: "ReadyForReviewEvent", At: base.Add(-time.Hour)},
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-time.Hour),
		},
		{
			name: "a force-push is author-side",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				comment("bob", false, "rebase please", base.Add(-2*time.Hour)),
				{Kind: "HeadRefForcePushedEvent", At: base.Add(-time.Hour)},
			}},
			wantSide: SideAuthor,
			wantAt:   base.Add(-time.Hour),
		},
		{
			name: "out-of-order timeline entries are compared by timestamp",
			pr: PR{Author: alice, UpdatedAt: base, Timeline: []Event{
				comment("bob", false, "newest", base.Add(-time.Hour)),
				commit(base.Add(-3 * time.Hour)),
			}},
			wantSide: SideReviewer,
			wantAt:   base.Add(-time.Hour),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.pr)
			if got.Side != tc.wantSide {
				t.Fatalf("side = %q, want %q", got.Side, tc.wantSide)
			}
			if !got.At.Equal(tc.wantAt) {
				t.Fatalf("at = %v, want %v", got.At, tc.wantAt)
			}
		})
	}
}

// The regression this whole reviews/timeline merge exists for: once discussion
// moves into review threads, GitHub stops reporting it in timelineItems, so an
// author who has answered everything still looks like the one being waited on.
func TestThreadOnlyReplyBeatsAStaleTimeline(t *testing.T) {
	pr := PR{
		Author:    Actor{Login: "alice"},
		Checks:    "passing",
		UpdatedAt: base,
		Timeline: []Event{
			commit(base.Add(-5 * time.Hour)),
			reviewEvent("bob", base.Add(-4*time.Hour)),
		},
		Reviews: []Review{
			threadReply("bob", "COMMENTED", base.Add(-4*time.Hour)),
			threadReply("alice", "COMMENTED", base.Add(-time.Hour)),
		},
	}
	got := Classify(pr)
	if got.Side != SideAuthor {
		t.Fatalf("side = %q, want %q: the author's thread reply is the newest action", got.Side, SideAuthor)
	}
	if got.Bucket != AwaitingReview {
		t.Fatalf("bucket = %q, want %q", got.Bucket, AwaitingReview)
	}
	if !got.At.Equal(base.Add(-time.Hour)) {
		t.Fatalf("at = %v, want %v", got.At, base.Add(-time.Hour))
	}
}

// A PENDING review is an unsubmitted draft only its author can see, so it has
// not moved the ball for anyone else.
func TestPendingReviewIsIgnored(t *testing.T) {
	pr := PR{
		Author:    Actor{Login: "alice"},
		Checks:    "passing",
		UpdatedAt: base,
		Timeline:  []Event{commit(base.Add(-2 * time.Hour))},
		Reviews:   []Review{threadReply("bob", "PENDING", base.Add(-time.Hour))},
	}
	got := Classify(pr)
	if got.Side != SideAuthor || !got.At.Equal(base.Add(-2*time.Hour)) {
		t.Fatalf("side/at = %q/%v, want author/%v", got.Side, got.At, base.Add(-2*time.Hour))
	}
}

// A bot opening and commenting on a PR on your behalf is you, not a reviewer.
func TestBotActingAsTheAuthorCountsAsAuthorSide(t *testing.T) {
	bot := Actor{Login: "release-bot", Bot: true}
	pr := PR{
		Author:    bot,
		Checks:    "passing",
		UpdatedAt: base,
		Timeline: []Event{
			comment("bob", false, "looks fine", base.Add(-2*time.Hour)),
			comment("release-bot", true, "version bumped", base.Add(-time.Hour)),
		},
	}
	if got := Classify(pr); got.Side != SideAuthor {
		t.Fatalf("side = %q, want %q", got.Side, SideAuthor)
	}
}

// Zero timestamps are what a partial GraphQL reply looks like; they must not win
// the "most recent" comparison and drag the sort key back to the epoch.
func TestZeroTimestampsAreIgnored(t *testing.T) {
	pr := PR{
		Author:    Actor{Login: "alice"},
		UpdatedAt: base,
		Timeline: []Event{
			comment("bob", false, "real", base.Add(-time.Hour)),
			comment("carol", false, "no timestamp", time.Time{}),
		},
	}
	if got := Classify(pr); !got.At.Equal(base.Add(-time.Hour)) {
		t.Fatalf("at = %v, want %v", got.At, base.Add(-time.Hour))
	}
}

func TestSortByUrgency(t *testing.T) {
	type row struct {
		name string
		res  Result
	}
	rows := []row{
		{"draft", Result{Bucket: Draft, At: base.Add(-100 * time.Hour)}},
		{"awaiting review, newer", Result{Bucket: AwaitingReview, At: base.Add(-time.Hour)}},
		{"awaiting review, older", Result{Bucket: AwaitingReview, At: base.Add(-9 * time.Hour)}},
		{"failing", Result{Bucket: FailingValidation, At: base}},
		{"ready", Result{Bucket: ReadyToMerge, At: base}},
	}
	SortByUrgency(rows, func(r row) Result { return r.res })

	want := []string{"ready", "failing", "awaiting review, older", "awaiting review, newer", "draft"}
	for i, w := range want {
		if rows[i].name != w {
			t.Fatalf("position %d = %q, want %q (full order: %v)", i, rows[i].name, w, names(rows, func(r row) string { return r.name }))
		}
	}
}

func names[T any](items []T, of func(T) string) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = of(it)
	}
	return out
}

// Every bucket needs a label: an unlabeled one would render as a raw enum in the
// cockpit.
func TestEveryBucketInOrderHasALabel(t *testing.T) {
	if len(Order) != 6 {
		t.Fatalf("Order has %d buckets, want 6", len(Order))
	}
	for _, b := range Order {
		if !b.Valid() {
			t.Fatalf("bucket %q is in Order but has no label", b)
		}
		// Labels are prose, so they must not contain the underscores of the enum.
		// "draft" legitimately labels itself, which is why this checks shape
		// rather than inequality.
		if s := b.Label(); s == "" || strings.Contains(s, "_") {
			t.Fatalf("bucket %q has label %q, want a prose label", b, s)
		}
	}
	if Bucket("nonsense").Valid() {
		t.Fatal("an unknown bucket reported itself as valid")
	}
}
