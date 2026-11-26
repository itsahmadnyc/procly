#!/bin/sh
# Builds the npm packages for a GitHub release and, with --publish, publishes them.
#
#   scripts/npm-release.sh 0.2.0            # build + pack into npm-dist/
#   scripts/npm-release.sh 0.2.0 --publish  # ...then npm publish (needs `npm login`)
#
# The binaries are downloaded from the GitHub release and checked against its
# checksums.txt, so npm ships exactly what the release ships.
set -eu

version=${1:?usage: scripts/npm-release.sh <version> [--publish]}
version=${version#v}
publish=${2:-}
repo="itsahmadnyc/procly"
cd "$(dirname "$0")/.."

out=npm-dist
rm -rf "$out"
mkdir -p "$out/download" "$out/tarballs"

base="https://github.com/$repo/releases/download/v$version"
echo "Downloading v$version from GitHub releases"
curl -fsSL "$base/checksums.txt" -o "$out/download/checksums.txt"

deps=""
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
	os=${target%/*}
	goarch=${target#*/}
	cpu=$goarch
	[ "$cpu" = amd64 ] && cpu=x64
	asset="procly_${os}_${goarch}.tar.gz"
	curl -fsSL "$base/$asset" -o "$out/download/$asset"
	want=$(awk -v f="$asset" '$2 == f { print $1 }' "$out/download/checksums.txt")
	got=$(shasum -a 256 "$out/download/$asset" | cut -d' ' -f1)
	[ -n "$want" ] && [ "$want" = "$got" ] || { echo "checksum mismatch for $asset" >&2; exit 1; }
	tar -xzf "$out/download/$asset" -C "$out/download"

	name="procly-$os-$cpu"
	dir="$out/$name"
	mkdir -p "$dir/bin"
	cp "$out/download/procly_${os}_${goarch}/procly" "$dir/bin/procly"
	chmod 0755 "$dir/bin/procly"
	cp LICENSE "$dir/"
	cat >"$dir/README.md" <<EOF
# $name

The procly binary for $os on $cpu. Install [procly](https://www.npmjs.com/package/procly) instead:

\`\`\`sh
npm install -g procly
\`\`\`
EOF
	cat >"$dir/package.json" <<EOF
{
  "name": "$name",
  "version": "$version",
  "description": "procly binary for $os $cpu. Install the procly package instead.",
  "homepage": "https://github.com/$repo#readme",
  "repository": { "type": "git", "url": "git+https://github.com/$repo.git" },
  "license": "MIT",
  "os": ["$os"],
  "cpu": ["$cpu"],
  "files": ["bin/procly", "README.md", "LICENSE"],
  "preferUnplugged": true
}
EOF
	deps="$deps\"$name\": \"$version\","
	echo "  $name"
done

main="$out/procly"
mkdir -p "$main/bin"
cp npm/procly/bin/procly.js "$main/bin/procly.js"
chmod 0755 "$main/bin/procly.js"
cp LICENSE "$main/"
# npm doesn't resolve relative links and images in a README; point them at GitHub.
sed -E -e "s|src=\"docs/|src=\"https://raw.githubusercontent.com/$repo/main/docs/|g" \
	-e "s|srcset=\"docs/|srcset=\"https://raw.githubusercontent.com/$repo/main/docs/|g" \
	-e "s#\]\(([A-Za-z][A-Za-z0-9_./-]*\.(sh|md|yml))\)#](https://github.com/$repo/blob/main/\1)#g" \
	-e "s#\]\(LICENSE\)#](https://github.com/$repo/blob/main/LICENSE)#g" \
	README.md >"$main/README.md"
VERSION=$version DEPS="{${deps%,}}" node -e '
const fs = require("fs");
const p = JSON.parse(fs.readFileSync("npm/procly/package.json", "utf8"));
p.version = process.env.VERSION;
p.optionalDependencies = JSON.parse(process.env.DEPS);
fs.writeFileSync(process.argv[1], JSON.stringify(p, null, 2) + "\n");
' "$main/package.json"
echo "  procly"

for dir in "$out"/procly-* "$main"; do
	(cd "$dir" && npm pack --silent --pack-destination ../tarballs >/dev/null)
done
echo
ls -lh "$out/tarballs"

if [ "$publish" = "--publish" ]; then
	echo
	echo "Publishing as $(npm whoami)"
	# Platform packages first, so procly's optional dependencies exist when it lands.
	for t in "$out"/tarballs/procly-*-*.tgz; do
		case "$t" in *"/procly-$version.tgz") continue ;; esac
		npm publish "$t" --access public
	done
	npm publish "$out/tarballs/procly-$version.tgz" --access public
	echo
	echo "Published procly $version: https://www.npmjs.com/package/procly"
fi
