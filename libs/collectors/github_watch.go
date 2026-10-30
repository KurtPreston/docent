package collectors

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// Watch queries follow a set of open PRs regardless of who wrote them, such as
// every PR that touches part of a repository or asks a team for review. They
// exist so an automation can act on a team's PRs, not just the user's own,
// without those PRs being mistaken for the user's work.

// ghRun is what every `gh` call from the collector needs besides its arguments.
type ghRun struct {
	env       []string
	directive userdata.Directive
	opts      *CollectOpts
}

// watchPRReviewSpecs plans one spec per declared watch query. They are searched
// before `authored` so a user's own PR in the watched set carries the watch
// relation, which is what a rule over that set matches on; forRow keeps it
// owned.
func watchPRReviewSpecs(extra []userdata.PRQuery) []prReviewSpec {
	var specs []prReviewSpec
	for _, q := range extra {
		qualifiers := strings.Fields(q.Qualifiers)
		if !q.Watch || len(qualifiers) == 0 {
			continue
		}
		specs = append(specs, prReviewSpec{
			relation: strings.TrimSpace(q.Relation),
			args:     qualifiers,
			watch:    true,
			paths:    normalizeWatchPaths(q.Paths),
		})
	}
	return specs
}

// forRow settles ownership for one PR a spec found. Every other spec decides it
// up front, but a watch query mixes the user's PRs with other people's: the
// user's own stay owned, so rules guarded by `self: true` still see them, and
// the rest are review candidates.
func (s prReviewSpec) forRow(row ghSearchActivityRow, user string) prReviewSpec {
	if !s.watch {
		return s
	}
	mine := strings.EqualFold(strings.TrimSpace(row.Author.Login), strings.TrimSpace(user))
	s.mine, s.reviewable = mine, !mine
	return s
}

// listPRReviewSpec lists the open PRs one spec finds. A path filter can't be
// expressed in GitHub search, so those specs list through a GraphQL search that
// returns each PR's changed paths in the same round trip.
func (c GitHubCollector) listPRReviewSpec(ctx context.Context, run ghRun, spec prReviewSpec) ([]ghSearchActivityRow, error) {
	if len(spec.paths) == 0 {
		return c.listOpenPRs(ctx, run.env, run.directive, run.opts, spec.args...)
	}
	return c.listOpenPRsTouching(ctx, run, spec)
}

//go:embed github_watch_search.graphql
var watchSearchQuery string

// watchSearchPage is one page of watchSearchQuery's reply.
type watchSearchPage struct {
	Data struct {
		Search struct {
			Nodes []watchSearchNode `json:"nodes"`
		} `json:"search"`
	} `json:"data"`
}

// watchSearchNode is one PR from watchSearchQuery.
type watchSearchNode struct {
	Title     string  `json:"title"`
	URL       string  `json:"url"`
	IsDraft   bool    `json:"isDraft"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
	Author    ghActor `json:"author"`
	Assignees struct {
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

// listOpenPRsTouching runs a path-filtered watch query: every page of the
// GraphQL search, then the PRs that change a file under one of its paths.
func (c GitHubCollector) listOpenPRsTouching(ctx context.Context, run ghRun, spec prReviewSpec) ([]ghSearchActivityRow, error) {
	q := "is:pr is:open " + strings.Join(spec.args, " ")
	args := []string{"api", "graphql", "--paginate", "-f", "q=" + q, "-f", "query=" + watchSearchQuery}
	out, err := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		return nil, fmt.Errorf("gh api graphql (search %q): %w", q, err)
	}
	nodes, err := parseWatchSearchPages(out)
	if err != nil {
		return nil, fmt.Errorf("parse search %q: %w", q, err)
	}

	// The search returns at most 100 paths per PR; a larger PR that shows no
	// match among them is listed in full before it is ruled out.
	var rows []ghSearchActivityRow
	for _, n := range nodes {
		paths := n.filePaths()
		if !touchesWatchPaths(paths, spec.paths) && n.Files.TotalCount > len(paths) {
			paths = c.listPRFiles(ctx, run, n.URL)
		}
		if touchesWatchPaths(paths, spec.paths) {
			rows = append(rows, n.searchRow())
		}
	}
	return rows, nil
}

// parseWatchSearchPages decodes `gh api graphql --paginate` output, which is one
// JSON document per page written back to back.
func parseWatchSearchPages(out []byte) ([]watchSearchNode, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var nodes []watchSearchNode
	for {
		var page watchSearchPage
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			return nodes, nil
		}
		if err != nil {
			return nil, err
		}
		// Search can return non-PR nodes as empty objects; skip them.
		for _, n := range page.Data.Search.Nodes {
			if n.URL != "" {
				nodes = append(nodes, n)
			}
		}
	}
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

// listPRFiles lists every path a PR changes through the REST API, which pages
// past the search's 100-path window. A failure yields no paths, so the PR is
// left out rather than failing the whole collection.
func (c GitHubCollector) listPRFiles(ctx context.Context, run ghRun, prURL string) []string {
	owner, name, number, ok := parsePRURL(prURL)
	if !ok {
		return nil
	}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%s/files", owner, name, strconv.Itoa(number))
	args := []string{"api", endpoint, "--paginate", "--jq", ".[].filename"}
	out, err := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	if err != nil {
		loggerFor(run.opts, run.directive.ID).Note("github: could not list files of %s: %v", prURL, err)
		return nil
	}
	// One filename per line; names may contain spaces.
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}

// normalizeWatchPaths trims each configured path to its bare form without
// surrounding slashes, so "services/api/" and "services/api" mean the same.
func normalizeWatchPaths(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.Trim(strings.TrimSpace(p), "/"); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// touchesWatchPaths reports whether any changed path is one of the watched
// paths or lies under one. Matching is by whole path segment, so "services/api"
// does not match "services/api-gateway/main.go".
func touchesWatchPaths(changed, watched []string) bool {
	for _, c := range changed {
		for _, w := range watched {
			if c == w || strings.HasPrefix(c, w+"/") {
				return true
			}
		}
	}
	return false
}
