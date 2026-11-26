#!/bin/sh
# Prints the GitHub release notes for a version: its section of CHANGELOG.md,
# without the "Upgrading:" paragraph (the install block below replaces it),
# followed by how to install and upgrade.
#
#   scripts/release-notes.sh 0.3.0
#   scripts/release-notes.sh latest     # the newest section, for dry runs
#
# Fails when CHANGELOG.md has no "## v<version>" heading, so a release can't go
# out with empty notes.
set -eu

version=${1:?usage: scripts/release-notes.sh <version|latest>}
version=${version#v}
cd "$(dirname "$0")/.."

if [ "$version" = latest ]; then
	version=$(awk '$1 == "##" && $2 ~ /^v[0-9]/ { sub(/^v/, "", $2); print $2; exit }' CHANGELOG.md)
fi

notes=$(awk -v want="v$version" '
	$1 == "##" && $2 == want { found = 1; next }
	found && $1 == "##"      { exit }
	found && /^Upgrading:/   { skip = 1 }
	found && skip && /^$/    { skip = 0; next }
	found && !skip           { lines[++n] = $0 }
	END {
		first = 1; while (first <= n && lines[first] == "") first++
		last = n;  while (last >= first && lines[last] == "") last--
		for (i = first; i <= last; i++) print lines[i]
	}
' CHANGELOG.md)

if [ -z "$notes" ]; then
	echo "CHANGELOG.md has no notes under \"## v$version\"" >&2
	exit 1
fi

printf '%s\n' "$notes"
cat <<'FOOTER'

### Install

```sh
curl -fsSL https://raw.githubusercontent.com/itsahmadnyc/procly/main/install.sh | sh
```

Upgrading: run the same command, then restart the daemon so it runs the new version. Your processes restart once. Update the agent on each connected server the same way.

```sh
procly save && procly kill && procly resurrect
```
FOOTER
