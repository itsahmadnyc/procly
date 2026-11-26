#!/bin/sh
# Builds release archives for every supported platform into dist/.
#
#   scripts/release.sh 0.1.0
#
# Asset names carry no version, so install.sh can always fetch
# releases/latest/download/procly_<os>_<arch>.tar.gz.
set -eu

version=${1:?usage: scripts/release.sh <version, e.g. 0.1.0>}
version=${version#v}
cd "$(dirname "$0")/.."

if [ -n "$(git status --porcelain)" ]; then
	echo "working tree is not clean; commit first" >&2
	exit 1
fi

rm -rf dist
mkdir -p dist
ldflags="-s -w -X github.com/itsahmadnyc/procly/internal/version.Version=$version"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os=${target%/*}
	arch=${target#*/}
	name="procly_${os}_${arch}"
	stage="dist/$name"
	mkdir -p "$stage"
	GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "$ldflags" -o "$stage/procly" ./cmd/procly
	cp LICENSE README.md "$stage/"
	tar -C dist -czf "dist/$name.tar.gz" "$name"
	rm -rf "$stage"
	echo "built dist/$name.tar.gz"
done

(cd dist && shasum -a 256 ./*.tar.gz | sed 's| \./| |' > checksums.txt)
echo
cat dist/checksums.txt
