# Plan: optimize-github-collection

## Goal

Cut the time a path-filtered GitHub watch query spends discovering open PRs.
Today every state collection pages through every open PR the query matches,
about 20 serial GraphQL pages and roughly a minute in a busy repository,
even though only a few dozen PRs change between polls. Reuse what earlier
runs learned about which PRs touch the watched paths, re-search only what
changed, and keep the result complete and verifiably current.

## Scope

In:

- Path-filtered watch queries (`pr_queries` with `watch: true` and `paths`).
- A persisted path-membership cache keyed by PR head and base.
- Incremental discovery (`updated:>=`) with rotating reconciliation scans.
- Open-state verification through the per-PR detail query.
- Regression tests proving incremental results equal full-scan results.

Out:

- Checks, reviews, mergeability, and every other per-PR detail. These are
  fetched fresh for every watched PR on every run, as today. Nothing is
  reused or invalidated based on `updatedAt` alone.
- Non-watch searches (authored, review-requested, assigned, followed repos).
- Backlog collectors, which another effort owns.
- Concurrency limits. Worker counts stay as they are.

## Design

- **Cache.** One JSON file per watch query under
  `<state dir>/cache/github-watch/`, named by a hash of host, user, search
  string, and sorted paths. It holds a schema version, the last scan's start
  time, a reconcile cursor, and one entry per open PR: URL, `createdAt`,
  `updatedAt`, `headRefOid`, `baseRefName`, whether it touches the watched
  paths, and the row fields the report shows. It is written atomically
  (temporary file, then rename) only after a collection succeeds. A missing,
  corrupt, or older-version file means a cold start. Collection uses the cache
  only when `CollectOpts.CacheDir` is set, so tests and other callers keep
  today's behavior.
- **Membership reuse.** A PR's path membership is reused only while its
  `headRefOid` and `baseRefName` match the cached ones. Any push or retarget
  recomputes it from the search's first 100 files or the full REST listing.
  A failed listing is reported (as in the parent branch) and never cached.
- **Incremental discovery.** On a warm cache, search
  `<query> updated:>=<last scan start - 30m>`. The overlap absorbs search
  index lag. Fresh rows replace cached ones; cached PRs not returned keep
  their cached rows.
- **Open-state check.** The detail query gains `state` and `baseRefName`. A
  watched PR whose detail says it is no longer open is dropped from the
  output and evicted from the cache.
- **Rotating reconciliation.** Each run also re-scans one `created:` slice of
  the query without the `updated:` filter, sized to stay within a few pages.
  The slice replaces its cached entries: PRs missing from it are evicted
  (closed, or no longer matching the qualifiers), and new ones are added.
  The cursor advances only when the slice succeeds. Slices cover the whole
  creation range in turn, which also reaches PRs beyond GitHub's
  1000-result cap. The incremental search and the slice run concurrently.
- **Cold start.** With no usable cache, or one older than 24 hours, run
  today's full scan (the 1000 most recently updated) to seed the cache, and
  keep the truncation warning until every slice has been reconciled once.
- **Failures.** A failed incremental search fails the collection, as a
  failed search does today. A failed reconcile slice yields a
  `collector_error` warning, and the cursor stays put. Stale data is never
  presented without a row saying so.

## Execution rules

- All work happens on the `optimize-github-collection` branch, stacked on
  `surface-watch-scan-gaps`. One plan, one branch, one commit per phase.
- Prefix every commit message with `optimize-github-collection: `.
- For each phase: implement only its scope, run validation, fix failures,
  commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- Keep source files under about 600 lines; put the cache and the incremental
  scan in their own files rather than growing `github.go` or
  `github_watch.go`.
- Never name private organizations, repositories, hosts, or people in code,
  tests, docs, commits, or the pull request. Use placeholders such as
  `my-org/my-repo` and `services/api`.
- After the last phase: update docs and commit, delete this file and commit,
  open a pull request (draft where supported; ask first only if the
  repository is public), fix each small unrelated bug on its own
  branch from the base branch with its own pull request, then report results
  with every obstacle and workaround, every bug found and how it was handled,
  and how the user can try the changes.

## Validation

```sh
export TMPDIR="$(cd "$TMPDIR" && pwd -P)"
export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.bareRepository GIT_CONFIG_VALUE_0=all
go build ./apps/... && go vet ./... && go test ./...
```

Also `go test -race` on the GitHub collector tests. Run `npm run typecheck`
in `apps/docentd/web` only if frontend files change.

## User steps

None. The pull request is opened only after the user approves its exact text.
Deployment follows the usual review, merge, release, and `docent update`.

## Phases

### Phase 1: Verify open state in the detail query

Add `state` and `baseRefName` to `prDetailQuery` and `prDetail`. Drop a
watched PR whose detail reports it closed or merged. Tests use the fake `gh`
harness: a watched PR closed between search and detail is left out, and open
PRs are unchanged.

### Phase 2: Watch membership cache

Add `github_watch_cache.go`: the cache type, key derivation, load and atomic
save, schema version, and corruption handling. Add `headRefOid` and
`baseRefName` to the watch search query. Add `CollectOpts.CacheDir`, set by
the report CLI and docentd to `<state dir>/cache`. The cache is written but
not yet read for discovery, so output is unchanged. Tests cover round trips,
key separation by host, user, query, and paths, corrupt files, and version
changes.

### Phase 3: Incremental discovery and rotating reconciliation

Add `github_watch_incremental.go`: the warm-cache path, membership reuse by
head and base, the `created:` slice planner and cursor, eviction, the
cold-start path, and the truncation warning until full coverage. Run the
incremental search and the slice concurrently. Tests drive several simulated
polls through the fake `gh`, mutating the fixture between them (new PR, push
that changes membership, retarget, close, label change that leaves the
qualifiers, PR beyond the 1000 cap, failed listing, failed slice), and assert
that each poll's output equals a full scan of the same fixture. They also
assert request counts per poll.

### Phase 4: Measure

With a temporary harness that isn't committed, compare full and incremental
discovery on fixtures for identical output, wall time, and `gh` call counts.
Then run one bounded, read-only live comparison on a real watch query from a
developer machine: one cold run, then one warm run a few minutes later.
Check that the warm run's watched set matches a fresh full scan, and record
timings and request counts. Stay within search rate limits, and never touch a
running daemon or its configuration.

## Obstacles

- The shell fake `gh` only gives canned replies, which can't model date
  ranges, sort orders, the result cap, or paging across polls. Added a
  stateful fake (`github_fakehub_test.go`) that reuses the test binary as
  `gh` through `TestMain`, so tests still go through the real exec path.
- A reconciliation slice whose last page hit the search cap looked like the
  end of the lap and claimed full coverage. A slice now ends a lap only when
  it has returned as many PRs as `issueCount` reports.
- A PR the user authored that leaves the watch query must keep its
  `authored` row, as a full scan would give it. Dropped watch rows are
  replanned from the pre-dedupe list, reusing resolved details.
- PRs that leave the query with no update at all (lost access) are only
  evicted when the slice reaches them, at most one lap later. A PR that
  leaves within 5 minutes of a poll stays one poll longer, in case the
  search index is lagging.

## Found bugs

- `go test -race ./libs/collectors` reports data races in the registry test
  fakes (`TestRegistryOnlyCollectorTypes`, `TestRegistryValidateAggregates`
  in `collectors_test.go`) on `main` as well. Separate and straightforward:
  fix on its own branch.
