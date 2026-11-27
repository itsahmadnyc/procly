'use strict';
// procly's cluster launcher. It runs a Node script as several workers that
// share the ports the script listens on, the way PM2's cluster mode does, and
// is what procly supervises in cluster mode: one process whose children are
// the workers.
//
//   node cluster_launcher.js --workers N [--exec-argv <flag>]... -- script [args...]
//
// - A worker that dies is replaced (after a growing pause if it keeps dying
//   young). If they all die in a loop the launcher exits with 1, and procly's
//   own restart policy takes over.
// - SIGUSR2 reloads: workers are replaced one at a time, the new one has to be
//   listening (or have been up for a while) before the old one is told to go,
//   so the port is never without a worker.
// - SIGTERM and SIGINT stop: workers get SIGTERM, then SIGKILL after 8 s.
const cluster = require('node:cluster');

if (!(cluster.isPrimary ?? cluster.isMaster)) {
  throw new Error('procly cluster launcher: only meant to run as the primary');
}

const argv = process.argv.slice(2);
let workers = 1;
const execArgv = [];
let i = 0;
for (; i < argv.length && argv[i] !== '--'; i++) {
  if (argv[i] === '--workers') workers = Math.max(1, parseInt(argv[++i], 10) || 1);
  else if (argv[i] === '--exec-argv') execArgv.push(argv[++i]);
}
const [script, ...args] = argv.slice(i + 1);
if (!script) {
  console.error('procly cluster launcher: no script given');
  process.exit(2);
}

const log = (msg) => console.log(`[procly] cluster: ${msg}`);
(cluster.setupPrimary ?? cluster.setupMaster).call(cluster, { exec: script, args, execArgv });

const slots = new Array(workers).fill(null);
const fails = new Array(workers).fill(0); // young deaths in a row, per slot
const deaths = []; // times of recent deaths, for spotting a crash loop
let stopping = false;
let reloading = false;

function fork(slot) {
  const w = cluster.fork({
    PROCLY_INSTANCE: String(slot),
    PROCLY_INSTANCES: String(workers),
    NODE_APP_INSTANCE: String(slot),
  });
  w.slot = slot;
  w.born = Date.now();
  slots[slot] = w;
  return w;
}

const alive = () => Object.values(cluster.workers).filter((w) => w && !w.isDead());

function stop(code) {
  if (stopping) return;
  stopping = true;
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
  if (w.expected) return; // a reload sent it away
  if (slots[w.slot] === w) slots[w.slot] = null;
  if (stopping) {
    if (!alive().length) process.exit(process.exitCode || 0);
    return;
  }
  const young = Date.now() - w.born < 10000;
  fails[w.slot] = young ? fails[w.slot] + 1 : 0;
  const now = Date.now();
  deaths.push(now);
  while (deaths.length && now - deaths[0] > 30000) deaths.shift();
  log(`worker ${w.slot} (pid ${w.process.pid}) ${signal ? 'was killed by ' + signal : 'exited with code ' + code}`);
  if (deaths.length > Math.max(10, workers * 4)) {
    log('workers keep dying; giving up so procly can decide what to do');
    stop(1);
    return;
  }
  const pause = young ? Math.min(250 * 2 ** (fails[w.slot] - 1), 10000) : 0;
  setTimeout(() => { if (!stopping && !slots[w.slot]) fork(w.slot); }, pause);
});

// resolves when the worker is listening, has died, or has been up for 10 s
const ready = (w) => new Promise((resolve) => {
  const done = () => { clearTimeout(t); resolve(); };
  const t = setTimeout(done, 10000);
  w.once('listening', done);
  w.once('exit', done);
});

async function reload() {
  if (reloading || stopping) return;
  reloading = true;
  log('reloading workers, one at a time');
  for (let slot = 0; slot < workers && !stopping; slot++) {
    const old = slots[slot];
    const fresh = fork(slot); // the slot now holds the new worker
    await ready(fresh);
    if (!old || old.isDead()) continue;
    old.expected = true;
    old.disconnect();
    const t = setTimeout(() => { if (!old.isDead()) old.process.kill('SIGTERM'); }, 5000);
    old.once('exit', () => clearTimeout(t));
  }
  reloading = false;
  log('reload done');
}

process.on('SIGTERM', () => stop(0));
process.on('SIGINT', () => stop(0));
process.on('SIGUSR2', () => { reload().catch((e) => { reloading = false; log(`reload failed: ${e.message}`); }); });

log(`${workers} worker${workers === 1 ? '' : 's'} of ${script}`);
for (let s = 0; s < workers; s++) fork(s);
