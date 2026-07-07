import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { fetchSignals } from "../lib/api";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { SignalsView, SignalUnit, SignalView } from "../lib/types";

const columns: Column<SignalView>[] = [
  { key: "kind", header: "Kind", className: "mono", render: (s) => s.kind, sortValue: (s) => s.kind },
  {
    key: "title",
    header: "Title",
    render: (s) => (
      <>
        {s.url ? (
          <a href={s.url} target="_blank" rel="noopener" onClick={(e) => e.stopPropagation()}>
            {s.title || "(untitled)"}
          </a>
        ) : (
          s.title || "(untitled)"
        )}
        {s.summary ? <div className="muted">{s.summary}</div> : null}
      </>
    ),
    sortValue: (s) => s.title || "",
    filterText: (s) => (s.title || "") + " " + (s.summary || ""),
  },
  {
    key: "observed",
    header: "Observed",
    className: "muted",
    render: (s) => timeAgo(s.observedAt),
    sortValue: (s) => (s.observedAt ? Date.parse(s.observedAt) : 0),
  },
  {
    key: "entity",
    header: "Entity",
    className: "mono",
    render: (s) => s.entityId || "",
    sortValue: (s) => s.entityId || "",
  },
  {
    key: "workItem",
    header: "Work item",
    render: (s) =>
      s.workItemKey ? (
        <Link to={"/workitem?key=" + encodeURIComponent(s.workItemKey)} onClick={(e) => e.stopPropagation()}>
          {s.workItemKey}
        </Link>
      ) : (
        <span className="muted">—</span>
      ),
    sortValue: (s) => s.workItemKey || "",
  },
];

function Unit({ u }: { u: SignalUnit }) {
  return (
    <div className="section">
      <div className="section-head">
        <span className="title">{u.directiveId}</span>
        <span className={"badge " + u.mode}>
          {u.collector} · {u.mode}
        </span>
        <span className="badge">{u.count} signals</span>
        {u.lastRun ? <span className="muted">ran {timeAgo(u.lastRun)}</span> : null}
        {u.lastErr ? <span className="err">{u.lastErr}</span> : null}
      </div>
      <DataTable
        columns={columns}
        rows={u.signals ?? []}
        rowKey={(_s, i) => i}
        filterable
        filterPlaceholder="Filter signals…"
        empty="No signals."
        storageKey="signals"
      />
    </div>
  );
}

export function Signals() {
  const [data, setData] = useState<SignalsView | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchSignals());
    } catch (e) {
      toast("refresh failed: " + errMsg(e), true);
    }
  }, []);

  useEffect(() => {
    document.title = "docent · signals";
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const units = data?.units ?? [];
  const total = units.reduce((n, u) => n + (u.count || 0), 0);
  const stats = data ? (
    <>
      <span>
        <b>{total}</b> signals
      </span>
      <span>
        <b>{units.length}</b> units
      </span>
    </>
  ) : null;

  return (
    <Layout mainClass="wrap" stats={stats} controls={<RefreshButton onClick={() => void load()} />}>
      {units.length === 0 ? (
        <div className="empty">No collection units configured.</div>
      ) : (
        units.map((u, i) => <Unit key={i} u={u} />)
      )}
    </Layout>
  );
}
