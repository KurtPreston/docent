import { useEffect } from "react";
import Editor, { type OnChange } from "@monaco-editor/react";
import type { JSONSchema } from "monaco-yaml";
import { configureYamlSchemas, monacoTheme } from "../lib/monacoSetup";

type Props = {
  id: string;
  value: string;
  onChange: (value: string) => void;
  /** JSON Schema for this file's contents, when one exists (config.yaml only, for now). */
  schema?: JSONSchema;
};

// modelUri gives each config file its own Monaco model (so switching tabs
// keeps separate undo history) without the URI needing to resolve to
// anything real; monaco-yaml also uses it to scope schema fileMatch.
function modelUri(id: string): string {
  return `docent://config/${id}.yaml`;
}

export function ConfigEditor({ id, value, onChange, schema }: Props) {
  useEffect(() => {
    configureYamlSchemas({
      schemas: schema ? [{ uri: `docent://config-schema/${id}`, fileMatch: [modelUri(id)], schema }] : [],
    });
  }, [id, schema]);

  const handleChange: OnChange = (v) => onChange(v ?? "");

  return (
    <Editor
      path={modelUri(id)}
      defaultLanguage="yaml"
      theme={monacoTheme}
      value={value}
      onChange={handleChange}
      height="560px"
      options={{
        minimap: { enabled: false },
        fontSize: 12.5,
        fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
        tabSize: 2,
        scrollBeyondLastLine: false,
        automaticLayout: true,
      }}
    />
  );
}
