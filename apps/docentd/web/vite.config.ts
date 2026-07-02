import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The dashboard is a pure client of docentd's JSON API. In dev we run the
// Vite dev server and proxy the API/data endpoints to a running docentd
// (default loopback :39787; override with DOCENTD_URL). In production the
// built dist/ is served same-origin by docentd itself, so no proxy applies.
const API_TARGET =
  (typeof process !== "undefined" && process.env && process.env.DOCENTD_URL) ||
  "http://127.0.0.1:39787";

const proxied = ["/api", "/sessions", "/ingest", "/health"];

export default defineConfig({
  plugins: [react()],
  base: "/",
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: Object.fromEntries(
      proxied.map((path) => [path, { target: API_TARGET, changeOrigin: true }]),
    ),
  },
});
