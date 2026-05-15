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

Modes are declarative: every run is described by an `ExecutionMode` value that bundles a **lookback window**, an optional **formatter** override, an **LLM prompt**, and a placeholder **scope**. Built-in modes ship with the binary; users can declare additional modes in `userdata/config.yaml` under `execution_modes:`.

| Mode | Lookback | Scope | Behavior |
|------|----------|-------|----------|
| `daily-plan` | Previous weekday 00:00 → now (Mon/weekends → last Fri) | `self` | AI output should use `## Yesterday` and `## Today`. **Scoped to your own contributions** (see *Self-only scoping* below). |
| `recent-activity` | `--days N` (default 7, or prompt) | `self` | Summarize activity; grouped markdown. **Scoped to your own contributions.** |
| `custom-prompt` | `--days N` | `all` | `--prompt` / `--prompt-file` / interactive prompt; model follows your instructions. Receives the unfiltered status list. |

Run without `--mode` on a TTY to pick interactively.

### Declaring your own modes

Add `execution_modes:` to `userdata/config.yaml`. Any property you omit is asked at runtime (or filled from CLI flags); set the ones you want to lock in:

```yaml
execution_modes:
  - id: repo-activity
    name: Repo activity (everyone)
    lookback: { kind: days, days: 14 }
    prompt:
      instruction: "Summarize recent activity across all contributors on the configured repos."
    scope: repo
```

A user-declared mode whose `id` matches a built-in (`daily-plan`, etc.) overrides the built-in for that run. The `scope` field is a placeholder for an upcoming collector-side effort: today only `self` has observable behavior (it triggers the same `FilterToSelf` step that drives the built-in self-scoped modes); `repo` and `all` skip the filter but otherwise collect the same data as `self` until per-collector scope support lands.

### Self-only scoping

`daily-plan` and `recent-activity` filter the collected status list down to entries each collector flagged as **your own** activity (`is_self: true`). `custom-prompt` skips the filter so you can ask cross-cutting questions. `collector_error` rows always pass through so collection failures stay visible.

How each collector decides "self":

- **`local-git`**: a commit is yours when its author email equals the per-repo `git config user.email`, the global `git config --global user.email`, **or** when `$USER` (e.g. `kpreston`) appears anywhere in the author name (case-insensitive). Reflog rows are always yours (they record your local checkout actions).
- **`github` / `github-enterprise`**: every result is yours by construction (search queries are scoped to `--author`, `--reviewed-by`, `--commenter`, `--involves` for the configured user / `@me`).
- **`gitea`**: repos returned for the resolved owner (defaults to the authenticated user via `/api/v1/user`).
- **`jira`**: default JQL scopes to issues you assign / report / watch.
- **`google-calendar`**: events from your secret iCal feed.

A future `repo-activity` mode will surface everyone's contributions on the configured repos (separate from your personal feed).

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

- `local-git` — commits + reflog under `code_home` or explicit `paths`.
- `github` / `github-enterprise` — PRs authored / reviewed, issues you're involved with, comments, and commits for `target.username` (or the authenticated `gh` user when `target.username` is empty).
- `gitea` — repos updated under `target.owner`; defaults to the authenticated user via `/api/v1/user` when `target.owner` is empty.
- `jira` — issues you assign / report / watch (override with `config.query` for project- or label-scoped JQL).
- `google-calendar` — events from a secret iCal URL.

## Layout

- `userdata/config.yaml` — only required config file.
- `userdata/.env` — secret values referenced by `credential_refs` (optional).
- `userdata/output/` — saved markdown.
- `userdata/.cache/ai-debug/` — optional Ollama request/response logs.
- `jsonschema/config.schema.json` — JSON Schema for `userdata/config.yaml` (canonical copy alongside embedded duplicate).

The `userdata/` directory is gitignored; initialize with [`scripts/setup`](scripts/setup), run the CLI once (minimal default config), or copy an example by hand.
