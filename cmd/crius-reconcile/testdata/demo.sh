#!/bin/sh
# demo.sh: a self-contained demo of crius-reconcile on a made-up board and a throwaway git repo.
# Run from anywhere: sh cmd/crius-reconcile/testdata/demo.sh
# It exits 0 only when the tool flags what it should (exit 4, STALE D1, UNLANDED D2, D3 clean), else 1.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d "${TMPDIR:-/tmp}/crius-reconcile-demo.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

(cd "$root" && GOTOOLCHAIN=local go build -o "$tmp/crius-reconcile" ./cmd/crius-reconcile)

# git with no user config and fixed dates, so the shas are the same on every run
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=demo GIT_AUTHOR_EMAIL=demo@example.com GIT_COMMITTER_NAME=demo GIT_COMMITTER_EMAIL=demo@example.com
n=0
commit() {
	n=$((n + 1))
	GIT_AUTHOR_DATE="2026-01-01T00:00:0${n}Z" GIT_COMMITTER_DATE="2026-01-01T00:00:0${n}Z" \
		git -C "$repo" commit -q --allow-empty -m "$1"
	git -C "$repo" rev-parse --short=8 HEAD
}

repo=$tmp/repo
git init -q "$repo"
git -C "$repo" checkout -q -b main
commit "init" >/dev/null
git -C "$repo" checkout -q -b side
side=$(commit "feat(D2): only on a side branch")
git -C "$repo" checkout -q main
commit "fix(D1): login keeps the session" >/dev/null # tagged fix for an open row: stale
commit "fix(D4): try a cache" >/dev/null
commit 'Revert "fix(D4): try a cache"' >/dev/null # reverted: D4 stays open, with a note
commit "feat(D3): export to csv (#7)" >/dev/null   # merge subject of PR 7, naming D3

case $side in
*[a-f]*) ;;
*) echo "demo: sha $side has no letter, a board would not read it as a sha" >&2; exit 1 ;;
esac

cat >"$tmp/board.md" <<EOF
# Demo board

## ROWS

- ⬜ D1 open: login keeps the session
- ✅ D2 done: side-branch work, $side
- ✅ D3 done: export to csv, PR 7
- ⬜ D4 open: cache, tried and reverted
EOF

echo "--- board"
cat "$tmp/board.md"
echo "--- crius-reconcile --board board.md --format md --section ROWS --git repo:main"
code=0
(cd "$tmp" && ./crius-reconcile --board board.md --format md --section ROWS --git repo:main) >"$tmp/out" 2>&1 || code=$?
cat "$tmp/out"
echo "--- exit code: $code"

ok=1
[ "$code" -eq 4 ] || ok=0
grep -q '^STALE D1 ' "$tmp/out" || ok=0
grep -q '^UNLANDED D2 ' "$tmp/out" || ok=0
grep -q ' D3 ' "$tmp/out" && ok=0
grep -q ' D4 ' "$tmp/out" && ok=0
if [ "$ok" -eq 1 ]; then
	echo "demo: as expected (D1 stale, D2 unlanded, D3 and D4 clean)"
else
	echo "demo: UNEXPECTED output, see above" >&2
	exit 1
fi
