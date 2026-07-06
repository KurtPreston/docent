import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { LinkButton } from "../components/LinkButton";
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

// ticketList normalizes a group's JIRA tickets: wb: branch items carry an
// explicit tickets[] list, while ticket-primary items only carry the single
// ticket/jiraUrl/jiraStatus fields.
function ticketList(g: DashboardGroup): DashboardTicket[] {
  if (g.tickets && g.tickets.length) return g.tickets;
  if (g.ticket) return [{ key: g.ticket, url: g.jiraUrl, status: g.jiraStatus }];
  return [];
}

// summaryText prefers the JIRA ticket summary (stripping the leading ticket
// key, which now lives in its own column) and drops the branch-name fallback so
// the Summary column doesn't just echo the Work item column.
const LEADING_TICKET_KEY = /^[A-Z][A-Z0-9]+-\d+[\s:·—–-]+/;

function summaryText(g: DashboardGroup): string {
  const s = (g.summary ?? "").trim();
  if (!s || s === g.branch) return "";
  return s.replace(LEADING_TICKET_KEY, "").trim();
}

function JiraCell({ g }: { g: DashboardGroup }) {
  const list = ticketList(g);
  if (list.length === 0) return <span className="muted">—</span>;
  return (
    <div className="jira-cell">
      {list.map((tk, i) => {
        const title = [tk.title || tk.key, tk.status ? "· " + tk.status : ""]
          .filter(Boolean)
          .join(" ");
        return (
          <LinkButton
            key={i}
            service="jira"
            href={tk.url}
            label={tk.key || "ticket"}
            title={title || undefined}
          />
        );
      })}
    </div>
  );
}

function PathCell({ path }: { path?: string }) {
  if (!path) return <span className="muted">—</span>;
  return (
    <span className="path-cell mono" title={path}>
      <span className="path-inner">{path}</span>
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
  const state = (pr.state || "open").toLowerCase();
  const badge = pr.draft ? "draft" : state;
  const title = [pr.title || "pull request", state, pr.draft ? "draft" : ""]
    .filter(Boolean)
    .join(" · ");
  return (
    <LinkButton
      service="github"
      href={pr.url || undefined}
      title={title}
      label={
        <>
          {pr.prNumber ? <span className="lb-num">#{pr.prNumber}</span> : null}
          {pr.title || "(untitled PR)"}
        </>
      }
      trailing={<span className={"lb-state " + badge}>{badge}</span>}
    />
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
  // Identity only: branch when present, else a live session name, else the
  // ticket key. JIRA links and local paths live in their own columns now.
  const name = g.branch || g.sessions?.[0]?.name || g.ticket;
  return (
    <span className="wi-cell" style={style}>
      <span className="swatch" />
      {name ? (
        <span className={g.branch ? "branch" : "ticket"}>{name}</span>
      ) : (
        <span className="ticket untracked">untracked</span>
      )}
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
  return [g.branch, g.ticket, ...(g.sessions ?? []).map((s) => s.name)]
    .filter(Boolean)
    .join(" ");
}

function jiraFilterText(g: DashboardGroup): string {
  return ticketList(g)
    .map((t) => t.key + " " + (t.title ?? ""))
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
      key: "jira",
      header: "JIRA",
      render: (g) => <JiraCell g={g} />,
      sortValue: (g) => ticketList(g)[0]?.key || "",
      filterText: jiraFilterText,
    },
    {
      key: "summary",
      header: "Summary",
      className: "summary-col",
      render: (g) => summaryText(g) || <span className="muted">—</span>,
      sortValue: (g) => summaryText(g),
      filterText: (g) => g.summary || "",
    },
    {
      key: "status",
      header: "Status",
      render: (g) => <StatusCell g={g} />,
      sortValue: (g) => g.jiraStatus || g.status || "",
    },
    {
      key: "repo",
      header: "Repo",
      className: "muted",
      render: (g) => g.repo || "—",
      sortValue: (g) => g.repo || "",
    },
    {
      key: "path",
      header: "Path",
      render: (g) => <PathCell path={g.openPath} />,
      sortValue: (g) => g.openPath || "",
      filterText: (g) => g.openPath || "",
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
