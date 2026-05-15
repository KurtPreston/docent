# slakkr-ai

Local-first CLI: collect **time-bounded activity** from configured sources, send it to an AI provider, and get a **Markdown** document (printed to stdout and saved under `userdata/output/`).

## Quick start

```sh
go test ./...
go run ./cmd/slakkr --help
# First run creates userdata/config.yaml if missing
go run ./cmd/slakkr --mode recent-activity --days 3
```

Or use [`scripts/slakkr`](scripts/slakkr) from the repo root.

## Setup

Bootstrap or refresh `userdata/config.yaml` and reconcile secret placeholders in `userdata/.env`:

```sh
./scripts/setup
# or: go run ./cmd/slakkr-setup --userdata userdata
```

The wizard picks an AI provider (cursor / ollama / offline `rule-based`), an activity formatter, walks collectors, and writes env-var **names** into `credential_refs` (never secret values). Missing variables are appended to `userdata/.env` as `KEY=` lines; stderr lists keys you still need to fill.

Config shape is validated at runtime against [`jsonschema/config.schema.json`](jsonschema/config.schema.json). The same file is embedded at [`internal/configschema/config.schema.json`](internal/configschema/config.schema.json); keep them identical (tests enforce this). After setup, the written config includes a header such as `# yaml-language-server: $schema=../jsonschema/config.schema.json` so editors can offer completions against the schema.

## Configuration (`userdata/config.yaml`)

Single file: `ai` and `directives`.

- **`directives`**: Collector, target, config, `credential_refs` for secrets in `userdata/.env`.
- **`local-git`**: Use **`paths`** for explicit repo roots, or **`code_home`** to scan that directory’s immediate children that contain `.git`.

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
  provider: ollama   # or cursor, rule-based
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
      token: SLAKKR_GITHUB_TOKEN
```

## Modes

Modes are declarative: every run is described by an `ExecutionMode` value that bundles a **lookback window**, an optional **formatter** override, an **LLM prompt**, and a **scope**. Built-in modes ship with the binary; users can declare additional modes in `userdata/config.yaml` under `execution_modes:`.

| Mode | Lookback | Scope | Behavior |
|------|----------|-------|----------|
| `daily-plan` | Previous weekday 00:00 → now (Mon/weekends → last Fri) | `involved` | AI output should use `## Yesterday` and `## Today`. Pulls your own activity plus PRs/issues you reviewed, were assigned, or were mentioned in (see *Scope semantics* below). |
| `recent-activity` | `--days N` (default 7, or prompt) | prompt (default `involved`) | Summarize activity; grouped markdown. The scope picker lets you broaden to `all` or narrow to `self` per run. |
| `custom-prompt` | `--days N` | `involved` | `--prompt` / `--prompt-file` / interactive prompt; model follows your instructions over the same `involved` set. Override with `scope: all` on a user-declared mode if you want everything. |

Run without `--mode` on a TTY to pick interactively.

### Declaring your own modes

Add `execution_modes:` to `userdata/config.yaml`. Any property you omit is asked at runtime (or filled from CLI flags) — including `scope`, which becomes an interactive picker (defaulting to `involved`) when left unset. Set the ones you want to lock in:

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
| `github` / `github-enterprise` | `gh search prs --author <you>` and commits authored by you. | Self plus PRs reviewed by you, issues you're involved with, and comments you left on either. | `involved` plus per-repo `gh search prs/issues/commits --repo <r>` for each entry in `config.followed_repos`. |
| `gitea` | Repos you own; issues + PRs created by you. | Self plus issues/PRs assigned to you or mentioning you (deduped). | `involved` plus per-repo issue + PR listings for each entry in `config.followed_repos`. Bare-`owner` entries fan out across all repos under that owner. |
| `jira` | `(assignee = currentUser() OR reporter = currentUser()) AND updated >= …` | Adds `OR watcher = currentUser()`. Today's default JQL. | Wraps with `project in (…) OR …` using `config.followed_projects` (falls back to `involved` when no projects are configured). |
| `google-calendar` | All scopes return all events on the secret iCal feed (the feed is your personal calendar by definition). | Same as `self`. | Same as `self`. |

How each collector decides whether a row is **yours** (`is_self: true`):

- **`local-git`**: author email matches per-repo/global `user.email`, or `$USER` appears (case-insensitive) in the author name. Reflog rows are always yours.
- **`github` / `github-enterprise`**: user-anchored queries (`--author`, `--reviewed-by`, `--commenter`, `--involves`) yield `is_self=true`. Repo-scoped queries used in `scope: all` yield `is_self=false` unless the result author matches your username.
- **`gitea`**: user-anchored queries (created/assigned/mentioned) yield `is_self=true`. Repo-scoped queries used in `scope: all` set `is_self=true` only when the issue/PR author matches your login.
- **`jira`**: `self` / `involved` rows are `is_self=true` (the JQL guarantees it). `all` rows are `is_self=true` only when the issue's assignee or reporter email matches `config.email` (Basic auth); otherwise `is_self=false`.
- **`google-calendar`**: every event is `is_self=true` today.

### Following repos / projects in scope: all

To make `scope: all` collect more than `involved` for the forge and ticket-tracker collectors, declare what you'd like to follow:

```yaml
directives:
  - id: github
    collector: github
    enabled: true
    config:
      followed_repos: "rust-lang/rust, golang/go"   # comma-, space-, or newline-separated
  - id: gitea
    collector: gitea
    enabled: true
    config:
      base_url: https://gitea.example
      followed_repos: "some-org, some-org/some-repo" # bare owner fans out across all that owner's repos
    credential_refs:
      token: SLAKKR_GITEA_TOKEN
  - id: jira
    collector: jira
    enabled: true
    config:
      base_url: https://jira.example
      followed_projects: "PROJ, OTHER"
    credential_refs:
      pat: SLAKKR_JIRA_PAT
```

Without these fields, `scope: all` collects the same set as `scope: involved` (the collectors have nothing extra to broaden on).

### Common flags

- `--userdata DIR` (default `userdata`)
- `--config PATH`, `-c PATH` (default `userdata/config.yaml`)
- `--out PATH` — default `userdata/output/<date>-<mode>.md`
- `--no-save` — stdout only
- `--date YYYY-MM-DD` — label for default output filename only
- `--mode ID` — execution mode (built-in or from `execution_modes:`); prompts interactively when omitted on a TTY
- `--days N` — overrides the mode's lookback for this run (always forces a days-based window)
- `--prompt TEXT` / `--prompt-file PATH` — overrides the mode's instruction for this run

## AI providers

- **`rule-based`**: Deterministic markdown (no network); uses the same `activity_formatter` shaping as cloud providers.
- **`ollama`**: HTTP chat to Ollama; streams to stderr when connected to a TTY.
- **`cursor`**: Shells out to `cursor-agent` (override with `ai.cursor.command` / `args`). Each call runs from a fresh temp directory in read-only `--mode=ask`, so the agent cannot edit files or run shell commands. `--sandbox=enabled` is intentionally not part of the defaults (it's host-dependent on Linux and `--mode=ask` already blocks the behaviors it would constrain); opt in via `ai.cursor.args` if you want it. Stderr is streamed to the terminal and any non-zero exit is surfaced with the captured stderr.

## Collectors

All collectors run in **date range** mode (`since` → `until`). Implemented:

- `local-git` — commits + reflog under `code_home` or explicit `paths`. Scope picks commits by author, by local-branch membership, or every commit on every ref.
- `github` / `github-enterprise` — PRs authored / reviewed, issues you're involved with, comments, and commits for `target.username` (or the authenticated `gh` user when `target.username` is empty). With `scope: all`, also pulls cross-repo activity from `config.followed_repos`.
- `gitea` — repos updated under `target.owner` plus issues + PRs you created, are assigned to, or are mentioned in (defaults to the authenticated user via `/api/v1/user` when `target.owner` is empty). With `scope: all`, also pulls activity from each entry in `config.followed_repos`.
- `jira` — issues you assign / report / watch by default (override actor coverage via `scope`, or scope to specific projects via `config.followed_projects` when `scope: all`).
- `google-calendar` — events from a secret iCal URL.

## Layout

- `userdata/config.yaml` — only required config file.
- `userdata/.env` — secret values referenced by `credential_refs` (optional).
- `userdata/output/` — saved markdown.
- `userdata/.cache/ai-debug/` — optional Ollama request/response logs.
- `jsonschema/config.schema.json` — JSON Schema for `userdata/config.yaml` (canonical copy alongside embedded duplicate).

The `userdata/` directory is gitignored; initialize with [`scripts/setup`](scripts/setup), run the CLI once (minimal default config), or copy an example by hand.
