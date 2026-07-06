import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
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

// Compact single-line rendering of a session, used inside the dashboard's
// Sessions cell (one shown by default, rest revealed via "+N more").
function SessionMini({ s, onReload }: { s: DashboardSession; onReload: () => void }) {
  const clickable = s.live;
  return (
    <span
      className={"mini session" + (clickable ? " clickable" : "")}
      title={clickable ? "Focus this window" : undefined}
      onClick={
        clickable
          ? (e) => {
              e.stopPropagation();
              void focusSession(s.name, s.host).then(() => window.setTimeout(onReload, 400));
            }
          : undefined
      }
    >
      <span className={"live" + (s.live ? " on" : "")} />
      <span className="name">
        {s.color ? <span className="csw" style={{ background: s.color }} /> : null}
        {s.name}
      </span>
      {s.host ? <span className="chip">{s.host}</span> : null}
      {s.needsFollowup ? (
        <span className="pill followup">needs follow-up</span>
      ) : s.status === "working" ? (
        <span className="pill working">working</span>
      ) : (
        <span className="pill status">{s.live ? "idle" : "closed"}</span>
      )}
    </span>
  );
}

function PrMini({ pr }: { pr: DashboardPR }) {
  return (
    <a
      className="mini pr clickable"
      href={pr.url || "#"}
      target="_blank"
      rel="noopener"
      onClick={(e) => e.stopPropagation()}
    >
      <span className="num">#{pr.prNumber}</span>
      <span className="name">{pr.title || "(untitled PR)"}</span>
      {pr.draft ? <span className="pill draft">draft</span> : null}
      <span className={"pill state " + (pr.state || "").toLowerCase()}>{pr.state || "open"}</span>
    </a>
  );
}

// Shows the first session/PR inline; a "+N more" toggle expands the cell to
// stack the rest in place (no navigation, no sub-rows).
function ExpandableCell<T>({
  items,
  renderItem,
  itemKey,
}: {
  items: T[];
  renderItem: (item: T) => ReactNode;
  itemKey: (item: T, i: number) => string | number;
}) {
  const [expanded, setExpanded] = useState(false);
  if (items.length === 0) return <span className="muted">—</span>;
  const shown = expanded ? items : items.slice(0, 1);
  return (
    <div className="cell-stack">
      {shown.map((item, i) => (
        <div key={itemKey(item, i)}>{renderItem(item)}</div>
      ))}
      {items.length > 1 ? (
        <button
          type="button"
          className="more-toggle"
          onClick={(e) => {
            e.stopPropagation();
            setExpanded((v) => !v);
          }}
        >
          {expanded ? "show less" : `+${items.length - 1} more`}
        </button>
      ) : null}
    </div>
  );
}

function WorkItemCell({ g }: { g: DashboardGroup }) {
  const style = g.color ? ({ "--g-color": g.color } as CSSProperties) : undefined;
  return (
    <span className="wi-cell" style={style}>
      <span className="swatch" />
      {g.branch ? (
        <>
          <span className="branch">{g.branch}</span>
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
      {g.openPath ? <span className="chip path">{g.openPath}</span> : null}
    </span>
  );
}

function StatusCell({ g }: { g: DashboardGroup }) {
  if (!g.jiraStatus && !g.status) return <span className="muted">—</span>;
  return (
    <span className="status-cell">
      {g.jiraStatus ? <span className="pill status">{g.jiraStatus}</span> : null}
      {g.status ? (
        <span className={"pill st-" + g.status}>{STATUS_LABELS[g.status] || g.status}</span>
      ) : null}
    </span>
  );
}

function ActionCell({ g }: { g: DashboardGroup }) {
  if (g.actionRequired) return <span className="action-dot" title="Action required by you" />;
  if (g.needsFollowup) return <span className="followup-dot" title="Needs follow-up" />;
  return <span className="muted">—</span>;
}

function workItemFilterText(g: DashboardGroup): string {
  return [
    g.branch,
    g.ticket,
    g.repo,
    g.summary,
    g.openPath,
    ...(g.tickets ?? []).map((t) => t.key + " " + (t.title ?? "")),
  ]
    .filter(Boolean)
    .join(" ");
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

  const columns: Column<DashboardGroup>[] = [
    {
      key: "workItem",
      header: "Work item",
      render: (g) => <WorkItemCell g={g} />,
      sortValue: (g) => g.branch || g.ticket || "",
      filterText: workItemFilterText,
    },
    {
      key: "repo",
      header: "Repo",
      className: "muted",
      render: (g) => g.repo || "—",
      sortValue: (g) => g.repo || "",
    },
    {
      key: "summary",
      header: "Summary",
      className: "summary-col",
      render: (g) => g.summary || "—",
      sortValue: (g) => g.summary || "",
    },
    {
      key: "status",
      header: "Status",
      render: (g) => <StatusCell g={g} />,
      sortValue: (g) => g.jiraStatus || g.status || "",
    },
    {
      key: "sessions",
      header: "Sessions",
      render: (g) => (
        <ExpandableCell
          items={g.sessions ?? []}
          itemKey={(s, i) => s.name + i}
          renderItem={(s) => <SessionMini s={s} onReload={() => void load()} />}
        />
      ),
      sortValue: (g) => {
        const sessions = g.sessions ?? [];
        const live = sessions.filter((s) => s.live).length;
        return live * 1000 + sessions.length;
      },
      filterText: (g) => (g.sessions ?? []).map((s) => s.name).join(" "),
    },
    {
      key: "prs",
      header: "PRs",
      render: (g) => (
        <ExpandableCell
          items={g.prs ?? []}
          itemKey={(pr, i) => pr.prNumber || i}
          renderItem={(pr) => <PrMini pr={pr} />}
        />
      ),
      sortValue: (g) => (g.prs ?? []).length,
      filterText: (g) => (g.prs ?? []).map((pr) => pr.title || "").join(" "),
    },
    {
      key: "lastActivity",
      header: "Last activity",
      className: "muted",
      render: (g) => timeAgo(g.lastActivity) || "—",
      sortValue: (g) => (g.lastActivity ? Date.parse(g.lastActivity) : 0),
    },
    {
      key: "action",
      header: "",
      render: (g) => <ActionCell g={g} />,
      sortValue: (g) => (g.actionRequired ? 2 : g.needsFollowup ? 1 : 0),
    },
    {
      key: "open",
      header: "",
      render: (g) => <LaunchButton workKey={g.key || g.ticket || ""} />,
      sortable: false,
    },
  ];

  return (
    <Layout mainClass="wrap" stats={stats} controls={controls}>
      {groups.length === 0 ? (
        <div className="empty">{errText ?? "No sessions, tickets, or PRs yet."}</div>
      ) : (
        <div className="section">
          <div className="section-head">
            <span className="title">Work items</span>
            <span className="grow" />
            {data?.generatedAt ? (
              <span className="muted">snapshot {timeAgo(data.generatedAt)}</span>
            ) : null}
          </div>
          <DataTable
            columns={columns}
            rows={groups}
            rowKey={(g) => g.key}
            rowClassName={(g) => (g.needsFollowup ? "followup" : "")}
            onRowClick={(g) => navigate("/workitem?key=" + encodeURIComponent(g.key || g.ticket || ""))}
            initialSort={{ key: "lastActivity", dir: "desc" }}
            filterable
            filterPlaceholder="Filter work items…"
          />
        </div>
      )}
    </Layout>
  );
}
