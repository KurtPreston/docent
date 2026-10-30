# Plan: pr-autoreview

## Goal

Let docent drive an automated reviewer over a team's pull requests: PRs that
touch a chosen part of a repository, or that request review from a chosen
team, regardless of who wrote them. The reviewer itself is a configured
`agent-inline` rule; docent only supplies generic building blocks.

## Scope

In:

- GitHub collector: `pr_queries` gain `watch` (a set of PRs followed
  regardless of author, with ownership decided by authorship) and `paths`
  (keep only PRs whose changed files fall under one of the given prefixes).
  Entries may share a relation, which unions them. Path-filtered queries list
  PRs through one paginated GraphQL search that includes changed files, so no
  per-PR file lookups are needed.
- `head_sha` on resolved `pr_review_status` rows, so a rule can fire on each
  new head.
- Reference docs, and a generic recipe for a reviewer that checks docs drift
  and added complexity, rejects with findings, never approves, and withdraws
  its own rejection once findings are resolved.
- `docs/best-practices.md` plus an `AGENTS.md` that requires reading it.

Out:

- Anything specific to one organization, repository, team, or path. Those
  live only in the user's own config or local scripts.
- Backfilling reviews of PRs already open when docentd starts.

Design decisions (agreed):

- Watch rows authored by the user stay `mine: true` and `IsSelf`; others are
  review candidates (`reviewable: true`, `IsSelf: false`). Watch queries take
  dedupe precedence over `authored`, so the user's own matching PRs carry the
  watch relation while keeping every owned-PR field.
- Watch rows are exempt from the candidate cap and from the closed-PR search.
- The review policy (verdicts, delta re-reviews, dismissing its own rejection,
  re-requesting a team) is prompt content in the recipe, not docent logic.

## Execution rules

- All work happens on the `pr-autoreview` branch. One plan, one branch, one commit per phase.
- Prefix every commit message with `pr-autoreview: `.
- For each phase: implement only its scope, run validation, fix failures, commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- After the last phase: update docs and commit, delete this file and commit,
  open a pull request (draft where supported; ask first only if the
  repository is public), fix each small unrelated bug on its own
  branch from the base branch with its own pull request, then report results
  with every obstacle and workaround, every bug found and how it was handled,
  and how the user can try the changes.
- This repository is public: no names of private organizations, repositories,
  teams, hosts, or people in code, docs, tests, commits, or this plan.
- `libs/collectors/github.go` is past the file-size limit: put new collector
  code in its own file rather than growing it.

## Validation

- `go build ./apps/... && go vet ./... && go test ./...`
- Web is untouched; skip `npm run typecheck` unless a phase changes it.

## User steps

- Phase 5: restart docentd so the new rule and queries load.
- Repository is public: the agent asks before pushing and opening the PR.

## Phases

### Phase 1: Best-practices doc

- `docs/best-practices.md`: docent is public and generic; private use cases
  become config-driven features, with specifics kept in user config or local
  scripts; examples use placeholders. `AGENTS.md` requires reading it.

### Phase 2: Watch and path-filtered PR queries

- `userdata.PRQuery`: add `Watch bool` and `Paths []string`. Validation allows
  a repeated relation only when every entry sharing it agrees on `watch`;
  paths must be non-empty relative prefixes, and only bare qualifiers are
  allowed alongside them. Update both schema copies
  (`jsonschema/config.schema.json`, `libs/config/configschema/config.schema.json`).
- Collector (new file): watch specs ordered before `authored`; ownership per
  row from the PR author; candidate cap skips watch rows; closed-PR specs skip
  watch queries. Path-filtered specs list via GraphQL `search` with `files`,
  falling back to the REST files listing when a PR has more than 100 files.
- Tests for spec planning, ownership, dedupe precedence, cap exemption, path
  filtering, and validation.

### Phase 3: head_sha on PR rows

- `headRefOid` in `prDetailQuery` and the `gh pr view` fallback; emit
  `head_sha` for resolved rows. Tests.

### Phase 4: Docs and recipe

- `docs/Reporting.md` (extra open-PR searches: `watch`, `paths`, shared
  relations, `head_sha`) and a generic "Review a team's PRs for docs drift and
  complexity" recipe in `docs/Automation-recipes.md`.

### Phase 5: Configure and try the rule (local only, nothing committed)

- Add the queries and rule to the user's own config, validate it, restart
  docentd, confirm the watch rows appear, and run the rule once manually
  against one of the user's own matching PRs, where the verdict can only be a
  comment.

## Obstacles

## Found bugs
