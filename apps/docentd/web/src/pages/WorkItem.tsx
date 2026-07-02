import { useCallback, useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router-dom";
import { Layout } from "../components/Layout";
import { RefreshButton } from "../components/Controls";
import { fetchWorkItem, launchWorkItem } from "../lib/api";
import { focusSession } from "../lib/wsm";
import { timeAgo, errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { WorkItemDetail, DashboardSession, DashboardPR } from "../lib/types";

function Section({
  title,
  headExtra,
  children,
}: {
  title: string;
  headExtra?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="section">
      <div className="section-head">
        <span className="title">{title}</span>
        {headExtra}
      </div>
      {children}
    </div>
  );
}

function KV({ k, v, link }: { k: string; v: string; link?: string }) {
  return (
    <div className="kv">
      <span className="k">{k}</span>
      {link ? (
        <a href={link} target="_blank" rel="noopener">
          {v}
        </a>
      ) : (
        <span>{v}</span>
      )}
    </div>
  );
}

function SessionRow({ s }: { s: DashboardSession }) {
  return (
    <div
      className={"row session" + (s.live ? " clickable" : "")}
      title={s.live ? "Focus this window" : undefined}
      onClick={s.live ? () => void focusSession(s.name, s.host) : undefined}
    >
      <span className={"live" + (s.live ? " on" : "")} />
      <span className="name">{s.name}</span>
      {s.host ? <span className="chip">{s.host}</span> : null}
      <span className="spacer" />
      {s.needsFollowup ? (
        <span className="pill followup">needs follow-up</span>
      ) : (
        <span className="pill status">{s.status || (s.live ? "idle" : "closed")}</span>
      )}
      {s.lastActivity ? <span className="meta">{timeAgo(s.lastActivity)}</span> : null}
    </div>
  );
}

function PrRow({ pr }: { pr: DashboardPR }) {
  return (
    <a className="row pr clickable" href={pr.url || "#"} target="_blank" rel="noopener">
      <span className="name">{pr.title || "(untitled PR)"}</span>
      {pr.repo ? <span className="chip">{pr.repo}</span> : null}
      <span className="spacer" />
      {pr.state ? <span className={"pill state " + pr.state.toLowerCase()}>{pr.state}</span> : null}
    </a>
  );
}

type LoadState = { status: "empty"; msg: string } | { status: "loaded"; d: WorkItemDetail };

export function WorkItem() {
  const [params] = useSearchParams();
  const key = params.get("key") ?? "";
  const [state, setState] = useState<LoadState | null>(null);

  const load = useCallback(async () => {
    if (!key) {
      setState({ status: "empty", msg: "No work item key provided." });
      return;
    }
    try {
      const d = await fetchWorkItem(key);
      if (!d) {
        setState({ status: "empty", msg: "Work item " + key + " not found." });
        return;
      }
      setState({ status: "loaded", d });
    } catch (e) {
      toast("load failed: " + errMsg(e), true);
    }
  }, [key]);

  useEffect(() => {
    document.title = "docent · work item";
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const stats = state?.status === "loaded" ? <span>{state.d.key}</span> : null;
  const controls = <RefreshButton onClick={() => void load()} />;

  if (!state || state.status === "empty") {
    return (
      <Layout stats={stats} controls={controls}>
        <div className="empty">{state?.status === "empty" ? state.msg : ""}</div>
      </Layout>
    );
  }

  const d = state.d;
  const sessions = d.sessions ?? [];
  const prs = d.prs ?? [];
  const entities = d.entities ?? [];
  const signals = d.signals ?? [];
  const tickets = d.tickets ?? [];

  return (
    <Layout mainClass="detail-grid" stats={stats} controls={controls}>
      <Section
        title="Overview"
        headExtra={
          <button
            className="launch-btn"
            type="button"
            title="Open editor for this work item"
            onClick={() => void launchWorkItem(d.key)}
          >
            open editor
          </button>
        }
      >
        <div className="wrap">
          <KV k="Key" v={d.key} />
          {d.branch ? <KV k="Branch" v={d.branch} /> : null}
          {d.branch && d.repo ? <KV k="Repo" v={d.repo} /> : null}
          {d.branch && d.openPath ? <KV k="Open path" v={d.openPath} /> : null}
          {d.summary || d.title ? <KV k="Summary" v={d.summary || d.title || ""} /> : null}
          {d.jiraStatus ? <KV k="Status" v={d.jiraStatus} /> : null}
          {d.jiraUrl ? <KV k="Jira" v={d.key} link={d.jiraUrl} /> : null}
        </div>
        {tickets.length ? (
          <div className="wrap">
            {tickets.map((t, i) => (
              <div className="kv" key={i}>
                <span className="k">Ticket</span>
                {t.url ? (
                  <a href={t.url} target="_blank" rel="noopener">
                    {t.key + (t.title ? " — " + t.title : "")}
                  </a>
                ) : (
                  <span>{t.key}</span>
                )}
                {t.status ? <span className="chip">{t.status}</span> : null}
              </div>
            ))}
          </div>
        ) : null}
      </Section>

      <Section title={`Sessions (${sessions.length})`}>
        <div className="rows">
          {sessions.map((s, i) => (
            <SessionRow key={i} s={s} />
          ))}
          {sessions.length === 0 ? <div className="wrap muted">No sessions.</div> : null}
        </div>
      </Section>

      <Section title={`Pull requests (${prs.length})`}>
        <div className="rows">
          {prs.map((pr, i) => (
            <PrRow key={i} pr={pr} />
          ))}
          {prs.length === 0 ? <div className="wrap muted">No pull requests.</div> : null}
        </div>
      </Section>

      <Section title={`Entities (${entities.length})`}>
        <table className="tbl">
          <tbody>
            <tr>
              {["Kind", "Title", "ID"].map((h) => (
                <th key={h}>{h}</th>
              ))}
            </tr>
            {entities.map((e, i) => (
              <tr key={i}>
                <td className="mono">{e.kind}</td>
                <td>
                  {e.url ? (
                    <a href={e.url} target="_blank" rel="noopener">
                      {e.title}
                    </a>
                  ) : (
                    e.title
                  )}
                </td>
                <td className="mono">{e.id}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>

      <Section title={`Contributing signals (${signals.length})`}>
        <table className="tbl">
          <tbody>
            <tr>
              {["Kind", "Title", "Observed"].map((h) => (
                <th key={h}>{h}</th>
              ))}
            </tr>
            {signals.map((s, i) => (
              <tr key={i}>
                <td className="mono">{s.kind}</td>
                <td>
                  {s.url ? (
                    <a href={s.url} target="_blank" rel="noopener">
                      {s.title || "(untitled)"}
                    </a>
                  ) : (
                    s.title || "(untitled)"
                  )}
                </td>
                <td className="muted">{timeAgo(s.observedAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Section>
    </Layout>
  );
}
