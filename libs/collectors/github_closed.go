package collectors

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// buildClosedPRSpecs plans the recently-closed searches: the user's own PRs
// only. The candidate pool and review-requested searches have no analogue here —
// a teammate's merged PR is not the user's work, and the review of it is over.
func buildClosedPRSpecs(scope Scope, user string, extra []userdata.PRQuery) []prReviewSpec {
	specs := []prReviewSpec{{relation: "authored", args: []string{"--author", user}, mine: true}}
	if scope == ScopeSelf {
		return specs
	}
	// Same reasoning as the open-PR path: a PR a bot opened on the user's behalf
	// is theirs to account for, and it merges like any other. A watch query is
	// not: the user's own PRs in it are already found by the authored search.
	for _, q := range extra {
		qualifiers := strings.Fields(q.Qualifiers)
		if q.Watch || len(qualifiers) == 0 {
			continue
		}
		specs = append(specs, prReviewSpec{relation: strings.TrimSpace(q.Relation), args: qualifiers, mine: true})
	}
	return specs
}

// closedPRLookback bounds the recently-closed search when the caller sets no
// window. docentd polls state with Until but no Since — it has no report window —
// and a date-filtered search needs a floor to stop it walking the whole history.
const closedPRLookback = 7 * 24 * time.Hour

// collectClosedPRs lists the user's own PRs that closed — merged or abandoned —
// no earlier than the window start, as Kind "pr_closed_status".
//
// The separate Kind is deliberate. Everything that reads pr_review_status takes
// "open" as given: the automation matcher's sourceKinds (a `checks: failing`
// rule would otherwise task an agent with fixing a branch that already merged),
// the cockpit's bucket classification, the `prs` report, and the standup's
// ready-for-review list. A distinct kind leaves all four correct without adding
// a state guard to each, and correlation still anchors these to their branch
// unit because branchAnchor keys on any kind containing "pr".
//
// What this adds over the events-side authored_pr row is head_branch, which that
// row cannot carry (`gh search prs --json` does not expose it). The standup needs
// it to tell a commit pushed to the PR from the squash-merge commit GitHub
// attributes to the user on the base branch.
func (c GitHubCollector) collectClosedPRs(ctx context.Context, env []string, directive userdata.Directive, user, host string, opts *CollectOpts) ([]StatusItem, error) {
	now := opts.windowEnd(c.Clock)
	since := now.Add(-closedPRLookback)
	if opts != nil && !opts.Since.IsZero() {
		since = opts.Since
	}

	var found []prReviewRow
	for _, spec := range buildClosedPRSpecs(opts.EffectiveScope(), user, directive.PRQueries) {
		rows, err := c.listClosedPRs(ctx, env, directive, opts, since, spec.args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			found = append(found, prReviewRow{spec: spec, row: row})
		}
	}
	// A PR can match both the authored search and a declared query; resolve it
	// once, keeping the first spec that claimed it (same precedence as the
	// open-PR path).
	rows := dedupePRReviewRows(found)

	type closedDetail struct {
		headBranch string
		merged     bool
	}
	details := make([]closedDetail, len(rows))
	// One round trip per PR, concurrent for the same reason the open-PR path is:
	// the calls are independent and dominated by latency.
	work := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < prDetailWorkers && i < len(rows); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				headBranch, state := c.fetchPRClosedDetail(ctx, env, rows[idx].row.URL, directive, opts)
				if state == "" {
					// The lookup failed, so trust the search. GitHub Enterprise
					// reports "merged" there even though the documented --state
					// values are only open/closed, which makes this a decent
					// fallback but a poor primary source.
					state = rows[idx].row.State
				}
				details[idx] = closedDetail{headBranch: headBranch, merged: strings.EqualFold(state, "merged")}
			}
		}()
	}
	for i := range rows {
		work <- i
	}
	close(work)
	wg.Wait()

	items := make([]StatusItem, 0, len(rows))
	for i, r := range rows {
		items = append(items, closedPRItem(directive, user, host, now, r.row, r.spec, details[i].headBranch, details[i].merged))
	}
	return items, nil
}

// closedPRItem builds one pr_closed_status StatusItem. merged separates a merge
// from an abandonment, which the search alone cannot.
func closedPRItem(directive userdata.Directive, user, host string, now time.Time, row ghSearchActivityRow, spec prReviewSpec, headBranch string, merged bool) StatusItem {
	repo := strings.TrimSpace(row.Repository.NameWithOwner)
	if repo == "" {
		repo = gitHubOwnerRepoFromURL(row.URL)
	}
	state := "closed"
	if merged {
		state = "merged"
	}
	fields := map[string]string{
		"username":   user,
		"host":       host,
		"repo":       repo,
		"state":      state,
		"relation":   spec.relation,
		"mine":       "true",
		"reviewable": "false",
		"is_draft":   strconv.FormatBool(row.IsDraft),
	}
	if login := strings.TrimSpace(row.Author.Login); login != "" {
		fields["pr_author"] = login
		fields["pr_author_bot"] = strconv.FormatBool(row.Author.IsBot)
	}
	// created_at against closed_at is what separates a PR opened and finished in
	// one window from an older one that finished in it.
	if v := strings.TrimSpace(row.CreatedAt); v != "" {
		fields["created_at"] = v
	}
	if v := strings.TrimSpace(row.ClosedAt); v != "" {
		fields["closed_at"] = v
	}
	if v := strings.TrimSpace(row.UpdatedAt); v != "" {
		fields["updated_at"] = v
	}
	if headBranch != "" {
		fields["head_branch"] = headBranch
	}
	// Report when the PR actually finished, not poll time, so the row lands in
	// the window that saw the merge.
	obs := now
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(row.ClosedAt)); err == nil {
		obs = t
	} else if t, err := time.Parse(time.RFC3339, strings.TrimSpace(row.UpdatedAt)); err == nil {
		obs = t
	}
	return StatusItem{
		DirectiveID: directive.ID,
		Repository:  repo,
		Source:      directive.Collector,
		Kind:        "pr_closed_status",
		Title:       row.Title,
		Summary:     fmt.Sprintf("%s pr relation=%s head_branch=%s", state, spec.relation, headBranch),
		URL:         row.URL,
		Severity:    "info",
		ObservedAt:  obs.UTC(),
		Author:      user,
		IsSelf:      true,
		Fields:      fields,
	}
}

// listClosedPRs runs `gh search prs <relationArgs> --state closed --closed
// >=<since>` and returns the parsed rows. The date filter is what bounds the
// query: without it the search would reach back over the whole history.
func (c GitHubCollector) listClosedPRs(ctx context.Context, env []string, directive userdata.Directive, opts *CollectOpts, since time.Time, relationArgs ...string) ([]ghSearchActivityRow, error) {
	args := append([]string{"search", "prs"}, relationArgs...)
	args = append(args,
		"--state", "closed",
		"--closed", ">="+since.UTC().Format("2006-01-02"),
		"--limit", "100",
		"--json", "title,url,state,isDraft,createdAt,updatedAt,closedAt,repository,author,assignees",
	)
	return c.runPRSearch(ctx, env, directive, opts, args)
}

// fetchPRClosedDetail resolves a closed PR's head branch and its OPEN/CLOSED/
// MERGED state. The head branch is the reason for the call — `gh search prs
// --json` cannot return it — and taking the state from the same round trip makes
// the merge-versus-abandonment call authoritative for free. An empty state means
// the lookup failed; callers fall back to what the search reported.
func (c GitHubCollector) fetchPRClosedDetail(ctx context.Context, env []string, prURL string, directive userdata.Directive, opts *CollectOpts) (headBranch, state string) {
	if strings.TrimSpace(prURL) == "" {
		return "", ""
	}
	view, err := c.fetchPRView(ctx, env, prURL, "headRefName,state", directive, opts)
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(view.HeadRefName), strings.TrimSpace(view.State)
}
