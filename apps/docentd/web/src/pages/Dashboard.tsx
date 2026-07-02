import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties } from "react";
import { useNavigate } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { fetchDashboard, launchWorkItem } from "../lib/api";
import { focusSession } from "../lib/wsm";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type {
  Dashboard as DashboardData,
  DashboardGroup,
  DashboardSession,
  DashboardPR,
  DashboardTicket,
} from "../lib/types";

const POLL_MS = 5000;

const STATUS_LABELS: Record<string, string> = {
  active: "active",
  approved: "approved",
  started: "started",
  "awaiting-response": "awaiting",
  assigned: "assigned",
};

function Stat({ value, label }: { value: number | string; label: string }) {
  return (
    <span>
      <b>{String(value)}</b>
      {label ? " " + label : null}
    </span>
  );
}

function LaunchButton({ workKey }: { workKey: string }) {
  return (
    <button
      className="launch-btn"
      type="button"
      title="Open editor for this work item"
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        void launchWorkItem(workKey);
      }}
    >
      open
    </button>
  );
}

function TicketLinks({
  tickets,
  jiraUrl,
  primaryTicket,
}: {
  tickets?: DashboardTicket[];
  jiraUrl?: string;
  primaryTicket?: string;
}) {
  const list = tickets ?? [];
  if (list.length === 0) {
    return (
      <span className="ticket-links">
        {primaryTicket ? (
          jiraUrl ? (
            <a
              className="ticket link"
              href={jiraUrl}
              target="_blank"
              rel="noopener"
              onClick={(e) => e.stopPropagation()}
            >
              {primaryTicket}
            </a>
          ) : (
            <span className="ticket">{primaryTicket}</span>
          )
        ) : (
          <span className="ticket untracked">no ticket</span>
        )}
      </span>
    );
  }
  return (
    <span className="ticket-links">
      {list.map((tk, i) => {
        const label = tk.key || tk.title || "ticket";
        return (
          <span key={i}>
            {i > 0 ? " " : null}
            {tk.url ? (
              <a
                className="ticket link"
                href={tk.url}
                target="_blank"
                rel="noopener"
                title={tk.status}
                onClick={(e) => e.stopPropagation()}
              >
                {label}
              </a>
            ) : (
              <span className="ticket" title={tk.status}>
                {label}
              </span>
            )}
          </span>
        );
      })}
    </span>
  );
}

function SessionRow({ s, onReload }: { s: DashboardSession; onReload: () => void }) {
  const clickable = s.live;
  return (
    <div
      className={"row session" + (clickable ? " clickable" : "")}
      title={clickable ? "Focus this window" : undefined}
      onClick={
        clickable
          ? () => void focusSession(s.name, s.host).then(() => window.setTimeout(onReload, 400))
          : undefined
      }
    >
      <span className={"live" + (s.live ? " on" : "")} />
      <span className="name">
        {s.color ? <span className="csw" style={{ background: s.color }} /> : null}
        {s.name}
      </span>
      {s.host ? <span className="chip">{s.host}</span> : null}
      <span className="spacer" />
      {s.needsFollowup ? (
        <span className="pill followup">needs follow-up</span>
      ) : s.status === "working" ? (
        <span className="pill working">working</span>
      ) : (
        <span className="pill status">{s.live ? "idle" : "closed"}</span>
      )}
      {s.lastActivity ? <span className="meta">{timeAgo(s.lastActivity)}</span> : null}
    </div>
  );
}

function PrRow({ pr }: { pr: DashboardPR }) {
  return (
    <a className="row pr clickable" href={pr.url || "#"} target="_blank" rel="noopener">
      <span className="num">#{pr.prNumber}</span>
      <span className="name">{pr.title || "(untitled PR)"}</span>
      {pr.repo ? <span className="chip">{pr.repo}</span> : null}
      <span className="spacer" />
      {pr.draft ? <span className="pill draft">draft</span> : null}
      <span className={"pill state " + (pr.state || "").toLowerCase()}>{pr.state || "open"}</span>
    </a>
  );
}

function Group({
  g,
  onOpen,
  onReload,
}: {
  g: DashboardGroup;
  onOpen: (key: string) => void;
  onReload: () => void;
}) {
  // React's CSSProperties type doesn't include CSS custom properties, so cast.
  const style = g.color ? ({ "--g-color": g.color } as CSSProperties) : undefined;
  return (
    <div className={"group" + (g.needsFollowup ? " followup" : "")} style={style}>
      <div
        className="group-head clickable"
        title="Open work-item details"
        onClick={() => onOpen(g.key || g.ticket || "")}
      >
        <span className="swatch" />
        {g.branch ? (
          <>
            <span className="branch">{g.branch}</span>
            {g.repo ? <span className="chip">{g.repo}</span> : null}
            <TicketLinks tickets={g.tickets} jiraUrl={g.jiraUrl} primaryTicket={g.ticket} />
          </>
        ) : g.ticket ? (
          g.jiraUrl ? (
            <a
              className="ticket link"
              href={g.jiraUrl}
              target="_blank"
              rel="noopener"
              onClick={(e) => e.stopPropagation()}
            >
              {g.ticket}
            </a>
          ) : (
            <span className="ticket">{g.ticket}</span>
          )
        ) : (
          <span className="ticket untracked">untracked</span>
        )}
        {!g.branch && g.summary ? <span className="summary">{g.summary}</span> : null}
        {g.openPath ? <span className="chip path">{g.openPath}</span> : null}
        {g.jiraStatus ? <span className="pill status">{g.jiraStatus}</span> : null}
        {g.lastActivity ? <span className="meta">{timeAgo(g.lastActivity)}</span> : null}
        {g.status ? <span className={"pill st-" + g.status}>{STATUS_LABELS[g.status] || g.status}</span> : null}
        {g.actionRequired ? (
          <span className="action-dot" title="Action required by you" />
        ) : g.needsFollowup ? (
          <span className="followup-dot" />
        ) : null}
        <LaunchButton workKey={g.key || g.ticket || ""} />
      </div>
      <div className="rows">
        {(g.sessions ?? []).map((s, i) => (
          <SessionRow key={"s" + i} s={s} onReload={onReload} />
        ))}
        {(g.prs ?? []).map((pr, i) => (
          <PrRow key={"p" + i} pr={pr} />
        ))}
      </div>
    </div>
  );
}

export function Dashboard() {
  const navigate = useNavigate();
  const [data, setData] = useState<DashboardData | null>(null);
  const [auto, setAuto] = useState(true);
  const [errText, setErrText] = useState<string | null>(null);
  const lastOk = useRef(false);

  const load = useCallback(async () => {
    try {
      const d = await fetchDashboard();
      lastOk.current = true;
      setErrText(null);
      setData(d);
    } catch (e) {
      const m = errMsg(e);
      if (!lastOk.current) setErrText("Cannot reach docent (" + m + ").");
      toast("refresh failed: " + m, true);
    }
  }, []);

  useEffect(() => {
    document.title = "docent";
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

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

  const groups = data?.groups ?? [];
  const liveCount = groups.reduce(
    (n, g) => n + (g.sessions ?? []).filter((s) => s.live).length,
    0,
  );
  const actionCount = groups.filter((g) => g.actionRequired).length;

  const stats = data ? (
    <>
      <Stat value={liveCount} label="live" />
      <Stat value={groups.length} label="groups" />
      {actionCount ? <Stat value={actionCount} label="need action" /> : null}
      <Stat value={timeAgo(data.generatedAt) || "now"} label="" />
    </>
  ) : null;

  const controls = (
    <>
      <AutoToggle checked={auto} onChange={setAuto} />
      <RefreshButton onClick={() => void load()} />
    </>
  );

  return (
    <Layout mainClass="board" stats={stats} controls={controls}>
      {groups.length === 0 ? (
        <div className="empty">{errText ?? "No sessions, tickets, or PRs yet."}</div>
      ) : (
        groups.map((g) => (
          <Group
            key={g.key}
            g={g}
            onOpen={(key) => navigate("/workitem?key=" + encodeURIComponent(key))}
            onReload={() => void load()}
          />
        ))
      )}
    </Layout>
  );
}
