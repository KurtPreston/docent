import { useCallback, useEffect, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Layout } from "../components/Layout";
import { fetchReportMeta, startReport, fetchReportJob } from "../lib/api";
import { errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { ReportJob, ReportMeta, ReportMode, ReportRequest } from "../lib/types";

const POLL_MS = 2000;
const sleep = (ms: number) => new Promise<void>((r) => window.setTimeout(r, ms));

/** Prefill form fields from a mode's declared (or non-interactive) defaults. */
function applyModeDefaults(
  m: ReportMode,
  set: {
    days: (v: string) => void;
    scope: (v: string) => void;
    prompt: (v: string) => void;
    collect: (v: string) => void;
  },
) {
  set.days(m.lookbackKind === "days" && m.lookbackDays ? String(m.lookbackDays) : "");
  set.scope(m.scope || "");
  set.prompt(m.prompt ?? "");
  set.collect(m.collect || "");
}

export function Report() {
  const [meta, setMeta] = useState<ReportMeta | null>(null);
  const [mode, setMode] = useState("");
  const [days, setDays] = useState("");
  const [scope, setScope] = useState("");
  const [prompt, setPrompt] = useState("");
  const [collect, setCollect] = useState("");
  const [job, setJob] = useState<ReportJob | null>(null);
  const [busy, setBusy] = useState(false);

  // aliveRef stops polling after unmount; runIdRef makes a fresh submit
  // supersede any in-flight poll loop for a previous job.
  const aliveRef = useRef(true);
  const runIdRef = useRef("");

  useEffect(() => {
    document.title = "docent · report";
    return () => {
      aliveRef.current = false;
    };
  }, []);

  useEffect(() => {
    (async () => {
      try {
        const m = await fetchReportMeta();
        setMeta(m);
        const first = m.modes[0];
        if (first) {
          setMode(first.id);
          applyModeDefaults(first, {
            days: setDays,
            scope: setScope,
            prompt: setPrompt,
            collect: setCollect,
          });
        }
      } catch (e) {
        toast("failed to load report options: " + errMsg(e), true);
      }
    })();
  }, []);

  const selectedMode = meta?.modes.find((m) => m.id === mode);
  const promptRequired = selectedMode?.promptRequired ?? false;
  const lookbackIsPreviousWeekday = selectedMode?.lookbackKind === "previous-weekday";

  const selectMode = useCallback(
    (id: string) => {
      setMode(id);
      const m = meta?.modes.find((x) => x.id === id);
      if (m) {
        applyModeDefaults(m, {
          days: setDays,
          scope: setScope,
          prompt: setPrompt,
          collect: setCollect,
        });
      }
    },
    [meta],
  );

  const poll = useCallback(async (id: string) => {
    while (aliveRef.current && runIdRef.current === id) {
      let j: ReportJob;
      try {
        j = await fetchReportJob(id);
      } catch (e) {
        toast("polling failed: " + errMsg(e), true);
        setBusy(false);
        return;
      }
      if (runIdRef.current !== id) return; // superseded by a newer run
      setJob(j);
      if (j.status === "done") {
        setBusy(false);
        return;
      }
      if (j.status === "error") {
        setBusy(false);
        toast(j.error ?? "report failed", true);
        return;
      }
      await sleep(POLL_MS);
    }
  }, []);

  const submit = useCallback(async () => {
    if (!mode) {
      toast("choose a mode first", true);
      return;
    }
    const trimmedPrompt = prompt.trim();
    if (promptRequired && trimmedPrompt === "") {
      toast("this mode requires a prompt", true);
      return;
    }
    const req: ReportRequest = { mode };
    if (days.trim() !== "") {
      const n = Number(days);
      if (!Number.isInteger(n) || n < 1) {
        toast("lookback days must be a positive whole number", true);
        return;
      }
      req.days = n;
    }
    if (scope) req.scope = scope;
    if (collect) req.collect = collect;
    if (trimmedPrompt !== "") req.prompt = trimmedPrompt;

    setBusy(true);
    setJob(null);
    try {
      const id = await startReport(req);
      runIdRef.current = id;
      setJob({ id, status: "pending" });
      void poll(id);
    } catch (e) {
      setBusy(false);
      toast("failed to start report: " + errMsg(e), true);
    }
  }, [mode, days, scope, collect, prompt, promptRequired, poll]);

  const download = useCallback(() => {
    if (!job?.markdown) return;
    const date = new Date().toISOString().slice(0, 10);
    const name = `${date}-${job.meta?.mode ?? mode}.md`;
    const blob = new Blob([job.markdown], { type: "text/markdown;charset=utf-8" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = name;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }, [job, mode]);

  const stats = meta ? (
    <span>
      AI: <b>{meta.provider.label}</b>
    </span>
  ) : null;

  const running = busy && (job?.status === "pending" || job?.status === "running" || !job);

  return (
    <Layout mainClass="wrap" stats={stats}>
      <div className="section report">
        <div className="section-head">
          <span className="title">Generate report</span>
        </div>
        <form
          className="report-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (!busy) void submit();
          }}
        >
          <label className="field">
            <span className="field-label">Mode</span>
            <select
              value={mode}
              onChange={(e) => selectMode(e.target.value)}
              disabled={!meta || busy}
            >
              {meta?.modes.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field-label">Lookback days</span>
            <input
              type="number"
              min={1}
              step={1}
              placeholder={lookbackIsPreviousWeekday ? "previous weekday" : "days"}
              value={days}
              onChange={(e) => setDays(e.target.value)}
              disabled={busy}
            />
          </label>

          <label className="field">
            <span className="field-label">Scope</span>
            <select value={scope} onChange={(e) => setScope(e.target.value)} disabled={busy}>
              {meta?.scopes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field-label">Collect</span>
            <select value={collect} onChange={(e) => setCollect(e.target.value)} disabled={busy}>
              {meta?.collects.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>

          <label className="field field-wide">
            <span className="field-label">
              Prompt {promptRequired ? <em className="req">required</em> : <em className="muted">editable</em>}
            </span>
            <textarea
              rows={4}
              placeholder={
                promptRequired
                  ? "Describe what you want the model to produce…"
                  : "Mode default prompt"
              }
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              disabled={busy}
            />
          </label>

          <div className="report-actions">
            <button type="submit" className="primary" disabled={!meta || busy}>
              {busy ? "Generating…" : "Generate"}
            </button>
            {running ? <span className="muted">status: {job?.status ?? "starting"}…</span> : null}
          </div>
        </form>
      </div>

      {job?.status === "done" ? (
        <div className="section report-result">
          <div className="section-head">
            <span className="title">{job.meta?.modeName ?? "Report"}</span>
            {job.meta ? (
              <span className="muted report-meta">
                {job.meta.lookbackDays > 0 ? `${job.meta.lookbackDays}d · ` : ""}
                {job.meta.scope} · {job.meta.statuses} signal{job.meta.statuses === 1 ? "" : "s"}
              </span>
            ) : null}
            <span className="grow" />
            <button type="button" onClick={download}>
              Download .md
            </button>
          </div>
          <div className="markdown-body">
            <ReactMarkdown remarkPlugins={[remarkGfm]}>{job.markdown ?? ""}</ReactMarkdown>
          </div>
        </div>
      ) : null}

      {job?.status === "error" ? (
        <div className="section report-result">
          <div className="section-head">
            <span className="title">Generation failed</span>
          </div>
          <div className="wrap err">{job.error ?? "unknown error"}</div>
        </div>
      ) : null}
    </Layout>
  );
}
