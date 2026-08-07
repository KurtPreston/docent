import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AgentConflictError,
  deleteAgent,
  promoteAgent,
  sendAgentTurn,
  startAgent,
  stopAgent,
  streamAgent,
} from "../lib/api";
import { openWSMPath } from "../lib/sessions";
import { errMsg, timeAgo } from "../lib/format";
import { toast } from "../lib/toast";
import type { AgentEvent, AgentSession, AgentStartRequest, RepoProject } from "../lib/types";

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

// How long to wait after opening the editor before firing the prompt deeplink,
// which lands in whichever window has focus. Long enough for a cold Cursor
// window to appear and claim it.
const PROMOTE_PROMPT_DELAY_MS = 2500;

// Blocks coalesce the stream into something readable: both CLIs flush prose in
// fragments, so rendering one node per event produces a column of loose words.
type Block =
  | { kind: "prompt" | "text" | "thinking"; text: string }
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
      return [...blocks, { kind: "prompt", text: ev.text ?? "" }];
    case "text":
      return append("text", ev.text ?? "");
    case "thinking":
      return append("thinking", ev.text ?? "");
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

function TranscriptBlock({ block }: { block: Block }) {
  switch (block.kind) {
    case "prompt":
      return <div className="ag-prompt">{block.text}</div>;
    case "text":
      return <div className="ag-text">{block.text}</div>;
    case "thinking":
      return (
        <details className="ag-thinking">
          <summary>thinking</summary>
          <div>{block.text}</div>
        </details>
      );
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

function Transcript({ blocks, busy }: { blocks: Block[]; busy: boolean }) {
  const box = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  // Follow the tail only while the user is already at the bottom; scrolling up
  // to read something during a long run should not be yanked back.
  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };
  useEffect(() => {
    const el = box.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [blocks]);

  return (
    <div className="ag-transcript" ref={box} onScroll={onScroll}>
      {blocks.length === 0 ? (
        <div className="muted small">{busy ? "Starting…" : "No transcript yet."}</div>
      ) : (
        blocks.map((b, i) => <TranscriptBlock key={i} block={b} />)
      )}
    </div>
  );
}

export type AgentPanelProps = {
  /** The session for this lane, when one exists. */
  session?: AgentSession;
  /** Where a new session runs; the branch is resolved to a worktree. */
  start: Omit<AgentStartRequest, "prompt">;
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
  projects,
  suggestBranch,
  seed,
  onChanged,
}: AgentPanelProps) {
  const [blocks, setBlocks] = useState<Block[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  // Only used when the lane has no worktree yet: the agent needs a repository
  // and a branch before it has anywhere to run.
  const [repo, setRepo] = useState(start.repo ?? "");
  const [branch, setBranch] = useState(start.branch ?? suggestBranch ?? "");
  // status is tracked locally so the panel reacts to the stream immediately,
  // rather than at the next five-second poll.
  const [status, setStatus] = useState(session?.status);
  // The reason the daemon refused this send, when it was something the user can
  // overrule -- an agent already working in the worktree.
  const [conflict, setConflict] = useState("");

  const id = session?.id;
  // A lane that already knows its worktree does not ask; one that does not
  // collects the target inline rather than sending the user elsewhere.
  const needsTarget = !session && !start.branch && !start.openPath;

  useEffect(() => setStatus(session?.status), [session?.status]);

  useEffect(() => {
    setRepo(start.repo ?? "");
    setBranch(start.branch ?? suggestBranch ?? "");
  }, [start.repo, start.branch, suggestBranch]);

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

  const targetReady = !needsTarget || (repo.trim() !== "" && branch.trim() !== "");

  // force carries the user's answer to the conflict note below. It is deliberately
  // not sticky: each send asks again, because whether someone else is editing the
  // worktree is a fact about right now.
  const send = useCallback(
    async (force = false) => {
      const prompt = draft.trim();
      if (!prompt || busy || !targetReady) return;
      setBusy(true);
      setConflict("");
      try {
        if (id) {
          await sendAgentTurn(id, prompt, force);
        } else {
          // Creating the worktree fetches from the remote, so this can take a
          // while; say so rather than leaving a disabled button.
          toast("provisioning the worktree…");
          await startAgent({
            ...start,
            repo: repo.trim() || start.repo,
            branch: branch.trim() || start.branch,
            prompt,
            force,
          });
          onChanged();
        }
        setDraft("");
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
    [branch, busy, draft, id, onChanged, repo, start, targetReady],
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
            <span className="muted tiny">
              {session.provider}
              {session.turns ? " · " + session.turns + " turns" : ""}
              {cost ? " · " + cost : ""}
            </span>
            {session.updatedAt ? (
              <span className="muted tiny">{timeAgo(session.updatedAt)}</span>
            ) : null}
          </>
        ) : (
          <span className="muted tiny">not started</span>
        )}
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

      {/* No session means nothing to transcribe, and an empty bordered box on
          every lane that has never run an agent is noise on the surface meant to
          be glanced at. It appears the moment a session does. */}
      {session || busy ? <Transcript blocks={blocks} busy={busy} /> : null}

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

      <div className="ag-compose">
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
          disabled={busy || running || !targetReady || draft.trim() === ""}
        >
          {session ? "send" : "start agent"}
        </button>
      </div>
    </section>
  );
}
