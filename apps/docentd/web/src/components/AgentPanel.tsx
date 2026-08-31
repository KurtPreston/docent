import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import {
  AgentConflictError,
  agentAttachmentUrl,
  deleteAgent,
  fetchWorktreeTargets,
  promoteAgent,
  sendAgentTurn,
  setAgentMode,
  startAgent,
  stopAgent,
  streamAgent,
  uploadAgentAttachment,
} from "../lib/api";
import { docentFetch } from "../lib/auth";
import { openWSMPath } from "../lib/sessions";
import { errMsg, timeAgo } from "../lib/format";
import { toast } from "../lib/toast";
import type {
  AgentAttachment,
  AgentEvent,
  AgentMode,
  AgentSession,
  AgentStartRequest,
  RepoProject,
  WorktreeTarget,
} from "../lib/types";

// The agent panel is the cockpit's centerpiece: a lane's agent runs here, in the
// browser, instead of in a Cursor window you have to go find. It shows the live
// transcript and takes the next prompt, so following up is typing in the pane
// you are already looking at.

// "idle" is the store's word for a finished turn, which from the outside means
// the agent is done and waiting on you -- the thing the cockpit exists to
// surface, so it is never labelled as inactivity.
export const AGENT_STATUS_LABEL: Record<string, string> = {
  running: "working",
  idle: "waiting for you",
  failed: "failed",
  stopped: "stopped",
};

// How long to let a branch name settle before asking the daemon where an agent
// could run for it. Each ask is a filesystem walk and a git subprocess.
const TARGET_DEBOUNCE_MS = 300;

// How long to wait after opening the editor before firing the prompt deeplink,
// which lands in whichever window has focus. Long enough for a cold Cursor
// window to appear and claim it.
const PROMOTE_PROMPT_DELAY_MS = 2500;

const AGENT_MODES: { value: AgentMode; label: string }[] = [
  { value: "", label: "agent" },
  { value: "plan", label: "plan" },
  { value: "ask", label: "ask" },
];

const AGENT_PROVIDERS: { value: string; label: string }[] = [
  { value: "claude", label: "Claude" },
  { value: "cursor", label: "cursor-agent" },
];

function normalizeAgentProvider(value?: string): string {
  return value === "cursor" ? "cursor" : "claude";
}

type PendingAttachment = {
  key: string;
  id?: string;
  name: string;
  contentType: string;
  size: number;
  previewUrl?: string;
  uploading: boolean;
  error?: string;
};

function pasteFileName(file: File): string {
  const trimmed = file.name.trim();
  if (trimmed && trimmed !== "image.png" && trimmed !== "blob") return trimmed;
  const ext =
    file.type === "image/jpeg"
      ? "jpg"
      : file.type === "image/webp"
        ? "webp"
        : file.type === "image/gif"
          ? "gif"
          : "png";
  return `pasted-${Date.now()}.${ext}`;
}

function withFilename(file: File, name: string): File {
  if (file.name === name) return file;
  return new File([file], name, { type: file.type || "application/octet-stream" });
}

function filesFromClipboard(e: React.ClipboardEvent): File[] {
  const out: File[] = [];
  for (const item of Array.from(e.clipboardData?.items ?? [])) {
    if (item.kind !== "file") continue;
    const file = item.getAsFile();
    if (file) out.push(withFilename(file, pasteFileName(file)));
  }
  return out;
}

function filesFromDataTransfer(dt: DataTransfer | null): File[] {
  if (!dt?.types.includes("Files")) return [];
  return Array.from(dt.files);
}

// Blocks coalesce the stream into something readable: both CLIs flush prose in
// fragments, so rendering one node per event produces a column of loose words.
type Block =
  | { kind: "prompt"; text: string; attachments?: AgentAttachment[] }
  | { kind: "text" | "thinking" | "plan"; text: string }
  | { kind: "tool"; tool: string; text: string }
  | { kind: "note"; text: string; error?: boolean };

function reduce(blocks: Block[], ev: AgentEvent): Block[] {
  const last = blocks[blocks.length - 1];
  const append = (kind: "prompt" | "text" | "thinking", text: string): Block[] => {
    if (last && last.kind === kind) {
      return [...blocks.slice(0, -1), { kind, text: last.text + text }];
    }
    return [...blocks, { kind, text }];
  };
  switch (ev.kind) {
    case "prompt":
      return [...blocks, { kind: "prompt", text: ev.text ?? "", attachments: ev.attachments }];
    case "text":
      return append("text", ev.text ?? "");
    case "thinking":
      return append("thinking", ev.text ?? "");
    case "plan":
      return [...blocks, { kind: "plan", text: ev.text ?? "" }];
    case "tool":
      return [...blocks, { kind: "tool", tool: ev.tool ?? "tool", text: ev.text ?? "" }];
    case "tool-result":
      // Folded into the invocation above it, so a long file read does not push
      // the conversation off the screen. The tool row shows the call; the result
      // matters mainly when it is an error, which surfaces as text anyway.
      return blocks;
    case "error":
      return [...blocks, { kind: "note", text: ev.error || "agent failed", error: true }];
    case "stopped":
      return [...blocks, { kind: "note", text: "stopped" }];
    case "done": {
      const r = ev.result;
      if (!r) return blocks;
      const bits = [
        r.durationMs ? Math.round(r.durationMs / 1000) + "s" : "",
        r.numTurns ? r.numTurns + " turns" : "",
        r.costUsd ? "$" + r.costUsd.toFixed(2) : "",
      ].filter(Boolean);
      return bits.length ? [...blocks, { kind: "note", text: bits.join(" · ") }] : blocks;
    }
    default:
      // "started" and "status" drive the header, not the transcript.
      return blocks;
  }
}

// Reasoning arrives as a run of titled sections -- "**Weighing the cache**" on
// its own line, then the prose under it -- but it streams as deltas, so the
// structure only exists once the text is reassembled. Recovering it here is
// what lets a collapsed thought say what it was about: a column of rows all
// reading "thinking" hides the one you wanted to read.
const THOUGHT_TITLE = /^\s*\*\*(.+?)\*\*\s*$/;

type Thought = { title?: string; text: string };

function thoughts(text: string): Thought[] {
  const out: Thought[] = [];
  let title: string | undefined;
  let lines: string[] = [];
  const flush = () => {
    const body = lines.join("\n").trim();
    if (title || body) out.push({ title, text: body });
    lines = [];
  };
  for (const line of text.split("\n")) {
    const m = THOUGHT_TITLE.exec(line);
    if (m) {
      flush();
      title = m[1].trim();
    } else {
      lines.push(line);
    }
  }
  flush();
  // A thought whose first delta has not landed yet still gets a row, so the
  // transcript does not flicker one in a moment later.
  return out.length ? out : [{ text: "" }];
}

function AgentMarkdown({ text, className }: { text: string; className: string }) {
  return (
    <div className={className}>
      <ReactMarkdown remarkPlugins={[remarkGfm]}>{text}</ReactMarkdown>
    </div>
  );
}

function AttachmentThumbnail({
  sessionId,
  name,
  contentType,
}: {
  sessionId: string;
  name: string;
  contentType?: string;
}) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    let objectUrl: string | null = null;
    void (async () => {
      try {
        const r = await docentFetch(agentAttachmentUrl(sessionId, name));
        if (!r.ok || !live) return;
        const blob = await r.blob();
        if (!live) return;
        objectUrl = URL.createObjectURL(blob);
        setUrl(objectUrl);
      } catch {
        /* thumbnail is optional */
      }
    })();
    return () => {
      live = false;
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [sessionId, name]);
  if (url && (contentType?.startsWith("image/") ?? true)) {
    return <img className="ag-attach-thumb" src={url} alt={name} />;
  }
  return <span className="ag-attach-chip-name">{name}</span>;
}

function PromptAttachments({
  sessionId,
  attachments,
}: {
  sessionId?: string;
  attachments?: AgentAttachment[];
}) {
  if (!attachments?.length) return null;
  return (
    <div className="ag-prompt-attachments">
      {attachments.map((a) => (
        <div key={a.name} className="ag-attach-chip">
          {sessionId ? (
            <AttachmentThumbnail sessionId={sessionId} name={a.name} contentType={a.contentType} />
          ) : (
            <span className="ag-attach-chip-name">{a.name}</span>
          )}
        </div>
      ))}
    </div>
  );
}

function ComposeAttachments({
  items,
  onRemove,
}: {
  items: PendingAttachment[];
  onRemove: (key: string) => void;
}) {
  if (items.length === 0) return null;
  return (
    <div className="ag-compose-attachments">
      {items.map((a) => (
        <div key={a.key} className={"ag-attach-chip" + (a.error ? " error" : "")}>
          {a.previewUrl && a.contentType.startsWith("image/") ? (
            <img className="ag-attach-thumb" src={a.previewUrl} alt={a.name} />
          ) : (
            <span className="ag-attach-chip-name">{a.name}</span>
          )}
          {a.uploading ? <span className="ag-attach-status muted tiny">uploading…</span> : null}
          {a.error ? <span className="ag-attach-status error tiny">{a.error}</span> : null}
          <button
            type="button"
            className="ag-attach-remove"
            aria-label={"Remove " + a.name}
            disabled={a.uploading}
            onClick={() => onRemove(a.key)}
          >
            ×
          </button>
        </div>
      ))}
    </div>
  );
}

function TranscriptBlock({ block, sessionId }: { block: Block; sessionId?: string }) {
  switch (block.kind) {
    case "prompt":
      return (
        <div className="ag-prompt">
          {block.text ? <div>{block.text}</div> : null}
          <PromptAttachments sessionId={sessionId} attachments={block.attachments} />
        </div>
      );
    case "text":
      return <AgentMarkdown text={block.text} className="ag-text" />;
    case "thinking":
      return (
        <>
          {thoughts(block.text).map((t, i) => (
            <details key={i} className="ag-thinking">
              <summary>
                thinking
                {t.title ? <span className="ag-thinking-title">{t.title}</span> : null}
              </summary>
              <div>{t.text}</div>
            </details>
          ))}
        </>
      );
    case "plan":
      return <AgentMarkdown text={block.text} className="ag-plan" />;
    case "tool":
      return (
        <div className="ag-tool">
          <span className="ag-tool-name">{block.tool}</span>
          {block.text ? <span className="ag-tool-arg mono tiny">{block.text}</span> : null}
        </div>
      );
    case "note":
      return <div className={"ag-note" + (block.error ? " error" : "")}>{block.text}</div>;
  }
}

// The transcript grows freely and is scrolled by the pane around it, so tailing
// has to drive that ancestor rather than the transcript's own box.
function scroller(el: HTMLElement | null): HTMLElement | null {
  for (let p = el?.parentElement ?? null; p; p = p.parentElement) {
    const overflow = getComputedStyle(p).overflowY;
    if (overflow === "auto" || overflow === "scroll") return p;
  }
  return null;
}

function Transcript({
  blocks,
  busy,
  sessionId,
}: {
  blocks: Block[];
  busy: boolean;
  sessionId?: string;
}) {
  const box = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  // Follow the tail only while the user is already at the bottom; scrolling up
  // to read something during a long run should not be yanked back.
  useEffect(() => {
    const el = scroller(box.current);
    if (!el) return;
    const onScroll = () => {
      pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, []);

  // A different lane arrives with the previous one's scroll position and pin
  // state, neither of which says anything about this transcript.
  useEffect(() => {
    pinned.current = true;
  }, [sessionId]);

  useEffect(() => {
    const el = scroller(box.current);
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [blocks]);

  return (
    <div className="ag-transcript" ref={box}>
      {blocks.length === 0 ? (
        <div className="muted small">{busy ? "Starting…" : "No transcript yet."}</div>
      ) : (
        blocks.map((b, i) => <TranscriptBlock key={i} block={b} sessionId={sessionId} />)
      )}
    </div>
  );
}

export type AgentPanelProps = {
  /** The session for this lane, when one exists. */
  session?: AgentSession;
  /** Where a new session runs; the branch is resolved to a worktree. */
  start: Omit<AgentStartRequest, "prompt">;
  /** Default agent CLI from ai.provider when no session exists yet. */
  defaultProvider?: string;
  /** Repositories to choose from when the lane has no branch yet (a ticket
   * nobody has started). Omitted once the target is already known. */
  projects?: RepoProject[];
  /** Branch to propose for that case, e.g. derived from the ticket key. */
  suggestBranch?: string;
  /** Prefilled first prompt, e.g. seeded from an inbox item. */
  seed?: string;
  /** Called after a session is created or deleted, to refresh the lane list. */
  onChanged: () => void;
};

export function AgentPanel({
  session,
  start,
  defaultProvider,
  projects,
  suggestBranch,
  seed,
  onChanged,
}: AgentPanelProps) {
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [draft, setDraft] = useState("");
  const [attachments, setAttachments] = useState<PendingAttachment[]>([]);
  const [dragging, setDragging] = useState(false);
  const [busy, setBusy] = useState(false);
  const dragDepth = useRef(0);
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const attachmentsRef = useRef(attachments);
  attachmentsRef.current = attachments;
  // Only used when the lane has no worktree yet: the agent needs a repository
  // and a branch before it has anywhere to run.
  const [repo, setRepo] = useState(start.repo ?? "");
  const [branch, setBranch] = useState(start.branch ?? suggestBranch ?? "");
  // status is tracked locally so the panel reacts to the stream immediately,
  // rather than at the next five-second poll.
  const [status, setStatus] = useState(session?.status);
  // The reason the daemon refused this send, when it was something the user can
  // overrule -- an agent already working in the worktree, or a branch that has
  // forked from the local copy.
  const [conflict, setConflict] = useState("");
  // Where a new session will run. The placements depend on what is on disk for
  // this repo and branch, so they are fetched rather than assumed.
  const [targets, setTargets] = useState<WorktreeTarget[]>([]);
  const [target, setTarget] = useState("");
  const [mode, setMode] = useState<AgentMode>("");
  const [provider, setProvider] = useState(() =>
    normalizeAgentProvider(session?.provider ?? start.provider ?? defaultProvider),
  );

  const id = session?.id;
  const isCursor = normalizeAgentProvider(session?.provider ?? provider) === "cursor";
  // A lane that already knows its worktree does not ask; one that does not
  // collects the target inline rather than sending the user elsewhere.
  const needsTarget = !session && !start.branch && !start.openPath;

  useEffect(() => setStatus(session?.status), [session?.status]);

  useEffect(() => {
    setMode(session?.mode ?? "");
  }, [session?.mode, session?.id]);

  useEffect(() => {
    if (session?.provider) {
      setProvider(normalizeAgentProvider(session.provider));
    } else {
      setProvider(normalizeAgentProvider(start.provider ?? defaultProvider));
    }
  }, [session?.id, session?.provider, start.provider, defaultProvider]);

  useEffect(() => {
    setRepo(start.repo ?? "");
    setBranch(start.branch ?? suggestBranch ?? "");
  }, [start.repo, start.branch, suggestBranch]);

  // Recomputed as the branch is typed, because two of the four placements are
  // named after a directory that does not exist yet, and a label naming the
  // wrong path is worse than none. Debounced so a keystroke is not a subprocess.
  const wantRepo = repo.trim();
  const wantBranch = branch.trim();
  const placing = !session && wantRepo !== "" && wantBranch !== "";
  useEffect(() => {
    if (!placing) {
      setTargets([]);
      return;
    }
    let live = true;
    const timer = window.setTimeout(() => {
      void fetchWorktreeTargets(wantRepo, wantBranch)
        .then((list) => {
          if (!live) return;
          setTargets(list);
          // Re-defaulted on every change rather than remembered: a placement
          // that quietly persisted into a start nobody thought about is exactly
          // the surprise the picker exists to prevent.
          setTarget(list.find((t) => t.default && !t.disabled)?.kind ?? "");
        })
        .catch(() => live && setTargets([]));
    }, TARGET_DEBOUNCE_MS);
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [placing, wantRepo, wantBranch]);

  // A seed replaces an untouched draft, so clicking a second inbox item does
  // what it looks like, but never discards something typed.
  useEffect(() => {
    if (seed) setDraft((d) => (d.trim() === "" ? seed : d));
  }, [seed]);

  useEffect(() => {
    if (!id) {
      setBlocks([]);
      return;
    }
    setBlocks([]);
    const ac = new AbortController();
    void (async () => {
      try {
        await streamAgent(
          id,
          (ev) => {
            setBlocks((b) => reduce(b, ev));
            if (ev.kind === "status" && ev.text) setStatus(ev.text as AgentSession["status"]);
          },
          ac.signal,
        );
      } catch (e) {
        if (!ac.signal.aborted) toast("transcript stream failed: " + errMsg(e), true);
      }
    })();
    return () => ac.abort();
  }, [id]);

  const running = status === "running";

  const changeMode = useCallback(
    async (next: AgentMode) => {
      const prev = mode;
      setMode(next);
      if (id) {
        try {
          await setAgentMode(id, next);
        } catch (e) {
          setMode(prev);
          toast(errMsg(e), true);
        }
      }
    },
    [id, mode],
  );

  const targetReady = !needsTarget || (repo.trim() !== "" && branch.trim() !== "");

  const revokePreview = useCallback((item: PendingAttachment) => {
    if (item.previewUrl) URL.revokeObjectURL(item.previewUrl);
  }, []);

  useEffect(
    () => () => {
      attachmentsRef.current.forEach(revokePreview);
    },
    [revokePreview],
  );

  const removeAttachment = useCallback(
    (key: string) => {
      setAttachments((prev) => {
        const item = prev.find((a) => a.key === key);
        if (item) revokePreview(item);
        return prev.filter((a) => a.key !== key);
      });
    },
    [revokePreview],
  );

  const addFiles = useCallback(async (files: File[]) => {
    for (const raw of files) {
      const file = withFilename(raw, pasteFileName(raw));
      const key = crypto.randomUUID();
      const previewUrl = file.type.startsWith("image/") ? URL.createObjectURL(file) : undefined;
      setAttachments((prev) => [
        ...prev,
        {
          key,
          name: file.name,
          contentType: file.type || "application/octet-stream",
          size: file.size,
          previewUrl,
          uploading: true,
        },
      ]);
      try {
        const staged = await uploadAgentAttachment(file);
        setAttachments((prev) =>
          prev.map((a) =>
            a.key === key
              ? { ...a, id: staged.id, name: staged.name, contentType: staged.contentType, uploading: false }
              : a,
          ),
        );
      } catch (e) {
        setAttachments((prev) =>
          prev.map((a) => (a.key === key ? { ...a, uploading: false, error: errMsg(e) } : a)),
        );
      }
    }
  }, []);

  const attachmentIds = useMemo(
    () => attachments.filter((a) => a.id && !a.error).map((a) => a.id as string),
    [attachments],
  );
  const attachmentsUploading = attachments.some((a) => a.uploading);
  const canSend =
    targetReady &&
    !busy &&
    !running &&
    !attachmentsUploading &&
    (draft.trim() !== "" || attachmentIds.length > 0);

  const clearAttachments = useCallback(() => {
    setAttachments((prev) => {
      prev.forEach(revokePreview);
      return [];
    });
  }, [revokePreview]);

  // force carries the user's answer to the conflict note below. It is deliberately
  // not sticky: each send asks again, because whether someone else is editing the
  // worktree is a fact about right now.
  const send = useCallback(
    async (force = false) => {
      const prompt = draft.trim();
      if ((!prompt && attachmentIds.length === 0) || busy || !targetReady || attachmentsUploading) return;
      setBusy(true);
      setConflict("");
      try {
        if (id) {
          await sendAgentTurn(id, prompt, force, attachmentIds);
        } else {
          // Creating the worktree fetches from the remote, so this can take a
          // while; say so rather than leaving a disabled button.
          toast("provisioning the worktree…");
          await startAgent({
            ...start,
            provider,
            repo: repo.trim() || start.repo,
            branch: branch.trim() || start.branch,
            target,
            mode: isCursor ? mode : undefined,
            prompt,
            attachmentIds,
            force,
          });
          onChanged();
        }
        setDraft("");
        clearAttachments();
      } catch (e) {
        // A conflict is shown in place rather than as a toast: it needs an
        // answer, the draft is still sitting there, and a message that vanishes
        // on a timer is the wrong shape for a decision.
        if (e instanceof AgentConflictError) setConflict(e.message);
        else toast(errMsg(e), true);
      } finally {
        setBusy(false);
      }
    },
    [
      attachmentIds,
      attachmentsUploading,
      branch,
      busy,
      clearAttachments,
      draft,
      id,
      isCursor,
      mode,
      onChanged,
      provider,
      repo,
      start,
      target,
      targetReady,
    ],
  );

  // promote is the escape hatch for work that needs hands: it stops the agent,
  // leaves HANDOFF.md in the worktree, opens that folder in an editor window,
  // and pre-fills the chat box pointing at the file.
  //
  // The prompt deeplink cannot be sent, only pre-filled, and it lands in
  // whatever window has focus -- so it is fired after a delay, giving the window
  // opened a moment before time to come up and take focus. That is the honest
  // limit of what Cursor exposes; the alternative is asking the user to paste.
  const promote = useCallback(
    async (sessionID: string) => {
      setBusy(true);
      try {
        const res = await promoteAgent(sessionID);
        onChanged();
        const opened = await openWSMPath(res.dir, res.session.branch || res.dir, res.sshHost);
        if (!opened && res.deepLink) window.location.href = res.deepLink;
        toast("handed off via " + res.handoffPath);
        window.setTimeout(() => {
          window.location.href =
            "cursor://anysphere.cursor-deeplink/prompt?text=" + encodeURIComponent(res.prompt);
        }, PROMOTE_PROMPT_DELAY_MS);
      } catch (e) {
        toast("promote failed: " + errMsg(e), true);
      } finally {
        setBusy(false);
      }
    },
    [onChanged],
  );

  const onPaste = (e: React.ClipboardEvent) => {
    const files = filesFromClipboard(e);
    if (files.length === 0) return;
    e.preventDefault();
    void addFiles(files);
  };

  const onDragEnter = (e: React.DragEvent) => {
    if (!e.dataTransfer.types.includes("Files")) return;
    e.preventDefault();
    dragDepth.current += 1;
    setDragging(true);
  };

  const onDragOver = (e: React.DragEvent) => {
    if (!e.dataTransfer.types.includes("Files")) return;
    e.preventDefault();
  };

  const onDragLeave = (e: React.DragEvent) => {
    if (!e.dataTransfer.types.includes("Files")) return;
    e.preventDefault();
    dragDepth.current -= 1;
    if (dragDepth.current <= 0) {
      dragDepth.current = 0;
      setDragging(false);
    }
  };

  const onDrop = (e: React.DragEvent) => {
    if (!e.dataTransfer.types.includes("Files")) return;
    e.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    const files = filesFromDataTransfer(e.dataTransfer);
    if (files.length) void addFiles(files);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    // Enter sends, Shift+Enter breaks the line: the convention every chat box
    // uses, and prompts here are usually one line.
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  };

  const cost = useMemo(() => {
    const r = session?.lastResult;
    if (!r) return "";
    return [
      r.durationMs ? Math.round(r.durationMs / 1000) + "s" : "",
      r.costUsd ? "$" + r.costUsd.toFixed(2) : "",
    ]
      .filter(Boolean)
      .join(" · ");
  }, [session?.lastResult]);

  return (
    <section className="agent-panel">
      <div className="ag-head">
        <h3>Agent</h3>
        {session ? (
          <>
            <span className={"pill ag-" + (status ?? "idle")}>
              {AGENT_STATUS_LABEL[status ?? "idle"] ?? status}
            </span>
            <select
              className="ag-provider"
              value={normalizeAgentProvider(session?.provider ?? provider)}
              disabled={!!session || running}
              aria-label="agent provider"
              onChange={(e) => setProvider(normalizeAgentProvider(e.target.value))}
            >
              {AGENT_PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
            {session?.mode ? <span className="muted tiny">{session.mode}</span> : null}
            {session?.turns ? <span className="muted tiny">{session.turns + " turns"}</span> : null}
            {cost ? <span className="muted tiny">{cost}</span> : null}
            {session?.updatedAt ? (
              <span className="muted tiny">{timeAgo(session.updatedAt)}</span>
            ) : null}
          </>
        ) : (
          <>
            <span className="muted tiny">not started</span>
            <select
              className="ag-provider"
              value={provider}
              disabled={busy}
              aria-label="agent provider"
              onChange={(e) => setProvider(normalizeAgentProvider(e.target.value))}
            >
              {AGENT_PROVIDERS.map((p) => (
                <option key={p.value} value={p.value}>
                  {p.label}
                </option>
              ))}
            </select>
          </>
        )}
        {isCursor ? (
          <div className="ag-mode" role="group" aria-label="execution mode">
            {AGENT_MODES.map((m) => (
              <button
                key={m.label}
                type="button"
                className={"ag-mode-btn" + (mode === m.value ? " active" : "")}
                disabled={running}
                onClick={() => void changeMode(m.value)}
              >
                {m.label}
              </button>
            ))}
          </div>
        ) : null}
        <span className="grow" />
        {session && running ? (
          <button
            type="button"
            className="mini-btn"
            onClick={() => void stopAgent(session.id).catch((e) => toast(errMsg(e), true))}
          >
            stop
          </button>
        ) : null}
        {session ? (
          <button
            type="button"
            className="mini-btn"
            title="Stop the agent, write HANDOFF.md, and open the worktree in Cursor"
            onClick={() => void promote(session.id)}
          >
            promote to Cursor
          </button>
        ) : null}
        {session && !running ? (
          <button
            type="button"
            className="mini-btn"
            onClick={() =>
              void deleteAgent(session.id)
                .then(onChanged)
                .catch((e) => toast(errMsg(e), true))
            }
          >
            discard
          </button>
        ) : null}
      </div>

      {session?.error ? <div className="ag-note error">{session.error}</div> : null}
      {session?.dir ? <div className="mono tiny muted">{session.dir}</div> : null}

      {needsTarget ? (
        <div className="ag-target">
          <select value={repo} onChange={(e) => setRepo(e.target.value)}>
            <option value="">choose a repository…</option>
            {(projects ?? []).map((p) => (
              <option key={p.repo} value={p.repo}>
                {p.repo}
              </option>
            ))}
          </select>
          <input
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
            placeholder="branch to create"
            spellCheck={false}
          />
        </div>
      ) : null}

      {placing && targets.length > 0 ? (
        <div className="ag-target">
          <select value={target} onChange={(e) => setTarget(e.target.value)}>
            {targets.map((t) => (
              <option key={t.kind} value={t.kind} disabled={!!t.disabled}>
                {t.disabled ? `${t.label} — ${t.disabled}` : t.label}
              </option>
            ))}
          </select>
        </div>
      ) : null}

      {/* No session means nothing to transcribe, and an empty bordered box on
          every lane that has never run an agent is noise on the surface meant to
          be glanced at. It appears the moment a session does. */}
      {session || busy ? <Transcript blocks={blocks} busy={busy} sessionId={id} /> : null}

      {conflict ? (
        <div className="ag-conflict">
          <span>{conflict}</span>
          <span className="grow" />
          <button type="button" className="mini-btn" onClick={() => void send(true)}>
            {session ? "send anyway" : "start anyway"}
          </button>
          <button type="button" className="mini-btn" onClick={() => setConflict("")}>
            not now
          </button>
        </div>
      ) : null}

      <div
        className={"ag-compose-wrap" + (dragging ? " dragging" : "")}
        onDragEnter={onDragEnter}
        onDragOver={onDragOver}
        onDragLeave={onDragLeave}
        onDrop={onDrop}
        onPaste={onPaste}
      >
        <ComposeAttachments items={attachments} onRemove={removeAttachment} />
        <div className="ag-compose">
          <button
            type="button"
            className="mini-btn ag-attach-btn"
            title="Attach a file"
            aria-label="Attach a file"
            disabled={busy || running}
            onClick={() => fileInputRef.current?.click()}
          >
            📎
          </button>
          <input
            ref={fileInputRef}
            type="file"
            multiple
            hidden
            onChange={(e) => {
              const list = e.target.files;
              if (list?.length) void addFiles(Array.from(list));
              e.target.value = "";
            }}
          />
          <textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={onKeyDown}
            rows={3}
            placeholder={
              session
                ? running
                  ? "One turn at a time — wait for this one to finish."
                  : "Reply to the agent…"
                : "Describe the work. A worktree is created for the branch."
            }
            disabled={busy || running}
          />
          <button
            type="button"
            className="mini-btn primary"
            onClick={() => void send()}
            disabled={!canSend}
          >
            {session ? "send" : "start agent"}
          </button>
        </div>
      </div>
    </section>
  );
}
