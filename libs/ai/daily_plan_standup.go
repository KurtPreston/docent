package ai

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/model"
	"github.com/KurtPreston/docent/libs/workitem"
)

const dailyPlanModeID = "daily-plan"

// RenderDailyPlanMarkdown produces the deterministic standup document:
//
//	**Monday**
//	- Started [TICKET](pr-or-jira) description
//	- Finished [TICKET](pr-or-jira) description
//
//	**Tuesday**
//	- Continue on [TICKET](pr-or-jira) description
//
//	PRs ready for review:
//	- [TICKET](pr-url)
//
// Classification drives off libs/workitem status, with a report-side done
// state for merged/closed authored PRs in-window or JIRA done-category.
func RenderDailyPlanMarkdown(in RunInput, _ ActivityFormatter) string {
	prevLabel := strings.TrimSpace(in.PrevDayLabel)
	nextLabel := strings.TrimSpace(in.NextDayLabel)
	if prevLabel == "" {
		prevLabel = "Previous"
	}
	if nextLabel == "" {
		nextLabel = "Next"
	}

	var started, finished []standupLine
	seenTicket := map[string]bool{}

	for _, wi := range in.WorkItems {
		line, ok := standupLineFromWorkItem(wi, in.Since, in.Now)
		if !ok {
			continue
		}
		key := line.Ticket
		if key == "" {
			key = wi.Key
		}
		if seenTicket[key] {
			continue
		}
		seenTicket[key] = true

		switch line.Kind {
		case "finished":
			finished = append(finished, line)
		case "started":
			started = append(started, line)
		}
	}
	sortStandupLines(started)
	sortStandupLines(finished)

	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", prevLabel)
	if len(started) == 0 && len(finished) == 0 {
		b.WriteString("- _none_\n")
	} else {
		for _, line := range started {
			fmt.Fprintf(&b, "- Started %s\n", line.Bullet)
		}
		for _, line := range finished {
			fmt.Fprintf(&b, "- Finished %s\n", line.Bullet)
		}
	}
	b.WriteByte('\n')

	fmt.Fprintf(&b, "**%s**\n", nextLabel)
	if len(started) == 0 {
		b.WriteString("- _none_\n")
	} else {
		for _, line := range started {
			fmt.Fprintf(&b, "- Continue on %s\n", line.Bullet)
		}
	}
	b.WriteByte('\n')

	b.WriteString("PRs ready for review:\n")
	ready := readyForReviewPRs(in.Statuses)
	if len(ready) == 0 {
		b.WriteString("- _none_\n")
	} else {
		for _, s := range ready {
			b.WriteString(prBullet(s))
			b.WriteByte('\n')
		}
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

type standupLine struct {
	Kind   string // "started" or "finished"
	Ticket string
	Bullet string // "[KEY](url) description"
}

func standupLineFromWorkItem(wi model.WorkItem, since, until time.Time) (standupLine, bool) {
	facts := accumulateReportFacts(wi, since, until)
	status, rank, _ := workitem.Classify(facts)
	if rank >= workitem.RankHidden || status == workitem.StatusAssigned {
		return standupLine{}, false
	}

	ticket, desc, link := primaryTicketLink(wi)
	// Standup lines require a real JIRA-style ticket key; skip unticketed
	// branches/commits that only have a StableID-ish work-item key.
	if ticket == "" || !jiraKeyPattern.MatchString(ticket) {
		return standupLine{}, false
	}
	if link == "" {
		link = ticketBrowseFallback(wi, ticket)
	}
	if link == "" {
		return standupLine{}, false
	}
	bullet := fmt.Sprintf("[%s](%s)", ticket, link)
	if desc != "" {
		bullet += " " + desc
	}

	kind := "started"
	if status == workitem.StatusDone {
		kind = "finished"
	}
	return standupLine{Kind: kind, Ticket: ticket, Bullet: bullet}, true
}

func ticketBrowseFallback(wi model.WorkItem, ticket string) string {
	for _, tr := range wi.Tickets {
		if strings.EqualFold(tr.Key, ticket) && strings.HasPrefix(tr.URL, "http") {
			return tr.URL
		}
	}
	for _, ent := range wi.Entities {
		switch ent.Kind {
		case "ticket", "issue", "issue_activity":
			if strings.HasPrefix(ent.URL, "http") {
				return ent.URL
			}
		}
	}
	return ""
}

func accumulateReportFacts(wi model.WorkItem, since, until time.Time) workitem.Facts {
	var facts workitem.Facts
	for _, ent := range wi.Entities {
		switch ent.Kind {
		case "session":
			if ent.State["live"] == "true" {
				facts.HasLiveSession = true
			}
			if ent.Coordinates["ticket"] != "" {
				facts.BranchEvidence = true
			}
			if ent.State["attention"] == "needs-followup" {
				facts.SessionNeedsFollowup = true
			}
		case "ticket", "issue_activity", "issue":
			// Annotation backfill stamps observedAt=now; only count JIRA
			// tiers when the issue itself moved inside the window.
			if !entityTimestampInWindow(ent, since, until) {
				continue
			}
			if ent.State != nil {
				switch ent.State["status_tier"] {
				case "started":
					facts.JiraStarted = true
				case "assigned":
					facts.JiraAssigned = true
				}
				if strings.EqualFold(ent.State["status_category"], "done") {
					facts.Done = true
				}
			}
		case "commit", "reflog":
			// Event collectors already window-filter these; treat as
			// in-window evidence that work happened on the ticket.
			if strings.HasPrefix(wi.Key, "wb:") || ent.Coordinates["ticket"] != "" {
				facts.BranchEvidence = true
			}
		case "branch":
			// Bare branch/worktree presence is not standup evidence —
			// every salsa worktree would otherwise flood Started.
			continue
		default:
			if !strings.Contains(ent.Kind, "pr") {
				continue
			}
			if authoredPRClosedInWindow(ent, since, until) {
				facts.Done = true
			}
			// State-pass pr_review_status is for the ready-for-review
			// section (via Statuses), not for Started/Continue, unless
			// the PR's own updated_at falls in the window.
			if ent.Kind == "pr_review_status" {
				if prUpdatedInWindow(ent, since, until) {
					workitem.ClassifyPR(&facts, ent)
				}
				continue
			}
			// Event-pass PR kinds (authored_pr, etc.) are window-filtered.
			workitem.ClassifyPR(&facts, ent)
		}
	}
	return facts
}

func entityTimestampInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil {
		return false
	}
	for _, key := range []string{"updated", "updated_at", "closed_at", "committed_at", "observedAt"} {
		raw := strings.TrimSpace(ent.State[key])
		if raw == "" {
			continue
		}
		if t, ok := parseFlexibleTime(raw); ok {
			return timeInWindow(t, since, until)
		}
	}
	return false
}

func prUpdatedInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil {
		return false
	}
	// Prefer explicit updated_at; for pr_review_status the collector stores
	// the PR's GitHub updatedAt in observedAt (not poll time).
	for _, key := range []string{"updated_at", "updated", "closed_at", "observedAt"} {
		raw := strings.TrimSpace(ent.State[key])
		if raw == "" {
			continue
		}
		if t, ok := parseFlexibleTime(raw); ok {
			return timeInWindow(t, since, until)
		}
	}
	return false
}

func parseFlexibleTime(raw string) (time.Time, bool) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000-0700",
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func timeInWindow(t, since, until time.Time) bool {
	if !since.IsZero() && t.Before(since) {
		return false
	}
	if !until.IsZero() && t.After(until) {
		return false
	}
	return true
}

func authoredPRClosedInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil {
		return false
	}
	// Only count the author's own PRs.
	if rel := ent.State["relation"]; rel != "" && rel != "authored" {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(ent.State["state"]))
	if state != "closed" && state != "merged" {
		return false
	}
	closedAt := strings.TrimSpace(ent.State["closed_at"])
	if closedAt == "" {
		closedAt = strings.TrimSpace(ent.State["updated_at"])
	}
	if closedAt == "" {
		return true // closed/merged with no timestamp — treat as done
	}
	t, ok := parseFlexibleTime(closedAt)
	if !ok {
		return true
	}
	return timeInWindow(t, since, until)
}

func primaryTicketLink(wi model.WorkItem) (ticket, desc, link string) {
	var tr model.TicketRef
	if len(wi.Tickets) > 0 {
		tr = wi.Tickets[0]
	} else if !strings.HasPrefix(wi.Key, "wb:") {
		tr.Key = wi.Key
		tr.Title = wi.Title
	}

	ticket = strings.TrimSpace(tr.Key)
	desc = ticketDescription(tr)
	if desc == "" && ticket != "" {
		desc = ticketDescription(model.TicketRef{Key: ticket, Title: wi.Title})
	}

	// Prefer a PR URL whose title/coords mention the ticket.
	link = prURLForTicket(wi, ticket)
	if link == "" {
		link = strings.TrimSpace(tr.URL)
	}
	if link == "" {
		// Any PR on the work item.
		for _, ent := range wi.Entities {
			if strings.Contains(ent.Kind, "pr") && ent.URL != "" {
				link = ent.URL
				break
			}
		}
	}
	return ticket, desc, link
}

func prURLForTicket(wi model.WorkItem, ticket string) string {
	ticket = strings.ToUpper(strings.TrimSpace(ticket))
	for _, ent := range wi.Entities {
		if !strings.Contains(ent.Kind, "pr") || ent.URL == "" {
			continue
		}
		if ticket == "" {
			return ent.URL
		}
		if strings.EqualFold(ent.Coordinates["ticket"], ticket) {
			return ent.URL
		}
		if key, _ := splitJiraKey(ent.Title); strings.EqualFold(key, ticket) {
			return ent.URL
		}
	}
	return ""
}

func sortStandupLines(lines []standupLine) {
	sort.SliceStable(lines, func(i, j int) bool {
		return lines[i].Ticket < lines[j].Ticket
	})
}

// readyForReviewPRs returns authored open PRs that are ready (not draft,
// checks passing) and not yet approved.
func readyForReviewPRs(statuses []collectors.StatusItem) []collectors.StatusItem {
	var ready []collectors.StatusItem
	for _, s := range statuses {
		if s.Kind != "pr_review_status" {
			continue
		}
		if s.Fields == nil || s.Fields["ready"] != "true" {
			continue
		}
		if strings.EqualFold(s.Fields["review_decision"], "APPROVED") {
			continue
		}
		if rel := s.Fields["relation"]; rel != "" && rel != "authored" {
			continue
		}
		ready = append(ready, s)
	}
	sortPRItems(ready)
	return ready
}
