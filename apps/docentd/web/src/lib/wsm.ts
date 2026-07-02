import { toast } from "./toast";
import { errMsg } from "./format";

// The window manager (wsm) runs on the machine the user sits at. The browser
// talks to it directly on loopback so focus works even when docentd is remote.
// Kept as a constant to preserve the previous dashboard's effective behavior
// (the old meta[name=wsm-url] tag was static, never server-templated).
export const WSM_URL = "http://127.0.0.1:39788";

// focusSession asks the local wsm daemon to focus (or report missing) the
// window for a session. It surfaces the result as a toast.
export async function focusSession(name: string, host?: string): Promise<void> {
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
