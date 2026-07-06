# docent

Monorepo suite containing local-first tooling for developer activity: **docent** (live dashboard + session focus) and **docent-reporter** (collectors → AI → Markdown reports). The local window manager it drives now lives in the separate [wsm](https://github.com/KurtPreston/wsm) project.

## Monorepo layout

```
libs/           shared Go packages (model, collectors, correlation, ai, config, …)
apps/
  docentd/              merged daemon (collectors, dashboard, /ingest)
  docent-launcher-*/    hotkey + webview launchers
  docent-reporter/      stateless CLI reporter (was `slakkr`)
  docent-setup/         config wizard + `check` validator
```

## Quick start (reporter)

```sh
go test ./...
go run ./apps/docent-reporter --help
# First run creates ~/.config/docent/config.yaml if missing
go run ./apps/docent-reporter --mode recent-activity --days 3
```

Or use [`scripts/docent-reporter`](scripts/docent-reporter) from the repo root.

## Setup

Bootstrap or refresh docent config (`~/.config/docent/config.yaml` by default) and reconcile secret placeholders in `.env`:

```sh
./scripts/setup
# or: go run ./apps/docent-setup --config-dir ~/.config/docent
```

Validate configured collectors without running a report:

```sh
./scripts/check
# or: go run ./apps/docent-setup check
```

The wizard picks an AI provider (cursor / claude / ollama / offline `rule-based`), an activity formatter, walks collectors, and writes env-var **names** into `credential_refs` (never secret values). Missing variables are appended to `~/.config/docent/.env` as `KEY=` lines; stderr lists keys you still need to fill.

Config shape is validated at runtime against [`jsonschema/config.schema.json`](jsonschema/config.schema.json). The same file is embedded at [`libs/config/configschema/config.schema.json`](libs/config/configschema/config.schema.json); keep them identical (tests enforce this). After setup, the written config includes a header such as `# yaml-language-server: $schema=../jsonschema/config.schema.json` so editors can offer completions against the schema.

## Configuration (`~/.config/docent/config.yaml`)

Single file: `ai`, `directives`, optional `execution_modes`, and optional `output_dir`.

- **`directives`**: Collector, target, config, `credential_refs` for secrets in `~/.config/docent/.env`.
- **`local-git`**: Use **`paths`** for explicit repo roots, or **`code_home`** to scan that directory’s immediate children that contain `.git`.
- **`output_dir`** (optional): where `docent-reporter` writes generated markdown (supports a leading `~`). Defaults to `~/docent`; override per-run with `--out-dir`.

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

Without these fields, `scope: all` collects the same set as `scope: involved` (the collectors have nothing extra to broaden on).

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

### Aborting slow collection

On an interactive terminal, docent-reporter prints `Press 'c' to abort pending collection…` while collectors run. Pressing **`c`** stops any in-flight and not-yet-started collector work and immediately proceeds to run the prompt against whatever was gathered so far (partial data is kept rather than discarded). This is handy when a broad-scope Slack run is taking longer than you want to wait. `Ctrl-C` still terminates the whole process as usual.

## Collectors

All collectors run in **date range** mode (`since` → `until`). Implemented:

- `local-git` — commits + reflog under `code_home` or explicit `paths`. Scope picks commits by author, by local-branch membership, or every commit on every ref.
- `github` / `github-enterprise` — PRs authored / reviewed, issues you're involved with, comments, and commits for `target.username` (or the authenticated `gh` user when `target.username` is empty). With `scope: all`, also pulls cross-repo activity from `config.followed_repos`.
- `gitea` — repos updated under `target.owner` plus issues + PRs you created, are assigned to, or are mentioned in (defaults to the authenticated user via `/api/v1/user` when `target.owner` is empty). With `scope: all`, also pulls activity from each entry in `config.followed_repos`.
- `jira` — issues you assign / report / watch by default (override actor coverage via `scope`, or scope to specific projects via `config.followed_projects` when `scope: all`).
- `google-calendar` — events from a secret iCal URL.
- `slack` — DMs, `@`-mentions, and your sent messages by default; thread replies + a 3-message context window per self-message at `involved`; explicit channels via `config.followed_channels` at `all`. Requires a User OAuth token (`xoxp-...`). See [docs/Slack.md](docs/Slack.md) for token setup and required scopes.

## docentd dashboard (binding + auth)

`docentd` serves the live dashboard and its data APIs (`/sessions`, `/api/*`).
By default it binds **`127.0.0.1` only and serves openly** — fine for localhost
or when reached over an SSH tunnel / Cursor Remote-SSH port forward.

To reach it directly from another machine, set a **shared secret**. Setting
`token:` in `docentd.yaml` (or the `DOCENT_TOKEN` env var, which wins) flips two
things at once:

- docentd binds **all interfaces (`0.0.0.0`)** by default, so it is reachable
  off the loopback (override with `bindHost:` — e.g. `127.0.0.1` to force
  loopback even with a token, or `-host` on the command line).
- Every **data** endpoint now requires `Authorization: Bearer <token>`.
  `/health` and the dashboard shell (the built SPA assets) stay open; only the
  data behind them is protected. Comparison is constant-time.

Clients:

- **Browser dashboard** — open `http://<host>:39787/?token=<secret>` once per
  browser. The page caches the token in `sessionStorage` (stripping it from the
  URL) and sends it on every data fetch; subsequent visits in that tab need no
  query string.
- **docent launcher (Windows)** — pass `-Token <secret>` (the installer wires
  this through); it sends the bearer header automatically.

Caveats:

- Binding externally exposes your activity data to anyone who can reach the
  port. A token is a reasonable bar for a personal dev box; open the host
  firewall for the port deliberately. There is **no TLS** — front it with a
  reverse proxy if the host is broadly reachable.
- With no token configured, behavior is unchanged (loopback-only, open). If you
  set `bindHost` to a non-loopback address **without** a token, docentd logs a
  loud startup warning that data is exposed unauthenticated.

### Dashboard frontend (build)

The dashboard is a **Vite + React + TypeScript** single-page app under
[`apps/docentd/web`](apps/docentd/web). It's a pure client of docentd's JSON API
(`/sessions`, `/api/*`) and is embedded into the `docentd` binary at build time,
so a released binary is self-contained. Requires **Node >= 18**.

- **Dev** (hot reload) — run a `docentd` (default `127.0.0.1:39787`), then:

  ```bash
  cd apps/docentd/web
  npm install
  npm run dev     # http://localhost:5173; proxies /api,/sessions,/ingest,/health to docentd
  ```

  Point the proxy at a non-default docentd with `DOCENTD_URL=http://host:port npm run dev`.

- **Release** (embedded) — build the SPA, then compile docentd with the `embed`
  tag so `dist/` is baked in (this is what the installers do):

  ```bash
  ( cd apps/docentd/web && npm ci && npm run build )   # -> apps/docentd/web/dist
  go build -tags embed ./apps/docentd
  ```

- **Bare `go build` / `go vet` / `go test` stay Node-free** — without `-tags
  embed` no assets are baked in and docentd serves the dashboard from disk via
  `-web` (default `apps/docentd/web/dist`, so it works after an `npm run build`).

### Report page (`/report`)

The dashboard's **Report** tab runs the same pipeline as the `docent-reporter`
CLI (both share [`libs/report`](libs/report)): pick a mode, an optional lookback
(days), a scope (`self` / `involved` / `all`, or the mode default), and an
optional prompt override, then generate a Markdown report, view it in-browser,
and download it as `.md`. Generation can take a while (LLM providers), so
docentd runs it as a background job the page polls; jobs are in-memory and
ephemeral (bounded, TTL-pruned, lost on restart — a report is cheap to re-run).

Its endpoints are auth-gated like every other data endpoint:

- **`POST /api/report`** — body `{ "mode": "<id>", "days"?: N, "scope"?: "self|involved|all", "prompt"?: "…" }`.
  Starts a background generation and returns `202` with `{ "id": "<job>" }`.
  A blank/omitted `days`/`scope` uses the mode default; a mode with no built-in
  prompt (e.g. `custom-prompt`) requires `prompt`.
- **`GET /api/report/{id}`** — poll a job: `{ "status": "pending|running|done|error", "markdown"?, "meta"?, "error"? }`.
- **`GET /api/report/meta`** — form metadata: available `modes`
  (`{id, name, promptRequired}`), the `scopes` list, and the configured AI
  `provider` (label + provider/model) used for the topbar.

## Window management & session dashboard

Beyond the reporter, `docentd` doubles as a **mission-control dashboard**: a live,
color-coded, grouped-by-ticket view of your Cursor sessions, JIRA tickets, and
GitHub PRs, with focus-or-open window control. The window control itself lives in
a small **local** REST service — the separate [wsm](https://github.com/KurtPreston/wsm)
project (`wsmd`) — so `docentd` can run remotely (on your dev box) while the windows
are managed on your workstation.

```
 dev box (grove / docentd)                     workstation (wsm + launcher)
 ─────────────────────────                     ────────────────────────────
 POST /open {host,path,name} ──► reverse SSH tunnel ──► 127.0.0.1:39788  wsmd
                                                              │
                                                              ▼
                                          open-or-focus a remote Cursor window
                                          (Windows: on a named virtual desktop;
                                           macOS: window raised, no Spaces)
```

### Window manager (wsm)

The window manager is now the standalone [wsm](https://github.com/KurtPreston/wsm)
daemon (`wsmd`): a localhost-only REST service that owns the Cursor windows on the
machine you sit at (default port **39788**). docent is a *client* of it via the
`wsm` collector (see below) and the dashboard's focus button. The contract
`GET /health`, `GET /windows`, `POST /open`, `POST /focus` is published as an
OpenAPI spec in the wsm repo. Install and run it from there; there is no window
manager binary in this repo anymore.

### Cursor hooks → docentd

`hooks/docent-notify.sh` + `hooks/hooks.snippet.json` report session activity to
`docentd`. Copy the script to `~/.cursor/hooks/` and merge the snippet into
`~/.cursor/hooks.json`; the hook POSTs to `docentd`'s `/ingest` (fire-and-forget, so
a down `docentd` never blocks Cursor). Point it with `DOCENT_URL` (remote base URL)
or `DOCENT_PORT` (default 39787 local); it loads `~/.config/docent/.env` and sends
`DOCENT_TOKEN` when set. See [`hooks/README.md`](hooks/README.md).

### grove → wsm

The [`grove`](https://github.com/KurtPreston/grove) sender POSTs the
`{host, path, name}` webhook to the **local** wsm `/open`, tunneled from the
dev box to the workstation over reverse SSH. wsm needs no SSH of its own —
the remote path arrives in the payload.

### Reaching a remote docentd (docent-tunnel)

When `docentd` runs on the dev box bound to `127.0.0.1`, the workstation
launcher/dashboard reach it through **`docent-tunnel`** (`apps/docent-tunnel`):
a small helper that holds an SSH **local**-forward from `127.0.0.1:39787` on the
workstation to the dev box's `docentd` loopback port. Because it runs as its own
background service (Scheduled Task / launchd `KeepAlive`), the forward is live
whenever you are logged in — it does **not** depend on a Cursor Remote-SSH
session being connected.

```
 workstation                                   dev box
 ─────────────────────────                     ────────────────────────────
 launcher / dashboard ──► 127.0.0.1:39787 ──┐
                                             │  docent-tunnel (local forward)
                                             └─► 127.0.0.1:39787  docentd
```

The per-OS installers set this up **by default in remote mode**, pointing the
launcher at the local end of the forward. Pass `--no-tunnel` / `-NoTunnel` to opt
out (and hit the remote URL directly), or `--ssh-host` / `-SshHost` to override
the SSH host (it otherwise defaults to the host in the remote URL). This mirrors
the reverse tunnel wsm owns for its own port, in the opposite direction — the two
projects share the pattern but not code.

### Launchers

Spotlight-style pickers bound to a global hotkey; type to fuzzy-filter your
sessions / tickets / PRs, **Enter** focuses the session (via wsm `/focus`)
or opens the URL, **Esc** dismisses. Session rows come from `docentd`'s `/sessions`,
which may point at a **remote** `docentd`.

- **Windows** — `apps/docent-launcher-windows/docent-launcher.ps1`, a WPF window with
  a Win32 `RegisterHotKey` (default **Ctrl+Alt+Space**). See its
  [README](apps/docent-launcher-windows/README.md).
- **macOS** — `apps/docent-launcher-macos/docent.lua`, a Hammerspoon chooser
  (default **Cmd+Alt+Space**). Copy to `~/.hammerspoon/` and `require("docent")`.

### docentd config (`~/.config/docent/docentd.yaml`)

The dashboard/daemon reads `docentd.yaml` (separate from the reporter's
`config.yaml`): `port` (default 39787), `refreshSec`, `wsmUrl`
(default `http://127.0.0.1:39788`, the local wsm daemon), and optional `token`/`bindHost` (see
[docentd dashboard (binding + auth)](#docentd-dashboard-binding--auth) above). See
[`config/docent/docentd.yaml.example`](config/docent/docentd.yaml.example).

`ticketProjects` (optional list, e.g. `[SALSA, JASPER]`) restricts ticket-key
matching — branch names, PR/commit titles, JIRA issue keys — to those project
keys, so generic hyphenated tokens like `PR-7373` or `release-2026` can't
false-match as tickets. The engine also auto-widens matching with any project
key observed on collected jira issues (and each jira directive's
`config.followed_projects`), so `ticketProjects` mainly matters when no jira
directive is configured at all. `ticketPattern` fully overrides matching with
a custom regex (first capture group is the key) instead.

## Installation

Per-OS installers build the relevant binaries, write config into
`~/.config/docent/`, and register background services. Re-running is idempotent.

The window manager itself is installed separately from the
[wsm](https://github.com/KurtPreston/wsm) repo (its own macOS/Windows installers).
The docent installers below set up `docentd`, the launcher, and Cursor hooks.

- **Linux** — [`scripts/install-docent-linux.sh`](scripts/install-docent-linux.sh):
  installs `docentd` only (the dashboard/collector daemon) as a `systemd --user`
  service. There is no window manager on Linux — install wsm on the Windows/macOS
  host that connects here.
- **macOS** — [`scripts/install-docent-macos.sh`](scripts/install-docent-macos.sh):
  installs `docentd` (optionally, locally via `launchd`), the Hammerspoon launcher
  by default, and Cursor hooks when Cursor.app is installed (`--no-hooks` /
  `--no-hammerspoon` to skip). In remote mode it also installs `docent-tunnel` as a
  `launchd` `KeepAlive` agent by default (`--no-tunnel` to hit the remote URL
  directly; `--ssh-host` to override the SSH host). Install the window manager from
  wsm separately.
- **Windows** — [`scripts/install-docent-windows.ps1`](scripts/install-docent-windows.ps1):
  installs `docent-launcher-windows` as a hidden, auto-restarting Scheduled Task
  (at-logon + a 1-minute watchdog), and optionally `docentd` locally. Prompts
  whether `docentd` runs locally or on a remote host; in remote mode it also
  installs `docent-tunnel` as its own watchdog task by default (`-NoTunnel` to opt
  out; `-SshHost` to override the SSH host). Install the window manager from wsm
  separately.

## Layout

- `libs/` — shared packages (`model`, `collectors`, `correlation`, `ai`, `config`, `wmclient`, `webhook`, …)
- `apps/docent-reporter/` — reporter CLI
- `apps/docent-setup/` — config wizard + `check`
- `apps/docentd/` — daemon + dashboard (Vite/React SPA in `apps/docentd/web`, embedded via `-tags embed`)
- `apps/docent-launcher-macos/`, `apps/docent-launcher-windows/` — hotkey launchers
- `apps/docent-tunnel/` — workstation SSH local-forward helper for a remote, loopback-only docentd
- the local window manager lives in the separate [wsm](https://github.com/KurtPreston/wsm) repo
- `hooks/` — Cursor hook (`docent-notify.sh`) + snippet that report sessions to `docentd`
- `scripts/install-docent-{linux,macos,windows}.*` — per-OS installers
- `~/.config/docent/` — `config.yaml`, `docentd.yaml`, `.env` (`$XDG_CONFIG_HOME/docent`)
- `~/.local/state/docent/logs/<run>/` — reporter run logs (`$XDG_STATE_HOME/docent`)
- `~/docent/` — saved markdown from the reporter (override via `output_dir` in config.yaml or `--out-dir`)
- `--userdata DIR` keeps the legacy all-in-one layout (config + .env + logs + output under one dir)
