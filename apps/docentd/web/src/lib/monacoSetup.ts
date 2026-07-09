// One-time Monaco setup shared by every ConfigEditor instance: point
// @monaco-editor/react at the bundled monaco-editor package (instead of its
// default CDN loader, since docentd is a local-first tool that should work
// offline), wire up web workers for Vite, define a theme matching the
// dashboard's dark palette, and expose a singleton monaco-yaml instance for
// per-file schema configuration.
import { loader } from "@monaco-editor/react";
// Import the trimmed editor core rather than the "monaco-editor" barrel,
// which eagerly bundles every language Monaco ships (Python, Go, C#, ...).
// monaco-yaml supplies validation/completion/hover for "yaml"; this
// contribution just adds the basic Monarch tokenizer so keys/strings/
// comments get syntax-highlighted.
import * as monaco from "monaco-editor/esm/vs/editor/editor.api.js";
import "monaco-editor/esm/vs/basic-languages/yaml/yaml.contribution.js";
import EditorWorker from "monaco-editor/esm/vs/editor/editor.worker?worker";
import { configureMonacoYaml, type MonacoYaml, type MonacoYamlOptions } from "monaco-yaml";
import YamlWorker from "./yamlWorker?worker";

loader.config({ monaco });

globalThis.MonacoEnvironment = {
  getWorker(_workerId, label) {
    if (label === "yaml") return new YamlWorker();
    return new EditorWorker();
  },
};

monaco.editor.defineTheme("docent-dark", {
  base: "vs-dark",
  inherit: true,
  rules: [],
  colors: {
    "editor.background": "#1e222e",
    "editor.foreground": "#e6e8ef",
    "editor.lineHighlightBackground": "#272b3855",
    "editorLineNumber.foreground": "#6b7080",
    "editorLineNumber.activeForeground": "#9aa0b4",
    "editorCursor.foreground": "#7aa2f7",
    "editor.selectionBackground": "#7aa2f74d",
    "editorGutter.background": "#1e222e",
  },
});

export const monacoTheme = "docent-dark";

// monaco-yaml supports exactly one configured instance at a time; every
// ConfigEditor shares this singleton and reconfigures its schema list via
// update() as the active file/schema changes.
let monacoYaml: MonacoYaml | undefined;

export function configureYamlSchemas(options: MonacoYamlOptions): void {
  if (!monacoYaml) {
    monacoYaml = configureMonacoYaml(monaco, {
      validate: true,
      hover: true,
      completion: true,
      ...options,
    });
    return;
  }
  void monacoYaml.update(options);
}

export { monaco };
