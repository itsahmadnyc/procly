'use strict';
// procly's cluster launcher. It runs a Node script as several workers that
// share the ports the script listens on, the way PM2's cluster mode does, and
// is what procly supervises in cluster mode: one process whose children are
// the workers.
//
//   node cluster_launcher.js --workers N [--exec-argv <flag>]...
//        [--max-worker-memory BYTES] [--listen-timeout MS]
//        [--status FILE] [--ctl FILE] -- script [args...]
//
// - Workers start one at a time: the next one is forked when the previous is
//   listening (or sent 'ready', or died, or --listen-timeout passed). An app
//   that syncs its database before it listens therefore does not do it N times
//   at once. Each worker has NODE_APP_INSTANCE (and PROCLY_INSTANCE) set to its
//   slot, so start-up work can be done by slot 0 only, as with PM2.
// - A worker that dies is replaced (after a growing pause if it keeps dying
//   young). If they keep dying, or the first workers never manage to start,
//   the launcher exits with 1 and procly's own restart policy takes over.
// - A worker over --max-worker-memory is replaced gracefully, the new one
//   listening before the old one is told to go (max_memory is per worker here,
//   as in PM2).
// - SIGUSR2 reloads: workers are replaced one at a time, the new one has to be
//   listening (or have been up for --listen-timeout) before the old one goes,
//   so the port is never without a worker.
// - --ctl names a file procly writes {"workers": N} to, to scale without a
//   restart: workers are added or gracefully removed.
// - --status names a file the launcher keeps up to date with the state of each
//   worker (pid, state, restarts, memory) for procly to show.
// - SIGTERM and SIGINT stop: workers get SIGTERM, then SIGKILL after 8 s.
const cluster = require('node:cluster');
const fs = require('node:fs');
const { execFile } = require('node:child_process');

if (!(cluster.isPrimary ?? cluster.isMaster)) {
  throw new Error('procly cluster launcher: only meant to run as the primary');
}

const argv = process.argv.slice(2);
let workers = 1;
let maxMem = 0;
let listenTimeout = 15000;
let statusFile = '';
let ctlFile = '';
const execArgv = [];
let i = 0;
for (; i < argv.length && argv[i] !== '--'; i++) {
  if (argv[i] === '--workers') workers = Math.max(1, parseInt(argv[++i], 10) || 1);
  else if (argv[i] === '--exec-argv') execArgv.push(argv[++i]);
  else if (argv[i] === '--max-worker-memory') maxMem = Math.max(0, parseInt(argv[++i], 10) || 0);
  else if (argv[i] === '--listen-timeout') listenTimeout = Math.max(1000, parseInt(argv[++i], 10) || 15000);
  else if (argv[i] === '--status') statusFile = argv[++i];
  else if (argv[i] === '--ctl') ctlFile = argv[++i];
}
const [script, ...args] = argv.slice(i + 1);
if (!script) {
  console.error('procly cluster launcher: no script given');
  process.exit(2);
}

const log = (msg) => console.log(`[procly] cluster: ${msg}`);
(cluster.setupPrimary ?? cluster.setupMaster).call(cluster, { exec: script, args, execArgv });

const slots = [];      // slot -> current worker (or null)
const fails = [];      // young deaths in a row, per slot
const restarts = [];   // times a slot's worker has been replaced, per slot
const rss = {};        // pid -> resident bytes, from the last sample
const deaths = [];     // times of recent deaths, for spotting a crash loop
const memReplaced = []; // times a worker was replaced for memory, per slot (recent)
let startupFails = 0;
let everReady = false;
let stopping = false;
let reloading = false;
let busy = new Set(); // slots being replaced right now

const MB = (n) => `${Math.round(n / 1048576)} MB`;
const alive = () => Object.values(cluster.workers).filter((w) => w && !w.isDead());

function writeStatus() {
  if (!statusFile) return;
  const list = [];
  for (let s = 0; s < slots.length; s++) {
    const w = slots[s];
    list.push({
      slot: s,
      pid: w && !w.isDead() ? w.process.pid : 0,
      state: !w || w.isDead() ? 'dead' : w.state || 'starting',
      restarts: restarts[s] || 0,
      rss: w && !w.isDead() ? rss[w.process.pid] || 0 : 0,
      started: w ? w.born : 0,
    });
  }
  const body = JSON.stringify({ pid: process.pid, target: workers, ready: everReady, reloading, stopping, updated: Date.now(), workers: list });
  try {
    fs.writeFileSync(statusFile + '.tmp', body);
    fs.renameSync(statusFile + '.tmp', statusFile);
  } catch { /* the status is a convenience */ }
}

function fork(slot) {
  const w = cluster.fork({
    PROCLY_INSTANCE: String(slot),
    PROCLY_INSTANCES: String(workers),
    NODE_APP_INSTANCE: String(slot),
  });
  w.slot = slot;
  w.born = Date.now();
  w.state = 'starting';
  w.on('online', () => { if (w.state === 'starting') w.state = 'online'; writeStatus(); });
  w.on('listening', () => { w.state = 'listening'; everReady = true; writeStatus(); });
  w.on('message', (m) => { if (m === 'ready') { w.state = 'listening'; everReady = true; writeStatus(); } });
  slots[slot] = w;
  writeStatus();
  return w;
}

// resolves when the worker is listening, said ready, died, or the time is up
const ready = (w) => new Promise((resolve) => {
  const done = () => { clearTimeout(t); resolve(); };
  const t = setTimeout(done, listenTimeout);
  w.once('listening', done);
  w.once('message', (m) => { if (m === 'ready') done(); });
  w.once('exit', done);
});

function retire(old) {
  if (!old || old.isDead()) return;
  old.expected = true;
  try { old.disconnect(); } catch { /* already gone */ }
  const t = setTimeout(() => { if (!old.isDead()) old.process.kill('SIGTERM'); }, 5000);
  old.once('exit', () => { clearTimeout(t); writeStatus(); });
}

function stop(code) {
  if (stopping) return;
  stopping = true;
  writeStatus();
  const ws = alive();
  if (!ws.length) process.exit(code);
  log(`stopping ${ws.length} worker${ws.length === 1 ? '' : 's'}`);
  for (const w of ws) w.process.kill('SIGTERM');
  setTimeout(() => {
    for (const w of alive()) {
      try { w.process.kill('SIGKILL'); } catch { /* gone */ }
    }
    process.exit(code);
  }, 8000).unref();
  process.exitCode = code;
}

cluster.on('exit', (w, code, signal) => {
  if (w.expected) { writeStatus(); return; } // we sent it away
  if (slots[w.slot] === w) slots[w.slot] = null;
  writeStatus();
  if (stopping) {
    if (!alive().length) process.exit(process.exitCode || 0);
    return;
  }
  if (w.slot >= workers) return; // scaled away meanwhile
  const young = Date.now() - w.born < 10000;
  fails[w.slot] = young ? (fails[w.slot] || 0) + 1 : 0;
  const now = Date.now();
  deaths.push(now);
  while (deaths.length && now - deaths[0] > 30000) deaths.shift();
  log(`worker ${w.slot} (pid ${w.process.pid}) ${signal ? 'was killed by ' + signal : 'exited with code ' + code}`);
  if (!everReady && young) {
    startupFails++;
    if (startupFails >= 3) {
      log('no worker managed to start (3 tries); giving up so procly can decide what to do');
      stop(1);
      return;
    }
  }
  if (deaths.length > Math.max(10, workers * 4)) {
    log('workers keep dying; giving up so procly can decide what to do');
    stop(1);
    return;
  }
  restarts[w.slot] = (restarts[w.slot] || 0) + 1;
  const pause = young ? Math.min(250 * 2 ** (fails[w.slot] - 1), 10000) : 0;
  setTimeout(() => { if (!stopping && w.slot < workers && !slots[w.slot]) fork(w.slot); writeStatus(); }, pause);
});

// replace swaps one slot's worker for a new one without a gap.
async function replace(slot) {
  const old = slots[slot];
  const fresh = fork(slot); // the slot now holds the new worker
  await ready(fresh);
  retire(old);
}

async function reload() {
  if (reloading || stopping) return;
  reloading = true;
  writeStatus();
  log('reloading workers, one at a time');
  for (let slot = 0; slot < workers && !stopping; slot++) await replace(slot);
  reloading = false;
  writeStatus();
  log('reload done');
}

// memory: every 5 s read the workers' resident size, replace the ones over the limit
function sample() {
  const pids = alive().map((w) => w.process.pid);
  if (!pids.length || stopping) return;
  execFile('ps', ['-o', 'pid=,rss=', '-p', pids.join(',')], (err, out) => {
    if (err && !out) return;
    for (const k of Object.keys(rss)) delete rss[k];
    for (const line of String(out).split('\n')) {
      const [pid, kb] = line.trim().split(/\s+/).map(Number);
      if (pid) rss[pid] = kb * 1024;
    }
    writeStatus();
    if (!maxMem || reloading) return;
    for (let s = 0; s < workers; s++) {
      const w = slots[s];
      if (!w || w.isDead() || busy.has(s) || !rss[w.process.pid] || rss[w.process.pid] <= maxMem) continue;
      const used = rss[w.process.pid];
      const now = Date.now();
      memReplaced[s] = (memReplaced[s] || []).filter((t) => now - t < 120000);
      memReplaced[s].push(now);
      if (memReplaced[s].length >= 3) {
        log(`worker ${s} went over its memory limit (${MB(used)} > ${MB(maxMem)}) 3 times in 2 minutes; the limit is too low for this app, giving up`);
        stop(1);
        return;
      }
      log(`worker ${s} uses ${MB(used)}, over its limit of ${MB(maxMem)}; replacing it`);
      restarts[s] = (restarts[s] || 0) + 1;
      busy.add(s);
      replace(s).catch(() => {}).finally(() => busy.delete(s));
    }
  });
}

// scale changes the number of workers without restarting the others
let scaling = Promise.resolve();
function scaleTo(n) {
  scaling = scaling.then(async () => {
    if (stopping || n === workers) return;
    log(`scaling from ${workers} to ${n} workers`);
    if (n > workers) {
      const from = workers;
      workers = n;
      for (let s = from; s < n && !stopping; s++) { const w = fork(s); await ready(w); }
    } else {
      for (let s = workers - 1; s >= n; s--) { const w = slots[s]; slots[s] = null; retire(w); }
      slots.length = n;
      workers = n;
    }
    writeStatus();
  }).catch((e) => log(`scale failed: ${e.message}`));
}

let lastCtl = 0;
function pollCtl() {
  if (!ctlFile || stopping) return;
  fs.stat(ctlFile, (err, st) => {
    if (err || st.mtimeMs <= lastCtl) return;
    lastCtl = st.mtimeMs;
    fs.readFile(ctlFile, 'utf8', (e2, text) => {
      if (e2) return;
      try {
        const n = parseInt(JSON.parse(text).workers, 10);
        if (n >= 1 && n <= 64) scaleTo(n);
      } catch { /* not for us */ }
    });
  });
}

process.on('SIGTERM', () => stop(0));
process.on('SIGINT', () => stop(0));
process.on('SIGUSR2', () => { reload().catch((e) => { reloading = false; log(`reload failed: ${e.message}`); }); });

log(`${workers} worker${workers === 1 ? '' : 's'} of ${script}`);
if (ctlFile) { try { lastCtl = fs.statSync(ctlFile).mtimeMs; } catch { /* none yet */ } }
setInterval(sample, 5000).unref();
setInterval(pollCtl, 1000).unref();
setInterval(writeStatus, 3000).unref();
(async () => {
  for (let s = 0; s < workers && !stopping; s++) {
    if (!slots[s]) { const w = fork(s); await ready(w); }
  }
})();
