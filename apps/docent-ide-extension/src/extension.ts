import * as vscode from "vscode";
import * as os from "os";
import * as http from "http";
import * as https from "https";
import { URL } from "url";

// The docent IDE extension owns editor-window lifecycle reporting: it posts
// open/close/focus/heartbeat session events to docentd's ingest API. Agent
// request/response events are reported separately by the Cursor shell hooks
// (those events are not exposed to the extension API).
//
// A session's identity is the composite of ide + ideHost + targetHost + path,
// matching docentd's registry key.

type SessionEvent =
  | "open"
  | "close"
  | "heartbeat";

interface Config {
  url: string;
  token: string;
  heartbeatMs: number;
}

function readConfig(): Config {
  const c = vscode.workspace.getConfiguration("docent");
  const seconds = c.get<number>("heartbeatSeconds", 30);
  return {
    url: (c.get<string>("url", "http://127.0.0.1:39787") || "").trim(),
    token: (c.get<string>("token", "") || "").trim(),
    heartbeatMs: Math.max(5, seconds) * 1000,
  };
}

// deriveIDE distinguishes Cursor from vanilla VS Code (and forks) via the
// application name, so docentd can key sessions by the actual editor.
function deriveIDE(): string {
  const app = (vscode.env.appName || "").toLowerCase();
  if (app.includes("cursor")) {
    return "cursor";
  }
  if (app.includes("windsurf")) {
    return "windsurf";
  }
  return "vscode";
}

// targetHost is the remote server the window edits, when this is a remote
// window (Remote-SSH, containers, WSL). Empty for a local window.
function deriveTargetHost(): string {
  return (vscode.env.remoteName || "").trim();
}

// currentPaths returns the workspace folder paths this window has open. A
// window with no folder yields a single empty path so it is still tracked.
function currentPaths(): string[] {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    return [""];
  }
  return folders.map((f) => f.uri.fsPath);
}

function leaf(p: string): string {
  const trimmed = p.replace(/[/\\]+$/, "");
  const idx = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
}

// postEvent fires a single session event at docentd. It is fire-and-forget with
// a short timeout: a slow or down docentd must never disrupt the editor.
function postEvent(cfg: Config, ide: string, ideHost: string, targetHost: string, path: string, event: SessionEvent): void {
  if (!cfg.url) {
    return;
  }
  let u: URL;
  try {
    u = new URL(cfg.url.replace(/\/$/, "") + "/api/sessions/events");
  } catch {
    return;
  }
  const bodyObj: Record<string, unknown> = { ide, ideHost, event };
  if (targetHost) {
    bodyObj.targetHost = targetHost;
  }
  if (path) {
    bodyObj.path = path;
    bodyObj.name = leaf(path);
  }
  const data = JSON.stringify(bodyObj);
  const mod = u.protocol === "https:" ? https : http;
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(data).toString(),
  };
  if (cfg.token) {
    headers["Authorization"] = "Bearer " + cfg.token;
  }
  const req = mod.request(
    {
      hostname: u.hostname,
      port: u.port || (u.protocol === "https:" ? 443 : 80),
      path: u.pathname,
      method: "POST",
      timeout: 2000,
      headers,
    },
    (res) => {
      // Drain and discard the response so the socket can be reused/closed.
      res.resume();
    },
  );
  req.on("error", () => {
    /* ignore: a down docentd must not disrupt the editor */
  });
  req.on("timeout", () => {
    req.destroy();
  });
  req.write(data);
  req.end();
}

export function activate(context: vscode.ExtensionContext): void {
  const ide = deriveIDE();
  const ideHost = os.hostname();

  const send = (event: SessionEvent): void => {
    const cfg = readConfig();
    const targetHost = deriveTargetHost();
    for (const p of currentPaths()) {
      postEvent(cfg, ide, ideHost, targetHost, p, event);
    }
  };

  // Report this window as open, then keep it alive with periodic heartbeats.
  send("open");

  let timer: NodeJS.Timeout = setInterval(() => send("heartbeat"), readConfig().heartbeatMs);

  // A focus change is a strong liveness signal; also re-arm the heartbeat timer
  // in case the interval config changed.
  context.subscriptions.push(
    vscode.window.onDidChangeWindowState((state) => {
      if (state.focused) {
        send("heartbeat");
      }
    }),
  );

  // Opening/closing folders changes which sessions this window represents.
  context.subscriptions.push(
    vscode.workspace.onDidChangeWorkspaceFolders((e) => {
      const cfg = readConfig();
      const targetHost = deriveTargetHost();
      for (const added of e.added) {
        postEvent(cfg, ide, ideHost, targetHost, added.uri.fsPath, "open");
      }
      for (const removed of e.removed) {
        postEvent(cfg, ide, ideHost, targetHost, removed.uri.fsPath, "close");
      }
    }),
  );

  // Re-arm the heartbeat when the interval setting changes.
  context.subscriptions.push(
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("docent.heartbeatSeconds")) {
        clearInterval(timer);
        timer = setInterval(() => send("heartbeat"), readConfig().heartbeatMs);
      }
    }),
  );

  context.subscriptions.push({ dispose: () => clearInterval(timer) });
  // Ensure a close is emitted on shutdown even if deactivate() is skipped.
  context.subscriptions.push({ dispose: () => send("close") });
}

export function deactivate(): void {
  const cfg = readConfig();
  const ide = deriveIDE();
  const ideHost = os.hostname();
  const targetHost = deriveTargetHost();
  for (const p of currentPaths()) {
    postEvent(cfg, ide, ideHost, targetHost, p, "close");
  }
}
