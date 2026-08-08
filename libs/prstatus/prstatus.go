// Package prstatus classifies an open pull request into exactly one of six
// mutually exclusive buckets, and decides whose court the ball is in.
//
// This is a port of pr-status-monitor's digest logic (itself a port of the older
// pr-review-digest.jq), which stays deployed for the team while docent grows its
// own personal view. The rules encode real GitHub review/CI semantics that took
// a while to get right, so they are worth reusing verbatim rather than
// re-deriving:
//
//   - draft beats everything, because a draft is nobody's to review or merge;
//   - a check rollup of NONE ("the head commit has no checks") counts as
//     passing, not as pending, because there is nothing to wait for;
//   - reviewDecision is authoritative when GitHub sets it, but repos without a
//     review policy leave it empty, so approval falls back to the raw verdicts;
//   - bot chatter and docent's own autofix comments must not count as reviewer
//     activity, or every autofixed PR looks like it is waiting on its author;
//   - a review that only replies inside an existing thread is missing from
//     timelineItems, so the reviews connection has to be merged in by timestamp
//     or a PR whose discussion moved into threads freezes in the wrong bucket.
//
// The port is not a copy: inputs use docent's already-normalized vocabulary
// (Checks as "passing"/"failing"/"pending"/"none"/"unknown", set by the GitHub
// collector's rollup reduction) rather than raw GraphQL enums, so there is one
// normalization step in docent rather than two disagreeing ones.
package prstatus

import (
	"regexp"
	"sort"
	"strings"
	"time"
)

// Bucket is one of the six mutually exclusive states an open PR can be in. The
// set is closed: these are properties of GitHub's review and CI model, not
// something configuration should invent.
type Bucket string

const (
	// AwaitingReview: checks green, not approved, author acted last. The ball is
	// in the reviewers' court.
	AwaitingReview Bucket = "awaiting_review"
	// AwaitingAuthor: checks green, not approved, a reviewer acted last. The
	// ball is back with the author.
	AwaitingAuthor Bucket = "awaiting_author"
	// ReadyToMerge: checks green and approved.
	ReadyToMerge Bucket = "ready_to_merge"
	// FailingValidation: the head commit's check rollup is failing.
	FailingValidation Bucket = "failing_validation"
	// PendingValidation: the head commit's check rollup is still running.
	PendingValidation Bucket = "pending_validation"
	// Draft: the PR is a draft, which takes precedence over every other bucket.
	Draft Bucket = "draft"
)

// Order is the canonical display order, most-actionable first. It differs from
// pr-status-monitor's digest order on purpose: that order suits a team digest
// read top to bottom, while docent's cockpit ranks by "how much does this need
// me right now", which puts a merge-ready PR above a queue of unreviewed ones.
var Order = []Bucket{
	ReadyToMerge,
	FailingValidation,
	AwaitingAuthor,
	AwaitingReview,
	PendingValidation,
	Draft,
}

// ReviewOrder is Order read from the other side of the PR: for somebody else's
// PR, "how much does this need me" inverts almost exactly. A PR waiting on a
// reviewer is the only one that wants anything from you, and an approved one
// wants the least — the opposite of where each sits when the PR is your own.
var ReviewOrder = []Bucket{
	AwaitingReview,
	PendingValidation,
	FailingValidation,
	AwaitingAuthor,
	ReadyToMerge,
	Draft,
}

var labels = map[Bucket]string{
	ReadyToMerge:      "ready to merge",
	FailingValidation: "checks failing",
	AwaitingAuthor:    "awaiting you",
	AwaitingReview:    "awaiting review",
	PendingValidation: "checks running",
	Draft:             "draft",
}

// Label returns a short human label for a bucket, or the raw value when unknown.
func (b Bucket) Label() string {
	if s, ok := labels[b]; ok {
		return s
	}
	return string(b)
}

// Valid reports whether b names a known bucket.
func (b Bucket) Valid() bool {
	_, ok := labels[b]
	return ok
}

// Side names whose court the ball is in after the last meaningful action.
type Side string

const (
	// SideAuthor means the PR author acted last, so others are being waited on.
	SideAuthor Side = "author"
	// SideReviewer means a reviewer acted last, so the author is being waited on.
	SideReviewer Side = "reviewer"
)

// Actor is a GitHub user or bot appearing as an author of something.
type Actor struct {
	Login string
	// Bot is true when GitHub typed the actor as a Bot (GraphQL __typename
	// "Bot"). Bot chatter is excluded from who-acted-last reasoning.
	Bot bool
}

// Review is one submitted review verdict.
type Review struct {
	// State is the GitHub verdict: APPROVED, CHANGES_REQUESTED, COMMENTED,
	// PENDING, DISMISSED.
	State string
	// At is when the review became visible to others (submittedAt), falling
	// back to when it was created.
	At     time.Time
	Body   string
	Author Actor
}

// Event is one timeline entry. Only the kinds that reveal who acted last are
// worth collecting.
type Event struct {
	// Kind is the GraphQL __typename: IssueComment, PullRequestReview,
	// PullRequestCommit, HeadRefForcePushedEvent, or ReadyForReviewEvent.
	Kind string
	// At is the effective timestamp (the commit date for commits, createdAt
	// otherwise).
	At     time.Time
	Body   string
	Author Actor
}

// PR is the input to classification: what docent knows about one open PR.
type PR struct {
	// Author is the PR's author, which is what makes an action "author-side".
	Author  Actor
	IsDraft bool
	// Checks is docent's normalized rollup label: passing, failing, pending,
	// none, or unknown.
	Checks string
	// ReviewDecision is GitHub's verdict (APPROVED / CHANGES_REQUESTED /
	// REVIEW_REQUIRED), empty on repos with no review policy.
	ReviewDecision string
	// UpdatedAt is the fallback timestamp when the timeline says nothing.
	UpdatedAt time.Time
	Reviews   []Review
	Timeline  []Event
}

// Result is the classification of one PR.
type Result struct {
	Bucket Bucket
	// Side is who acted last. It only decides the AwaitingAuthor /
	// AwaitingReview split, but is worth surfacing on its own: "a reviewer is
	// waiting on you" reads differently from "CI is".
	Side Side
	// At is when the ball last moved, which is the honest sort key for a
	// follow-up queue. GitHub's updatedAt moves on label edits and other
	// non-actions, so it is only the fallback.
	At time.Time
}

// Classify buckets a PR and reports who acted last.
func Classify(pr PR) Result {
	side, at := resolveLastAction(pr)
	return Result{Bucket: bucketFor(pr, side), Side: side, At: at}
}

// bucketFor is classification given a precomputed side, so Classify does not
// walk the timeline twice.
func bucketFor(pr PR, side Side) Bucket {
	// A draft is nobody's to review or merge, so it wins over every check and
	// approval state. Checked first for that reason: a draft with red CI is
	// still just a draft.
	if pr.IsDraft {
		return Draft
	}
	switch normalizeChecks(pr.Checks) {
	case "failing":
		return FailingValidation
	case "pending":
		return PendingValidation
	}
	// Passing, none, or unknown: split by approval, then by whose court it is
	// in. "unknown" (docent could not read the rollup, usually a token missing
	// the checks scope) deliberately falls through here rather than becoming its
	// own bucket: an unreadable rollup should not hide a PR that is otherwise
	// approved and mergeable.
	if IsApproved(pr) {
		return ReadyToMerge
	}
	if side == SideReviewer {
		return AwaitingAuthor
	}
	return AwaitingReview
}

// normalizeChecks accepts both docent's labels and raw GitHub rollup states, so
// a caller holding an unreduced GraphQL value still classifies correctly.
func normalizeChecks(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "FAILING", "FAILURE", "ERROR":
		return "failing"
	case "PENDING", "EXPECTED", "IN_PROGRESS", "QUEUED":
		return "pending"
	default:
		// passing, none, unknown, SUCCESS, NONE, ""
		return "passing"
	}
}

// IsApproved trusts reviewDecision when GitHub sets it, and falls back to the
// raw verdicts on repos with no review policy, where reviewDecision is always
// empty and would otherwise make every PR look unapproved forever.
func IsApproved(pr PR) bool {
	switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
	case "APPROVED":
		return true
	case "":
		approved, changes := false, false
		for _, r := range pr.Reviews {
			if r.Author.Bot {
				continue
			}
			switch strings.ToUpper(strings.TrimSpace(r.State)) {
			case "APPROVED":
				approved = true
			case "CHANGES_REQUESTED":
				changes = true
			}
		}
		return approved && !changes
	default:
		// CHANGES_REQUESTED, REVIEW_REQUIRED, or anything GitHub adds later.
		return false
	}
}

// autofixRe matches docent's own autofix comments so an agent that pushed a fix
// does not read as a reviewer waiting on the author.
var autofixRe = regexp.MustCompile(`(?i)docent autofix`)

// resolveLastAction falls back to author-side at UpdatedAt when the timeline has
// nothing to say. Author-side is the right default: a PR nobody has engaged with
// is waiting on reviewers, not on its author.
func resolveLastAction(pr PR) (Side, time.Time) {
	if side, at, ok := lastAction(pr); ok {
		return side, at
	}
	return SideAuthor, pr.UpdatedAt
}

// lastAction returns the most recent non-noise action and whose court it puts
// the ball in.
//
// Reviews are merged with the timeline by timestamp rather than appended,
// because they are two overlapping views of the same history: GitHub omits from
// timelineItems any review that only replies inside an existing review thread,
// so on a PR whose discussion has moved into its threads the timeline stops
// updating and an author who has answered everything still looks like the one
// being waited on.
func lastAction(pr PR) (side Side, at time.Time, ok bool) {
	me := pr.Author.Login
	consider := func(candidate Side, t time.Time) {
		if t.IsZero() || (ok && !t.After(at)) {
			return
		}
		side, at, ok = candidate, t, true
	}
	for _, e := range pr.Timeline {
		switch e.Kind {
		case "PullRequestCommit", "HeadRefForcePushedEvent", "ReadyForReviewEvent":
			// A push is author-side regardless of who pushed it: it is new code
			// to look at, so the ball moves to the reviewers.
			consider(SideAuthor, e.At)
		default: // IssueComment, PullRequestReview
			if s, counts := remarkSide(e.Author, me, e.Body); counts {
				consider(s, e.At)
			}
		}
	}
	for _, r := range pr.Reviews {
		// A PENDING review is an unsubmitted draft nobody else can see yet, so
		// it has not moved the ball.
		if strings.EqualFold(strings.TrimSpace(r.State), "PENDING") {
			continue
		}
		if s, counts := remarkSide(r.Author, me, r.Body); counts {
			consider(s, r.At)
		}
	}
	return side, at, ok
}

// remarkSide reports whose court a comment or review puts the ball in, and
// whether it counts at all.
func remarkSide(a Actor, me, body string) (Side, bool) {
	// A bot is only allowed to speak for the author, covering the case where the
	// PR itself was opened by a bot on your behalf.
	if a.Bot && !strings.EqualFold(a.Login, me) {
		return "", false
	}
	if autofixRe.MatchString(body) {
		return "", false
	}
	if a.Login != "" && strings.EqualFold(a.Login, me) {
		return SideAuthor, true
	}
	return SideReviewer, true
}

// Rank orders buckets by Order, for sorting a mixed list of PRs. Unknown buckets
// sort last.
func Rank(b Bucket) int {
	return rankIn(Order, b)
}

// ReviewRank orders buckets by ReviewOrder, for a list of other people's PRs.
func ReviewRank(b Bucket) int {
	return rankIn(ReviewOrder, b)
}

func rankIn(order []Bucket, b Bucket) int {
	for i, id := range order {
		if id == b {
			return i
		}
	}
	return len(order)
}

// SortByUrgency orders results most-actionable first, breaking ties oldest-first
// so the PR that has been stuck longest is the one you see.
func SortByUrgency[T any](items []T, of func(T) Result) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := of(items[i]), of(items[j])
		if ra, rb := Rank(a.Bucket), Rank(b.Bucket); ra != rb {
			return ra < rb
		}
		return a.At.Before(b.At)
	})
}
