# Plan: backlog-queue

## Goal

Let docent run agents from a [Backlog.md](https://github.com/MrLesk/Backlog.md)
board used as a work queue. A person labels a task for an agent and writes a
brief in its Implementation Plan; docent notices, provisions a worktree, runs
the agent unattended, and reports the result. This replaces ad-hoc "start a
thread for this task" delegation, which can silently never start.

## Scope

In:

- A read-only `backlog` collector that reports each task on a board as a state
  entity, including a computed `queued` field.
- A generic trigger option so transition rules can fire for entities that
  already match when the daemon first sees them, so a task queued while the
  daemon was down still runs after a restart.
- Config schema, docs, and an automation recipe for a board-driven work queue.

Out:

- Writing to the board from docent. The agent claims and updates its task
  itself with `backlog` commands, following the recipe's prompt.
- Board syncing between machines. Users bring their own (for example, a git
  remote and a sync script run by `pull_command`).
- Retries. A failed run is surfaced to a human via a `-failed` label instead.

Also in: a generic per-rule concurrency limit, `conditions.max_concurrent`.
Today every matched event starts its own goroutine (`go d.runJob` in
`libs/automation/dispatcher.go`); only per-worktree locks and per-entity
cooldown exist, so several queued tasks would start several agents at once.

## Design

Collector directive:

```yaml
- id: tasks
  name: Task board
  collector: backlog
  enabled: true
  config:
    board: ~/tasks              # directory containing backlog/ (required)
    command: backlog            # optional, default "backlog"
    pull_command: ""            # optional, run before each collect (e.g. a sync script)
    statuses: "To Do,In Progress" # optional filter; default all non-archived
    queue_label: agent          # optional; enables the queued field
    queue_status: "To Do"       # optional, default "To Do"
  events:
    poll: { interval: 5m }
```

- Lists tasks with `backlog task list --json`, then `backlog task view ID --json`
  only for tasks carrying `queue_label`, to read the plan without N calls.
- Runs commands with `BACKLOG_CWD=<board>`.
- Emits one `StatusItem` per task: `Source: backlog`, `Kind: task`,
  `StableID: task:<directive>:<id>`, `Title`, and fields `id`, `status`,
  `labels` (comma-joined), `priority`, `project`, `due_date`, `path`,
  `ready` (from `isReady`), and `queued` (`"true"` when the task has
  `queue_label`, is in `queue_status`, and is ready, else `"false"`).
- For queued tasks, also exposes `plan` (the Implementation Plan text) and any
  leading `Key: value` header lines of the plan as fields, lower-cased, so a
  brief starting `Repo: my-org/my-repo` and `Branch: my-branch` sets `repo` and
  `branch` and the existing `workdir: worktree` provisioning works unchanged.
- No `ObservedAt`: a task listing is state, not activity.

Trigger option: `trigger.include_existing: true` on a `transition` rule makes
the first collect after start fire for entities already matching `when.to`
(and `match.fields`), instead of only seeding the baseline. Default stays
`false`, so existing rules are unchanged.

Recipe rule:

```yaml
- id: backlog-agent-queue
  trigger:
    type: transition
    source: backlog
    kind: task
    include_existing: true
    when: { field: queued, to: "true" }
  conditions:
    cooldown: 30m
    max_concurrent: 1
  actions:
    - type: agent-inline
      provider: copilot
      workdir: worktree
      prompt: |-
        Claim {{index .Fields "id"}} (set it In Progress) before doing anything,
        then follow its Implementation Plan ...
    - type: shell
      command: ~/.config/docent/notify.sh
```

## Execution rules

- All work happens on the `backlog-queue` branch. One plan, one branch, one commit per phase.
- Prefix every commit message with `backlog-queue: `.
- For each phase: implement only its scope, run validation, fix failures, commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- Docent is public: use placeholders (`my-org/my-repo`, `~/tasks`) in code,
  tests, docs, commits, and this plan. Name no private org, repo, host, or person.
- After the last phase: update docs and commit, delete this file and commit,
  ask before pushing (public repository), open a draft pull request, fix each
  small unrelated bug on its own branch from `main` with its own pull request,
  then report results with every obstacle and workaround, every bug found and
  how it was handled, and how the user can try the changes.

## Validation

```sh
go build ./apps/... && go vet ./... && go test ./...
```

Plus, for schema changes, any schema test under `jsonschema/` or `libs/config`.

## User steps

After the pull request merges, on the automation host:

1. Make the board reachable there (clone it, or host a bare git remote both
   machines sync with) and install the `backlog` CLI (`npm i -g backlog.md`).
2. Update docent to `main`, rebuild, and restart `docentd`.
3. Add the `backlog` directive and the queue rule to `~/.config/docent/config.yaml`.
4. Queue a low-risk task and watch it run.

## Phases

### Phase 1: backlog collector

- `libs/collectors/backlog.go`: `BacklogCollector` implementing `CollectState`
  and `ValidateDirective` (board required and exists; command on PATH).
  Command execution goes through an injectable runner so tests use canned JSON.
- Register it as `backlog` in `NewRegistry` (`libs/collectors/collectors.go`).
- `libs/collectors/backlog_test.go`: fields mapping, status filter, `queued`
  logic (label, status, readiness), plan header parsing, `pull_command`
  failure surfaces as a collect error, validation issues.
- Done: unit tests pass; `docent` validate accepts a sample directive.

### Phase 2: include_existing for transition triggers

- Add `IncludeExisting bool` (`include_existing`) to the trigger config type.
- In the engine's first-collect path, still seed the baseline but evaluate
  transition rules that set `include_existing` against an empty previous
  snapshot, so matching entities fire once.
- Tests: rule with the flag fires on first collect for a matching entity and
  not again on the second; rule without it keeps today's behavior.
- Done: engine and matcher tests pass.

### Phase 3: per-rule concurrency limit

- Add `conditions.max_concurrent` (int; 0 or unset = unlimited, today's
  behavior) to the rule conditions config.
- In the dispatcher, keep a limiter keyed by rule ID (so it survives
  `SetRules` reloads). When a rule already has `max_concurrent` jobs running,
  a new job waits FIFO instead of being dropped, and the job store and logs
  show it as queued.
- Generic: no backlog-specific code.
- Tests: `max_concurrent: 1` runs two matched events one after the other;
  unset keeps parallel behavior; a waiting job runs once the first finishes.

### Phase 4: schema and validation

- `jsonschema/config.schema.json`: `backlog` collector config keys,
  `trigger.include_existing`, and `conditions.max_concurrent`.
- Config validation warns when `include_existing` is set on a non-transition
  trigger.
- Done: schema tests pass; a sample config validates.

### Phase 5: docs and recipe

- `docs/Automations.md`: document `include_existing` in the Triggers table and
  the restart-baseline note, and `max_concurrent` with the other conditions.
- Collector docs (wherever collectors are listed): the `backlog` directive and
  its fields.
- `docs/Automation-recipes.md`: "Work queue from a Backlog.md board" recipe,
  including the claim-first prompt and a brief template with `Repo:` and
  `Branch:` header lines. The recipe sets `max_concurrent: 1`, and its prompt
  defines the outcomes:
  - Claim: sync the board if the user has a sync script, set the task
    In Progress, sync again, then start.
  - Success: move the task to Waiting (pull request open) or Done, with a note
    and the pull request as a reference.
  - Failure: append a note on where it stopped, move the task back to To Do,
    and replace the queue label with `<queue_label>-failed` (e.g.
    `agent-failed`) so it isn't re-queued and a human sees it in triage.
- Done: docs build/links check if any; validation passes.

## Obstacles

- Phase 1: the task's file is exposed as `file` (absolute), not `path`.
  Transition events read an entity's `path` coordinate as the work's checkout
  (`OpenPath`), and worktree provisioning prefers the project containing it,
  so a board that is itself a git repository would be picked as the agent's
  repository. `task list` also omits the path, so `file` is only set for
  viewed (queue candidate) tasks.
- Phase 1: the Design's directive example uses `events: poll`, but the
  collector is state-only; docs use `state: { poll: ... }`.
- Phase 1: in the agent shell, six local-git/worktree tests fail before any
  change here: the shell sets `safe.bareRepository=explicit` through
  `GIT_CONFIG_*` env vars, and `TMPDIR` is a symlinked path. With those
  removed and `TMPDIR` resolved, the full suite passes; validation is run
  that way.

## Found bugs
