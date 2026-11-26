# Changelog

## Unreleased

- `instances: N` (or `--instances N`): start several copies of an app, named `name-1` .. `name-N` (1 to 64), each with its own logs and `PROCLY_INSTANCE` (0-based) and `PROCLY_INSTANCES` in its environment. `stop`, `restart`, `reload` and `delete` with the base name act on the whole group, and a group restart goes one instance at a time. `ls` shows a group under a summary line; the panel's Processes page shows it as one row that opens, with a single confirmation that says how many processes are affected. `save` and `resurrect` keep groups; `ls --json` stays flat with `group`, `index` and `instances`. These are separate processes, not cluster mode; derive a port from `PROCLY_INSTANCE`. Names that are taken (or a group named like a process) are refused before anything starts.
- `procly web --tls-cert <file> --tls-key <file>`: the panel serves HTTPS itself (TLS 1.2 and newer, HTTP/2, `Secure` session cookie). A certificate that cannot be loaded stops it at start. Renewed certificates are picked up without a restart: the files are checked every ten seconds (a symlink moved to a new certificate counts), a half-written or broken pair is reported once and the previous certificate keeps being served, and `SIGHUP` loads them again at once (`procly reload panel` with `--reload-signal SIGHUP`).
- `procly web --trusted-proxy <address or range>` (repeatable, or comma separated): behind a reverse proxy the login lockout now counts against the real visitor, not the proxy's address, so one person's typos no longer lock everyone out. A connection from a trusted address is believed about `X-Forwarded-For` (the last address that is not itself a trusted proxy is used); the header from any other address is ignored, and nothing is trusted by default. The panel prints what it trusts and warns about `0.0.0.0/0`.
- Scheduled restarts: `restart_every: 24h` (from each start) and `restart_at: "04:00"` (every day, server local time), or `--restart-every` and `--restart-at`. Recorded as a `scheduled` event, they show in the restarts column but leave the give-up counter alone, and `save` and `resurrect` keep them. `restart_every` needs a unit and at least a minute; no cron expressions yet. The panel and `describe` show the schedule.
- `stop_signal` and `reload_signal`, and `procly reload <name|id|all>`. Stopping sends `stop_signal` (default `SIGTERM`) to the process group and `SIGKILL` after `kill_timeout_ms`, as before. `procly reload` sends `reload_signal` to the process, records a `reloaded` event, does not touch the restart counters, and fails clearly when none is set (`all` reloads those that have one). `--stop-signal` and `--reload-signal` on `start`; a Reload button in the panel; `describe` and `ls --json` show them.
- **Behaviour change:** managed processes now start with the default SIGHUP handling. The daemon used to ignore SIGHUP, and an ignored signal is inherited by every process it starts, so shells and other programs that keep what they inherit could not handle SIGHUP at all (which made `reload_signal: SIGHUP` do nothing for them). The daemon now catches SIGHUP, and so still survives a closing terminal.
- `max_memory`: restart a process that uses too much memory. `max_memory: 500MB` in `procly.yml` or `--max-memory 500MB` on `start`; the process and everything it started (its process group) are measured every five seconds. A restart is recorded as a `memory` event (shown in the panel's Events tab) and sends an alert. A leak found after 30 seconds or more is not a crash and does not count toward `max_restarts`; going over the limit within 30 seconds of starting counts like a quick crash, and procly gives up after `max_restarts` of them (`errored`, with a message to raise `max_memory`) instead of restarting every few seconds for ever. `describe` and the process page show the limit.
- Secrets without a command-line argument: `procly alerts telegram`, `alerts slack`, `agent join` and `server add` also read the token or webhook from an environment variable (`PROCLY_TELEGRAM_TOKEN`, `PROCLY_SLACK_WEBHOOK`, `PROCLY_TOKEN`) or from standard input with `--token -` / `--webhook -`, so it stays out of `ps` and the shell history. The flags work as before.
- `procly ls --json` and `procly describe <name> --json` print machine-readable output: the same objects the panel uses (stable field names, no colors, environment variable names but never values), also with `--server`. `ls --json` prints `[]` when no daemon is running.
- `procly start` error for a file it cannot run now names the file and a command to pass (`go run`, `java -jar`, or `<command>`), and says `chmod +x` makes it runnable directly. `.mjs`, `.cjs` and executable files already ran; they now have tests.

## v0.3.1

Apps no longer get lost when the daemon dies, `ls` is faster on Linux, and the numbers are from a real server.

- `procly ls` is faster on Linux: it reads CPU and memory from `/proc` instead of starting `ps` on every call. On one server that took `ls` from about 55 ms to 18 ms with ten apps. macOS still uses `ps`.
- If the daemon dies while apps are running (a crash, `kill -9`, the out-of-memory
  killer), the apps are no longer invisible and no longer started twice. The next
  daemon lists them as `orphaned` with their pid; `procly resurrect` and `procly start`
  skip an app that is still running from before; `procly stop`, `delete` and `kill`
  end them. `ls`, `stop`, `kill`, `describe`, `monit` and the panel start a daemon
  by themselves when survivors exist. `procly save` leaves orphans out, and
  `restart` refuses them. The daemon keeps `running.json` for this (no environment
  values), and checks the process fingerprint so a reused pid is never signalled.
  See [docs/daemon-failure.md](docs/daemon-failure.md).
- The panel and `procly monit` show orphans (yellow, an open ring) and offer Stop,
  not Restart.
- Under systemd, `kill -9` on the daemon is handled by the unit: systemd stops what
  is left, starts `daemon --resurrect` after two seconds and the saved apps come
  back, one copy each. Tested on a real server and written up in
  [docs/daemon-failure.md](docs/daemon-failure.md).
- The benchmark in the README was re-measured on a Linux x86_64 server (12 CPUs):
  daemon 11.5 MB against 76.2 MB for PM2 with ten apps, `ls` 18 ms against 308 ms.
  `scripts/bench.sh` no longer reads the size of a symlink instead of Node, and no
  longer leaves a PM2 daemon behind.
- Dependency updates: Bubble Tea and other Go modules, including `x/net` and
  `x/crypto`.

Upgrading: restart the daemon so it runs the new version (`procly kill`, then start
your apps or `procly resurrect`). Apps that an older daemon started are not tracked
for the orphan check until they are started again by the new one.

## v0.3.0

A new logo and a redesigned web panel.

- Branding: the keepalive-ring logo, colors and type are in `docs/brand`.
- Web panel, rebuilt with a sidebar and separate pages: Overview, Processes, a
  page per process (overview, logs, events, configuration), Servers and
  Settings. Phone-friendly, with a light, dark or system theme.
- Confirmations before restart, stop and delete; deleting asks for the name.
- Every server shows load, memory, disk and uptime. Agents report them at
  `/v1/system`; agents older than this version just show no host stats.
- Each process remembers its last 40 events (starts, stops, crashes, give-ups,
  failed health checks), with the stderr lines of the run that crashed.
- Searchable logs that can be paused, wrapped and downloaded.
- Issue templates, and Discussions for questions.

Upgrading: restart the daemon so it runs the new version, and update agents
too, or their servers show no host stats in the panel. Nothing else changes.

## v0.2.0

Keeps servers tidy and tells you when something goes wrong.

- Log rotation: logs rotate at 10 MB and keep 3 old files by default
  (`--log-max-size`, `--log-keep`, `log_max_size`, `log_keep`), without
  restarting the process. The daemon's own log rotates too.
- Health checks: `--health http://…` or `tcp://host:port`; three failures in a
  row restart the process. Shown in `ls`, `describe` and the web panel.
- Alerts: `procly alerts telegram|slack|test|off`. Messages on crashes (at most
  one per process every 5 minutes), give-ups and failed health checks.
- Web panel: **New process** form (`n`) to start a command on the panel's server.

## v0.1.0 — first release

procly is a process manager for your servers: one small Go binary that runs
Node, Python, Go or any other command, restarts it when it crashes, keeps its
logs, and brings everything back after a reboot.

### Run processes

- `procly start` takes a file (`server.js`, `worker.py`, `job.sh`), any command
  (`"npm run start"`, `"PORT=3000 node app.js"`) or a `procly.yml` with many apps.
- Crashes restart with exponential backoff (1s, 2s, 4s… up to 30s); after 15
  quick crashes in a row the process is marked errored instead of looping.
- `stop` sends SIGTERM to the whole process group, then SIGKILL after 5s.
- `stop`, `restart`, `delete`, `describe` and `logs` take a name, an id or `all`.
- `procly save` / `resurrect`, and `procly startup` for systemd (Linux) or
  launchd (macOS) to bring everything back after a reboot.

### Watch them

- `procly ls`: a table with status, PID, uptime, restarts, CPU and memory.
- `procly logs`: recent lines and live output, stderr marked, `--lines`,
  `--out`, `--err`, `--no-follow`.
- `procly monit`: a terminal dashboard with CPU/memory graphs, live logs and
  keys to restart, stop, start and delete.
- `procly web`: the same in the browser, built into the binary. Password login
  (bcrypt), HttpOnly SameSite cookies, CSRF protection, per-IP login limit,
  strict CSP. Works on phones; follows light and dark mode.

### Many servers

- `procly agent` on each reachable server and `procly server add` on the hub.
  TLS 1.3 with the agent's certificate pinned by fingerprint; the agent stores
  only a hash of its token and can't be used to start new commands.
- Behind NAT: `procly hub` plus `procly server invite`, and on the other side
  `procly agent join` / `procly agent connect`. The agent dials out and keeps
  one connection; requests and logs run as HTTP/2 streams over it.
- `--server <name>` (or `PROCLY_SERVER`) runs `ls`, `describe`, `stop`,
  `restart`, `delete`, `logs` and `save` on any connected server, and the web
  panel shows a tab per server.

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/itsahmadnyc/procly/main/install.sh | sh
```

Prebuilt for linux/darwin on amd64 and arm64. Checksums are in `checksums.txt`.
