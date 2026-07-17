package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
	Title      string `json:"title"`
	URL        string `json:"url"`
	State      string `json:"state"`
	IsDraft    bool   `json:"isDraft"`
	UpdatedAt  string `json:"updatedAt"`
	ClosedAt   string `json:"closedAt"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
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

// CollectState lists the user's currently-open authored PRs with draft and
// aggregate checks status: the "what is true right now" view, independent of
// the collection window.
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
		jsonFields:   "title,url,state,updatedAt,closedAt,repository",
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
				jsonFields: "title,url,state,updatedAt,closedAt,repository",
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
			Fields: map[string]string{
				"query":      spec.summary,
				"username":   user,
				"host":       host,
				"repo":       repo,
				"state":      row.State,
				"updated_at": row.UpdatedAt,
				"closed_at":  row.ClosedAt,
			},
		})
	}
	return items, nil
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

// collectPRReviewStatus lists the user's currently-open PRs in two
// relationships that drive the dashboard's status/action_required
// classification, independent of the collection window (PRs are open
// regardless of when they were last touched):
//
//   - authored (`--author`): each PR resolves draft state, aggregate
//     checks, and reviewDecision so the engine can tell "approved" from
//     "awaiting-response".
//   - review-requested (`--review-requested`): PRs where the user's
//     review is still requested (GitHub drops you from the requested
//     reviewers once you submit a review, so presence is a good proxy for
//     "my review is not yet given").
//
// Each open PR becomes one StatusItem with Kind "pr_review_status" and
// Fields: relation (authored|review_requested), is_draft, checks
// (passing|failing|pending|none|unknown), review_decision, mergeable
// (mergeable|conflicting|unknown), and ready ("true" only when authored,
// not a draft, and checks are passing/none).
func (c GitHubCollector) collectPRReviewStatus(ctx context.Context, env []string, directive userdata.Directive, user, host string, opts *CollectOpts) ([]StatusItem, error) {
	now := opts.windowEnd(c.Clock)

	authored, err := c.listOpenPRs(ctx, env, directive, opts, "--author", user)
	if err != nil {
		return nil, err
	}
	reviewRequested, err := c.listOpenPRs(ctx, env, directive, opts, "--review-requested", user)
	if err != nil {
		return nil, err
	}

	totalSteps := len(authored) + 1
	completed := 1
	reportProgress(opts, DirectiveProgress{
		DirectiveID: directive.ID,
		Description: directive.Name,
		Status:      "running",
		Detail:      fmt.Sprintf("%d authored, %d review-requested PR(s)", len(authored), len(reviewRequested)),
		Completed:   completed,
		Total:       totalSteps,
	})

	var items []StatusItem
	for _, row := range authored {
		checks, decision, headBranch, mergeable := c.fetchPRStatus(ctx, env, row.URL, directive, opts)
		completed++
		reportProgress(opts, DirectiveProgress{
			DirectiveID: directive.ID,
			Description: directive.Name,
			Status:      "running",
			Detail:      "checking PR status",
			Completed:   completed,
			Total:       totalSteps,
		})
		ready := !row.IsDraft && (checks == "passing" || checks == "none")
		items = append(items, prReviewItem(directive, user, host, now, row, "authored", checks, decision, ready, headBranch, mergeable))
	}
	for _, row := range reviewRequested {
		headBranch := c.fetchPRHeadRef(ctx, env, row.URL, directive, opts)
		items = append(items, prReviewItem(directive, user, host, now, row, "review_requested", "", "", false, headBranch, ""))
	}
	return dedupePRReviewItems(items), nil
}

// listOpenPRs runs `gh search prs <relationArgs> --state open` and returns
// the parsed rows. relationArgs is a flag/value pair such as
// {"--author", user} or {"--review-requested", user}.
func (c GitHubCollector) listOpenPRs(ctx context.Context, env []string, directive userdata.Directive, opts *CollectOpts, relationArgs ...string) ([]ghSearchActivityRow, error) {
	args := append([]string{"search", "prs"}, relationArgs...)
	args = append(args, "--state", "open", "--limit", "100", "--json", "title,url,isDraft,repository,updatedAt")
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

// prReviewItem builds one pr_review_status StatusItem for an open PR in the
// given relationship. checks/decision/ready/mergeable are only meaningful for
// authored PRs; review-requested rows pass zero values.
func prReviewItem(directive userdata.Directive, user, host string, now time.Time, row ghSearchActivityRow, relation, checks, decision string, ready bool, headBranch, mergeable string) StatusItem {
	repo := strings.TrimSpace(row.Repository.NameWithOwner)
	if repo == "" {
		repo = gitHubOwnerRepoFromURL(row.URL)
	}
	fields := map[string]string{
		"username": user,
		"host":     host,
		"repo":     repo,
		"state":    "open",
		"relation": relation,
		"is_draft": strconv.FormatBool(row.IsDraft),
	}
	if headBranch != "" {
		fields["head_branch"] = headBranch
	}
	if relation == "authored" {
		fields["checks"] = checks
		fields["review_decision"] = decision
		fields["ready"] = strconv.FormatBool(ready)
		fields["mergeable"] = mergeable
	}
	// Prefer the PR's real last-updated time so an open PR reports when it was
	// actually touched (opened / pushed / commented / reviewed) rather than the
	// poll time. Fall back to poll time only when GitHub omits a parseable
	// updatedAt.
	obs := now
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(row.UpdatedAt)); err == nil {
		obs = t
	}
	return StatusItem{
		DirectiveID: directive.ID,
		Repository:  repo,
		Source:      directive.Collector,
		Kind:        "pr_review_status",
		Title:       row.Title,
		Summary:     fmt.Sprintf("open pr relation=%s draft=%t checks=%s review=%s mergeable=%s", relation, row.IsDraft, checks, decision, mergeable),
		URL:         row.URL,
		Severity:    "info",
		ObservedAt:  obs.UTC(),
		Author:      user,
		IsSelf:      true,
		Fields:      fields,
	}
}

// dedupePRReviewItems collapses PRs that surface in more than one
// relationship (e.g. authored and review-requested) keyed by URL. The
// authored row wins because it carries the richer checks/review_decision
// fields the engine needs.
func dedupePRReviewItems(items []StatusItem) []StatusItem {
	seen := make(map[string]int, len(items))
	out := make([]StatusItem, 0, len(items))
	for _, it := range items {
		key := it.URL
		if key == "" {
			key = it.Kind + "|" + it.Title
		}
		if idx, ok := seen[key]; ok {
			if it.Fields["relation"] == "authored" && out[idx].Fields["relation"] != "authored" {
				out[idx] = it
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, it)
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
// permission — and gh fails the *entire* `pr view` when it can't read that one
// field. Keeping statusCheckRollup out of the metadata call means a token that
// can't read checks still resolves the head branch (which correlation needs to
// anchor the PR to its repo), plus the review decision and mergeability; only
// the checks status degrades to unknown.
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

	args := []string{"auth", "status"}
	if host != "" && host != "github.com" {
		args = append(args, "--hostname", host)
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "gh", args...)
	env := os.Environ()
	if tokenVal != "" {
		env = append(env, "GITHUB_TOKEN="+tokenVal)
	}
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
	}
	return issues
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
