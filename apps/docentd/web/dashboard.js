"use strict";

const POLL_MS = 5000;
const board = document.getElementById("board");
const statsEl = document.getElementById("stats");
const autoEl = document.getElementById("auto");
const refreshEl = document.getElementById("refresh");
const toastEl = document.getElementById("toast");

const WM_URL = (() => {
  const meta = document.querySelector('meta[name="docent-wm-url"]');
  if (meta && meta.content) return meta.content.replace(/\/$/, "");
  if (window.DOCENT_WM_URL) return String(window.DOCENT_WM_URL).replace(/\/$/, "");
  return "http://127.0.0.1:39788";
})();

let timer = null;
let lastOk = null;

function el(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
}

function toast(msg, isErr) {
  toastEl.textContent = msg;
  toastEl.classList.toggle("err", !!isErr);
  toastEl.classList.add("show");
  clearTimeout(toast._t);
  toast._t = setTimeout(() => toastEl.classList.remove("show"), 2200);
}

function timeAgo(iso) {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (isNaN(t)) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return Math.floor(s) + "s ago";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  return Math.floor(s / 86400) + "d ago";
}

async function focusSession(name, host) {
  try {
    const r = await fetch(WM_URL + "/focus", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, host: host || null }),
    });
    const d = await r.json().catch(() => ({}));
    if (r.ok && d.ok) toast("focused " + name);
    else if (r.status === 404) toast("no open window for " + name, true);
    else toast("focus failed: " + (d.error || r.status), true);
    setTimeout(load, 400);
  } catch (e) {
    toast("focus error: " + e.message, true);
  }
}

function renderSessionRow(s) {
  const clickable = s.live;
  const row = el(clickable ? "div" : "div", "row session" + (clickable ? " clickable" : ""));
  if (clickable) {
    row.title = "Focus this window";
    row.addEventListener("click", () => focusSession(s.name, s.host));
  }

  row.appendChild(el("span", "live" + (s.live ? " on" : "")));

  const name = el("span", "name");
  if (s.color) {
    const sw = el("span", "csw");
    sw.style.background = s.color;
    name.appendChild(sw);
  }
  name.appendChild(document.createTextNode(s.name));
  row.appendChild(name);

  if (s.host) row.appendChild(el("span", "chip", s.host));
  row.appendChild(el("span", "spacer"));

  if (s.needsFollowup) row.appendChild(el("span", "pill followup", "needs follow-up"));
  else if (s.status === "working") row.appendChild(el("span", "pill working", "working"));
  else row.appendChild(el("span", "pill status", s.live ? "idle" : "closed"));

  if (s.lastActivity) row.appendChild(el("span", "meta", timeAgo(s.lastActivity)));
  return row;
}

function renderPrRow(pr) {
  const row = el("a", "row pr clickable");
  row.href = pr.url || "#";
  row.target = "_blank";
  row.rel = "noopener";
  row.appendChild(el("span", "num", "#" + pr.prNumber));
  row.appendChild(el("span", "name", pr.title || "(untitled PR)"));
  if (pr.repo) row.appendChild(el("span", "chip", pr.repo));
  row.appendChild(el("span", "spacer"));
  if (pr.draft) row.appendChild(el("span", "pill draft", "draft"));
  row.appendChild(el("span", "pill state " + (pr.state || "").toLowerCase(), pr.state || "open"));
  return row;
}

function renderGroup(g) {
  const card = el("div", "group" + (g.needsFollowup ? " followup" : ""));
  if (g.color) card.style.setProperty("--g-color", g.color);

  const head = el("div", "group-head clickable");
  head.title = "Open work-item details";
  head.addEventListener("click", (e) => {
    // Let the Jira link handle its own click without navigating away.
    if (e.target.closest("a.ticket")) return;
    window.location.href = "/workitem?key=" + encodeURIComponent(g.key || g.ticket || "");
  });
  const sw = el("span", "swatch");
  head.appendChild(sw);

  if (g.ticket) {
    const t = g.jiraUrl ? el("a", "ticket link", g.ticket) : el("span", "ticket", g.ticket);
    if (g.jiraUrl) {
      t.href = g.jiraUrl;
      t.target = "_blank";
      t.rel = "noopener";
    }
    head.appendChild(t);
  } else {
    head.appendChild(el("span", "ticket untracked", "untracked"));
  }

  head.appendChild(el("span", "summary", g.summary || ""));
  if (g.jiraStatus) head.appendChild(el("span", "pill status", g.jiraStatus));
  if (g.needsFollowup) head.appendChild(el("span", "followup-dot"));
  card.appendChild(head);

  const rows = el("div", "rows");
  (g.sessions || []).forEach((s) => rows.appendChild(renderSessionRow(s)));
  (g.prs || []).forEach((pr) => rows.appendChild(renderPrRow(pr)));
  card.appendChild(rows);
  return card;
}

function render(data) {
  board.innerHTML = "";
  const groups = data.groups || [];
  if (groups.length === 0) {
    board.appendChild(el("div", "empty", "No sessions, tickets, or PRs yet."));
  } else {
    groups.forEach((g) => board.appendChild(renderGroup(g)));
  }

  const liveCount = groups.reduce(
    (n, g) => n + (g.sessions || []).filter((s) => s.live).length, 0);
  const followCount = groups.filter((g) => g.needsFollowup).length;

  statsEl.innerHTML = "";
  statsEl.appendChild(makeStat(liveCount, "live"));
  statsEl.appendChild(makeStat(groups.length, "groups"));
  if (followCount) statsEl.appendChild(makeStat(followCount, "need follow-up"));
  statsEl.appendChild(makeStat(timeAgo(data.generatedAt) || "now", ""));
}

function makeStat(value, label) {
  const s = el("span");
  s.appendChild(el("b", null, String(value)));
  if (label) s.appendChild(document.createTextNode(" " + label));
  return s;
}

async function load() {
  try {
    const r = await fetch("/sessions", { cache: "no-store" });
    if (!r.ok) throw new Error("HTTP " + r.status);
    const data = await r.json();
    lastOk = Date.now();
    render(data);
  } catch (e) {
    if (!lastOk) board.innerHTML = "";
    if (!lastOk) board.appendChild(el("div", "empty", "Cannot reach docent (" + e.message + ")."));
    toast("refresh failed: " + e.message, true);
  }
}

function schedule() {
  clearInterval(timer);
  if (autoEl.checked) timer = setInterval(load, POLL_MS);
}

refreshEl.addEventListener("click", () => {
  refreshEl.classList.add("spin");
  setTimeout(() => refreshEl.classList.remove("spin"), 400);
  load();
});
autoEl.addEventListener("change", schedule);
document.addEventListener("visibilitychange", () => {
  if (!document.hidden) load();
});

load();
schedule();
