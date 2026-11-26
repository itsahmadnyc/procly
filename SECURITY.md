# Security

procly runs commands on your servers and holds the keys to several of them, so
security reports are taken seriously.

## Reporting a vulnerability

Please **don't open a public issue**. Use GitHub's private report instead:
[Security → Report a vulnerability](https://github.com/itsahmadnyc/procly/security/advisories/new).

Include what you found, how to reproduce it and the version
(`procly version`). You'll get an answer within a few days, and credit in the
release notes if you want it.

## What is in scope

- The web panel: login, sessions, CSRF, anything that lets someone without the
  password act on a server.
- Agents and the hub: the TLS pinning, the token checks, and anything that lets
  an agent or a network peer start commands or read files it shouldn't.
- The daemon's unix socket and the files under `~/.procly`.

## Trust model

procly runs commands for you, so a few things work as designed:

- **Signing in to the web panel is the same as having a shell** on the machine
  that runs it, as the user running procly: the panel can start any command
  (**New process**). Treat the panel password like a login, and put the panel
  behind HTTPS if it leaves localhost.
- **Whoever can use the daemon's unix socket** (`~/.procly`, owner-only) can do
  the same from the command line.
- **Agents are narrower.** An agent can list, restart, stop, start, delete and
  stream logs for processes that already exist. It cannot start a new command.
- **Working directories and health-check addresses are set by whoever sets up a
  process.** procly checks their shape (an absolute directory that exists;
  `http`, `https` or `tcp`), but it does not try to limit where a health check
  may point.

A report is a vulnerability when it lets someone **without** the panel password,
the socket or the agent token reach any of this, or when it lets an agent, a
log line or a process name do more than the above.

## Supported versions

The latest release. Fixes go into a new release rather than into old ones.

## How it is built

The design notes are in the README: [Several servers](README.md#several-servers)
and the security paragraph under [Web panel](README.md#web-panel).
