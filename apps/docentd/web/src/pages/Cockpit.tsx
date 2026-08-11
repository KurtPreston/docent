import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { useNavigate } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { AGENT_STATUS_LABEL, AgentPanel } from "../components/AgentPanel";
import { fetchAgents, fetchCockpit, fetchProjects } from "../lib/api";
import { activate, canLaunchSession, launchSession } from "../lib/sessions";
import type { SessionLaunchTarget } from "../lib/sessions";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type {
  AgentSession,
  Attention,
  Cockpit as CockpitData,
  CockpitLane,
  DashboardSession,
  RepoProject,
  InboxItem,
  InboxKind,
} from "../lib/types";

// The cockpit is the "what needs me right now" surface: a rail of lanes on the
// left, one per branch or ticket, and the selected lane's detail plus a
// follow-up inbox on the right. It replaces keeping four Cursor windows in four
// corners of the screen and three browser tabs open beside them.
//
// It deliberately shows less than the dashboard. Every lane here can name why it
// is here, and the assigned-but-unstarted backlog is a separate collapsed list
// rather than lanes, because a 43-item backlog next to 20 real lanes hides them.

const POLL_MS = 5000;

// Lane colors come from the backend (model.ColorForName), which derives a
// branch's color from its name, so a lane in the rail is the same color as the
// editor window it opens.
const ATTENTION_LABEL: Record<Attention, string> = {
  "agent-waiting": "agent done",
  "pr-my-turn": "your turn",
  "ready-to-merge": "ready to merge",
  "review-requested": "review requested",
  "agent-working": "agent working",
  "in-progress": "in progress",
  todo: "to do",
  reviewable: "could review",
};

// The six buckets docent classifies a PR into, from libs/prstatus. Rendered as
// prose because the raw values are enum-shaped.
const BUCKET_LABEL: Record<string, string> = {
  ready_to_merge: "ready to merge",
  failing_validation: "checks failing",
  awaiting_author: "awaiting you",
  awaiting_review: "awaiting review",
  pending_validation: "checks running",
  draft: "draft",
};

// The same buckets read from the other side of the PR. Only one differs, and it
// has to: "awaiting you" is true of your own PR and false of everyone else's.
const REVIEW_BUCKET_LABEL: Record<string, string> = {
  ...BUCKET_LABEL,
  awaiting_author: "awaiting its author",
};

function bucketLabel(bucket: string, reviewable?: boolean): string {
  const labels = reviewable ? REVIEW_BUCKET_LABEL : BUCKET_LABEL;
  return labels[bucket] ?? bucket;
}

const INBOX_LABEL: Record<InboxKind, string> = {
  "agent-waiting": "agent",
  "review-comment": "comment",
  "checks-failing": "checks",
  "changes-requested": "changes",
  "ready-to-merge": "merge",
  "review-requested": "review",
};

// The default action each inbox row offers: what an agent would be asked to do.
// Phrased as the verb rather than "run agent", because the point is the outcome.
const ACTION_LABEL: Record<InboxKind, string> = {
  "agent-waiting": "reply",
  "review-comment": "address it",
  "checks-failing": "fix CI",
  "changes-requested": "address it",
  "ready-to-merge": "merge it",
  "review-requested": "summarize it",
};

function laneName(l: CockpitLane): string {
  return l.branch || l.ticket || l.sessions[0]?.name || l.title || l.key;
}

// Stop words that carry no meaning in a branch slug. Short list on purpose: the
// name is a suggestion the user can edit, not something to be clever about.
const SLUG_SKIP = new Set(["the", "a", "an", "for", "to", "of", "in", "on", "and", "with", "is"]);

// branchForTicket proposes the branch a ticket's worktree would use, matching
// the convention already in use: the lowercased key plus a short slug of the
// summary, e.g. "salsa-12345-contracts-widget-npe".
function branchForTicket(lane: CockpitLane): string {
  if (!lane.ticket) return "";
  const words = (lane.title ?? "")
    .toLowerCase()
    .replace(/[^a-z0-9\s-]/g, " ")
    .split(/[\s-]+/)
    .filter((w) => w.length > 1 && !SLUG_SKIP.has(w))
    .slice(0, 3);
  return [lane.ticket.toLowerCase(), ...words].join("-");
}

// laneHaystack is everything about a lane worth typing to find it. Session names
// are included because a lane whose branch is unknown is named after its window.
function laneHaystack(l: CockpitLane): string {
  return [l.ticket, l.branch, l.title, l.repo, l.jiraStatus, l.key, ...l.sessions.map((s) => s.name)]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

// filterLanes narrows the rail by substring, with whitespace-separated tokens
// ANDed: "salsa merge" means both, and a bare number still finds SALSA-12675
// since each token matches anywhere. Substring rather than prefix because the
// part of a ticket you remember is rarely the front of it.
function filterLanes(lanes: CockpitLane[], query: string): CockpitLane[] {
  const tokens = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (tokens.length === 0) return lanes;
  return lanes.filter((l) => {
    const hay = laneHaystack(l);
    return tokens.every((t) => hay.includes(t));
  });
}

// Rail rows are addressable so the filter box can name the row it has
// highlighted, for screen readers and for scrolling that row into view. Lane
// keys look like "wb:Chip/salsa@salsa-42-fix", so lookups go through
// getElementById rather than a CSS selector.
function rowID(laneKey: string): string {
  return "rail-row-" + laneKey;
}

function Chip({ value, label }: { value: number; label: string }) {
  if (!value) return null;
  return (
    <span>
      <b>{value}</b> {label}
    </span>
  );
}

// active is the filter box's keyboard highlight, which is not the selection:
// the pane keeps showing the selected lane until Enter moves it.
function LaneRow({
  lane,
  agent,
  selected,
  active,
  onSelect,
}: {
  lane: CockpitLane;
  agent?: AgentSession;
  selected: boolean;
  active: boolean;
  onSelect: () => void;
}) {
  const style = lane.color ? ({ "--g-color": lane.color } as CSSProperties) : undefined;
  const live = lane.sessions.some((s) => s.live);
  return (
    <button
      type="button"
      id={rowID(lane.key)}
      className={"lane" + (selected ? " selected" : "") + (active ? " active" : "")}
      style={style}
      onClick={onSelect}
      aria-current={selected}
    >
      <span className="lane-top">
        <span className="swatch" />
        <span className="lane-name">{laneName(lane)}</span>
        {live ? <span className="live on" title="Window is live" /> : null}
      </span>
      <span className="lane-bottom">
        <span className={"pill at-" + lane.attention}>{ATTENTION_LABEL[lane.attention]}</span>
        {/* A hosted agent is docent's own doing, so it is reported here rather
            than through the lane's attention bucket, which describes the world
            outside. */}
        {agent ? (
          <span className={"pill ag-" + agent.status}>
            agent {AGENT_STATUS_LABEL[agent.status] ?? agent.status}
          </span>
        ) : null}
        {lane.lastActivity ? <span className="muted tiny">{timeAgo(lane.lastActivity)}</span> : null}
      </span>
      <span className="lane-reason">{lane.reasons.join(" · ")}</span>
    </button>
  );
}

// promptFor turns an inbox row into the prompt its default action seeds, so
// following up is one click plus a glance rather than copying a URL into a chat
// box. Each kind gets the context an agent would otherwise have to hunt for.
// An agent that is done waiting on you is the one kind with no prompt to seed:
// the reply is whatever you want to say next, and the compose box is right
// there.
type SeedableKind = Exclude<InboxKind, "agent-waiting">;

function promptFor(item: InboxItem & { kind: SeedableKind }): string {
  const where = [item.file, item.line ? "line " + item.line : ""].filter(Boolean).join(", ");
  const pr = item.prNumber ? `PR #${item.prNumber}` : "the pull request";
  switch (item.kind) {
    case "review-comment":
    case "changes-requested":
      return [
        `Address this review comment on ${pr}${where ? " (" + where + ")" : ""}:`,
        item.author ? `From ${item.author}:` : "",
        item.body ? "\n" + item.body + "\n" : "",
        item.url ? `Thread: ${item.url}` : "",
        "Make the change, then reply to the thread explaining what you did.",
      ]
        .filter(Boolean)
        .join("\n");
    case "checks-failing":
      return [
        `CI is failing on ${pr}${item.branch ? " (branch " + item.branch + ")" : ""}.`,
        item.url ? `PR: ${item.url}` : "",
        "Find the failing checks, reproduce the failure locally, fix it, and push.",
      ]
        .filter(Boolean)
        .join("\n");
    case "ready-to-merge":
      return [
        `${pr} is approved and its checks are green.`,
        item.url ? `PR: ${item.url}` : "",
        "Rebase onto the base branch if needed, confirm it is still green, and merge it.",
      ]
        .filter(Boolean)
        .join("\n");
    case "review-requested":
      return [
        `Review ${pr}: ${item.title}`,
        item.url ? `PR: ${item.url}` : "",
        "Read the diff and summarize what it changes, what looks wrong, and what you would ask about.",
        "Do not approve or comment on my behalf.",
      ]
        .filter(Boolean)
        .join("\n");
  }
}

// planPrompt is the default action for a ticket nobody has started. It asks for
// a plan rather than a change: an unstarted ticket is usually unstarted because
// the approach is not obvious, and the plan is the artifact worth reviewing.
// PLAN.md lands in the worktree, so promoting the session to Cursor arrives at a
// checkout with the thinking already in it.
function planPrompt(lane: CockpitLane): string {
  return [
    `Plan the work for ${lane.ticket ?? lane.key}: ${lane.title ?? ""}`.trim(),
    lane.jiraUrl ? `Ticket: ${lane.jiraUrl}` : "",
    "",
    "Investigate the codebase and write PLAN.md at the repo root covering what needs to change,",
    "which files are involved, the order to do it in, and anything ambiguous that needs a decision.",
    "Do not change any other file yet.",
  ]
    .filter((l) => l !== undefined)
    .join("\n");
}

// onOpenLane is omitted when the row is already rendered inside its own lane's
// detail, where a "go to lane" button would be a no-op. onSeed is omitted for
// rows whose lane has nowhere to run an agent.
function InboxRow({
  item,
  onOpenLane,
  onSeed,
}: {
  item: InboxItem;
  onOpenLane?: () => void;
  onSeed?: () => void;
}) {
  const style = item.color ? ({ "--g-color": item.color } as CSSProperties) : undefined;
  const where = [item.file, item.line ? "L" + item.line : ""].filter(Boolean).join(":");
  return (
    <div className="inbox-item" style={style}>
      <span className="swatch" />
      <div className="inbox-body">
        <div className="inbox-head">
          <span className={"pill ik-" + item.kind}>{INBOX_LABEL[item.kind]}</span>
          <span className="inbox-title">{item.title}</span>
          {item.at ? <span className="muted tiny">{timeAgo(item.at)}</span> : null}
        </div>
        {item.body ? <div className="inbox-quote">{item.body}</div> : null}
        <div className="inbox-actions">
          {where ? <span className="mono tiny muted">{where}</span> : null}
          {item.url ? (
            <a className="mini-btn" href={item.url} target="_blank" rel="noreferrer">
              open on GitHub
            </a>
          ) : null}
          {onOpenLane ? (
            <button type="button" className="mini-btn" onClick={onOpenLane}>
              go to lane
            </button>
          ) : null}
          {onSeed ? (
            <button type="button" className="mini-btn primary" onClick={onSeed}>
              {ACTION_LABEL[item.kind]}
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}

// WindowRow is one open editor window, clickable when the provider can reach
// it. It launches the session's own deep link rather than the lane's, so a lane
// with two windows open reveals the one you clicked.
function WindowRow({
  session,
  provider,
  laneKey,
}: {
  session: DashboardSession;
  provider: string;
  laneKey: string;
}) {
  const target: SessionLaunchTarget = {
    provider,
    workItemKey: laneKey,
    deepLink: session.deepLink,
    name: session.name,
    targetHost: session.targetHost,
  };
  // The record outlives the window it describes: the registry holds it for the
  // retention window when no close event arrived. Without a heartbeat the
  // window is gone, so report that rather than its last known activity.
  const status = session.live ? session.status : "closed";
  const body = (
    <>
      <span className={"live" + (session.live ? " on" : "")} />
      <span className="name">{session.name}</span>
      {session.host ? <span className="chip">{session.host}</span> : null}
      <span className={"pill st-" + status}>{status}</span>
      {session.lastActivity ? (
        <span className="muted tiny">{timeAgo(session.lastActivity)}</span>
      ) : null}
    </>
  );
  if (!canLaunchSession(target)) {
    return <div className="detail-row">{body}</div>;
  }
  const verb = session.live ? "Reveal this window" : "Reopen this window";
  return (
    <button
      type="button"
      className="detail-row clickable"
      title={session.path ? verb + " — " + session.path : verb}
      onClick={() => void launchSession(target)}
    >
      {body}
    </button>
  );
}

function LaneDetail({
  lane,
  provider,
  defaultAgentProvider,
  inbox,
  agent,
  projects,
  seed,
  onSeed,
  onReload,
}: {
  lane: CockpitLane;
  provider: string;
  defaultAgentProvider?: string;
  inbox: InboxItem[];
  agent?: AgentSession;
  projects: RepoProject[];
  seed?: string;
  onSeed: (prompt: string) => void;
  onReload: () => void;
}) {
  const style = lane.color ? ({ "--g-color": lane.color } as CSSProperties) : undefined;
  const mine = inbox.filter((i) => i.laneKey === lane.key);
  return (
    <div className="lane-detail" style={style}>
      <div className="detail-head">
        <span className="swatch big" />
        <div className="detail-title">
          <h2>{laneName(lane)}</h2>
          {lane.title && lane.title !== laneName(lane) ? (
            <div className="muted">{lane.title}</div>
          ) : null}
        </div>
        <span className="grow" />
        {lane.jiraUrl && lane.ticket ? (
          <a className="mini-btn" href={lane.jiraUrl} target="_blank" rel="noreferrer">
            {lane.ticket}
          </a>
        ) : null}
        {/* Offered only while there is no session: once one exists the compose
            box below is where prompts go, and a button that overwrites a draft
            would be a trap. */}
        {lane.ticket && !agent ? (
          <button type="button" className="mini-btn" onClick={() => onSeed(planPrompt(lane))}>
            plan it
          </button>
        ) : null}
        {provider && (lane.openAction ?? "none") !== "none" ? (
          <button
            type="button"
            className="mini-btn primary"
            onClick={() => void activate(provider, lane).then(() => window.setTimeout(onReload, 400))}
          >
            {openButtonLabel(lane)}
          </button>
        ) : null}
      </div>

      <div className="detail-reasons">
        {lane.reasons.map((r, i) => (
          <span key={i} className="pill reason">
            {r}
          </span>
        ))}
      </div>

      {lane.openPath ? <div className="mono tiny muted detail-path">{lane.openPath}</div> : null}

      {lane.sessions.length > 0 ? (
        <section>
          <h3>Windows</h3>
          {/* A row is the affordance for the window it names: a lane can have
              several, and the button above can only reach one of them. */}
          {lane.sessions.map((s, i) => (
            <WindowRow key={s.name + i} session={s} provider={provider} laneKey={lane.key} />
          ))}
        </section>
      ) : null}

      {lane.prs.length > 0 ? (
        <section>
          <h3>Pull requests</h3>
          {lane.prs.map((pr, i) => (
            <div className="detail-row" key={pr.prNumber || i}>
              <a className="pr-link" href={pr.url} target="_blank" rel="noreferrer">
                {pr.prNumber ? <b>#{pr.prNumber}</b> : null} {pr.title || "(untitled)"}
              </a>
              {/* The bucket subsumes both of these -- draft is one of the six
                  buckets, and the review decision is what picks among the rest --
                  so they show only when classification is unavailable. Rendering
                  both puts two verdicts on one row, or literally "draft draft". */}
              {!pr.bucket && pr.draft ? <span className="pill">draft</span> : null}
              {pr.bucket ? (
                <span className={"pill bk-" + pr.bucket}>
                  {bucketLabel(pr.bucket, pr.reviewable)}
                </span>
              ) : null}
              {pr.author ? <span className="muted tiny">by {pr.author}</span> : null}
              {pr.checks ? <span className={"pill checks-" + pr.checks}>{pr.checks}</span> : null}
              {!pr.bucket && pr.reviewDecision ? (
                <span className="pill">{pr.reviewDecision.toLowerCase().replace(/_/g, " ")}</span>
              ) : null}
              {pr.unresolved ? <span className="pill amber">{pr.unresolved} unresolved</span> : null}
            </div>
          ))}
        </section>
      ) : null}

      {mine.length > 0 ? (
        <section>
          <h3>Needs a response</h3>
          {mine.map((item, i) => (
            <InboxRow
              key={i}
              item={item}
              onSeed={isSeedable(item) ? () => onSeed(promptFor(item)) : undefined}
            />
          ))}
        </section>
      ) : null}

      <AgentPanel
        session={agent}
        seed={seed}
        projects={projects}
        defaultProvider={defaultAgentProvider}
        // A lane with no branch is a ticket nobody has started; propose the
        // branch name the worktree would get so starting is one click.
        suggestBranch={branchForTicket(lane)}
        start={{
          repo: lane.repo,
          branch: lane.branch,
          openPath: lane.openPath,
          title: laneName(lane),
        }}
        onChanged={onReload}
      />
    </div>
  );
}

// QueueList is the assigned-but-not-started work, grouped by the project's own
// JIRA status names so "To Do" and "Backlog" separate themselves without docent
// knowing either name. Collapsed by default: it answers "what's next", which is
// a different question from "what needs me now".
//
// forceOpen expands the list while the rail is filtered: a ticket you search for
// is usually one you have not started, so requiring a click to reveal the
// matches would make the filter look like it found nothing.
//
// open is owned by the cockpit rather than here because the filter box's arrow
// keys walk what the rail is showing, and a list that kept its own expanded
// state would be visible but unreachable.
function QueueList({
  queue,
  selected,
  active,
  open,
  forceOpen,
  onToggle,
  onSelect,
}: {
  queue: CockpitLane[];
  selected?: string;
  active?: string | null;
  open: boolean;
  forceOpen?: boolean;
  onToggle: () => void;
  onSelect: (key: string) => void;
}) {
  const groups = useMemo(() => {
    const by = new Map<string, CockpitLane[]>();
    for (const l of queue) {
      const k = l.jiraStatus || "assigned";
      const list = by.get(k);
      if (list) list.push(l);
      else by.set(k, [l]);
    }
    return [...by.entries()];
  }, [queue]);

  if (queue.length === 0) return null;
  return (
    <div className="queue">
      {forceOpen ? (
        <div className="queue-toggle static">Assigned to you ({queue.length} matching)</div>
      ) : (
        <button type="button" className="queue-toggle" onClick={onToggle}>
          {open ? "▾" : "▸"} Assigned to you ({queue.length})
        </button>
      )}
      {open || forceOpen
        ? groups.map(([status, lanes]) => (
            <div className="queue-group" key={status}>
              <div className="queue-status">
                {status} <span className="muted tiny">({lanes.length})</span>
              </div>
              {/* A button, not a JIRA link: selecting the ticket opens it as a
                  lane, which is where an agent can be pointed at it. The ticket
                  link is in the lane detail. */}
              {lanes.map((l) => (
                <button
                  type="button"
                  id={rowID(l.key)}
                  className={
                    "queue-item" +
                    (selected === l.key ? " selected" : "") +
                    (active === l.key ? " active" : "")
                  }
                  key={l.key}
                  onClick={() => onSelect(l.key)}
                >
                  <span className="ticket">{l.ticket || l.key}</span>
                  <span className="muted">{l.title}</span>
                </button>
              ))}
            </div>
          ))
        : null}
    </div>
  );
}

// ReviewList is the pool of PRs the user could review: everything open in a
// followed repo, plus anything assigned to them. Nobody asked for any of it, so
// it sits below the rail with the backlog rather than in it, collapsed by
// default, and answers "what could I pick up" rather than "what needs me".
//
// Grouped by state in the order the backend sorted them, which is review order:
// the PRs still waiting on a reviewer first, the ones already approved last.
function ReviewList({
  queue,
  selected,
  active,
  open,
  forceOpen,
  onToggle,
  onSelect,
}: {
  queue: CockpitLane[];
  selected?: string;
  active?: string | null;
  open: boolean;
  forceOpen?: boolean;
  onToggle: () => void;
  onSelect: (key: string) => void;
}) {
  const groups = useMemo(() => {
    const by = new Map<string, CockpitLane[]>();
    for (const l of queue) {
      const k = l.reviewBucket || "open";
      const list = by.get(k);
      if (list) list.push(l);
      else by.set(k, [l]);
    }
    return [...by.entries()];
  }, [queue]);

  if (queue.length === 0) return null;
  return (
    <div className="queue">
      {forceOpen ? (
        <div className="queue-toggle static">PRs to review ({queue.length} matching)</div>
      ) : (
        <button type="button" className="queue-toggle" onClick={onToggle}>
          {open ? "▾" : "▸"} PRs to review ({queue.length})
        </button>
      )}
      {open || forceOpen
        ? groups.map(([bucket, lanes]) => (
            <div className="queue-group" key={bucket}>
              <div className="queue-status">
                {REVIEW_BUCKET_LABEL[bucket] ?? bucket}{" "}
                <span className="muted tiny">({lanes.length})</span>
              </div>
              {lanes.map((l) => {
                const pr = l.prs.find((p) => p.reviewable) ?? l.prs[0];
                return (
                  <button
                    type="button"
                    id={rowID(l.key)}
                    className={
                      "queue-item" +
                      (selected === l.key ? " selected" : "") +
                      (active === l.key ? " active" : "")
                    }
                    key={l.key}
                    onClick={() => onSelect(l.key)}
                  >
                    {pr?.prNumber ? <span className="ticket">#{pr.prNumber}</span> : null}
                    <span className="muted">{pr?.title || l.title || l.key}</span>
                    {/* Whose it is, which is most of how a reviewer picks. */}
                    {pr?.author ? <span className="who tiny">{pr.author}</span> : null}
                  </button>
                );
              })}
            </div>
          ))
        : null}
    </div>
  );
}

// SourceWarning tells the user when a collector has not reported yet, so an
// empty inbox is never mistaken for "nothing to do". GitHub takes ~20s from a
// cold start, and silently showing no PRs during that window is the same class
// of failure as a reporter posting to a dead endpoint.
function SourceWarning({ data }: { data: CockpitData }) {
  const pending = data.sources.filter((s) => !s.loaded);
  const failed = data.sources.filter((s) => s.error);
  if (pending.length === 0 && failed.length === 0) return null;
  return (
    <div className="source-warning">
      {pending.length > 0 ? (
        <span>Still loading: {pending.map((s) => s.id).join(", ")}. </span>
      ) : null}
      {failed.map((s) => (
        <span key={s.id} className="src-err">
          {s.id}: {s.error}
        </span>
      ))}
    </div>
  );
}

function isSeedable(item: InboxItem): item is InboxItem & { kind: SeedableKind } {
  return item.kind !== "agent-waiting";
}

// In the inbox column a row's action also has to find its lane on screen, since
// the button works by selecting one; selecting a lane that is not rendered would
// silently seed a prompt into whichever lane happened to be first.
function canSeed(
  lanes: CockpitLane[],
  item: InboxItem,
): item is InboxItem & { kind: SeedableKind } {
  return isSeedable(item) && lanes.some((l) => l.key === item.laneKey);
}

// openButtonLabel names what the click will do, because the three outcomes are
// hard to tell apart afterwards: a live window on this checkout is revealed, a
// lane with a checkout but no window gets one, and a branch with no checkout at
// all gets a worktree made for it first.
//
// Between the first two it is liveness that decides, not the presence of a
// session: a record left behind by a window that died without reporting its
// close would otherwise promise to "go to" a window that is not there.
function openButtonLabel(lane: CockpitLane): string {
  if (lane.openAction === "create") return "Create worktree";
  return lane.sessions.some((s) => s.live) ? "Go to window" : "Open in Cursor";
}

// agentKey identifies the worktree a session is working in, which is what a
// lane and a session have in common. Repo-qualified because the same branch name
// in two repos is two different pieces of work.
function agentKey(repo?: string, branch?: string): string {
  if (!branch) return "";
  return (repo ?? "").toLowerCase() + "#" + branch;
}

export function Cockpit() {
  const navigate = useNavigate();
  const [data, setData] = useState<CockpitData | null>(null);
  const [agents, setAgents] = useState<AgentSession[]>([]);
  const [projects, setProjects] = useState<RepoProject[]>([]);
  const [auto, setAuto] = useState(true);
  const [selected, setSelected] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  // The rail's keyboard highlight, held as a lane key rather than a row index so
  // that a poll which reorders the rail leaves it on the same lane. Null until
  // an arrow key is pressed: a ring nobody asked for reads as a second
  // selection.
  const [active, setActive] = useState<string | null>(null);
  // Whether each list under the rail is expanded. Held here rather than in the
  // lists so the arrow keys can walk exactly the rows on screen.
  const [queueOpen, setQueueOpen] = useState(false);
  const [reviewOpen, setReviewOpen] = useState(false);
  // seeds are per-lane so switching lanes and coming back keeps a prompt you
  // were composing, and an inbox click never overwrites another lane's draft.
  const [seeds, setSeeds] = useState<Record<string, string>>({});
  const [errText, setErrText] = useState<string | null>(null);
  const lastOk = useRef(false);
  const filterRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    try {
      const [d, a] = await Promise.all([
        fetchCockpit(),
        // A broken agent setup must not blank the cockpit: the lanes are still
        // worth seeing, and the agent panel reports the reason itself.
        fetchAgents().catch(() => [] as AgentSession[]),
      ]);
      lastOk.current = true;
      setErrText(null);
      setData(d);
      setAgents(a);
    } catch (e) {
      const m = errMsg(e);
      if (!lastOk.current) setErrText("Cannot reach docent (" + m + ").");
      toast("refresh failed: " + m, true);
    }
  }, []);

  // The cockpit is meant to live as a pinned app window, so its title is the
  // one thing visible without focusing it. Carry the actionable count there.
  // The "docent cockpit" prefix is load-bearing: docent-cockpit.ps1 finds the
  // window by matching it.
  useEffect(() => {
    const n = data?.counts?.actionable ?? 0;
    document.title = n > 0 ? `docent cockpit (${n})` : "docent cockpit";
  }, [data]);

  useEffect(() => {
    void load();
  }, [load]);

  // Projects change when a repository is cloned, which is rare enough that once
  // per page load is right and a scan on every poll would be waste.
  useEffect(() => {
    void fetchProjects()
      .then(setProjects)
      .catch(() => setProjects([]));
  }, []);

  useEffect(() => {
    if (!auto) return;
    const id = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(id);
  }, [auto, load]);

  useEffect(() => {
    const onVis = () => {
      if (!document.hidden) void load();
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, [load]);

  const lanes = data?.lanes ?? [];
  const queue = data?.queue ?? [];
  const reviewQueue = data?.reviewQueue ?? [];
  const query = filter.trim();
  const shownLanes = useMemo(() => filterLanes(lanes, query), [lanes, query]);
  const shownQueue = useMemo(() => filterLanes(queue, query), [queue, query]);
  const shownReview = useMemo(() => filterLanes(reviewQueue, query), [reviewQueue, query]);

  // Every row the rail is currently showing, in the order it shows them, which
  // is the order the filter box's arrow keys walk: lanes first, then whichever
  // of the two lists below them is expanded.
  const railRows = useMemo(
    () => [
      ...shownLanes,
      ...(queueOpen || query ? shownQueue : []),
      ...(reviewOpen || query ? shownReview : []),
    ],
    [shownLanes, shownQueue, shownReview, queueOpen, reviewOpen, query],
  );

  // A highlight is dropped rather than moved when its row leaves the rail: one
  // more character typed should not hand the ring to an unrelated lane.
  const activeKey = active && railRows.some((l) => l.key === active) ? active : null;

  // The backlog is selectable too, even though it is not in the rail: picking a
  // ticket to start is the whole point of having it here. Keep the user's
  // selection across polls, but never leave a stale lane selected once it stops
  // needing attention.
  //
  // While filtering, the selection follows the filter, falling through to the
  // backlog: typing a ticket key should land on that ticket, not leave the
  // previous lane in the pane beside a rail that no longer contains it.
  const current =
    shownLanes.find((l) => l.key === selected) ??
    shownQueue.find((l) => l.key === selected) ??
    shownReview.find((l) => l.key === selected) ??
    (query ? (shownLanes[0] ?? shownQueue[0] ?? shownReview[0]) : lanes[0]);

  // Newest session wins when a branch has been worked more than once: an old
  // finished conversation should not shadow the one running now.
  const agentByLane = useMemo(() => {
    const by = new Map<string, AgentSession>();
    for (const a of agents) {
      const k = agentKey(a.repo, a.branch);
      if (!k) continue;
      const prev = by.get(k);
      if (!prev || a.updatedAt > prev.updatedAt) by.set(k, a);
    }
    return by;
  }, [agents]);

  // seedLane sends an inbox row's prompt to its lane and selects it, which is
  // the whole one-click follow-up: the prompt lands in the box, and you either
  // send it or edit it first. The filter is dropped because the inbox is not
  // filtered, so its rows can point at a lane the rail is currently hiding.
  const seedLane = useCallback((laneKey: string, prompt: string) => {
    setFilter("");
    setSelected(laneKey);
    setSeeds((s) => ({ ...s, [laneKey]: prompt }));
  }, []);

  const selectLane = useCallback((laneKey: string) => {
    setFilter("");
    setSelected(laneKey);
  }, []);

  // Keep the highlighted row on screen: the rail scrolls, and a highlight the
  // arrow keys walked off the bottom of it is indistinguishable from none.
  useEffect(() => {
    if (!activeKey) return;
    document.getElementById(rowID(activeKey))?.scrollIntoView({ block: "nearest" });
  }, [activeKey]);

  // The filter box drives the rail, so finding a ticket and opening it never
  // needs the mouse: type enough of it to narrow the rail, walk the matches with
  // the arrow keys, and load the one you want with Enter.
  //
  // Enter loads rather than the highlight doing it, because a lane's detail
  // mounts its agent panel and opens a transcript stream; arrowing past twenty
  // lanes would open twenty of them.
  const onFilterKey = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      setFilter("");
      setActive(null);
      e.currentTarget.blur();
    } else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      if (railRows.length === 0) return;
      // Otherwise the caret jumps to the end of the query on every press.
      e.preventDefault();
      // With nothing highlighted yet, walking starts from whatever the pane is
      // showing, so the first press steps off the current lane rather than
      // jumping back to the top of the rail.
      const step = e.key === "ArrowDown" ? 1 : -1;
      const from = railRows.findIndex((l) => l.key === (activeKey ?? current?.key));
      // Clamped rather than wrapped: the rail is a list you scroll, not a dial.
      const next = from < 0 ? (step > 0 ? 0 : railRows.length - 1) : from + step;
      if (next >= 0 && next < railRows.length) setActive(railRows[next].key);
    } else if (e.key === "Enter") {
      // Without a highlight this pins whatever the filter already landed on,
      // which is what keeps it selected once the query is cleared.
      const key = activeKey ?? current?.key;
      if (key) setSelected(key);
    }
  };

  // "/" focuses the filter, the convention everywhere else it exists. Guarded on
  // the event target, since most typing on this page happens in the agent's
  // compose box and a shortcut that eats a slash mid-prompt is worse than none.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.ctrlKey || e.metaKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.isContentEditable || /^(input|textarea|select)$/i.test(t.tagName))) return;
      e.preventDefault();
      filterRef.current?.focus();
      filterRef.current?.select();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // What an empty rail says. An unreachable daemon outranks the filter, since
  // "nothing matches" would blame the query for a rail that is empty because
  // there is no data at all; and a filter that matched only backlog tickets says
  // nothing, because those rows below are the answer.
  const railMessage =
    errText ??
    (query
      ? shownQueue.length > 0 || shownReview.length > 0
        ? null
        : `Nothing matches “${query}”.`
      : "Nothing needs you right now.");

  const counts = data?.counts;
  const stats = data ? (
    <>
      <Chip value={counts?.actionable ?? 0} label="need you" />
      <Chip value={counts?.agentWorking ?? 0} label="working" />
      <Chip value={lanes.length} label="lanes" />
      <span className="muted">of {data.total}</span>
    </>
  ) : null;

  const controls = (
    <>
      <AutoToggle checked={auto} onChange={setAuto} />
      <RefreshButton onClick={() => void load()} />
    </>
  );

  return (
    <Layout mainClass="cockpit" stats={stats} controls={controls}>
      {data ? <SourceWarning data={data} /> : null}
      <div className="cockpit-grid">
        <aside className="rail">
          <div className="rail-filter">
            {/* Focused on arrival, because the cockpit is usually opened by its
                hotkey to get to one particular ticket, and typing its number
                should not need a click first. */}
            <input
              ref={filterRef}
              autoFocus
              type="search"
              value={filter}
              placeholder="Filter lanes and tickets  /"
              aria-label="Filter lanes and tickets"
              aria-activedescendant={activeKey ? rowID(activeKey) : undefined}
              onChange={(e) => setFilter(e.target.value)}
              // The highlight belongs to the keyboard: leaving the box for the
              // pane would otherwise leave a ring behind that nothing moves.
              onBlur={() => setActive(null)}
              onKeyDown={onFilterKey}
            />
          </div>
          {shownLanes.length > 0 ? (
            shownLanes.map((l) => (
              <LaneRow
                key={l.key}
                lane={l}
                agent={agentByLane.get(agentKey(l.repo, l.branch))}
                selected={current?.key === l.key}
                active={activeKey === l.key}
                onSelect={() => setSelected(l.key)}
              />
            ))
          ) : railMessage ? (
            <div className="empty small">{railMessage}</div>
          ) : null}
          <QueueList
            queue={shownQueue}
            selected={current?.key}
            active={activeKey}
            open={queueOpen}
            forceOpen={query !== ""}
            onToggle={() => setQueueOpen((v) => !v)}
            onSelect={setSelected}
          />
          <ReviewList
            queue={shownReview}
            selected={current?.key}
            active={activeKey}
            open={reviewOpen}
            forceOpen={query !== ""}
            onToggle={() => setReviewOpen((v) => !v)}
            onSelect={setSelected}
          />
        </aside>

        <div className="pane">
          {current ? (
            <LaneDetail
              lane={current}
              provider={data?.provider ?? ""}
              defaultAgentProvider={data?.agentProvider}
              inbox={data?.inbox ?? []}
              agent={agentByLane.get(agentKey(current.repo, current.branch))}
              projects={projects}
              seed={seeds[current.key]}
              onSeed={(prompt) => seedLane(current.key, prompt)}
              onReload={() => void load()}
            />
          ) : (
            <div className="empty">Select a lane.</div>
          )}
        </div>

        <aside className="inbox">
          <div className="section-head">
            <span className="title">Follow-up</span>
            <span className="grow" />
            <span className="muted tiny">{data?.inbox.length ?? 0}</span>
          </div>
          {(data?.inbox ?? []).length === 0 ? (
            <div className="empty small">Inbox clear.</div>
          ) : (
            (data?.inbox ?? []).map((item, i) => (
              <InboxRow
                key={i}
                item={item}
                onOpenLane={() => selectLane(item.laneKey)}
                onSeed={
                  canSeed(lanes, item) ? () => seedLane(item.laneKey, promptFor(item)) : undefined
                }
              />
            ))
          )}
          <button type="button" className="mini-btn wide" onClick={() => navigate("/dashboard")}>
            Full dashboard ({data?.total ?? 0})
          </button>
        </aside>
      </div>
    </Layout>
  );
}
