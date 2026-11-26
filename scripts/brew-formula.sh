#!/bin/sh
# Prints the Homebrew formula for a release, with the checksums read from the
# release's own checksums.txt, so the formula ships exactly what the release does.
#
#   scripts/brew-formula.sh 0.3.1 > Formula/procly.rb      # in a clone of itsahmadnyc/homebrew-tap
#   scripts/brew-formula.sh 0.3.1 path/to/checksums.txt    # use a local copy instead of downloading
set -eu

version=${1:?usage: scripts/brew-formula.sh <version> [checksums.txt]}
version=${version#v}
repo="itsahmadnyc/procly"
sums=${2:-}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
if [ -z "$sums" ]; then
	curl -fsSL "https://github.com/$repo/releases/download/v$version/checksums.txt" -o "$tmp/checksums.txt"
	sums=$tmp/checksums.txt
fi

sum() { # sum <os> <arch>
	s=$(awk -v f="procly_$1_$2.tar.gz" '$2 == f { print $1 }' "$sums")
	[ -n "$s" ] || { echo "no checksum for procly_$1_$2.tar.gz in $sums" >&2; exit 1; }
	printf '%s' "$s"
}
url() { printf 'https://github.com/%s/releases/download/v%s/procly_%s_%s.tar.gz' "$repo" "$version" "$1" "$2"; }

cat <<FORMULA
class Procly < Formula
  desc "Process manager with a web panel, health checks and alerts"
  homepage "https://github.com/$repo"
  license "MIT"

  on_macos do
    on_arm do
      url "$(url darwin arm64)"
      sha256 "$(sum darwin arm64)"
    end
    on_intel do
      url "$(url darwin amd64)"
      sha256 "$(sum darwin amd64)"
    end
  end

  on_linux do
    on_arm do
      url "$(url linux arm64)"
      sha256 "$(sum linux arm64)"
    end
    on_intel do
      url "$(url linux amd64)"
      sha256 "$(sum linux amd64)"
    end
  end

  def install
    bin.install "procly"
  end

  test do
    assert_match "procly #{version}", shell_output("#{bin}/procly version")
  end
end
FORMULA
