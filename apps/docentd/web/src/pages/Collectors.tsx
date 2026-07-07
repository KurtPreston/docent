import { useCallback, useEffect, useState } from "react";
import { Layout } from "../components/Layout";
import { RefreshButton } from "../components/Controls";
import { DataTable } from "../components/DataTable";
import type { Column } from "../components/DataTable";
import { fetchCollectors, collectUnit } from "../lib/api";
import { timeAgoSigned, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { CollectorsView, UnitView } from "../lib/types";

function CollectCell({ u, onCollected }: { u: UnitView; onCollected: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      disabled={busy}
      onClick={async (e) => {
        e.stopPropagation();
        setBusy(true);
        try {
          await collectUnit(u.directiveId, u.mode);
        } catch (e) {
          toast("collect error: " + errMsg(e), true);
        } finally {
          setBusy(false);
          onCollected();
        }
      }}
    >
      {busy ? "collecting…" : "collect"}
    </button>
  );
}

export function Collectors() {
  const [data, setData] = useState<CollectorsView | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchCollectors());
    } catch (e) {
      toast("refresh failed: " + errMsg(e), true);
    }
  }, []);

  useEffect(() => {
    document.title = "docent · collectors";
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const units = data?.units ?? [];
  const stats = data ? (
    <span>
      <b>{units.length}</b> units
    </span>
  ) : null;

  const columns: Column<UnitView>[] = [
    { key: "directive", header: "Directive", render: (u) => u.directiveId, sortValue: (u) => u.directiveId },
    {
      key: "collector",
      header: "Collector",
      className: "mono",
      render: (u) => u.collector,
      sortValue: (u) => u.collector,
    },
    {
      key: "mode",
      header: "Mode",
      render: (u) => <span className={"badge " + u.mode}>{u.mode}</span>,
      sortValue: (u) => u.mode,
    },
    {
      key: "interval",
      header: "Interval",
      className: "muted",
      render: (u) => u.interval || "—",
      sortValue: (u) => u.interval || "",
    },
    {
      key: "onRequest",
      header: "On request",
      className: "muted",
      render: (u) => (u.onRequest ? "yes" : "—"),
      sortValue: (u) => (u.onRequest ? 1 : 0),
    },
    {
      key: "onLoad",
      header: "On load",
      className: "muted",
      render: (u) => (u.onLoad ? "yes" : "—"),
      sortValue: (u) => (u.onLoad ? 1 : 0),
    },
    {
      key: "lastRun",
      header: "Last run",
      className: "muted",
      render: (u) => timeAgoSigned(u.lastRun),
      sortValue: (u) => (u.lastRun ? Date.parse(u.lastRun) : 0),
    },
    {
      key: "nextDue",
      header: "Next due",
      className: "muted",
      render: (u) => (u.nextDue ? timeAgoSigned(u.nextDue) : "—"),
      sortValue: (u) => (u.nextDue ? Date.parse(u.nextDue) : 0),
    },
    { key: "items", header: "Items", render: (u) => u.itemCount, sortValue: (u) => u.itemCount },
    {
      key: "error",
      header: "Error",
      className: "err",
      render: (u) => u.lastErr || "",
      filterText: (u) => u.lastErr || "",
    },
    {
      key: "collect",
      header: "",
      render: (u) => <CollectCell u={u} onCollected={() => void load()} />,
      sortable: false,
    },
  ];

  return (
    <Layout mainClass="wrap" stats={stats} controls={<RefreshButton onClick={() => void load()} />}>
      <div className="section">
        <div className="section-head">
          <span className="title">Collection units</span>
          <span className="grow" />
          {data?.generatedAt ? (
            <span className="muted">snapshot {timeAgoSigned(data.generatedAt)}</span>
          ) : null}
        </div>
        <DataTable
          columns={columns}
          rows={units}
          rowKey={(u, i) => u.directiveId + "/" + u.mode + i}
          filterable
          filterPlaceholder="Filter units…"
          empty="No collection units configured."
          storageKey="collectors"
        />
      </div>
    </Layout>
  );
}
