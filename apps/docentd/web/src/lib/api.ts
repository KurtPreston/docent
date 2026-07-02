import { docentFetch } from "./auth";
import { toast } from "./toast";
import { errMsg } from "./format";
import type { Dashboard, SignalsView, CollectorsView, WorkItemDetail } from "./types";

async function getJSON<T>(url: string): Promise<T> {
  const r = await docentFetch(url, { cache: "no-store" });
  if (!r.ok) throw new Error("HTTP " + r.status);
  return (await r.json()) as T;
}

export const fetchDashboard = (): Promise<Dashboard> => getJSON<Dashboard>("/sessions");
export const fetchSignals = (): Promise<SignalsView> => getJSON<SignalsView>("/api/signals");
export const fetchCollectors = (): Promise<CollectorsView> => getJSON<CollectorsView>("/api/collectors");

// fetchWorkItem returns null on a 404 (unknown key) so callers can render a
// friendly "not found" state distinct from a transport error.
export async function fetchWorkItem(key: string): Promise<WorkItemDetail | null> {
  const r = await docentFetch("/api/workitems/" + encodeURIComponent(key), { cache: "no-store" });
  if (r.status === 404) return null;
  if (!r.ok) throw new Error("HTTP " + r.status);
  return (await r.json()) as WorkItemDetail;
}

// launchWorkItem asks docentd to open the editor for a work item and toasts the
// outcome (mirrors the old dashboard's launch button).
export async function launchWorkItem(key: string): Promise<void> {
  if (!key) return;
  try {
    const r = await docentFetch("/api/workitems/" + encodeURIComponent(key) + "/launch", {
      method: "POST",
    });
    const d = (await r.json().catch(() => ({}))) as { ok?: boolean; message?: string; error?: string };
    if (r.ok && d.ok) toast(d.message ?? "opened editor");
    else toast(d.message ?? d.error ?? "launch failed", true);
  } catch (e) {
    toast("launch error: " + errMsg(e), true);
  }
}

// collectUnit force-collects one (directive, mode) unit now, ignoring its poll
// interval. Throws on failure so the caller can toast and refresh.
export async function collectUnit(directive: string, mode: string): Promise<void> {
  const r = await docentFetch(
    `/api/units/${encodeURIComponent(directive)}/${encodeURIComponent(mode)}/collect`,
    { method: "POST" },
  );
  const d = (await r.json().catch(() => ({}))) as { ok?: boolean; error?: string };
  if (r.ok && d.ok) toast("collected " + directive + "/" + mode);
  else toast("collect failed: " + (d.error ?? r.status), true);
}
