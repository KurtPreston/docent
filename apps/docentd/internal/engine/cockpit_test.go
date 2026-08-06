package engine

import (
	"context"
	"testing"

	"github.com/KurtPreston/docent/libs/prstatus"
)

// The bug the cockpit exists to fix: a work item whose only evidence is a local
// branch is reported by the dashboard as started + actionRequired, which on a
// real workstation described ~120 stale branches. It must not earn a lane.
func TestBranchEvidenceAloneIsNotActionable(t *testing.T) {
	g := DashboardGroup{
		Key:            "wb:Chip/salsa@some-old-branch",
		Branch:         "some-old-branch",
		Status:         statusStarted,
		StatusRank:     rankStarted,
		ActionRequired: true, // what the dashboard says
		Sessions:       []DashboardSession{},
		PRs:            []DashboardPR{},
	}
	lane, inbox := laneFor(g)
	if lane.AttentionRank < rankNotInCockpit {
		t.Errorf("stale branch earned a lane (%s: %v)", lane.Attention, lane.Reasons)
	}
	if len(inbox) != 0 {
		t.Errorf("stale branch produced inbox items: %+v", inbox)
	}
}

// A shipped ticket whose worktree is still on disk reads as Status "started"
// with a JIRA status like "In QA" or "Done", because Status folds in local
// branch evidence. Only the tier JIRA itself assigned may create a lane; this
// distinction is what took the live cockpit from 88 lanes to a usable handful.
func TestTicketLaneRequiresJiraTier(t *testing.T) {
	cases := []struct {
		name  string
		group DashboardGroup
		want  bool
	}{
		{
			name:  "stale worktree, ticket already in QA",
			group: DashboardGroup{Key: "SALSA-1", Status: statusStarted, JiraStatus: "In QA"},
			want:  false,
		},
		{
			name:  "stale worktree, ticket done",
			group: DashboardGroup{Key: "SALSA-2", Status: statusStarted, JiraStatus: "Done", JiraDone: true},
			want:  false,
		},
		{
			name:  "tier says done but JIRA closed it",
			group: DashboardGroup{Key: "SALSA-3", JiraTier: "started", JiraStatus: "Done", JiraDone: true},
			want:  false,
		},
		{
			name:  "JIRA says in development",
			group: DashboardGroup{Key: "SALSA-4", JiraTier: "started", JiraStatus: "In Development"},
			want:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lane, _ := laneFor(tc.group)
			got := lane.AttentionRank < rankNotInCockpit
			if got != tc.want {
				t.Errorf("in cockpit = %v, want %v (attention %q, reasons %v)", got, tc.want, lane.Attention, lane.Reasons)
			}
		})
	}
}

// A done ticket that still has a window open is legitimately in the cockpit:
// the window is real regardless of what JIRA thinks.
func TestDoneTicketWithOpenWindowStillShows(t *testing.T) {
	g := DashboardGroup{
		Key: "SALSA-5", JiraDone: true, JiraStatus: "Done",
		Sessions: []DashboardSession{{Name: "salsa-5", Status: "needs-followup"}},
	}
	lane, _ := laneFor(g)
	if lane.Attention != AttentionAgentWaiting {
		t.Errorf("attention = %q, want %q", lane.Attention, AttentionAgentWaiting)
	}
}

func TestLaneClassification(t *testing.T) {
	cases := []struct {
		name       string
		group      DashboardGroup
		want       string
		wantReason string
	}{
		{
			name: "agent finished waiting on human",
			group: DashboardGroup{
				Key:      "SALSA-1",
				Sessions: []DashboardSession{{Name: "salsa-1", Status: "needs-followup"}},
			},
			want:       AttentionAgentWaiting,
			wantReason: "agent finished and is waiting on you",
		},
		{
			name: "agent mid-turn",
			group: DashboardGroup{
				Key:      "SALSA-2",
				Sessions: []DashboardSession{{Name: "salsa-2", Status: "working"}},
			},
			want:       AttentionAgentWorking,
			wantReason: "agent is working",
		},
		{
			name: "idle window",
			group: DashboardGroup{
				Key:      "SALSA-3",
				Sessions: []DashboardSession{{Name: "salsa-3", Status: "idle"}},
			},
			want:       AttentionInProgress,
			wantReason: "window open",
		},
		{
			name: "failing checks on my PR",
			group: DashboardGroup{
				Key: "SALSA-4",
				PRs: []DashboardPR{{Title: "fix", Mine: true, Checks: "failing"}},
			},
			want:       AttentionMyTurnPR,
			wantReason: "checks failing",
		},
		{
			name: "changes requested on my PR",
			group: DashboardGroup{
				Key: "SALSA-5",
				PRs: []DashboardPR{{Title: "fix", Mine: true, Checks: "passing", ReviewDecision: "CHANGES_REQUESTED"}},
			},
			want:       AttentionMyTurnPR,
			wantReason: "changes requested",
		},
		{
			name: "approved and unmerged",
			group: DashboardGroup{
				Key: "SALSA-6",
				PRs: []DashboardPR{{Title: "fix", Mine: true, Checks: "passing", ReviewDecision: "APPROVED"}},
			},
			want:       AttentionReadyToMerge,
			wantReason: "approved, not merged",
		},
		{
			name: "someone else's PR",
			group: DashboardGroup{
				Key: "SALSA-7",
				PRs: []DashboardPR{{Title: "theirs", Mine: false}},
			},
			want:       AttentionReviewRequested,
			wantReason: "waiting on your review",
		},
		{
			name: "my draft",
			group: DashboardGroup{
				Key: "SALSA-8",
				PRs: []DashboardPR{{Title: "wip", Mine: true, Draft: true}},
			},
			want:       AttentionInProgress,
			wantReason: "draft PR open",
		},
		{
			name: "my PR simply awaiting review is not my move",
			group: DashboardGroup{
				Key: "SALSA-9",
				PRs: []DashboardPR{{Title: "fix", Mine: true, Checks: "passing", ReviewDecision: ""}},
			},
			want:       AttentionInProgress,
			wantReason: "PR open, awaiting review",
		},
		{
			// The gap the who-acted-last classifier closes: a reviewer commented
			// at PR level without requesting changes and without leaving an
			// unresolved thread, so nothing else here can tell it is my move.
			name: "a reviewer commented with no verdict and no open thread",
			group: DashboardGroup{
				Key: "SALSA-12",
				PRs: []DashboardPR{{
					Title: "fix", Mine: true, Checks: "passing",
					Bucket: string(prstatus.AwaitingAuthor), LastAction: "reviewer",
				}},
			},
			want:       AttentionMyTurnPR,
			wantReason: "a reviewer is waiting on you",
		},
		{
			// A repo with no review policy leaves reviewDecision empty forever,
			// so only the classifier's fallback can see the approval.
			name: "approved on a repo with no review policy",
			group: DashboardGroup{
				Key: "SALSA-13",
				PRs: []DashboardPR{{
					Title: "fix", Mine: true, Checks: "none", ReviewDecision: "",
					Bucket: string(prstatus.ReadyToMerge),
				}},
			},
			want:       AttentionReadyToMerge,
			wantReason: "approved, not merged",
		},
		{
			// Naming CI keeps the cockpit from telling you to go chase a reviewer
			// who cannot start until the checks finish.
			name: "my PR with checks still running says so",
			group: DashboardGroup{
				Key: "SALSA-14",
				PRs: []DashboardPR{{
					Title: "fix", Mine: true, Checks: "pending",
					Bucket: string(prstatus.PendingValidation),
				}},
			},
			want:       AttentionInProgress,
			wantReason: "PR open, checks running",
		},
		{
			// awaiting_review is the author's own ball; it must not be promoted.
			name: "awaiting_review stays in progress",
			group: DashboardGroup{
				Key: "SALSA-15",
				PRs: []DashboardPR{{
					Title: "fix", Mine: true, Checks: "passing",
					Bucket: string(prstatus.AwaitingReview), LastAction: "author",
				}},
			},
			want:       AttentionInProgress,
			wantReason: "PR open, awaiting review",
		},
		{
			name: "assigned ticket",
			group: DashboardGroup{
				Key: "SALSA-10", Status: statusAssigned, JiraTier: "assigned", JiraStatus: "To Do",
			},
			want:       AttentionTodo,
			wantReason: "assigned to you: To Do",
		},
		{
			name: "in-progress ticket",
			group: DashboardGroup{
				Key: "SALSA-11", Status: statusStarted, JiraTier: "started", JiraStatus: "In Development",
			},
			want:       AttentionInProgress,
			wantReason: "ticket in progress: In Development",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lane, _ := laneFor(tc.group)
			if lane.Attention != tc.want {
				t.Errorf("attention = %q, want %q (reasons %v)", lane.Attention, tc.want, lane.Reasons)
			}
			found := false
			for _, r := range lane.Reasons {
				if r == tc.wantReason {
					found = true
				}
			}
			if !found {
				t.Errorf("reasons = %v, want to include %q", lane.Reasons, tc.wantReason)
			}
		})
	}
}

// Only threads whose last word was someone else's are the user's turn; a thread
// already replied to is waiting on the reviewer.
func TestUnresolvedThreadsOnlyCountWhenAwaitingMe(t *testing.T) {
	g := DashboardGroup{
		Key: "SALSA-20",
		PRs: []DashboardPR{{
			Title: "fix", Mine: true, Checks: "passing", PRNumber: 7,
			URL: "https://gh/o/r/pull/7", Unresolved: 3,
			Threads: []DashboardThread{
				{ID: "a", Author: "bob", Body: "why?", File: "a.ts", Line: 4, Mine: false, UpdatedAt: "2026-01-03T00:00:00Z"},
				{ID: "b", Author: "carol", Body: "nit", File: "b.ts", Mine: false},
				{ID: "c", Author: "bob", Body: "q", Mine: true},
			},
		}},
	}
	lane, inbox := laneFor(g)
	if lane.Attention != AttentionMyTurnPR {
		t.Errorf("attention = %q, want %q", lane.Attention, AttentionMyTurnPR)
	}
	wantReason := "2 unresolved comments"
	found := false
	for _, r := range lane.Reasons {
		if r == wantReason {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons = %v, want %q", lane.Reasons, wantReason)
	}
	comments := 0
	for _, it := range inbox {
		if it.Kind == "review-comment" {
			comments++
			if it.Body == "" || it.Author == "" {
				t.Errorf("comment item missing body/author: %+v", it)
			}
		}
	}
	if comments != 2 {
		t.Errorf("got %d review-comment items, want 2 (the one I answered is not mine to act on)", comments)
	}
	// A file/line is what makes the comment actionable without opening GitHub.
	for _, it := range inbox {
		if it.Kind == "review-comment" && it.File == "a.ts" && it.Line != 4 {
			t.Errorf("line lost on %+v", it)
		}
	}
}

// An unclassified PR (the collector could not read its timeline) must keep
// working off the individual signals rather than losing them to an empty bucket.
func TestUnclassifiedPRStillUsesRawSignals(t *testing.T) {
	for _, tc := range []struct {
		name string
		pr   DashboardPR
		want string
	}{
		{"failing checks", DashboardPR{Title: "a", Mine: true, Checks: "failing"}, AttentionMyTurnPR},
		{"changes requested", DashboardPR{Title: "b", Mine: true, Checks: "passing", ReviewDecision: "CHANGES_REQUESTED"}, AttentionMyTurnPR},
		{"approved", DashboardPR{Title: "c", Mine: true, Checks: "passing", ReviewDecision: "APPROVED"}, AttentionReadyToMerge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.pr.Bucket != "" {
				t.Fatal("this test is about the unclassified case")
			}
			lane, _ := laneFor(DashboardGroup{Key: "K", PRs: []DashboardPR{tc.pr}})
			if lane.Attention != tc.want {
				t.Errorf("attention = %q, want %q (reasons %v)", lane.Attention, tc.want, lane.Reasons)
			}
		})
	}
}

// The bucket must not double-report: a PR that already has an unresolved comment
// is my turn for that reason, and adding "a reviewer is waiting on you" on top
// would show the same fact twice in the same lane.
func TestBucketDoesNotDuplicateAnExistingReason(t *testing.T) {
	g := DashboardGroup{
		Key: "SALSA-21",
		PRs: []DashboardPR{{
			Title: "fix", Mine: true, Checks: "passing",
			Bucket: string(prstatus.AwaitingAuthor),
			Threads: []DashboardThread{
				{ID: "a", Author: "bob", Body: "why?", Mine: false},
			},
		}},
	}
	lane, _ := laneFor(g)
	if lane.Attention != AttentionMyTurnPR {
		t.Fatalf("attention = %q, want %q", lane.Attention, AttentionMyTurnPR)
	}
	for _, r := range lane.Reasons {
		if r == "a reviewer is waiting on you" {
			t.Errorf("reasons = %v: the unresolved comment already said this", lane.Reasons)
		}
	}
}

// A lane can qualify several ways; it takes the most urgent but keeps every
// reason, so the UI can explain the whole picture rather than just the winner.
func TestMostUrgentWinsButAllReasonsKept(t *testing.T) {
	g := DashboardGroup{
		Key:      "SALSA-30",
		Sessions: []DashboardSession{{Name: "s", Status: "needs-followup"}},
		PRs: []DashboardPR{
			{Title: "mine", Mine: true, Checks: "failing"},
			{Title: "theirs", Mine: false},
		},
	}
	lane, _ := laneFor(g)
	if lane.Attention != AttentionAgentWaiting {
		t.Errorf("attention = %q, want %q (agent-waiting outranks everything)", lane.Attention, AttentionAgentWaiting)
	}
	if len(lane.Reasons) < 3 {
		t.Errorf("reasons = %v, want the agent, the failing checks, and the review request", lane.Reasons)
	}
}

func TestCockpitFiltersAndCounts(t *testing.T) {
	e := &Engine{}
	e.lastDashboard = Dashboard{Groups: []DashboardGroup{
		{Key: "keep-agent", Sessions: []DashboardSession{{Name: "a", Status: "needs-followup"}}},
		{Key: "keep-pr", PRs: []DashboardPR{{Title: "p", Mine: true, Checks: "failing"}}},
		{Key: "keep-todo", Status: statusAssigned, JiraTier: "assigned", JiraStatus: "To Do"},
		{Key: "drop-branch-1", Status: statusStarted, ActionRequired: true},
		{Key: "drop-branch-2", Status: statusStarted, ActionRequired: true},
		{Key: "drop-empty"},
	}}

	got := e.Cockpit(context.Background())
	if got.Total != 6 {
		t.Errorf("Total = %d, want 6 (the cockpit reports what it hid)", got.Total)
	}
	if len(got.Lanes) != 2 {
		keys := make([]string, 0, len(got.Lanes))
		for _, l := range got.Lanes {
			keys = append(keys, l.Key)
		}
		t.Fatalf("lanes = %v, want the 2 in-flight ones (the todo goes to Queue)", keys)
	}
	// Most urgent first.
	if got.Lanes[0].Key != "keep-agent" || got.Lanes[1].Key != "keep-pr" {
		t.Errorf("lane order = %q, %q", got.Lanes[0].Key, got.Lanes[1].Key)
	}
	if len(got.Queue) != 1 || got.Queue[0].Key != "keep-todo" {
		t.Errorf("queue = %+v, want keep-todo", got.Queue)
	}
	if got.Counts.AgentWaiting != 1 || got.Counts.MyTurnPR != 1 || got.Counts.Todo != 1 {
		t.Errorf("counts = %+v", got.Counts)
	}
	if len(got.Inbox) == 0 {
		t.Error("inbox should carry the agent-waiting and failing-checks items")
	}
}

// The assigned-but-not-started backlog must stay out of the lane rail: on this
// workstation it is 43 tickets against ~20 real lanes, so mixing them buries
// the work in flight.
func TestQueueIsSeparateFromLanes(t *testing.T) {
	e := &Engine{}
	e.lastDashboard = Dashboard{Groups: []DashboardGroup{
		{Key: "SALSA-1", JiraTier: "assigned", JiraStatus: "To Do"},
		{Key: "SALSA-2", JiraTier: "assigned", JiraStatus: "Backlog"},
		{Key: "SALSA-3", JiraTier: "assigned", JiraStatus: "Backlog"},
		{Key: "SALSA-4", JiraTier: "started", JiraStatus: "In Development"},
		{Key: "SALSA-5", PRs: []DashboardPR{{Title: "p", Mine: true, Checks: "failing"}}},
	}}

	got := e.Cockpit(context.Background())
	laneKeys := make([]string, 0, len(got.Lanes))
	for _, l := range got.Lanes {
		laneKeys = append(laneKeys, l.Key)
	}
	if len(got.Lanes) != 2 {
		t.Errorf("lanes = %v, want only the in-flight work", laneKeys)
	}
	if len(got.Queue) != 3 {
		t.Errorf("queue has %d entries, want the 3 assigned tickets", len(got.Queue))
	}
	// Grouped by status name so "To Do" and "Backlog" separate without docent
	// hardcoding either name.
	if got.Queue[0].JiraStatus != "Backlog" || got.Queue[2].JiraStatus != "To Do" {
		t.Errorf("queue not grouped by status: %q, %q, %q",
			got.Queue[0].JiraStatus, got.Queue[1].JiraStatus, got.Queue[2].JiraStatus)
	}
	// Actionable is the badge number: the backlog and in-progress work are not
	// waiting on a decision.
	if got.Counts.Actionable != 1 {
		t.Errorf("Actionable = %d, want 1 (only the failing PR)", got.Counts.Actionable)
	}
	if got.Counts.Todo != 3 {
		t.Errorf("Todo = %d, want 3", got.Counts.Todo)
	}
}

// Every lane must be able to say why it is there; an unexplained lane is the
// dashboard's failure mode repeated.
func TestEveryLaneHasAReason(t *testing.T) {
	e := &Engine{}
	e.lastDashboard = Dashboard{Groups: []DashboardGroup{
		{Key: "a", Sessions: []DashboardSession{{Name: "a", Status: "idle"}}},
		{Key: "b", PRs: []DashboardPR{{Title: "p", Mine: true, Checks: "passing", ReviewDecision: "APPROVED"}}},
		{Key: "c", Status: statusAssigned, JiraTier: "assigned"},
	}}
	for _, lane := range e.Cockpit(context.Background()).Lanes {
		if len(lane.Reasons) == 0 {
			t.Errorf("lane %q has no reason", lane.Key)
		}
	}
}
