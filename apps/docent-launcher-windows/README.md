# docent-launcher-windows

Two Windows-side surfaces for docent: a transient picker you summon and dismiss
(`docent-launcher.ps1`), and a persistent cockpit window you jump to
(`docent-cockpit.ps1`).

## docent-launcher.ps1 — the picker

A Spotlight-style, always-on-top picker for docent on Windows, bound to a global
hotkey (default **Ctrl+Alt+Space**). Type to fuzzy-filter dashboard **work
items** (plus nested sessions / JIRA tickets / GitHub PRs); **Enter** opens or
launches a work item, focuses a session window, or opens a ticket/PR URL;
**Esc** hides it. The **Open ↗** button hands the dashboard URL to the shell, so
it opens in your **default browser** like any other link — when `-Token` (or
`DOCENT_TOKEN`) is set it is forwarded as a one-time `?token=` query param, which
the dashboard caches in `sessionStorage` and strips from the address bar. A
second global hotkey (`-DashboardHotkey`, default **Ctrl+Alt+Shift+Space**) does
the same thing without summoning the picker first; pass `-DashboardHotkey ''` to
skip registering it. For the dashboard as a window you keep rather than a tab you
reopen, see `docent-cockpit.ps1` below.

Built on WPF + Win32 `RegisterHotKey` (both ship with Windows) — no extra
runtime, no admin. It is a faithful port of the legacy docent WPF launcher,
adapted for the monorepo split:

- **Work-item rows** come from docentd's `GET /api/workitems` (`-SessionsUrl`, which
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

# custom chords for the picker and the dashboard
pwsh -File docent-launcher.ps1 -Hotkey "Win+Space" -DashboardHotkey "Win+Shift+Space"

# connectivity / parsing check (no GUI)
pwsh -File docent-launcher.ps1 -SelfTest
```

`scripts/install-docent-windows.ps1` registers this as a hidden, auto-restarting
Scheduled Task (see the repo `docent-powershell` README for the watchdog
pattern), and takes `-Hotkey` / `-DashboardHotkey` to override both chords.
`SessionsUrl`/`WsmUrl`/`Token`/`Hotkey` may also be supplied via the
`DOCENT_SESSIONS_URL` (or `DOCENT_URL`), `WSM_URL`, and `DOCENT_TOKEN`
environment variables.

## docent-cockpit.ps1 — the cockpit window

The cockpit (docentd's `/`) is meant to be a window you never close, so it gets a
window rather than a tab. Bound to **Ctrl+Alt+C** by default:

```powershell
# open the cockpit on its own virtual desktop and watch the hotkey
pwsh -File docent-cockpit.ps1

# remote docentd, different hotkey
pwsh -File docent-cockpit.ps1 -Url http://desktop:39787 -Hotkey "Ctrl+Alt+D"

# open-or-focus once and exit (useful from another launcher or a shortcut)
pwsh -File docent-cockpit.ps1 -Once
```

What it does, and why:

- Launches the cockpit with Chromium's `--app=` flag, so there is no tab strip or
  address bar and nothing else can hide in the same window. A dedicated
  `--user-data-dir` under `%LOCALAPPDATA%\docent` keeps it out of your normal
  browser profile, so quitting your browser never takes the cockpit with it.
- **Focus-or-open**, never open-again: the hotkey finds the existing window by
  title (the cockpit page sets `document.title` to `docent cockpit`, plus the
  count of items needing you — so the taskbar entry is readable without focusing
  it) instead of stacking duplicates.
- Parks the window on a **named virtual desktop** (`-Desktop`, default
  `cockpit`) and switches there on focus. It uses the same
  [MScholtes VirtualDesktop](https://github.com/MScholtes/PSVirtualDesktop)
  module wsm requires, and addresses the desktop **by name**, so wsm reuses this
  desktop rather than creating a rival one. Unlike wsm, this module is optional
  here: without it the script still works and simply leaves placement to Windows.

Note that wsm's own `POST /open-url` is deliberately *not* used for this: it
opens a plain `--new-window` browser window as a companion to a *Cursor
workspace* desktop, and then hands focus back to that workspace's editor
window — the opposite of a standalone app window that owns its desktop.
