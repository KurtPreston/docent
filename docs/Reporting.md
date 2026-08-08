# docent-reporter

`docent-reporter` is the stateless CLI half of docent: collectors gather your
recent activity, an AI provider (or the deterministic `rule-based` formatter)
turns it into prose, and the result is saved as Markdown. This page is the
full reference for its configuration, execution modes, scope semantics, and
CLI flags. See the [README](../README.md) for how it fits into the rest of
the monorepo.

## Quick start

```sh
go test ./...
go run ./apps/docent-reporter --help
# First run creates ~/.config/docent/config.yaml if missing
go run ./apps/docent-reporter --mode recent-activity --days 3
```

Or use [`scripts/docent-reporter`](../scripts/docent-reporter) from the repo root.

Run `./scripts/setup` (or `docent-setup`) first if you haven't yet — see
[README › Setup](../README.md#setup).

## Configuration (`~/.config/docent/config.yaml`)

Single file: `ai`, `directives`, optional `execution_modes`, optional
`automations` (see [docs/Automations.md](Automations.md)), and optional
`output_dir`.

- **`directives`**: Collector, target, config, `credential_refs` for secrets in `~/.config/docent/.env`.
- **`local-git`**: Use **`paths`** for explicit repo roots, or **`code_home`** to scan that directory's immediate children that contain `.git`. Set **`config.scan_depth: "2"`** to also look one level further in — see [Nested repos](#nested-repos-configscan_depth).
- **`output_dir`** (optional): where `docent-reporter` writes generated markdown (supports a leading `~`). Defaults to `~/docent`; override per-run with `--out-dir`.

Config shape is validated at runtime against [`jsonschema/config.schema.json`](../jsonschema/config.schema.json) (kept in sync with the embedded copy at [`libs/config/configschema/config.schema.json`](../libs/config/configschema/config.schema.json); tests enforce this). See [README › Setup](../README.md#setup) for how `docent-setup` populates the file.

### Activity formatter (`ai.activity_formatter`)

Optional field on **`ai`**. It chooses how raw collector rows are turned into the **activity text** that is injected into model prompts and into **`rule-based`** markdown—the same shaping runs for every provider.

| Value | What you get |
|-------|----------------|
| **`repo-chronological`** (default) | Markdown grouped by `repository`: a heading per repo (repos sorted alphabetically), signals in time order within each repo, one compact line per signal (RFC3339 time, source, kind-specific summary). Rows without a repository go under a **`(no repository)`** section. **`collector_error`** rows are listed last under **`Collector errors`**. In **`daily-plan`** and **`custom-prompt`**, repo headings use `###` so they nest under the outer `##` sections; **`recent-activity`** uses `##` for repos at the top level. |
| **`json-signal-list`** | The full collected status list as indented JSON (every field on each item). Heavier prompts; useful for debugging or when you want structured input. |

If you omit **`activity_formatter`**, it defaults to **`repo-chronological`**. Values are compared case-insensitively; underscores are treated like hyphens (for example `repo_chronological` works).

Example:

```yaml
# yaml-language-server: $schema=../jsonschema/config.schema.json
ai:
  provider: ollama   # or cursor, claude, rule-based
  activity_formatter: repo-chronological  # optional; or json-signal-list
  ollama:
    base_url: http://127.0.0.1:11434
    model: llama3

directives:
  - id: local-git
    name: Local repos
    collector: local-git
    enabled: true
    code_home: /Users/me/Code
  - id: github
    name: GitHub
    collector: github
    enabled: true
    # target.username is optional; omit to track the authenticated `gh` user (@me).
    credential_refs:
      token: DOCENT_GITHUB_TOKEN
```

## Modes

Modes are declarative: every run is described by an `ExecutionMode` value that bundles a **lookback window**, an optional **formatter** override, an **LLM prompt**, a **scope**, and an optional **collector allow-list**. Built-in modes ship with the binary; users can declare additional modes in `~/.config/docent/config.yaml` under `execution_modes:`.

| Mode | Lookback | Scope | Behavior |
|------|----------|-------|----------|
| `daily-plan` | Previous weekday 00:00 → now (Mon/weekends → last Fri) | `involved` | AI output should use `## Yesterday` and `## Today`. Pulls your own activity plus PRs/issues you reviewed, were assigned, or were mentioned in (see *Scope semantics* below). |
| `recent-activity` | `--days N` (default 7, or prompt) | prompt (default `involved`) | Summarize activity; grouped markdown. The scope picker lets you broaden to `all` or narrow to `self` per run. |
| `prs` (Pull request status) | n/a (lists currently-open PRs) | `self` | Lists your open GitHub PRs split into **Ready for review:** (not a draft, all checks passing) and **Work in progress:** (everything else). Each bullet links the Jira ticket key (parsed from the title) to the PR, followed by the title with the ticket stripped. **Only runs the `github` / `github-enterprise` collectors** (see *Restricting collectors* below). Rendered deterministically — the AI provider is not consulted. |
| `custom-prompt` | `--days N` (default 7, or prompt) | `involved` | `--prompt` / `--prompt-file` / interactive prompt; model follows your instructions over the same `involved` set. Override with `scope: all` on a user-declared mode if you want everything. |

Run without `--mode` on a TTY to pick interactively.

### Restricting collectors

A mode may declare a `collectors:` allow-list of collector types. When set, only directives whose `collector` matches an entry participate in that run; all other enabled directives are skipped. The built-in `prs` mode uses this to run GitHub-only:

```yaml
execution_modes:
  - id: github-only
    name: GitHub only
    lookback: { kind: days, days: 7 }
    prompt:
      instruction: "Summarize my GitHub activity."
    collectors: [github, github-enterprise]
```

Leaving `collectors:` unset (the default) collects from every enabled directive, as before.

### Declaring your own modes

Add `execution_modes:` to `~/.config/docent/config.yaml`. Any property you omit is asked at runtime (or filled from CLI flags) — including `scope`, which becomes an interactive picker (defaulting to `involved`) when left unset. Set the ones you want to lock in:

```yaml
execution_modes:
  - id: repo-activity
    name: Repo activity (everyone)
    lookback: { kind: days, days: 14 }
    prompt:
      instruction: "Summarize recent activity across all contributors on the configured repos."
    scope: all
```

A user-declared mode whose `id` matches a built-in (`daily-plan`, etc.) overrides the built-in for that run. Scope `all` only broadens the collection in collectors that have a `followed_*` directive config to anchor on (see *Scope semantics* below).

### Scope semantics

Each collector honors `scope` directly — there is no post-collection filter. `collector_error` rows always pass through so collection failures stay visible.

| Collector | `self` | `involved` (default) | `all` |
|-----------|--------|----------------------|-------|
| `local-git` | Commits whose author matches your `git config user.email` / `$USER`. Reflog rows always emitted. | Self commits **plus** commits on local branches (branches you've created or checked out). | Every commit on every ref in the window. |
| `github` / `github-enterprise` | `gh search prs --author <you>` and commits authored by you. | Self plus PRs reviewed by you, issues you're involved with, comments you left on either, PRs awaiting your review, any [`pr_queries`](#extra-open-pr-searches) declared on the directive, and the [review candidates](#prs-you-could-review). | `involved` plus per-repo `gh search prs/issues/commits --repo <r>` for each entry in `config.followed_repos`. |
| `gitea` | Repos you own; issues + PRs created by you. | Self plus issues/PRs assigned to you or mentioning you (deduped). | `involved` plus per-repo issue + PR listings for each entry in `config.followed_repos`. Bare-`owner` entries fan out across all repos under that owner. |
| `jira` | `(assignee = currentUser() OR reporter = currentUser()) AND updated >= …` | Adds `OR watcher = currentUser()`. Today's default JQL. | Wraps with `project in (…) OR …` using `config.followed_projects` (falls back to `involved` when no projects are configured). |
| `google-calendar` | All scopes return all events on the secret iCal feed (the feed is your personal calendar by definition). | Same as `self`. | Same as `self`. |

How each collector decides whether a row is **yours** (`is_self: true`):

- **`local-git`**: author email matches per-repo/global `user.email`, or `$USER` appears (case-insensitive) in the author name. Reflog rows are always yours.
- **`github` / `github-enterprise`**: user-anchored queries (`--author`, `--reviewed-by`, `--commenter`, `--involves`) yield `is_self=true`. Repo-scoped queries used in `scope: all` yield `is_self=false` unless the result author matches your username, and the [review candidates](#prs-you-could-review) are always `is_self=false`.
- **`gitea`**: user-anchored queries (created/assigned/mentioned) yield `is_self=true`. Repo-scoped queries used in `scope: all` set `is_self=true` only when the issue/PR author matches your login.
- **`jira`**: `self` / `involved` rows are `is_self=true` (the JQL guarantees it). `all` rows are `is_self=true` only when the issue's assignee or reporter email matches `config.email` (Basic auth); otherwise `is_self=false`.
- **`google-calendar`**: every event is `is_self=true` today.

### Following repos / projects

`followed_repos` does two jobs. It is what `scope: all` broadens on for the forge
collectors, and — for `github` / `github-enterprise` only — it is also the pool
the cockpit's [PRs to review](#prs-you-could-review) list is drawn from, which
needs no `scope: all`. Declare what you'd like to follow:

```yaml
directives:
  - id: github
    collector: github
    enabled: true
    config:
      followed_repos: "rust-lang/rust, golang/go"   # comma-, space-, or newline-separated; owner/repo only
  - id: gitea
    collector: gitea
    enabled: true
    config:
      base_url: https://gitea.example
      followed_repos: "some-org, some-org/some-repo" # bare owner fans out across all that owner's repos
    credential_refs:
      token: DOCENT_GITEA_TOKEN
  - id: jira
    collector: jira
    enabled: true
    config:
      base_url: https://jira.example
      followed_projects: "PROJ, OTHER"
    credential_refs:
      pat: DOCENT_JIRA_PAT
```

Without these fields, `scope: all` collects the same set as `scope: involved` (the collectors have nothing extra to broaden on), and the cockpit's review queue holds only PRs assigned to you.

### PRs you could review

Docent's other PR searches all answer "who asked for me": you authored it, a bot
authored it for you, someone requested your review. The review queue answers the
question before that one — what is there to pick up — so it searches for open
PRs nobody has pointed at you at all:

- every open, non-draft PR in each `followed_repos` entry (`owner/repo`; bare
  owners are skipped, since `gh search prs --repo` rejects them);
- every open PR assigned to you, draft or not, wherever it lives. A draft handed
  to you personally is still yours to look at.

Anything an earlier search already claimed stays what that search called it, so
your own PRs never turn up here. What is left is emitted as `pr_review_status`
rows with `relation: reviewable`, `reviewable: "true"`, and — this part matters —
`is_self: false`. They are somebody else's work, and `conditions.self` is what
keeps an automation like `autofix-pr` from pushing commits at a teammate's PR.

Each candidate is resolved as fully as one of your own, because the
[bucket](#how-an-open-pr-is-classified) is the whole reason a candidate is worth
listing: it says whether anyone is still waiting on a reviewer. That costs one
API round trip each, so the pool is capped at the 50 most recently updated and
the lookups run concurrently. A repo with hundreds of open PRs will show you the
newest 50 rather than spending a collection on the rest.

Like `pr_queries`, these searches run at `involved` and `all` only: `scope: self`
is your own work by definition, and other people's PRs are the opposite of that.

### Extra open-PR searches

The `github` / `github-enterprise` collectors look for open PRs you authored and
PRs awaiting your review. Neither finds work a bot opens **on your behalf** — a
backport bot cherry-picking your commit onto a release branch authors the PR
itself, so the PR is yours in every sense that matters but invisible to docent.

Declare extra searches with `pr_queries` to close that gap:

```yaml
directives:
  - id: github-enterprise
    collector: github-enterprise
    enabled: true
    config:
      base_url: https://git.example.com/
    pr_queries:
      - relation: backport
        qualifiers: author:app/ci-bot assignee:@me
```

`qualifiers` is [GitHub search syntax](https://docs.github.com/search-github/searching-on-github/searching-issues-and-pull-requests),
split on whitespace and passed to `gh search prs` (docent adds `--state open`).
Pick qualifiers precise enough that only your own work matches — `assignee:@me`
on its own usually means "a teammate asked me to review this", which is why the
example pairs it with the bot's login.

Matches are treated exactly like PRs you authored: docent resolves their checks,
review decision, and merge state, counts them in the dashboard's
action-required tally, and lists them in the standup. `relation` labels the rows
so automations can single them out via `match.fields`; it may be any lowercase
identifier other than the built-in `authored`, `review_requested`, and
`reviewable`.

Because a PR opened on your behalf is adjacent context rather than something you
wrote, these searches run at `involved` and `all` only. `scope: self` — which
the `prs` mode pins — stays limited to PRs you authored.

### How an open PR is classified

Every open PR docent resolves — yours, and the ones you could review — is put
into exactly one of six buckets, reported on the `pr_review_status` signal as
the `bucket` field (so automations can match it via `match.fields`) and shown as
a pill in the cockpit:

| Bucket | Meaning |
|--------|---------|
| `draft` | It is a draft. Wins over every other state — a draft is nobody's to review or merge. |
| `failing_validation` | The head commit's check rollup is failing. |
| `pending_validation` | Checks are still running. |
| `ready_to_merge` | Checks green (or absent) and approved. |
| `awaiting_author` | Checks green, not approved, and a **reviewer** acted last: the author's move. |
| `awaiting_review` | Checks green, not approved, and the **author** acted last: a reviewer's move. |

The last two are written from the PR's own point of view because they read from
both sides: `awaiting_author` is your move on a PR of yours and somebody else's
on a PR you could review, which is why the cockpit labels the same bucket
"awaiting you" in your lanes and "awaiting its author" in the review queue.

Two details are worth knowing because they are easy to get wrong:

- **A PR with no checks configured counts as green**, not as pending. There is
  nothing to wait for, so it can reach `ready_to_merge`.
- **Approval falls back to the raw review verdicts** when GitHub leaves
  `reviewDecision` empty, which it does on any repo without a review policy.
  Without the fallback nothing in such a repo would ever look mergeable.

The `awaiting_author` / `awaiting_review` split comes from walking the PR's
timeline **and** its reviews, merged by timestamp. Both are needed: GitHub omits
from the timeline any review that only replies inside an existing review thread,
so on a PR whose discussion has moved into its threads the timeline alone freezes
and an author who has answered everything still looks like the one being waited
on. Pushes always count as author-side; bot chatter and docent's own autofix
comments are skipped so an autofixed PR does not look like it is waiting on you.
The side is also reported on its own as `last_action` (`author` / `reviewer`),
with `last_action_at` as the timestamp — which is a better sort key than GitHub's
`updatedAt`, since that moves on label edits and other non-actions.

The rules are shared with [`pr-status-monitor`](https://git.drwholdings.com/kpreston/pr-status-monitor),
which publishes the team digest; docent has its own copy in `libs/prstatus` so
the two can be read and tested independently.

### Common flags

Paths follow the XDG base-directory layout by default:

- `--config-dir DIR` — config.yaml + .env (default `~/.config/docent`, i.e. `$XDG_CONFIG_HOME/docent`)
- `--config PATH`, `-c PATH` (default `<config-dir>/config.yaml`)
- `--state-dir DIR` — run logs under `<state-dir>/logs/` (default `~/.local/state/docent`, i.e. `$XDG_STATE_HOME/docent`)
- `--out-dir DIR` — generated markdown (default config `output_dir`, then `~/docent`)
- `--out PATH` — explicit output file (default `<out-dir>/<date>-<mode>.md`)
- `--userdata DIR` — legacy: put config.yaml/.env/logs/output all under one dir (overrides the three above)
- `--no-save` — stdout only
- `--date YYYY-MM-DD` — label for default output filename only
- `--mode ID` — execution mode (built-in or from `execution_modes:`); prompts interactively when omitted on a TTY
- `--days N` — overrides the mode's lookback for this run (always forces a days-based window)
- `--prompt TEXT` / `--prompt-file PATH` — overrides the mode's instruction for this run

## AI providers

- **`rule-based`**: Deterministic markdown (no network); uses the same `activity_formatter` shaping as cloud providers.
- **`ollama`**: HTTP chat to Ollama; streams to stderr when connected to a TTY.
- **`cursor`**: Shells out to `cursor-agent` (override with `ai.cursor.command` / `args`). Each call runs from a fresh temp directory in read-only `--mode=ask`, so the agent cannot edit files or run shell commands. `--sandbox=enabled` is intentionally not part of the defaults (it's host-dependent on Linux and `--mode=ask` already blocks the behaviors it would constrain); opt in via `ai.cursor.args` if you want it. Stderr is streamed to the terminal and any non-zero exit is surfaced with the captured stderr.
- **`claude`**: Shells out to the Claude Code CLI `claude` (override with `ai.claude.command` / `args`). Each call runs from a fresh temp directory in non-interactive `--print` mode with the file-mutating and shell tools disabled (`--disallowedTools=Bash,Edit,Write,MultiEdit,NotebookEdit`), so the agent cannot edit files or run shell commands; override the whole flag set via `ai.claude.args` if you need different behavior. Stderr is streamed to the terminal and any non-zero exit is surfaced with the captured stderr.

These same providers back the dashboard's [Report page](Dashboard.md#report-page) and the automations [`report` action](Automations.md#action-types) — configure `ai:` once and every surface uses it.

### Aborting slow collection

On an interactive terminal, docent-reporter prints `Press 'c' to abort pending collection…` while collectors run. Pressing **`c`** stops any in-flight and not-yet-started collector work and immediately proceeds to run the prompt against whatever was gathered so far (partial data is kept rather than discarded). This is handy when a broad-scope Slack run is taking longer than you want to wait. `Ctrl-C` still terminates the whole process as usual.

## Collectors

All collectors run in **date range** mode (`since` → `until`). Implemented:

- `local-git` — commits + reflog under `code_home` or explicit `paths`. Scope picks commits by author, by local-branch membership, or every commit on every ref. See [Nested repos](#nested-repos-configscan_depth) if your working trees sit a level deeper.
- `github` / `github-enterprise` — PRs authored / reviewed, issues you're involved with, comments, and commits for `target.username` (or the authenticated `gh` user when `target.username` is empty). Open-PR status also covers anything matched by [`pr_queries`](#extra-open-pr-searches) and the [PRs you could review](#prs-you-could-review). With `scope: all`, also pulls cross-repo activity from `config.followed_repos`.
- `gitea` — repos updated under `target.owner` plus issues + PRs you created, are assigned to, or are mentioned in (defaults to the authenticated user via `/api/v1/user` when `target.owner` is empty). With `scope: all`, also pulls activity from each entry in `config.followed_repos`.
- `jira` — issues you assign / report / watch by default (override actor coverage via `scope`, or scope to specific projects via `config.followed_projects` when `scope: all`).
- `google-calendar` — events from a secret iCal URL.
- `slack` — DMs, `@`-mentions, and your sent messages by default; thread replies + a 3-message context window per self-message at `involved`; explicit channels via `config.followed_channels` at `all`. Requires a User OAuth token (`xoxp-...`). See [Slack.md](Slack.md) for token setup and required scopes.

### Nested repos (`config.scan_depth`)

By default `local-git` looks exactly one level in: the immediate children of `code_home`, or the `paths` entries themselves. That misses layouts where a project directory is a container rather than a checkout — most commonly a **worktree project**, where `~/Code/salsa` holds a bare clone plus one worktree directory per branch, so the working trees live at `~/Code/salsa/<branch>`.

Set **`config.scan_depth`** to `"2"` to check one level further in whenever a candidate directory is not itself a git working tree:

```yaml
directives:
  - id: local-git
    name: Local repos
    collector: local-git
    enabled: true
    code_home: ~/Code
    config:
      scan_depth: "2"   # 1 (default) through 3
```

A directory that **is** a working tree ends the descent, so this never walks into a repo's own source tree or picks up vendored clones, and dot-directories (including the bare clone, conventionally `.base`) are skipped. The same depth applies to `paths`, so you can list `~/Code/salsa` directly and get its worktrees.

Scanning deeper multiplies the number of directories visited — a worktree project can hold a dozen worktrees. Commit history is only walked once per repository even so (sibling worktrees share one object store), but each worktree still gets its own reflog read, which is why the option is opt-in.
