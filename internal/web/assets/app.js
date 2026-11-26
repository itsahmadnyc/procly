"use strict";

// procly web panel. Plain DOM, no framework. Every value from the server is
// written with textContent, never innerHTML, so names and log lines can't
// inject markup.

const POLL_MS = 2000;
const SERVERS_POLL_MS = 5000;
const HISTORY = 60;
const LOG_KEEP = 3000;

const $ = (id) => document.getElementById(id);

const state = {
  server: "local",
  servers: [],
  host: "",
  serverTimer: null,
  procs: [],
  sel: null,
  filter: "",
  hist: new Map(), // "server/name" -> {pid, cpu: [], mem: []}
  busy: new Map(), // name -> verb
  stream: null,
  streamName: null,
  follow: true,
  pollTimer: null,
  daemonDown: false,
};

// ---------- api ----------

const base = () => `/api/servers/${encodeURIComponent(state.server)}`;
const histKey = (name) => `${state.server}\u0000${name}`;

class AuthError extends Error {}

async function api(method, path, body) {
  const opts = { method, headers: { "X-Procly": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
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

// ---------- formatting ----------

function fmtBytes(b) {
  if (!b) return "–";
  const u = ["B", "KB", "MB", "GB"];
  let i = 0;
  while (b >= 1024 && i < u.length - 1) { b /= 1024; i++; }
  return (i >= 2 ? b.toFixed(1) : Math.round(b)) + " " + u[i];
}

function fmtUptime(ms) {
  if (!ms) return "–";
  const s = Math.floor(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60);
  if (m < 60) return m + "m";
  const h = Math.floor(m / 60);
  if (h < 24) return h + "h " + (m % 60) + "m";
  return Math.floor(h / 24) + "d " + (h % 24) + "h";
}

const progressive = { restart: "restarting", start: "starting", stop: "stopping", delete: "deleting" };
const past = { restart: "Restarted", start: "Started", stop: "Stopped", delete: "Deleted" };

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// ---------- login ----------

function showLogin(message) {
  stopPolling();
  closeStream();
  $("app").hidden = true;
  $("login").hidden = false;
  $("login-error").textContent = message || "";
  $("password").value = "";
  $("password").focus();
}

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

$("logout-btn").addEventListener("click", async () => {
  try { await api("POST", "/api/logout"); } catch { /* signing out anyway */ }
  showLogin();
});

// ---------- app ----------

async function startApp() {
  try {
    const info = await api("GET", "/api/info");
    state.host = info.host || "";
    state.home = info.home || "/";
    $("host").textContent = state.host;
  } catch (err) {
    if (err instanceof AuthError) return showLogin();
  }
  $("login").hidden = true;
  $("app").hidden = false;
  await Promise.all([refreshServers(), refresh()]);
  startPolling();
}

function startPolling() {
  stopPolling();
  state.pollTimer = setInterval(refresh, POLL_MS);
  state.serverTimer = setInterval(refreshServers, SERVERS_POLL_MS);
}

function stopPolling() {
  clearInterval(state.pollTimer);
  clearInterval(state.serverTimer);
  state.pollTimer = state.serverTimer = null;
}

// ---------- servers ----------

async function refreshServers() {
  let data;
  try {
    data = await api("GET", "/api/servers");
  } catch (err) {
    if (err instanceof AuthError) return showLogin("Your session ended. Sign in again.");
    return;
  }
  state.servers = data.servers || [];
  if (!state.servers.some((s) => s.name === state.server)) switchServer("local");
  renderServers();
}

function serverLabel(name) {
  const s = state.servers.find((x) => x.name === name);
  return s ? s.label : name;
}

function renderServers() {
  const nav = $("servers");
  const multi = state.servers.length > 1;
  nav.hidden = !multi;
  $("new-btn").hidden = state.server !== "local";
  $("host").textContent = multi ? serverLabel(state.server) : state.host;
  document.title = `procly · ${serverLabel(state.server) || "procly"}`;
  if (!multi) return;
  nav.replaceChildren();
  for (const s of state.servers) {
    const tab = el("button", "server-tab");
    tab.type = "button";
    tab.dataset.server = s.name;
    if (s.name === state.server) tab.setAttribute("aria-current", "true");
    const problems = (s.by_status.errored || 0) + (s.by_status.waiting || 0);
    const dot = el("span", "dot" + (!s.ok ? " down" : problems ? " warn" : ""));
    tab.title = s.ok ? `${s.total} processes` : s.error;
    tab.append(dot, el("span", "", s.label));
    if (s.local) tab.append(el("span", "local", "this server"));
    tab.append(el("span", "count", s.ok ? String(s.total) : "–"));
    nav.append(tab);
  }
}

$("servers").addEventListener("click", (e) => {
  const tab = e.target.closest(".server-tab");
  if (tab) switchServer(tab.dataset.server);
});

function switchServer(name) {
  if (name === state.server) return;
  state.server = name;
  state.procs = [];
  state.sel = null;
  state.filter = "";
  $("filter").value = "";
  closeStream();
  setDaemonDown(false);
  renderServers();
  render();
  refresh();
}

async function refresh() {
  try {
    const server = state.server;
    const procs = await api("GET", `${base()}/processes`);
    if (server !== state.server) return; // switched while loading
    state.procs = procs;
    setDaemonDown(false);
  } catch (err) {
    if (err instanceof AuthError) return showLogin("Your session ended. Sign in again.");
    state.procs = [];
    closeStream();
    setDaemonDown(true, err.status ? err.message : "Can't reach the panel server.");
    render();
    return;
  }
  record();
  if (state.sel && !state.procs.some((p) => p.name === state.sel)) state.sel = null;
  if (!state.sel && state.procs.length) state.sel = state.procs[0].name;
  render();
}

function setDaemonDown(down, msg) {
  state.daemonDown = down;
  $("banner").hidden = !down;
  if (down) $("banner").textContent = msg;
}

function record() {
  const seen = new Set();
  for (const p of state.procs) {
    seen.add(histKey(p.name));
    if (!p.pid) continue;
    let h = state.hist.get(histKey(p.name));
    if (!h || h.pid !== p.pid) { // a new run starts a new graph
      h = { pid: p.pid, cpu: [], mem: [] };
      state.hist.set(histKey(p.name), h);
    }
    h.cpu.push(p.cpu);
    h.mem.push(p.memory);
    if (h.cpu.length > HISTORY) { h.cpu.shift(); h.mem.shift(); }
  }
  const prefix = histKey("");
  for (const key of state.hist.keys()) if (key.startsWith(prefix) && !seen.has(key)) state.hist.delete(key);
}

function selected() {
  return state.procs.find((p) => p.name === state.sel) || null;
}

function visibleProcs() {
  const f = state.filter.trim().toLowerCase();
  return f ? state.procs.filter((p) => p.name.toLowerCase().includes(f)) : state.procs;
}

// ---------- render ----------

function render() {
  renderSummary();
  renderTable();
  renderDetail();
}

function renderSummary() {
  const box = $("summary");
  box.replaceChildren();
  if (state.daemonDown) return; // counts would read as "0 processes"
  const counts = {};
  for (const p of state.procs) counts[p.status] = (counts[p.status] || 0) + 1;
  const n = state.procs.length;
  box.append(el("span", "", `${n} ${n === 1 ? "process" : "processes"}`));
  for (const s of ["online", "waiting", "stopped", "errored"]) {
    if (!counts[s]) continue;
    box.append(el("span", "sep", "·"), el("span", "status " + s, `${counts[s]} ${s}`));
  }
}

function statusEl(p) {
  const verb = state.busy.get(p.name);
  if (verb) return el("span", "status waiting busy", progressive[verb]);
  return el("span", "status " + p.status, p.status);
}

function renderTable() {
  const rows = $("rows");
  const list = visibleProcs();
  rows.replaceChildren();
  for (const p of list) {
    const tr = el("tr", p.name === state.sel ? "sel" : "");
    tr.dataset.name = p.name;
    const up = p.pid > 0;
    const name = el("td", "name", p.name);
    name.title = p.name;
    const status = el("td");
    status.append(statusEl(p));
    tr.append(
      name,
      status,
      el("td", "num hide-sm dim", up ? String(p.pid) : "–"),
      el("td", "num", up ? p.cpu.toFixed(1) + "%" : "–"),
      el("td", "num", up ? fmtBytes(p.memory) : "–"),
      el("td", "num hide-sm", up ? fmtUptime(p.uptime_ms) : "–"),
      el("td", "num" + (p.restarts > 0 ? " warn" : " dim"), String(p.restarts)),
    );
    rows.append(tr);
  }

  const empty = $("empty");
  empty.replaceChildren();
  if (!state.procs.length && !state.daemonDown) {
    empty.append(el("div", "", `No processes on ${serverLabel(state.server)} yet. Start one there:`), el("code", "", "procly start app.js"));
    empty.hidden = false;
  } else if (state.procs.length && !list.length) {
    empty.append(el("div", "", "No process matches the filter."));
    empty.hidden = false;
  } else {
    empty.hidden = true;
  }
}

$("rows").addEventListener("click", (e) => {
  const tr = e.target.closest("tr");
  if (tr) select(tr.dataset.name);
});

function select(name) {
  if (!name || name === state.sel) return;
  state.sel = name;
  render();
}

function renderDetail() {
  const p = selected();
  $("detail-empty").hidden = !!p;
  $("detail-body").hidden = !p;
  if (!p) { closeStream(); return; }

  $("d-name").textContent = p.name;
  const st = $("d-status");
  const verb = state.busy.get(p.name);
  st.className = "status " + (verb ? "waiting busy" : p.status);
  st.textContent = verb ? progressive[verb] : p.status;

  const facts = $("d-facts");
  facts.replaceChildren();
  const add = (k, v, wide) => {
    const d = el("div", wide ? "wide" : "");
    const dd = el("dd", "", v);
    dd.title = v;
    d.append(el("dt", "", k), dd);
    facts.append(d);
  };
  add("id", String(p.id));
  if (p.pid) {
    add("pid", String(p.pid));
    add("uptime", fmtUptime(p.uptime_ms));
  } else {
    add("last exit", String(p.exit_code));
  }
  add("restarts", String(p.restarts));
  add("autorestart", p.autorestart ? "on" : "off");
  if (p.health_check) {
    add("health", (p.health || "not running") + " · " + p.health_check, true);
    const dd = facts.lastElementChild.querySelector("dd");
    if (p.health === "healthy" || p.health === "unhealthy") dd.classList.add(p.health);
    if (p.health_error) add("last check", p.health_error, true);
  }
  add("cwd", p.cwd, true);
  if (p.env_keys.length) add("env", p.env_keys.join(", "), true);

  $("d-cmd").textContent = p.command;

  const running = p.status === "online" || p.status === "waiting";
  const toggle = $("act-toggle");
  toggle.querySelector("span").textContent = running ? "Stop" : "Start";
  for (const id of ["act-restart", "act-toggle", "act-delete"]) $(id).disabled = !!verb;

  const h = state.hist.get(histKey(p.name));
  $("m-cpu").textContent = p.pid ? p.cpu.toFixed(1) + "%" : "–";
  $("m-mem").textContent = p.pid ? fmtBytes(p.memory) : "–";
  drawSpark($("s-cpu"), h ? h.cpu : [], { fromZero: true, minSpan: 10 });
  drawSpark($("s-mem"), h ? h.mem : [], { fromZero: false, minSpan: 8 * 1024 * 1024 });

  if (state.streamName !== histKey(p.name)) openStream(p.name);
}

const SVG_NS = "http://www.w3.org/2000/svg";

function drawSpark(svg, vals, { fromZero, minSpan }) {
  svg.replaceChildren();
  if (vals.length < 2) return;
  const w = 120, h = 32, pad = 2;
  let lo = Math.min(...vals), hi = Math.max(...vals);
  if (fromZero) lo = 0;
  const span = Math.max(hi - lo, minSpan);
  const step = w / (HISTORY - 1);
  const x0 = w - (vals.length - 1) * step;
  const pts = vals.map((v, i) => [x0 + i * step, h - pad - ((v - lo) / span) * (h - pad * 2)]);
  const line = pts.map(([x, y], i) => (i ? "L" : "M") + x.toFixed(2) + " " + y.toFixed(2)).join(" ");
  const area = document.createElementNS(SVG_NS, "path");
  area.setAttribute("class", "area");
  area.setAttribute("d", `${line} L${w} ${h} L${x0.toFixed(2)} ${h} Z`);
  const path = document.createElementNS(SVG_NS, "path");
  path.setAttribute("class", "line");
  path.setAttribute("d", line);
  svg.append(area, path);
}

// ---------- actions ----------

async function act(verb, name) {
  if (state.busy.has(name)) return;
  if (verb === "delete") {
    const ok = await confirmDelete(name);
    if (!ok) return;
  }
  state.busy.set(name, verb);
  render();
  try {
    await api("POST", `${base()}/processes/${encodeURIComponent(name)}/${verb}`);
    toast(`${past[verb]} ${name}` + (state.server === "local" ? "" : ` on ${serverLabel(state.server)}`));
  } catch (err) {
    if (err instanceof AuthError) return showLogin("Your session ended. Sign in again.");
    toast(err.message, true);
  } finally {
    state.busy.delete(name);
    await refresh();
  }
}

function confirmDelete(name) {
  const dlg = $("confirm");
  $("confirm-title").textContent = `Delete ${name}?`;
  $("confirm-text").textContent = "It will be stopped and removed from procly. Its log files stay on disk.";
  dlg.returnValue = "";
  dlg.showModal();
  return new Promise((resolve) => {
    dlg.addEventListener("close", () => resolve(dlg.returnValue === "ok"), { once: true });
  });
}

function toggleSelected() {
  const p = selected();
  if (!p) return;
  const running = p.status === "online" || p.status === "waiting";
  act(running ? "stop" : "start", p.name);
}

$("act-restart").addEventListener("click", () => state.sel && act("restart", state.sel));
$("act-toggle").addEventListener("click", toggleSelected);
$("act-delete").addEventListener("click", () => state.sel && act("delete", state.sel));

$("save-btn").addEventListener("click", async () => {
  try {
    const r = await api("POST", `${base()}/save`);
    toast(`Saved ${r.saved} ${r.saved === 1 ? "process" : "processes"} on ${serverLabel(state.server)}. They come back after a reboot.`);
  } catch (err) {
    if (err instanceof AuthError) return showLogin();
    toast(err.message, true);
  }
});

let toastTimer;
function toast(msg, isErr) {
  const t = $("toast");
  t.textContent = msg;
  t.className = "toast" + (isErr ? " err" : "");
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, isErr ? 6000 : 3000);
}

// ---------- new process ----------

function openNewProcess() {
  if (state.server !== "local") return;
  const form = $("new-form");
  form.reset();
  $("new-cwd").value = state.home || "/";
  $("new-host").textContent = state.host || "this server";
  $("new-error").textContent = "";
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

$("new-btn").addEventListener("click", openNewProcess);
$("new-cancel").addEventListener("click", () => $("new-dialog").close());

$("new-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const err = $("new-error");
  err.textContent = "";
  const name = $("new-name").value.trim();
  const command = $("new-command").value.trim();
  if (!name || !command) {
    err.textContent = "Name and command are required.";
    return;
  }
  let env;
  try { env = parseEnv($("new-env").value); } catch (ex) { err.textContent = ex.message; return; }
  const btn = $("new-submit");
  btn.disabled = true;
  try {
    await api("POST", `${base()}/processes`, {
      name, command, env,
      cwd: $("new-cwd").value.trim(),
      health: $("new-health").value.trim(),
      autorestart: $("new-restart").checked,
    });
    $("new-dialog").close();
    toast(`Started ${name}`);
    state.sel = name;
    await refresh();
  } catch (ex) {
    if (ex instanceof AuthError) return showLogin("Your session ended. Sign in again.");
    err.textContent = ex.message;
  } finally {
    btn.disabled = false;
  }
});

// ---------- logs ----------

function closeStream() {
  if (state.stream) state.stream.close();
  state.stream = null;
  state.streamName = null;
}

function setStreamState(text, live) {
  const s = $("stream-state");
  s.textContent = text;
  s.className = "stream-state" + (live ? " live" : "");
}

function openStream(name) {
  closeStream();
  const log = $("log");
  log.replaceChildren();
  state.follow = true;
  $("jump").hidden = true;
  state.streamName = histKey(name);
  setStreamState("connecting…");

  const es = new EventSource(`${base()}/processes/${encodeURIComponent(name)}/logs`);
  state.stream = es;
  let first = true;
  es.onopen = () => setStreamState("live", true);
  es.onmessage = (e) => {
    let batch;
    try { batch = JSON.parse(e.data); } catch { return; }
    appendLines(batch);
    if (first && !batch.length) log.append(el("div", "note", "No output yet."));
    first = false;
  };
  es.onerror = () => {
    if (es.readyState === EventSource.CLOSED) setStreamState("disconnected");
    else setStreamState("reconnecting…");
  };
}

function appendLines(batch) {
  const log = $("log");
  const note = log.querySelector(".note");
  if (note && batch.length) note.remove();
  const frag = document.createDocumentFragment();
  for (const { s, l } of batch) frag.append(el("div", s === "e" ? "ln err" : "ln", l || " "));
  log.append(frag);
  while (log.childElementCount > LOG_KEEP) log.firstElementChild.remove();
  if (state.follow) log.scrollTop = log.scrollHeight;
  else $("jump").hidden = false;
}

$("log").addEventListener("scroll", () => {
  const log = $("log");
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 24;
  state.follow = atBottom;
  if (atBottom) $("jump").hidden = true;
});

$("jump").addEventListener("click", () => {
  const log = $("log");
  log.scrollTop = log.scrollHeight;
  state.follow = true;
  $("jump").hidden = true;
});

$("clear-logs").addEventListener("click", () => $("log").replaceChildren());
$("only-err").addEventListener("change", (e) => $("log").classList.toggle("only-err", e.target.checked));

// ---------- filter + keys ----------

$("filter").addEventListener("input", (e) => {
  state.filter = e.target.value;
  renderTable();
});

document.addEventListener("keydown", (e) => {
  if ($("app").hidden || $("confirm").open || $("new-dialog").open) return;
  const t = e.target;
  const typing = t instanceof HTMLInputElement || t instanceof HTMLTextAreaElement;
  if (typing) {
    if (e.key === "Escape") { t.value = ""; state.filter = ""; t.blur(); renderTable(); }
    return;
  }
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  const list = visibleProcs();
  const i = list.findIndex((p) => p.name === state.sel);
  switch (e.key) {
    case "/": e.preventDefault(); $("filter").focus(); break;
    case "n": e.preventDefault(); openNewProcess(); break;
    case "j": case "ArrowDown":
      if (list.length) { e.preventDefault(); select(list[Math.min(i + 1, list.length - 1)].name); }
      break;
    case "k": case "ArrowUp":
      if (list.length) { e.preventDefault(); select(list[Math.max(i - 1, 0)].name); }
      break;
    case "r": if (state.sel) act("restart", state.sel); break;
    case "[": case "]": {
      if (state.servers.length < 2) break;
      const idx = state.servers.findIndex((s) => s.name === state.server);
      const next = (idx + (e.key === "]" ? 1 : -1) + state.servers.length) % state.servers.length;
      switchServer(state.servers[next].name);
      break;
    }
    case "s": toggleSelected(); break;
  }
});

document.addEventListener("visibilitychange", () => {
  if ($("app").hidden) return;
  if (document.hidden) stopPolling();
  else { refresh(); refreshServers(); startPolling(); }
});

// ---------- boot ----------

startApp();
