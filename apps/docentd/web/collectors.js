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
  if (!iso) return "never";
  const t = Date.parse(iso);
  if (isNaN(t)) return "";
  const s = (Date.now() - t) / 1000;
  const abs = Math.abs(s);
  let label;
  if (abs < 60) label = Math.floor(abs) + "s";
  else if (abs < 3600) label = Math.floor(abs / 60) + "m";
  else if (abs < 86400) label = Math.floor(abs / 3600) + "h";
  else label = Math.floor(abs / 86400) + "d";
  return s >= 0 ? label + " ago" : "in " + label;
}

async function collectNow(directive, mode, btn) {
  btn.disabled = true;
  const original = btn.textContent;
  btn.textContent = "collecting…";
  try {
    const r = await fetch("/api/units/" + encodeURIComponent(directive) + "/" + encodeURIComponent(mode) + "/collect", {
      method: "POST",
    });
    const d = await r.json().catch(() => ({}));
    if (r.ok && d.ok) toast("collected " + directive + "/" + mode);
    else toast("collect failed: " + (d.error || r.status), true);
  } catch (e) {
    toast("collect error: " + e.message, true);
  } finally {
    btn.disabled = false;
    btn.textContent = original;
    load();
  }
}

function render(data) {
  board.innerHTML = "";
  const units = data.units || [];
  const sec = el("div", "section");
  const head = el("div", "section-head");
  head.appendChild(el("span", "title", "Collection units"));
  head.appendChild(el("span", "grow"));
  if (data.generatedAt) head.appendChild(el("span", "muted", "snapshot " + timeAgo(data.generatedAt)));
  sec.appendChild(head);

  if (units.length === 0) {
    sec.appendChild(el("div", "wrap muted", "No collection units configured."));
    board.appendChild(sec);
    return;
  }

  const tbl = el("table", "tbl");
  const thead = el("tr");
  ["Directive", "Collector", "Mode", "Interval", "On request", "On load", "Last run", "Next due", "Items", "Error", ""].forEach((h) => thead.appendChild(el("th", null, h)));
  tbl.appendChild(thead);
  units.forEach((u) => {
    const tr = el("tr");
    tr.appendChild(el("td", null, u.directiveId));
    tr.appendChild(el("td", "mono", u.collector));
    tr.appendChild(el("td", null, "")).appendChild(el("span", "badge " + u.mode, u.mode));
    tr.appendChild(el("td", "muted", u.interval || "—"));
    tr.appendChild(el("td", "muted", u.onRequest ? "yes" : "—"));
    tr.appendChild(el("td", "muted", u.onLoad ? "yes" : "—"));
    tr.appendChild(el("td", "muted", timeAgo(u.lastRun)));
    tr.appendChild(el("td", "muted", u.nextDue ? timeAgo(u.nextDue) : "—"));
    tr.appendChild(el("td", null, String(u.itemCount)));
    tr.appendChild(el("td", "err", u.lastErr || ""));
    const actTd = el("td");
    const btn = el("button", null, "collect");
    btn.addEventListener("click", () => collectNow(u.directiveId, u.mode, btn));
    actTd.appendChild(btn);
    tr.appendChild(actTd);
    tbl.appendChild(tr);
  });
  sec.appendChild(tbl);
  board.appendChild(sec);

  statsEl.innerHTML = "";
  const a = el("span"); a.appendChild(el("b", null, String(units.length))); a.appendChild(document.createTextNode(" units"));
  statsEl.appendChild(a);
}

async function load() {
  try {
    const r = await fetch("/api/collectors", { cache: "no-store" });
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
