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
automations engine and the dashboard's Report tab.

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

### Cursor hooks → docentd

`hooks/docent-notify.sh` + `hooks/hooks.snippet.json` report session activity to
`docentd`. Copy the script to `~/.cursor/hooks/` and merge the snippet into
`~/.cursor/hooks.json`; the hook POSTs to `docentd`'s `/ingest` (fire-and-forget, so
a down `docentd` never blocks Cursor). Point it with `DOCENT_URL` (remote base URL)
or `DOCENT_PORT` (default 39787 local); it loads `~/.config/docent/.env` and sends
`DOCENT_TOKEN` when set. See [`hooks/README.md`](hooks/README.md).

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

## Installation

Per-OS installers build the relevant binaries, write config into
`~/.config/docent/`, and register background services. Re-running is idempotent.

The window manager itself is installed separately from the
[wsm](https://github.com/KurtPreston/wsm) repo (its own macOS/Windows installers).
The docent installers below set up `docentd`, the launcher, and Cursor hooks.

- **Linux** — [`scripts/install-docent-linux.sh`](scripts/install-docent-linux.sh):
  installs `docentd` only (the dashboard/collector daemon) as a `systemd --user`
  service, and enables lingering (`loginctl enable-linger`) by default so docentd
  keeps running — and scheduled automations still fire — even when you're logged
  out (pass `--no-linger` to opt out). There is no window manager on Linux —
  install wsm on the Windows/macOS host that connects here.
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
