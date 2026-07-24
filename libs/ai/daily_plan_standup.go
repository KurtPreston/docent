package ai

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/model"
)

const dailyPlanModeID = "daily-plan"

// standup line categories: how a line flows through the report sections.
const (
	// categoryDone: merged/closed PRs — appear under the previous day only.
	categoryDone = "done"
	// categoryInProgress: opened/updated/started work — previous day AND
	// "continue on" for today.
	categoryInProgress = "in_progress"
	// categoryReviewed: a PR I reviewed — its own "Reviewed:" section.
	categoryReviewed = "reviewed"
)

// RenderDailyPlanMarkdown produces the deterministic standup document using
// Slack mrkdwn (single-asterisk *bold*):
//
//	*Yesterday (Mon)*
//	- Merged PR for [TICKET](pr) description
//	- Opened draft PR for [TICKET](pr) description
//	- Started [TICKET](jira) description
//
//	*Today*
//	- Continue on [TICKET](pr-or-jira) description
//
//	Reviewed:
//	- [TICKET](pr-url) description
//
//	PRs ready for review:
//	- [TICKET](pr-url)
//
// Each previous-day line is derived from what actually happened to the work
// item inside the window (see deriveStandupLine): the verb reflects the PR
// action (opened/updated/merged/closed) or genuinely-started work, so items
// with no real self-authored activity are dropped rather than mislabeled.
func RenderDailyPlanMarkdown(in RunInput, _ ActivityFormatter) string {
	prevLabel := strings.TrimSpace(in.PrevDayLabel)
	nextLabel := strings.TrimSpace(in.NextDayLabel)
	if prevLabel == "" {
		prevLabel = "Previous"
	}
	if nextLabel == "" {
		nextLabel = "Next"
	}

	var yesterday, reviewed []standupLine
	seenKey := map[string]bool{}

	for _, wi := range in.WorkItems {
		line, ok := deriveStandupLine(wi, in.Since, in.Now)
		if !ok {
			continue
		}
		key := line.Ticket
		if key == "" {
			key = wi.Key
		}
		if seenKey[key] {
			continue
		}
		seenKey[key] = true

		if line.Category == categoryReviewed {
			reviewed = append(reviewed, line)
		} else {
			yesterday = append(yesterday, line)
		}
	}
	sortStandupLines(yesterday)
	sortStandupLines(reviewed)

	var b strings.Builder
	fmt.Fprintf(&b, "*%s*\n", prevLabel)
	if len(yesterday) == 0 {
		b.WriteString("- _none_\n")
	} else {
		for _, line := range yesterday {
			fmt.Fprintf(&b, "- %s %s\n", line.Verb, line.Bullet)
		}
	}
	b.WriteByte('\n')

	// Today continues the in-flight work (not the merged/closed items).
	var today []standupLine
	for _, line := range yesterday {
		if line.Category == categoryInProgress {
			today = append(today, line)
		}
	}
	fmt.Fprintf(&b, "*%s*\n", nextLabel)
	if len(today) == 0 {
		b.WriteString("- _none_\n")
	} else {
		for _, line := range today {
			fmt.Fprintf(&b, "- Continue on %s\n", line.Bullet)
		}
	}
	b.WriteByte('\n')

	if len(reviewed) > 0 {
		b.WriteString("Reviewed:\n")
		for _, line := range reviewed {
			fmt.Fprintf(&b, "- %s\n", line.Bullet)
		}
		b.WriteByte('\n')
	}

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
	Ticket   string
	Bullet   string // "[KEY](url) description"
	Verb     string // "Started", "Merged PR for", ...
	Category string // categoryDone | categoryInProgress | categoryReviewed
	rank     int    // display order within a section
}

// deriveStandupLine inspects a work item's window-scoped signals and returns
// the single standup line that best describes what happened to it yesterday,
// or ok=false when nothing genuine did. Precedence (highest first): merged >
// closed > opened(draft) > opened > updated (pushed commits to an existing
// PR) > started (new commits with no PR, or a ticket moved into the started
// tier in-window backed by branch activity or one of my PRs) > reviewed (a PR
// I reviewed). Items whose only evidence is a reflog branch-visit, a stale
// open PR merely "updated", or a watcher-only Jira change fall through to
// ok=false and are dropped.
func deriveStandupLine(wi model.WorkItem, since, until time.Time) (standupLine, bool) {
	var (
		merged, closed, opened, anyDraft bool
		selfCommit, hasAuthoredPR        bool
		recentAuthoredPR                 bool
		jiraStarted, branchEvidence      bool
		reviewed                         bool
	)
	for _, ent := range wi.Entities {
		switch ent.Kind {
		case "commit":
			// Only the user's own commits count; commits from CI bots or
			// teammates that happen to sit on a checked-out branch do not.
			// (Missing is_self is treated as self for backward compatibility.)
			if entityTimestampInWindow(ent, since, until) && ent.State["is_self"] != "false" {
				selfCommit = true
				branchEvidence = true
			}
		case "github_commit":
			// github_commit comes from an author-anchored search, so it is
			// always the user's own work.
			if entityTimestampInWindow(ent, since, until) {
				selfCommit = true
				branchEvidence = true
			}
		case "reflog", "branch":
			if entityTimestampInWindow(ent, since, until) {
				branchEvidence = true
			}
		case "reviewed_pr":
			if prUpdatedInWindow(ent, since, until) {
				reviewed = true
			}
		case "ticket", "issue", "issue_activity":
			if entityTimestampInWindow(ent, since, until) && ent.State["status_tier"] == "started" {
				jiraStarted = true
			}
		case "authored_pr", "pr_review_status":
			// Only my authored PRs; pr_review_status carries relation.
			if rel := ent.State["relation"]; rel != "" && rel != "authored" {
				continue
			}
			hasAuthoredPR = true
			if ent.State["is_draft"] == "true" {
				anyDraft = true
			}
			if authoredPRMergedInWindow(ent, since, until) {
				merged = true
			} else if authoredPRClosedNotMergedInWindow(ent, since, until) {
				closed = true
			}
			if prCreatedInWindow(ent, since, until) {
				opened = true
			}
			// A PR created no earlier than the window start (in-window or since,
			// e.g. opened this morning) is fresh work, distinct from a months-old
			// stale draft that merely still exists.
			if prCreatedOnOrAfter(ent, since) {
				recentAuthoredPR = true
			}
		}
	}
	// A push counts only for an existing PR (not one opened/closed in-window).
	pushed := hasAuthoredPR && selfCommit && !opened && !merged && !closed

	var verb, category string
	var rank int
	switch {
	case merged:
		verb, category, rank = "Merged PR for", categoryDone, 0
	case closed:
		verb, category, rank = "Closed PR for", categoryDone, 1
	case opened && anyDraft:
		verb, category, rank = "Opened draft PR for", categoryInProgress, 2
	case opened:
		verb, category, rank = "Opened PR for", categoryInProgress, 3
	case pushed:
		verb, category, rank = "Updated PR for", categoryInProgress, 4
	case selfCommit && !hasAuthoredPR:
		verb, category, rank = "Started", categoryInProgress, 5
	case jiraStarted && (branchEvidence || recentAuthoredPR):
		// A ticket in the started tier (updated in-window), backed by local
		// branch activity or a freshly-opened PR of mine, was genuinely
		// started. Requiring one of those excludes both watcher-only tickets
		// and long-lived stale drafts that merely still sit in the tier.
		verb, category, rank = "Started", categoryInProgress, 5
	case reviewed:
		verb, category, rank = "Reviewed", categoryReviewed, 6
	default:
		return standupLine{}, false
	}

	ticket, desc, link := primaryTicketLink(wi)
	if link == "" {
		link = ticketBrowseFallback(wi, ticket)
	}

	// Prefer a real JIRA-style ticket key with a link.
	if ticket != "" && jiraKeyPattern.MatchString(ticket) && link != "" {
		bullet := fmt.Sprintf("[%s](%s)", ticket, link)
		if desc != "" {
			bullet += " " + desc
		}
		return standupLine{Ticket: ticket, Bullet: bullet, Verb: verb, Category: category, rank: rank}, true
	}

	// Reviewed PRs need not carry a JIRA ticket; link the PR itself.
	if category == categoryReviewed {
		title, url := reviewedPRTitleURL(wi)
		if url == "" {
			return standupLine{}, false
		}
		if title == "" {
			title = url
		}
		return standupLine{Ticket: url, Bullet: fmt.Sprintf("[%s](%s)", title, url), Verb: verb, Category: category, rank: rank}, true
	}

	return standupLine{}, false
}

// reviewedPRTitleURL returns the title and URL of the first reviewed PR on the
// work item, used when a reviewed PR has no associated JIRA ticket.
func reviewedPRTitleURL(wi model.WorkItem) (title, url string) {
	for _, ent := range wi.Entities {
		if ent.Kind == "reviewed_pr" && ent.URL != "" {
			return strings.TrimSpace(ent.Title), ent.URL
		}
	}
	return "", ""
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

// authoredPRMergedInWindow reports whether an authored PR entity is in the
// merged state with its close time inside the window.
func authoredPRMergedInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil || strings.ToLower(strings.TrimSpace(ent.State["state"])) != "merged" {
		return false
	}
	return prCloseTimeInWindow(ent, since, until)
}

// authoredPRClosedNotMergedInWindow reports whether an authored PR entity was
// closed without merging, with its close time inside the window.
func authoredPRClosedNotMergedInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil || strings.ToLower(strings.TrimSpace(ent.State["state"])) != "closed" {
		return false
	}
	return prCloseTimeInWindow(ent, since, until)
}

// prCloseTimeInWindow checks whether a closed/merged PR's close time falls in
// the window, preferring closed_at and falling back to updated_at. A missing
// or unparseable timestamp is treated as in-window (the state already says it
// closed and event rows are window-filtered).
func prCloseTimeInWindow(ent model.Entity, since, until time.Time) bool {
	closedAt := strings.TrimSpace(ent.State["closed_at"])
	if closedAt == "" || strings.HasPrefix(closedAt, "0001") {
		closedAt = strings.TrimSpace(ent.State["updated_at"])
	}
	if closedAt == "" {
		return true
	}
	t, ok := parseFlexibleTime(closedAt)
	if !ok {
		return true
	}
	return timeInWindow(t, since, until)
}

// prCreatedInWindow reports whether an authored PR was created inside the
// window (i.e. opened yesterday). Requires the created_at field the collector
// now captures; absent it, returns false so the PR is treated as pre-existing.
func prCreatedInWindow(ent model.Entity, since, until time.Time) bool {
	if ent.State == nil {
		return false
	}
	raw := strings.TrimSpace(ent.State["created_at"])
	if raw == "" {
		return false
	}
	t, ok := parseFlexibleTime(raw)
	if !ok {
		return false
	}
	return timeInWindow(t, since, until)
}

// prCreatedOnOrAfter reports whether an authored PR was created no earlier than
// the window start. Unlike prCreatedInWindow it also accepts PRs opened after
// the window (e.g. this morning), so a ticket started yesterday whose PR went
// up today still reads as fresh work rather than a stale months-old draft.
func prCreatedOnOrAfter(ent model.Entity, since time.Time) bool {
	if ent.State == nil {
		return false
	}
	raw := strings.TrimSpace(ent.State["created_at"])
	if raw == "" {
		return false
	}
	t, ok := parseFlexibleTime(raw)
	if !ok {
		return false
	}
	return !t.Before(since)
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
		if lines[i].rank != lines[j].rank {
			return lines[i].rank < lines[j].rank
		}
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
