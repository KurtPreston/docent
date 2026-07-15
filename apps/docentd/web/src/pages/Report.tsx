import { useCallback, useEffect, useRef, useState } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { Layout } from "../components/Layout";
import { fetchReportMeta, startReport, fetchReportJob, streamReport } from "../lib/api";
import { errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type {
  ReportCollectorProgress,
  ReportEvent,
  ReportJob,
  ReportMeta,
  ReportMode,
  ReportRequest,
} from "../lib/types";

const POLL_MS = 2000;
const CUSTOM_PROMPT_MODE = "custom-prompt";
const sleep = (ms: number) => new Promise<void>((r) => window.setTimeout(r, ms));

const PHASE_LABELS: Record<string, string> = {
  collecting: "Collecting signals…",
  correlating: "Correlating work items…",
  generating: "Generating report…",
};

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
  const [phase, setPhase] = useState("");
  const [collectors, setCollectors] = useState<ReportCollectorProgress[]>([]);
  const [partial, setPartial] = useState("");
  const [thinking, setThinking] = useState("");

  // aliveRef stops work after unmount; runIdRef makes a fresh submit
  // supersede any in-flight stream/poll for a previous job.
  const aliveRef = useRef(true);
  const runIdRef = useRef("");
  const abortRef = useRef<AbortController | null>(null);
  const thinkingRef = useRef<HTMLDivElement | null>(null);
  const partialRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = thinkingRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [thinking]);

  useEffect(() => {
    const el = partialRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [partial]);

  useEffect(() => {
    document.title = "docent · report";
    return () => {
      aliveRef.current = false;
      abortRef.current?.abort();
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
  // After leaving a preset via form edits we keep the edited values but the
  // mode id becomes custom-prompt, so the days placeholder should follow
  // that mode (not the preset we left).
  const lookbackIsPreviousWeekday =
    mode !== CUSTOM_PROMPT_MODE && selectedMode?.lookbackKind === "previous-weekday";

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

  // Any tweak to lookback/scope/collect/prompt after loading a preset leaves
  // the preset: switch mode to custom-prompt but keep the user's edits.
  const bumpToCustom = useCallback(() => {
    setMode((cur) => (cur && cur !== CUSTOM_PROMPT_MODE ? CUSTOM_PROMPT_MODE : cur));
  }, []);

  const onDaysChange = useCallback(
    (value: string) => {
      bumpToCustom();
      setDays(value);
    },
    [bumpToCustom],
  );
  const onScopeChange = useCallback(
    (value: string) => {
      bumpToCustom();
      setScope(value);
    },
    [bumpToCustom],
  );
  const onCollectChange = useCallback(
    (value: string) => {
      bumpToCustom();
      setCollect(value);
    },
    [bumpToCustom],
  );
  const onPromptChange = useCallback(
    (value: string) => {
      bumpToCustom();
      setPrompt(value);
    },
    [bumpToCustom],
  );

  const applyEvent = useCallback((id: string, ev: ReportEvent) => {
    if (runIdRef.current !== id || !aliveRef.current) return;
    switch (ev.type) {
      case "phase":
        setPhase(ev.phase ?? "");
        break;
      case "collector":
        if (ev.collector) {
          setCollectors((prev) => {
            const next = prev.filter((c) => c.id !== ev.collector!.id);
            next.push(ev.collector!);
            return next;
          });
        }
        break;
      case "token":
        if (ev.text) setPartial((p) => p + ev.text);
        break;
      case "thinking":
        if (ev.text) setThinking((t) => t + ev.text);
        break;
      case "done":
        setJob({
          id,
          status: "done",
          markdown: ev.markdown,
          meta: ev.meta,
        });
        setBusy(false);
        setPhase("");
        break;
      case "error":
        setJob({ id, status: "error", error: ev.error ?? "report failed" });
        setBusy(false);
        setPhase("");
        toast(ev.error ?? "report failed", true);
        break;
    }
  }, []);

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
      if (runIdRef.current !== id) return;
      setJob(j);
      if (j.phase) setPhase(j.phase);
      if (j.partial) setPartial(j.partial);
      if (j.status === "done") {
        setBusy(false);
        setPhase("");
        return;
      }
      if (j.status === "error") {
        setBusy(false);
        setPhase("");
        toast(j.error ?? "report failed", true);
        return;
      }
      await sleep(POLL_MS);
    }
  }, []);

  const watch = useCallback(
    async (id: string) => {
      abortRef.current?.abort();
      const ac = new AbortController();
      abortRef.current = ac;
      let sawTerminal = false;
      try {
        await streamReport(id, {
          signal: ac.signal,
          onEvent: (ev) => {
            if (ev.type === "done" || ev.type === "error") sawTerminal = true;
            applyEvent(id, ev);
          },
        });
      } catch (e) {
        if (ac.signal.aborted || runIdRef.current !== id) return;
        // Fall back to polling so a report never appears stuck if SSE fails.
        toast("live progress unavailable; falling back to poll", true);
        void poll(id);
        return;
      }
      if (!sawTerminal && aliveRef.current && runIdRef.current === id) {
        void poll(id);
      }
    },
    [applyEvent, poll],
  );

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

    abortRef.current?.abort();
    setBusy(true);
    setJob(null);
    setPhase("starting");
    setCollectors([]);
    setPartial("");
    setThinking("");
    try {
      const id = await startReport(req);
      runIdRef.current = id;
      setJob({ id, status: "pending" });
      void watch(id);
    } catch (e) {
      setBusy(false);
      setPhase("");
      toast("failed to start report: " + errMsg(e), true);
    }
  }, [mode, days, scope, collect, prompt, promptRequired, watch]);

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
  const phaseLabel =
    PHASE_LABELS[phase] ?? (phase === "starting" ? "Starting…" : phase ? phase + "…" : "Generating…");

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
              onChange={(e) => onDaysChange(e.target.value)}
              disabled={busy}
            />
          </label>

          <label className="field">
            <span className="field-label">Scope</span>
            <select value={scope} onChange={(e) => onScopeChange(e.target.value)} disabled={busy}>
              {meta?.scopes.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </label>

          <label className="field">
            <span className="field-label">Collect</span>
            <select
              value={collect}
              onChange={(e) => onCollectChange(e.target.value)}
              disabled={busy}
            >
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
              onChange={(e) => onPromptChange(e.target.value)}
              disabled={busy}
            />
          </label>

          <div className="report-actions">
            <button type="submit" className="primary" disabled={!meta || busy}>
              {busy ? "Generating…" : "Generate"}
            </button>
            {running ? <span className="muted">{phaseLabel}</span> : null}
          </div>
        </form>
      </div>

      {running ? (
        <div className="section report-progress">
          <div className="section-head">
            <span className="title">Progress</span>
            <span className="muted">{phaseLabel}</span>
          </div>
          {collectors.length > 0 ? (
            <ul className="report-collectors">
              {collectors.map((c) => (
                <li key={c.id} className={"report-collector status-" + (c.status || "unknown")}>
                  <span className="report-collector-name">{c.description || c.id}</span>
                  <span className="muted">
                    {c.status}
                    {c.total && c.total > 0 ? ` · ${c.completed ?? 0}/${c.total}` : ""}
                    {c.detail ? ` · ${c.detail}` : ""}
                  </span>
                </li>
              ))}
            </ul>
          ) : (
            <div className="muted report-progress-empty">Waiting for collectors…</div>
          )}
          {thinking ? (
            <div ref={thinkingRef} className="muted report-thinking">
              {thinking}
            </div>
          ) : null}
          {partial ? (
            <div className="report-partial">
              <div className="section-head">
                <span className="title">Live preview</span>
              </div>
              <div ref={partialRef} className="markdown-body">
                <ReactMarkdown remarkPlugins={[remarkGfm]}>{partial}</ReactMarkdown>
              </div>
            </div>
          ) : null}
        </div>
      ) : null}

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
