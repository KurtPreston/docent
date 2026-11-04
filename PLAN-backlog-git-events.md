# Plan: backlog-git-events

## Goal

Let reports answer "what did I do last week?" from a Backlog.md board's
committed task changes, while preserving offline use, existing state
collection, and queue automations. Enable the feature on an existing remote
installation after verifying it end to end.

## Scope

- Opt-in Git-backed events on the existing `backlog` collector, using
  `config.git_events: "true"`; absent or `"false"` keeps history disabled.
- Recorded task additions, status transitions, and substantive content
  updates, with the historical title, notes/final-summary changes, task
  identity, and commit evidence needed to explain the activity.
- Current Backlog state in combined reports, alongside the existing GitHub
  state pass. Activity-only reports receive historical events, not snapshots.
- Correlation and rendering that retain task details and distinguish current
  state from activity in the requested window.
- A backed-up, verified deployment to the user's existing instance. Keep its
  host, paths, configuration, and rollout evidence in the private task record,
  never this public repository.

Out of scope: tracker migration, history-writing agent rules, changes to
Backlog.md itself, a separate event journal, exact time tracking, and inferring
completion from `updatedAt`. There is no timestamp fallback when Git events
are disabled or unavailable. Current state continues to work without Git.

## Design contract

### Optional collection and compatibility

- Validate the string-valued boolean using the repository's existing config
  conventions. Reject malformed values explicitly.
- When disabled, event collection performs no Git or sync commands and emits
  no events. Existing state-only configurations and daemon unit defaults must
  remain state-only in practice despite the collector gaining an interface.
- `statuses` continues to filter current state only. It must not hide a
  historical transition because a task is now Done or archived.
- Reuse `pull_command` before an enabled history read, failing explicitly if
  it fails. Use a resolved HEAD snapshot for the whole traversal so concurrent
  board writes cannot mix revisions. Do not introduce implicit fetches,
  commits, checkouts, or changes to the board.
- Follow existing board ownership semantics: a configured personal board's
  events are relevant regardless of the identity of its sync commit author.
  Preserve commit attribution as evidence, not proof of who performed work.

### History semantics

- Read the board checkout's HEAD first-parent history. Compare each selected
  commit with its first parent, including merge commits, so a merged snapshot
  appears once rather than replaying every side-branch commit.
- Use commit recording time for the inclusive report window. Document that
  intermediate edits between snapshots and side-branch transitions collapsed
  by a merge are not recoverable. Do not call these exact action timestamps.
- Read the preceding snapshot even when it predates the window. Do not stop a
  traversal early merely because commit timestamps are non-monotonic.
- Identify tasks by their stored IDs within the directive, not filename,
  commit subject, or Git rename heuristics. Cover active, completed, and
  archived task locations and board paths nested inside a Git repository.
  Respect supported Backlog layout configuration rather than scanning
  unrelated Markdown files as tasks.
- A file addition is "first recorded"/"added to board", not proof that the
  underlying work began then. A root commit is a recorded baseline; an import
  of existing tasks must not be described as new accomplishments.
- Retain exact old/new statuses, including repeated transitions and reopenings.
  Archiving, moving, renaming, or deleting a file is not evidence of completion.
- Ignore timestamp-only, ordering-only, and formatting-only changes.
  Prioritization, labels, and archive moves are board maintenance, not completed
  work; avoid flooding activity reports with these alone.
- Include changed descriptive content, checklist progress, notes, comments,
  and final summaries where useful. Do not attach today's content to older
  events or repeatedly present an unchanged final summary as new work.
- Emit one deterministic activity signal per task per commit, with a
  structured set of changes. Its stable ID includes directive, task ID, and
  commit hash. Use a distinct activity kind from the existing `task` state
  kind, and never expose queue-trigger fields on activity signals.

### Reliability and presentation

- Reuse the collector's execution logging, cancellation, timeout, and shared
  Git concurrency/no-lazy-fetch protections. Batch history/blob reads where
  practical; do not spawn a full history traversal per current task.
- Empty repositories are a documented no-history case. Enabled collection
  against a non-repository, missing required Git objects, invalid task data,
  or a failed command produces an actionable error, not a successful empty
  report. Detect shallow boundaries that prevent comparison and explain how
  to obtain adequate history; do not synthesize creations at such boundaries.
- Keep Backlog identities separate from issue-tracker key namespaces and from
  identical task IDs on other boards. Group one task's events and state
  together without treating the board directory as a code checkout.
- Render transition details, meaningful content changes, and provenance in
  both compact Markdown and JSON. Do not reduce Backlog to an activity count.
  Treat board text as input data, not instructions.
- Keep current-state context visibly separate from in-window accomplishments.
  Existing unrelated report output should not change.

## Execution rules

- All work happens on the `backlog-git-events` branch. One plan, one branch, one commit per phase.
- Prefix every commit message with `backlog-git-events: `.
- For each phase: implement only its scope, run validation, fix failures, commit.
- Work around small obstacles and log them under Obstacles. Stop only if a
  fundamental assumption is wrong or a finding reshapes the effort.
- Log existing bugs found along the way under Found bugs. Fold in fixes that
  belong to this effort, save small unrelated fixes for their own branches,
  and leave nuanced ones unfixed for discussion.
- Keep modules focused and generally below 600 lines. Move responsibilities
  out of oversized files in a behavior-preserving commit before changing
  their behavior; preserve existing public names.
- After the last phase: review/update docs and commit any remaining changes,
  delete this file and commit, then open a draft pull request. This repository
  is public: show the exact proposed title/description and obtain permission
  before pushing or opening it. Request the repository owner's review.
- Fix small unrelated bugs on separate branches from the base branch, then
  report results with every obstacle/workaround, every bug and its disposition,
  deployment evidence, and exact commands for trying the feature.
- Do not publish private operational details or alter unrelated configuration,
  automations, startup hooks, or working trees.

## Validation

After each source-code phase, run targeted tests while iterating, then:

```sh
go build ./apps/... && go vet ./... && go test ./...
```

If anything under `apps/docentd/web` changes, also run:

```sh
cd apps/docentd/web && npm run typecheck
```

Use temporary Git repositories with controlled commit times and actual
Markdown task snapshots, plus existing injected runners for failure paths.
Do not use the real board as a test fixture or commit private data.
Pure documentation changes do not require a build.

## User steps

- Before implementation: approve this committed plan.
- Before public publication: approve the exact push and draft-PR actions.
  A public push or release is not required for the private deployment.
- Deployment is agent-operated using existing access. If active automation
  jobs cannot safely finish before restart, stop and agree on a restart window;
  never interrupt them merely to complete the rollout.

## Phases

### Phase 1: Isolate correlation responsibilities without behavior changes

`libs/correlation/correlation.go` currently exceeds 800 lines. Extract signal
conversion and the grouping responsibility that will need Backlog identity
support into focused files in the same package, preserving public functions,
ordering, and output. Move existing code, do not introduce new behavior.

Done: existing correlation, report, and formatter tests pass unchanged, and
the upcoming work can be made in appropriately sized modules.

Validation: targeted correlation/report/AI tests, then the full commands above.
Commit this pure move separately.

### Phase 2: Implement optional Git-backed Backlog events

Add configuration parsing/schema/help and doctor checks in the existing
Backlog collector and `libs/config/configschema`. Implement focused history,
snapshot-parsing, and event-mapping modules under `libs/collectors`, reusing
existing YAML and Git helpers where suitable. Preserve current state/queue
collection and test registry/daemon capability behavior.

Implement the design contract, including explicit disabled behavior,
first-parent comparisons, evidence timestamps, stable identities, historical
content, and failures. Reuse Backlog's Markdown section conventions, checking
the installed supported format rather than inventing a parallel task format.

Fixtures must cover:

- Disabled/default settings, invalid options, a non-Git board, an empty repo,
  malformed data, sync failure, cancellation, and missing/shallow history.
- Both window boundaries, parent snapshots outside the window, out-of-order
  timestamps, repeated collection, and multiple changes in one commit.
- Root/import baselines, repeated status transitions, reopening, substantive
  notes/checklist/final-summary changes, and metadata-only noise.
- Renames with heavy edits, archives/completed locations, deletions, nested
  board paths, spaces/non-ASCII filenames, and stable task-ID matching.
- Merge commits without duplicate side-branch activity; uncommitted changes
  must not contaminate committed events.
- Existing state filters and queue behavior, including default daemon units
  and no historical signals triggering the work queue.

Done: enabled directives return faithful, bounded historical signals;
disabled directives retain current behavior without new prerequisites.

Validation: collector/config/engine regression tests, then full validation.

### Phase 3: Integrate reports, correlation, rendering, and documentation

Update `libs/report/report.go` so the combined state pass includes Backlog
alongside GitHub, honoring collector allow-lists. Keep activity-only and
GitHub-only modes constrained. Add board-scoped task grouping in the extracted
correlation modules and retain meaningful event details through model
conversion and `libs/ai/workitem_formatter.go`.

Test an end-to-end report with both Backlog and another source so the
work-item renderer cannot silently discard Backlog detail. Exercise the
shared pipeline used by the report CLI and daemon, Markdown and JSON
formatters, opt-in and disabled behavior, two boards with the same task ID,
archived historical tasks, and present-state versus historical-state labels.
Preserve unrelated collector semantics and issue-tracker matching.

Update `docs/Reporting.md` and the relevant automation recipe/schema guidance:
the opt-in setting, state versus events, optional daemon event polling,
first-parent/recording-time limitations, troubleshooting, and examples with
generic paths. Explain that reports invoke event collection directly; adding
background event polling is not a prerequisite for reports.

Done: a fixture-based recent-activity report describes actual in-window task
changes and their evidence; a daily plan also includes current board context,
without claiming unchanged tasks were worked on yesterday.

Validation: report/correlation/formatter and affected daemon tests, then full
validation. No frontend changes are expected.

### Phase 4: Deploy and verify on the existing remote installation

Perform operational steps only after source validation. Record actual host
details and commands in the private task record, not public files.

1. Reinspect installed versions, lifecycle management, configured board path,
   enabled directive, report entry point, history availability, and active
   automation jobs. Do not assume the runtime still matches planning-time
   observations.
2. Save recoverable backups of the existing binaries and affected config.
   Build/package the tested revision with its dashboard assets and matching
   CLI/daemon versions using existing build conventions. Transfer only the
   required artifacts; do not change another session's checkout or require a
   public branch push. Record the deployed revision and restoration commands.
3. Set `git_events: "true"` on the existing Backlog directive. Preserve its
   ID, state poll, sync command, queue settings, credentials, and unrelated
   configuration. Do not add a background events poll unless needed beyond
   the on-demand reports requested here.
4. Validate configuration, let active jobs finish, then use the installed
   lifecycle commands for the controlled restart. Verify health, version,
   collector errors, state collection, and queue continuity.
5. Run a seven-day activity report and a daily plan using the instance's
   actual supported report interface. Inspect collected signals and rendered
   input as well as output. Cross-check representative changes against the
   board's Git diffs: at least a status change and a substantive content
   change, including an archived task if one exists in the window. Verify
   event counts against the relevant history, not merely a successful exit.
6. Confirm config/binaries persist in the existing installation and normal
   startup still uses them. Do not restart the entire host to prove this.
   On failure, restore the previous binaries/config and verify recovery;
   report the failure explicitly rather than leaving a partial deployment.

Done: the running instance produces verified Backlog activity in reports and
retains its existing queue behavior. Clean up temporary build/verification
processes and artifacts; leave the intended installed service running.

This phase's commit records generic deployment/troubleshooting guidance and
any directly related documentation refinements. Private rollout evidence
belongs only in the task record.

## Obstacles

- Phase 4 is paused at the restart gate: the existing instance has an active
  queue job and another queued job. Do not interrupt them. Source phases 1-3
  are committed and validated; version-stamped Linux binaries are staged,
  not installed. Private paths and job IDs are in the task record.
- The staged collector and offline report pipeline were verified without
  running the sync command or changing the daemon. An independent
  whole-archive comparison matched event identities and status transitions,
  including archived tasks. A daily plan retained current board context.
  Measurements and report evidence are in the private task record.
- The embedded dashboard build initially lacked TypeScript dependencies.
  Restoring the locked dependencies with `npm ci` resolved this; the
  production dashboard build and Linux cross-compilation then succeeded.
- Phase 1 validation initially hit existing test-environment assumptions:
  bare-repository tests conflict with a restrictive user Git setting, and
  macOS temporary-directory aliases produce duplicate paths. Validation passes
  with a canonical `TMPDIR` and process-local
  `GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.bareRepository
  GIT_CONFIG_VALUE_0=all`. No global Git settings or production code changed.

## Found bugs

None identified outside this effort. Backlog's absence from reports and the
loss of detailed task activity in count-only rendering are in scope.
The test-environment failures described above are tracked separately; no
unrelated product changes were made. The deterministic daily-plan renderer's
omission of Backlog activity was fixed as part of report integration.
