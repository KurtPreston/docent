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
| `transition` | An entity's state field changes value between two collects. | `source`, `kind`, `when.field` (required), `when.from` / `when.to` (either can be the sentinel `me`, meaning "belongs to you", e.g. `assignee: me` on an assignment). Empty `from`/`to` matches any value. |
| `schedule` | A time is reached. | `cron` (5-field `min hour dom month dow`, local time, supports `*`, ranges, lists, `*/N` steps) **or** `at` (`"HH:MM"`, local time) with an optional `weekday` name to restrict to one day. |

Signal and transition rules are evaluated right after each collector run, so
they fire close to real time (subject to that directive's poll interval — see
[the webhook nudge](#webhook-nudge-forcing-a-collect) below to force it).
Schedule rules are checked once a second in the daemon and deduped so a rule
fires at most once per calendar minute.

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
| `report` | Generate and deliver an execution-mode report | `mode` (an [execution mode](Reporting.md#modes) id, or the special `goal-alignment`), `days`, `deliver` (`file` default / `slack` / `webhook`), `out_path`. See [Report delivery](#report-delivery). |
| `agent` | Run a write-capable coding agent (`cursor` or `claude`) in a provisioned workdir | `provider`, `workdir` (`worktree` default, or `open_path`), `prompt` (required), `post` (see below). **Queued** to the [`docent-automations`](#the-docent-automations-worker) worker — see that section. |
| `agent-inline` | Same as `agent`, but runs in-process instead of queuing | Same fields as `agent`; used by tests or setups that don't run the worker. |
| `open` | Open a path in the editor via the configured [session manager](Dashboard.md#session-manager-cursor--wsm--none) | An optional templated `cwd`. Only available when `session_manager` is configured. |

An action's failure doesn't stop the chain: every action in a rule runs even
if an earlier one failed, and each subsequent action's template/env context
gets an `ActionError` / `DOCENT_ACTION_ERROR` string describing every prior
failure so far (handy for a trailing `shell` or `slack-post` notifier that
reports what actually went wrong). The job as a whole is marked failed if
*any* action failed.

### Agent post-steps

`agent` / `agent-inline` actions accept a `post:` map run after the agent
finishes, in the provisioned workdir:

- **`validate: "cmd1|cmd2"`** — pipe-separated shell commands (e.g. lint/test); any failure fails the action.
- **`commit: "true"`** and optional `commit_message` — `git add -A && git commit` (a clean tree is not an error).
- **`push: "true"`** — `git push -u origin HEAD:<branch>`.
- **`jira_comment: "true"`** and optional `jira_comment_body` (templated) — posts a comment to the matched ticket; requires an enabled `jira` directive.
- **`keep_workdir: "true"`** — skip cleanup of the provisioned worktree/clone (useful for debugging a failed run).

`workdir: worktree` (the default) clones/reuses a docent-owned worktree for
the signal's repo+branch; `workdir: open_path` runs the agent directly in the
work item's existing local path instead. Concurrent agent actions targeting
the same worktree are serialized so two rules can't provision/reset it at
once.

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
lost on restart. That's separate from the durable, on-disk queue described
next.

## The `docent-automations` worker

`agent` actions are **not** run in-process by `docentd`. Because a coding
agent run can take up to 30 minutes and needs a provisioned git
worktree/clone, `docentd` instead writes a job to a durable, on-disk queue
(`$XDG_STATE_HOME/docent/automation-jobs/*.json` by default), and a separate
binary drains it:

```sh
go run ./apps/docent-automations           # daemon: polls the queue every 5s
go run ./apps/docent-automations --once     # drain whatever's pending, then exit
```

| Flag | Default | Meaning |
|------|---------|---------|
| `--state-dir` | XDG state dir | Root containing `automation-jobs/`; must match `docentd`'s. |
| `--once` | `false` | Process pending jobs once and exit, instead of polling. |
| `--poll` | `5s` | Poll interval in daemon mode. |
| `--provider` | `cursor` | Default agent provider (`cursor` or `claude`) when an action omits `provider`. |

**This worker is not installed by any of the per-OS installers** — if you
have any `agent` (not `agent-inline`) actions configured, you need to build
and run `docent-automations` yourself (e.g. as its own `systemd --user`
service alongside `docentd`) or those actions will queue forever and never
execute. `agent-inline` runs in-process inside `docentd` instead and needs no
worker — use it for quick testing, but prefer `agent` in production so a slow
agent run can't block the daemon's collection loop.

## Gotchas

- **Cron and `at` use local time**, not UTC.
- **Manual Run bypasses cooldown** and works on disabled rules — don't be surprised if a "disabled" rule still fires when you click Run.
- **No hot reload** — edits to `automations:` in `config.yaml` take effect on the next `docentd` restart.
- **Action chains keep going after a failure** — a later action still runs (and can see `.ActionError` / `DOCENT_ACTION_ERROR`), but the job is recorded as failed overall.
- **`agent` actions silently queue with no worker running** — see [The `docent-automations` worker](#the-docent-automations-worker) above.
- **Kind aliases** — e.g. a rule with `kind: pr` also matches the concrete entity kind `pr_review_status`/`pr_activity`; `ticket`/`issue`/`issue_activity` are similarly interchangeable.
- **The `me` sentinel** in `when.to: me` / `when.from: me` means "the field's new/old value belongs to you" (`is_self`), not the literal string `"me"`.
