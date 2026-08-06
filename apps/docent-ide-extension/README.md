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
- `ideHost` — this machine's hostname (`os.hostname()`). This is declared as a
  UI extension (`"extensionKind": ["ui"]`), so it always runs on the machine
  with the GUI, even for a Remote-SSH window. `ideHost` is therefore the host
  you sit at, never the remote.
- `targetHost` — the ssh alias the window edits, parsed out of the workspace
  folder's `vscode-remote` URI authority (`ssh-remote+<host>`), else empty. The
  authority is used rather than `vscode.env.remoteName`, which only names the
  resolver kind (`ssh-remote`) and not which host.
- `path` — each open workspace folder (a folderless window still reports). For a
  remote folder this is `uri.path`, the path as the *remote* spells it, not
  `uri.fsPath`. On a Windows client `fsPath` would render `/home/me/x` as
  `\home\me\x`, which no longer matches what the agent hook reports from the
  remote, and docentd would file one window as two sessions.

Because the extension runs on the GUI host while the agent hook runs wherever
the agent executes, a single Remote-SSH window is reported by two clients on two
different machines. docentd reconciles them by `ide` + normalized path; see
`resolveKeyLocked` in the session registry.

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
