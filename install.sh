#!/bin/sh
# Installs procly from GitHub releases.
#
#   curl -fsSL https://raw.githubusercontent.com/itsahmadnyc/procly/main/install.sh | sh
#
# PROCLY_VERSION=0.1.0     install a specific version (default: latest)
# PROCLY_INSTALL_DIR=...   where to put the binary (default: /usr/local/bin,
#                          or ~/.local/bin when that isn't writable and sudo isn't available)
# PROCLY_DOWNLOAD_BASE=... fetch from a mirror instead of GitHub
set -eu

# Everything runs from main, called on the last line, so a download cut
# off halfway through `curl | sh` can't run half a script.
main() {
	repo="itsahmadnyc/procly"

	fail() {
		echo "procly install: $*" >&2
		exit 1
	}

	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported system $(uname -s); procly runs on Linux and macOS" ;;
	esac

	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "unsupported CPU $(uname -m); procly is built for amd64 and arm64" ;;
	esac

	if [ -n "${PROCLY_DOWNLOAD_BASE:-}" ]; then
		base=$PROCLY_DOWNLOAD_BASE # a mirror holding the same files as a release
	elif [ -n "${PROCLY_VERSION:-}" ]; then
		base="https://github.com/$repo/releases/download/v${PROCLY_VERSION#v}"
	else
		base="https://github.com/$repo/releases/latest/download"
	fi
	asset="procly_${os}_${arch}.tar.gz"

	if command -v curl >/dev/null 2>&1; then
		fetch() { curl -fsSL "$1" -o "$2"; }
	elif command -v wget >/dev/null 2>&1; then
		fetch() { wget -q "$1" -O "$2"; }
	else
		fail "needs curl or wget"
	fi

	if command -v sha256sum >/dev/null 2>&1; then
		sha256() { sha256sum "$1" | cut -d' ' -f1; }
	elif command -v shasum >/dev/null 2>&1; then
		sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
	else
		fail "needs sha256sum or shasum to check the download"
	fi

	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	echo "Downloading $asset"
	fetch "$base/$asset" "$tmp/$asset" || fail "download failed: $base/$asset"
	fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

	want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")
	got=$(sha256 "$tmp/$asset")
	[ -n "$want" ] || fail "$asset is not listed in checksums.txt"
	[ "$want" = "$got" ] || fail "checksum mismatch for $asset (expected $want, got $got)"

	tar -xzf "$tmp/$asset" -C "$tmp"
	bin="$tmp/procly_${os}_${arch}/procly"
	[ -x "$bin" ] || fail "archive did not contain the procly binary"

	dir=${PROCLY_INSTALL_DIR:-/usr/local/bin}
	sudo=""
	if [ -z "${PROCLY_INSTALL_DIR:-}" ] && [ "$(id -u)" -ne 0 ] && ! { [ -d "$dir" ] && [ -w "$dir" ]; }; then
		if command -v sudo >/dev/null 2>&1; then
			echo "Installing to $dir (needs sudo)"
			sudo="sudo"
		else
			dir="$HOME/.local/bin"
		fi
	fi
	$sudo mkdir -p "$dir"
	$sudo install -m 0755 "$bin" "$dir/procly"

	echo "Installed $("$dir/procly" version) to $dir/procly"
	case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "Add $dir to your PATH to run procly directly." ;;
	esac
	echo
	echo "Next: procly start app.js    (procly help for everything else)"
}

main "$@"
