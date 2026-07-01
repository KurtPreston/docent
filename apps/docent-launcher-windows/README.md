# docent-launcher-windows

A Spotlight-style, always-on-top picker for docent on Windows, bound to a global
hotkey (default **Ctrl+Alt+Space**). Type to fuzzy-filter your live Cursor
sessions / JIRA tickets / GitHub PRs; **Enter** focuses the session window or
opens the ticket/PR URL; **Esc** hides it. The **Open ↗** button pops the full
dashboard out into your system browser — when `-Token` (or `DOCENT_TOKEN`) is
set it is forwarded as a one-time `?token=` query param, which the dashboard
caches in `sessionStorage` and strips from the address bar.

Built on WPF + Win32 `RegisterHotKey` (both ship with Windows) — no extra
runtime, no admin. It is a faithful port of the legacy docent WPF launcher,
adapted for the monorepo split:

- **Session rows** come from docentd's `GET /sessions` (`-SessionsUrl`, which may
  point at a **remote** docentd).
- **Focusing a session** POSTs to the **local** [wsm](https://github.com/KurtPreston/wsm)
  `/focus` (`-WmUrl`, default `http://127.0.0.1:39788`) — the window manager that
  owns the windows on this machine.

```powershell
# defaults: sessions from 127.0.0.1:39787, focus via 127.0.0.1:39788
pwsh -File docent-launcher.ps1

# remote docentd, local window manager
pwsh -File docent-launcher.ps1 -SessionsUrl http://desktop:39787 -WmUrl http://127.0.0.1:39788

# connectivity / parsing check (no GUI)
pwsh -File docent-launcher.ps1 -SelfTest
```

`scripts/install-docent-windows.ps1` registers this as a hidden, auto-restarting
Scheduled Task (see the repo `docent-powershell` README for the watchdog
pattern). `SessionsUrl`/`WmUrl`/`Token`/`Hotkey` may also be supplied via the
`DOCENT_SESSIONS_URL` (or `DOCENT_URL`), `DOCENT_WM_URL`, and `DOCENT_TOKEN`
environment variables.
