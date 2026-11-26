#!/usr/bin/env bash
# Compares procly with PM2 on this machine.
#
#   scripts/bench.sh [path/to/procly]
#
# Needs bash 5, node, npm and curl. Installs PM2 into a temporary folder (not
# globally) and uses throwaway PROCLY_HOME / PM2_HOME folders, so it doesn't
# touch an existing setup. Measures, for each tool:
#   - install size (procly binary vs PM2 package; Node itself listed apart)
#   - memory of the manager daemon with $APPS apps running
#   - time from the start command until a fresh app answers HTTP (cold daemon)
#   - average time of `ls`
# Each round runs $RUNS times and the median is reported.
set -euo pipefail

PROCLY=${1:-procly}
APPS=${APPS:-10}
RUNS=${RUNS:-3}
PM2_VERSION=${PM2_VERSION:-latest}

work=$(mktemp -d)
cleanup() {
	PROCLY_HOME=$work/procly-home "$PROCLY" kill >/dev/null 2>&1 || true
	[ -x "$work/pm2/node_modules/.bin/pm2" ] && PM2_HOME=$work/pm2-home "$work/pm2/node_modules/.bin/pm2" kill >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

now_ms() { local t=${EPOCHREALTIME/./}; echo $((t / 1000)); }
filesize() { stat -c %s "$1" 2>/dev/null || stat -f %z "$1"; }
rss_kb() { ps -o rss= -p "$1" | tr -d ' '; }
children_rss_kb() { # sum of RSS of a process's direct children
	local total=0 r
	for r in $(ps -o rss= --ppid "$1" 2>/dev/null || true); do total=$((total + r)); done
	echo "$total"
}
median() { printf '%s\n' "$@" | sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}'; }
wait_http() {
	local deadline=$(($(now_ms) + 15000))
	until curl -fs "http://127.0.0.1:$1/" >/dev/null 2>&1; do
		[ "$(now_ms)" -lt "$deadline" ] || { echo "app on port $1 never answered" >&2; exit 1; }
		sleep 0.005
	done
}

cat >"$work/app.js" <<'EOF'
require("http").createServer((req, res) => res.end("ok")).listen(+process.env.PORT, "127.0.0.1");
EOF
cd "$work"

echo "Installing PM2 ($PM2_VERSION) into $work/pm2 ..." >&2
npm install --silent --no-audit --no-fund --prefix "$work/pm2" "pm2@$PM2_VERSION" >/dev/null
PM2="$work/pm2/node_modules/.bin/pm2"
pm2_version=$("$PM2" --version 2>/dev/null | tail -1)

declare -a p_cold p_rss p_apps p_ls m_cold m_rss m_apps m_ls

ls_avg_ms() { # $@ = command
	local t0 t1 i
	t0=$(now_ms)
	for i in $(seq 20); do "$@" >/dev/null 2>&1; done
	t1=$(now_ms)
	echo $(((t1 - t0) / 20))
}

for run in $(seq "$RUNS"); do
	echo "run $run/$RUNS: procly" >&2
	export PROCLY_HOME=$work/procly-home
	base=$((20000 + run * 100))
	t0=$(now_ms)
	PORT=$base "$PROCLY" start app.js --name a0 -e PORT=$base >/dev/null 2>&1 &
	wait_http "$base"
	p_cold+=($(($(now_ms) - t0)))
	wait
	for i in $(seq 1 $((APPS - 1))); do
		"$PROCLY" start app.js --name "a$i" -e PORT=$((base + i)) >/dev/null 2>&1
	done
	for i in $(seq 0 $((APPS - 1))); do wait_http $((base + i)); done
	sleep 10
	pid=$(cat "$PROCLY_HOME/daemon.pid")
	p_rss+=($(rss_kb "$pid"))
	p_apps+=($(children_rss_kb "$pid"))
	p_ls+=($(ls_avg_ms "$PROCLY" ls))
	"$PROCLY" kill >/dev/null 2>&1
	rm -rf "$PROCLY_HOME"
	unset PROCLY_HOME

	echo "run $run/$RUNS: pm2" >&2
	export PM2_HOME=$work/pm2-home
	base=$((30000 + run * 100))
	t0=$(now_ms)
	PORT=$base "$PM2" start app.js --name a0 >/dev/null 2>&1 &
	wait_http "$base"
	m_cold+=($(($(now_ms) - t0)))
	wait
	for i in $(seq 1 $((APPS - 1))); do
		PORT=$((base + i)) "$PM2" start app.js --name "a$i" >/dev/null 2>&1
	done
	for i in $(seq 0 $((APPS - 1))); do wait_http $((base + i)); done
	sleep 10
	pid=$(cat "$PM2_HOME/pm2.pid")
	m_rss+=($(rss_kb "$pid"))
	m_apps+=($(children_rss_kb "$pid"))
	m_ls+=($(ls_avg_ms "$PM2" ls))
	"$PM2" kill >/dev/null 2>&1
	rm -rf "$PM2_HOME"
	unset PM2_HOME
done

mb() { awk -v k="$1" 'BEGIN { printf "%.1f MB", k / 1024 }'; }
procly_size=$(filesize "$(command -v "$PROCLY")")
pm2_size_kb=$(du -sk "$work/pm2/node_modules" | cut -f1)
node_size=$(filesize "$(command -v node)")

cat <<EOF

Machine: $(uname -sm), $(nproc 2>/dev/null || sysctl -n hw.ncpu) CPUs · node $(node --version) · PM2 $pm2_version · $("$PROCLY" version)
$APPS apps (a small Node HTTP server each), median of $RUNS runs.

| | procly | PM2 |
|---|---|---|
| Install size | $(mb $((procly_size / 1024))) (one binary, no runtime) | $(mb "$pm2_size_kb") in node_modules, plus Node ($(mb $((node_size / 1024)))) |
| Daemon memory, $APPS apps running | $(mb "$(median "${p_rss[@]}")") | $(mb "$(median "${m_rss[@]}")") |
| Memory of the $APPS apps themselves | $(mb "$(median "${p_apps[@]}")") | $(mb "$(median "${m_apps[@]}")") |
| Start to first HTTP response (cold daemon) | $(median "${p_cold[@]}") ms | $(median "${m_cold[@]}") ms |
| \`ls\` | $(median "${p_ls[@]}") ms | $(median "${m_ls[@]}") ms |
EOF
