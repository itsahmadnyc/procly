# Contributing to procly

Thanks for looking. procly is small on purpose, and help is welcome: bug
reports, ideas, docs and code.

## Ways to help

- **Found a bug?** [Open an issue](https://github.com/itsahmadnyc/procly/issues/new/choose).
  The template asks for the version, your OS and the output; that is usually
  enough to reproduce it.
- **Have an idea or a question?** Start a
  [discussion](https://github.com/itsahmadnyc/procly/discussions) first. For
  anything bigger than a small fix, agreeing on the shape before you write code
  saves you work. Deploy from git and cluster mode are on the roadmap and
  deserve a conversation.
- **Want something to work on?** Look for
  [good first issues](https://github.com/itsahmadnyc/procly/labels/good%20first%20issue)
  and [help wanted](https://github.com/itsahmadnyc/procly/labels/help%20wanted).

## Setting up

You need Go (the version in `go.mod`; a newer `go` fetches it for you).

```sh
git clone https://github.com/itsahmadnyc/procly && cd procly
go build -o bin/procly ./cmd/procly
go test -race ./...
```

Use a throwaway home while you try things, so you don't touch your own
processes:

```sh
export PROCLY_HOME=$(mktemp -d)
./bin/procly start "sleep 600" --name test
./bin/procly web --port 9000
```

## Where things are

| Path | What is there |
|---|---|
| `cmd/procly` | `main` |
| `internal/cli` | commands, flags and output |
| `internal/daemon`, `internal/client` | the background daemon and its unix-socket API |
| `internal/proc` | running, restarting and watching processes, health checks, event history |
| `internal/tui` | `procly monit` (Bubble Tea) |
| `internal/web` | the web panel: server in Go, frontend in `assets/` |
| `internal/agent`, `internal/hub`, `internal/remote`, `internal/tlsid` | several servers: agents, NAT hub, pinned-certificate TLS |
| `internal/alerts` | Telegram and Slack messages |
| `internal/sysinfo` | load, memory, disk and uptime for the panel |
| `docs/brand` | logo, colors and type |

## The web panel

The frontend is plain HTML, CSS and JavaScript in `internal/web/assets`,
embedded into the binary with `go:embed`. There is no build step, no npm and no
framework, and that is deliberate. Edit a file, run `go build`, restart
`procly web`.

- Write every value that comes from the server with `textContent`, never
  `innerHTML`: process names and log lines are untrusted.
- Colors and spacing are tokens at the top of `style.css`, taken from
  `docs/brand`. Green means "running"; amber and red are for state only.
- Pages are redrawn from state every two seconds, so use the `fill()` helper in
  `app.js` instead of replacing nodes by hand. Its comment explains why.

## Before you open a pull request

```sh
gofmt -l .          # prints nothing when formatted
go vet ./...
go test -race ./...
```

- Tests start real processes, daemons and TLS connections. Process groups, `ps`
  and `/bin/sh` behave differently on Linux and macOS, so if you can, run the
  tests on both. On a Mac, a `golang` container run as a non-root user with
  `procps` installed covers Linux.
- Keep a pull request to one change, and add or update a test for it.
- If people will notice the change, add a line to `CHANGELOG.md` and update the
  README.
- Commit messages: a short subject in the imperative ("Add log search"), and a
  few lines on *why* when it isn't obvious.

## Releasing (maintainers)

1. Add a `## vX.Y.Z` section to `CHANGELOG.md` and merge it through a pull
   request. The release notes are that section, so a release with no section is
   refused.
2. Tag `main` and push the tag:

   ```sh
   git tag -a vX.Y.Z -m "procly vX.Y.Z" && git push origin vX.Y.Z
   ```

   The Release workflow runs the tests, builds the four archives and
   `checksums.txt` with `scripts/release.sh`, checks them, and publishes the
   GitHub release. A tag that is not on `main` is refused, and a tag with a
   hyphen (`v0.5.0-rc1`) is published as a pre-release.
3. Publish to npm from your own terminal, because it needs 2FA:
   `scripts/npm-release.sh X.Y.Z --publish`.
4. Update the Homebrew formula in
   [itsahmadnyc/homebrew-tap](https://github.com/itsahmadnyc/homebrew-tap): in a
   clone of it, run `scripts/brew-formula.sh X.Y.Z > Formula/procly.rb` (from this
   repository; it reads the checksums from the release), check it with
   `brew audit --strict --online itsahmadnyc/tap/procly` and push.

To try the whole thing without publishing, run the workflow by hand
(`gh workflow run release.yml`): it builds and checks everything and keeps the
files as a workflow artifact for a week. Release tags can't be moved or deleted
once pushed, so read the changelog before you tag.

By contributing you agree that your work is released under the [MIT license](LICENSE).
