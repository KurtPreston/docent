# Automations

Docent Automations are IFTTT-style rules: a **trigger** (a signal, a state
transition, or a schedule), optional **conditions**, and one or more
**actions** (webhook, shell, JIRA comment, Slack post, generate-and-deliver a
report, run a coding agent, or open an editor). `docentd` evaluates rules
continuously and dispatches matching ones; see the [README](../README.md) for
how this fits into the rest of docent.

## Where rules live

Rules are declared under `automations:` in `~/.config/docent/config.yaml`
(the same file as `ai:` and `directives:`), validated against
[`jsonschema/config.schema.json`](../jsonschema/config.schema.json) on load.
**Editing is YAML-only** — the dashboard's Automations page is read-only plus
a manual Run button (see [Dashboard](#dashboard-page) below) — and **changes
require a `docentd` restart**; there is no hot reload.

```yaml
automations:
  - id: checks-failing
    enabled: true
    trigger:
      type: transition
      source: github
      kind: pr
      when:
        field: checks
        to: failing
    actions:
      - type: agent
        prompt: "Fix the failing checks on this PR."
        workdir: worktree
        post:
          commit: "true"
          push: "true"

  - id: daily-standup
    enabled: true
    trigger:
      type: schedule
      at: "05:00"
      weekday: friday
    actions:
      - type: report
        mode: recent-activity
        days: 7
        deliver: slack
        channel: "#standup"
```

## Triggers

`trigger.type` is `signal`, `transition`, or `schedule` (empty is treated as
`signal`).

| Type | Fires when | Key fields |
|------|-----------|------------|
| `signal` (default) | A newly-collected signal matches the filters. | `source` (collector, e.g. `github`), `kind` (one or a list; friendly aliases like `pr` match `pr_review_status`/`pr_activity`, `ticket`/`issue` are interchangeable), `match.text` (regex against title+summary), `match.ticket_key` (require a JIRA key to be extractable), `match.fields` (exact field equality). |
| `transition` | An entity's state field changes value between two collects. | `source`, `kind`, `when.field` (required), `when.from` / `when.to` (either can be the sentinel `me`, meaning "belongs to you", e.g. `assignee: me` on an assignment). Empty `from`/`to` matches any value. `match.fields` narrows which entities are eligible, as it does for signals — an entity's state carries every field of the signal it came from. |
| `schedule` | A time is reached. | `cron` (5-field `min hour dom month dow`, local time, supports `*`, ranges, lists, `*/N` steps) **or** `at` (`"HH:MM"`, local time) with an optional `weekday` name to restrict to one day. |

Signal and transition rules are evaluated right after each collector run, so
they fire close to real time (subject to that directive's poll interval — see
[the webhook nudge](#webhook-nudge-forcing-a-collect) below to force it).
Schedule rules are checked once a second in the daemon and deduped so a rule
fires at most once per calendar minute.

**An entity missing from the previous snapshot reads as an empty old value**,
so `when.to: <value>` also fires the first time a matching entity is seen —
one rule covers both "this appeared already in the state I care about" and
"this later changed into it". Pin `when.from` when you want only the change.

**A daemon restart doesn't replay history**: the first successful collect of
each unit after startup only seeds a baseline (so docentd knows what
"existing" looks like); automations only fire from the *second* collect of a
unit onward. Otherwise every signal/state already in the lookback window
would look "new" on every restart.

## Conditions

Optional gates evaluated after the trigger matches, before any action runs:

- **`self: true|false`** — restrict to signals/entities that are (or aren't) yours.
- **`repos: [...]`** — restrict to listed repos.
- **`cooldown: "30m"`** (or `"7d"`, etc.) — suppress re-firing the same dedupe key within the window.
- **`dedupe_key: "..."`** — override the default dedupe key (`rule ID + signal/entity stable ID`, or `rule ID + from->to` for transitions).

## Action types

| Type | Purpose | Notable fields |
|------|---------|-----------------|
| `webhook` | HTTP POST | `url`, `headers`, `body` (Go templates — see [Templating](#templating) below; a JSON payload is sent by default when `body` is empty). |
| `shell` | Run a local command | `command`, `args`, `cwd`; the process gets `DOCENT_*` env vars (see Templating). Times out after 5 minutes. |
| `jira-comment` | Post a JIRA comment | `issue` (defaults to the matched ticket key), `body` (required). Uses the first enabled `jira` directive. |
| `slack-post` | Post a Slack message | `channel`, `body` (required). Uses the first enabled `slack` directive. |
| `report` | Generate and deliver an execution-mode report | `mode` (an [execution mode](Reporting.md#modes) id, or the special `goal-alignment`), `days`, `deliver` (`file` default / `slack` / `webhook`), `out_path`, and the optional prompt controls `prompt` / `context` (see [Report delivery](#report-delivery)). |
| `agent` | Start a hosted agent session in a provisioned workdir | `provider`, `workdir` (`worktree` default, or `open_path`), `base_ref` (branch point for a brand-new branch), `prompt` (required). Streams to the cockpit and ignores `post` — see [`agent` vs `agent-inline`](#agent-vs-agent-inline). |
| `agent-inline` | Run an agent to completion, then run `post`, all unattended | Same fields as `agent`, plus `post` (see below). |
| `open` | Open a path in the editor via the configured [open trigger](Dashboard.md#open-trigger--live-window-polling-cursor--wsm--none) | An optional templated `cwd`. Only available when `open_trigger` is configured. |

An action's failure doesn't stop the chain: every action in a rule runs even
if an earlier one failed, and each subsequent action's template/env context
gets an `ActionError` / `DOCENT_ACTION_ERROR` string describing every prior
failure so far (handy for a trailing `shell` or `slack-post` notifier that
reports what actually went wrong). The job as a whole is marked failed if
*any* action failed.

### Agent post-steps

`agent-inline` actions accept a `post:` map run after the agent finishes, in
the provisioned workdir:

- **`validate: "cmd1|cmd2"`** — pipe-separated shell commands (e.g. lint/test); any failure fails the action.
- **`commit: "true"`** and optional `commit_message` — `git add -A && git commit` (a clean tree is not an error).
- **`push: "true"`** — `git push -u origin HEAD:<branch>`.
- **`jira_comment: "true"`** and optional `jira_comment_body` (templated) — posts a comment to the matched ticket; requires an enabled `jira` directive.

### Where an agent runs

`workdir: worktree` (the default) resolves the branch's worktree through
[grove](https://github.com/KurtPreston/grove): `grove path <branch>`, run in the
grove project for the repo, creating the worktree if it does not exist yet. The
agent therefore lands in your normal `~/Code/<project>/<branch>` checkout, with
grove's copied `.env` files and its deterministic branch color, and promoting a
session to an editor is just opening a directory that is already right.

docent used to keep its own bare clone and worktree tree under the state dir.
That produced a second universe: a checkout you had never opened, missing every
piece of per-worktree setup. Nothing there is used any more, and
`~/.local/state/docent/repos` and `.../worktrees` can be deleted.

Two consequences of the worktree being yours:

- **Nothing is cleaned up after a run.** Deleting a real worktree because an
  agent finished with it would take uncommitted work with it. Use `grove prune`,
  which knows what has been merged.
- **The repository must already be a grove project.** docent will not clone it
  for you; run `grove clone` once. The project is found by walking up from the
  work item's local path when it has one, and otherwise by matching the repo's
  `origin` against the grove projects at (or one level below) the `code_home` /
  `paths` roots of your enabled `local-git` directives.

`base_ref` (templated) picks the ref a brand-new branch is created from. It is
ignored when the branch already exists locally or on the remote, which is the
usual case for a PR-triggered action; it matters when an automation opens fresh
work, e.g. `base_ref: release/4.1` for a backport.

`workdir: open_path` runs the agent directly in the work item's existing local
path instead, without involving grove.

Concurrent agent actions targeting the same repo+branch are serialized: two
agents sharing a git index corrupt each other, and now that the worktree is
yours, the damage would be to real work.

### Report delivery

The `report` action's `deliver` field picks the destination:

- **`file`** (default) — writes Markdown to `out_path` (templated), or `<output_dir>/standup-<date>.md` when unset.
- **`slack`** — posts to `channel` (or the action's default), truncated at 3500 characters with a `_(truncated)_` note; requires an enabled `slack` directive.
- **`webhook`** — POSTs the Markdown as the body to `url`.

`mode: goal-alignment` is a special mode id (not a real [execution
mode](Reporting.md#modes)): it loads active goals from
[`goals.yaml`](../README.md#goals) and asks the AI provider to review recent
`recent-activity`-shaped activity against them, instead of running a
configured mode's own prompt.

Two optional fields tune the prompt for any `report` action (built-in mode or
`goal-alignment`):

- **`context`** — extra guidance appended to the resolved prompt (after a
  blank line) without replacing it. Use this to layer team- or workflow-
  specific notes onto a shared prompt, e.g. telling the model how to interpret
  a particular JIRA status. This keeps such specifics out of the built-in
  prompts, which stay generic.
- **`prompt`** — a full instruction that *replaces* the mode's prompt for this
  run. For `goal-alignment`, supplying `prompt` overrides the generated
  alignment prompt (goals are then not injected automatically); leave it empty
  to keep the goals-driven prompt and only add `context`.

```yaml
      - type: report
        mode: goal-alignment
        days: 7
        deliver: file
        context: |-
          When interpreting JIRA tickets, a ticket that has reached the "Q/A"
          status (or any later status) is complete as far as this developer is
          concerned: their responsibility ends at handing work to the QA team.
          Do not describe such tickets as unfinished or stalled.
```

### Templating

Action strings that accept templates (`webhook.url`/`headers`/`body`,
`shell.command`/`args`/`cwd`, `jira-comment.body`, `slack-post.body`,
`agent.prompt`, `report.out_path`/`channel`) are Go `text/template` strings
rendered against the event: `.RuleID`, `.Source`, `.Kind`, `.Title`,
`.Summary`, `.URL`, `.Repo`, `.Branch`, `.OpenPath`, `.Ticket.Key` /
`.Ticket.Title` / `.Ticket.URL`, `.From`, `.To`, `.Fields` (map), `.IsSelf`,
`.ActionError`, and more. `shell` actions additionally get every field as a
`DOCENT_*` environment variable (e.g. `DOCENT_ACTION_ERROR`, `DOCENT_TICKET`).

## Webhook nudge (forcing a collect)

Signal/transition rules only see new data once the owning directive collects
again, which normally waits for its poll interval. `POST
/api/hooks/{directive}` (see [Dashboard › HTTP API](Dashboard.md#http-api))
force-collects that directive's `state` and `events` units immediately, so an
external event (e.g. a GitHub webhook relay) can make a rule fire right away.
It accepts the daemon's bearer token, an `X-Docent-Hook-Secret` header, or a
GitHub-style `X-Hub-Signature-256` HMAC against `DOCENT_HOOK_SECRET`.

## Dashboard page

The `/automations` tab lists every configured rule (id, enabled, trigger
summary, action types) and recent job history (status, error/message),
auto-refreshing every 10s. It's **read-only** for the rule definitions
themselves — edit `config.yaml` in Settings and restart docentd — but each
rule has a **Run** button that fires it immediately via `POST
/api/automations/{id}/run`. Manual runs:

- Work on **disabled** rules too (useful for testing a rule before flipping `enabled: true`).
- **Bypass cooldown** entirely.
- Accept an optional JSON body (`title`, `url`, `repo`, `branch`, `ticket`, `openPath`, `from`, `to`, `source`, `kind`, `fields`) that synthesizes an event, so signal/transition rules can be exercised without waiting for a real one.
- Run synchronously (the request waits up to 15 minutes) and return `{ ok, job }`.

`GET /api/automations` (`?limit=N`, default 50) returns the rule list plus
job history; `GET /api/automations/{id}` returns one rule's definition. Job
history is **in-memory only** — capped at 256 entries with a 24h TTL, and
lost on restart. Agent sessions are the exception: they persist, because a
conversation you come back to is not a job you forget.

## `agent` vs `agent-inline`

An `agent` action starts a **hosted session**: `docentd` provisions the
worktree, runs the turn in the background, streams the transcript to
[`/api/agents/{id}/events`](Dashboard.md#agent-sessions), and persists both the
record and the transcript under `$XDG_STATE_HOME/docent/agent-sessions/`. The
job itself completes as soon as the session starts, so a thirty-minute agent
run never blocks the collection loop. The result is a lane in the cockpit you
can watch, follow up on, and resume after a restart.

Sessions do **not** run the action's `post:` steps. Tell the agent to commit
and push in the prompt, or use `agent-inline`.

An `agent-inline` action is the older shape: run the agent to completion
in-process and then run `post:` (validate, commit, push, jira_comment) as one
unattended unit. Use it when you want a rule to finish a job end to end with
nobody watching; use `agent` when you want a session you will interact with.

This replaced a queue-to-disk handoff to a separate `docent-automations`
worker. No installer ever installed that worker, so every `agent` action had
been queuing forever with nothing to say so — the failure mode that made
hosting sessions in `docentd` worth doing.

## Gotchas

- **Cron and `at` use local time**, not UTC.
- **Manual Run bypasses cooldown** and works on disabled rules — don't be surprised if a "disabled" rule still fires when you click Run.
- **No hot reload** — edits to `automations:` in `config.yaml` take effect on the next `docentd` restart.
- **Action chains keep going after a failure** — a later action still runs (and can see `.ActionError` / `DOCENT_ACTION_ERROR`), but the job is recorded as failed overall.
- **`agent` actions ignore `post:` steps** — they start a session, which is interactive by nature; see [`agent` vs `agent-inline`](#agent-vs-agent-inline) above.
- **Kind aliases** — e.g. a rule with `kind: pr` also matches the concrete entity kind `pr_review_status`/`pr_activity`; `ticket`/`issue`/`issue_activity` are similarly interchangeable.
- **The `me` sentinel** in `when.to: me` / `when.from: me` means "the field's new/old value belongs to you" (`is_self`), not the literal string `"me"`.
- **PRs a bot opens for you need a `pr_queries` entry** — a `checks`/`mergeable` transition can only fire on a PR the collector actually sees, and by default that means PRs you authored. Backport bots author the PR themselves, so declare a search for them on the directive (see [Extra open-PR searches](Reporting.md#extra-open-pr-searches)). Match just those rows with `match.fields: { relation: <your label> }`.
- **Prefer `transition` over `signal` for GitHub PRs** — PR signals carry no `StableID`, so every PR a rule matches in one collect shares the dedupe key `<rule id>:`, and all but the first are dropped as duplicates. That silently loses four of five sibling backports of the same change. Transitions key off the entity ID (derived from the PR title) instead, which is distinct per PR.
