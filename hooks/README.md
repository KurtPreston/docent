# Integration notes

## Cursor hooks -> docentd `/ingest`

Copy `hooks/docent-notify.sh` to `~/.cursor/hooks/` and merge `hooks/hooks.snippet.json` into `~/.cursor/hooks.json`.

Set `DOCENT_PORT` (default 39787) and optionally `DOCENT_TOKEN`.

## grove -> docent-wm `/open`

Point grove's webhook at the **local** docent-wm service:

```
POST http://127.0.0.1:39788/open
{"host":"<ssh-host>","path":"/remote/path","name":"<workspace-name>"}
```

On the Windows<->Ubuntu split, tunnel docent-wm's port from the desktop to the dev box (same pattern as the old docent `/open` tunnel, but targeting docent-wm).

## docent-launcher-macos

Copy `apps/docent-launcher-macos/docent.lua` to `~/.hammerspoon/docent.lua` and add `require("docent")` to `init.lua`.

Environment:
- `DOCENT_PORT` — docentd (default 39787)
- `DOCENT_WM_PORT` — local docent-wm (default 39788)

Focus actions go to docent-wm; session list comes from docentd `/sessions`.

## doctor

```
go run ./apps/docentd doctor -config userdata/docentd.yaml
```
