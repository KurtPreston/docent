import { useCallback, useEffect, useState } from "react";
import type { JSONSchema } from "monaco-yaml";
import { Layout } from "../components/Layout";
import { ConfigEditor } from "../components/ConfigEditor";
import { fetchConfigFiles, fetchConfigSchema, saveConfigFile } from "../lib/api";
import { errMsg } from "../lib/format";
import { toast } from "../lib/toast";
import type { ConfigFileID, ConfigFileView } from "../lib/types";

// Settings edits docentd's two YAML config files with a Monaco editor: the
// daemon re-reads them from disk only at startup, so this page writes
// verbatim content (preserving comments) and relies on server-side
// validation for correctness. When a file has a JSON Schema (config.yaml
// today; docentd.yaml in a later phase), the editor gets inline
// validation/completion for it via monaco-yaml. A schema-driven form view
// comes in a later phase too.
export function Settings() {
  const [files, setFiles] = useState<ConfigFileView[] | null>(null);
  const [activeId, setActiveId] = useState<ConfigFileID>("config");
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [problems, setProblems] = useState<Record<string, string[]>>({});
  const [justSaved, setJustSaved] = useState<Record<string, boolean>>({});
  const [saving, setSaving] = useState(false);
  const [schemas, setSchemas] = useState<Record<string, JSONSchema | null>>({});

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

  // Lazily fetch (and cache) each file's JSON Schema the first time it's
  // viewed; fetchConfigSchema resolves to null when the file has none.
  useEffect(() => {
    if (!active || active.id in schemas) return;
    const id = active.id;
    void fetchConfigSchema(id)
      .then((schema) => setSchemas((prev) => ({ ...prev, [id]: schema })))
      .catch(() => setSchemas((prev) => ({ ...prev, [id]: null })));
  }, [active, schemas]);
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
            {!active.exists ? (
              <div className="muted config-new-notice">This file doesn't exist yet — saving will create it.</div>
            ) : null}
            <div className="config-editor-frame">
              <ConfigEditor
                id={active.id}
                value={draft}
                onChange={(v) => editDraft(active.id, v)}
                schema={schemas[active.id] ?? undefined}
              />
            </div>
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
