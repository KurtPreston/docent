package collectors

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/prstatus"
)

// prDetail is everything docent needs to know about one open PR of its own.
type prDetail struct {
	// Checks is the normalized rollup label: passing, failing, pending, none, or
	// unknown.
	Checks         string
	ReviewDecision string
	HeadBranch     string
	Mergeable      string
	IsDraft        bool
	Threads        []ReviewThread
	// Status is the six-bucket classification and who-acted-last verdict. Its
	// Bucket is empty when the timeline could not be read, which is the signal
	// that classification is unavailable rather than "awaiting review".
	Status prstatus.Result
}

// prDetailQuery fetches, in one round trip, every per-PR field docent needs:
// the review decision and merge state, the head commit's check rollup, the
// review threads, and the reviews plus timeline that decide whose court the ball
// is in.
//
// It is one query rather than the three calls this replaced (`gh pr view` twice
// plus a threads query) because these are all properties of the same PR, and the
// collector runs this per open PR on every poll — the call count is the cost
// that matters. GraphQL also degrades better than REST here: a token that cannot
// read checks gets `statusCheckRollup: null` alongside otherwise-complete data,
// where the REST field would have failed the whole request.
//
// The `last:`/`first:` windows are deliberately unpaginated. Only the most recent
// action decides a bucket, so older history cannot change the answer; and a PR
// with more than 50 unresolved threads is past the point where a follow-up queue
// helps.
const prDetailQuery = `
query($owner:String!,$name:String!,$number:Int!){
  repository(owner:$owner,name:$name){
    pullRequest(number:$number){
      isDraft mergeable reviewDecision updatedAt headRefName
      author{ login __typename }
      commits(last:1){ nodes{ commit{ statusCheckRollup{ state } } } }
      reviewThreads(first:50){
        nodes{
          id isResolved isOutdated path line
          comments(first:50){ nodes{ author{login} body url createdAt } }
        }
      }
      reviews(last:30){ nodes{ state createdAt submittedAt body author{login __typename} } }
      timelineItems(last:30, itemTypes:[ISSUE_COMMENT,PULL_REQUEST_REVIEW,PULL_REQUEST_COMMIT,HEAD_REF_FORCE_PUSHED_EVENT,READY_FOR_REVIEW_EVENT]){
        nodes{
          __typename
          ... on IssueComment{ createdAt body author{login __typename} }
          ... on PullRequestReview{ createdAt submittedAt state author{login __typename} }
          ... on PullRequestCommit{ commit{ committedDate } }
          ... on HeadRefForcePushedEvent{ createdAt }
          ... on ReadyForReviewEvent{ createdAt }
        }
      }
    }
  }
}`

// ghActor decodes an author, carrying __typename so bots can be told apart. A
// null author (a deleted account) decodes to the zero value.
type ghActor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

func (a ghActor) actor() prstatus.Actor {
	return prstatus.Actor{Login: a.Login, Bot: strings.EqualFold(a.Typename, "Bot")}
}

// ghCommitNode carries the head commit's aggregate check rollup. The rollup is a
// pointer because a commit with no checks configured has a null one, which is
// GitHub saying "nothing to wait for" rather than "I could not tell you".
type ghCommitNode struct {
	Commit struct {
		StatusCheckRollup *ghCheckRollup `json:"statusCheckRollup"`
	} `json:"commit"`
}

type ghCheckRollup struct {
	State string `json:"state"`
}

type ghPRDetailResponse struct {
	Data struct {
		Repository struct {
			PullRequest *ghPRDetail `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type ghPRDetail struct {
	IsDraft        bool    `json:"isDraft"`
	Mergeable      string  `json:"mergeable"`
	ReviewDecision string  `json:"reviewDecision"`
	UpdatedAt      string  `json:"updatedAt"`
	HeadRefName    string  `json:"headRefName"`
	Author         ghActor `json:"author"`
	Commits        struct {
		Nodes []ghCommitNode `json:"nodes"`
	} `json:"commits"`
	ReviewThreads struct {
		Nodes []ghReviewThread `json:"nodes"`
	} `json:"reviewThreads"`
	Reviews struct {
		Nodes []struct {
			State string `json:"state"`
			// createdAt is when the review was started, submittedAt when it
			// became visible to others. Only the latter has moved the ball.
			CreatedAt   string  `json:"createdAt"`
			SubmittedAt string  `json:"submittedAt"`
			Body        string  `json:"body"`
			Author      ghActor `json:"author"`
		} `json:"nodes"`
	} `json:"reviews"`
	TimelineItems struct {
		Nodes []struct {
			Typename    string  `json:"__typename"`
			CreatedAt   string  `json:"createdAt"`
			SubmittedAt string  `json:"submittedAt"`
			Body        string  `json:"body"`
			Author      ghActor `json:"author"`
			Commit      struct {
				CommittedDate string `json:"committedDate"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"timelineItems"`
}

// fetchPRDetail resolves one PR through prDetailQuery, falling back to the
// `gh pr view` path when the query cannot be used or fails.
//
// The fallback is not defensive clutter: enterprise GitHub versions differ in
// which timeline item types and connections they expose, and losing the whole
// PR (its checks, its review decision, its conflict state) because one field was
// rejected would be a far worse outcome than losing the bucket. Callers can tell
// the difference: a fallback result has an empty Status.Bucket.
func (c GitHubCollector) fetchPRDetail(ctx context.Context, env []string, prURL, viewer string, directive userdata.Directive, opts *CollectOpts) prDetail {
	owner, name, number, ok := parsePRURL(prURL)
	if !ok {
		return c.fetchPRDetailFallback(ctx, env, prURL, viewer, directive, opts)
	}
	args := []string{
		"api", "graphql",
		"-f", "owner=" + owner,
		"-f", "name=" + name,
		"-F", "number=" + strconv.Itoa(number),
		"-f", "query=" + prDetailQuery,
	}
	// Errors are not returned early: `gh api graphql` exits non-zero whenever the
	// reply carries an errors array, including the partial-data case this query is
	// designed to tolerate (an unreadable check rollup). The body is parsed either
	// way, and only an unparseable or empty pullRequest falls back.
	out, _ := runAndLogExecContext(ctx, "gh", args, "", env, opts, directive.ID)
	var resp ghPRDetailResponse
	if err := json.Unmarshal(out, &resp); err != nil || resp.Data.Repository.PullRequest == nil {
		loggerFor(opts, directive.ID).Note("github: PR detail query unusable for %s, falling back to gh pr view", prURL)
		return c.fetchPRDetailFallback(ctx, env, prURL, viewer, directive, opts)
	}
	return buildPRDetail(*resp.Data.Repository.PullRequest, viewer)
}

// buildPRDetail converts a decoded GraphQL PR into docent's shape and classifies
// it.
func buildPRDetail(pr ghPRDetail, viewer string) prDetail {
	d := prDetail{
		Checks:         checksFromRollup(pr),
		ReviewDecision: strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)),
		HeadBranch:     strings.TrimSpace(pr.HeadRefName),
		Mergeable:      normalizeMergeable(pr.Mergeable),
		IsDraft:        pr.IsDraft,
	}
	d.Threads = filterUnresolvedThreads(pr.ReviewThreads.Nodes, viewer)

	in := prstatus.PR{
		Author:         pr.Author.actor(),
		IsDraft:        pr.IsDraft,
		Checks:         d.Checks,
		ReviewDecision: d.ReviewDecision,
		UpdatedAt:      parseGitHubTime(pr.UpdatedAt),
	}
	for _, r := range pr.Reviews.Nodes {
		at := parseGitHubTime(r.SubmittedAt)
		if at.IsZero() {
			at = parseGitHubTime(r.CreatedAt)
		}
		in.Reviews = append(in.Reviews, prstatus.Review{
			State:  strings.ToUpper(strings.TrimSpace(r.State)),
			At:     at,
			Body:   r.Body,
			Author: r.Author.actor(),
		})
	}
	for _, t := range pr.TimelineItems.Nodes {
		ev := prstatus.Event{Kind: t.Typename, Body: t.Body, Author: t.Author.actor()}
		switch t.Typename {
		case "PullRequestCommit":
			// A commit's own date, not when it landed on the PR: a rebased
			// branch would otherwise look freshly pushed.
			ev.At = parseGitHubTime(t.Commit.CommittedDate)
		case "PullRequestReview":
			ev.At = parseGitHubTime(t.SubmittedAt)
			if ev.At.IsZero() {
				ev.At = parseGitHubTime(t.CreatedAt)
			}
		default:
			ev.At = parseGitHubTime(t.CreatedAt)
		}
		in.Timeline = append(in.Timeline, ev)
	}
	d.Status = prstatus.Classify(in)
	return d
}

// checksFromRollup maps GitHub's aggregate rollup state onto docent's labels. A
// null rollup means the head commit has no checks configured, which is "none"
// (nothing to wait for), not "unknown".
func checksFromRollup(pr ghPRDetail) string {
	nodes := pr.Commits.Nodes
	if len(nodes) == 0 {
		return "unknown"
	}
	rollup := nodes[len(nodes)-1].Commit.StatusCheckRollup
	if rollup == nil {
		return "none"
	}
	switch strings.ToUpper(strings.TrimSpace(rollup.State)) {
	case "SUCCESS":
		return "passing"
	case "FAILURE", "ERROR":
		return "failing"
	case "PENDING", "EXPECTED":
		return "pending"
	default:
		return "unknown"
	}
}

func parseGitHubTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// fetchPRDetailFallback resolves what it can through `gh pr view` and the
// standalone threads query. It leaves Status.Bucket empty, since neither route
// exposes the timeline the classification needs.
func (c GitHubCollector) fetchPRDetailFallback(ctx context.Context, env []string, prURL, viewer string, directive userdata.Directive, opts *CollectOpts) prDetail {
	checks, decision, headBranch, mergeable := c.fetchPRStatus(ctx, env, prURL, directive, opts)
	return prDetail{
		Checks:         checks,
		ReviewDecision: decision,
		HeadBranch:     headBranch,
		Mergeable:      mergeable,
		Threads:        c.fetchPRUnresolvedThreads(ctx, env, prURL, viewer, directive, opts),
	}
}
