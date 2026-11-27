# Changelog

## v0.5.3

- **Processes page**: a summary strip (running, need attention, CPU, memory, restarts), two-line rows (name with a cluster tag, command under it), status pills, memory bars, always-visible restart and stop buttons, and a sort menu (name, CPU, memory, restarts, uptime).
- **Overview polish**: the resource chart fills its whole width from the first samples, in green, with the current and peak value in its title and the scale in the corner. Decorative icon tiles were tried and removed: they looked generated.

## v0.5.2

- **Mode where you can see it**: the New process dialog has Mode (fork or cluster) and Workers/Copies, and a process page has a Fork/Cluster button in its header that opens the switch dialog. Starting a process from the panel can now run it as a cluster.

## v0.5.1

- **Sign-in page** now fills the whole window (illustration on the left, form on the right) instead of a centered card.

## v0.5.0

Cluster mode, a password you can change, and a fuller server page.

- **Cluster mode**: `exec_mode: cluster` (or `procly start app.js --exec-mode cluster -i 4`) runs a Node app as a master plus workers that share its ports (`node:cluster`, connections handed round-robin). `instances` is the worker count, 0 means one per CPU. procly supervises the master; CPU and memory are summed over the whole group; `procly reload` is a rolling reload (one worker at a time, no dropped connections).
- **`procly mode <name> fork|cluster [-i n]`**: switch a running app between the two. It asks first, restarts the app, refuses without touching it when the app cannot run in that mode, and leaves a stopped app stopped. The panel gets the same through `POST /api/servers/<server>/processes/<name>/mode` (this server only).
- **Password change**: `POST /api/password` in the panel (current password required).
- **A livelier panel**: a greeting, KPI cards with status chips and a CPU/memory chart with a hover tooltip on the overview; a split-card sign-in page with its own illustration; skeletons while loading and pictures in empty states; gentle motion that stops for `prefers-reduced-motion`.
- **Log terminal themes** in Settings (Procly, Dracula, One Dark, Nord, Monokai, Tokyo Night, Gruvbox, Tomorrow Night, Solarized Dark and Light, GitHub Light), text size, and long lines wrap by default.
- **Change the password** and **switch fork/cluster** from the panel. Restart, stop, delete, mode switch, password change and save list all ask first, in the panel and in `procly monit` (press `m` there to switch mode).
- **Server page** shows CPU model, memory, swap, disk, uptime, Node version, the user procly runs as, time zone and every address.
- **Server info**: CPU model, swap, addresses, Node version, user and timezone are reported by `sysinfo`.

## v0.4.7

Which domain reaches an app.

- **`procly domains`**: for each running process, the TCP ports it listens on and the domain names that nginx, Caddy or Apache send to them: the name, the path (`/api`), `https` or `http`, which proxy and file, and where the name points in DNS (this server, Cloudflare, another address, or nowhere). `--json`, `--refresh` (look again), and `--server <name>` for a connected server. It reads `/etc/nginx/nginx.conf` and its includes (with a parser for nginx's syntax: `upstream` blocks, `include` globs and symlinked `sites-enabled`, quoted and wildcard names, comments), the Caddyfile (site blocks, `reverse_proxy`, `handle`, `import`) and Apache virtual hosts (`ProxyPass`, `ProxyPassMatch`, `<Location>`). It changes nothing. Tried on a real server with 89 nginx sites.
- **The panel**: a Domain column in the process table (shown when something has a domain), a Domain figure on a process's page, and a Domain row in its details with every name, how it is served, where it points, why none were found, and a Scan again button.
- **`procly monit`**: a domain column in the table and a domain line in the details ("none found, listens on :3000; no nginx, Caddy or Apache site sends traffic there"). `procly describe` has the same row.
- Ports of a process are found from `/proc` on Linux and with `lsof` on macOS. New: `GET /v1/domains` on the daemon and agent, `/api/servers/<name>/domains` in the panel. A server on an older version says it needs 0.4.7.

## v0.4.6

A redesigned web panel.

- **Answers first.** The overview opens with one sentence of state ("All 6 processes are running", "1 of 6 needs attention") and the few figures that matter beside it. What needs attention comes next, then the servers, the busiest processes and the recent activity as a plain log. The row of identical big-number tiles is gone.
- **The whole window.** No page has a maximum width any more. The panel used to stop at 1480 px and sit in a strip on the left of a wide screen; now the columns widen with the window, and the gutter grows from 1800 px up. A test keeps it that way.
- **A calmer look.** The sidebar is a notch dimmer than the page and its selected item is a quiet background. Lines between rows are faint hairlines, nothing sits in a bordered box inside another, icons are fewer and smaller, and the colored icon tiles are gone. Labels are sentence case, not small uppercase letters. Numbers in tables use tabular figures in the interface font; monospace is kept for commands, paths and logs.
- **Denser tables.** One line per row at 38 px: the name and its command share the line. The health column is only drawn when something has a health check. CPU and memory have a short bar (memory against `max_memory` when there is one). The row's restart, stop and menu buttons appear on hover and keyboard focus, and always on touch screens. A line under the table sums it up, and the header stays in place when the list scrolls. A process name is a real link, so it works from the keyboard and opens in a new tab.
- **A process page with room.** CPU and memory charts sit side by side across the page with an area under the line and a time axis, uptime, restarts and health in a row above them, then the details and the recent events as a timeline of dots. A process that never ran does not draw empty charts. The Configuration tab and Settings use two columns.
- **Loading and small screens.** Tables show rows with the shape of what is coming while they load. Below 720 px a row is two lines (name and command, then state and number) at 52 px, with a 40 px menu button.
- The design and the research behind it are in `docs/web-design.md`.

## v0.4.5

`procly monit`: scrolling the logs no longer lags, most of all over SSH.

- **Cause**: scrolling the logs moves every line, so every frame redraws about 33 rows, 7 KB. At the old 60 frames a second that is up to 400 KB/s, more than a link with some latency takes in at once, so the screen kept moving for seconds after you stopped. Measured by the bytes `monit` sends for one gesture: a trackpad flick (40 wheel events) 161 KB, a key held for a second 195 KB.
- `monit` now sends at most 30 frames a second, and 8 when it runs over SSH (`SSH_CONNECTION` or `SSH_TTY` is set). `PROCLY_FPS=1..60` overrides. The same gestures now send 22 KB and 62 KB.
- Scrolling speeds up the longer it goes on: one step stays one step (a line with the keys, three with the wheel), but steps in a row, less than 150 ms apart, grow up to eight times. A held key or a flick reaches its place in fewer frames, and still stops exactly at the oldest line.

## v0.4.4

`procly ls` in a terminal.

- A framed table with the version, the process count and the counts by status in the top edge. CPU and memory have bars: memory is measured against `max_memory` when the process has one (yellow from 70%, red from 90%), and against the biggest process otherwise. A group of instances is a tree (`▾ api ×3`, `├ api-1`, `└ api-3`) with the sums on its first line, restarts in yellow, and health as `✓ healthy` or `✗ unhealthy`.
- Under the table, what needs a look and what to run: `✗ billing gave up after 15 restarts (exit code 1)  procly logs billing`, an app failing its health check, and processes left over from a dead daemon.
- It fits the terminal: columns go in order of how little they matter (pid, memory bar, CPU bar, uptime, health, id, restarts), a long name is cut with `…` before cpu and memory go, and a line never wraps.
- Nothing changes for scripts: piped or redirected output, `NO_COLOR` and `--json` are exactly as before. The new table is only drawn on a terminal.

## v0.4.3

`procly monit`: scrolling up in the logs no longer breaks the screen.

- **The bug**: scroll up in a log to a startup banner with emoji (`🚀`, `👨‍💻`, `⏱️`) and the header, the footer and the frames scrolled away or were drawn twice. Terminals disagree about how many cells some emoji take (`⏱️` is 1 or 2, a joined `👨‍💻` is 2 or 4), so a line that procly had cut to the terminal's width was wider in the terminal, wrapped, and every redraw after that was off by a line. It only showed when the old lines came into view; the end of the log was fine.
- **The fix**: text from your apps (logs, command lines, error messages) is shown without emoji: each becomes a `·`, and one picture made of several (a family, a flag, a hand with a skin tone) becomes a single `·`. Plain symbols that every terminal draws one cell wide (`✓ ✗ → • ─ █`) are kept. Bells, backspaces and other control characters in a log line are dropped instead of being sent to the terminal. The apps' own logs are not changed, only what `monit` draws.
- Scrolling up stops when the oldest line is at the top of the pane (it used to go on until one line was left on an empty pane), counting wrapped lines as the rows they take.
- A process's stdout history is shown before its stderr history again, as in 0.4.0; 0.4.1 and 0.4.2 spread the few stderr lines through a long banner. A group's instances are still merged within each stream.
- In full-screen logs, `esc back` is the first hint in the footer, so a narrow terminal does not drop it.

## v0.4.2

Restarts that go one at a time now wait until each app serves again.

- `procly upgrade` and a group restart (`procly restart <group>`) used to move on as soon as an app's process was running. A server that needs a few seconds to boot was then still down when the next one went down too: upgrading three Node apps took all three down at the same time for about five seconds. Now, on Linux, procly notes the TCP ports the app's processes listen on before stopping it (from `/proc`, nothing to configure) and waits until the new process accepts connections on them before it touches the next app. `procly upgrade` says how long that took (`restarted api (serving on :3000 after 2.4s)`). Waits are capped: a minute per app for an upgrade, and for a group restart 30 seconds per instance and 60 in all. An app that listens on nothing, or one on macOS, is waited for as before (running, and healthy when it has a health check).

## v0.4.1

A new `procly monit`.

- **Layout**: panes with frames: processes, details and logs. Wide terminals put the table and the details side by side; narrow ones stack them, and table columns give way in order (pid, bars, uptime, health, restarts) so the name and status always fit. The header shows the machine's load and memory, and CPU on Linux.
- **Catches up with v0.4.0**: instances show as one group with a summary line (`▾ api ×3`, `2/3 online`), which `←` `→` or a second click fold and unfold; actions on the group's line act on all of them, and restart goes one at a time. The details say what a process is doing in words (restarting, attempt 3 of 15; gave up after 15 restarts), and show health, `max_memory` (with the memory graph and the table turning yellow and red near it), `reload_signal`, `restart_every`, `restart_at` and the last few events. `R` reloads.
- **Logs**: scroll back with `↑` `↓` (after `tab`), `pgup` `pgdn` or the mouse wheel; the pane pauses, counts new lines, and `f` follows again. `/` searches and shows only matching lines, highlighted. `e` switches between both streams, stderr only and stdout only, `w` wraps or cuts long lines. A group's logs are merged and each line names its instance. History from the two files (and from each instance) is interleaved instead of shown one file after the other, and escape codes and tabs from apps no longer break the layout.
- **Finding things**: `/` on the table filters processes by name, `o` sorts by id, name, CPU, memory or restarts.
- **Mouse**: click a row to select it, click the logs to focus them, scroll either with the wheel. Hold shift (option in iTerm2) to select text.
- Keys that arrive together (key repeat, a paste) now each do their thing; before, they were ignored.

## v0.4.0

Running apps for real: several copies of an app, memory limits, scheduled restarts, signals and reloads, `.env` files, an upgrade that restarts one app at a time, and a panel you can serve over HTTPS or put behind a proxy.

### Running apps

- `instances: N` (or `--instances N`): start several copies of an app, named `name-1` .. `name-N` (1 to 64), each with its own logs and `PROCLY_INSTANCE` (0-based) and `PROCLY_INSTANCES` in its environment. `stop`, `restart`, `reload` and `delete` with the base name act on the whole group, and a group restart goes one instance at a time. `ls` shows a group under a summary line; the panel's Processes page shows it as one row that opens, with a single confirmation that says how many processes are affected. `save` and `resurrect` keep groups; `ls --json` stays flat with `group`, `index` and `instances`. These are separate processes, not cluster mode; derive a port from `PROCLY_INSTANCE`. Names that are taken (or a group named like a process) are refused before anything starts.
- `max_memory`: restart a process that uses too much memory. `max_memory: 500MB` in `procly.yml` or `--max-memory 500MB` on `start`; the process and everything it started (its process group) are measured every five seconds. A restart is recorded as a `memory` event (shown in the panel's Events tab) and sends an alert. A leak found after 30 seconds or more is not a crash and does not count toward `max_restarts`; going over the limit within 30 seconds of starting counts like a quick crash, and procly gives up after `max_restarts` of them (`errored`, with a message to raise `max_memory`) instead of restarting every few seconds for ever. `describe` and the process page show the limit.
- Scheduled restarts: `restart_every: 24h` (from each start) and `restart_at: "04:00"` (every day, server local time), or `--restart-every` and `--restart-at`. Recorded as a `scheduled` event, they show in the restarts column but leave the give-up counter alone, and `save` and `resurrect` keep them. `restart_every` needs a unit and at least a minute; no cron expressions yet. The panel and `describe` show the schedule.
- `stop_signal` and `reload_signal`, and `procly reload <name|id|all>`. Stopping sends `stop_signal` (default `SIGTERM`) to the process group and `SIGKILL` after `kill_timeout_ms`, as before. `procly reload` sends `reload_signal` to the process, records a `reloaded` event, does not touch the restart counters, and fails clearly when none is set (`all` reloads those that have one). `--stop-signal` and `--reload-signal` on `start`; a Reload button in the panel; `describe` and `ls --json` show them.
- `env_file`: read environment variables from `.env` style files, in `procly.yml` (one path or a list; relative paths start from the config file) or with `procly start --env-file`. Read on every start, so edit the file and restart. Later files override earlier ones and `env` overrides them all. Syntax: `KEY=value`, quotes, `#` comments, an optional `export `; no expansion, no multi-line values. A missing file or a bad line stops the start and names the file and line. The panel and `describe` show the names, never the values.
- **Behaviour change:** managed processes now start with the default SIGHUP handling. The daemon used to ignore SIGHUP, and an ignored signal is inherited by every process it starts, so shells and other programs that keep what they inherit could not handle SIGHUP at all (which made `reload_signal: SIGHUP` do nothing for them). The daemon now catches SIGHUP, and so still survives a closing terminal.

### Upgrading procly

- `procly upgrade`: install the latest release and hand the daemon over, with as little interruption as there can be today. It downloads the release, checks it against `checksums.txt`, and replaces the binary (sudo when needed; the old one is kept until the end). The old daemon then exits **without stopping the apps**, a daemon from the new binary starts, and every running app is restarted once, **one at a time** (waiting for it to be up, or healthy; the instances of a group roll). Stopped apps stay stopped and the saved list is not changed. A new daemon cannot supervise apps it did not start (that is the "re-adopt" work), which is why each app restarts. If the new daemon does not start, the old binary is put back and the apps are restarted under it; an interrupted handover is finished by running `procly upgrade` again. `--check` only reports, `--dry-run` shows the plan, `--no-restart` only replaces the binary, `--version` installs a given one. A daemon run by systemd is not handed over (the binary is replaced and the command says to `sudo systemctl restart procly`), and Homebrew or npm installs are sent to `brew upgrade` or `npm update -g`. New: `POST /v1/handover` and `POST /v1/add` on the daemon, and `supervisor` in `ping`.

### The panel and the command line

- `procly web --tls-cert <file> --tls-key <file>`: the panel serves HTTPS itself (TLS 1.2 and newer, HTTP/2, `Secure` session cookie). A certificate that cannot be loaded stops it at start. Renewed certificates are picked up without a restart: the files are checked every ten seconds (a symlink moved to a new certificate counts), a half-written or broken pair is reported once and the previous certificate keeps being served, and `SIGHUP` loads them again at once (`procly reload panel` with `--reload-signal SIGHUP`).
- `procly web --trusted-proxy <address or range>` (repeatable, or comma separated): behind a reverse proxy the login lockout now counts against the real visitor, not the proxy's address, so one person's typos no longer lock everyone out. A connection from a trusted address is believed about `X-Forwarded-For` (the last address that is not itself a trusted proxy is used); the header from any other address is ignored, and nothing is trusted by default. The panel prints what it trusts and warns about `0.0.0.0/0`.
- `procly ls --json` and `procly describe <name> --json` print machine-readable output: the same objects the panel uses (stable field names, no colors, environment variable names but never values), also with `--server`. `ls --json` prints `[]` when no daemon is running.
- Secrets without a command-line argument: `procly alerts telegram`, `alerts slack`, `agent join` and `server add` also read the token or webhook from an environment variable (`PROCLY_TELEGRAM_TOKEN`, `PROCLY_SLACK_WEBHOOK`, `PROCLY_TOKEN`) or from standard input with `--token -` / `--webhook -`, so it stays out of `ps` and the shell history. The flags work as before.
- `procly start` error for a file it cannot run now names the file and a command to pass (`go run`, `java -jar`, or `<command>`), and says `chmod +x` makes it runnable directly. `.mjs`, `.cjs` and executable files already ran; they now have tests.

Upgrading: from 0.3.x there is no `procly upgrade` yet, so install this version (the install script, `brew upgrade procly` or `npm update -g procly`) and restart the daemon, which restarts every app once: `procly save && procly kill && procly resurrect`. From here on `procly upgrade` does it one app at a time. If something in your setup sends SIGHUP to your apps and relied on them ignoring it, give them `nohup`, or make SIGHUP their `reload_signal`: managed processes now get the default SIGHUP handling.

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
