# Plan: gh-request-volume

## Goal

Cut how many GitHub requests one GitHub state collect makes, and how many it
makes at once. A measured poll of a typical configuration made about 80 `gh`
calls in about 23 seconds, every 5 minutes, and its parallel searches
occasionally tripped GitHub's secondary rate limit, which punishes bursts and
concurrent requests. Target: about 15 calls per poll, with plain searches never
running concurrently.

## Scope

In:

- Cache recently-closed PR details (head branch and state), which cannot change
  while a PR stays closed, instead of re-fetching them every poll.
- Fetch open-PR detail for several PRs per GraphQL request instead of one each.
- Run plain `gh search prs` calls one at a time within a collect.

Out:

- Changing what data docent collects or how it classifies PRs.
- Poll intervals and other user configuration.
- The events-mode collect, and automations that call GitHub themselves.
- Retrying after a rate limit (a separate pull request covers alerting).

## Execution rules

- All work happens on the `gh-request-volume` branch. One plan, one branch, one commit per phase.
- Prefix every commit message with `gh-request-volume: `.
- For each phase: implement only its scope, run validation, fix failures, commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- After the last phase: update docs and commit, delete this file and commit,
  open a pull request (ask first only if the repository is public; open
  it as a draft where supported unless the user approved it first), fix
  each small unrelated bug on its own branch from the base branch with its
  own pull request, then report results
  with every obstacle and workaround, every bug found and how it was handled,
  and how the user can try the changes.
- Docent is public: no private organization, repository, team, or person names
  in code, tests, docs, commits, or the pull request.

## Validation

```sh
go build ./apps/... && go vet ./... && go test ./...
```

## Phases

### Phase 1: Cache closed-PR details

`collectClosedPRs` runs `gh pr view --json headRefName,state` for every PR the
user closed in the last 7 days, on every poll. Store each answer in a small
JSON cache under `opts.CacheDir` (like the watch cache), keyed by PR URL and
the search row's `closedAt`, so a PR that is reopened and closed again is
fetched afresh. Only cache successful lookups with a terminal state (CLOSED or
MERGED). Prune entries whose PR no longer appears in the closed search. With no
`CacheDir`, behave as today.

Done: a second collect with an unchanged closed list makes no `gh pr view`
calls; tests cover a cache hit, a changed `closedAt`, a failed lookup not being
cached, and pruning.

### Phase 2: Batch open-PR detail queries

`resolveReviewRows` sends `prDetailQuery` once per PR, six at a time. Build one
GraphQL query that aliases the same `pullRequest` selection for up to 10 PRs
(grouped by repository or with per-alias repository lookups), run batches in
turn or with low concurrency, and parse each alias with `buildPRDetail`. A PR
whose alias comes back null or unparseable falls back to the existing single-PR
path, so partial errors behave as today. Rows needing only a head branch keep
their current path.

Done: 44 PRs resolve in about 5 requests; tests cover a full batch, a batch
with one null alias falling back, and results landing on the right rows.

### Phase 3: Run plain searches one at a time

`listPRReviewSpecs` runs every spec's search on up to 6 workers, and
`collectClosedPRs` runs its searches alongside. Serialize the plain
`gh search prs` calls across both through one gate per collect, while
path-filtered GraphQL watch scans keep running concurrently so a long scan
doesn't delay the rest.

Done: a test with a fake `gh` shows no two plain searches overlap, and watch
scans still run alongside them.

## Obstacles

## Found bugs
