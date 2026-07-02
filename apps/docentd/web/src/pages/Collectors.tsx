import { useCallback, useEffect, useState } from "react";
import { Layout } from "../components/Layout";
import { RefreshButton } from "../components/Controls";
import { fetchCollectors, collectUnit } from "../lib/api";
import { timeAgoSigned, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { CollectorsView, UnitView } from "../lib/types";

const COLUMNS = [
  "Directive",
  "Collector",
  "Mode",
  "Interval",
  "On request",
  "On load",
  "Last run",
  "Next due",
  "Items",
  "Error",
  "",
];

function UnitRow({ u, onCollected }: { u: UnitView; onCollected: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <tr>
      <td>{u.directiveId}</td>
      <td className="mono">{u.collector}</td>
      <td>
        <span className={"badge " + u.mode}>{u.mode}</span>
      </td>
      <td className="muted">{u.interval || "—"}</td>
      <td className="muted">{u.onRequest ? "yes" : "—"}</td>
      <td className="muted">{u.onLoad ? "yes" : "—"}</td>
      <td className="muted">{timeAgoSigned(u.lastRun)}</td>
      <td className="muted">{u.nextDue ? timeAgoSigned(u.nextDue) : "—"}</td>
      <td>{u.itemCount}</td>
      <td className="err">{u.lastErr || ""}</td>
      <td>
        <button
          disabled={busy}
          onClick={async () => {
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
      </td>
    </tr>
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
        {units.length === 0 ? (
          <div className="wrap muted">No collection units configured.</div>
        ) : (
          <table className="tbl">
            <tbody>
              <tr>
                {COLUMNS.map((h, i) => (
                  <th key={i}>{h}</th>
                ))}
              </tr>
              {units.map((u, i) => (
                <UnitRow key={i} u={u} onCollected={() => void load()} />
              ))}
            </tbody>
          </table>
        )}
      </div>
    </Layout>
  );
}
