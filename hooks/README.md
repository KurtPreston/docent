# Integration notes

## Cursor hooks -> docentd `/api/sessions/events`

Copy `hooks/docent-notify.sh` to `~/.cursor/hooks/` and merge `hooks/hooks.snippet.json` into `~/.cursor/hooks.json`.

The hook reports **agent activity only** — `beforeSubmitPrompt` → `agent_request_sent`
and `stop` → `agent_response_received` — because those events are not available to
the [docent IDE extension](../apps/docent-ide-extension/README.md), which owns
window lifecycle (open/close/focus) and heartbeats. Install the extension to get
live-window tracking.

**`ideHost` for remote windows.** The hook runs on the box where the agent
executes — the *remote* for a Remote-SSH window — so it can name neither the
client host nor the ssh alias. When Cursor sets `CURSOR_CODE_REMOTE=true`, the
hook sends `ideHost` as the boolean `true` ("remote, host unknown") instead of a
wrong hostname; docentd then binds the event to the client-side extension's live
window record for the same `path`. For a local session the hook sends its real
`hostname` as a string.

Set `DOCENT_URL` (remote docentd base URL) or `DOCENT_PORT` (default 39787 for local).
Hooks load `~/.config/docent/.env` when present. Optionally set `DOCENT_TOKEN`.

## grove -> wsm `/open`

Point grove's webhook at the **local** [wsm](https://github.com/KurtPreston/wsm) service:

```
POST http://127.0.0.1:39788/open
{"host":"<ssh-host>","path":"/remote/path","name":"<workspace-name>"}
```

On the Windows<->Ubuntu split, tunnel wsm's port from the desktop to the dev box (same pattern as the old docent `/open` tunnel, but targeting wsm).

## docent-launcher-macos

Copy `apps/docent-launcher-macos/docent.lua` to `~/.hammerspoon/docent.lua` and add `require("docent")` to `init.lua`.

Environment (or `~/.config/docent/launcher.lua` written by install script):
- `DOCENT_URL` — remote docentd base URL
- `DOCENT_PORT` — local docentd (default 39787)
- `WSM_PORT` — local wsm window manager (default 39788)

Focus actions go to wsm; session list comes from docentd `/api/workitems`.

## doctor

```
docentd doctor
# or: go run ./apps/docentd doctor
```

Config lives in `~/.config/docent/` (`docentd.yaml`, `config.yaml`, `.env`). Override with `DOCENT_CONFIG` or `DOCENT_CONFIG_DIR`.
