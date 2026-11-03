# docent

Monorepo suite containing local-first tooling for developer activity: **docentd** (live dashboard + session focus + automations) and **docent-reporter** (collectors → AI → Markdown reports). The local window manager it drives now lives in the separate [wsm](https://github.com/KurtPreston/wsm) project.

## Monorepo layout

```
libs/           shared Go packages (model, collectors, correlation, ai,
                automation, goals, report, workitem, sessionmanager, config, …)
apps/
  docentd/              merged daemon (collectors, dashboard, automations, agent sessions, session ingest)
  docent-launcher-*/    hotkey + webview launchers
  docent-reporter/      stateless CLI reporter (was `slakkr`)
  docent-setup/         config wizard + `check` validator
```

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

The wizard picks an AI provider (cursor / claude / copilot / ollama / offline `rule-based`), an activity formatter, walks collectors, and writes env-var **names** into `credential_refs` (never secret values). Missing variables are appended to `~/.config/docent/.env` as `KEY=` lines; stderr lists keys you still need to fill.

Config shape is validated at runtime against [`jsonschema/config.schema.json`](jsonschema/config.schema.json). The same file is embedded at [`libs/config/configschema/config.schema.json`](libs/config/configschema/config.schema.json); keep them identical (tests enforce this). After setup, the written config includes a header such as `# yaml-language-server: $schema=../jsonschema/config.schema.json` so editors can offer completions against the schema.

## The reporter

`docent-reporter` is the stateless CLI half of docent: collectors gather your
recent activity, an AI provider turns it into prose (or the deterministic
`rule-based` formatter, for offline/no-network use), and the result is saved
as Markdown.

```sh
go run ./apps/docent-reporter --mode recent-activity --days 3
```

Built-in modes:

- **`daily-plan`** — a yesterday/today plan built from your `involved` activity since the last workday.
- **`recent-activity`** — a `--days N` digest (default 7); the default pick in the interactive menu.
- **`prs`** — your open GitHub PRs, split into ready-for-review vs. work-in-progress; deterministic, no AI call.
- **`custom-prompt`** — your own instruction run over the same activity window.

Every mode reads the same `~/.config/docent/config.yaml` (`ai`, `directives`,
optional `execution_modes`) and honors a **scope** (`self` / `involved` /
`all`) that controls how much of *other people's* activity gets pulled in.
The same collectors, `config.yaml`, and AI providers also back the
[automations](#automations) engine and the dashboard's Report tab.

See **[docs/Reporting.md](docs/Reporting.md)** for the full reference: the
`config.yaml` schema, the activity formatter, declaring custom modes,
per-collector scope semantics, AI provider details, common CLI flags, and the
built-in collector list (including [Slack setup](docs/Slack.md)).

## The dashboard (docentd)

`docentd` is the other half of docent: a long-running daemon that continuously
collects, correlates, and serves a live dashboard (Dashboard, Signals,
Collectors, Report, Automations, Settings tabs), binds a lightweight HTTP API,
and drives window focus through the separate [wsm](https://github.com/KurtPreston/wsm)
project.

By default it binds **`127.0.0.1` only and serves openly** — fine for
localhost or over an SSH tunnel. Setting a shared secret (`token:` in
`docentd.yaml`, or `DOCENT_TOKEN`) lets it bind every interface and requires
`Authorization: Bearer <token>` on every data endpoint.

See **[docs/Dashboard.md](docs/Dashboard.md)** for the full reference: a tour
of every page, the binding/auth model, the complete HTTP API, the frontend
build (dev/embedded), the Report tab, the session-manager providers (`cursor`
/ `wsm` / none), reaching a remote docentd (`docent-tunnel`), and the
`docentd.yaml` config fields (including the work-item launch hook).

## Automations

`docentd` also runs IFTTT-style rules declared under `automations:` in
`config.yaml`: a **trigger** (a new signal, a state transition, or a
schedule) fires one or more **actions** — post a webhook, run a shell
command, comment on a JIRA ticket, post to Slack, generate-and-deliver a
report, run a write-capable coding agent, or open an editor.

```yaml
automations:
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

Rules are edited as YAML (Settings, or by hand) and take effect on the next
`docentd` restart; the dashboard's Automations tab lists rules and job
history and can fire one manually. An `agent` action starts a hosted agent
session inside `docentd`, streamed live into the cockpit — see
**[docs/Automations.md](docs/Automations.md)** for the full reference:
every trigger/condition/action type, delivery destinations, the dashboard
API, agent sessions, and a list of gotchas.

## Goals

`~/.config/docent/goals.yaml` is a short list of long-lived objectives you
want your activity checked against: an `id`, a `title`, an optional
`description`, and an `active` flag (defaults to true). Edit it in the
dashboard's Settings tab (validated against its own JSON Schema, alongside
`config.yaml` and `docentd.yaml`) or by hand.

```yaml
goals:
  - id: ship-feature-x
    title: Ship feature X
    description: Get the migration merged and rolled out.
```

The special **`goal-alignment`** mode loads your active goals and asks the AI
provider to review recent activity against them — use it as an automation's
`report` action (`mode: goal-alignment`, see [Automations](#automations)) or
add it to `execution_modes:` to run it from the reporter/dashboard too.
(`repos`, `labels`, and `ticket_keys` are accepted and validated but not yet
used beyond storage.)

### IDE extension → docentd

The [docent IDE extension](apps/docent-ide-extension/README.md) is a small
VS Code / Cursor extension that reports editor **window lifecycle** and
**heartbeats** to `docentd`'s `POST /api/sessions/events`, so the dashboard
knows which windows are open without polling. Build it with
`scripts/build-extension.sh`; the platform installers offer to install it (and
write `docent.url` / `docent.token`) when they detect Cursor or VS Code.

### Cursor hooks → docentd

`hooks/docent-notify.sh` + `hooks/hooks.snippet.json` report **agent activity**
to `docentd` (the window lifecycle is owned by the extension above). Copy the
script to `~/.cursor/hooks/` and merge the snippet into `~/.cursor/hooks.json`;
the hook POSTs to `docentd`'s `/api/sessions/events` (fire-and-forget, so a down
`docentd` never blocks Cursor). Point it with `DOCENT_URL` (remote base URL) or
`DOCENT_PORT` (default 39787 local); it loads `~/.config/docent/.env` and sends
`DOCENT_TOKEN` when set. See [`hooks/README.md`](hooks/README.md).

### Launchers

Spotlight-style pickers bound to a global hotkey; type to fuzzy-filter your
sessions / tickets / PRs, **Enter** focuses the session or opens the URL,
**Esc** dismisses. Both launchers share the same data flow: session/ticket/PR
rows come from `docentd`'s `GET /api/workitems` (which may point at a **remote**
`docentd`, typically through [`docent-tunnel`](docs/Dashboard.md#reaching-a-remote-docentd-docent-tunnel)),
while focusing a session POSTs to the **local** wsm `/focus` — the window
manager on the machine you're sitting at. An **Open ↗** toolbar button pops
the full dashboard into your system browser; when a token is configured it's
forwarded once as a `?token=` query param, which the dashboard caches in
`sessionStorage` and strips from the address bar. A second hotkey — the picker's
chord plus **Shift** (default **Ctrl+Alt+Shift+Space**) — opens the dashboard in
your default browser directly, for when the dashboard rather than a work item is
what you're after.

- **Windows** — `apps/docent-launcher-windows/docent-launcher.ps1`: a WPF
  window with a Win32 `RegisterHotKey` (default **Ctrl+Alt+Space**), no extra
  runtime or admin required. Configurable via flags (`-SessionsUrl`,
  `-WsmUrl`, `-Token`, `-Hotkey`, `-DashboardHotkey`) or the equivalent `DOCENT_SESSIONS_URL` /
  `DOCENT_URL`, `WSM_URL`, `DOCENT_TOKEN` env vars; `-SelfTest` checks
  connectivity/parsing without opening a GUI. `scripts/install-docent-windows.ps1`
  registers it as a hidden, auto-restarting Scheduled Task. See its
  [README](apps/docent-launcher-windows/README.md) for the full flag/env
  reference and the `docent-tunnel` wiring.
- **macOS** — `apps/docent-launcher-macos/docent.lua`: a Hammerspoon chooser
  (default **Ctrl+Alt+Space**). Copy to `~/.hammerspoon/` and add
  `require("docent")` to `init.lua`. Reads `DOCENT_PORT` / `WSM_PORT` /
  `DOCENT_TOKEN` env vars, or overrides written to
  `~/.config/docent/launcher.lua` by the install script (e.g. a remote `url`);
  `hotkey` / `dashboardHotkey` can be overridden there too.
  Focus failures due to missing Accessibility permissions surface as a native
  notification with the fix.

## Installation

Per-OS installers put the docent binaries in `~/.local/bin`, write config into
`~/.config/docent/`, and register background services. Re-running is idempotent.

On Linux and macOS the installers download the latest
[release](https://github.com/KurtPreston/docent/releases) for your platform and
check it against the release's `checksums.txt`, so neither Go nor Node is
needed. Pass `--version vX.Y.Z` (or set `DOCENT_VERSION`) to pin a release, or
`--from-source` to build from your checkout instead (needs Go, plus Node >= 18
to build the dashboard). The Windows installer still builds from source.

The window manager itself is installed separately from the
[wsm](https://github.com/KurtPreston/wsm) repo (its own macOS/Windows installers).
The docent installers below set up `docentd`, the launcher, and Cursor hooks.

- **Linux** — [`scripts/install-docent-linux.sh`](scripts/install-docent-linux.sh):
  installs `docentd` only (the dashboard/collector daemon) as a `systemd --user`
  service, and enables lingering (`loginctl enable-linger`) by default so docentd
  keeps running — and scheduled automations still fire — even when you're logged
  out (pass `--no-linger` to opt out). Where systemd isn't running (e.g.
  containers), it starts docentd as a background process instead and prints a
  `--start-only` command to add to your container/workspace startup script.
  There is no window manager on Linux —
  install wsm on the Windows/macOS host that connects here.
- **macOS** — [`scripts/install-docent-macos.sh`](scripts/install-docent-macos.sh):
  installs `docentd` (optionally, locally via `launchd`), the Hammerspoon launcher
  by default, and Cursor hooks when Cursor.app is installed (`--no-hooks` /
  `--no-hammerspoon` to skip). In remote mode it also installs `docent-tunnel` as a
  `launchd` `KeepAlive` agent by default (`--no-tunnel` to hit the remote URL
  directly). The wizard picks the SSH host from `~/.ssh/config` (fzf when installed;
  `--ssh-host` to skip) and checks that docentd answers there. The tunnel honors `~/.ssh/config`
  aliases (including `ProxyCommand`, e.g. Coder workspaces), and the wizard
  verifies the connection (including the token) before finishing. Install the window manager from wsm separately.
  To remove it, run [`scripts/uninstall-docent-macos.sh`](scripts/uninstall-docent-macos.sh),
  which undoes all of the above and keeps `~/.config/docent` unless you pass `--purge`
  (`--dry-run` to preview).
- **Windows** — [`scripts/install-docent-windows.ps1`](scripts/install-docent-windows.ps1):
  installs `docent-launcher-windows` as a hidden, auto-restarting Scheduled Task
  (at-logon + a 1-minute watchdog), and optionally `docentd` locally. Prompts
  whether `docentd` runs locally or on a remote host; in remote mode it also
  installs `docent-tunnel` as its own watchdog task by default (`-NoTunnel` to opt
  out). The wizard picks the SSH host from `~/.ssh/config` (fzf when installed;
  `-SshHost` to skip). The tunnel honors `~/.ssh/config`
  aliases (including `ProxyCommand`), and the wizard verifies the connection (including the token) before finishing.
  Install the window manager from wsm separately.

### Updating

`docentd update` replaces an installed release with the latest one, along with
any other docent binaries (`docent-tunnel`, `docent-setup`, `docent-reporter`)
installed beside it. Checksum verification is required; missing manifests or
entries abort the update. All replacements are staged first, and a failed
replacement rolls back binaries already replaced.

It then restarts the matching daemon through systemd or launchd, or relaunches
a single Linux background process with the same arguments, directory,
environment, and shared stdout/stderr log. A stopped service is not treated as
a running daemon: starting it requires confirmation. Without a terminal,
the command prints startup instructions instead.

Automatic restarts are refused when process settings cannot be preserved,
when several instances use the same binary, or for hand-started macOS daemons.
Use `--restart=never` and restart those instances yourself. Service-managed
macOS installs use launchd normally.

```sh
docentd version            # what's installed
docentd update --check     # is a newer release out?
docentd update             # install it and restart docentd
docentd update --version v0.1.0 --restart=never
```

A docentd built from source reports version `dev`, and `update` won't replace
it unless you pass `--force`; pull and re-run the installer instead.
For installations predating the `update` command, pull this checkout and run
the platform installer once after the first release is published. An old binary
cannot bootstrap itself with a command it does not yet have.
`DOCENT_REPO` (default `KurtPreston/docent`) and `DOCENT_DOWNLOAD_BASE` (default
`https://github.com`) point the installers and `update` at a fork or mirror.

### Releasing

Pushing a `vX.Y.Z` tag runs [`.github/workflows/release.yml`](.github/workflows/release.yml):
it builds and tests, then [GoReleaser](.goreleaser.yaml) builds the dashboard
and publishes `docent_<os>_<arch>.tar.gz` archives for Linux and macOS (amd64
and arm64) with `checksums.txt`. Each archive holds `docentd` (dashboard
embedded), `docent-tunnel`, `docent-reporter`, and `docent-setup`, stamped with
the tag's version.

```sh
git tag v0.1.0 && git push origin v0.1.0
```

To try the release build locally without publishing anything, run
`go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean`
and look in `dist/`.

## Layout

- `libs/` — shared packages: `model`, `collectors`, `correlation`, `workitem` (grouping + status classification), `ai`, `config`, `automation` (automations engine), `goals`, `report` (shared reporter/dashboard/automations pipeline), `sessionmanager`, `wmclient`, `webhook`, `runlog`, …
- `apps/docent-reporter/` — reporter CLI
- `apps/docent-setup/` — config wizard + `check`
- `apps/docentd/` — daemon + dashboard (Vite/React SPA in `apps/docentd/web`, embedded via `-tags embed`)
- `apps/docent-launcher-macos/`, `apps/docent-launcher-windows/` — hotkey launchers
- `apps/docent-tunnel/` — workstation SSH local-forward helper for a remote, loopback-only docentd
- the local window manager lives in the separate [wsm](https://github.com/KurtPreston/wsm) repo
- `hooks/` — Cursor hook (`docent-notify.sh`) + snippet that report sessions to `docentd`
- `scripts/install-docent-{linux,macos,windows}.*` — per-OS installers; `scripts/uninstall-docent-macos.sh` removes a macOS install
- `~/.config/docent/` — `config.yaml` (reporter/automations), `docentd.yaml` (daemon), `goals.yaml`, `.env` (`$XDG_CONFIG_HOME/docent`)
- `~/.local/state/docent/logs/<run>/` — reporter run logs (`$XDG_STATE_HOME/docent`)
- `~/.local/state/docent/agent-sessions/` — hosted agent sessions and their transcripts
- `~/.local/state/docent/projects/<repo>/` — docent's own copy of a repository: a bare
  clone in `.base` plus one worktree per branch it has worked on. Never cleaned up;
  safe to delete for a repository you are done with
- `~/docent/` — saved markdown from the reporter (override via `output_dir` in config.yaml or `--out-dir`)
- `--userdata DIR` keeps the legacy all-in-one layout (config + .env + logs + output under one dir)
