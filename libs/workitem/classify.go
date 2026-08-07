// Package workitem holds the pure status-classification core shared by the
// docentd dashboard and the report pipeline. Callers accumulate Facts from
// entity evidence (sessions, PRs, JIRA tiers, local branches) and Classify
// maps those facts to a status + display rank + action_required flag.
package workitem

import "github.com/KurtPreston/docent/libs/model"

// Work-item status tiers, ordered by display priority (lower rank sorts
// first). A work-item takes the lowest-rank status it qualifies for; one
// with no qualifying status is hidden from the dashboard.
const (
	StatusDone     = "done"
	StatusActive   = "active"
	StatusApproved = "approved"
	StatusStarted  = "started"
	StatusAwaiting = "awaiting-response"
	StatusAssigned = "assigned"
	// StatusReviewable is somebody else's open PR that nobody has asked the
	// user to look at. It ranks below even assigned work because it is the only
	// tier that is not the user's in any sense: it is a list to pick from.
	StatusReviewable = "reviewable"

	RankDone       = 0
	RankActive     = 1
	RankApproved   = 2
	RankStarted    = 3
	RankAwaiting   = 4
	RankAssigned   = 5
	RankReviewable = 6
	RankHidden     = 99
)

// Facts accumulates the entity-derived signals a work-item needs to be
// classified into a status + action_required.
type Facts struct {
	Done                 bool // report-side: merged/closed authored PR or JIRA done category
	HasLiveSession       bool // a session with a fresh heartbeat (actively live)
	HasOpenSession       bool // an IDE window is registered/open for this work item, even if idle
	SessionNeedsFollowup bool
	AuthoredApproved     bool // authored, non-draft, approved, checks passing/none
	AuthoredDraft        bool // authored draft PR
	AuthoredAwaiting     bool // authored, non-draft, not approved
	AuthoredMyTurn       bool // authored, non-draft, changes-requested or failing checks
	ReviewRequested      bool // someone else's PR awaiting my review
	ReviewCandidate      bool // someone else's PR nobody has asked me to review
	JiraStarted          bool
	JiraAssigned         bool
	BranchEvidence       bool // a local branch/commit/reflog/session ties work to the ticket
}

// ClassifyPR folds one PR entity's state into the group facts. An unowned PR
// means either that my review is pending on it or that it is merely one I could
// review, which are different enough to rank apart: the first is a request and
// the second is an option.
//
// Ownership is read from the collector's `mine` and `reviewable` flags rather
// than the relation name, because directives can declare their own relations (a
// CI bot's backports are owned but are neither "authored" nor
// "review_requested").
func ClassifyPR(facts *Facts, ent model.Entity) {
	if facts == nil || ent.State == nil {
		return
	}
	if ent.State["reviewable"] == "true" {
		facts.ReviewCandidate = true
		return
	}
	if ent.State["mine"] == "false" {
		facts.ReviewRequested = true
		return
	}
	// Treat everything else (owned PRs, and rows predating the flag) as my
	// own.
	draft := ent.State["is_draft"] == "true"
	if draft {
		facts.AuthoredDraft = true
		return
	}
	decision := ent.State["review_decision"]
	checks := ent.State["checks"]
	if decision == "APPROVED" && (checks == "passing" || checks == "none") {
		facts.AuthoredApproved = true
		return
	}
	facts.AuthoredAwaiting = true
	if decision == "CHANGES_REQUESTED" || checks == "failing" {
		facts.AuthoredMyTurn = true
	}
}

// Classify maps accumulated facts to (status, rank, action_required),
// choosing the lowest-rank status the work-item qualifies for. Returns
// RankHidden when nothing matches so the caller can drop the group.
//
// StatusDone is report-side only: the dashboard never sets Facts.Done, so
// done work items stay hidden there (nothing else matches either).
func Classify(f Facts) (status string, rank int, actionRequired bool) {
	switch {
	case f.Done:
		return StatusDone, RankDone, false
	case f.HasLiveSession || f.HasOpenSession:
		// An open IDE window (live heartbeat or merely registered/idle) pins
		// the work item to the top; a live session that needs follow-up also
		// flags action-required.
		return StatusActive, RankActive, f.SessionNeedsFollowup
	case f.AuthoredApproved:
		return StatusApproved, RankApproved, true // not merged yet
	case f.JiraStarted || f.AuthoredDraft || f.BranchEvidence:
		return StatusStarted, RankStarted, f.BranchEvidence
	case f.AuthoredAwaiting || f.ReviewRequested:
		return StatusAwaiting, RankAwaiting, f.ReviewRequested || f.AuthoredMyTurn
	case f.JiraAssigned:
		return StatusAssigned, RankAssigned, false
	// Never action-required: nobody has asked for this, and a surface that
	// flags every open PR on the team is the surface people stop reading.
	case f.ReviewCandidate:
		return StatusReviewable, RankReviewable, false
	default:
		return "", RankHidden, false
	}
}
