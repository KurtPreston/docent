# Docent Session Reporter

A tiny VS Code / Cursor extension that reports editor **window lifecycle** and
**heartbeats** to [`docentd`](../../README.md)'s session ingest API
(`POST /api/sessions/events`). It lets docentd know which editor windows are
open, on which host, targeting which remote, and at which path — without polling
`cursor --status`.

## What it reports

A session's identity is the composite of `ide` + `ideHost` + `targetHost` +
`path`, matching docentd's registry key:

- `ide` — `cursor`, `vscode`, or `windsurf` (derived from the app name).
- `ideHost` — this machine's hostname (`os.hostname()`). For a Remote-SSH
  window this is the remote host, since the extension host runs there.
- `targetHost` — `vscode.env.remoteName` when the window is remote, else empty.
- `path` — each open workspace folder (a folderless window still reports).

Events sent:

| Trigger | Event |
|---------|-------|
| activation | `open` |
| every `docent.heartbeatSeconds` (and on window focus) | `heartbeat` |
| folder added / removed | `open` / `close` |
| deactivation / shutdown | `close` |

Agent request/response events are **not** sent by this extension (they are not
available to the extension API); the slim Cursor shell hook
([`hooks/docent-notify.sh`](../../hooks/docent-notify.sh)) reports those.

## Settings

| Setting | Default | Description |
|---------|---------|-------------|
| `docent.url` | `http://127.0.0.1:39787` | Base URL of docentd. |
| `docent.token` | `""` | Bearer token (only if docentd requires auth). |
| `docent.heartbeatSeconds` | `30` | Heartbeat cadence. |

All requests are fire-and-forget with a 2s timeout, so a slow or down docentd
never disrupts the editor.

## Build

```bash
scripts/build-extension.sh   # from the repo root; writes docent-ide-extension.vsix
```

Install the packaged extension with:

```bash
cursor --install-extension apps/docent-ide-extension/docent-ide-extension.vsix
# or
code --install-extension apps/docent-ide-extension/docent-ide-extension.vsix
```

The platform installers (`scripts/install-docent-*`) offer to do this for you
when Cursor and/or VS Code is detected.
