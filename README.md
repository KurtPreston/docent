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

### Session manager (cursor / wsm / none)

How the dashboard lists and opens editor windows is selected by a
`session_manager` block in `config.yaml` (mirroring the `ai:` block). There is
**no default** — set one explicitly (the Linux remote installer may suggest
`cursor` when that CLI is present; macOS/Windows installers leave it unset):

- **`cursor`** — lists windows via `cursor --status` and renders each work
  item's path as a `cursor://` deep link. Clicking it first syncs the work
  item's color into the repo's `.vscode/settings.json` (via
  `POST /api/workitems/:key/open`, disable with `cursor.write_color: false`)
  and then navigates the link to open/focus the window. Exact-window focus is
  best-effort (Cursor may open a duplicate). Prefer this on a remote Linux
  docentd that shares Cursor's remote-cli IPC; on macOS/Windows local
  docentd, polling `cursor --status` can spawn a second GUI briefly.
- **`wsm`** — lists and focuses windows through the local [wsm](https://github.com/KurtPreston/wsm)
  daemon. Choose this on the workstation when you need reliable exact-window focus.
- **unset** — no session column and no clickable links.

```yaml
session_manager:
  provider: cursor      # or: wsm
  cursor:
    write_color: true   # sync work-item color into .vscode/settings.json (default)
```

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
