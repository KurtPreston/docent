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

// prFacts is what one PR of mine contributes: the window-scoped events it saw,
// plus the title and URL a line links when the work item carries no JIRA ticket.
type prFacts struct {
	title      string
	url        string
	headBranch string
	isDraft    bool
	merged     bool // merged inside the window
	closed     bool // closed without merging, inside the window
	created    bool // opened inside the window
	// createdSinceStart also accepts a PR opened after the window (e.g. this
	// morning), which still marks a ticket as freshly started rather than stale.
	createdSinceStart bool
}

// deriveStandupLine inspects a work item's window-scoped signals and returns
// the single standup line that best describes what happened to it yesterday,
// or ok=false when nothing genuine did.
//
// PR verbs are decided per PR and the most advanced one wins (see
// standupPRVerb), because a work item can carry several of mine — an older PR
// that merged today alongside a follow-up opened today — and folding their
// facts together would report one PR as having been both opened and merged.
// Failing that, precedence runs: started (new commits with no PR, or a ticket
// moved into the started tier in-window backed by branch activity or one of my
// PRs) > reviewed (a PR I reviewed). Neither "started" form fires when the
// branch carries somebody else's PR: commits I pushed while reviewing a
// colleague's branch are not work of my own beginning. Items whose only
// evidence is a reflog branch-visit, a stale open PR merely "updated", or a
// watcher-only Jira change fall through to ok=false and are dropped.
func deriveStandupLine(wi model.WorkItem, since, until time.Time) (standupLine, bool) {
	var (
		selfCommit                  bool
		jiraStarted, branchEvidence bool
		reviewed, othersPR          bool
		prOrder                     []string
	)
	prs := map[string]*prFacts{}
	// Branches I committed to inside the window. Only local commits carry a
	// branch, and that is what lets them prove a push to a PR: a github_commit
	// has none, so a squash-merge landing on the base branch — authored by me,
	// since squashing preserves the author — is indistinguishable there from a
	// commit I pushed to the PR itself.
	pushedBranches := map[string]bool{}

	for _, ent := range wi.Entities {
		switch ent.Kind {
		case "commit":
			// Only the user's own commits count; commits from CI bots or
			// teammates that happen to sit on a checked-out branch do not.
			// (Missing is_self is treated as self for backward compatibility.)
			if entityTimestampInWindow(ent, since, until) && ent.State["is_self"] != "false" {
				selfCommit = true
				branchEvidence = true
				if b := strings.TrimSpace(ent.Coordinates["branch"]); b != "" {
					pushedBranches[b] = true
				}
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
		case "authored_pr", "pr_review_status", "pr_closed_status":
			// Only PRs I own; pr_review_status carries the mine flag. A PR
			// somebody else owns still tells us something: work items group
			// by head branch, so its presence means I was committing on a
			// colleague's branch rather than starting work of my own.
			if ent.State["mine"] == "false" {
				othersPR = true
				continue
			}
			// Keyed by URL because one PR arrives as several entities: a merged
			// one is both an events-side authored_pr and a state-side
			// pr_closed_status, and only the latter knows its head branch.
			key := strings.TrimSpace(ent.URL)
			if key == "" {
				key = ent.ID
			}
			f, ok := prs[key]
			if !ok {
				f = &prFacts{}
				prs[key] = f
				prOrder = append(prOrder, key)
			}
			mergePRFacts(f, ent, since, until)
		}
	}

	var (
		verb, category   string
		rank             int
		chosen           *prFacts
		recentAuthoredPR bool
	)
	for _, key := range prOrder {
		pr := prs[key]
		if pr.createdSinceStart {
			recentAuthoredPR = true
		}
		v, c, r, ok := standupPRVerb(*pr, selfCommit, pr.headBranch != "" && pushedBranches[pr.headBranch])
		if !ok {
			continue
		}
		// Most advanced wins; URL breaks a tie so the choice is deterministic
		// rather than dependent on entity order.
		if chosen == nil || r < rank || (r == rank && pr.url < chosen.url) {
			verb, category, rank, chosen = v, c, r, pr
		}
	}

	switch {
	case chosen != nil:
		// Verb already set above.
	case selfCommit && len(prs) == 0 && !othersPR:
		verb, category, rank = "Started", categoryInProgress, 5
	case jiraStarted && !othersPR && (branchEvidence || recentAuthoredPR):
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
	// The line describes one particular PR, so link that one rather than
	// whichever primaryTicketLink found first: on a ticket carrying several of
	// mine, pointing "Landed" at a sibling that is still open reads as an error.
	if chosen != nil && chosen.url != "" {
		link = chosen.url
	}
	// A reviewed line has to point at the PR I actually reviewed. One ticket
	// can carry several — a backport of the reviewed PR, or a follow-up
	// somebody else opened — and primaryTicketLink picks whichever comes
	// first, since it has no way to tell them apart.
	if category == categoryReviewed {
		if _, url := reviewedPRTitleURL(wi); url != "" {
			link = url
		}
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

	// Nor does a PR of my own: not every repo I work in tracks its work in a
	// JIRA project, so link the PR itself rather than drop the line. Unticketed
	// work with no PR behind it — local commits, a branch visit — stays dropped.
	if chosen != nil && chosen.url != "" {
		title := chosen.title
		if title == "" {
			title = chosen.url
		}
		return standupLine{Ticket: chosen.url, Bullet: fmt.Sprintf("[%s](%s)", title, chosen.url), Verb: verb, Category: category, rank: rank}, true
	}

	return standupLine{}, false
}

// mergePRFacts folds one entity's view of a PR into the facts already gathered
// for it. Merged rather than appended because the same PR arrives more than
// once and the views differ in what they know: the events-side authored_pr has
// the merge state, the state-side pr_closed_status adds the head branch.
func mergePRFacts(f *prFacts, ent model.Entity, since, until time.Time) {
	if f.title == "" {
		f.title = strings.TrimSpace(ent.Title)
	}
	if f.url == "" {
		f.url = strings.TrimSpace(ent.URL)
	}
	if f.headBranch == "" {
		f.headBranch = strings.TrimSpace(ent.Coordinates["head_branch"])
	}
	if ent.State["is_draft"] == "true" {
		f.isDraft = true
	}
	if authoredPRMergedInWindow(ent, since, until) {
		f.merged = true
	} else if authoredPRClosedNotMergedInWindow(ent, since, until) {
		f.closed = true
	}
	if prCreatedInWindow(ent, since, until) {
		f.created = true
	}
	if prCreatedOnOrAfter(ent, since) {
		f.createdSinceStart = true
	}
}

// standupPRVerb returns the verb one PR of mine earns, or ok=false when nothing
// happened to it inside the window. Lower rank is more advanced.
//
// A terminal event keeps the opening verb rather than replacing it, so a PR I
// opened and finished the same day reads differently from an old one that merely
// finished: "Landed" and "Abandoned" each say both halves in a single word, and
// "Updated/Merged" credits the push that preceded the merge. Closing without
// merging is the exception — work thrown away is not worth crediting a push for,
// so a pre-existing PR I pushed to and then closed is just "Closed".
//
// pushedToHead is required for "Updated/Merged" but not for "Updated": a merged
// PR has a squash commit on the base branch that GitHub attributes to me, and
// without the head-branch check that commit alone would make every merge read as
// updated. An open PR has no such commit, so any commit of mine on the work item
// is good evidence for it.
func standupPRVerb(pr prFacts, selfCommit, pushedToHead bool) (verb, category string, rank int, ok bool) {
	switch {
	case pr.merged && pr.created:
		return "Landed PR for", categoryDone, 0, true
	case pr.merged && pushedToHead:
		return "Updated/Merged PR for", categoryDone, 0, true
	case pr.merged:
		return "Merged PR for", categoryDone, 0, true
	case pr.closed && pr.created:
		return "Abandoned PR for", categoryDone, 1, true
	case pr.closed:
		return "Closed PR for", categoryDone, 1, true
	case pr.created && pr.isDraft:
		return "Opened draft PR for", categoryInProgress, 2, true
	case pr.created:
		return "Opened PR for", categoryInProgress, 3, true
	case selfCommit:
		return "Updated PR for", categoryInProgress, 4, true
	}
	return "", "", 0, false
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

// readyForReviewPRs returns open PRs I own that are ready (not draft, checks
// passing) and not yet approved.
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
		if s.Fields["mine"] == "false" {
			continue
		}
		ready = append(ready, s)
	}
	sortPRItems(ready)
	return ready
}
