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

The SPA (`apps/docentd/web`) has eight nav tabs plus one deep-linked detail
route, all served by `docentd` itself:

| Route | Page | What it's for |
|-------|------|----------------|
| `/` | **Cockpit** | The default surface, and the one meant to be left open: a rail of actionable lanes (one per branch or ticket, colored to match the editor title bar for the same branch), the selected lane's detail with its [hosted agent](#agent-sessions), and a follow-up inbox whose rows each seed an agent prompt. See [Cockpit](#cockpit) below. |
| `/dashboard` | **Dashboard** | The full, unfiltered table: work items grouped by ticket/branch, with JIRA/PR/session columns (each gated on whether that collector/session manager is configured), status pills, and an **open** action per row. Auto-refreshes every 5s. |
| `/signals` | **Signals** | Every collection unit (directive × collector × mode) with its last run/error, and the raw signals it produced — useful for debugging what a collector actually saw. |
| `/collectors` | **Collectors** | Operational view of collection units: poll interval, last run, next due, item counts, errors. A **collect** button force-runs one unit immediately. |
| `/sessions` | **Sessions** | The raw session registry as reported by IDE extensions and Cursor hooks, with liveness and a launch action per session. |
| `/report` | **Report** | Generate a Markdown report in-browser: pick a mode, lookback, scope, and collect mode, watch live progress (collector-by-collector, then token-by-token), and download the result. See [Report page](#report-page) below. |
| `/automations` | **Automations** | Lists configured rules and recent job history, with a manual **Run** button per rule. Rules themselves are edited as YAML in Settings. See [docs/Automations.md](Automations.md). |
| `/settings` | **Settings** | A Monaco YAML editor (lazy-loaded, so its editor bundle doesn't cost other pages) for `config.yaml`, `docentd.yaml`, and `goals.yaml`, with JSON Schema validation. Saves write straight to disk; **docentd restart is required** to pick up changes (no hot reload). |
| `/workitem?key=…` | **Work item detail** | Not in the nav — reached by clicking a Dashboard row. Shows the full picture for one ticket/branch: sessions, PRs, linked JIRA, entities, and the raw signals that fed it. |

## Binding + auth

`docentd` serves the live dashboard and its data APIs (`/api/*`).
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
| `GET` | `/api/workitems` | Dashboard payload (work-item groups); triggers an on-request refresh of any collectors flagged `onRequest`. |
| `GET` | `/api/cockpit` | The actionable subset of the same data — lanes, the follow-up inbox, the assigned-but-unstarted queue, and per-source load state — for the Cockpit page. |
| `GET` | `/api/workitems/{key}` | One work item's full detail (sessions, PRs, JIRA, entities, signals). |
| `POST` | `/api/workitems/{key}/launch` | Runs the [`onClickScript`](#docentdyaml-reference) hook with `DOCENT_*` env vars describing the work item. |
| `POST` | `/api/workitems/{key}/open` | Resolves where to open the work item — an agent session's directory, an existing worktree, or a worktree created in your project for the occasion — syncs its color into `.vscode/settings.json`, and returns a `cursor://` deep link for the client to navigate. May report `divergence` when docent's copy of the branch is ahead of the one opened. |
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
| `POST` | `/api/sessions/events` | Session ingest: IDE extensions and Cursor hooks POST session lifecycle/activity events (`open`/`close`/`agent_request_sent`/`agent_response_received`/`heartbeat`) keyed by `ide`+`ideHost`+`targetHost`+`path`. See [Cursor hooks → docentd](../README.md#cursor-hooks--docentd). |
| `GET` | `/api/sessions` | The ingest view of live/known sessions from the registry (composite-keyed), with heartbeat-derived liveness. |
| `POST` | `/api/agents` | Starts an [agent session](#agent-sessions): provisions the branch's worktree at the chosen `target` and runs the opening turn in the background. Returns `202 { session }`, or `409` with `conflict: "foreign-agent"` when an editor's own agent is working there and `conflict: "diverged"` when docent's copy of the branch has forked from yours — retry with `force: true` to proceed anyway. |
| `GET` | `/api/agents` | Every persisted session, most recently updated first. |
| `GET` | `/api/agents/{id}` | One session's record. |
| `DELETE` | `/api/agents/{id}` | Stops any running turn and deletes the session and its transcript. The worktree is left alone; nothing under docent's state directory is ever cleaned up automatically. |
| `POST` | `/api/agents/{id}/turn` | Runs another turn, resuming the conversation. `409` when one is already running in that worktree, or when an editor's agent is; `force: true` overrides the latter. |
| `POST` | `/api/agents/{id}/stop` | Cancels the running turn. The session stays resumable. |
| `POST` | `/api/agents/{id}/promote` | Stops the agent and writes `HANDOFF.md` into the worktree; returns the deep link and prompt text for the client to open an editor with. See [Promoting to Cursor](#promoting-to-cursor). |
| `GET` | `/api/agents/{id}/events` | Server-Sent Events transcript. Replays the whole session, then streams; stays open across turns. |
| `GET` | `/api/projects` | The repositories an agent can be started in, for the Cockpit's repository picker. |
| `GET` | `/api/worktree-targets` | `?repo=&branch=`: the placements an agent could run in, each with a `kind` (`existing`, `create`, `isolated`, `in_place`), the directory it would use, a label, and a `disabled` reason when it cannot be picked. |
| `GET` | `/*` | Serves the built SPA; any extensionless, unmatched path falls back to `index.html` so client-side routes work. |

## Cockpit

The Dashboard answers "what am I working on"; the Cockpit answers "what needs me
right now", which is a much shorter list. It is the surface meant to be left
open all day as its own window.

`GET /api/cockpit` returns only lanes that can say why they are there: a live
session, a PR of yours with unresolved comments or failing checks or an
approval, a PR awaiting your review, or an in-progress ticket. Everything
assigned but unstarted goes to a collapsed queue instead of a lane, because a
backlog rendered at the same weight as live work hides the live work. The rail's
colors come from `model.ColorForName`, which derives them from the branch
name, so a lane
matches the title bar of the window it opens.

Two collapsed lists sit under the rail for the same reason, one for each kind of
work nobody has actually asked you for. **Assigned to you** is the unstarted
backlog, grouped by the project's own JIRA status names. **PRs to review** is
every open PR in a [followed repo](Reporting.md#prs-you-could-review) — plus
anything assigned to you, draft or not — that is not yours and did not name you
as a reviewer, grouped by review state with the ones still waiting on a reviewer
first. Neither list counts toward the "need you" badge and neither
reaches the inbox; a PR you were actually asked to review is a lane, not a
queue entry. Selecting either opens it like any other lane, so an agent can be
pointed at it.

Three columns: the lane rail, the selected lane's detail (windows, PRs, the
threads waiting on a reply, and its agent), and the follow-up inbox. Each inbox
row has a default action that seeds an agent prompt into the row's lane —
addressing a review comment, fixing CI, merging an approved PR, or summarizing a
PR you were asked to review — and each ticket lane offers a **plan it** action
that asks for a `PLAN.md` rather than a change. Seeding fills the compose box; it
does not send, so the prompt is always editable first.

For running it as a pinned app window with hotkey access, see
[`apps/docent-launcher-windows/docent-cockpit.ps1`](../apps/docent-launcher-windows/docent-cockpit.ps1).

## Agent sessions

`docentd` runs coding agents itself, in the browser, one lane per branch.
[`libs/agentsession`](../libs/agentsession) drives Claude Code and `cursor-agent`
as one short-lived subprocess per turn (neither has a server mode; both can
resume a conversation by id), normalizes their two streaming JSON dialects into
one event vocabulary, and persists both the session record and the full
transcript under `$XDG_STATE_HOME/docent/agent-sessions/`.

- **You choose where the agent runs**, from the placements docent finds for the
  repository and branch: reuse a worktree that already has the branch, add one
  to your project, switch an ordinary clone in place, or take docent's own
  isolated worktree under `$XDG_STATE_HOME/docent/projects/`. The isolated one
  is the default and the only one an automation ever gets, since it is the one
  that cannot disturb something you have open. Deleting a session leaves the
  directory alone.
- **docent's own worktree is kept in step with yours.** Before every turn it
  fetches your git directory and `origin`, fast-forwarding when cleanly behind
  and refusing with `409 conflict: "diverged"` when the two have forked. After
  every turn it commits, so the work is fetchable rather than invisible. The
  open button then creates a worktree in *your* project and puts docent's tip
  beside it as `refs/remotes/docent/<branch>` — never merged into your branch.
- **Every new directory gets one run of `worktreeHook`.** That is where the
  `.env` files and dependency installs a fresh checkout needs live; see
  [Automations](Automations.md#per-worktree-setup).
- **One agent per worktree**, enforced. Two agents share a git index and corrupt
  each other, so a second turn in the same directory is refused with `409`
  rather than queued.
- **That includes agents docent did not start.** An IDE window's own agent is
  visible through the session registry — the Cursor hook reports each turn's
  start and end against the workspace path — so a start into a worktree Cursor
  is already working in is refused too. That claim is inferred rather than known,
  so it is a warning with an override (`force: true`) rather than a lock, and it
  is ignored once the window stops heartbeating or its turn outlasts
  `agentsession.DefaultTurnTimeout`, so a dropped hook event cannot fence docent
  out of a directory permanently.
- **Sessions outlive turns and restarts.** The stream stays open across turns,
  and a record left marked `running` by a restart is reconciled to `stopped`
  rather than shown as work that will never finish.
- **A headless turn cannot ask you for approval.** This build of Claude Code has
  no `--permission-prompt-tool`, so "needs a decision" is the signal to promote
  the session rather than something to answer in the browser.

### Promoting to Cursor

Cursor exposes no way to open a new scoped chat programmatically. The only
deeplink is `cursor://anysphere.cursor-deeplink/prompt?text=`, which pre-fills
whichever window has focus and still needs a manual send. So promotion is built
around that limit rather than pretending past it:

1. `POST /api/agents/{id}/promote` stops the agent — you are taking the worktree
   over — and writes `HANDOFF.md` at the worktree root summarizing the session,
   its last few turns, and where it stopped. The file is added to the worktree's
   own `.git/info/exclude`, so the next `git add -A` cannot commit it into the PR.
2. The browser opens that directory through wsm's `/open` (falling back to the
   `cursor://` folder deep link when wsm is not running).
3. A moment later it fires the prompt deeplink, pre-filling the new window's chat
   box with an instruction to read `HANDOFF.md` and continue.

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

## Open trigger + live-window polling (cursor / wsm / none)

Two independent concerns:

1. **Open trigger** — how the dashboard opens/focuses an editor window for a
   work item — is selected by an `open_trigger` block in `config.yaml`
   (mirroring the `ai:` block). There is **no default**.
2. **Live-window listing** — whether the dashboard shows which windows are open
   — is a separate collector directive (`cursor` or `wsm`). Session activity can
   also arrive via the ingest API (`POST /api/sessions/events`).

The Linux installer sets `open_trigger.provider: cursor` and adds a `cursor`
directive when that CLI is present; macOS/Windows installers leave both unset.

Open trigger providers:

- **`cursor`** — renders each work item's path as a `cursor://` deep link.
  Clicking it first syncs the work item's color into the repo's
  `.vscode/settings.json` (via `POST /api/workitems/:key/open`, disable with
  `cursor.write_color: false`) and then navigates the link to open/focus the
  window. Exact-window focus is best-effort (Cursor may open a duplicate).
- **`wsm`** — opens/focuses windows through the local [wsm](https://github.com/KurtPreston/wsm)
  daemon. Choose this on the workstation when you need reliable exact-window focus.
- **unset** — no clickable open/focus links.

To also list live windows, declare a matching collector directive. On a remote
Linux docentd that shares Cursor's remote-cli IPC, a `cursor` directive polling
`cursor --status` works well; on macOS/Windows local docentd it can spawn a
second GUI briefly, so omit the `cursor` directive there (deep-link open still
works).

```yaml
open_trigger:
  provider: cursor      # or: wsm
  cursor:
    write_color: true   # sync work-item color into .vscode/settings.json (default)

directives:
  - id: local-cursor    # list live windows via cursor --status
    name: Cursor sessions
    collector: cursor
    enabled: true
    config:
      machine: local
    state:
      poll:
        on_request: true
        on_load: true
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

## Session ingest: IDE extension + Cursor hooks

Besides polling collectors, docentd receives session events directly at
`POST /api/sessions/events`, keyed by the composite `ide`+`ideHost`+`targetHost`+`path`:

- The [docent IDE extension](../apps/docent-ide-extension/README.md) (a small
  VS Code / Cursor extension) reports window lifecycle — `open` on activate,
  periodic `heartbeat`, `close` on shutdown/folder-removal. A session is
  presumed dead once it is silent for `sessions.heartbeat_interval *
  sessions.missed_heartbeats` (default 30s × 2), then swept from the registry.
- The slim [Cursor hook](../hooks/README.md) reports agent activity
  (`agent_request_sent` / `agent_response_received`), which the extension API
  cannot observe.

The extension additionally reports each window's `remoteAuthority` — the
workspace URI authority verbatim — which docentd stores alongside the session
and echoes back in the `cursor://` deep link. Cursor decides whether a link
reveals an open window or opens a new one by comparing whole workspace URIs, and
recent builds spell the ssh target as hex-encoded JSON
(`ssh-remote+7b22686f73744e616d65223a226465736b746f70227d` for
`{"hostName":"desktop"}`), so a link synthesized from the ssh alias addresses a
*different* workspace and opens a duplicate window. The ssh alias remains the
fallback for a checkout no window has open.

Build the extension with `scripts/build-extension.sh`; the platform installers
offer to install it (and write `docent.url` / `docent.token` into the editor's
settings) when they detect Cursor or VS Code. See
[`GET /api/sessions`](#docentd-http-api) for the ingest view of live sessions.

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

Because `docent-tunnel` dials the host directly with Go's SSH library, it does
**not** read `~/.ssh/config`. The installers therefore run `ssh -G <host>` to
resolve an SSH alias (e.g. `desktop`) to its real `HostName` and to pick up the
configured `IdentityFile`, then pass those to the tunnel. Before finishing, the
wizard verifies the connection end-to-end (a one-shot `docent-tunnel -check` that
performs the same SSH dial + `known_hosts` check + an authenticated request,
including the bearer token) and will not complete until it succeeds — so a bad
alias, missing key, untrusted host key, or wrong token is caught during install
rather than surfacing later as `ERR_CONNECTION_REFUSED`.

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
| `worktreeHook` | `~/.config/docent/worktree.sh` | Run once in every working directory docent creates; skipped when the file does not exist. See [Per-worktree setup](Automations.md#per-worktree-setup). `DOCENT_WORKTREE_HOOK` env var overrides. |
| `sshHost` | unset | SSH alias for remote-open, passed to the hook as `DOCENT_HOST`. |
| `ticketProjects` | auto-detected | Restricts ticket-key matching (branch names, PR/commit titles, JIRA issue keys) to these project keys, e.g. `[SALSA, JASPER]`. Without a jira directive, generic `WORD-digits` scanning is disabled (so Dependabot branches like `fontawesome-free-7` don't invent phantom tickets); set `ticketProjects` (or `ticketPattern`) to opt into matching. With jira enabled, matching starts generic and the engine auto-widens/narrows via `followed_projects` and observed issue keys. |
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
(`/api/*`) and is embedded into the `docentd` binary at build time,
so a released binary is self-contained. Requires **Node >= 18**.

- **Dev** (hot reload) — run a `docentd` (default `127.0.0.1:39787`), then:

  ```bash
  cd apps/docentd/web
  npm install
  npm run dev     # http://localhost:5173; proxies /api,/health to docentd
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
