// Vite cannot bundle monaco-yaml's worker when it's referenced directly by
// bare specifier inside MonacoEnvironment.getWorker, so this thin re-export
// module is imported with the `?worker` suffix instead. See monaco-yaml's
// documented Vite workaround: https://github.com/remcohaszing/monaco-yaml#why-doesnt-it-work-with-vite
import "monaco-yaml/yaml.worker.js";
