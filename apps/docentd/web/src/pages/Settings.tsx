import { useCallback, useEffect, useState } from "react";
import { Layout } from "../components/Layout";
import { fetchConfigFiles, saveConfigFile } from "../lib/api";
import { errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { ConfigFileID, ConfigFileView } from "../lib/types";

// Settings edits docentd's two YAML config files as plain text: the daemon
// re-reads them from disk only at startup, so this page writes verbatim
// content (preserving comments) and relies on server-side validation rather
// than any client-side schema awareness. A richer editor (Monaco) and a
// schema-driven form view come in later phases.
export function Settings() {
  const [files, setFiles] = useState<ConfigFileView[] | null>(null);
  const [activeId, setActiveId] = useState<ConfigFileID>("config");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [problems, setProblems] = useState<Record<string, string[]>>({});
  const [justSaved, setJustSaved] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    document.title = "docent · settings";
  }, []);

  const load = useCallback(async () => {
    try {
      const loaded = await fetchConfigFiles();
      setFiles(loaded);
      setDrafts((prev) => {
        const next = { ...prev };
        for (const f of loaded) if (!(f.id in next)) next[f.id] = f.content;
        return next;
      });
      setActiveId((cur) => (loaded.some((f) => f.id === cur) ? cur : loaded[0]?.id ?? cur));
    } catch (e) {
      toast("failed to load config: " + errMsg(e), true);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const active = files?.find((f) => f.id === activeId);
  const draft = drafts[activeId] ?? "";
  const dirty = active !== undefined && draft !== active.content;
  const activeProblems = problems[activeId] ?? [];

  const editDraft = useCallback((id: string, value: string) => {
    setDrafts((prev) => ({ ...prev, [id]: value }));
    setProblems((prev) => ({ ...prev, [id]: [] }));
    setJustSaved((prev) => ({ ...prev, [id]: false }));
  }, []);

  const revert = useCallback(() => {
    if (!active) return;
    editDraft(active.id, active.content);
  }, [active, editDraft]);

  const save = useCallback(async () => {
    if (!active) return;
    setSaving(true);
    try {
      const result = await saveConfigFile(active.id, draft);
      if (result.ok) {
        setProblems((prev) => ({ ...prev, [active.id]: [] }));
        setJustSaved((prev) => ({ ...prev, [active.id]: true }));
        setFiles((prev) => prev?.map((f) => (f.id === active.id ? { ...f, content: draft, exists: true } : f)) ?? prev);
        toast("saved " + active.label);
      } else {
        setProblems((prev) => ({ ...prev, [active.id]: result.problems ?? [result.error ?? "save failed"] }));
        toast("save failed: " + active.label, true);
      }
    } catch (e) {
      toast("save failed: " + errMsg(e), true);
    } finally {
      setSaving(false);
    }
  }, [active, draft]);

  return (
    <Layout mainClass="wrap">
      <div className="section">
        <div className="section-head">
          <div className="settings-tabs">
            {files?.map((f) => (
              <button
                key={f.id}
                type="button"
                className={"settings-tab" + (f.id === activeId ? " active" : "")}
                onClick={() => setActiveId(f.id)}
              >
                {f.label}
                {!f.exists ? <em className="muted"> (new)</em> : null}
              </button>
            ))}
          </div>
          <span className="grow" />
          {active ? <span className="muted mono">{active.path}</span> : null}
        </div>

        {active ? (
          <div className="config-editor-wrap">
            <textarea
              className="config-editor"
              value={draft}
              onChange={(e) => editDraft(active.id, e.target.value)}
              spellCheck={false}
              placeholder={active.exists ? undefined : "This file doesn't exist yet — saving will create it."}
            />
            {activeProblems.length > 0 ? (
              <ul className="config-problems">
                {activeProblems.map((p, i) => (
                  <li key={i}>{p}</li>
                ))}
              </ul>
            ) : null}
            <div className="config-actions">
              <button type="button" className="primary" disabled={!dirty || saving} onClick={() => void save()}>
                {saving ? "Saving…" : "Save"}
              </button>
              <button type="button" disabled={!dirty || saving} onClick={revert}>
                Revert
              </button>
              {justSaved[active.id] ? (
                <span className="muted">Saved — restart docentd to apply changes.</span>
              ) : dirty ? (
                <span className="muted">Unsaved changes</span>
              ) : null}
            </div>
          </div>
        ) : (
          <div className="empty">Loading…</div>
        )}
      </div>
    </Layout>
  );
}
