#!/bin/sh
# stopped-demo.sh: bus-post posts a line to a stopped role, and says so on stderr.
# Run from anywhere: sh cmd/bus-post/testdata/stopped-demo.sh
# It uses a throwaway Crius home and bus log (never ~/.crius/bus). One DECISION goes to three roles: quotes:lead (last
# line a STOPPED), quotes:dev (silent since 11-01) and quotes:qa (posted just now). Exits 0 only when the line is posted,
# bus-post exits 0, and stderr warns about quotes:lead and quotes:dev and not about quotes:qa; else 1.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/bp-stopped.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
unset T3CODE_THREAD_ID

(cd "$root" && GOTOOLCHAIN=local go build -o "$tmp/bus-post" ./cmd/bus-post)

home=$tmp/home
mkdir -p "$home/bus" "$home/runs"
log=$home/bus/quotes.log
now=$(date +%Y-%m-%dT%H:%M)
cat >"$log" <<EOF
2026-11-01T10:00 DONE quotes:dev -> crius:lead — 8094 pushed
2026-11-11T01:25 STOPPED quotes:lead -> crius:lead — clean; bus waiter stopped.
$now INFO quotes:qa -> crius:lead — walking 8091
EOF

echo "--- bus before"
cat "$log"
echo "--- bus-post DECISION crius:lead -> quotes:lead,quotes:dev,quotes:qa"
set +e
CRIUS_HOME=$home "$tmp/bus-post" "$log" DECISION crius:lead quotes:lead,quotes:dev,quotes:qa "heavy slots 1 -> 2" 2>"$tmp/err"
rc=$?
set -e
echo "exit: $rc"
echo "--- stderr"
cat "$tmp/err"
echo "--- bus after"
cat "$log"

ok=1
[ "$rc" -eq 0 ] || { echo "demo: FAIL: exit $rc, want 0" >&2; ok=0; }
/usr/bin/grep -q 'DECISION crius:lead -> quotes:lead,quotes:dev,quotes:qa — heavy slots' "$log" ||
	{ echo "demo: FAIL: the line was not posted" >&2; ok=0; }
/usr/bin/grep -q 'quotes:lead is STOPPED (its last line, 2026-11-11T01:25' "$tmp/err" ||
	{ echo "demo: FAIL: no STOPPED warning for quotes:lead" >&2; ok=0; }
/usr/bin/grep -q 'quotes:dev has posted no line for [0-9]* h (its last line is 2026-11-01T10:00)' "$tmp/err" ||
	{ echo "demo: FAIL: no silence warning for quotes:dev" >&2; ok=0; }
if /usr/bin/grep -q 'quotes:qa' "$tmp/err"; then
	echo "demo: FAIL: quotes:qa posted just now, yet it is warned about" >&2
	ok=0
fi
[ "$ok" -eq 1 ] || exit 1
echo "demo: PASS"
