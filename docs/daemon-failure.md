# When the daemon stops

procly's daemon starts your apps and watches them. What happens to the apps if
the daemon itself goes away depends on *how* it goes away. This page says what
happens, what was not tested, and what to do.

Since procly 0.4 the apps a dead daemon leaves behind are found again and never
started twice. [Before that](#before-04) they were invisible and `resurrect`
duplicated them.

## At a glance

| What happens to the daemon | Your apps |
|---|---|
| `procly kill`, or `systemctl stop procly` (it runs `procly kill`) | **Stopped.** SIGTERM, then SIGKILL after the kill timeout (5 s by default). Nothing is left behind. |
| SIGTERM or SIGINT sent to the daemon | **Stopped**, the same way. |
| `kill -9`, a crash, or the out-of-memory killer | **Keep running, unsupervised.** The next daemon lists them as `orphaned`. See below. |
| The machine reboots | **All die.** With `procly startup`, the boot unit runs `procly daemon --resurrect`, which brings back the last saved list. |

## If the daemon is killed or crashes

The apps keep running; they are taken over by init. What procly does about it:

- **It remembers what it started.** While it runs, the daemon keeps
  `running.json` in the procly home: each process's name, pid, command,
  working directory and log paths, and a fingerprint of the process (its start
  time), so a pid that has been reused by something else is never mistaken for
  one of yours. It holds no environment values.
- **The next daemon finds the survivors.** Any command that needs a daemon
  (`ls`, `stop`, `delete`, `kill`, `describe`, `monit`, `resurrect`, the web
  panel) starts one if processes were left behind, and the survivors are listed
  with the status `orphaned` and their pid. A survivor that exits on its own
  drops off the list.
- **It never starts a second copy.** `procly resurrect` skips an app that is
  still running from before, and `procly start` of that name is refused with
  the pid and what to do. `procly restart` is refused too: an orphan's
  environment is not known, so it cannot be started again from what procly
  remembers.
- **You can stop them.** `procly stop web` (or `delete`, or `procly kill` for
  everything) ends the process group the way a normal stop does (SIGTERM, then
  SIGKILL after the kill timeout) and forgets it. Then `procly start` or
  `procly resurrect` brings it back properly supervised.
- **`procly save` does not save orphans**, for the same reason: it would lose
  their environment.

What an orphan does *not* get: it is not restarted if it crashes, its logs are
not rotated, it has no health check and sends no alerts. That is the point of
stopping and starting it again. The web panel and `procly monit` show it
(yellow, with an open ring) and offer Stop but not Restart.

To recover after a crash:

```sh
procly ls                 # shows what survived, as orphaned
procly stop web           # for each one you want back under procly
procly resurrect          # starts the saved apps that are not running
```

Or `procly kill` to stop everything and start from the saved list. Find out why
the daemon died: `~/.procly/daemon.log`, and `dmesg | grep -i oom` for the
out-of-memory killer.

## Before 0.4

On Linux with four apps, after `kill -9` on the daemon: the apps kept running,
`procly ls` said "Daemon is not running" and listed nothing, and `procly
resurrect` started a second copy of every saved app. A second Node server
crash-looped on `EADDRINUSE` while the first kept serving; two loops wrote to
the same log; for a queue worker, two copies means every job can be taken twice.
`procly stop` reached only the new copy. If you are on 0.3, look for survivors
with `ps -eo pid,ppid,args | grep <your app>` (a parent pid of 1 means it was
left behind) and stop them before you resurrect.

## Reboots

- Only the **last `procly save`** comes back. An app you started after saving is
  gone after a reboot, so run `procly save` after you add or remove apps.
- An app that was **stopped** when you saved starts again after the reboot.
  `resurrect` starts every saved entry.

## Restarting the daemon on purpose

```sh
procly save && procly kill && procly resurrect
```

Every app restarts once. This is also how to upgrade procly.

## Under systemd and launchd (not tested yet)

These are expectations from the unit files, not observations:

- **systemd.** The unit has `Restart=on-failure` and `RestartSec=2` and no
  `KillMode`, so systemd's default (`control-group`) applies. When the daemon
  exits abnormally, systemd should stop whatever is left of the unit, wait two
  seconds and start `daemon --resurrect`: the apps restart from the last save
  instead of becoming orphans, with a short outage. If a setup keeps the
  survivors alive, procly lists them as orphans instead of duplicating them. To
  be confirmed on a real server ([issue #3](https://github.com/itsahmadnyc/procly/issues/3)).
- **launchd (macOS).** The plist has `RunAtLoad` but no `KeepAlive`, so a daemon
  that dies is not restarted by launchd; the next procly command starts one,
  which finds the survivors. Apps run in their own process groups, which
  launchd's cleanup should leave alone. Not tested.

## How this was tested

The unit tests start real processes under one manager and recognise them from a
second, and the end-to-end tests run the real binary on Linux and macOS in CI:
`kill -9` on the daemon then `ls`, `resurrect`, `start`, `restart`, `stop` and
`kill`; SIGTERM leaving nothing behind; and `daemon --resurrect` (what systemd
runs after a crash) with the old apps still alive. The scenario was also run by
hand in a Debian 12 container.

Not covered: systemd and launchd, a real reboot, and the out-of-memory killer
itself. It sends SIGKILL, so the daemon's side matches `kill -9`, but it may kill
an app too.
