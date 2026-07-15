import { useCallback, useEffect, useState } from "react";
import { Layout } from "../components/Layout";
import { DataTable, type Column } from "../components/DataTable";
import { fetchAutomations, runAutomation } from "../lib/api";
import type { AutomationJob, AutomationRule } from "../lib/types";
import { errMsg } from "../lib/format";
import { toast } from "../lib/toast";

// RunCell triggers one rule immediately (bypassing schedule/cooldown) and
// refreshes the job history so the resulting job shows up. RunRuleNow is
// synchronous on the daemon, so keep the button disabled while it's in flight.
function RunCell({ id, onRun }: { id: string; onRun: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <button
      type="button"
      className="launch-btn"
      disabled={busy}
      title="Run this automation now (bypasses schedule and cooldown)"
      onClick={async () => {
        setBusy(true);
        try {
          await runAutomation(id);
        } catch (e) {
          toast("run error: " + errMsg(e), true);
        } finally {
          setBusy(false);
          onRun();
        }
      }}
    >
      {busy ? "Running…" : "Run"}
    </button>
  );
}

const jobColumns: Column<AutomationJob>[] = [
  {
    key: "createdAt",
    header: "When",
    render: (r) => r.createdAt,
    sortValue: (r) => r.createdAt,
    sortable: true,
  },
  {
    key: "ruleId",
    header: "Rule",
    render: (r) => <code>{r.ruleId}</code>,
    sortValue: (r) => r.ruleId,
    filterText: (r) => r.ruleId,
    sortable: true,
  },
  {
    key: "status",
    header: "Status",
    render: (r) => r.status,
    sortValue: (r) => r.status,
    filterText: (r) => r.status,
    sortable: true,
  },
  {
    key: "detail",
    header: "Detail",
    render: (r) => r.error || r.message || "",
    filterText: (r) => r.error || r.message || "",
  },
];

export function Automations() {
  const [rules, setRules] = useState<AutomationRule[]>([]);
  const [jobs, setJobs] = useState<AutomationJob[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  const load = useCallback(() => {
    setLoading(true);
    fetchAutomations()
      .then((d) => {
        setRules(d.rules ?? []);
        setJobs(d.jobs ?? []);
        setError("");
      })
      .catch((e) => setError(errMsg(e)))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => {
    load();
    const t = setInterval(load, 10000);
    return () => clearInterval(t);
  }, [load]);

  return (
    <Layout
      mainClass="wrap"
      stats={
        <>
          <span className="chip">{rules.length} rules</span>
          <span className="chip">{jobs.length} recent jobs</span>
        </>
      }
      controls={
        <button type="button" className="btn" onClick={load} disabled={loading}>
          Refresh
        </button>
      }
    >
      {error ? <div className="empty error">{error}</div> : null}

      <section className="panel">
        <h2>Rules</h2>
        <p className="muted">
          Edit rules in Settings → config.yaml under <code>automations:</code>. Changes apply on
          daemon restart.
        </p>
        {rules.length === 0 ? (
          <div className="empty">No automations configured.</div>
        ) : (
          <table className="tbl">
            <thead>
              <tr>
                <th>ID</th>
                <th>Enabled</th>
                <th>Trigger</th>
                <th>Actions</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id}>
                  <td>
                    <code>{r.id}</code>
                    {r.name ? <div className="muted">{r.name}</div> : null}
                  </td>
                  <td>{r.enabled ? "yes" : "no"}</td>
                  <td>
                    <code>{r.trigger?.type ?? "?"}</code>
                    {r.trigger?.source ? ` · ${r.trigger.source}` : ""}
                    {r.trigger?.at ? ` · ${r.trigger.at}` : ""}
                    {r.trigger?.cron ? ` · ${r.trigger.cron}` : ""}
                  </td>
                  <td>{(r.actions ?? []).map((a) => a.type).join(", ")}</td>
                  <td>
                    <RunCell id={r.id} onRun={load} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="panel" style={{ marginTop: "1.5rem" }}>
        <h2>Job history</h2>
        <DataTable
          columns={jobColumns}
          rows={jobs}
          rowKey={(r) => r.id}
          filterable
          empty="No jobs yet."
          storageKey="automations-jobs"
        />
      </section>
    </Layout>
  );
}
