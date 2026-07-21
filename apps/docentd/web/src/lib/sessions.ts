import { toast } from "./toast";
import { errMsg } from "./format";
import { docentFetch } from "./auth";
import type { DashboardGroup } from "./types";

// Provider-aware session actions. docent's open_trigger provider decides how
// a session/path click behaves:
//   - "cursor": sync the work item's color (backend) then navigate a cursor://
//     deep link so the local Cursor opens/focuses the folder.
//   - "wsm":    focus the exact window via the local wsm daemon.
//   - none:     no clickable actions (the dashboard hides the session column).

// The window manager (wsm) runs on the machine the user sits at. The browser
// talks to it directly on loopback so focus works even when docentd is remote.
export const WSM_URL = "http://127.0.0.1:39788";

// focusWSMSession asks the local wsm daemon to focus (or report missing) the
// window for a session, surfacing the result as a toast. This is wsm's niche:
// reliable exact-window focus.
export async function focusWSMSession(name: string, host?: string): Promise<void> {
  try {
    const r = await fetch(WSM_URL + "/focus", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, host: host ?? null }),
    });
    const d = (await r.json().catch(() => ({}))) as { ok?: boolean; error?: string };
    if (r.ok && d.ok) toast("focused " + name);
    else if (r.status === 404) toast("no open window for " + name, true);
    else toast("focus failed: " + (d.error ?? r.status), true);
  } catch (e) {
    toast("focus error: " + errMsg(e), true);
  }
}

type OpenResult = { ok?: boolean; deepLink?: string; colorSynced?: boolean; error?: string };

// openViaDeepLink is the cursor-provider path: POST /open lets docentd sync the
// work item's color into its repo .vscode/settings.json (co-located with the
// files on the dev box), then the browser navigates the provider deep link so
// the local Cursor GUI opens/focuses the window with the title-bar color already
// in sync. Navigation proceeds as long as a link is available, even if the color
// sync reported trouble.
export async function openViaDeepLink(group: Pick<DashboardGroup, "key" | "deepLink">): Promise<void> {
  let link = group.deepLink ?? "";
  try {
    const r = await docentFetch("/api/workitems/" + encodeURIComponent(group.key) + "/open", {
      method: "POST",
    });
    const d = (await r.json().catch(() => ({}))) as OpenResult;
    if (d.deepLink) link = d.deepLink;
    if (!r.ok && d.error) toast("open: " + d.error, true);
  } catch (e) {
    toast("open error: " + errMsg(e), true);
  }
  if (link) {
    window.location.href = link;
  } else {
    toast("no editor deep link for this work item", true);
  }
}

// activate is the provider-aware entry point for a session/path click. For
// cursor it navigates the deep link (Cursor focuses by folder, so the specific
// session is not needed). For wsm it focuses the exact window.
export async function activate(
  provider: string | undefined,
  group: Pick<DashboardGroup, "key" | "deepLink">,
  session?: { name: string; host?: string },
): Promise<void> {
  if (provider === "cursor") {
    await openViaDeepLink(group);
    return;
  }
  if (provider === "wsm" && session) {
    await focusWSMSession(session.name, session.host);
  }
}

// SessionLaunchTarget is the subset of a RegistrySession the Sessions page uses
// to open/focus a session's IDE. Unlike activate() it does not require a work
// item: cursor sessions carry their own path-derived deepLink, and wsm sessions
// focus by window name.
export type SessionLaunchTarget = {
  provider?: string;
  workItemKey?: string;
  deepLink?: string;
  name?: string;
  targetHost?: string;
};

// canLaunchSession reports whether launchSession has an action for the session
// under the current provider, so the UI can hide/disable the button otherwise.
export function canLaunchSession(s: SessionLaunchTarget): boolean {
  if (s.provider === "cursor") return !!(s.deepLink || s.workItemKey);
  if (s.provider === "wsm") return !!s.name;
  return false;
}

// launchSession opens/focuses the IDE for a raw registry session. For cursor it
// navigates the session's own deep link (its exact path + host) so Cursor
// reveals the existing window rather than opening a mismatched new one; it only
// falls back to the work item's /open flow when the session itself has no deep
// link. For wsm it focuses the exact window by name.
export async function launchSession(s: SessionLaunchTarget): Promise<void> {
  if (s.provider === "cursor") {
    if (s.deepLink) {
      window.location.href = s.deepLink;
    } else if (s.workItemKey) {
      await openViaDeepLink({ key: s.workItemKey });
    } else {
      toast("no editor deep link for this session", true);
    }
    return;
  }
  if (s.provider === "wsm" && s.name) {
    await focusWSMSession(s.name, s.targetHost);
  }
}
