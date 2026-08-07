package collectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// GitHubCollector returns activity (PRs, issues, comments, commits) authored
// by, reviewed by, or commented on by a user. When target.username is empty,
// the GitHub search literal "@me" is used so that the authenticated `gh` user
// is queried. The same struct backs both the `github` and `github-enterprise`
// directive types; enterprise hosts route requests via config.base_url.
type GitHubCollector struct {
	Clock func() time.Time
}

type ghSearchActivityRow struct {
	Title      string         `json:"title"`
	URL        string         `json:"url"`
	State      string         `json:"state"`
	IsDraft    bool           `json:"isDraft"`
	CreatedAt  string         `json:"createdAt"`
	UpdatedAt  string         `json:"updatedAt"`
	ClosedAt   string         `json:"closedAt"`
	Author     ghSearchUser   `json:"author"`
	Assignees  []ghSearchUser `json:"assignees"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
}

// ghSearchUser is an account named by a search row. Only open-PR searches ask
// for these fields; activity searches leave them zero.
type ghSearchUser struct {
	Login string `json:"login"`
	IsBot bool   `json:"is_bot"`
}

// ghPRView is the subset of `gh pr view --json` we parse for the PR
// review-readiness path. statusCheckRollup is a heterogeneous array of
// CheckRun (GitHub Actions etc.) and StatusContext (legacy commit
// statuses) entries; see ghCheckRollupEntry. reviewDecision is one of
// APPROVED / CHANGES_REQUESTED / REVIEW_REQUIRED (or "" when the repo
// has no required reviews configured). mergeable is GitHub's merge-conflict
// verdict: MERGEABLE / CONFLICTING / UNKNOWN.
type ghPRView struct {
	StatusCheckRollup []ghCheckRollupEntry `json:"statusCheckRollup"`
	ReviewDecision    string               `json:"reviewDecision"`
	HeadRefName       string               `json:"headRefName"`
	Mergeable         string               `json:"mergeable"`
}

// ghCheckRollupEntry covers both shapes returned in statusCheckRollup.
// CheckRun entries carry Status (QUEUED/IN_PROGRESS/COMPLETED) and
// Conclusion (SUCCESS/FAILURE/SKIPPED/…); StatusContext entries carry
// State (SUCCESS/PENDING/FAILURE/ERROR/EXPECTED).
type ghCheckRollupEntry struct {
	Typename   string `json:"__typename"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

// ReviewThread is one review conversation on a PR, reduced to what a follow-up
// queue needs: where it is, who spoke last, and what they said.
//
// Threads are the unit of PR follow-up, not individual comments: a reviewer's
// question plus your reply plus their acknowledgement is one item of work, and
// whether it still needs you depends only on who spoke last.
type ReviewThread struct {
	ID     string `json:"id"`
	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
	URL    string `json:"url,omitempty"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	// Mine reports whether the most recent comment is the viewer's own, which
	// separates "a reviewer is waiting on me" from "I replied and am waiting on
	// them".
	Mine      bool   `json:"mine"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// ghReviewThreadsResponse is the `gh api graphql` reply for a PR's review
// threads. gh pr view cannot report threads at all (there is no reviewThreads
// JSON field), so this is the only route to resolution state.
type ghReviewThreadsResponse struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []ghReviewThread `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type ghReviewThread struct {
	ID         string `json:"id"`
	IsResolved bool   `json:"isResolved"`
	IsOutdated bool   `json:"isOutdated"`
	Path       string `json:"path"`
	Line       *int   `json:"line"`
	Comments   struct {
		Nodes []ghReviewComment `json:"nodes"`
	} `json:"comments"`
}

type ghReviewComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	Body      string `json:"body"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
}

type ghSearchCommitRow struct {
	SHA        string `json:"sha"`
	URL        string `json:"url"`
	Repository struct {
		FullName string `json:"fullName"`
	} `json:"repository"`
	Commit struct {
		Message string `json:"message"`
		Author  struct {
			Date string `json:"date"`
		} `json:"author"`
	} `json:"commit"`
}

// ghContext resolves the search user, host, and process environment shared by
// both collection modes. When target.username is empty, the GitHub search
// literal "@me" is used so the authenticated `gh` user is queried.
func (c GitHubCollector) ghContext(directive userdata.Directive, opts *CollectOpts) (user, host string, env []string) {
	user = strings.TrimSpace(directive.Target["username"])
	if user == "" {
		user = "@me"
	}
	token := ""
	if opts != nil {
		token = userdata.ResolveEnv(opts.UserdataDir, directive.CredentialRefs["token"])
	}
	baseURL := strings.TrimSpace(directive.Config["base_url"])
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	host = hostname(baseURL)
	env = os.Environ()
	if token != "" {
		env = append(env, "GITHUB_TOKEN="+token)
	}
	// `gh search` (and most other `gh` commands) target the host indicated by
	// the GH_HOST environment variable; the `--hostname` flag is only honored
	// by a handful of commands like `gh auth` and `gh api`.
	if host != "" && host != "github.com" {
		env = append(env, "GH_HOST="+host)
	}
	return user, host, env
}

// CollectState lists the user's currently-open PRs with draft and aggregate
// checks status: the "what is true right now" view, independent of the
// collection window. Scope decides how far past the user's own PRs to reach:
//
//   - ScopeSelf: only PRs the user authored.
//   - ScopeInvolved (default) / ScopeAll: also PRs awaiting the user's review
//     and any declared by `pr_queries` on the directive.
//
// See buildPRReviewSpecs.
func (c GitHubCollector) CollectState(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	user, host, env := c.ghContext(directive, opts)
	return c.collectPRReviewStatus(ctx, env, directive, user, host, opts)
}

// CollectEvents runs scoped GitHub search queries for PR/issue/comment/commit
// activity in opts.Since → window end. The exact set of queries depends on
// the resolved scope:
//
//   - ScopeSelf: only queries anchored on the user (`--author`).
//   - ScopeInvolved (default): user-anchored queries plus reviewed-by,
//     commenter, and involves queries.
//   - ScopeAll: ScopeInvolved queries plus per-repo searches for each
//     entry in `config.followed_repos` (no user filter). With no
//     followed_repos configured, ScopeAll behaves identically to
//     ScopeInvolved.
func (c GitHubCollector) CollectEvents(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	user, host, env := c.ghContext(directive, opts)

	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	dateStr := since.Format("2006-01-02")

	scope := opts.EffectiveScope()
	followedRepos := parseFollowedList(directive.Config["followed_repos"])
	searches, commitSearches := buildGitHubSearchSpecs(scope, user, dateStr, followedRepos)

	// One unit of progress per `gh search` invocation. Each issues/prs
	// search is a separate process spawn (a few hundred ms on a warm
	// cache, a few seconds otherwise) so a step-by-step counter is a
	// significantly better signal than the indefinite spinner.
	totalSteps := len(searches) + len(commitSearches)
	completed := 0
	emit := func(detail string) {
		reportProgress(opts, DirectiveProgress{
			DirectiveID: directive.ID,
			Description: directive.Name,
			Status:      "running",
			Detail:      detail,
			Completed:   completed,
			Total:       totalSteps,
		})
	}

	var items []StatusItem
	for _, spec := range searches {
		emit(fmt.Sprintf("search %s", spec.itemKind))
		batch, err := runGitHubSearch(ctx, env, spec, directive, user, host, since, now, opts)
		completed++
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	for _, spec := range commitSearches {
		emit(fmt.Sprintf("search %s", spec.itemKind))
		batch, err := runGitHubCommitSearch(ctx, env, spec, directive, user, host, since, now, opts)
		completed++
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	return dedupeGitHubItems(items), nil
}

// ghSearchSpec describes a single `gh search prs|issues` invocation.
//
// userAnchored is true for queries that are scoped to the configured user
// (author/reviewer/commenter/involves). Rows from those queries are
// IsSelf=true unconditionally. Repo-scoped queries (used in ScopeAll) set
// IsSelf based on whether the row's repository and the resolved user
// match.
type ghSearchSpec struct {
	queryType    string // "prs" or "issues"
	args         []string
	summary      string
	itemKind     string
	jsonFields   string
	userAnchored bool
}

// ghCommitSearchSpec mirrors ghSearchSpec for `gh search commits`, which
// returns a different JSON shape.
type ghCommitSearchSpec struct {
	args         []string
	summary      string
	itemKind     string
	userAnchored bool
}

// buildGitHubSearchSpecs returns the list of gh search invocations to run
// for the given scope. Exported (lowercase) for tests in this package.
func buildGitHubSearchSpecs(scope Scope, user, dateStr string, followedRepos []string) ([]ghSearchSpec, []ghCommitSearchSpec) {
	updatedFilter := ">=" + dateStr
	authoredPRs := ghSearchSpec{
		queryType:    "prs",
		args:         []string{"--author", user, "--updated", updatedFilter},
		summary:      fmt.Sprintf("author:%s updated:>=%s", user, dateStr),
		itemKind:     "authored_pr",
		jsonFields:   "title,url,state,isDraft,createdAt,updatedAt,closedAt,repository",
		userAnchored: true,
	}
	authoredCommits := ghCommitSearchSpec{
		args:         []string{"--author", user, "--author-date", updatedFilter},
		summary:      fmt.Sprintf("author:%s author-date:>=%s", user, dateStr),
		itemKind:     "github_commit",
		userAnchored: true,
	}

	if scope == ScopeSelf {
		return []ghSearchSpec{authoredPRs}, []ghCommitSearchSpec{authoredCommits}
	}

	// ScopeInvolved and ScopeAll both include the user-anchored set;
	// ScopeAll layers repo-scoped queries on top.
	involved := []ghSearchSpec{
		authoredPRs,
		{
			queryType:    "prs",
			args:         []string{"--reviewed-by", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("reviewed-by:%s updated:>=%s", user, dateStr),
			itemKind:     "reviewed_pr",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		{
			queryType:    "issues",
			args:         []string{"--involves", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("involves:%s updated:>=%s", user, dateStr),
			itemKind:     "involved_issue",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		// GitHub issue search requires is:issue or is:pull-request in the query;
		// --include-prs does not satisfy that, so split issues vs PRs.
		{
			queryType:    "issues",
			args:         []string{"is:issue", "--commenter", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("is:issue commenter:%s updated:>=%s", user, dateStr),
			itemKind:     "left_comment",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
		{
			queryType:    "prs",
			args:         []string{"--commenter", user, "--updated", updatedFilter},
			summary:      fmt.Sprintf("is:pull-request commenter:%s updated:>=%s", user, dateStr),
			itemKind:     "left_comment",
			jsonFields:   "title,url,state,updatedAt,repository",
			userAnchored: true,
		},
	}
	commits := []ghCommitSearchSpec{authoredCommits}

	if scope != ScopeAll || len(followedRepos) == 0 {
		return involved, commits
	}

	for _, repo := range followedRepos {
		involved = append(involved,
			ghSearchSpec{
				queryType:  "prs",
				args:       []string{"--repo", repo, "--updated", updatedFilter},
				summary:    fmt.Sprintf("repo:%s is:pull-request updated:>=%s", repo, dateStr),
				itemKind:   "repo_pr",
				jsonFields: "title,url,state,isDraft,createdAt,updatedAt,closedAt,repository",
			},
			ghSearchSpec{
				queryType:  "issues",
				args:       []string{"is:issue", "--repo", repo, "--updated", updatedFilter},
				summary:    fmt.Sprintf("repo:%s is:issue updated:>=%s", repo, dateStr),
				itemKind:   "repo_issue",
				jsonFields: "title,url,state,updatedAt,repository",
			},
		)
		commits = append(commits, ghCommitSearchSpec{
			args:     []string{"--repo", repo, "--author-date", updatedFilter},
			summary:  fmt.Sprintf("repo:%s author-date:>=%s", repo, dateStr),
			itemKind: "repo_commit",
		})
	}
	return involved, commits
}

func runGitHubSearch(ctx context.Context, env []string, spec ghSearchSpec, directive userdata.Directive, user, host string, since, now time.Time, opts *CollectOpts) ([]StatusItem, error) {
	args := append([]string{"search", spec.queryType}, spec.args...)
	args = append(args, "--limit", "25", "--json", spec.jsonFields)
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchActivityRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var items []StatusItem
	for _, row := range rows {
		obs, err := time.Parse(time.RFC3339, strings.TrimSpace(row.UpdatedAt))
		if err != nil || obs.Before(since) || obs.After(now) {
			continue
		}
		repo := strings.TrimSpace(row.Repository.NameWithOwner)
		if repo == "" {
			repo = gitHubOwnerRepoFromURL(row.URL)
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Repository:  repo,
			Source:      directive.Collector,
			Kind:        spec.itemKind,
			Title:       row.Title,
			Summary:     fmt.Sprintf("%s state=%s updated=%s", spec.summary, row.State, row.UpdatedAt),
			URL:         row.URL,
			Severity:    "info",
			ObservedAt:  obs.UTC(),
			Author:      user,
			IsSelf:      spec.userAnchored,
			Fields:      buildGitHubSearchFields(spec, user, host, repo, row),
		})
	}
	return items, nil
}

// buildGitHubSearchFields assembles the Fields map for a search row. For PR
// queries it additionally records is_draft and created_at (only present when
// the query requested them, i.e. authored_pr / repo_pr) so the report pipeline
// can tell an opened-in-window draft from an existing PR merely updated.
func buildGitHubSearchFields(spec ghSearchSpec, user, host, repo string, row ghSearchActivityRow) map[string]string {
	fields := map[string]string{
		"query":      spec.summary,
		"username":   user,
		"host":       host,
		"repo":       repo,
		"state":      row.State,
		"updated_at": row.UpdatedAt,
		"closed_at":  row.ClosedAt,
	}
	if spec.queryType == "prs" {
		fields["is_draft"] = strconv.FormatBool(row.IsDraft)
		if strings.TrimSpace(row.CreatedAt) != "" {
			fields["created_at"] = row.CreatedAt
		}
	}
	return fields
}

func runGitHubCommitSearch(ctx context.Context, env []string, spec ghCommitSearchSpec, directive userdata.Directive, user, host string, since, now time.Time, opts *CollectOpts) ([]StatusItem, error) {
	args := append([]string{"search", "commits"}, spec.args...)
	args = append(args, "--limit", "25", "--json", "sha,url,repository,commit")
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchCommitRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	var items []StatusItem
	for _, row := range rows {
		obs, err := time.Parse(time.RFC3339, strings.TrimSpace(row.Commit.Author.Date))
		if err != nil || obs.Before(since) || obs.After(now) {
			continue
		}
		msg := strings.TrimSpace(row.Commit.Message)
		title := msg
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			title = strings.TrimSpace(msg[:i])
		}
		if title == "" {
			title = row.SHA
		}
		repo := strings.TrimSpace(row.Repository.FullName)
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			Repository:  repo,
			Source:      directive.Collector,
			Kind:        spec.itemKind,
			Title:       title,
			Summary:     fmt.Sprintf("%s repo=%s sha=%s authored=%s", spec.summary, repo, row.SHA, row.Commit.Author.Date),
			URL:         row.URL,
			Severity:    "info",
			ObservedAt:  obs.UTC(),
			Author:      user,
			IsSelf:      spec.userAnchored,
			Fields: map[string]string{
				"query":       spec.summary,
				"username":    user,
				"host":        host,
				"repo":        repo,
				"sha":         row.SHA,
				"authored_at": row.Commit.Author.Date,
			},
		})
	}
	return items, nil
}

// dedupeGitHubItems merges duplicates that arise when the same PR / issue /
// commit shows up in multiple search results (e.g. a PR authored by the user
// is also reviewed by them, or repo-scoped queries surface the same PR an
// involves query already found). We key off URL when present, falling back
// to (kind, title, observedAt). When merging, IsSelf=true wins so the
// strongest signal sticks.
func dedupeGitHubItems(items []StatusItem) []StatusItem {
	if len(items) <= 1 {
		return items
	}
	seen := make(map[string]int, len(items))
	out := make([]StatusItem, 0, len(items))
	for _, it := range items {
		key := it.URL
		if key == "" {
			key = fmt.Sprintf("%s|%s|%s", it.Kind, it.Title, it.ObservedAt.UTC().Format(time.RFC3339Nano))
		}
		if idx, ok := seen[key]; ok {
			if it.IsSelf && !out[idx].IsSelf {
				out[idx].IsSelf = true
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, it)
	}
	return out
}

// prReviewSpec is one open-PR search feeding the review-status view. Args are
// spliced into `gh search prs` verbatim, so they may be flag pairs
// ("--author", user) or bare GitHub search qualifiers ("author:app/ci-bot").
// Mine marks PRs whose outcome the user owns, which decides both how much
// status we resolve for them and how the dashboard classifies them.
type prReviewSpec struct {
	relation string
	args     []string
	mine     bool
	// reviewable marks the candidate pool: open PRs in repos the user follows
	// that nobody has asked them to look at. They are neither the user's work
	// nor a request of them, which is a third thing from mine — see
	// buildPRReviewSpecs.
	reviewable bool
}

// relationReviewable labels every row from the candidate pool. Unlike the other
// relations it says nothing about where the PR came from, because the pool is
// defined by what it is not: not mine, and not asked of me.
const relationReviewable = "reviewable"

// buildPRReviewSpecs plans the open-PR searches for the review-status view.
//
//   - authored (`--author`): the user's own PRs, at every scope.
//   - review-requested (`--review-requested`): someone else's PR awaiting the
//     user's review. GitHub drops you from the requested reviewers once you
//     submit a review, so presence is a good proxy for "my review is not yet
//     given". Adjacent context, so involved/all only.
//   - one spec per directive-declared pr_queries entry: PRs a bot opens on the
//     user's behalf. Owned like authored PRs, but likewise adjacent context
//     rather than something the user wrote, so involved/all only.
//   - the candidate pool: every open non-draft PR in a followed repo, plus any
//     open PR assigned to the user. Nothing here has asked for the user, which
//     is the point — it is the set they could pick a review from. Drafts are
//     excluded from the repo searches (an unfinished PR is not review work) but
//     not from the assignee search, since a draft handed to you personally is
//     still yours to look at.
//
// Order defines dedupe precedence when a PR matches several searches, which is
// what keeps the candidate pool from swallowing the user's own PRs: it is last,
// so a PR that any earlier search already found stays whatever that search said
// it was.
func buildPRReviewSpecs(scope Scope, user string, extra []userdata.PRQuery, followedRepos []string) []prReviewSpec {
	specs := []prReviewSpec{{relation: "authored", args: []string{"--author", user}, mine: true}}
	if scope == ScopeSelf {
		return specs
	}
	for _, q := range extra {
		qualifiers := strings.Fields(q.Qualifiers)
		if len(qualifiers) == 0 {
			continue
		}
		specs = append(specs, prReviewSpec{relation: strings.TrimSpace(q.Relation), args: qualifiers, mine: true})
	}
	specs = append(specs, prReviewSpec{relation: "review_requested", args: []string{"--review-requested", user}, mine: false})
	for _, repo := range followedRepos {
		specs = append(specs, prReviewSpec{
			relation:   relationReviewable,
			args:       []string{"--repo", repo, "--draft=false"},
			reviewable: true,
		})
	}
	return append(specs, prReviewSpec{
		relation:   relationReviewable,
		args:       []string{"--assignee", user},
		reviewable: true,
	})
}

// collectPRReviewStatus lists the currently-open PRs across the searches
// buildPRReviewSpecs plans, independent of the collection window (PRs are open
// regardless of when they were last touched). The result drives the dashboard's
// status/action_required classification.
//
// Each open PR becomes one StatusItem with Kind "pr_review_status" and
// Fields: relation, mine (true|false), reviewable (true|false), is_draft,
// pr_author, assignees, checks (passing|failing|pending|none|unknown),
// review_decision, mergeable (mergeable|conflicting|unknown), and ready ("true"
// only when the PR is the user's own, not a draft, and checks are passing/none).
// See resolvePRDetail for which searches resolve how much.
func (c GitHubCollector) collectPRReviewStatus(ctx context.Context, env []string, directive userdata.Directive, user, host string, opts *CollectOpts) ([]StatusItem, error) {
	now := opts.windowEnd(c.Clock)
	specs := buildPRReviewSpecs(opts.EffectiveScope(), user, directive.PRQueries, followedPRRepos(directive))

	var found []prReviewRow
	counts := map[string]int{}
	order := make([]string, 0, len(specs))
	for _, spec := range specs {
		rows, err := c.listOpenPRs(ctx, env, directive, opts, spec.args...)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			found = append(found, prReviewRow{spec: spec, row: row})
		}
		if _, seen := counts[spec.relation]; !seen {
			order = append(order, spec.relation)
		}
		counts[spec.relation] += len(rows)
	}
	// Deduped before resolution rather than after: a PR can match several
	// searches (one of the user's own also lives in a followed repo), and
	// resolving it once per match would spend a round trip to arrive at the
	// same answer and then throw it away.
	rows := capReviewCandidates(dedupePRReviewRows(found))

	details := make([]string, 0, len(order))
	for _, relation := range order {
		details = append(details, fmt.Sprintf("%d %s", counts[relation], relation))
	}
	totalSteps := len(rows) + 1
	completed := 1
	reportProgress(opts, DirectiveProgress{
		DirectiveID: directive.ID,
		Description: directive.Name,
		Status:      "running",
		Detail:      strings.Join(details, ", ") + " PR(s)",
		Completed:   completed,
		Total:       totalSteps,
	})

	// Resolved concurrently because each PR is an independent round trip and
	// there can be dozens: serially this is the slowest thing docent does, and
	// a scheduled collection has a fixed budget to finish inside.
	var mu sync.Mutex
	work := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < prDetailWorkers && i < len(rows); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range work {
				detail := c.resolvePRDetail(ctx, env, rows[idx].spec, rows[idx].row.URL, user, directive, opts)
				// The progress callback belongs to the caller and is not
				// promised to be safe to call from several goroutines, so it
				// reports under the same lock that advances the counter.
				mu.Lock()
				rows[idx].detail = detail
				completed++
				reportProgress(opts, DirectiveProgress{
					DirectiveID: directive.ID,
					Description: directive.Name,
					Status:      "running",
					Detail:      "checking PR status",
					Completed:   completed,
					Total:       totalSteps,
				})
				mu.Unlock()
			}
		}()
	}
	for i := range rows {
		work <- i
	}
	close(work)
	wg.Wait()

	items := make([]StatusItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, prReviewItem(directive, user, host, now, r.row, r.spec, r.detail))
	}
	return items, nil
}

// prDetailWorkers bounds the concurrent per-PR lookups. The calls are
// independent and dominated by latency, so a handful of workers turns a minute
// of waiting into a few seconds; the cap keeps docent from answering a busy
// week with a burst of dozens of `gh` processes.
const prDetailWorkers = 6

// maxReviewCandidates bounds the candidate pool. Each candidate costs a round
// trip, and a repo the user follows can have hundreds of open PRs, which would
// spend a whole collection on a list nobody scrolls to the bottom of. The most
// recently updated survive: a PR untouched for months is not what a review
// queue is for.
const maxReviewCandidates = 50

// prReviewRow is one search hit plus the spec that found it, carrying the
// resolved detail once it has one.
type prReviewRow struct {
	spec   prReviewSpec
	row    ghSearchActivityRow
	detail prDetail
}

// followedPRRepos returns the owner/repo entries of config.followed_repos.
//
// Bare-owner entries are dropped rather than passed through: `gh search prs
// --repo` requires owner/repo and errors on anything else, and this list now
// runs on every state collection, so one malformed entry would otherwise take
// down every open PR the daemon knows about.
func followedPRRepos(directive userdata.Directive) []string {
	var out []string
	for _, entry := range parseFollowedList(directive.Config["followed_repos"]) {
		if strings.Count(entry, "/") == 1 && !strings.HasPrefix(entry, "/") && !strings.HasSuffix(entry, "/") {
			out = append(out, entry)
		}
	}
	return out
}

// resolvePRDetail resolves as much of a PR as the search that found it warrants.
//
// A PR the user owns needs the full picture because docent classifies and acts
// on it, and a candidate needs it to answer the only question the review queue
// asks: is this waiting on a reviewer, or has it already moved on? A PR that
// merely requests the user's review gets only its head branch, which
// correlation needs to anchor it to a repo. That is not a saving so much as a
// deliberate omission: those rows are the user's own by the IsSelf reckoning in
// prReviewItem, so giving them a checks field would make a teammate's failing
// build indistinguishable, to a transition rule watching `checks`, from the
// user's own.
func (c GitHubCollector) resolvePRDetail(ctx context.Context, env []string, spec prReviewSpec, prURL, user string, directive userdata.Directive, opts *CollectOpts) prDetail {
	if spec.mine || spec.reviewable {
		return c.fetchPRDetail(ctx, env, prURL, user, directive, opts)
	}
	return prDetail{HeadBranch: c.fetchPRHeadRef(ctx, env, prURL, directive, opts)}
}

// listOpenPRs runs `gh search prs <relationArgs> --state open` and returns
// the parsed rows. relationArgs is a flag/value pair such as
// {"--author", user} or {"--review-requested", user}.
func (c GitHubCollector) listOpenPRs(ctx context.Context, env []string, directive userdata.Directive, opts *CollectOpts, relationArgs ...string) ([]ghSearchActivityRow, error) {
	args := append([]string{"search", "prs"}, relationArgs...)
	args = append(args, "--state", "open", "--limit", "100", "--json", "title,url,isDraft,createdAt,repository,updatedAt,author,assignees")
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchActivityRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// prReviewItem builds one pr_review_status StatusItem for an open PR found by
// the given search. How much of detail is populated depends on the search; see
// resolvePRDetail.
func prReviewItem(directive userdata.Directive, user, host string, now time.Time, row ghSearchActivityRow, spec prReviewSpec, detail prDetail) StatusItem {
	repo := strings.TrimSpace(row.Repository.NameWithOwner)
	if repo == "" {
		repo = gitHubOwnerRepoFromURL(row.URL)
	}
	fields := map[string]string{
		"username": user,
		"host":     host,
		"repo":     repo,
		"state":    "open",
		"relation": spec.relation,
		// Relations are open-ended (directives declare their own), so
		// downstream classification keys off these flags rather than trying to
		// enumerate relation names.
		"mine":       strconv.FormatBool(spec.mine),
		"reviewable": strconv.FormatBool(spec.reviewable),
		"is_draft":   strconv.FormatBool(row.IsDraft),
	}
	// Who wrote it and who holds it. Free from the search, and the two things a
	// review queue is read by: a candidate PR is chosen by whose it is long
	// before its diff is opened.
	if login := strings.TrimSpace(row.Author.Login); login != "" {
		fields["pr_author"] = login
		fields["pr_author_bot"] = strconv.FormatBool(row.Author.IsBot)
	}
	if assignees := assigneeLogins(row); assignees != "" {
		fields["assignees"] = assignees
	}
	// created_at lets the report tell a PR opened in-window from a pre-existing
	// one merely updated (open authored PRs surface here, not as authored_pr).
	if ca := strings.TrimSpace(row.CreatedAt); ca != "" {
		fields["created_at"] = ca
	}
	if detail.HeadBranch != "" {
		fields["head_branch"] = detail.HeadBranch
	}
	if spec.mine || spec.reviewable {
		fields["checks"] = detail.Checks
		fields["review_decision"] = detail.ReviewDecision
		fields["mergeable"] = detail.Mergeable
		// Unresolved review threads are the "someone commented and is waiting
		// on me" signal, which review_decision alone misses: a reviewer can
		// leave blocking questions without ever setting CHANGES_REQUESTED.
		fields["unresolved"] = strconv.Itoa(len(detail.Threads))
		// The six-bucket classification and whose court the ball is in. Omitted
		// rather than defaulted when the timeline could not be read, so a
		// consumer can tell "not classified" from "awaiting review".
		if detail.Status.Bucket != "" {
			fields["bucket"] = string(detail.Status.Bucket)
			fields["last_action"] = string(detail.Status.Side)
			if !detail.Status.At.IsZero() {
				fields["last_action_at"] = detail.Status.At.UTC().Format(time.RFC3339)
			}
		}
	}
	if spec.mine {
		fields["ready"] = strconv.FormatBool(!row.IsDraft && (detail.Checks == "passing" || detail.Checks == "none"))
		fields["unresolved_mine"] = strconv.Itoa(countThreadsAwaitingMe(detail.Threads))
		// The threads themselves travel only for the user's own PRs, where a
		// comment is something to answer. Carrying every thread on every
		// candidate would put a followed repo's whole review history in the
		// signal store to render a count.
		if len(detail.Threads) > 0 {
			// Fields are flat strings, so the thread list travels as JSON.
			if b, err := json.Marshal(detail.Threads); err == nil {
				fields["unresolved_threads"] = string(b)
			}
		}
	}
	// Prefer the PR's real last-updated time so an open PR reports when it was
	// actually touched (opened / pushed / commented / reviewed) rather than the
	// poll time. Fall back to poll time only when GitHub omits a parseable
	// updatedAt.
	obs := now
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(row.UpdatedAt)); err == nil {
		obs = t
	}
	// A candidate is somebody else's work, and saying otherwise would do real
	// damage: IsSelf is what puts a row in the user's own activity reports, and
	// it is the whole of the `self: true` guard that keeps an automation rule —
	// one that pushes commits at a failing build, say — pointed at the user's
	// PRs and not at a teammate's.
	author, self := user, true
	if spec.reviewable {
		self = false
		if login := strings.TrimSpace(row.Author.Login); login != "" {
			author = login
		}
	}
	return StatusItem{
		DirectiveID: directive.ID,
		Repository:  repo,
		Source:      directive.Collector,
		Kind:        "pr_review_status",
		Title:       row.Title,
		Summary: fmt.Sprintf("open pr relation=%s draft=%t checks=%s review=%s mergeable=%s bucket=%s",
			spec.relation, row.IsDraft, detail.Checks, detail.ReviewDecision, detail.Mergeable, detail.Status.Bucket),
		URL:        row.URL,
		Severity:   "info",
		ObservedAt: obs.UTC(),
		Author:     author,
		IsSelf:     self,
		Fields:     fields,
	}
}

// assigneeLogins joins a PR's assignees for the flat field map, empty when
// nobody holds it.
func assigneeLogins(row ghSearchActivityRow) string {
	logins := make([]string, 0, len(row.Assignees))
	for _, a := range row.Assignees {
		if login := strings.TrimSpace(a.Login); login != "" {
			logins = append(logins, login)
		}
	}
	return strings.Join(logins, ",")
}

// dedupePRReviewRows collapses PRs that surface in more than one search (both
// authored and review-requested, or one of the user's own that also lives in a
// followed repo) keyed by URL, keeping the first occurrence. Callers must pass
// rows in precedence order — collectPRReviewStatus collects them in
// buildPRReviewSpecs order, which puts authored first so its richer relation
// wins and the candidate pool last so it only ever contributes PRs no other
// search claimed.
func dedupePRReviewRows(rows []prReviewRow) []prReviewRow {
	seen := make(map[string]bool, len(rows))
	out := make([]prReviewRow, 0, len(rows))
	for _, r := range rows {
		key := r.row.URL
		if key == "" {
			key = r.spec.relation + "|" + r.row.Title
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// capReviewCandidates trims the candidate pool to the most recently updated
// maxReviewCandidates, leaving every other row untouched and in place. Only the
// candidates are capped because they are the only rows whose count is set by
// how busy other people are rather than by how much the user has in flight.
func capReviewCandidates(rows []prReviewRow) []prReviewRow {
	candidates := 0
	for _, r := range rows {
		if r.spec.reviewable {
			candidates++
		}
	}
	if candidates <= maxReviewCandidates {
		return rows
	}
	ranked := make([]int, 0, candidates)
	for i, r := range rows {
		if r.spec.reviewable {
			ranked = append(ranked, i)
		}
	}
	sort.SliceStable(ranked, func(a, b int) bool {
		return rows[ranked[a]].row.UpdatedAt > rows[ranked[b]].row.UpdatedAt
	})
	dropped := make(map[int]bool, candidates-maxReviewCandidates)
	for _, i := range ranked[maxReviewCandidates:] {
		dropped[i] = true
	}
	out := make([]prReviewRow, 0, len(rows)-len(dropped))
	for i, r := range rows {
		if !dropped[i] {
			out = append(out, r)
		}
	}
	return out
}

// fetchPRStatus resolves the aggregate checks status, review decision, head
// branch, and merge-conflict verdict for a single PR via `gh pr view --json
// ...`. checks is one of "passing", "failing", "pending", "none", or
// "unknown" (when the call fails or the payload can't be parsed); mergeable is
// one of "mergeable", "conflicting", or "unknown". Failures are non-fatal: an
// unknown status keeps the PR out of "ready" without aborting the whole run.
//
// The PR-level metadata (headRefName, reviewDecision, mergeable) and the
// statusCheckRollup are fetched in two separate `gh pr view` calls on purpose.
// statusCheckRollup needs read access to a PR's check results that fine-grained
// PATs cannot grant on private repos — there is no fine-grained "checks"
// permission (see ValidateDirective's probe) — and gh fails the *entire*
// `pr view` when it can't read that one field. Keeping statusCheckRollup out of
// the metadata call means a token that can't read checks still resolves the
// head branch (which correlation needs to anchor the PR to its repo), plus the
// review decision and mergeability; only the checks status degrades to unknown.
func (c GitHubCollector) fetchPRStatus(ctx context.Context, env []string, prURL string, directive userdata.Directive, opts *CollectOpts) (checks, reviewDecision, headBranch, mergeable string) {
	if strings.TrimSpace(prURL) == "" {
		return "unknown", "", "", "unknown"
	}
	view, err := c.fetchPRView(ctx, env, prURL, "reviewDecision,headRefName,mergeable", directive, opts)
	if err != nil {
		return "unknown", "", "", "unknown"
	}
	reviewDecision = strings.ToUpper(strings.TrimSpace(view.ReviewDecision))
	headBranch = strings.TrimSpace(view.HeadRefName)
	mergeable = normalizeMergeable(view.Mergeable)

	// Best-effort: a permission error (or any other failure) reading the
	// check rollup leaves checks="unknown" rather than discarding the
	// metadata resolved above.
	checks = "unknown"
	if rollup, err := c.fetchPRView(ctx, env, prURL, "statusCheckRollup", directive, opts); err == nil {
		checks = rollupChecksState(rollup.StatusCheckRollup)
	}
	return checks, reviewDecision, headBranch, mergeable
}

// fetchPRView runs `gh pr view <url> --json <fields>` and parses the subset of
// fields docent consumes. Splitting field sets across calls lets callers
// isolate a field (like statusCheckRollup) that needs permissions a token may
// lack, so one inaccessible field doesn't fail the whole lookup.
func (c GitHubCollector) fetchPRView(ctx context.Context, env []string, prURL, fields string, directive userdata.Directive, opts *CollectOpts) (ghPRView, error) {
	args := []string{"pr", "view", prURL, "--json", fields}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		return ghPRView{}, err
	}
	var view ghPRView
	if err := json.Unmarshal(out, &view); err != nil {
		return ghPRView{}, err
	}
	return view, nil
}

// normalizeMergeable maps gh's mergeable enum (MERGEABLE / CONFLICTING /
// UNKNOWN) to a lowercase label. GitHub computes mergeability asynchronously,
// so a freshly pushed PR reports UNKNOWN until that settles; that (and any
// unrecognized/empty value) collapses to "unknown" so a transient blip never
// looks like a real conflict.
func normalizeMergeable(raw string) string {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "MERGEABLE":
		return "mergeable"
	case "CONFLICTING":
		return "conflicting"
	default:
		return "unknown"
	}
}

// reviewThreadsQuery asks for a PR's review threads with enough of each
// conversation to decide whether it is still waiting on someone. 50 threads and
// 50 comments per thread is not paginated on purpose: a PR that exceeds either
// is past the point where a follow-up queue helps, and an unbounded walk would
// multiply API calls across every open PR on every poll.
const reviewThreadsQuery = `
query($owner:String!,$name:String!,$number:Int!){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      reviewThreads(first:50){
        nodes{
          id isResolved isOutdated path line
          comments(first:50){ nodes{ author{login} body url createdAt } }
        }
      }
    }
  }
}`

// fetchPRUnresolvedThreads returns the PR's unresolved review threads.
//
// Failure is non-fatal and yields no threads: unresolved comments are one
// attention signal among several, and a PR whose threads cannot be read should
// still surface for its checks and review decision.
func (c GitHubCollector) fetchPRUnresolvedThreads(ctx context.Context, env []string, prURL, viewer string, directive userdata.Directive, opts *CollectOpts) []ReviewThread {
	owner, name, number, ok := parsePRURL(prURL)
	if !ok {
		return nil
	}
	args := []string{
		"api", "graphql",
		"-f", "owner=" + owner,
		"-f", "name=" + name,
		"-F", "number=" + strconv.Itoa(number),
		"-f", "query=" + reviewThreadsQuery,
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		return nil
	}
	var resp ghReviewThreadsResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil
	}
	return filterUnresolvedThreads(resp.Data.Repository.PullRequest.ReviewThreads.Nodes, viewer)
}

// filterUnresolvedThreads reduces review-thread nodes to the threads that are
// still open work, summarizing each by its opening comment and marking whether
// the viewer spoke last. It takes the nodes rather than a whole response so both
// the standalone threads query and the consolidated PR-detail query can share it.
func filterUnresolvedThreads(nodes []ghReviewThread, viewer string) []ReviewThread {
	var threads []ReviewThread
	for _, node := range nodes {
		// An outdated thread points at a line that no longer exists, which
		// usually means the code was already changed in response to it. Only a
		// still-open, still-relevant thread is follow-up work.
		if node.IsResolved || node.IsOutdated {
			continue
		}
		comments := node.Comments.Nodes
		if len(comments) == 0 {
			continue
		}
		// The queue shows what was originally asked, since that is the work;
		// who spoke last only decides whose turn it is.
		first, last := comments[0], comments[len(comments)-1]
		t := ReviewThread{
			ID:        node.ID,
			Author:    first.Author.Login,
			Body:      truncateThreadBody(first.Body),
			URL:       first.URL,
			File:      node.Path,
			Mine:      viewer != "" && strings.EqualFold(last.Author.Login, viewer),
			UpdatedAt: last.CreatedAt,
		}
		if node.Line != nil {
			t.Line = *node.Line
		}
		threads = append(threads, t)
	}
	return threads
}

// countThreadsAwaitingMe counts unresolved threads whose last word was someone
// else's, i.e. the ones that are actually my turn.
func countThreadsAwaitingMe(threads []ReviewThread) int {
	n := 0
	for _, t := range threads {
		if !t.Mine {
			n++
		}
	}
	return n
}

// threadBodyLimit keeps a single quoted comment from dominating a dashboard
// payload that carries every open PR.
const threadBodyLimit = 500

func truncateThreadBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= threadBodyLimit {
		return s
	}
	return strings.TrimSpace(s[:threadBodyLimit]) + "…"
}

// parsePRURL splits a PR URL into owner, repo, and number. It accepts any host
// so the same parsing works for github.com and an enterprise instance.
func parsePRURL(raw string) (owner, name string, number int, ok bool) {
	i := strings.LastIndex(raw, "/pull/")
	if i < 0 {
		return "", "", 0, false
	}
	num, err := strconv.Atoi(strings.SplitN(strings.Trim(raw[i+len("/pull/"):], "/"), "/", 2)[0])
	if err != nil || num <= 0 {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(raw[:i], "/"), "/")
	if len(parts) < 2 {
		return "", "", 0, false
	}
	owner, name = parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return "", "", 0, false
	}
	return owner, name, num, true
}

// fetchPRHeadRef resolves only the PR head branch name. Used for
// review-requested PRs where we don't need checks/review_decision.
func (c GitHubCollector) fetchPRHeadRef(ctx context.Context, env []string, prURL string, directive userdata.Directive, opts *CollectOpts) string {
	if strings.TrimSpace(prURL) == "" {
		return ""
	}
	view, err := c.fetchPRView(ctx, env, prURL, "headRefName", directive, opts)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(view.HeadRefName)
}

// rollupChecksState reduces a statusCheckRollup array to a single label.
// Precedence is failing > pending > passing; an empty rollup (no checks
// configured) is "none". SUCCESS/NEUTRAL/SKIPPED count as passing.
func rollupChecksState(rollup []ghCheckRollupEntry) string {
	if len(rollup) == 0 {
		return "none"
	}
	failing := false
	pending := false
	for _, entry := range rollup {
		if entry.Typename == "StatusContext" {
			switch strings.ToUpper(entry.State) {
			case "SUCCESS":
			case "PENDING", "EXPECTED", "":
				pending = true
			default: // FAILURE, ERROR
				failing = true
			}
			continue
		}
		// CheckRun (and any unrecognized shape with a status field).
		if strings.ToUpper(entry.Status) != "COMPLETED" {
			pending = true
			continue
		}
		switch strings.ToUpper(entry.Conclusion) {
		case "SUCCESS", "NEUTRAL", "SKIPPED", "":
		default: // FAILURE, TIMED_OUT, CANCELLED, ACTION_REQUIRED, STARTUP_FAILURE, STALE
			failing = true
		}
	}
	switch {
	case failing:
		return "failing"
	case pending:
		return "pending"
	default:
		return "passing"
	}
}

// ValidateDirective checks that `gh` is installed, the optional token env var
// is populated, and `gh auth status` succeeds for the target host.
func (c GitHubCollector) ValidateDirective(ctx context.Context, directive userdata.Directive, opts *ValidateOpts) []ValidationIssue {
	var issues []ValidationIssue
	if _, err := exec.LookPath("gh"); err != nil {
		return []ValidationIssue{{
			Field:       "gh",
			Message:     "`gh` CLI not found on PATH",
			Remediation: "install GitHub CLI (https://cli.github.com/) and run `gh auth login`",
		}}
	}

	baseURL := strings.TrimSpace(directive.Config["base_url"])
	if baseURL == "" {
		baseURL = "https://github.com"
	}
	host := hostname(baseURL)

	userdataDir := ""
	if opts != nil {
		userdataDir = opts.UserdataDir
	}

	tokenKey := strings.TrimSpace(directive.CredentialRefs["token"])
	tokenVal := ""
	if tokenKey != "" {
		tokenVal = userdata.ResolveEnv(userdataDir, tokenKey)
		if tokenVal == "" {
			issues = append(issues, ValidationIssue{
				Field:       "credential_refs.token",
				Message:     fmt.Sprintf("token env %q is empty", tokenKey),
				Remediation: fmt.Sprintf("set %s in your environment or in %s/.env", tokenKey, userdataDir),
			})
		}
	}

	env := os.Environ()
	if tokenVal != "" {
		env = append(env, "GITHUB_TOKEN="+tokenVal)
	}
	// `gh` (other than `gh auth`/`gh api`) targets the host from GH_HOST, so
	// mirror ghContext here to keep the probes below pointed at an enterprise
	// host when configured.
	if host != "" && host != "github.com" {
		env = append(env, "GH_HOST="+host)
	}

	args := []string{"auth", "status"}
	if host != "" && host != "github.com" {
		args = append(args, "--hostname", host)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "gh", args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		remediation := "run `gh auth login`"
		if host != "" && host != "github.com" {
			remediation = fmt.Sprintf("run `gh auth login --hostname %s`", host)
		}
		issues = append(issues, ValidationIssue{
			Field:       "gh auth",
			Message:     fmt.Sprintf("`gh %s` failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out))),
			Remediation: remediation,
		})
		// Auth is broken; the token-permission probe would only produce
		// noise on top of the real problem, so stop here.
		return issues
	}

	// Auth works, but the token may still lack permissions the collector
	// relies on. Probe the one that silently breaks the dashboard: reading a
	// PR's check rollup (statusCheckRollup). Fine-grained PATs cannot grant
	// this on private repos — there is no fine-grained "checks" permission —
	// and gh fails the whole `pr view` when it can't read that field, which
	// (before the split in fetchPRStatus) also cost the PR its head branch and
	// therefore its repo attribution on the dashboard.
	user := strings.TrimSpace(directive.Target["username"])
	if user == "" {
		user = "@me"
	}
	issues = append(issues, probeGitHubTokenPermissions(ctx, env, user)...)
	return issues
}

// probeGitHubTokenPermissions checks whether the configured token can read a
// PR's check rollup, which is the permission gap most likely to silently
// degrade the dashboard. It returns a warning-severity issue when the token
// can't (rather than a hard failure), since docent still works without it.
// Returns nil when there's no open PR to probe against or the token can read
// checks fine.
func probeGitHubTokenPermissions(ctx context.Context, env []string, user string) []ValidationIssue {
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	prURL := ghSampleAuthoredPRURL(probeCtx, env, user)
	if prURL == "" {
		// No open authored PR to exercise the checks path; nothing to probe.
		return nil
	}

	// statusCheckRollup in isolation, so any permission error is unambiguously
	// about reading checks.
	cmd := exec.CommandContext(probeCtx, "gh", "pr", "view", prURL, "--json", "statusCheckRollup")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isChecksPermissionError(stderr.String()) {
			return []ValidationIssue{{
				Field:       "gh token permissions",
				Severity:    "warning",
				Message:     "GitHub token cannot read PR check status (statusCheckRollup): " + truncateMessage(firstLine(stderr.String()), 200),
				Remediation: "known fine-grained PAT limitation (there is no fine-grained \"checks\" permission); the dashboard still resolves PRs but their checks show as unknown. Use a classic PAT with `repo` scope if you need check status.",
			}}
		}
		// Any other failure (network blip, rate limit, an unrelated gh error)
		// isn't a token-permission problem; don't report it to avoid noisy
		// false positives in `docent doctor`.
	}
	return nil
}

// ghSampleAuthoredPRURL returns the URL of one open PR authored by user, or ""
// when none is found (or the lookup fails). Used only to give the permission
// probe a real PR to test against.
func ghSampleAuthoredPRURL(ctx context.Context, env []string, user string) string {
	cmd := exec.CommandContext(ctx, "gh", "search", "prs", "--author", user, "--state", "open", "--limit", "1", "--json", "url")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var rows []struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(out, &rows); err != nil || len(rows) == 0 {
		return ""
	}
	return strings.TrimSpace(rows[0].URL)
}

// isChecksPermissionError reports whether gh stderr indicates the token lacks
// access to a PR's statusCheckRollup (the fine-grained PAT limitation). GitHub
// returns "Resource not accessible by personal access token" with the offending
// GraphQL field path; we require the statusCheckRollup path so unrelated
// permission errors don't match.
func isChecksPermissionError(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "resource not accessible by personal access token") &&
		strings.Contains(s, "statuscheckrollup")
}

// firstLine returns the first non-empty line of s, trimmed.
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// truncateMessage caps s at max runes, appending an ellipsis when it had to cut.
func truncateMessage(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func hostname(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}
	return parsed.Hostname()
}

func gitHubOwnerRepoFromURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Path == "" {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}
