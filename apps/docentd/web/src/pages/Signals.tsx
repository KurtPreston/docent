import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton } from "../components/Controls";
import { fetchSignals } from "../lib/api";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { SignalsView, SignalUnit, SignalView } from "../lib/types";

function SignalRow({ s }: { s: SignalView }) {
  return (
    <tr>
      <td className="mono">{s.kind}</td>
      <td>
        {s.url ? (
          <a href={s.url} target="_blank" rel="noopener">
            {s.title || "(untitled)"}
          </a>
        ) : (
          s.title || "(untitled)"
        )}
        {s.summary ? <div className="muted">{s.summary}</div> : null}
      </td>
      <td className="muted">{timeAgo(s.observedAt)}</td>
      <td className="mono">{s.entityId || ""}</td>
      <td>
        {s.workItemKey ? (
          <Link to={"/workitem?key=" + encodeURIComponent(s.workItemKey)}>{s.workItemKey}</Link>
        ) : (
          <span className="muted">—</span>
        )}
      </td>
    </tr>
  );
}

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
      {!u.signals || u.signals.length === 0 ? (
        <div className="wrap muted">No signals.</div>
      ) : (
        <table className="tbl">
          <tbody>
            <tr>
              {["Kind", "Title", "Observed", "Entity", "Work item"].map((h) => (
                <th key={h}>{h}</th>
              ))}
            </tr>
            {u.signals.map((s, i) => (
              <SignalRow key={i} s={s} />
            ))}
          </tbody>
        </table>
      )}
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
