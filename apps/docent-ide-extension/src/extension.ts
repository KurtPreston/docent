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
//
// This is declared as a UI extension ("extensionKind": ["ui"] in package.json),
// so it always runs in the client-side (local) extension host — the machine
// with the GUI — even for Remote-SSH windows. That makes os.hostname() the host
// you actually sit at (the ideHost), while the remote server being edited is
// derived separately from the workspace folder's remote authority (targetHost).
// Reaching docentd still works from the client because docent.url points at a
// tunnel/loopback that forwards to the daemon.

type SessionEvent =
  | "open"
  | "close"
  | "focus"
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

// A folder this window has open, paired with the remote host it lives on and
// the verbatim remote authority that identifies it to the editor.
interface FolderTarget {
  path: string;
  targetHost: string;
  remoteAuthority: string;
}

// remoteAuthorityFor returns the authority verbatim, exactly as this window
// spells it ("" for a local folder).
//
// This is what a cursor:// deep link has to carry to reach *this* window.
// The editor decides whether a link targets an already-open window by comparing
// whole workspace URIs, and the authority is part of that comparison — so
// `ssh-remote+{"hostName":"desktop"}` (hex-encoded, the form recent Cursor
// builds produce) and `ssh-remote+desktop` are two different workspaces even
// though both resolve to the same box over the same ssh alias. Rebuilding the
// authority from the friendly host name therefore produces a link that always
// opens a second window onto the folder already open in the first.
function remoteAuthorityFor(uri: vscode.Uri): string {
  return uri.scheme === "vscode-remote" ? uri.authority || "" : "";
}

// targetHostFor derives the remote server a folder lives on ("" for a local
// folder). Remote windows (Remote-SSH, containers, WSL) expose a vscode-remote
// URI whose authority encodes the remote as "<resolver>+<host>"
// (e.g. "ssh-remote+desktop"); local folders have no authority. We read the URI
// authority rather than vscode.env.remoteName because remoteName only reports
// the resolver *kind* ("ssh-remote"), not which host.
//
// Unlike remoteAuthorityFor this is the *friendly* host: the ssh alias
// `cursor --status` shows as "[SSH: <host>]", which is what makes the
// target-host column readable and what docentd falls back to when it has to
// build a link for a folder no window has open.
function targetHostFor(uri: vscode.Uri): string {
  if (uri.scheme !== "vscode-remote" || !uri.authority) {
    return "";
  }
  return hostFromRemoteAuthority(uri.authority);
}

// hostFromRemoteAuthority extracts the target host from a remote authority of
// the form "<resolver>+<host>" (e.g. "ssh-remote+desktop", "wsl+Ubuntu"). Some
// editor builds hex-encode the host segment, and newer Cursor/VS Code builds
// encode it as a JSON object such as {"hostName":"desktop"} rather than a bare
// label. We decode the hex when it round-trips to printable text, then pull the
// host out of the JSON form, so the ssh alias is reported consistently instead
// of leaking a raw JSON blob into the target-host column and the deep link.
function hostFromRemoteAuthority(authority: string): string {
  const plus = authority.indexOf("+");
  const seg = plus >= 0 ? authority.slice(plus + 1) : authority;

  let raw = seg;
  if (seg.length >= 4 && seg.length % 2 === 0 && /^[0-9a-fA-F]+$/.test(seg)) {
    try {
      const decoded = Buffer.from(seg, "hex").toString("utf8");
      if (/^[\x20-\x7E]+$/.test(decoded)) {
        raw = decoded;
      }
    } catch {
      /* keep the raw segment */
    }
  }

  return hostFromJSON(raw) || raw;
}

// hostFromJSON returns the ssh host when s is a JSON object like
// {"hostName":"desktop"} (the form recent Cursor/VS Code builds encode into the
// remote authority), or "" when s is not such an object.
function hostFromJSON(s: string): string {
  const t = s.trim();
  if (!t.startsWith("{")) {
    return "";
  }
  try {
    const obj = JSON.parse(t) as Record<string, unknown>;
    const v = obj.hostName ?? obj.host ?? obj.name;
    if (typeof v === "string" && v.trim() !== "") {
      return v.trim();
    }
  } catch {
    /* not JSON: fall back to the raw value */
  }
  return "";
}

// pathFor returns the workspace path as the machine that owns it spells it.
//
// This matters because a session's identity includes its path, and the other
// reporter for the same window — the Cursor agent hook — runs on the *remote*
// box and can only ever report a POSIX path. Since this extension runs
// client-side, uri.fsPath renders a remote path in the client's local
// convention: on Windows, /home/me/Code/x becomes \home\me\Code\x. The two
// reporters then never agree, docentd files them as separate sessions, and agent
// status silently detaches from the window it belongs to. uri.path keeps the
// remote's own spelling. Local folders still use fsPath, where the platform
// convention is the correct one (c:\Users\me, not /c:/Users/me).
function pathFor(uri: vscode.Uri): string {
  return uri.scheme === "vscode-remote" ? uri.path : uri.fsPath;
}

function folderTarget(uri: vscode.Uri): FolderTarget {
  return {
    path: pathFor(uri),
    targetHost: targetHostFor(uri),
    remoteAuthority: remoteAuthorityFor(uri),
  };
}

// currentFolders returns the workspace folders this window has open, each with
// its remote host. A window with no folder yields a single empty entry so it is
// still tracked.
function currentFolders(): FolderTarget[] {
  const folders = vscode.workspace.workspaceFolders;
  if (!folders || folders.length === 0) {
    return [{ path: "", targetHost: "", remoteAuthority: "" }];
  }
  return folders.map((f) => folderTarget(f.uri));
}

function leaf(p: string): string {
  const trimmed = p.replace(/[/\\]+$/, "");
  const idx = Math.max(trimmed.lastIndexOf("/"), trimmed.lastIndexOf("\\"));
  return idx >= 0 ? trimmed.slice(idx + 1) : trimmed;
}

// postEvent fires a single session event at docentd. It is fire-and-forget with
// a short timeout: a slow or down docentd must never disrupt the editor.
function postEvent(cfg: Config, ide: string, ideHost: string, folder: FolderTarget, event: SessionEvent): void {
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
  if (folder.targetHost) {
    bodyObj.targetHost = folder.targetHost;
  }
  if (folder.remoteAuthority) {
    bodyObj.remoteAuthority = folder.remoteAuthority;
  }
  if (folder.path) {
    bodyObj.path = folder.path;
    bodyObj.name = leaf(folder.path);
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
    for (const f of currentFolders()) {
      postEvent(cfg, ide, ideHost, f, event);
    }
  };

  // Report this window as open, then keep it alive with periodic heartbeats.
  send("open");

  let timer: NodeJS.Timeout = setInterval(() => send("heartbeat"), readConfig().heartbeatMs);

  // Focusing the window means the user has seen it: report a "focus" event so
  // docentd can clear a pending needs-followup (it also counts as liveness).
  context.subscriptions.push(
    vscode.window.onDidChangeWindowState((state) => {
      if (state.focused) {
        send("focus");
      }
    }),
  );

  // Opening/closing folders changes which sessions this window represents.
  context.subscriptions.push(
    vscode.workspace.onDidChangeWorkspaceFolders((e) => {
      const cfg = readConfig();
      for (const added of e.added) {
        postEvent(cfg, ide, ideHost, folderTarget(added.uri), "open");
      }
      for (const removed of e.removed) {
        postEvent(cfg, ide, ideHost, folderTarget(removed.uri), "close");
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
  for (const f of currentFolders()) {
    postEvent(cfg, ide, ideHost, f, "close");
  }
}
