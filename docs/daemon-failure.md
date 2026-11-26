# When the daemon stops

procly's daemon starts your apps and watches them. What happens to the apps if
the daemon itself goes away depends on *how* it goes away. This page says what
was observed, what was not tested, and what to do.

It describes procly 0.3.x. Making crash recovery safer is tracked in
[issue #11](https://github.com/itsahmadnyc/procly/issues/11).

## At a glance

| What happens to the daemon | Your apps |
|---|---|
| `procly kill`, or `systemctl stop procly` (it runs `procly kill`) | **Stopped.** SIGTERM, then SIGKILL after the kill timeout (5 s by default). The pid file and socket are removed. |
| SIGTERM or SIGINT sent to the daemon | **Stopped**, the same way. |
| `kill -9`, a crash, or the out-of-memory killer | **Keep running, with nobody watching them.** See below. |
| The machine reboots | **All die.** With `procly startup`, the boot unit runs `procly daemon --resurrect`, which brings back the last saved list. |

## If the daemon is killed or crashes

Observed on Linux with four apps (two shell loops writing a log line a second,
one app that crashes every three seconds, and a Node HTTP server on port 4555):

- **The apps keep running**, taken over by init. The one that was crash-looping
  stays dead after its next crash: nothing restarts it.
- **Logs keep being written**, because the apps hold the log files open. Nothing
  rotates them until a daemon runs again.
- **`procly ls` says "Daemon is not running" and lists nothing.** The surviving
  apps are invisible to procly. (`procly ls` does not start a daemon; `procly
  start` and `procly resurrect` do.)
- The dead daemon leaves `daemon.pid` and `procly.sock` behind. The next daemon
  removes them; they do not get in the way.
- **`procly resurrect` starts a second copy of every saved app.** It skips only
  the apps the *new* daemon already knows, and a fresh daemon knows none. We saw
  two copies of each loop writing to the same log, and a second Node server
  crash-looping on `EADDRINUSE` while the original kept serving. For a queue
  worker, two copies means every job can be taken twice.
- **The old copies are not managed.** `procly stop web` stops only the new one.

What to do if it happens:

1. Look for survivors before you restart anything: `ps -eo pid,ppid,args | grep
   <your app>`. A parent pid of 1 means it was left behind.
2. Stop them (`kill <pid>`), then run `procly resurrect` or `procly start`.
3. Find out why the daemon died: `~/.procly/daemon.log`, and `dmesg | grep -i
   oom` for the out-of-memory killer.

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
  instead of becoming orphans, with a short outage. To be confirmed on a real
  server ([issue #3](https://github.com/itsahmadnyc/procly/issues/3)).
- **launchd (macOS).** The plist has `RunAtLoad` but no `KeepAlive`, so a daemon
  that dies is not restarted. Apps run in their own process groups, which
  launchd's cleanup should leave alone, so they would survive as they do on
  Linux. Not tested.

## How this was tested

A Debian 12 container (arm64, with an init process) running a procly 0.3.1
development build. The scenarios: `kill -9` with a saved list then `ls` then
`resurrect`; SIGTERM to the daemon; `procly kill`; and a container restart as a
stand-in for a reboot, followed by `daemon --resurrect`.

Not covered: systemd and launchd, a real reboot, and the out-of-memory killer
itself. It sends SIGKILL, so the daemon's side matches `kill -9`, but it may kill
an app too.
