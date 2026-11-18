package collectors

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed github_watch_search.graphql
var watchSearchQuery string

// watchSearchError preserves GitHub's diagnostic alongside the process error.
func watchSearchError(query string, err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if stderr := strings.TrimSpace(string(exit.Stderr)); stderr != "" {
			return fmt.Errorf("gh api graphql (search %q): %w: %s", query, err, stderr)
		}
	}
	return fmt.Errorf("gh api graphql (search %q): %w", query, err)
}

// watchSearchPage is one page of watchSearchQuery's reply.
type watchSearchPage struct {
	Data struct {
		Search struct {
			IssueCount int `json:"issueCount"`
			PageInfo   struct {
				HasNextPage bool   `json:"hasNextPage"`
				EndCursor   string `json:"endCursor"`
			} `json:"pageInfo"`
			Nodes []watchSearchNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

// watchScan is what a paginated watch search returned: the PR nodes, how many
// results the search reported matching, and how many it actually returned.
type watchScan struct {
	nodes    []watchSearchNode
	matched  int
	returned int
}

// watchSearchNode is one PR from watchSearchQuery.
type watchSearchNode struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	IsDraft   bool   `json:"isDraft"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	// HeadRefOid and BaseRefName pin what a membership verdict was computed
	// against.
	HeadRefOid  string  `json:"headRefOid"`
	BaseRefName string  `json:"baseRefName"`
	Author      ghActor `json:"author"`
	Assignees   struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Files struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
}

// searchWatchPages runs one watch search, reading every page it returns. It
// pages by hand rather than with `gh --paginate`, so a page GitHub fails to
// serve is retried on its own instead of restarting the whole search.
func (c GitHubCollector) searchWatchPages(ctx context.Context, run ghRun, q string) (watchScan, error) {
	var scan watchScan
	after := ""
	for {
		p, err := c.fetchWatchPage(ctx, run, q, after)
		if err != nil {
			return watchScan{}, err
		}
		scan.addPage(p)
		if !p.Data.Search.PageInfo.HasNextPage {
			return scan, nil
		}
		after = p.Data.Search.PageInfo.EndCursor
	}
}

// fetchWatchPage reads one page of a watch search, starting after the cursor
// after, or at the start when it's empty. A page that fails the way an
// overloaded GitHub does is tried again before the failure counts.
func (c GitHubCollector) fetchWatchPage(ctx context.Context, run ghRun, q, after string) (watchSearchPage, error) {
	cmd := []string{"api", "graphql", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
	if after != "" {
		cmd = append(cmd, "-f", "endCursor="+after)
	}
	var page watchSearchPage
	err := retryGH(ctx, run, func() error {
		out, err := runAndLogExecContext(ctx, "gh", cmd, "", run.env, run.opts, run.directive.ID)
		if err != nil {
			return watchSearchError(q, err)
		}
		if page, err = parseWatchSearchPage(out); err != nil {
			return fmt.Errorf("parse search %q: %w", q, err)
		}
		return nil
	})
	return page, err
}

// ghRetryBackoff is how long to wait before each retry of a gh call that hit a
// passing GitHub failure. GitHub's search API answers a few percent of pages
// with a 5xx or a reply cut off mid-body, and a long scan reads dozens of
// pages, so without retries one bad page would fail the whole collection.
var ghRetryBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// retryGH runs attempt, retrying it after each backoff while it fails in a way
// IsTransient recognizes. It returns the last attempt's error.
func retryGH(ctx context.Context, run ghRun, attempt func() error) error {
	for i := 0; ; i++ {
		err := attempt()
		if err == nil || i >= len(ghRetryBackoff) || !IsTransient(err) || ctx.Err() != nil {
			return err
		}
		loggerFor(run.opts, run.directive.ID).Note("github: retrying after %v", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(ghRetryBackoff[i]):
		}
	}
}

// transientPattern matches the failures a server or network usually gets past
// within seconds: a 5xx status, or a reply cut off before its JSON ended.
var transientPattern = regexp.MustCompile(`(?i)\bHTTP 5\d\d\b|unexpected end of JSON input|unexpected EOF|connection reset by peer|i/o timeout`)

// IsTransient reports whether err looks like a passing server or network
// failure that retrying, or the next collection, is likely to get past.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		msg += " " + string(exit.Stderr)
	}
	return transientPattern.MatchString(msg)
}

// searchWatchWindows lists every open PR a watch query matches, past GitHub
// search's 1000-result cap. It searches newest created first, and each time
// the cap cuts a search short, searches again from the oldest creation time
// it reached. PRs come back most recently updated first, as a single search
// sorted that way would list them.
func (c GitHubCollector) searchWatchWindows(ctx context.Context, run ghRun, args []string) (watchScan, error) {
	var all watchScan
	seen := map[string]bool{}
	var upper time.Time
	for {
		// Search the next window, newest created first
		q := "is:pr is:open " + strings.Join(args, " ")
		if !upper.IsZero() {
			q += " created:<=" + upper.UTC().Format(time.RFC3339)
		}
		q += " sort:created-desc"
		w, err := c.searchWatchPages(ctx, run, q)
		if err != nil {
			return watchScan{}, err
		}

		// Keep the PRs no earlier window returned, noting how far back it reached
		var last time.Time
		added := 0
		for _, n := range w.nodes {
			last = parseGitHubTime(n.CreatedAt)
			if !seen[n.URL] {
				seen[n.URL] = true
				all.nodes = append(all.nodes, n)
				added++
			}
		}
		if w.returned >= w.matched {
			break
		}
		if added == 0 || last.IsZero() {
			return watchScan{}, fmt.Errorf("search %q stopped at the result cap without reaching older PRs", q)
		}

		// Resume at the oldest creation time reached, stepping past a second
		// the window didn't get beyond
		if last.Equal(upper) {
			last = last.Add(-time.Second)
		}
		upper = last
	}

	// Order as an updated-desc search would, settling ties by URL
	sort.SliceStable(all.nodes, func(i, j int) bool {
		ti, tj := parseGitHubTime(all.nodes[i].UpdatedAt), parseGitHubTime(all.nodes[j].UpdatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all.nodes[i].URL < all.nodes[j].URL
	})
	all.matched, all.returned = len(all.nodes), len(all.nodes)
	return all, nil
}

// addPage adds one search page to the scan.
func (s *watchScan) addPage(page watchSearchPage) {
	// Every page reports the total; keep the largest in case it moved
	s.matched = max(s.matched, page.Data.Search.IssueCount)
	s.returned += len(page.Data.Search.Nodes)

	// Search can return non-PR nodes as empty objects; skip them.
	for _, n := range page.Data.Search.Nodes {
		if n.URL != "" {
			s.nodes = append(s.nodes, n)
		}
	}
}

// parseWatchSearchPage decodes a single page of watchSearchQuery's reply.
func parseWatchSearchPage(out []byte) (watchSearchPage, error) {
	var page watchSearchPage
	err := json.Unmarshal(out, &page)
	return page, err
}

// filePaths returns the changed paths the search included.
func (n watchSearchNode) filePaths() []string {
	paths := make([]string, 0, len(n.Files.Nodes))
	for _, f := range n.Files.Nodes {
		paths = append(paths, f.Path)
	}
	return paths
}

// searchRow converts a search node into the row shape `gh search prs` yields,
// so the rest of the open-PR path treats both sources alike.
func (n watchSearchNode) searchRow() ghSearchActivityRow {
	row := ghSearchActivityRow{
		Title:     n.Title,
		URL:       n.URL,
		State:     "open",
		IsDraft:   n.IsDraft,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Author:    ghSearchUser{Login: n.Author.Login, IsBot: strings.EqualFold(n.Author.Typename, "Bot")},
	}
	for _, a := range n.Assignees.Nodes {
		row.Assignees = append(row.Assignees, ghSearchUser{Login: a.Login})
	}
	row.Repository.NameWithOwner = n.Repository.NameWithOwner
	return row
}
