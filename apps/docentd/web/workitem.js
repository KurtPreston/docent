"use strict";

const board = document.getElementById("board");
const statsEl = document.getElementById("stats");
const refreshEl = document.getElementById("refresh");
const toastEl = document.getElementById("toast");

const WM_URL = (() => {
  const meta = document.querySelector('meta[name="docent-wm-url"]');
  if (meta && meta.content) return meta.content.replace(/\/$/, "");
  return "http://127.0.0.1:39788";
})();

const KEY = new URLSearchParams(window.location.search).get("key") || "";

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
  } catch (e) {
    toast("focus error: " + e.message, true);
  }
}

function section(title) {
  const sec = el("div", "section");
  const head = el("div", "section-head");
  head.appendChild(el("span", "title", title));
  sec.appendChild(head);
  return sec;
}

function renderSession(s) {
  const row = el("div", "row session" + (s.live ? " clickable" : ""));
  if (s.live) {
    row.title = "Focus this window";
    row.addEventListener("click", () => focusSession(s.name, s.host));
  }
  row.appendChild(el("span", "live" + (s.live ? " on" : "")));
  row.appendChild(el("span", "name", s.name));
  if (s.host) row.appendChild(el("span", "chip", s.host));
  row.appendChild(el("span", "spacer"));
  if (s.needsFollowup) row.appendChild(el("span", "pill followup", "needs follow-up"));
  else row.appendChild(el("span", "pill status", s.status || (s.live ? "idle" : "closed")));
  if (s.lastActivity) row.appendChild(el("span", "meta", timeAgo(s.lastActivity)));
  return row;
}

function renderPr(pr) {
  const row = el("a", "row pr clickable");
  row.href = pr.url || "#";
  row.target = "_blank";
  row.rel = "noopener";
  row.appendChild(el("span", "name", pr.title || "(untitled PR)"));
  if (pr.repo) row.appendChild(el("span", "chip", pr.repo));
  row.appendChild(el("span", "spacer"));
  if (pr.state) row.appendChild(el("span", "pill state " + pr.state.toLowerCase(), pr.state));
  return row;
}

function render(d) {
  board.className = "detail-grid";
  board.innerHTML = "";

  const overview = section("Overview");
  const kv = el("div", "wrap");
  const addKV = (k, v, link) => {
    const row = el("div", "kv");
    row.appendChild(el("span", "k", k));
    if (link) {
      const a = el("a", null, v);
      a.href = link; a.target = "_blank"; a.rel = "noopener";
      row.appendChild(a);
    } else {
      row.appendChild(el("span", null, v));
    }
    kv.appendChild(row);
  };
  addKV("Key", d.key);
  if (d.summary || d.title) addKV("Summary", d.summary || d.title);
  if (d.jiraStatus) addKV("Status", d.jiraStatus);
  if (d.jiraUrl) addKV("Jira", d.key, d.jiraUrl);
  overview.appendChild(kv);
  board.appendChild(overview);

  const sessions = section("Sessions (" + (d.sessions || []).length + ")");
  const srows = el("div", "rows");
  (d.sessions || []).forEach((s) => srows.appendChild(renderSession(s)));
  if ((d.sessions || []).length === 0) srows.appendChild(el("div", "wrap muted", "No sessions."));
  sessions.appendChild(srows);
  board.appendChild(sessions);

  const prs = section("Pull requests (" + (d.prs || []).length + ")");
  const prows = el("div", "rows");
  (d.prs || []).forEach((pr) => prows.appendChild(renderPr(pr)));
  if ((d.prs || []).length === 0) prows.appendChild(el("div", "wrap muted", "No pull requests."));
  prs.appendChild(prows);
  board.appendChild(prs);

  const entities = section("Entities (" + (d.entities || []).length + ")");
  const etbl = el("table", "tbl");
  const eh = el("tr");
  ["Kind", "Title", "ID"].forEach((h) => eh.appendChild(el("th", null, h)));
  etbl.appendChild(eh);
  (d.entities || []).forEach((e) => {
    const tr = el("tr");
    tr.appendChild(el("td", "mono", e.kind));
    const td = el("td");
    if (e.url) { const a = el("a", null, e.title); a.href = e.url; a.target = "_blank"; a.rel = "noopener"; td.appendChild(a); }
    else td.appendChild(document.createTextNode(e.title));
    tr.appendChild(td);
    tr.appendChild(el("td", "mono", e.id));
    etbl.appendChild(tr);
  });
  entities.appendChild(etbl);
  board.appendChild(entities);

  const signals = section("Contributing signals (" + (d.signals || []).length + ")");
  const stbl = el("table", "tbl");
  const sh = el("tr");
  ["Kind", "Title", "Observed"].forEach((h) => sh.appendChild(el("th", null, h)));
  stbl.appendChild(sh);
  (d.signals || []).forEach((s) => {
    const tr = el("tr");
    tr.appendChild(el("td", "mono", s.kind));
    const td = el("td");
    if (s.url) { const a = el("a", null, s.title || "(untitled)"); a.href = s.url; a.target = "_blank"; a.rel = "noopener"; td.appendChild(a); }
    else td.appendChild(document.createTextNode(s.title || "(untitled)"));
    tr.appendChild(td);
    tr.appendChild(el("td", "muted", timeAgo(s.observedAt)));
    stbl.appendChild(tr);
  });
  signals.appendChild(stbl);
  board.appendChild(signals);

  statsEl.innerHTML = "";
  statsEl.appendChild(el("span", null, d.key));
}

async function load() {
  if (!KEY) {
    board.className = "";
    board.innerHTML = "";
    board.appendChild(el("div", "empty", "No work item key provided."));
    return;
  }
  try {
    const r = await fetch("/api/workitems/" + encodeURIComponent(KEY), { cache: "no-store" });
    if (r.status === 404) {
      board.className = "";
      board.innerHTML = "";
      board.appendChild(el("div", "empty", "Work item " + KEY + " not found."));
      return;
    }
    if (!r.ok) throw new Error("HTTP " + r.status);
    render(await r.json());
  } catch (e) {
    toast("load failed: " + e.message, true);
  }
}

refreshEl.addEventListener("click", () => {
  refreshEl.classList.add("spin");
  setTimeout(() => refreshEl.classList.remove("spin"), 400);
  load();
});

load();
