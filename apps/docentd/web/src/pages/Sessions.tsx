import { useCallback, useEffect, useState } from "react";
import { Layout } from "../components/Layout";
import { RefreshButton, AutoToggle } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { fetchSessions } from "../lib/api";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { RegistrySession } from "../lib/types";

const POLL_MS = 5000;

function StatusPill({ s }: { s: RegistrySession }) {
  if (!s.live) return <span className="pill status">closed</span>;
  else if (s.status === "needs-followup") return <span className="pill followup">needs follow-up</span>;
  else if (s.status === "working") return <span className="pill working">working</span>;
  return <span className="pill status">idle</span>;
}

export function Sessions() {
  const [sessions, setSessions] = useState<RegistrySession[] | null>(null);
  const [auto, setAuto] = useState(true);

  const load = useCallback(async () => {
    try {
      const d = await fetchSessions();
      setSessions(d.sessions ?? []);
    } catch (e) {
      toast("refresh failed: " + errMsg(e), true);
    }
  }, []);

  useEffect(() => {
    document.title = "docent · sessions";
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!auto) return;
    const id = window.setInterval(() => void load(), POLL_MS);
    return () => window.clearInterval(id);
  }, [auto, load]);

  const rows = sessions ?? [];
  const liveCount = rows.filter((s) => s.live).length;
  const stats = sessions ? (
    <span>
      <b>{liveCount}</b> live · <b>{rows.length}</b> registered
    </span>
  ) : null;

  const columns: Column<RegistrySession>[] = [
    {
      key: "live",
      header: "",
      render: (s) => <span className={"live" + (s.live ? " on" : "")} />,
      sortValue: (s) => (s.live ? 1 : 0),
      className: "live-col",
    },
    {
      key: "name",
      header: "Name",
      render: (s) => s.name || "(unnamed)",
      sortValue: (s) => s.name || "",
      filterText: (s) => s.name || "",
    },
    {
      key: "status",
      header: "Status",
      render: (s) => <StatusPill s={s} />,
      sortValue: (s) => (!s.live ? "closed" : s.status),
    },
    {
      key: "ide",
      header: "IDE",
      render: (s) => s.ide || "—",
      sortValue: (s) => s.ide || "",
    },
    {
      key: "ideHost",
      header: "IDE host",
      className: "mono",
      render: (s) => s.ideHost || "—",
      sortValue: (s) => s.ideHost || "",
      filterText: (s) => s.ideHost || "",
    },
    {
      key: "targetHost",
      header: "Target host",
      className: "mono",
      render: (s) => s.targetHost || "—",
      sortValue: (s) => s.targetHost || "",
      filterText: (s) => s.targetHost || "",
    },
    {
      key: "path",
      header: "Path",
      className: "mono",
      render: (s) => s.path || "—",
      sortValue: (s) => s.path || "",
      filterText: (s) => s.path || "",
    },
    {
      key: "lastActivity",
      header: "Last activity",
      className: "muted",
      render: (s) => timeAgo(s.lastActivity) || "—",
      sortValue: (s) => (s.lastActivity ? Date.parse(s.lastActivity) : 0),
    },
  ];

  return (
    <Layout
      mainClass="wrap"
      stats={stats}
      controls={
        <>
          <AutoToggle checked={auto} onChange={setAuto} />
          <RefreshButton onClick={() => void load()} />
        </>
      }
    >
      <div className="section">
        <div className="section-head">
          <span className="title">Registered sessions</span>
          <span className="grow" />
        </div>
        <DataTable
          columns={columns}
          rows={rows}
          rowKey={(s) => s.key}
          initialSort={{ key: "live", dir: "desc" }}
          filterable
          filterPlaceholder="Filter sessions…"
          empty="No sessions registered."
          storageKey="sessions"
        />
      </div>
    </Layout>
  );
}
