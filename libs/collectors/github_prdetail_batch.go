package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// A poll can find dozens of open PRs that each need their full detail, and
// asking for each in its own request made the per-PR lookups most of a poll's
// GitHub traffic. GraphQL can ask for many PRs in one request by aliasing the
// same selection, so those lookups go out in batches instead.

// prDetailBatchSize is how many PRs one batched request asks for. Each PR
// selects up to 50 threads of 50 comments, so a batch stays well inside
// GitHub's per-request node and time limits.
const prDetailBatchSize = 10

// prResolveWorkers bounds how many lookups (batches or single PRs) run at
// once. Batching leaves only a few requests per poll, so a small pool keeps
// them from arriving together without costing much time.
const prResolveWorkers = 3

// prDetailBatchResponse decodes a batched reply: one aliased repository per PR.
type prDetailBatchResponse struct {
	Data map[string]*struct {
		PullRequest *ghPRDetail `json:"pullRequest"`
	} `json:"data"`
}

// prDetailBatchQuery builds a query asking for n PRs, aliased p0 … p(n-1),
// each with its own owner, name, and number variables.
func prDetailBatchQuery(n int) string {
	var vars, fields strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			vars.WriteString(",")
		}
		fmt.Fprintf(&vars, "$o%d:String!,$n%d:String!,$k%d:Int!", i, i, i)
		fmt.Fprintf(&fields, "  p%d: repository(owner:$o%d,name:$n%d){ pullRequest(number:$k%d){ ...prDetail } }\n", i, i, i, i)
	}
	return "query(" + vars.String() + "){\n" + fields.String() + "}\n" + prDetailFragment
}

// fetchPRDetailBatch asks for several PRs' detail in one request and returns
// what came back, keyed by the PR's position in urls. A PR that is missing from
// the reply, such as one the viewer can't read, is left out, and so is every
// PR when the reply can't be parsed; callers look those up on their own.
func (c GitHubCollector) fetchPRDetailBatch(ctx context.Context, run ghRun, urls []string) map[int]ghPRDetail {
	// Pass each PR's coordinates as variables rather than in the query text
	args := []string{"api", "graphql"}
	for i, u := range urls {
		owner, name, number, _ := parsePRURL(u)
		args = append(args, "-f", fmt.Sprintf("o%d=%s", i, owner), "-f", fmt.Sprintf("n%d=%s", i, name), "-F", fmt.Sprintf("k%d=%d", i, number))
	}
	args = append(args, "-f", "query="+prDetailBatchQuery(len(urls)))

	// As for one PR, a reply with errors still carries the PRs it could read
	out, _ := runAndLogExecContext(ctx, "gh", args, "", run.env, run.opts, run.directive.ID)
	var resp prDetailBatchResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		loggerFor(run.opts, run.directive.ID).Note("github: batched PR detail unusable (%v), looking up %d PR(s) one at a time", err, len(urls))
		return nil
	}
	got := make(map[int]ghPRDetail, len(urls))
	for i := range urls {
		if repo := resp.Data["p"+strconv.Itoa(i)]; repo != nil && repo.PullRequest != nil {
			got[i] = *repo.PullRequest
		}
	}
	return got
}

// planReviewLookups groups the rows to look up into lookup jobs. Rows that need
// full detail and have a parseable URL go in batches of prDetailBatchSize; every
// other row is a job of its own.
func planReviewLookups(rows []prReviewRow, indices []int) [][]int {
	var jobs [][]int
	var batch []int
	for _, i := range indices {
		r := rows[i]
		if _, _, _, ok := parsePRURL(r.row.URL); !ok || !(r.spec.mine || r.spec.reviewable) {
			jobs = append(jobs, []int{i})
			continue
		}
		batch = append(batch, i)
		if len(batch) == prDetailBatchSize {
			jobs = append(jobs, batch)
			batch = nil
		}
	}
	if len(batch) > 0 {
		jobs = append(jobs, batch)
	}
	return jobs
}

// resolveLookupJob looks up one job's rows and returns their detail in the
// same order. A batch of several PRs goes out as one request; a lone PR, or
// one a batch couldn't answer, takes the single-PR path with its fallbacks.
func (c GitHubCollector) resolveLookupJob(ctx context.Context, run ghRun, picked []prReviewRow) []prDetail {
	var got map[int]ghPRDetail
	if len(picked) > 1 {
		urls := make([]string, len(picked))
		for k, r := range picked {
			urls[k] = r.row.URL
		}
		got = c.fetchPRDetailBatch(ctx, run, urls)
	}

	// Build each answered PR, and look up the rest on their own
	out := make([]prDetail, len(picked))
	for k, r := range picked {
		if pr, ok := got[k]; ok {
			out[k] = buildPRDetail(pr, run.user)
			continue
		}
		out[k] = c.resolvePRDetail(ctx, run.env, r.spec, r.row.URL, run.user, run.directive, run.opts)
	}
	return out
}
