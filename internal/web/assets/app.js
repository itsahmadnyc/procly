"use strict";

// procly web panel. Hash routes, plain DOM, no framework. Every value from
// the server is written with textContent, never innerHTML, so process names
// and log lines can't inject markup.

const POLL_MS = 2000;
const HISTORY = 60;      // samples kept per process for charts (2 minutes)
const LOG_KEEP = 5000;   // log lines kept in the browser

const $ = (id) => document.getElementById(id);

// Attributes that navigate or load something stay inside the panel: a route
// (#/...), a path on this server (/...) or a blob: download. Nothing from the
// server can ever become a javascript: or off-site URL, even if a future
// change passes it here by mistake. A value that fails is left out.
const URL_ATTRS = new Set(["href", "src", "action", "formaction", "xlink:href"]);
const insidePanel = (v) => /^(#|\/(?!\/)|blob:)/.test(String(v));

// h("div", {class, text, onclick, ...attrs}, ...children)
function h(tag, props, ...kids) {
  const e = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue;
      if (k === "class") e.className = v;
      else if (k === "text") e.textContent = v;
      else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
      else if (URL_ATTRS.has(k) && !insidePanel(v)) continue;
      else e.setAttribute(k, v === true ? "" : String(v));
    }
  }
  for (const c of kids.flat(3)) {
    if (c == null || c === false) continue;
    e.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return e;
}

// fill sets el's children to kids, skipping null, false and "" so optional
// parts can be written inline. Pages redraw every poll, so instead of
// replacing nodes it patches the existing ones: buttons under the pointer
// survive a refresh, hover and focus stay, and a click is never lost.
// Nodes that carry a click handler must have a data-key, so a kept node is
// never reused for different data.
function fill(el, ...kids) {
  kids = kids.flat(3).filter((k) => k != null && k !== false && k !== "")
    .map((k) => (k instanceof Node ? k : document.createTextNode(String(k))));
  if (kids.some((k) => k.nodeType === Node.DOCUMENT_FRAGMENT_NODE)) { el.replaceChildren(...kids); return; }
  for (let i = 0; i < kids.length; i++) {
    const old = el.childNodes[i];
    if (!old) el.appendChild(kids[i]); // always a Node here, never a string
    else morph(old, kids[i]);
  }
  while (el.childNodes.length > kids.length) el.lastChild.remove();
}

function morph(from, to) {
  if (from.nodeType !== to.nodeType || from.nodeName !== to.nodeName ||
      (from.nodeType === Node.ELEMENT_NODE && from.getAttribute("data-key") !== to.getAttribute("data-key"))) {
    from.replaceWith(to);
    return;
  }
  if (from.nodeType === Node.TEXT_NODE) {
    if (from.data !== to.data) from.data = to.data;
    return;
  }
  for (const a of [...from.attributes]) if (!to.hasAttribute(a.name)) from.removeAttribute(a.name);
  for (const a of [...to.attributes]) {
    if (from.getAttribute(a.name) === a.value) continue;
    // Inline styles go through CSSOM; the CSP blocks style attributes.
    if (a.name === "style") from.style.cssText = to.style.cssText;
    else if (URL_ATTRS.has(a.name) && !insidePanel(a.value)) continue;
    else from.setAttribute(a.name, a.value);
  }
  if (from.isEqualNode(to)) return;
  fill(from, ...to.childNodes);
}

const S = {
  info: { host: "", home: "/", version: "" },
  servers: [],
  server: load("procly.server") || "local",
  procs: new Map(),  // server -> {list, error}
  system: new Map(), // server -> {data, error}
  hist: new Map(),   // key(server, name) -> {pid, cpu: [], mem: []}
  busy: new Map(),   // key(server, name) -> verb
  connected: true,
  timer: null,
  tick: 0,
};

const key = (server, name) => server + "\u0000" + name;

function load(k) { try { return localStorage.getItem(k); } catch { return null; } }
function store(k, v) { try { v == null ? localStorage.removeItem(k) : localStorage.setItem(k, v); } catch { /* private mode */ } }

// ---------- api ----------

class AuthError extends Error {}

async function api(method, path, body) {
  const opts = { method, headers: { "X-Procly": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    const err = new Error("Can't reach the panel server.");
    err.offline = true;
    throw err;
  }
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (res.status === 401 && path !== "/api/login") throw new AuthError();
  if (!res.ok) {
    const err = new Error((data && data.error) || res.statusText);
    err.status = res.status;
    throw err;
  }
  return data;
}

const srvPath = (server) => `/api/servers/${encodeURIComponent(server)}`;

function handleErr(err) {
  if (err instanceof AuthError) { showLogin("Your session ended. Sign in again."); return true; }
  if (err.offline) { setConnected(false); return true; }
  return false;
}

async function loadServers() {
  try {
    const data = await api("GET", "/api/servers");
    S.servers = data.servers || [];
    setConnected(true);
    if (!S.servers.some((s) => s.name === S.server)) S.server = "local";
  } catch (err) { handleErr(err); }
}

async function loadProcs(server) {
  try {
    const list = await api("GET", `${srvPath(server)}/processes`);
    S.procs.set(server, { list, error: null });
    record(server, list);
    setConnected(true);
  } catch (err) {
    if (handleErr(err)) return;
    S.procs.set(server, { list: [], error: err.message });
  }
}

async function loadSystem(server) {
  try {
    const data = await api("GET", `${srvPath(server)}/system`);
    S.system.set(server, { data, error: null });
  } catch (err) {
    if (handleErr(err)) return;
    const old = err.status === 404 && server !== "local" ? "Host stats need procly 0.3 or newer on this server." : err.message;
    S.system.set(server, { data: null, error: old });
  }
}

function record(server, list) {
  const seen = new Set();
  for (const p of list) {
    const k = key(server, p.name);
    seen.add(k);
    if (!p.pid) continue;
    let hst = S.hist.get(k);
    if (!hst || hst.pid !== p.pid) { // a new run starts a new graph
      hst = { pid: p.pid, cpu: [], mem: [] };
      S.hist.set(k, hst);
    }
    hst.cpu.push(p.cpu);
    hst.mem.push(p.memory);
    if (hst.cpu.length > HISTORY) { hst.cpu.shift(); hst.mem.shift(); }
  }
  const prefix = key(server, "");
  for (const k of S.hist.keys()) if (k.startsWith(prefix) && !seen.has(k)) S.hist.delete(k);
}

const procsOf = (server) => (S.procs.get(server) || { list: [] }).list;
const serverInfo = (name) => S.servers.find((s) => s.name === name);
const serverLabel = (name) => (serverInfo(name) || { label: name === "local" ? S.info.host : name }).label || name;
const okServers = () => S.servers.filter((s) => s.ok);

// ---------- polling ----------

async function poll() {
  if (document.hidden || $("app").hidden) return;
  const n = S.tick++;
  const page = current.page;
  const jobs = [loadProcs(S.server)];
  if (n % 3 === 0) jobs.push(loadServers());
  const fleet = page === "overview" || page === "servers";
  if (fleet && n % 2 === 0) {
    for (const s of S.servers) if (s.name !== S.server && s.ok) jobs.push(loadProcs(s.name));
  }
  if (n % 5 === 0) {
    if (fleet) for (const s of S.servers) jobs.push(loadSystem(s.name));
    else if (page === "server") jobs.push(loadSystem(current.server));
  }
  await Promise.allSettled(jobs);
  refresh();
}

function startPolling() {
  stopPolling();
  S.timer = setInterval(poll, POLL_MS);
}

function stopPolling() {
  clearInterval(S.timer);
  S.timer = null;
}

document.addEventListener("visibilitychange", () => {
  if (!$("app").hidden && !document.hidden) poll();
});

function setConnected(ok) {
  if (S.connected === ok) return;
  S.connected = ok;
  renderChrome();
}

// ---------- formatting ----------

function fmtBytes(b) {
  if (!b) return "–";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (i >= 2 ? b.toFixed(b >= 100 ? 0 : 1) : Math.round(b)) + " " + u[i];
}

function fmtUptime(ms) {
  if (!ms) return "–";
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m";
  const hr = Math.floor(m / 60);
  if (hr < 24) return hr + "h " + (m % 60) + "m";
  return Math.floor(hr / 24) + "d " + (hr % 24) + "h";
}

function fmtAgo(ms) {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 10) return "just now";
  if (s < 60) return s + "s ago";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m ago";
  const hr = Math.floor(m / 60);
  if (hr < 24) return hr + "h ago";
  return Math.floor(hr / 24) + "d ago";
}

function fmtDur(ms) {
  if (ms < 1000) return ms + "ms";
  if (ms < 60000) return +(ms / 1000).toFixed(1) + "s";
  return +(ms / 60000).toFixed(1) + "m";
}

const clock = (d) => d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
const plural = (n, one, many) => `${n} ${n === 1 ? one : many || one + "s"}`;
// An orphan is running, but a daemon that has died started it: nothing watches
// it. It can be stopped, not restarted.
const running = (p) => p.status === "online" || p.status === "waiting" || p.status === "orphaned";
const needsAttention = (p) => p.status === "errored" || p.status === "waiting" || p.status === "orphaned" || p.health === "unhealthy";
const canRestart = (p) => p.status !== "orphaned";

const progressive = { restart: "restarting", start: "starting", stop: "stopping", delete: "deleting" };
const past = { restart: "Restarted", start: "Started", stop: "Stopped", delete: "Deleted" };

// ---------- small components ----------

function statusEl(server, p) {
  const verb = S.busy.get(key(server, p.name));
  if (verb) return h("span", { class: "status waiting", text: progressive[verb] });
  return h("span", { class: "status " + p.status, text: p.status });
}

function healthEl(p) {
  if (!p.health_check) return h("span", { class: "health-ic none", text: "–" });
  const state = p.health || "none";
  const ic = { healthy: "check", unhealthy: "alert", starting: "clock" }[state] || "pulse";
  return h("span", { class: "health-ic " + state, title: p.health_error || p.health_check }, icon(ic), state === "none" ? "not running" : state);
}

function countStatus(list) {
  const c = { online: 0, waiting: 0, orphaned: 0, errored: 0, stopped: 0 };
  for (const p of list) c[p.status] = (c[p.status] || 0) + 1;
  return c;
}

function healthBar(c) {
  const bar = h("div", { class: "health-bar", "aria-hidden": "true" });
  let total = 0;
  for (const s of ["online", "waiting", "orphaned", "errored", "stopped"]) {
    if (!c[s]) continue;
    total += c[s];
    const seg = h("span", { class: s });
    seg.style.flex = String(c[s]);
    bar.append(seg);
  }
  if (!total) bar.append(h("span", { class: "none" }));
  return bar;
}

function countsEl(c) {
  const box = h("div", { class: "counts" });
  for (const s of ["online", "waiting", "orphaned", "errored", "stopped"]) {
    if (c[s]) box.append(h("span", { class: s }, h("b", { text: c[s] }), " " + s));
  }
  if (!box.childElementCount) box.append(h("span", { text: "no processes" }));
  return box;
}

function sparkPath(vals, w, hgt, pad, { fromZero, minSpan, slots }) {
  let lo = Math.min(...vals), hi = Math.max(...vals);
  if (fromZero) lo = 0;
  const span = Math.max(hi - lo, minSpan);
  const step = w / (slots - 1);
  const x0 = w - (vals.length - 1) * step;
  const pts = vals.map((v, i) => [x0 + i * step, hgt - pad - ((v - lo) / span) * (hgt - pad * 2)]);
  return { d: pts.map(([x, y], i) => (i ? "L" : "M") + x.toFixed(2) + " " + y.toFixed(2)).join(" "), last: pts[pts.length - 1], hi: lo + span };
}

function svgEl(tag, attrs) {
  const e = document.createElementNS(SVG_NS, tag);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, String(v));
  return e;
}

function miniSpark(vals) {
  const svg = svgEl("svg", { class: "mini", viewBox: "0 0 56 18", preserveAspectRatio: "none", "aria-hidden": "true" });
  svg.append(svgEl("line", { class: "track", x1: 0, x2: 56, y1: 17.5, y2: 17.5 }));
  if (vals.length > 1) svg.append(svgEl("path", { d: sparkPath(vals.slice(-24), 56, 18, 2, { fromZero: true, minSpan: 10, slots: 24 }).d }));
  return svg;
}

// A chart inside a .kpi: line, dashed midline, a dot on the newest sample
// and the scale's top value in the corner.
function drawChart(kpi, vals, opts) {
  let svg = kpi.querySelector(".chart");
  if (!svg) {
    svg = svgEl("svg", { class: "chart", viewBox: "0 0 200 56", preserveAspectRatio: "none", "aria-hidden": "true" });
    kpi.append(svg, h("span", { class: "chart-max" }), h("span", { class: "chart-dot", hidden: true }));
  }
  fill(svg, 
    svgEl("line", { class: "grid", x1: 0, x2: 200, y1: 28, y2: 28 }),
    svgEl("line", { class: "base", x1: 0, x2: 200, y1: 55.5, y2: 55.5 }),
  );
  const dot = kpi.querySelector(".chart-dot");
  const max = kpi.querySelector(".chart-max");
  if (vals.length < 2) { dot.hidden = true; max.textContent = ""; return; }
  const { d, last, hi } = sparkPath(vals, 200, 56, 3, { ...opts, slots: HISTORY });
  svg.append(svgEl("path", { class: "line", d }));
  dot.hidden = false;
  dot.style.top = `${svg.offsetTop + (last[1] / 56) * svg.clientHeight}px`;
  max.textContent = opts.label(hi);
}

function kpi(label, ic, extra) {
  return h("div", { class: "kpi" + (extra ? " " + extra : "") },
    h("div", { class: "kpi-label" }, ic && icon(ic), label),
    h("div", { class: "kpi-value" }),
    h("div", { class: "kpi-sub" }));
}

function setKpi(el, value, sub, tone) {
  const v = el.querySelector(".kpi-value");
  v.className = "kpi-value" + (tone ? " " + tone : "");
  if (value instanceof Node) fill(v, value); else v.textContent = value;
  el.querySelector(".kpi-sub").textContent = sub || "";
}

function meter(label, ic, used, total, text) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0;
  const bar = h("div", { class: "bar" + (!total ? " none" : pct >= 90 ? " bad" : pct >= 75 ? " warn" : "") }, h("span"));
  bar.firstChild.style.width = pct.toFixed(1) + "%";
  return h("div", { class: "meter" + (label ? "" : " compact") },
    h("div", { class: "meter-row" }, label && icon(ic), label, h("b", { text: text })),
    bar);
}

// shortVersion turns a dev build's "0.2.1-0.2026…-c877a9d+dirty" into "0.2.1".
const shortVersion = (v) => (v || "").replace(/^v/, "").split(/[-+]/)[0];

function systemMeters(sys, compact) {
  const loadPct = sys.cpus ? (sys.load[0] / sys.cpus) * 100 : 0;
  const memUsed = sys.mem_available ? sys.mem_total - sys.mem_available : 0;
  return [
    meter(!compact && "Load", "cpu", loadPct, 100, sys.load[0].toFixed(2)),
    meter(!compact && "Mem", "memory", memUsed, sys.mem_available ? sys.mem_total : 0, sys.mem_available ? Math.round((memUsed / sys.mem_total) * 100) + "%" : fmtBytes(sys.mem_total)),
    meter(!compact && "Disk", "disk", sys.disk_total - sys.disk_free, sys.disk_total, sys.disk_total ? Math.round(((sys.disk_total - sys.disk_free) / sys.disk_total) * 100) + "%" : "–"),
  ];
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Plain http on another host has no clipboard API; fall back.
    const t = h("textarea", { class: "mono" });
    t.value = text;
    document.body.append(t);
    t.select();
    document.execCommand("copy");
    t.remove();
  }
  toast("Copied");
}

function cmdline(text) {
  return h("div", { class: "cmdline" },
    h("span", { text }),
    h("button", { class: "icon-btn", type: "button", title: "Copy", "aria-label": "Copy command", onclick: () => copyText(text) }, icon("copy")));
}

function emptyBox(ic, title, text, extra) {
  return h("div", { class: "empty-box" }, icon(ic), h("b", { text: title }), text && h("div", { text }), extra);
}

const EVENT = {
  crashed: { ic: "alert", tone: "warn", title: () => "Crashed" },
  errored: { ic: "alert", tone: "bad", title: (e) => e.detail && e.detail.startsWith("gave up") ? "Gave up restarting" : e.detail && e.detail.startsWith("failed to start") ? "Failed to start" : "Exited with an error" },
  unhealthy: { ic: "pulse", tone: "bad", title: () => "Health check failed" },
  memory: { ic: "alert", tone: "warn", title: () => "Restarted: used too much memory" },
  started: { ic: "play", tone: "good", title: () => "Started" },
  stopped: { ic: "stop", tone: "", title: () => "Stopped" },
  orphaned: { ic: "alert", tone: "warn", title: () => "Found running after its daemon died" },
  exited: { ic: "check", tone: "", title: () => "Exited cleanly" },
};
const eventInfo = (e) => EVENT[e.kind] || { ic: "info", tone: "", title: () => e.kind };
const isProblem = (e) => e.kind === "crashed" || e.kind === "errored" || e.kind === "unhealthy" || e.kind === "memory";

// ---------- menus ----------

let openMenu = null;

function closeMenu() {
  if (!openMenu) return;
  openMenu.el.remove();
  if (openMenu.anchor) openMenu.anchor.setAttribute("aria-expanded", "false");
  openMenu = null;
}

// popMenu shows items under anchor. Items: {label, icon, onclick, danger, disabled, kbd} or "sep".
function popMenu(anchor, items) {
  if (openMenu && openMenu.anchor === anchor) { closeMenu(); return; }
  closeMenu();
  const el = h("div", { class: "menu floating", role: "menu" });
  for (const it of items) {
    if (it === "sep") { el.append(h("div", { class: "menu-sep" })); continue; }
    el.append(h("button", {
      class: "menu-item" + (it.danger ? " danger" : ""), role: "menuitem", type: "button", disabled: it.disabled,
      onclick: (e) => { e.stopPropagation(); closeMenu(); it.onclick(); },
    }, icon(it.icon), h("span", { class: "grow", text: it.label }), it.kbd && h("kbd", { text: it.kbd })));
  }
  document.body.append(el);
  const r = anchor.getBoundingClientRect();
  const w = el.offsetWidth, ht = el.offsetHeight;
  el.style.left = Math.max(8, Math.min(r.right - w, innerWidth - w - 8)) + "px";
  el.style.top = (r.bottom + ht + 8 > innerHeight ? r.top - ht - 6 : r.bottom + 6) + "px";
  anchor.setAttribute("aria-expanded", "true");
  openMenu = { el, anchor };
  el.querySelector(".menu-item:not(:disabled)")?.focus();
}

document.addEventListener("click", (e) => {
  if (openMenu && !openMenu.el.contains(e.target) && !openMenu.anchor.contains(e.target)) closeMenu();
  const sw = $("switch-menu");
  if (!sw.hidden && !sw.contains(e.target) && !$("switch-btn").contains(e.target)) toggleSwitcher(false);
});
window.addEventListener("resize", closeMenu);
document.addEventListener("scroll", closeMenu, true);

// ---------- dialogs ----------

const CONFIRM = {
  restart: (n) => ({ ic: "restart", tone: "", title: `Restart ${n}?`, text: "procly stops it and starts it again. Requests in flight may fail for a moment.", ok: "Restart" }),
  stop: (n) => ({ ic: "stop", tone: "warn", title: `Stop ${n}?`, text: "It stays down until you start it again. Autorestart won't bring it back.", ok: "Stop process" }),
  delete: (n) => ({ ic: "trash", tone: "bad", title: `Delete ${n}?`, text: "It is stopped and removed from procly. Its log files stay on disk.", ok: "Delete process", danger: true, type: n }),
};

// confirmDialog resolves true when the person confirms.
function confirmDialog({ ic, tone, title, text, ok, danger, type }) {
  const dlg = $("confirm");
  const iconBox = $("confirm-icon");
  iconBox.className = "dialog-icon" + (tone ? " " + tone : "");
  fill(iconBox, icon(ic, 18));
  $("confirm-title").textContent = title;
  const p = $("confirm-text");
  fill(p, ...(Array.isArray(text) ? text : [text]));
  const okBtn = $("confirm-ok");
  okBtn.textContent = ok;
  okBtn.className = "btn " + (danger ? "danger-solid" : "primary");
  const wrap = $("confirm-type-wrap");
  const input = $("confirm-type");
  wrap.hidden = !type;
  input.value = "";
  okBtn.disabled = !!type;
  if (type) {
    fill($("confirm-type-label"), "Type ", h("b", { text: type }), " to confirm");
    input.oninput = () => { okBtn.disabled = input.value !== type; };
  }
  dlg.returnValue = "";
  dlg.showModal();
  (type ? input : okBtn).focus();
  return new Promise((resolve) => {
    dlg.addEventListener("close", () => resolve(dlg.returnValue === "ok"), { once: true });
  });
}

// OK is the form's only submit button, so Enter confirms, and does nothing
// while OK is disabled (the name isn't typed yet).
$("confirm-cancel").addEventListener("click", () => $("confirm").close("cancel"));

// ---------- actions ----------

async function act(server, name, verb) {
  const k = key(server, name);
  if (S.busy.has(k)) return;
  if (CONFIRM[verb]) {
    const spec = CONFIRM[verb](name);
    const p = fresh(server, name);
    if (verb === "stop" && p && p.status === "orphaned") {
      spec.text = `It is left over from a daemon that died. Stopping ends it and procly forgets it; start it again afterwards (procly resurrect brings back the saved list).`;
    }
    if (!(await confirmDialog(spec))) return;
  }
  S.busy.set(k, verb);
  refresh();
  try {
    await api("POST", `${srvPath(server)}/processes/${encodeURIComponent(name)}/${verb}`);
    toast(`${past[verb]} ${name}` + (server === "local" ? "" : ` on ${serverLabel(server)}`));
    if (verb === "delete" && current.page === "process" && current.name === name) go(`#/s/${encodeURIComponent(server)}/processes`);
  } catch (err) {
    if (!handleErr(err)) toast(err.message, true);
  } finally {
    S.busy.delete(k);
    await loadProcs(server);
    refresh();
  }
}

function toggleVerb(p) { return running(p) ? "stop" : "start"; }

// Handlers on kept nodes must not trust the data they were drawn with.
const fresh = (server, name) => procsOf(server).find((p) => p.name === name);
function toggle(server, name) {
  const p = fresh(server, name);
  if (p) act(server, name, toggleVerb(p));
}

async function saveList(server) {
  const n = procsOf(server).length;
  const ok = await confirmDialog({
    ic: "save", title: `Save the list on ${serverLabel(server)}?`,
    text: [`The ${plural(n, "process", "processes")} there now come back after a reboot. This replaces the last saved list.`],
    ok: "Save list",
  });
  if (!ok) return;
  try {
    const r = await api("POST", `${srvPath(server)}/save`);
    toast(`Saved ${plural(r.saved, "process", "processes")} on ${serverLabel(server)}`);
  } catch (err) {
    if (!handleErr(err)) toast(err.message, true);
  }
}

let toastTimer;
function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (isErr ? " err" : "");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isErr ? 6000 : 2800);
}

// ---------- new process ----------

function openNewProcess() {
  if (S.server !== "local") { toast("New processes can only be started on this server. Agents refuse new commands.", true); return; }
  const form = $("new-form");
  form.reset();
  $("new-cwd").value = S.info.home || "/";
  $("new-host").textContent = S.info.host || "this server";
  $("new-error").textContent = "";
  fill($("new-icon"), icon("terminal", 18));
  $("new-dialog").showModal();
  $("new-name").focus();
}

function parseEnv(text) {
  const env = {};
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const i = line.indexOf("=");
    if (i <= 0) throw new Error(`Environment line "${line}" needs KEY=value.`);
    env[line.slice(0, i).trim()] = line.slice(i + 1);
  }
  return env;
}

$("new-cancel").addEventListener("click", () => $("new-dialog").close());
$("new-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("new-error");
  err.textContent = "";
  const name = $("new-name").value.trim();
  const command = $("new-command").value.trim();
  if (!name || !command) { err.textContent = "Name and command are required."; return; }
  let env;
  try { env = parseEnv($("new-env").value); } catch (ex) { err.textContent = ex.message; return; }
  const btn = $("new-submit");
  btn.disabled = true;
  try {
    await api("POST", `${srvPath("local")}/processes`, {
      name, command, env,
      cwd: $("new-cwd").value.trim(),
      health: $("new-health").value.trim(),
      autorestart: $("new-restart").checked,
    });
    $("new-dialog").close();
    toast(`Started ${name}`);
    await loadProcs("local");
    go(`#/s/local/p/${encodeURIComponent(name)}`);
  } catch (ex) {
    if (!handleErr(ex)) err.textContent = ex.message;
  } finally {
    btn.disabled = false;
  }
});

// ---------- log stream ----------

const LOG = { key: null, es: null, lines: [], errs: 0, state: ["", ""], onLine: null, onState: null };

function openLogs(server, name) {
  const k = key(server, name);
  if (LOG.key === k) return;
  closeLogs();
  LOG.key = k;
  LOG.lines = [];
  LOG.errs = 0;
  setLogState("connecting", "warn");
  const es = new EventSource(`${srvPath(server)}/processes/${encodeURIComponent(name)}/logs`);
  LOG.es = es;
  es.onopen = () => setLogState("live", "on");
  es.onmessage = (e) => {
    let batch;
    try { batch = JSON.parse(e.data); } catch { return; }
    const added = batch.map(({ s, l }) => ({ e: s === "e", t: l || "" }));
    for (const a of added) if (a.e) LOG.errs++;
    LOG.lines.push(...added);
    let dropped = 0;
    if (LOG.lines.length > LOG_KEEP) {
      dropped = LOG.lines.length - LOG_KEEP;
      LOG.lines.splice(0, dropped);
    }
    if (LOG.onLine) LOG.onLine(added, dropped);
  };
  es.onerror = () => setLogState(es.readyState === EventSource.CLOSED ? "disconnected" : "reconnecting", es.readyState === EventSource.CLOSED ? "" : "warn");
}

function setLogState(text, mode) {
  LOG.state = [text, mode];
  if (LOG.onState) LOG.onState();
}

function closeLogs() {
  if (LOG.es) LOG.es.close();
  LOG.es = null;
  LOG.key = null;
  LOG.onLine = LOG.onState = null;
}

// ---------- router ----------

let current = { page: "" };
let page = null;

function go(hash) {
  if (location.hash === hash) route(); else location.hash = hash;
}

function parseRoute() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map((x) => { try { return decodeURIComponent(x); } catch { return x; } });
  if (parts[0] === "s" && parts[1]) {
    if (parts[2] === "p" && parts[3]) return { page: "process", server: parts[1], name: parts[3], tab: parts[4] || "overview" };
    return { page: "processes", server: parts[1] };
  }
  if (parts[0] === "processes") return { page: "processes", server: S.server };
  if (parts[0] === "servers" && parts[1]) return { page: "server", server: parts[1] };
  if (parts[0] === "servers") return { page: "servers" };
  if (parts[0] === "settings") return { page: "settings" };
  return { page: "overview" };
}

function route() {
  if ($("app").hidden) return;
  const r = parseRoute();
  if (r.server && r.server !== S.server) {
    S.server = r.server;
    store("procly.server", r.server);
    loadProcs(r.server).then(refresh);
  }
  closeMenu();
  toggleSwitcher(false);
  $("app").classList.remove("menu-open");
  $("scrim").hidden = true;

  const same = page && current.page === r.page && current.server === r.server && current.name === r.name;
  current = r;
  if (same && page.route) {
    page.route(r);
  } else {
    if (page && page.unmount) page.unmount();
    const view = $("view");
    fill(view, );
    view.scrollTop = 0;
    page = PAGES[r.page]();
    page.mount(view, r);
    if (r.page === "overview" || r.page === "servers") {
      Promise.allSettled([...okServers().map((s) => loadProcs(s.name)), ...S.servers.map((s) => loadSystem(s.name))]).then(refresh);
    } else if (r.page === "server") {
      loadSystem(r.server).then(refresh);
    }
  }
  refresh();
}

window.addEventListener("hashchange", route);

function refresh() {
  renderChrome();
  if (page) page.update();
}

// ---------- chrome: sidebar, switcher, banner ----------

const NAV = [
  { id: "overview", label: "Overview", ic: "overview", href: () => "#/" },
  { id: "processes", label: "Processes", ic: "processes", href: () => `#/s/${encodeURIComponent(S.server)}/processes`, match: ["processes", "process"] },
  { id: "servers", label: "Servers", ic: "servers", href: () => "#/servers", match: ["servers", "server"] },
];

function buildChrome() {
  fill($("side-close"), icon("close"));
  fill($("menu-btn"), icon("menu"));
  fill($("switch-chev"), icon("chevronDown"));
  fill($("logout-icon"), icon("logout"));
  const nav = $("nav");
  fill(nav, );
  for (const n of NAV) {
    nav.append(h("a", { class: "nav-item", href: n.href(), "data-nav": n.id }, icon(n.ic), h("span", { text: n.label }), h("span", { class: "nav-count" })));
  }
  const bottom = document.querySelector(".side-bottom");
  if (!bottom.querySelector("[data-nav=settings]")) {
    bottom.insertBefore(h("a", { class: "nav-item", href: "#/settings", "data-nav": "settings" }, icon("settings"), h("span", { text: "Settings" })), $("logout-btn"));
  }
}

function renderChrome() {
  const active = current.page;
  for (const a of document.querySelectorAll("[data-nav]")) {
    const n = NAV.find((x) => x.id === a.dataset.nav);
    const on = n ? (n.match || [n.id]).includes(active) : a.dataset.nav === active;
    if (on) a.setAttribute("aria-current", "page"); else a.removeAttribute("aria-current");
    if (n) a.setAttribute("href", n.href());
  }
  const list = procsOf(S.server);
  const issues = list.filter(needsAttention).length;
  const pc = document.querySelector("[data-nav=processes] .nav-count");
  pc.textContent = issues ? String(issues) : list.length ? String(list.length) : "";
  pc.className = "nav-count" + (issues ? " bad" : "");
  pc.title = issues ? plural(issues, "process needs", "processes need") + " attention" : "";
  const down = S.servers.filter((s) => !s.ok).length;
  const sc = document.querySelector("[data-nav=servers] .nav-count");
  sc.textContent = S.servers.length > 1 ? String(S.servers.length) : "";
  sc.className = "nav-count" + (down ? " bad" : "");

  const info = serverInfo(S.server);
  const dotCls = serverDot(info);
  $("switch-name").textContent = serverLabel(S.server);
  $("switch-dot").className = "dot" + dotCls;
  fill($("mobile-server"), h("span", { class: "dot" + dotCls }), h("span", { text: serverLabel(S.server) }));

  fill($("conn"), 
    h("span", { class: "dot" + (S.connected ? "" : " down") }),
    h("span", { text: S.connected ? "connected" : "offline" }),
    h("span", { class: "ver", text: S.info.version ? "v" + shortVersion(S.info.version) : "" }));

  const banner = $("banner");
  banner.hidden = S.connected;
  if (!S.connected) fill(banner, icon("alert"), "Can't reach the panel server. Retrying every few seconds.");
}

function serverDot(info) {
  if (!info) return "";
  if (!info.ok) return " down";
  const bad = (info.by_status.errored || 0) + (info.by_status.waiting || 0) + (info.by_status.orphaned || 0);
  return bad ? " warn" : info.total ? "" : " idle";
}

function toggleSwitcher(open) {
  const menu = $("switch-menu");
  const btn = $("switch-btn");
  if (open === undefined) open = menu.hidden;
  menu.hidden = !open;
  btn.setAttribute("aria-expanded", String(open));
  if (!open) return;
  fill(menu, h("div", { class: "menu-title", text: "Servers" }));
  for (const s of S.servers) {
    menu.append(h("button", {
      class: "menu-item", role: "option", type: "button", "aria-selected": String(s.name === S.server),
      onclick: () => switchServer(s.name),
    }, h("span", { class: "dot" + serverDot(s) }), h("span", { class: "grow", text: s.label }), h("span", { class: "meta", text: s.ok ? String(s.total) : "down" })));
  }
  menu.append(h("div", { class: "menu-sep" }),
    h("button", { class: "menu-item", type: "button", onclick: () => go("#/servers") }, icon("servers"), h("span", { class: "grow", text: "Manage servers" })));
  menu.querySelector("[aria-selected=true]")?.focus();
}

function switchServer(name) {
  toggleSwitcher(false);
  if (current.page === "processes" || current.page === "process") go(`#/s/${encodeURIComponent(name)}/processes`);
  else if (current.page === "server") go(`#/servers/${encodeURIComponent(name)}`);
  else {
    S.server = name;
    store("procly.server", name);
    loadProcs(name).then(refresh);
    refresh();
  }
}

$("switch-btn").addEventListener("click", () => toggleSwitcher());
$("menu-btn").addEventListener("click", () => { $("app").classList.add("menu-open"); $("scrim").hidden = false; });
$("side-close").addEventListener("click", () => { $("app").classList.remove("menu-open"); $("scrim").hidden = true; });
$("scrim").addEventListener("click", () => { $("app").classList.remove("menu-open"); $("scrim").hidden = true; });

// ---------- pages ----------

function pageHead({ crumbs, title, sub, actions }) {
  return h("div", {},
    crumbs && h("div", { class: "crumbs" }, crumbs.flatMap((c, i) => [i ? icon("chevronRight") : null, c.href ? h("a", { href: c.href, text: c.label }) : h("span", { text: c.label })])),
    h("div", { class: "page-head" },
      h("div", { class: "page-title" }, h("h1", {}, title), sub && h("p", {}, sub)),
      actions && h("div", { class: "page-actions" }, actions)));
}

function sectionHead(title, sub, right) {
  return h("div", { class: "section-head" }, h("h2", { text: title }), sub && h("span", { class: "muted", text: sub }), right && h("div", { class: "right" }, right));
}

const PAGES = {};

// ----- overview -----

PAGES.overview = () => {
  let refs;
  return {
    mount(root) {
      document.title = "Overview · procly";
      refs = {
        sub: h("span"),
        online: kpi("Online", "check"), attention: kpi("Need attention", "alert"),
        cpu: kpi("CPU", "cpu"), mem: kpi("Memory", "memory"), restarts: kpi("Restarts", "restart"),
        servers: h("div", { class: "server-grid" }),
        issues: h("div", { class: "card" }),
        events: h("div", { class: "card" }),
      };
      root.append(h("div", { class: "page" },
        pageHead({ title: "Overview", sub: refs.sub }),
        h("div", { class: "card kpis" }, refs.online, refs.attention, refs.cpu, refs.mem, refs.restarts),
        h("div", { class: "grid-2 section" },
          h("div", {}, sectionHead("Servers", null, h("a", { class: "more", href: "#/servers" }, "All servers", icon("chevronRight"))), refs.servers),
          h("div", {},
            sectionHead("Needs attention"), refs.issues,
            h("div", { class: "section" }, sectionHead("Recent events", "across all servers"), refs.events)))));
    },
    update() {
      const all = [];
      for (const s of S.servers) if (s.ok) for (const p of procsOf(s.name)) all.push({ s: s.name, p });
      const c = countStatus(all.map((x) => x.p));
      const total = all.length;
      refs.sub.textContent = `${plural(S.servers.length || 1, "server")} · ${plural(total, "process", "processes")}`;
      setKpi(refs.online, h("span", {}, String(c.online), h("small", { text: "/ " + total })), total ? `${Math.round((c.online / total) * 100)}% running` : "nothing running yet", c.online && c.online === total ? "good" : "");
      const bad = all.filter((x) => needsAttention(x.p));
      setKpi(refs.attention, String(bad.length), bad.length ? bad.slice(0, 3).map((x) => x.p.name).join(", ") : "all good", bad.length ? "bad" : "");
      const up = all.filter((x) => x.p.pid);
      setKpi(refs.cpu, up.reduce((a, x) => a + x.p.cpu, 0).toFixed(1) + "%", `across ${plural(up.length, "process", "processes")}`);
      setKpi(refs.mem, fmtBytes(up.reduce((a, x) => a + x.p.memory, 0)), "used by processes");
      const rs = all.reduce((a, x) => a + x.p.restarts, 0);
      setKpi(refs.restarts, String(rs), rs ? "since procly started them" : "no restarts", rs ? "warn" : "");

      fill(refs.servers, ...(S.servers.length ? S.servers : [{ name: "local", label: S.info.host, ok: true, local: true, by_status: {}, total: 0 }]).map(serverCard));

      const issues = h("div");
      if (!bad.length) {
        issues.append(emptyBox("check", "Everything is running", "Crashed, restarting or unhealthy processes show up here."));
      } else {
        const list = h("div", { class: "list" });
        for (const { s, p } of bad) {
          const why = p.status === "errored" ? "gave up after " + plural(p.restarts, "restart") : p.status === "waiting" ? "crashed, restarting" : p.status === "orphaned" ? "left over from a daemon that died" : "health check failing";
          list.append(h("a", { class: "list-item", href: `#/s/${encodeURIComponent(s)}/p/${encodeURIComponent(p.name)}` },
            h("span", { class: "ev-icon " + (p.status === "waiting" || p.status === "orphaned" ? "warn" : "bad") }, icon(p.status === "waiting" ? "restart" : "alert")),
            h("div", { class: "grow" }, h("div", { class: "title" }, h("b", { text: p.name })), h("div", { class: "sub", text: `${serverLabel(s)} · ${why}` })),
            statusEl(s, p)));
        }
        issues.append(list);
      }
      fill(refs.issues, ...issues.childNodes);

      const evs = [];
      for (const { s, p } of all) for (const e of p.events || []) evs.push({ s, p, e });
      evs.sort((a, b) => a.e.ago_ms - b.e.ago_ms);
      const events = h("div");
      if (!evs.length) {
        events.append(emptyBox("events", "No events yet", "Starts, stops, crashes and failed health checks are listed here."));
      } else {
        const list = h("div", { class: "list" });
        for (const { s, p, e } of evs.slice(0, 8)) {
          const inf = eventInfo(e);
          list.append(h("a", { class: "list-item", href: `#/s/${encodeURIComponent(s)}/p/${encodeURIComponent(p.name)}/events` },
            h("span", { class: "ev-icon " + inf.tone }, icon(inf.ic)),
            h("div", { class: "grow" },
              h("div", { class: "title" }, h("b", { text: p.name }), " " + inf.title(e).toLowerCase()),
              h("div", { class: "sub", text: serverLabel(s) + (e.exit_code ? ` · exit ${e.exit_code}` : "") })),
            h("span", { class: "when", text: fmtAgo(e.ago_ms) })));
        }
        events.append(list);
      }
      fill(refs.events, ...events.childNodes);
    },
  };
};

function serverCard(s) {
  const sys = S.system.get(s.name);
  const where = s.local ? "this server" : s.url === "tunnel" ? "connects in through the hub" : s.url;
  const card = h("a", { class: "card server-card", href: `#/servers/${encodeURIComponent(s.name)}` },
    h("div", { class: "sc-head" },
      h("span", { class: "dot" + serverDot(s) }),
      h("div", { class: "sc-title" }, h("div", { class: "sc-name", text: s.label }), h("div", { class: "sc-where", text: where })),
      sys && sys.data && h("span", { class: "badge", text: sys.data.distro ? sys.data.distro.replace(/ LTS$/, "") : sys.data.os })));
  if (!s.ok) {
    card.append(h("div", { class: "sc-error", text: s.error || "unreachable" }));
  } else if (sys && sys.data) {
    card.append(h("div", { class: "sc-meters" }, systemMeters(sys.data)));
  } else if (sys && sys.error) {
    card.append(h("div", { class: "sc-error muted", text: sys.error }));
  } else {
    card.append(h("div", { class: "sc-meters" }, meter("Load", "cpu", 0, 0, "…"), meter("Mem", "memory", 0, 0, "…"), meter("Disk", "disk", 0, 0, "…")));
  }
  const c = s.ok ? (S.procs.has(s.name) ? countStatus(procsOf(s.name)) : s.by_status) : {};
  card.append(h("div", { class: "sc-foot" }, healthBar(c), countsEl(c)));
  return card;
}

// ----- processes -----

PAGES.processes = () => {
  let refs, filter = "", show = "all";
  const server = () => current.server;
  return {
    mount(root) {
      refs = {
        sub: h("span"),
        search: h("input", { type: "search", placeholder: "Filter by name or command", "aria-label": "Filter processes", spellcheck: "false", autocomplete: "off" }),
        seg: h("div", { class: "seg", role: "group", "aria-label": "Show" }),
        shown: h("span", { class: "muted mono" }),
        body: h("tbody"),
        empty: h("div"),
        actions: h("div", { class: "page-actions" }),
      };
      refs.search.addEventListener("input", () => { filter = refs.search.value; this.update(); });
      root.append(h("div", { class: "page" },
        h("div", { class: "page-head" },
          h("div", { class: "page-title" }, h("h1", { text: "Processes" }), h("p", {}, refs.sub)),
          refs.actions),
        h("div", { class: "card" },
          h("div", { class: "toolbar" },
            h("label", { class: "search" }, icon("search"), refs.search, h("kbd", { text: "/" })),
            refs.seg,
            h("div", { class: "right" }, refs.shown)),
          h("div", { class: "table-wrap" },
            h("table", { class: "table" },
              h("thead", {}, h("tr", {},
                h("th", { text: "Name" }), h("th", { text: "Status" }), h("th", { class: "hide-md", text: "Health" }),
                h("th", { class: "r", text: "CPU" }), h("th", { class: "r hide-sm", text: "Memory" }), h("th", { class: "r hide-sm", text: "Uptime" }),
                h("th", { class: "r hide-sm", text: "Restarts" }), h("th", { class: "r" }, h("span", { class: "hide-sm", text: "Actions" })))),
              refs.body)),
          refs.empty)));
    },
    route() { this.update(); },
    keys(e) {
      if (e.key === "/") { e.preventDefault(); refs.search.focus(); return true; }
      return false;
    },
    update() {
      const srv = server();
      document.title = `Processes · ${serverLabel(srv)} · procly`;
      const entry = S.procs.get(srv);
      const list = entry ? entry.list : [];
      fill(refs.sub, h("span", { class: "dot" + serverDot(serverInfo(srv)) }), " ", serverLabel(srv), srv === "local" ? " · this server" : "");
      refs.sub.firstChild.style.marginRight = "2px";

      fill(refs.actions, 
        h("button", { class: "btn", onclick: () => saveList(srv), title: "Remember this list for reboots (procly save)" }, icon("save"), "Save list"),
        srv === "local" && h("button", { class: "btn primary", onclick: openNewProcess }, icon("plus"), "New process", h("kbd", { text: "n" })));

      const c = countStatus(list);
      const issues = list.filter(needsAttention).length;
      const groups = [["all", "All", list.length], ["running", "Running", c.online], ["issues", "Issues", issues], ["stopped", "Stopped", c.stopped]];
      fill(refs.seg, ...groups.map(([id, label, n]) => h("button", {
        type: "button", "aria-pressed": String(show === id), onclick: () => { show = id; this.update(); },
      }, label, h("span", { class: "count" + (id === "issues" && n ? " bad" : ""), text: n }))));

      const f = filter.trim().toLowerCase();
      const vis = list.filter((p) =>
        (show === "all" || (show === "running" && p.status === "online") || (show === "issues" && needsAttention(p)) || (show === "stopped" && p.status === "stopped")) &&
        (!f || p.name.toLowerCase().includes(f) || p.command.toLowerCase().includes(f)));
      refs.shown.textContent = list.length ? `${vis.length} of ${list.length}` : "";

      fill(refs.body, ...vis.map((p) => procRow(srv, p)));
      const emptyBoxes = h("div");
      if (entry && entry.error) emptyBoxes.append(emptyBox("alert", `Can't reach ${serverLabel(srv)}`, entry.error));
      else if (entry && !list.length) {
        emptyBoxes.append(emptyBox("terminal", "No processes yet",
          srv === "local" ? "Start one with New process, or from a terminal on this server:" : "Start one from a terminal on that server:",
          cmdline("procly start app.js --name api")));
      } else if (list.length && !vis.length) emptyBoxes.append(emptyBox("search", "Nothing matches", "Try another filter."));
      else if (!entry) emptyBoxes.append(emptyBox("clock", "Loading…"));
      fill(refs.empty, ...emptyBoxes.childNodes);
    },
  };
};

function procRow(server, p) {
  const href = `#/s/${encodeURIComponent(server)}/p/${encodeURIComponent(p.name)}`;
  const up = p.pid > 0;
  const hst = S.hist.get(key(server, p.name));
  const busy = S.busy.has(key(server, p.name));
  const tr = h("tr", { class: "click", "data-key": "p:" + p.name, onclick: () => go(href) },
    h("td", {}, h("div", { class: "cell-name" }, h("b", { text: p.name, title: p.name }), h("span", { text: p.command, title: p.command }))),
    h("td", {}, statusEl(server, p)),
    h("td", { class: "hide-md" }, healthEl(p)),
    h("td", { class: "num r" + (up ? "" : " dim") }, h("div", { class: "cell-cpu" }, up && h("span", { class: "hide-md" }, miniSpark(hst ? hst.cpu : [])), h("span", { text: up ? p.cpu.toFixed(1) + "%" : "–" }))),
    h("td", { class: "num r hide-sm" + (up ? "" : " dim"), text: up ? fmtBytes(p.memory) : "–" }),
    h("td", { class: "num r hide-sm" + (up ? "" : " dim"), text: up ? fmtUptime(p.uptime_ms) : "–" }),
    h("td", { class: "num r hide-sm" + (p.restarts ? " warn" : " dim"), text: p.restarts }),
    h("td", {}, h("div", { class: "cell-actions" },
      h("button", { class: "icon-btn hide-sm", type: "button", title: canRestart(p) ? "Restart" : "Not supervised: stop it first", "aria-label": `Restart ${p.name}`, disabled: busy || !canRestart(p), onclick: (e) => { e.stopPropagation(); act(server, p.name, "restart"); } }, icon("restart")),
      h("button", { class: "icon-btn hide-sm", type: "button", title: running(p) ? "Stop" : "Start", "aria-label": `${running(p) ? "Stop" : "Start"} ${p.name}`, disabled: busy, onclick: (e) => { e.stopPropagation(); toggle(server, p.name); } }, icon(running(p) ? "stop" : "play")),
      h("button", { class: "icon-btn", type: "button", title: "More", "aria-label": `More actions for ${p.name}`, "aria-haspopup": "menu", onclick: (e) => { e.stopPropagation(); const q = fresh(server, p.name); if (q) popMenu(e.currentTarget, procMenu(server, q)); } }, icon("more")))));
  return tr;
}

function procMenu(server, p) {
  const base = `#/s/${encodeURIComponent(server)}/p/${encodeURIComponent(p.name)}`;
  const busy = S.busy.has(key(server, p.name));
  return [
    { label: "Restart", icon: "restart", disabled: busy || !canRestart(p), onclick: () => act(server, p.name, "restart") },
    { label: running(p) ? "Stop" : "Start", icon: running(p) ? "stop" : "play", disabled: busy, onclick: () => act(server, p.name, toggleVerb(p)) },
    "sep",
    { label: "Logs", icon: "terminal", onclick: () => go(base + "/logs") },
    { label: "Events", icon: "events", onclick: () => go(base + "/events") },
    { label: "Configuration", icon: "config", onclick: () => go(base + "/config") },
    "sep",
    { label: "Delete", icon: "trash", danger: true, disabled: busy, onclick: () => act(server, p.name, "delete") },
  ];
}

// ----- one process -----

const TABS = [["overview", "Overview", "overview"], ["logs", "Logs", "terminal"], ["events", "Events", "events"], ["config", "Configuration", "config"]];

PAGES.process = () => {
  let refs, tab = null, tabPage = null;
  const srv = () => current.server, name = () => current.name;
  const proc = () => procsOf(srv()).find((p) => p.name === name());
  const base = () => `#/s/${encodeURIComponent(srv())}/p/${encodeURIComponent(name())}`;

  function setTab(t) {
    if (!TABS.some(([id]) => id === t)) t = "overview";
    if (tab === t) return;
    tab = t;
    for (const b of refs.tabs.children) b.setAttribute("aria-selected", String(b.dataset.tab === t));
    if (tabPage && tabPage.unmount) tabPage.unmount();
    fill(refs.body, );
    tabPage = TAB_PAGES[t](srv, name, proc);
    tabPage.mount(refs.body);
    refs.page.classList.toggle("fill", t === "logs");
  }

  return {
    mount(root, r) {
      refs = {
        page: h("div", { class: "page" }),
        title: h("h1"),
        sub: h("p"),
        actions: h("div", { class: "page-actions" }),
        callout: h("div"),
        tabs: h("div", { class: "tabs", role: "tablist" }),
        body: h("div", { class: "tab-body" }),
        missing: h("div", { hidden: true }),
      };
      for (const [id, label, ic] of TABS) {
        refs.tabs.append(h("a", { class: "tab", role: "tab", href: id === "overview" ? base() : `${base()}/${id}`, "data-tab": id }, icon(ic), label, id === "events" && h("span", { class: "tab-count" })));
      }
      refs.page.append(
        h("div", { class: "crumbs" }, h("a", { href: `#/s/${encodeURIComponent(srv())}/processes`, text: "Processes" }), icon("chevronRight"), h("span", { class: "mono", text: name() })),
        h("div", { class: "page-head" }, h("div", { class: "page-title" }, refs.title, refs.sub), refs.actions),
        refs.missing, refs.callout, refs.tabs, refs.body);
      root.append(refs.page);
      openLogs(srv(), name());
      setTab(r.tab);
    },
    route(r) { setTab(r.tab); this.update(); },
    unmount() { if (tabPage && tabPage.unmount) tabPage.unmount(); closeLogs(); },
    keys(e) {
      const p = proc();
      if (!p) return false;
      const map = { r: () => { if (canRestart(p)) act(srv(), p.name, "restart"); }, s: () => toggle(srv(), p.name), o: () => go(base()), l: () => go(base() + "/logs"), e: () => go(base() + "/events"), c: () => go(base() + "/config") };
      if (map[e.key]) { map[e.key](); return true; }
      return tabPage && tabPage.keys ? tabPage.keys(e) : false;
    },
    update() {
      const p = proc();
      document.title = `${name()} · procly`;
      const loaded = S.procs.has(srv());
      refs.missing.hidden = !!p || !loaded;
      refs.tabs.hidden = refs.body.hidden = !p;
      if (!p) {
        fill(refs.title, h("span", { class: "mono", text: name() }));
        refs.sub.textContent = loaded ? "" : "Loading…";
        fill(refs.actions, );
        fill(refs.callout, );
        if (loaded) fill(refs.missing, h("div", { class: "card" }, emptyBox("info", "This process isn't on " + serverLabel(srv()), "It may have been deleted.", h("div", {}, h("a", { class: "btn", href: `#/s/${encodeURIComponent(srv())}/processes` }, icon("arrowLeft"), "All processes")))));
        return;
      }
      const busy = S.busy.has(key(srv(), p.name));
      fill(refs.title, h("span", { class: "mono", text: p.name }), statusEl(srv(), p), p.health_check && p.health && h("span", { class: "badge " + ({ healthy: "good", unhealthy: "bad", starting: "warn" }[p.health] || "") }, icon("pulse"), p.health));
      fill(refs.sub, `id ${p.id}`, p.pid ? ` · pid ${p.pid}` : ` · last exit ${p.exit_code}`, ` · on ${serverLabel(srv())}`);
      refs.sub.classList.add("mono");
      fill(refs.actions, 
        h("button", { class: "btn", disabled: busy || !canRestart(p), title: canRestart(p) ? null : "Not supervised: stop it first", onclick: () => act(srv(), name(), "restart") }, icon("restart"), "Restart", h("kbd", { text: "r" })),
        h("button", { class: "btn", disabled: busy, onclick: () => toggle(srv(), name()) }, icon(running(p) ? "stop" : "play"), running(p) ? "Stop" : "Start", h("kbd", { text: "s" })),
        h("button", { class: "btn ghost danger", disabled: busy, onclick: () => act(srv(), name(), "delete") }, icon("trash"), "Delete"));

      const calloutBox = h("div");
      const last = (p.events || []).find(isProblem);
      if (p.status === "errored") {
        calloutBox.append(callout("bad", "alert", last && last.detail && last.detail.startsWith("gave up") ? `procly gave up on ${p.name}` : `${p.name} exited with an error`,
          last && last.detail && last.detail.startsWith("gave up") ? `It crashed ${p.max_restarts} times in a row without staying up for 30 seconds. Fix the cause, then start it again.` : `Exit code ${p.exit_code}. Autorestart is ${p.autorestart ? "on" : "off"}.`,
          last && last.detail));
      } else if (p.status === "waiting") {
        calloutBox.append(callout("warn", "restart", `${p.name} crashed and is restarting`, "procly waits a little longer after each quick crash, up to 30 seconds.", last && last.detail));
      } else if (p.status === "orphaned") {
        calloutBox.append(callout("warn", "alert", `${p.name} is left over from a daemon that died`,
          `It is still running as pid ${p.pid}, but nothing supervises it: it is not restarted if it crashes, and its logs are not rotated. Stop it, then start it again; procly resurrect brings back the saved list.`));
      } else if (p.health === "unhealthy") {
        calloutBox.append(callout("bad", "pulse", "Health check failing", `procly restarts ${p.name} after ${p.health_retries} failed checks in a row.`, p.health_error));
      }
      fill(refs.callout, ...calloutBox.childNodes);
      const probs = (p.events || []).filter(isProblem).length;
      const tc = refs.tabs.querySelector(".tab-count");
      tc.textContent = p.events && p.events.length ? String(p.events.length) : "";
      tc.className = "tab-count" + (probs ? " bad" : "");
      if (tabPage) tabPage.update(p);
    },
  };
};

function callout(tone, ic, title, text, detail) {
  return h("div", { class: "callout " + tone }, icon(ic),
    h("div", { class: "grow" }, h("b", { text: title }), h("p", { text }), detail && h("pre", { class: "last-error", text: detail })));
}

const TAB_PAGES = {};

TAB_PAGES.overview = (srv, name) => {
  let refs;
  return {
    mount(root) {
      refs = { cpu: kpi("CPU", "cpu"), mem: kpi("Memory", "memory"), up: kpi("Uptime", "clock"), rs: kpi("Restarts", "restart"), facts: h("dl", { class: "facts" }), events: h("div") };
      root.append(
        h("div", { class: "card kpis k4" }, refs.cpu, refs.mem, refs.up, refs.rs),
        h("div", { class: "grid-2 section" },
          h("div", {}, sectionHead("Details"), h("div", { class: "card" }, refs.facts)),
          h("div", {}, sectionHead("Recent events", null, h("a", { class: "more", href: `#/s/${encodeURIComponent(srv())}/p/${encodeURIComponent(name())}/events` }, "All events", icon("chevronRight"))), refs.events)));
    },
    update(p) {
      const hst = S.hist.get(key(srv(), p.name));
      setKpi(refs.cpu, p.pid ? p.cpu.toFixed(1) + "%" : "–", p.pid ? "of one core" : "not running");
      setKpi(refs.mem, p.pid ? fmtBytes(p.memory) : "–", p.pid ? "resident" : "not running");
      drawChart(refs.cpu, p.pid && hst ? hst.cpu : [], { fromZero: true, minSpan: 10, label: (v) => v.toFixed(0) + "%" });
      drawChart(refs.mem, p.pid && hst ? hst.mem : [], { fromZero: false, minSpan: 8 << 20, label: fmtBytes });
      setKpi(refs.up, p.pid ? fmtUptime(p.uptime_ms) : "–", p.pid ? "since " + clock(new Date(Date.now() - p.uptime_ms)) : p.status === "errored" ? "gave up restarting" : "not running");
      setKpi(refs.rs, String(p.restarts),
        p.restarts === 0 ? "no crashes" : p.pid ? "running again" : p.status === "waiting" ? "backing off" : "",
        p.restarts ? "warn" : "");

      const facts = h("dl");
      const add = (k, v, cls) => facts.append(h("dt", { text: k }), v instanceof Node ? h("dd", { class: cls || "" }, v) : h("dd", { class: cls || "", text: v, title: v }));
      add("Command", p.command);
      add("Directory", p.cwd);
      add("Autorestart", p.autorestart ? `on, gives up after ${p.max_restarts} quick crashes` : "off");
      if (p.health_check) add("Health check", `${p.health || "not running"} · ${p.health_check}`, p.health === "healthy" ? "good" : p.health === "unhealthy" ? "bad" : "");
      if (p.max_memory) add("Memory limit", `restarts above ${fmtBytes(p.max_memory)}, counting its children`);
      add("Environment", p.env_keys.length ? h("div", { class: "chips" }, p.env_keys.map((k) => h("span", { class: "badge", text: k }))) : h("span", { class: "muted", text: "none set" }));
      add("Logs", p.out_log || "–");
      fill(refs.facts, ...facts.childNodes);

      fill(refs.events, eventsCard((p.events || []).slice(0, 5)));
    },
  };
};

function eventsCard(evs) {
  if (!evs.length) return h("div", { class: "card" }, emptyBox("events", "No events yet", "Crashes, restarts and failed health checks show up here."));
  return h("div", { class: "card timeline" }, evs.map((e) => {
    const inf = eventInfo(e);
    return h("div", { class: "tl-item" },
      h("span", { class: "ev-icon " + inf.tone }, icon(inf.ic)),
      h("div", { class: "tl-main" },
        h("div", { class: "tl-title" }, inf.title(e), e.exit_code ? h("span", { class: "badge", text: "exit " + e.exit_code }) : null, e.kind === "started" && e.detail ? h("span", { class: "badge", text: e.detail }) : null),
        e.detail && e.kind !== "started" && h("pre", { class: "tl-detail", text: e.detail })),
      h("span", { class: "tl-when", text: fmtAgo(e.ago_ms), title: new Date(Date.now() - e.ago_ms).toLocaleString() }));
  }));
}

TAB_PAGES.events = () => {
  let box;
  return {
    mount(root) { box = h("div"); root.append(box); },
    update(p) { fill(box, eventsCard(p.events || []), h("p", { class: "muted", text: "procly keeps the last 40 events per process in memory. They reset when the daemon restarts." })); box.lastChild.style.fontSize = "12.5px"; },
  };
};

TAB_PAGES.config = () => {
  let box;
  return {
    mount(root) { box = h("div", { class: "settings" }); root.append(box); },
    update(p) {
      const card = (ic, title, rows) => {
        const dl = h("dl", { class: "facts" });
        for (const [k, v] of rows) dl.append(h("dt", { text: k }), v instanceof Node ? h("dd", {}, v) : h("dd", { text: v, title: v }));
        return h("div", { class: "card" }, h("div", { class: "card-title" }, icon(ic), h("h2", { text: title })), dl);
      };
      fill(box, 
        card("terminal", "Run", [
          ["Command", p.command], ["Directory", p.cwd], ["procly id", String(p.id)],
          ["Environment", p.env_keys.length ? h("div", { class: "chips" }, p.env_keys.map((k) => h("span", { class: "badge", text: k }))) : h("span", { class: "muted", text: "none set" })],
          ["", h("span", { class: "muted", text: "Values are never sent to the panel, only names." })],
        ]),
        card("restart", "Restart policy", [
          ["Autorestart", p.autorestart ? "on" : "off"],
          ["Gives up after", `${p.max_restarts} crashes in a row`],
          ["Retry delay", `${fmtDur(p.restart_delay_ms)}, doubling up to 30s`],
          ["Counter resets", "after a run longer than 30s"],
          ["Stop", `SIGTERM, then SIGKILL after ${fmtDur(p.kill_timeout_ms)}`],
        ]),
        card("config", "Logs", [
          ["stdout", p.out_log || "–"], ["stderr", p.err_log || "–"],
          ["Rotation", p.log_max_bytes ? `at ${fmtBytes(p.log_max_bytes)}, keeps ${plural(p.log_keep, "old file")}` : "off"],
        ]),
        card("pulse", "Health check", p.health_check ? [
          ["Target", p.health_check], ["Every", fmtDur(p.health_every_ms)],
          ["Restarts after", plural(p.health_retries, "failed check")],
          ["Now", p.health || "not running"], p.health_error ? ["Last error", p.health_error] : null,
        ].filter(Boolean) : [["", h("span", { class: "muted", text: "None. Add one with --health http://127.0.0.1:3000/health or a tcp:// address." })]]),
        h("div", { class: "card card-pad" }, h("div", { class: "muted", text: "Change these by starting the process again with new flags or a procly.yml. Same details in a terminal:" }), cmdline(`procly describe ${p.name}`)),
      );
    },
  };
};

TAB_PAGES.logs = (srv, name) => {
  let refs, filter = "", only = "all", wrap = load("procly.wrap") === "1", follow = true, paused = false;
  const matches = (ln) => (only === "all" || (only === "err") === ln.e) && (!filter || ln.t.toLowerCase().includes(filter));

  function lineEl(ln) {
    const d = h("div", { class: ln.e ? "ln err" : "ln" });
    if (filter) {
      const lower = ln.t.toLowerCase();
      let i = 0, j;
      while ((j = lower.indexOf(filter, i)) !== -1) {
        d.append(ln.t.slice(i, j), h("mark", { text: ln.t.slice(j, j + filter.length) }));
        i = j + filter.length;
      }
      d.append(ln.t.slice(i));
    } else {
      d.textContent = ln.t || " ";
    }
    return d;
  }

  function renderAll() {
    const frag = document.createDocumentFragment();
    let n = 0;
    for (const ln of LOG.lines) if (matches(ln)) { frag.append(lineEl(ln)); n++; }
    fill(refs.log, frag);
    if (!n) refs.log.append(h("div", { class: "note", text: LOG.lines.length ? "No lines match." : "No output yet." }));
    if (follow) refs.log.scrollTop = refs.log.scrollHeight;
    foot();
  }

  function foot() {
    fill(refs.foot, 
      h("span", { text: plural(LOG.lines.length, "line") }),
      h("span", { text: plural(LOG.errs, "stderr line") }),
      h("span", { class: "right", text: paused ? "paused" : follow ? "following" : "scrolled up" }));
  }

  function state() {
    const [text, mode] = LOG.state;
    fill(refs.state, h("span", { class: "live-dot" + (mode ? " " + mode : "") }), paused ? "paused" : text);
  }

  return {
    mount(root) {
      refs = {
        state: h("span", { class: "stream-state" }),
        log: h("div", { class: "log" + (wrap ? " wrap" : ""), tabindex: "0", "aria-label": "Log output" }),
        foot: h("div", { class: "term-foot" }),
        jump: h("button", { class: "btn small jump", hidden: true, text: "Jump to latest ↓" }),
        search: h("input", { type: "search", placeholder: "Search logs", "aria-label": "Search logs", spellcheck: "false", autocomplete: "off" }),
        seg: h("div", { class: "seg", role: "group", "aria-label": "Which output" }),
        wrapBtn: h("button", { class: "icon-btn", type: "button", title: "Wrap long lines", "aria-pressed": String(wrap) }, icon("wrap")),
        pauseBtn: h("button", { class: "icon-btn", type: "button", title: "Pause", "aria-pressed": "false" }, icon("pause")),
      };
      for (const [id, label] of [["all", "All"], ["out", "stdout"], ["err", "stderr"]]) {
        refs.seg.append(h("button", { type: "button", "aria-pressed": String(id === only), onclick: () => {
          only = id;
          for (const b of refs.seg.children) b.setAttribute("aria-pressed", String(b.textContent === label));
          renderAll();
        } }, label));
      }
      refs.search.addEventListener("input", () => { filter = refs.search.value.toLowerCase(); renderAll(); });
      refs.wrapBtn.addEventListener("click", () => {
        wrap = !wrap; store("procly.wrap", wrap ? "1" : null);
        refs.log.classList.toggle("wrap", wrap); refs.wrapBtn.setAttribute("aria-pressed", String(wrap));
      });
      refs.pauseBtn.addEventListener("click", () => {
        paused = !paused;
        fill(refs.pauseBtn, icon(paused ? "play" : "pause"));
        refs.pauseBtn.title = paused ? "Resume" : "Pause";
        refs.pauseBtn.setAttribute("aria-pressed", String(paused));
        if (!paused) renderAll();
        state(); foot();
      });
      refs.log.addEventListener("scroll", () => {
        const atBottom = refs.log.scrollHeight - refs.log.scrollTop - refs.log.clientHeight < 24;
        if (atBottom !== follow) { follow = atBottom; foot(); }
        if (atBottom) refs.jump.hidden = true;
      });
      refs.jump.addEventListener("click", () => { refs.log.scrollTop = refs.log.scrollHeight; follow = true; refs.jump.hidden = true; foot(); });

      root.append(h("div", { class: "term" },
        h("div", { class: "term-head" },
          refs.state, h("span", { class: "grow" }),
          h("label", { class: "search" }, icon("search"), refs.search),
          refs.seg, refs.wrapBtn, refs.pauseBtn,
          h("button", { class: "icon-btn", type: "button", title: "Download these lines", onclick: () => {
            const text = LOG.lines.filter(matches).map((l) => l.t).join("\n") + "\n";
            const a = h("a", { href: URL.createObjectURL(new Blob([text], { type: "text/plain" })), download: `${name()}-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, "-")}.log` });
            document.body.append(a); a.click(); a.remove();
            setTimeout(() => URL.revokeObjectURL(a.href), 1000);
          } }, icon("download")),
          h("button", { class: "icon-btn", type: "button", title: "Clear the view", onclick: () => { LOG.lines = []; LOG.errs = 0; renderAll(); } }, icon("trash"))),
        refs.log, refs.jump, refs.foot));

      LOG.onLine = (added, dropped) => {
        if (paused) { foot(); return; }
        if (dropped) {
          // With nothing filtered, screen lines match the buffer one to one,
          // so drop the same number from the top; otherwise redraw.
          if (filter || only !== "all") { renderAll(); return; }
          for (let i = 0; i < dropped && refs.log.firstChild; i++) refs.log.firstChild.remove();
        }
        const frag = document.createDocumentFragment();
        for (const ln of added) if (matches(ln)) frag.append(lineEl(ln));
        const shown = frag.childNodes.length;
        if (shown) refs.log.querySelector(".note")?.remove();
        refs.log.append(frag);
        if (follow) refs.log.scrollTop = refs.log.scrollHeight;
        else if (shown) refs.jump.hidden = false;
        foot();
      };
      LOG.onState = state;
      state();
      renderAll();
    },
    keys(e) {
      if (e.key === "/") { e.preventDefault(); refs.search.focus(); return true; }
      return false;
    },
    unmount() { LOG.onLine = LOG.onState = null; },
    update() { /* lines arrive by stream */ },
  };
};

// ----- servers -----

PAGES.servers = () => {
  let refs;
  return {
    mount(root) {
      document.title = "Servers · procly";
      refs = { sub: h("span"), body: h("tbody") };
      root.append(h("div", { class: "page" },
        pageHead({ title: "Servers", sub: refs.sub, actions: h("a", { class: "btn", href: "#/servers", onclick: (e) => { e.preventDefault(); document.getElementById("add-server").scrollIntoView({ behavior: "smooth" }); } }, icon("plus"), "Add a server") }),
        h("div", { class: "card" }, h("div", { class: "table-wrap" }, h("table", { class: "table" },
          h("thead", {}, h("tr", {},
            h("th", { text: "Server" }), h("th", { text: "Status" }), h("th", { class: "hide-md", text: "System" }),
            h("th", { class: "hide-sm", text: "Load" }), h("th", { class: "hide-sm", text: "Memory" }), h("th", { class: "hide-lg", text: "Disk" }),
            h("th", { class: "r hide-md", text: "Up" }), h("th", { text: "Processes" }), h("th"))),
          refs.body))),
        h("div", { class: "section", id: "add-server" }, sectionHead("Add a server", "servers are added from a terminal"),
          h("div", { class: "grid-even" },
            h("div", { class: "card" },
              h("div", { class: "card-title" }, icon("link"), h("h2", { text: "Reachable server" }), h("span", { class: "right badge", text: "port 4748" })),
              h("ol", { class: "steps" },
                h("li", {}, h("span", { text: "On the new server, start the agent. It prints a token and a fingerprint." }), cmdline("procly agent")),
                h("li", {}, h("span", { text: "Here, add it with the command the agent printed:" }), cmdline("procly server add <name> https://<host>:4748 --token <token> --fingerprint <sha256:…>")))),
            h("div", { class: "card" },
              h("div", { class: "card-title" }, icon("tunnel"), h("h2", { text: "Behind NAT or a firewall" }), h("span", { class: "right badge", text: "dials in" })),
              h("ol", { class: "steps" },
                h("li", {}, h("span", { text: "Here, accept agents and make an invite:" }), h("div", { class: "cmdblock" }, cmdline("procly hub"), cmdline("procly server invite <name>"))),
                h("li", {}, h("span", { text: "On that server, run the join command the invite printed, then stay connected:" }), h("div", { class: "cmdblock" }, cmdline("procly agent join <hub:4749> …"), cmdline("procly agent connect")))))))));
    },
    update() {
      const ok = okServers().length;
      refs.sub.textContent = `${plural(S.servers.length, "server")}, ${ok} reachable`;
      fill(refs.body, ...S.servers.map((s) => {
        const sys = (S.system.get(s.name) || {}).data;
        const c = s.ok ? (S.procs.has(s.name) ? countStatus(procsOf(s.name)) : s.by_status) : {};
        const href = `#/servers/${encodeURIComponent(s.name)}`;
        const m = sys ? systemMeters(sys, true) : [];
        return h("tr", { class: "click", "data-key": "s:" + s.name, onclick: () => go(href) },
          h("td", {}, h("div", { class: "cell-name" }, h("b", { text: s.label }), h("span", { text: s.local ? "this server" : s.url === "tunnel" ? "tunnel through the hub" : s.url }))),
          h("td", {}, h("span", { class: "status " + (s.ok ? "online" : "down"), text: s.ok ? "connected" : "unreachable", title: s.error || "" })),
          h("td", { class: "hide-md" }, sys ? h("div", { class: "cell-name" }, h("b", { text: sys.distro || sys.os }), h("span", { text: `${sys.arch} · ${plural(sys.cpus, "core")} · procly ${shortVersion(sys.version)}` })) : h("span", { class: "dim", text: "–" })),
          h("td", { class: "hide-sm" }, m[0] || "–"), h("td", { class: "hide-sm" }, m[1] || "–"), h("td", { class: "hide-lg" }, m[2] || "–"),
          h("td", { class: "num r hide-md" + (sys ? "" : " dim"), text: sys ? fmtUptime(sys.uptime_s * 1000) : "–" }),
          h("td", {}, h("div", { class: "sc-foot", style: null }, healthBar(c), h("span", { class: "num", text: s.ok ? s.total : "–" }))),
          h("td", { class: "r" }, h("span", { class: "dim" }, icon("chevronRight"))));
      }));
      for (const td of refs.body.querySelectorAll("td .meter")) td.parentElement.style.minWidth = "96px";
      for (const f of refs.body.querySelectorAll(".sc-foot")) { f.style.padding = "0"; f.style.border = "0"; f.style.minWidth = "120px"; }
    },
  };
};

// ----- one server -----

PAGES.server = () => {
  let refs;
  const srv = () => current.server;
  return {
    mount(root) {
      refs = {
        title: h("h1"), sub: h("p"), actions: h("div", { class: "page-actions" }),
        load: kpi("Load", "cpu"), mem: kpi("Memory", "memory"), disk: kpi("Disk", "disk"), up: kpi("Host uptime", "clock"), procs: kpi("Processes", "processes"),
        facts: h("dl", { class: "facts" }), top: h("div"), note: h("div"),
      };
      root.append(h("div", { class: "page" },
        h("div", { class: "crumbs" }, h("a", { href: "#/servers", text: "Servers" }), icon("chevronRight"), h("span", { text: serverLabel(srv()) })),
        h("div", { class: "page-head" }, h("div", { class: "page-title" }, refs.title, refs.sub), refs.actions),
        refs.note,
        h("div", { class: "card kpis" }, refs.load, refs.mem, refs.disk, refs.up, refs.procs),
        h("div", { class: "grid-2 section" },
          h("div", {}, sectionHead("Busiest processes", null, h("a", { class: "more", href: `#/s/${encodeURIComponent(srv())}/processes` }, "All processes", icon("chevronRight"))), refs.top),
          h("div", {}, sectionHead("System"), h("div", { class: "card" }, refs.facts)))));
    },
    update() {
      const s = serverInfo(srv());
      const label = serverLabel(srv());
      document.title = `${label} · procly`;
      const sysE = S.system.get(srv()) || {};
      const sys = sysE.data;
      fill(refs.title, label, s && h("span", { class: "status " + (s.ok ? "online" : "down"), text: s.ok ? "connected" : "unreachable" }));
      fill(refs.sub, h("span", { class: "mono", text: !s ? "" : s.local ? "this server" : s.url === "tunnel" ? "connects in through the hub" : s.url }), sys ? ` · procly ${shortVersion(sys.version)}` : "");
      fill(refs.actions, 
        h("button", { class: "btn", onclick: () => saveList(srv()) }, icon("save"), "Save list"),
        h("a", { class: "btn primary", href: `#/s/${encodeURIComponent(srv())}/processes` }, icon("processes"), "Open processes"));
      fill(refs.note,
        s && !s.ok ? callout("bad", "alert", `Can't reach ${label}`, s.error || "No answer from its agent.")
        : sysE.error ? callout("info", "info", "No host stats", sysE.error) : null);

      if (sys) {
        const cores = sys.cpus || 1;
        setKpi(refs.load, sys.load[0].toFixed(2), `5m ${sys.load[1].toFixed(2)} · 15m ${sys.load[2].toFixed(2)} · ${plural(cores, "core")}`, sys.load[0] > cores ? "warn" : "");
        if (sys.mem_available) {
          const used = sys.mem_total - sys.mem_available;
          setKpi(refs.mem, Math.round((used / sys.mem_total) * 100) + "%", `${fmtBytes(used)} of ${fmtBytes(sys.mem_total)}`, used / sys.mem_total > 0.9 ? "bad" : "");
        } else setKpi(refs.mem, fmtBytes(sys.mem_total), "total");
        const du = sys.disk_total - sys.disk_free;
        setKpi(refs.disk, sys.disk_total ? Math.round((du / sys.disk_total) * 100) + "%" : "–", sys.disk_total ? `${fmtBytes(sys.disk_free)} free of ${fmtBytes(sys.disk_total)}` : "", sys.disk_total && du / sys.disk_total > 0.9 ? "bad" : "");
        setKpi(refs.up, fmtUptime(sys.uptime_s * 1000), "since " + new Date(Date.now() - sys.uptime_s * 1000).toLocaleDateString());
      } else {
        for (const k of [refs.load, refs.mem, refs.disk, refs.up]) setKpi(k, "–", sysE.error ? "not available" : "loading…");
      }
      const list = procsOf(srv());
      const c = countStatus(list);
      setKpi(refs.procs, h("span", {}, String(c.online), h("small", { text: "/ " + list.length })), list.filter(needsAttention).length ? plural(list.filter(needsAttention).length, "needs", "need") + " attention" : "online", list.filter(needsAttention).length ? "warn" : "");

      const facts = h("dl");
      const add = (k, v) => facts.append(h("dt", { text: k }), h("dd", { text: v || "–", title: v || "" }));
      if (sys) {
        add("Hostname", sys.hostname); add("System", sys.distro || sys.os); add("Kernel", sys.kernel); add("Architecture", sys.arch);
        add("CPU cores", String(sys.cpus)); add("procly", sys.version);
      }
      add("Address", !s ? "" : s.local ? "local socket" : s.url === "tunnel" ? "tunnel through the hub" : s.url);
      fill(refs.facts, ...facts.childNodes);

      const top = [...list].filter((p) => p.pid).sort((a, b) => b.cpu - a.cpu || b.memory - a.memory).slice(0, 6);
      fill(refs.top, top.length ? h("div", { class: "card list" }, top.map((p) => h("a", { class: "list-item", href: `#/s/${encodeURIComponent(srv())}/p/${encodeURIComponent(p.name)}` },
        h("div", { class: "grow" }, h("div", { class: "title" }, h("b", { text: p.name })), h("div", { class: "sub mono", text: p.command })),
        h("span", { class: "num muted", text: fmtBytes(p.memory) }),
        h("span", { class: "num", text: p.cpu.toFixed(1) + "%" })))) : h("div", { class: "card" }, emptyBox("processes", "Nothing running", "Running processes are listed here by CPU.")));
    },
  };
};

// ----- settings -----

PAGES.settings = () => {
  let refs;
  return {
    mount(root) {
      document.title = "Settings · procly";
      refs = { theme: h("div", { class: "seg", role: "group", "aria-label": "Theme" }), panel: h("dl", { class: "facts" }) };
      const keyRow = (label, ...keys) => h("div", { class: "key-row" }, h("span", { text: label }), h("span", { class: "keys" }, keys.map((k) => h("kbd", { text: k }))));
      root.append(h("div", { class: "page" },
        pageHead({ title: "Settings", sub: "This panel and how procly reaches you." }),
        h("div", { class: "settings" },
          h("div", { class: "card" },
            h("div", { class: "card-title" }, icon("monitor"), h("h2", { text: "Appearance" })),
            h("div", { class: "set-row" }, h("div", { class: "grow" }, h("b", { text: "Theme" }), h("p", { text: "System follows your computer. Saved in this browser." })), refs.theme)),
          h("div", { class: "card" },
            h("div", { class: "card-title" }, icon("key"), h("h2", { text: "Panel" }), h("button", { class: "btn small right", onclick: logout }, icon("logout"), "Sign out")),
            refs.panel,
            h("div", { class: "set-row" }, h("div", { class: "grow" }, h("b", { text: "Forgot the password?" }), h("p", { text: "Stop the running panel, then start it again with this flag. It prints a new password and signs every session out." })), cmdline("procly web --reset-password"))),
          h("div", { class: "card" },
            h("div", { class: "card-title" }, icon("bell"), h("h2", { text: "Alerts" })),
            h("div", { class: "set-row" }, h("div", { class: "grow" }, h("b", { text: "Crash and health alerts" }), h("p", { text: "procly messages you when a process crashes, gives up or fails its health check. At most one message per process every 5 minutes." }))),
            h("ol", { class: "steps" },
              h("li", {}, h("span", { text: "Telegram: create a bot with @BotFather, then:" }), cmdline("procly alerts telegram --token <bot token> --chat <chat id>")),
              h("li", {}, h("span", { text: "Slack: make an incoming webhook, then:" }), cmdline("procly alerts slack --webhook <https://hooks.slack.com/…>")),
              h("li", {}, h("span", { text: "Check what is set up:" }), cmdline("procly alerts")))),
          h("div", { class: "card", id: "keys" },
            h("div", { class: "card-title" }, icon("keyboard"), h("h2", { text: "Keyboard shortcuts" })),
            h("div", { class: "keys-grid" },
              keyRow("Overview", "g", "o"), keyRow("Processes", "g", "p"),
              keyRow("Servers", "g", "s"), keyRow("Settings", "g", ","),
              keyRow("Next / previous server", "[", "]"), keyRow("Filter or search", "/"),
              keyRow("New process", "n"), keyRow("Restart (on a process)", "r"),
              keyRow("Stop or start", "s"), keyRow("Logs / events / config", "l", "e", "c"))))));
    },
    update() {
      const cur = load("procly.theme") || "system";
      fill(refs.theme, ...[["system", "System", "monitor"], ["light", "Light", "sun"], ["dark", "Dark", "moon"]].map(([id, label, ic]) =>
        h("button", { type: "button", "aria-pressed": String(cur === id), onclick: () => { setTheme(id); this.update(); } }, icon(ic), label)));
      fill(refs.panel, 
        h("dt", { text: "Server" }), h("dd", { text: S.info.host }),
        h("dt", { text: "Version" }), h("dd", { text: S.info.version }),
        h("dt", { text: "Session" }), h("dd", {}, h("span", { class: "muted", text: "Signed in on this browser. Sessions end after 7 days or when you sign out." })));
    },
  };
};

function setTheme(t) {
  store("procly.theme", t === "system" ? null : t);
  applyTheme();
}

function applyTheme() {
  const t = load("procly.theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
  else delete document.documentElement.dataset.theme;
}

// ---------- keys ----------

let gPending = 0;
document.addEventListener("keydown", (e) => {
  if ($("app").hidden || $("confirm").open || $("new-dialog").open) return;
  if (e.key === "Escape") {
    if (openMenu) { closeMenu(); return; }
    if (!$("switch-menu").hidden) { toggleSwitcher(false); return; }
    if ($("app").classList.contains("menu-open")) { $("scrim").click(); return; }
  }
  const t = e.target;
  if (t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement) {
    if (e.key === "Escape") { t.value = ""; t.dispatchEvent(new Event("input")); t.blur(); }
    return;
  }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (gPending && Date.now() - gPending < 1200) {
    gPending = 0;
    const dest = { o: "#/", p: `#/s/${encodeURIComponent(S.server)}/processes`, s: "#/servers", ",": "#/settings" }[e.key];
    if (dest) { e.preventDefault(); go(dest); }
    return;
  }
  if (e.key === "g") { gPending = Date.now(); return; }
  if (page && page.keys && page.keys(e)) return;
  if (e.key === "n") { e.preventDefault(); openNewProcess(); return; }
  if (e.key === "?") { go("#/settings"); return; }
  if ((e.key === "[" || e.key === "]") && S.servers.length > 1) {
    const i = S.servers.findIndex((s) => s.name === S.server);
    const next = S.servers[(i + (e.key === "]" ? 1 : -1) + S.servers.length) % S.servers.length];
    switchServer(next.name);
  }
});

// ---------- login / boot ----------

function showLogin(message) {
  stopPolling();
  closeLogs();
  if (page && page.unmount) page.unmount();
  page = null;
  current = { page: "" };
  $("app").hidden = true;
  $("login").hidden = false;
  $("login-error").textContent = message || "";
  $("password").value = "";
  $("password").focus();
}

async function logout() {
  try { await api("POST", "/api/logout"); } catch { /* signing out anyway */ }
  showLogin();
}

$("logout-btn").addEventListener("click", logout);

$("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const btn = $("login-btn");
  btn.disabled = true;
  $("login-error").textContent = "";
  try {
    await api("POST", "/api/login", { password: $("password").value });
    $("password").value = "";
    await startApp();
  } catch (err) {
    $("login-error").textContent = err.message;
    $("password").select();
  } finally {
    btn.disabled = false;
  }
});

async function startApp() {
  try {
    S.info = await api("GET", "/api/info");
  } catch (err) {
    if (err instanceof AuthError) return showLogin();
  }
  $("login").hidden = true;
  $("app").hidden = false;
  buildChrome();
  await Promise.allSettled([loadServers(), loadProcs(S.server)]);
  if (!S.procs.has(S.server)) await loadProcs(S.server);
  route();
  startPolling();
}

applyTheme();
startApp();
