package engine

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/prstatus"
)

// The cockpit is a filtered, re-ranked view of the same work items the
// dashboard shows, built for the question "what should I pick up right now?".
//
// It exists because the dashboard answers a different question. The dashboard
// shows everything correlated, which on a real workstation is ~180 groups, and
// its action_required flag fires on bare local-branch evidence — so ~120 of
// those groups claim to need action because a branch exists on disk. A view
// where nearly everything is flagged carries no signal, which is why the
// dashboard did not survive as a daily surface.
//
// The cockpit therefore inverts the default: a lane appears only when something
// concrete wants a decision from the user, and it must be able to name what.
// Every lane carries at least one Reason, and a lane with no reason is dropped
// rather than shown with an empty explanation.

// Attention buckets, ordered by how urgently they want the user. Lower rank
// sorts first.
const (
	// AttentionAgentWaiting is an agent that finished and is waiting on a human.
	// It sorts above everything: the work is done and blocked only on the user.
	AttentionAgentWaiting = "agent-waiting"
	// AttentionMyTurnPR is a PR of mine a reviewer is waiting on, or whose
	// checks are failing.
	AttentionMyTurnPR = "pr-my-turn"
	// AttentionReadyToMerge is an approved PR of mine that is not merged yet.
	AttentionReadyToMerge = "ready-to-merge"
	// AttentionReviewRequested is someone else's PR waiting on my review.
	AttentionReviewRequested = "review-requested"
	// AttentionAgentWorking is an agent mid-turn: visible, but not actionable.
	AttentionAgentWorking = "agent-working"
	// AttentionInProgress is work with an open window or an in-progress ticket
	// and nothing outstanding.
	AttentionInProgress = "in-progress"
	// AttentionTodo is an assigned ticket not started yet.
	AttentionTodo = "todo"

	rankAgentWaiting    = 0
	rankMyTurnPR        = 1
	rankReadyToMerge    = 2
	rankReviewRequested = 3
	rankAgentWorking    = 4
	rankInProgress      = 5
	rankTodo            = 6
	rankNotInCockpit    = 99
)

// Cockpit is the payload served by GET /api/cockpit.
type Cockpit struct {
	GeneratedAt string `json:"generatedAt"`
	Provider    string `json:"provider,omitempty"`
	SSHHost     string `json:"sshHost,omitempty"`
	// Total is how many work items the dashboard knows about, so the UI can
	// show what fraction the cockpit is hiding rather than pretending the rest
	// does not exist.
	Total  int           `json:"total"`
	Counts CockpitCounts `json:"counts"`
	Lanes  []CockpitLane `json:"lanes"`
	// Queue is work assigned to the user but not started: the "what's next"
	// list. It is kept out of Lanes on purpose. A lane is a place work is
	// happening, and a backlog is not — on this workstation the assigned-to-me
	// query returns 43 tickets, which would be six times the number of real
	// lanes and would bury them.
	Queue []CockpitLane `json:"queue"`
	// Inbox is the flat follow-up queue: individual review comments and PR
	// states that want a response, newest first.
	Inbox []InboxItem `json:"inbox"`
	// Sources reports whether each collector has actually reported yet, so the
	// UI can say "PRs still loading" instead of implying there are none. The
	// GitHub collector takes ~20s from a cold start, and a cockpit that
	// confidently shows an empty inbox during that window is worse than one
	// that admits it does not know yet.
	Sources []CockpitSource `json:"sources"`
}

// CockpitSource is one collector's freshness.
type CockpitSource struct {
	ID      string `json:"id"`
	LastRun string `json:"lastRun,omitempty"`
	Items   int    `json:"items"`
	Error   string `json:"error,omitempty"`
	// Loaded is false before the collector's first successful report.
	Loaded bool `json:"loaded"`
}

// CockpitCounts summarizes the lanes by attention bucket for at-a-glance chips.
type CockpitCounts struct {
	AgentWaiting    int `json:"agentWaiting"`
	MyTurnPR        int `json:"myTurnPR"`
	ReadyToMerge    int `json:"readyToMerge"`
	ReviewRequested int `json:"reviewRequested"`
	AgentWorking    int `json:"agentWorking"`
	InProgress      int `json:"inProgress"`
	Todo            int `json:"todo"`
	// Actionable counts the lanes that want a decision now, which is the one
	// number worth putting on a hotkey badge.
	Actionable int `json:"actionable"`
}

// CockpitLane is one branch/ticket worth of work, with the reasons it is here.
type CockpitLane struct {
	Key      string `json:"key"`
	Title    string `json:"title,omitempty"`
	Ticket   string `json:"ticket,omitempty"`
	Repo     string `json:"repo,omitempty"`
	Branch   string `json:"branch,omitempty"`
	OpenPath string `json:"openPath,omitempty"`
	DeepLink string `json:"deepLink,omitempty"`
	// OpenAction says what the open button would do here: reveal a directory,
	// create a worktree first, or nothing at all. See DashboardGroup.
	OpenAction string `json:"openAction,omitempty"`
	JiraURL    string `json:"jiraUrl,omitempty"`
	// JiraStatus is the project's own status name ("In Development", "To Do"),
	// which the UI groups the queue by so each project's workflow names appear
	// as-is rather than being mapped onto docent's vocabulary.
	JiraStatus string `json:"jiraStatus,omitempty"`
	// Color is the branch's color, so a lane in the cockpit
	// matches the title bar of the window it opens.
	Color         string `json:"color,omitempty"`
	FG            string `json:"fg,omitempty"`
	Attention     string `json:"attention"`
	AttentionRank int    `json:"attentionRank"`
	// Reasons names every concrete thing wanting attention in this lane. A lane
	// is never shown without at least one.
	Reasons      []string           `json:"reasons"`
	LastActivity string             `json:"lastActivity,omitempty"`
	Sessions     []DashboardSession `json:"sessions"`
	PRs          []DashboardPR      `json:"prs"`
}

// InboxItem is one thing to respond to, addressable on its own.
type InboxItem struct {
	// Kind is "review-comment", "checks-failing", "changes-requested",
	// "ready-to-merge", "review-requested", or "agent-waiting".
	Kind     string `json:"kind"`
	LaneKey  string `json:"laneKey"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Author   string `json:"author,omitempty"`
	URL      string `json:"url,omitempty"`
	Repo     string `json:"repo,omitempty"`
	PRNumber int    `json:"prNumber,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Ticket   string `json:"ticket,omitempty"`
	Branch   string `json:"branch,omitempty"`
	OpenPath string `json:"openPath,omitempty"`
	Color    string `json:"color,omitempty"`
	At       string `json:"at,omitempty"`
}

// Cockpit builds the focused view by filtering the dashboard rather than
// collecting separately, so the two can never disagree.
//
// It refreshes on-request units the same way GET /api/workitems does, because
// this is meant to be the surface left open all day: a snapshot-only read would
// serve an empty view immediately after a restart and go stale between polls.
func (e *Engine) Cockpit(ctx context.Context) Cockpit {
	dash := e.RefreshOnRequest(ctx)
	out := Cockpit{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Provider:    dash.Provider,
		SSHHost:     dash.SSHHost,
		Total:       len(dash.Groups),
		Lanes:       []CockpitLane{},
		Queue:       []CockpitLane{},
		Inbox:       []InboxItem{},
	}
	for _, g := range dash.Groups {
		lane, items := laneFor(g)
		if lane.AttentionRank >= rankNotInCockpit {
			continue
		}
		switch lane.Attention {
		case AttentionAgentWaiting:
			out.Counts.AgentWaiting++
		case AttentionMyTurnPR:
			out.Counts.MyTurnPR++
		case AttentionReadyToMerge:
			out.Counts.ReadyToMerge++
		case AttentionReviewRequested:
			out.Counts.ReviewRequested++
		case AttentionAgentWorking:
			out.Counts.AgentWorking++
		case AttentionInProgress:
			out.Counts.InProgress++
		case AttentionTodo:
			out.Counts.Todo++
		}
		if lane.Attention == AttentionTodo {
			out.Queue = append(out.Queue, lane)
			continue
		}
		out.Lanes = append(out.Lanes, lane)
		out.Inbox = append(out.Inbox, items...)
	}
	out.Counts.Actionable = out.Counts.AgentWaiting + out.Counts.MyTurnPR +
		out.Counts.ReadyToMerge + out.Counts.ReviewRequested

	byUrgencyThenRecency := func(lanes []CockpitLane) func(i, j int) bool {
		return func(i, j int) bool {
			if lanes[i].AttentionRank != lanes[j].AttentionRank {
				return lanes[i].AttentionRank < lanes[j].AttentionRank
			}
			if lanes[i].LastActivity != lanes[j].LastActivity {
				return lanes[i].LastActivity > lanes[j].LastActivity
			}
			return lanes[i].Key < lanes[j].Key
		}
	}
	sort.SliceStable(out.Lanes, byUrgencyThenRecency(out.Lanes))
	// The queue is grouped by JIRA status in the UI so "To Do" and "Backlog"
	// separate themselves, without docent hardcoding either project's status
	// names. Sorting by status here keeps those groups contiguous.
	sort.SliceStable(out.Queue, func(i, j int) bool {
		if out.Queue[i].JiraStatus != out.Queue[j].JiraStatus {
			return out.Queue[i].JiraStatus < out.Queue[j].JiraStatus
		}
		if out.Queue[i].LastActivity != out.Queue[j].LastActivity {
			return out.Queue[i].LastActivity > out.Queue[j].LastActivity
		}
		return out.Queue[i].Key < out.Queue[j].Key
	})
	sort.SliceStable(out.Inbox, func(i, j int) bool {
		if out.Inbox[i].At != out.Inbox[j].At {
			return out.Inbox[i].At > out.Inbox[j].At
		}
		return out.Inbox[i].Title < out.Inbox[j].Title
	})

	for _, u := range e.Collectors().Units {
		out.Sources = append(out.Sources, CockpitSource{
			ID:      u.DirectiveID,
			LastRun: u.LastRun,
			Items:   u.ItemCount,
			Error:   u.LastErr,
			Loaded:  u.LastRun != "",
		})
	}
	sort.SliceStable(out.Sources, func(i, j int) bool { return out.Sources[i].ID < out.Sources[j].ID })
	return out
}

// laneFor classifies one dashboard group into a cockpit lane plus its inbox
// items, returning rankNotInCockpit when nothing about it wants attention.
func laneFor(g DashboardGroup) (CockpitLane, []InboxItem) {
	lane := CockpitLane{
		Key:           g.Key,
		Title:         g.Summary,
		Ticket:        g.Ticket,
		Repo:          g.Repo,
		Branch:        g.Branch,
		OpenPath:      g.OpenPath,
		OpenAction:    g.OpenAction,
		DeepLink:      g.DeepLink,
		JiraURL:       g.JiraURL,
		JiraStatus:    g.JiraStatus,
		Color:         g.Color,
		FG:            g.FG,
		LastActivity:  g.LastActivity,
		Sessions:      g.Sessions,
		PRs:           g.PRs,
		Attention:     "",
		AttentionRank: rankNotInCockpit,
		Reasons:       []string{},
	}
	var inbox []InboxItem

	// A lane can qualify several ways at once; it takes the most urgent, but
	// records every reason so the UI can explain the whole picture.
	promote := func(attention string, rank int) {
		if rank < lane.AttentionRank {
			lane.Attention, lane.AttentionRank = attention, rank
		}
	}

	for _, s := range g.Sessions {
		// Only a session with a fresh heartbeat speaks for a window that is
		// still there. A window that dies without delivering its "close" event
		// — the app quitting, a crash, the machine sleeping — leaves its record
		// behind for the whole retention window, and claiming it is open,
		// working, or waiting on you is wrong in all three cases.
		if !s.Live {
			continue
		}
		switch s.Status {
		case "needs-followup":
			promote(AttentionAgentWaiting, rankAgentWaiting)
			lane.Reasons = append(lane.Reasons, "agent finished and is waiting on you")
			inbox = append(inbox, InboxItem{
				Kind: "agent-waiting", LaneKey: g.Key,
				Title: "Agent finished in " + displayName(s.Name, g), Ticket: g.Ticket,
				Branch: g.Branch, OpenPath: firstNonEmptyStr(s.Path, g.OpenPath),
				Color: g.Color, At: s.LastActivity,
			})
		case "working":
			promote(AttentionAgentWorking, rankAgentWorking)
			lane.Reasons = append(lane.Reasons, "agent is working")
		default:
			promote(AttentionInProgress, rankInProgress)
			lane.Reasons = append(lane.Reasons, "window open")
		}
	}

	for _, pr := range g.PRs {
		if !pr.Mine {
			promote(AttentionReviewRequested, rankReviewRequested)
			lane.Reasons = append(lane.Reasons, "waiting on your review")
			inbox = append(inbox, prInbox("review-requested", g, pr, "Review requested: "+pr.Title))
			continue
		}
		if pr.Draft {
			// A draft is mine and unfinished; that is in-progress, not a queue
			// item.
			promote(AttentionInProgress, rankInProgress)
			lane.Reasons = append(lane.Reasons, "draft PR open")
			continue
		}
		// The collector's six-bucket classification. Empty when it could not read
		// the PR's timeline, in which case the individual signals below still
		// carry the lane on their own.
		bucket := prstatus.Bucket(pr.Bucket)

		myTurn := false
		if pr.Checks == "failing" {
			myTurn = true
			lane.Reasons = append(lane.Reasons, "checks failing")
			inbox = append(inbox, prInbox("checks-failing", g, pr, "Checks failing on "+pr.Title))
		}
		if pr.ReviewDecision == "CHANGES_REQUESTED" {
			myTurn = true
			lane.Reasons = append(lane.Reasons, "changes requested")
			inbox = append(inbox, prInbox("changes-requested", g, pr, "Changes requested on "+pr.Title))
		}
		// Unresolved threads whose last word was someone else's are the ones
		// waiting on the user; a thread they already replied to is not.
		awaiting := 0
		for _, t := range pr.Threads {
			if t.Mine {
				continue
			}
			awaiting++
			item := prInbox("review-comment", g, pr, commentTitle(t, pr))
			item.Body = t.Body
			item.Author = t.Author
			item.File = t.File
			item.Line = t.Line
			if t.URL != "" {
				item.URL = t.URL
			}
			if t.UpdatedAt != "" {
				item.At = t.UpdatedAt
			}
			inbox = append(inbox, item)
		}
		if awaiting > 0 {
			myTurn = true
			lane.Reasons = append(lane.Reasons, plural(awaiting, "unresolved comment", "unresolved comments"))
		}
		// The one thing no other signal here catches: a reviewer who commented at
		// PR level, or inside a thread they then resolved, without ever setting
		// CHANGES_REQUESTED. There is no unresolved thread and no review verdict
		// to find, so before who-acted-last existed this PR read as "awaiting
		// review" while in fact it was waiting on the user.
		if !myTurn && bucket == prstatus.AwaitingAuthor {
			myTurn = true
			lane.Reasons = append(lane.Reasons, "a reviewer is waiting on you")
			inbox = append(inbox, prInbox("changes-requested", g, pr, "A reviewer replied on "+pr.Title))
		}
		if myTurn {
			promote(AttentionMyTurnPR, rankMyTurnPR)
			continue
		}
		// The bucket is preferred over the raw review decision because it also
		// covers repos with no review policy, where GitHub leaves reviewDecision
		// empty forever and an approved PR would otherwise never look mergeable.
		// The raw check remains for PRs the collector could not classify.
		approved := bucket == prstatus.ReadyToMerge ||
			(bucket == "" && pr.ReviewDecision == "APPROVED" && (pr.Checks == "passing" || pr.Checks == "none"))
		if approved {
			promote(AttentionReadyToMerge, rankReadyToMerge)
			lane.Reasons = append(lane.Reasons, "approved, not merged")
			inbox = append(inbox, prInbox("ready-to-merge", g, pr, "Ready to merge: "+pr.Title))
			continue
		}
		// An open PR of mine awaiting review is not my move; it stays visible
		// as in-progress so the branch does not vanish from the cockpit. Naming
		// CI separately matters because "awaiting review" invites you to go chase
		// a reviewer who cannot start until the checks finish.
		promote(AttentionInProgress, rankInProgress)
		if bucket == prstatus.PendingValidation || pr.Checks == "pending" {
			lane.Reasons = append(lane.Reasons, "PR open, checks running")
		} else {
			lane.Reasons = append(lane.Reasons, "PR open, awaiting review")
		}
	}

	// A ticket only earns a lane on its own when JIRA itself says it is live and
	// mine, which is what the user's own tier JQL encodes. Group.Status is not
	// good enough here: it also fires on local branch evidence, which is how a
	// months-old worktree for a shipped ticket claimed to be in progress.
	if lane.AttentionRank >= rankNotInCockpit && !g.JiraDone {
		switch g.JiraTier {
		case "started":
			lane.Attention, lane.AttentionRank = AttentionInProgress, rankInProgress
			lane.Reasons = append(lane.Reasons, withStatus("ticket in progress", g.JiraStatus))
		case "assigned":
			lane.Attention, lane.AttentionRank = AttentionTodo, rankTodo
			lane.Reasons = append(lane.Reasons, withStatus("assigned to you", g.JiraStatus))
		}
	}

	if len(lane.Reasons) == 0 {
		lane.AttentionRank = rankNotInCockpit
	}
	return lane, inbox
}

func withStatus(reason, status string) string {
	if strings.TrimSpace(status) == "" {
		return reason
	}
	return reason + ": " + status
}

func prInbox(kind string, g DashboardGroup, pr DashboardPR, title string) InboxItem {
	return InboxItem{
		Kind: kind, LaneKey: g.Key, Title: title, URL: pr.URL,
		Repo: pr.Repo, PRNumber: pr.PRNumber,
		Ticket: firstNonEmptyStr(pr.Ticket, g.Ticket), Branch: g.Branch,
		OpenPath: g.OpenPath, Color: g.Color, At: g.LastActivity,
	}
}

func commentTitle(t DashboardThread, pr DashboardPR) string {
	who := t.Author
	if who == "" {
		who = "Someone"
	}
	where := t.File
	if where == "" {
		where = pr.Title
	}
	return who + " commented on " + where
}

// displayName prefers a session's own name, falling back to the lane's ticket
// or branch, so an item never renders as a bare empty string.
func displayName(name string, g DashboardGroup) string {
	return firstNonEmptyStr(name, g.Branch, g.Ticket, g.Summary, g.Key)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func plural(n int, one, many string) string {
	word := many
	if n == 1 {
		word = one
	}
	return strconv.Itoa(n) + " " + word
}
