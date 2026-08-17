import type { JSONSchema } from "monaco-yaml";
import { docentFetch } from "./auth";
import { toast } from "./toast";
import { errMsg } from "./format";
import type {
  Dashboard,
  SignalsView,
  CollectorsView,
  WorkItemDetail,
  ReportMeta,
  ReportJob,
  ReportRequest,
  ReportEvent,
  ConfigFileID,
  ConfigFileView,
  ConfigSaveResult,
  AutomationsView,
  SessionsView,
  Cockpit,
  AgentSession,
  AgentEvent,
  AgentStartRequest,
  AgentMode,
  RepoProject,
  StagedAttachment,
  WorktreeTarget,
} from "./types";

async function getJSON<T>(url: string): Promise<T> {
  const r = await docentFetch(url, { cache: "no-store" });
  if (!r.ok) throw new Error("HTTP " + r.status);
  return (await r.json()) as T;
}

export const fetchDashboard = (): Promise<Dashboard> => getJSON<Dashboard>("/api/workitems");
export const fetchCockpit = (): Promise<Cockpit> => getJSON<Cockpit>("/api/cockpit");
export const fetchSignals = (): Promise<SignalsView> => getJSON<SignalsView>("/api/signals");
export const fetchCollectors = (): Promise<CollectorsView> => getJSON<CollectorsView>("/api/collectors");
export const fetchSessions = (): Promise<SessionsView> => getJSON<SessionsView>("/api/sessions");

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

// Report API: fetch the form metadata, start an async generation job, then
// either stream live progress via SSE or poll the job snapshot. Generation
// runs in a docentd goroutine, so startReport returns quickly with a job id.
export const fetchReportMeta = (): Promise<ReportMeta> => getJSON<ReportMeta>("/api/report/meta");

export async function startReport(req: ReportRequest): Promise<string> {
  const r = await docentFetch("/api/report", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(req),
  });
  const d = (await r.json().catch(() => ({}))) as { ok?: boolean; id?: string; error?: string };
  if (!r.ok || !d.id) throw new Error(d.error ?? "HTTP " + r.status);
  return d.id;
}

export const fetchReportJob = (id: string): Promise<ReportJob> =>
  getJSON<ReportJob>("/api/report/" + encodeURIComponent(id));

export type StreamReportHandlers = {
  onEvent: (ev: ReportEvent) => void;
  signal?: AbortSignal;
};

/** Consume an SSE feed of JSON `data:` frames. Uses docentFetch so the bearer
 * token rides along (EventSource cannot set Authorization). Resolves when the
 * stream ends (terminal event or connection close). Throws on non-OK HTTP. */
async function streamJSON<T>(
  url: string,
  onEvent: (ev: T) => void,
  signal?: AbortSignal,
): Promise<void> {
  const r = await docentFetch(url, {
    cache: "no-store",
    headers: { Accept: "text/event-stream" },
    signal,
  });
  if (!r.ok) {
    const d = (await r.json().catch(() => ({}))) as { error?: string };
    throw new Error(d.error ?? "HTTP " + r.status);
  }
  if (!r.body) throw new Error("streaming unsupported");

  const reader = r.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    // SSE frames are separated by a blank line.
    for (;;) {
      const sep = buffer.indexOf("\n\n");
      if (sep < 0) break;
      const frame = buffer.slice(0, sep);
      buffer = buffer.slice(sep + 2);
      for (const line of frame.split("\n")) {
        if (!line.startsWith("data:")) continue;
        const raw = line.slice(5).trimStart();
        if (!raw) continue;
        try {
          onEvent(JSON.parse(raw) as T);
        } catch {
          /* ignore malformed frames */
        }
      }
    }
  }
}

export const streamReport = (id: string, h: StreamReportHandlers): Promise<void> =>
  streamJSON<ReportEvent>("/api/report/" + encodeURIComponent(id) + "/stream", h.onEvent, h.signal);

// Agent sessions. Starting one provisions a worktree, which the first time a
// repository is seen means cloning it, so startAgent can take a while; the turn
// itself runs in the background and is watched through streamAgent.

export const fetchAgents = (): Promise<AgentSession[]> =>
  getJSON<{ sessions: AgentSession[] }>("/api/agents").then((r) => r.sessions ?? []);

export const fetchProjects = (): Promise<RepoProject[]> =>
  getJSON<{ projects: RepoProject[] }>("/api/projects").then((r) => r.projects ?? []);

/** A refusal the user can overrule, e.g. an editor's own agent already working
 * in the worktree. `conflict` is what the UI keys off: matching on message text
 * to decide whether to offer an override would break the first time the wording
 * changed. */
export class AgentConflictError extends Error {
  readonly conflict: string;
  constructor(message: string, conflict: string) {
    super(message);
    this.name = "AgentConflictError";
    this.conflict = conflict;
  }
}

async function agentPost<T>(url: string, body?: unknown): Promise<T> {
  const r = await docentFetch(url, {
    method: "POST",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const d = (await r.json().catch(() => ({}))) as {
    ok?: boolean;
    error?: string;
    conflict?: string;
  } & T;
  if (!r.ok || !d.ok) {
    const msg = d.error ?? "HTTP " + r.status;
    if (d.conflict) throw new AgentConflictError(msg, d.conflict);
    throw new Error(msg);
  }
  return d;
}

async function agentPatch<T>(url: string, body: unknown): Promise<T> {
  const r = await docentFetch(url, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  const d = (await r.json().catch(() => ({}))) as {
    ok?: boolean;
    error?: string;
    conflict?: string;
  } & T;
  if (!r.ok || !d.ok) {
    const msg = d.error ?? "HTTP " + r.status;
    if (d.conflict) throw new AgentConflictError(msg, d.conflict);
    throw new Error(msg);
  }
  return d;
}

export const fetchWorktreeTargets = (repo: string, branch: string): Promise<WorktreeTarget[]> =>
  getJSON<{ targets: WorktreeTarget[] }>(
    "/api/worktree-targets?repo=" + encodeURIComponent(repo) + "&branch=" + encodeURIComponent(branch),
  ).then((r) => r.targets ?? []);

export async function startAgent(req: AgentStartRequest): Promise<AgentSession> {
  const d = await agentPost<{ session: AgentSession }>("/api/agents", req);
  return d.session;
}

export async function uploadAgentAttachment(file: File): Promise<StagedAttachment> {
  const form = new FormData();
  form.append("file", file, file.name);
  const r = await docentFetch("/api/agents/attachments", { method: "POST", body: form });
  const d = (await r.json().catch(() => ({}))) as {
    ok?: boolean;
    error?: string;
    attachment?: StagedAttachment;
  };
  if (!r.ok || !d.ok || !d.attachment) throw new Error(d.error ?? "HTTP " + r.status);
  return d.attachment;
}

/** URL for a session attachment served by docentd (thumbnails in the transcript). */
export function agentAttachmentUrl(sessionId: string, name: string): string {
  return (
    "/api/agents/" +
    encodeURIComponent(sessionId) +
    "/attachments/" +
    encodeURIComponent(name)
  );
}

export const sendAgentTurn = (
  id: string,
  prompt: string,
  force = false,
  attachmentIds: string[] = [],
): Promise<unknown> =>
  agentPost(`/api/agents/${encodeURIComponent(id)}/turn`, { prompt, force, attachmentIds });

export async function setAgentMode(id: string, mode: AgentMode): Promise<AgentSession> {
  const d = await agentPatch<{ session: AgentSession }>(`/api/agents/${encodeURIComponent(id)}`, { mode });
  return d.session;
}

export const stopAgent = (id: string): Promise<unknown> =>
  agentPost(`/api/agents/${encodeURIComponent(id)}/stop`);

export async function deleteAgent(id: string): Promise<void> {
  const r = await docentFetch("/api/agents/" + encodeURIComponent(id), { method: "DELETE" });
  const d = (await r.json().catch(() => ({}))) as { ok?: boolean; error?: string };
  if (!r.ok || !d.ok) throw new Error(d.error ?? "HTTP " + r.status);
}

export type PromoteResult = {
  session: AgentSession;
  handoffPath: string;
  dir: string;
  deepLink?: string;
  sshHost?: string;
  prompt: string;
};

/** Stop the agent and write HANDOFF.md into its worktree. The daemon does not
 * open anything: the editor and its chat box live on the user's machine, so
 * that half is the browser's. */
export const promoteAgent = (id: string): Promise<PromoteResult> =>
  agentPost<PromoteResult>(`/api/agents/${encodeURIComponent(id)}/promote`);

/** Stream one session's transcript. The daemon replays the whole transcript
 * before live events, so a late subscriber sees the full conversation, and the
 * stream stays open across turns until the caller aborts. */
export const streamAgent = (
  id: string,
  onEvent: (ev: AgentEvent) => void,
  signal?: AbortSignal,
): Promise<void> =>
  streamJSON<AgentEvent>("/api/agents/" + encodeURIComponent(id) + "/events", onEvent, signal);

// Settings API: fetch every editable docent config file, then validate/save
// one at a time. saveConfigFile and validateConfigFile never throw on
// validation failure — the returned ConfigSaveResult carries ok/problems so
// the Settings page can render inline errors instead of a toast.

export const fetchConfigFiles = (): Promise<ConfigFileView[]> =>
  getJSON<{ files: ConfigFileView[] }>("/api/config").then((r) => r.files);

async function putConfig(url: string, content: string): Promise<ConfigSaveResult> {
  const r = await docentFetch(url, {
    method: url.endsWith("/validate") ? "POST" : "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ content }),
  });
  const d = (await r.json().catch(() => ({}))) as ConfigSaveResult;
  if (r.ok) return { ok: true };
  return { ok: false, problems: d.problems, error: d.error };
}

export const saveConfigFile = (id: ConfigFileID, content: string): Promise<ConfigSaveResult> =>
  putConfig("/api/config/" + encodeURIComponent(id), content);

export const validateConfigFile = (id: ConfigFileID, content: string): Promise<ConfigSaveResult> =>
  putConfig("/api/config/" + encodeURIComponent(id) + "/validate", content);

// fetchConfigSchema returns the JSON Schema for a config file's contents (for
// the Monaco editor's inline validation/completion), or null when the file
// has no schema.
export async function fetchConfigSchema(id: ConfigFileID): Promise<JSONSchema | null> {
  const r = await docentFetch("/api/config/" + encodeURIComponent(id) + "/schema", { cache: "no-store" });
  if (r.status === 404) return null;
  if (!r.ok) throw new Error("HTTP " + r.status);
  return (await r.json()) as JSONSchema;
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

export const fetchAutomations = (): Promise<AutomationsView> =>
  getJSON<AutomationsView>("/api/automations");

// runAutomation fires one rule's actions immediately, bypassing its schedule and
// cooldown (works even for disabled rules). The daemon records a job in the
// history and returns it; we toast the outcome. Throws on transport failure so
// the caller can toast and refresh.
export async function runAutomation(id: string): Promise<void> {
  const r = await docentFetch(`/api/automations/${encodeURIComponent(id)}/run`, {
    method: "POST",
  });
  const d = (await r.json().catch(() => ({}))) as {
    ok?: boolean;
    error?: string;
    job?: { status?: string; message?: string; error?: string };
  };
  if (r.ok && d.ok) toast("ran " + id + (d.job?.message ? ": " + d.job.message : ""));
  else toast("run failed: " + (d.job?.error ?? d.error ?? r.status), true);
}

