"use strict";

const board = document.getElementById("board");
const statsEl = document.getElementById("stats");
const refreshEl = document.getElementById("refresh");
const toastEl = document.getElementById("toast");

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

function workItemLink(key) {
  const a = el("a", null, key);
  a.href = "/workitem?key=" + encodeURIComponent(key);
  return a;
}

function renderUnit(u) {
  const sec = el("div", "section");
  const head = el("div", "section-head");
  head.appendChild(el("span", "title", u.directiveId));
  head.appendChild(el("span", "badge " + u.mode, u.collector + " · " + u.mode));
  head.appendChild(el("span", "badge", u.count + " signals"));
  if (u.lastRun) head.appendChild(el("span", "muted", "ran " + timeAgo(u.lastRun)));
  if (u.lastErr) head.appendChild(el("span", "err", u.lastErr));
  sec.appendChild(head);

  if (!u.signals || u.signals.length === 0) {
    sec.appendChild(el("div", "wrap muted", "No signals."));
    return sec;
  }

  const tbl = el("table", "tbl");
  const thead = el("tr");
  ["Kind", "Title", "Observed", "Entity", "Work item"].forEach((h) => thead.appendChild(el("th", null, h)));
  tbl.appendChild(thead);
  u.signals.forEach((s) => {
    const tr = el("tr");
    tr.appendChild(el("td", "mono", s.kind));
    const titleTd = el("td");
    if (s.url) {
      const a = el("a", null, s.title || "(untitled)");
      a.href = s.url; a.target = "_blank"; a.rel = "noopener";
      titleTd.appendChild(a);
    } else {
      titleTd.appendChild(document.createTextNode(s.title || "(untitled)"));
    }
    if (s.summary) {
      const sm = el("div", "muted");
      sm.textContent = s.summary;
      titleTd.appendChild(sm);
    }
    tr.appendChild(titleTd);
    tr.appendChild(el("td", "muted", timeAgo(s.observedAt)));
    tr.appendChild(el("td", "mono", s.entityId || ""));
    const wiTd = el("td");
    if (s.workItemKey) wiTd.appendChild(workItemLink(s.workItemKey));
    else wiTd.appendChild(el("span", "muted", "—"));
    tr.appendChild(wiTd);
    tbl.appendChild(tr);
  });
  sec.appendChild(tbl);
  return sec;
}

function render(data) {
  board.innerHTML = "";
  const units = data.units || [];
  if (units.length === 0) {
    board.appendChild(el("div", "empty", "No collection units configured."));
  } else {
    units.forEach((u) => board.appendChild(renderUnit(u)));
  }
  const total = units.reduce((n, u) => n + (u.count || 0), 0);
  statsEl.innerHTML = "";
  const a = el("span"); a.appendChild(el("b", null, String(total))); a.appendChild(document.createTextNode(" signals"));
  statsEl.appendChild(a);
  const b = el("span"); b.appendChild(el("b", null, String(units.length))); b.appendChild(document.createTextNode(" units"));
  statsEl.appendChild(b);
}

async function load() {
  try {
    const r = await fetch("/api/signals", { cache: "no-store" });
    if (!r.ok) throw new Error("HTTP " + r.status);
    render(await r.json());
  } catch (e) {
    toast("refresh failed: " + e.message, true);
  }
}

refreshEl.addEventListener("click", () => {
  refreshEl.classList.add("spin");
  setTimeout(() => refreshEl.classList.remove("spin"), 400);
  load();
});

load();
