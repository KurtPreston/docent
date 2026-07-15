# docentd (the dashboard daemon)

Beyond running the reporter, `docentd` is a **mission-control dashboard**: a
live, color-coded, grouped-by-ticket view of your Cursor sessions, JIRA
tickets, and GitHub PRs, with focus-or-open window control. It also hosts the
[automations](Automations.md) engine and the Settings editor for every
docent config file. Window control itself lives in a small **local** REST
service — the separate [wsm](https://github.com/KurtPreston/wsm) project
(`wsmd`) — so `docentd` can run remotely (on your dev box) while the windows
it focuses are managed on your workstation. See the [README](../README.md)
for the monorepo overview.

## Pages

The SPA (`apps/docentd/web`) has six nav tabs plus one deep-linked detail
route, all served by `docentd` itself:

| Route | Page | What it's for |
|-------|------|----------------|
| `/` | **Dashboard** | The main table: work items grouped by ticket/branch, with JIRA/PR/session columns (each gated on whether that collector/session manager is configured), status pills, and an **open** action per row. Auto-refreshes every 5s. |
| `/signals` | **Signals** | Every collection unit (directive × collector × mode) with its last run/error, and the raw signals it produced — useful for debugging what a collector actually saw. |
| `/collectors` | **Collectors** | Operational view of collection units: poll interval, last run, next due, item counts, errors. A **collect** button force-runs one unit immediately. |
| `/report` | **Report** | Generate a Markdown report in-browser: pick a mode, lookback, scope, and collect mode, watch live progress (collector-by-collector, then token-by-token), and download the result. See [Report page](#report-page) below. |
| `/automations` | **Automations** | Lists configured rules and recent job history, with a manual **Run** button per rule. Rules themselves are edited as YAML in Settings. See [docs/Automations.md](Automations.md). |
| `/settings` | **Settings** | A Monaco YAML editor (lazy-loaded, so its editor bundle doesn't cost other pages) for `config.yaml`, `docentd.yaml`, and `goals.yaml`, with JSON Schema validation. Saves write straight to disk; **docentd restart is required** to pick up changes (no hot reload). |
| `/workitem?key=…` | **Work item detail** | Not in the nav — reached by clicking a Dashboard row. Shows the full picture for one ticket/branch: sessions, PRs, linked JIRA, entities, and the raw signals that fed it. |

## Binding + auth

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
- **Launchers** — pass the token through (`-Token` on Windows, `DOCENT_TOKEN`
  on macOS); see the [README's Launchers section](../README.md#launchers).

Caveats:

- Binding externally exposes your activity data to anyone who can reach the
  port. A token is a reasonable bar for a personal dev box; open the host
  firewall for the port deliberately. There is **no TLS** — front it with a
  reverse proxy if the host is broadly reachable.
- With no token configured, behavior is unchanged (loopback-only, open). If you
  set `bindHost` to a non-loopback address **without** a token, docentd logs a
  loud startup warning that data is exposed unauthenticated.

## HTTP API

Every route below is registered in
[`apps/docentd/internal/server/server.go`](../apps/docentd/internal/server/server.go).
All routes except `/health` and the static SPA shell require the bearer
token when one is configured (see [Binding + auth](#binding--auth) above).

| Method | Path | Purpose |
|--------|------|---------|
| `GET` | `/health` | Liveness probe; always open. |
| `GET` | `/sessions`, `/api/workitems` | Dashboard payload (work-item groups); triggers an on-request refresh of any collectors flagged `onRequest`. Both paths hit the same handler. |
| `GET` | `/api/workitems/{key}` | One work item's full detail (sessions, PRs, JIRA, entities, signals). |
| `POST` | `/api/workitems/{key}/launch` | Runs the [`onClickScript`](#docentdyaml-reference) hook with `DOCENT_*` env vars describing the work item. |
| `POST` | `/api/workitems/{key}/open` | Cursor session manager only: syncs the work item's color into `.vscode/settings.json`, then returns a `cursor://` deep link for the client to navigate. |
| `GET` | `/api/signals` | Raw signals per collection unit, for the Signals page. |
| `GET` | `/api/collectors` | Collection-unit health/metadata, for the Collectors page. |
| `POST` | `/api/units/{directive}/{mode}/collect` | Force-collects one `state` or `events` unit right now, ignoring its poll interval. |
| `GET` | `/api/config` | Contents of every editable config file (`config`, `docentd`, `goals`), for Settings. |
| `PUT` | `/api/config/{id}` | Validates and writes one config file. |
| `POST` | `/api/config/{id}/validate` | Validates without writing (live feedback as you type). |
| `GET` | `/api/config/{id}/schema` | JSON Schema for a config file, for Monaco's inline validation. |
| `POST` | `/api/report` | Starts a background report job; returns `202 { id }`. See [Report page](#report-page). |
| `GET` | `/api/report/meta` | Modes, scopes, collect options, and the active AI provider, for the Report form. |
| `GET` | `/api/report/{id}` | Polls a report job's status/markdown/error. |
| `GET` | `/api/report/{id}/stream` | Server-Sent Events feed of report progress (phase, per-collector status, streamed tokens/thinking). |
| `POST` | `/api/hooks/{directive}` | Nudge webhook: force-collects a directive's `state` and `events` units so signal/transition automations can fire without waiting for the next poll. Auth accepts the bearer token, an `X-Docent-Hook-Secret`, or a GitHub-style `X-Hub-Signature-256` HMAC against `DOCENT_HOOK_SECRET`. |
| `GET` | `/api/automations` | Configured rules plus recent job history (`?limit=N`, default 50). See [docs/Automations.md](Automations.md). |
| `GET` | `/api/automations/{id}` | One rule's definition. |
| `POST` | `/api/automations/{id}/run` | Manually fires a rule's actions now, bypassing its trigger, cooldown, and enabled flag; an optional JSON body supplies synthetic event context. |
| `POST` | `/ingest` | Cursor hooks POST here to report session activity; see [Cursor hooks → docentd](../README.md#cursor-hooks--docentd) in the README. |
| `GET` | `/*` | Serves the built SPA; any extensionless, unmatched path falls back to `index.html` so client-side routes work. |

## Report page

The **Report** tab runs the same pipeline as the `docent-reporter` CLI (both
share [`libs/report`](../libs/report)): pick a mode, an optional lookback
(days), a scope (`self` / `involved` / `all`, or the mode default), an
optional `collect` override (`events` / `state` / `both`), and an optional
prompt override, then generate a Markdown report, watch it stream in over
SSE, view it in-browser, and download it as `.md`.

Generation can take a while (LLM providers), so docentd runs it as a
background job the page polls or subscribes to; jobs are in-memory and
ephemeral (bounded, TTL-pruned, lost on restart — a report is cheap to
re-run). See [docs/Reporting.md](Reporting.md) for what each mode/scope
actually does and how AI providers are configured — the Report tab and the
CLI share both.

## Session manager (cursor / wsm / none)

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

## Window manager (wsm)

The window manager is now the standalone [wsm](https://github.com/KurtPreston/wsm)
daemon (`wsmd`): a localhost-only REST service that owns the Cursor windows on the
machine you sit at (default port **39788**). docent is a *client* of it via the
`wsm` collector and the dashboard's focus button. The contract
`GET /health`, `GET /windows`, `POST /open`, `POST /focus` is published as an
OpenAPI spec in the wsm repo. Install and run it from there; there is no window
manager binary in this repo anymore.

## grove → wsm

The [`grove`](https://github.com/KurtPreston/grove) sender POSTs the
`{host, path, name}` webhook to the **local** wsm `/open`, tunneled from the
dev box to the workstation over reverse SSH. wsm needs no SSH of its own —
the remote path arrives in the payload.

## Reaching a remote docentd (docent-tunnel)

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

## docentd.yaml reference

The dashboard/daemon reads `docentd.yaml` (separate from the reporter's
`config.yaml`). See [`config/docent/docentd.yaml.example`](../config/docent/docentd.yaml.example)
for a starting point.

| Field | Default | Purpose |
|-------|---------|---------|
| `port` | `39787` | Listen port. |
| `bindHost` | `127.0.0.1`, or `0.0.0.0` when `token` is set | Listen interface; see [Binding + auth](#binding--auth). |
| `token` | unset | Shared secret; `DOCENT_TOKEN` env var wins if both are set. |
| `refreshSec` | `60` | Dashboard poll interval hint. |
| `wsmUrl` | `http://127.0.0.1:39788` | Local wsm daemon URL injected into the dashboard. |
| `onClickScript` | `~/.config/docent/onclick.sh` | Hook run by `POST /api/workitems/{key}/launch`; see below. `DOCENT_ONCLICK` env var overrides. |
| `sshHost` | unset | SSH alias for remote-open, passed to the hook as `DOCENT_HOST`. |
| `ticketProjects` | auto-detected | Restricts ticket-key matching (branch names, PR/commit titles, JIRA issue keys) to these project keys, e.g. `[SALSA, JASPER]`, so generic hyphenated tokens like `PR-7373` or `release-2026` can't false-match as tickets. The engine also auto-widens matching with any project key observed on collected JIRA issues (and each jira directive's `config.followed_projects`), so this mainly matters when no jira directive is configured at all. |
| `ticketPattern` | unset | Fully overrides ticket matching with a custom regex (first capture group is the key) instead of `ticketProjects`. |

### Launch hook (`onClickScript`)

Clicking **open** on a work item that has no session manager deep link (or
whose session manager can't focus it) runs the hook script with `DOCENT_*`
environment variables describing the work item (`DOCENT_BRANCH`,
`DOCENT_OPEN_PATH`, `DOCENT_HOST`, `DOCENT_TICKET`, …). The default script,
[`examples/onclick.sh`](../examples/onclick.sh) (installed to
`~/.config/docent/onclick.sh` by `docent-setup` when nothing exists there
yet), tries in order: a `grove`-managed worktree, a local `cursor
--new-window`, then a remote wsm `/open` over the SSH host — customize freely.

### `docentd doctor`

```sh
docentd doctor
# or: go run ./apps/docentd doctor
```

Prints where `docentd` resolves its config from (`docentd.yaml`,
`config.yaml`, `.env`), honoring the same `DOCENT_CONFIG` /
`DOCENT_CONFIG_DIR` overrides the daemon itself uses.

## Dashboard frontend (build)

The dashboard is a **Vite + React + TypeScript** single-page app under
[`apps/docentd/web`](../apps/docentd/web). It's a pure client of docentd's JSON API
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
