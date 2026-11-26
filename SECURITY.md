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

## Supported versions

The latest release. Fixes go into a new release rather than into old ones.

## How it is built

The design notes are in the README: [Several servers](README.md#several-servers)
and the security paragraph under [Web panel](README.md#web-panel).
