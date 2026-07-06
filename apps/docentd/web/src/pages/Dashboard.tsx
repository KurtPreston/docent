import { useCallback, useEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { LinkButton } from "../components/LinkButton";
import { fetchCollectors, fetchDashboard, launchWorkItem } from "../lib/api";
import { activate } from "../lib/sessions";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type {
  CollectorsView,
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

// PathCell shows the work item's repo path. When onOpen is provided (i.e. the
// provider supplied a deep link), the path becomes the clickable open/focus
// action anchor.
function PathCell({ path, onOpen }: { path?: string; onOpen?: () => void }) {
  if (!path) return <span className="muted">—</span>;
  if (onOpen) {
    return (
      <span
        className="path-cell mono clickable"
        title={"Open " + path}
        onClick={(e) => {
          e.stopPropagation();
          onOpen();
        }}
      >
        <span className="path-inner">{path}</span>
      </span>
    );
  }
  return (
    <span className="path-cell mono" title={path}>
      <span className="path-inner">{path}</span>
    </span>
  );
}

// Compact single-line rendering of a session, used inside the dashboard's
// Sessions cell (one shown by default, rest revealed via "+N more"). onActivate,
// when set, makes a live session clickable (focus for wsm, open for cursor).
function SessionMini({
  s,
  onActivate,
  activateTitle,
}: {
  s: DashboardSession;
  onActivate?: () => void;
  activateTitle?: string;
}) {
  const clickable = s.live && !!onActivate;
  return (
    <span
      className={"mini session" + (clickable ? " clickable" : "")}
      title={clickable ? activateTitle : undefined}
      onClick={
        clickable
          ? (e) => {
              e.stopPropagation();
              onActivate?.();
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

// Which columns to show is driven by the docent config: a collector-backed
// column only appears when its backing collector is configured. `collectors` is
// null until the /api/collectors fetch resolves, in which case we optimistically
// show every column (matching the pre-gating behavior) rather than flashing them
// away. The session column is instead gated on the active session_manager
// provider (see sessionHeaderFor / the columns filter).
type ColumnGating = { jira: boolean; github: boolean };

function columnGating(collectors: CollectorsView | null): ColumnGating {
  if (!collectors) return { jira: true, github: true };
  const configured = new Set(collectors.units.map((u) => u.collector));
  return {
    jira: configured.has("jira"),
    github: configured.has("github") || configured.has("github-enterprise"),
  };
}

// sessionHeaderFor names the single session column after the active provider.
function sessionHeaderFor(provider: string): string {
  if (provider === "cursor") return "Cursor Sessions";
  if (provider === "wsm") return "WSM Sessions";
  return "Sessions";
}

export function Dashboard() {
  const navigate = useNavigate();
  const [data, setData] = useState<DashboardData | null>(null);
  const [collectors, setCollectors] = useState<CollectorsView | null>(null);
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

  // Collector config rarely changes, so fetch it once to decide which columns
  // are relevant. On failure we keep `collectors` null and show all columns.
  useEffect(() => {
    fetchCollectors()
      .then(setCollectors)
      .catch(() => {});
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

  const gating = columnGating(collectors);
  const provider = data?.provider ?? "";
  const allColumns: Column<DashboardGroup>[] = [
    {
      key: "repo",
      header: "Repo",
      className: "muted",
      render: (g) => g.repo || "—",
      sortValue: (g) => g.repo || "",
    },
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
      key: "path",
      header: "Path",
      render: (g) => (
        <PathCell
          path={g.openPath}
          onOpen={g.deepLink ? () => void activate(provider, g) : undefined}
        />
      ),
      sortValue: (g) => g.openPath || "",
      filterText: (g) => g.openPath || "",
    },
    {
      key: "sessions",
      header: sessionHeaderFor(provider),
      render: (g) => (
        <ExpandableCell
          items={g.sessions ?? []}
          itemKey={(s, i) => s.name + i}
          renderItem={(s) => (
            <SessionMini
              s={s}
              activateTitle={provider === "cursor" ? "Open in Cursor" : "Focus this window"}
              onActivate={
                provider === "cursor"
                  ? g.deepLink
                    ? () => void activate(provider, g)
                    : undefined
                  : () =>
                      void activate(provider, g, { name: s.name, host: s.host }).then(() =>
                        window.setTimeout(() => void load(), 400),
                      )
              }
            />
          )}
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

  // Gate collector-backed columns on the docent config: JIRA needs a jira
  // collector and PRs need a github collector. The session column shows only
  // when a session_manager provider is configured (empty provider ⇒ no column,
  // no clickable links).
  const columns = allColumns.filter((c) => {
    if (c.key === "jira") return gating.jira;
    if (c.key === "prs") return gating.github;
    if (c.key === "sessions") return provider !== "";
    return true;
  });

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
