# docent-launcher-windows

A Spotlight-style, always-on-top picker for docent on Windows, bound to a global
hotkey (default **Ctrl+Alt+Space**). Type to fuzzy-filter dashboard **work
items** (plus nested sessions / JIRA tickets / GitHub PRs); **Enter** opens or
launches a work item, focuses a session window, or opens a ticket/PR URL;
**Esc** hides it. The **Open ↗** button pops the full dashboard out into your
system browser — when `-Token` (or `DOCENT_TOKEN`) is set it is forwarded as a
one-time `?token=` query param, which the dashboard caches in `sessionStorage`
and strips from the address bar.

Built on WPF + Win32 `RegisterHotKey` (both ship with Windows) — no extra
runtime, no admin. It is a faithful port of the legacy docent WPF launcher,
adapted for the monorepo split:

- **Work-item rows** come from docentd's `GET /sessions` (`-SessionsUrl`, which
  may point at a **remote** docentd) — one primary row per dashboard group.
- **Opening a work item** POSTs `/api/workitems/{key}/open` (Cursor deep link)
  or `/launch` on docentd.
- **Focusing a session** POSTs to the **local** [wsm](https://github.com/KurtPreston/wsm)
  `/focus` (`-WsmUrl`, default `http://127.0.0.1:39788`) — the window manager that
  owns the windows on this machine.

When docentd runs on a remote dev box, the installer sets up **docent-tunnel** (a
local SSH forward) **by default** and points `-SessionsUrl` at
`http://127.0.0.1:39787` — the local end of the forward:

```powershell
scripts/install-docent-windows.ps1 -RemoteUrl http://<host>:39787
# -SshHost <host> overrides the SSH host (defaults to the URL host);
# -NoTunnel opts out and points -SessionsUrl straight at the remote URL.
```

That forward is owned by docent-tunnel (its own Scheduled Task), so it is live at
logon independent of any Cursor Remote-SSH session.

```powershell
# defaults: sessions from 127.0.0.1:39787, focus via 127.0.0.1:39788
pwsh -File docent-launcher.ps1

# remote docentd, local window manager
pwsh -File docent-launcher.ps1 -SessionsUrl http://desktop:39787 -WsmUrl http://127.0.0.1:39788

# connectivity / parsing check (no GUI)
pwsh -File docent-launcher.ps1 -SelfTest
```

`scripts/install-docent-windows.ps1` registers this as a hidden, auto-restarting
Scheduled Task (see the repo `docent-powershell` README for the watchdog
pattern). `SessionsUrl`/`WsmUrl`/`Token`/`Hotkey` may also be supplied via the
`DOCENT_SESSIONS_URL` (or `DOCENT_URL`), `WSM_URL`, and `DOCENT_TOKEN`
environment variables.
