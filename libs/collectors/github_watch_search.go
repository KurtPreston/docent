package collectors

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
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

// searchWatchPages runs one watch search, reading every page it returns.
func (c GitHubCollector) searchWatchPages(ctx context.Context, run ghRun, q string) (watchScan, error) {
	args := []string{"api", "graphql", "--paginate", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
	out, err := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		return watchScan{}, watchSearchError(q, err)
	}
	scan, err := parseWatchSearchPages(out)
	if err != nil {
		return watchScan{}, fmt.Errorf("parse search %q: %w", q, err)
	}
	return scan, nil
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

// parseWatchSearchPages decodes `gh api graphql --paginate` output, which is one
// JSON document per page written back to back.
func parseWatchSearchPages(out []byte) (watchScan, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var scan watchScan
	for {
		var page watchSearchPage
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return scan, nil
		}
		if err != nil {
			return watchScan{}, err
		}

		// Every page reports the total; keep the largest in case it moved
		scan.matched = max(scan.matched, page.Data.Search.IssueCount)
		scan.returned += len(page.Data.Search.Nodes)

		// Search can return non-PR nodes as empty objects; skip them.
		for _, n := range page.Data.Search.Nodes {
			if n.URL != "" {
				scan.nodes = append(scan.nodes, n)
			}
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
