#!/bin/sh
# Demo of crius-wakewatch restart --from-ready --dry-run on a made-up role demo:qa, in a temp CRIUS_HOME: nothing is sent
# or saved, and no real role, bus or template is read. Run from the repo root:
#   sh cmd/crius-wakewatch/testdata/from-ready-demo/demo.sh
# 1. READY-RESTART, a Crius ASK, the role's ANSWER: the brief is built, step 6 holds the ASK and the ANSWER.
# 2. The same plus a DONE from the role: refused, naming the DONE.
set -eu
tmp=$(mktemp -d)
GOTOOLCHAIN=local go build -o "$tmp/crius-wakewatch" ./cmd/crius-wakewatch
mkdir -p "$tmp/home/bus"
echo "demo mac $tmp/RESTART-{role}.md" > "$tmp/home/restart-briefs.txt"
cat > "$tmp/RESTART-qa.md" <<'T'
You are **qa** on the **demo** team.
5. Run one quick smoke check.
6. Then continue with: your 05:00 READY-RESTART line (notes=old.md, next=old item)
   Crius since then: nothing
7. Post START.
T
cat > "$tmp/home/bus/demo.log" <<'B'
2026-11-19T06:00 INFO demo:qa -> crius:lead — READY-RESTART notes=~/w/demo/NOTES.md next=item 7 re-check
2026-11-19T06:05 ASK crius:lead -> demo:qa — is the item 6 gate green on the new box?
2026-11-19T06:07 ANSWER demo:qa -> crius:lead — Re 06:05: green, run 4 (31/31)
B
echo "== 1. an answer to Crius after READY-RESTART"
code=0
CRIUS_HOME="$tmp/home" "$tmp/crius-wakewatch" restart demo:qa --from-ready --dry-run || code=$?
echo "exit $code"
echo "2026-11-19T06:20 DONE demo:qa -> crius:lead — item 7 re-checked, PASS" >> "$tmp/home/bus/demo.log"
echo "== 2. then a DONE from the role"
code=0
CRIUS_HOME="$tmp/home" "$tmp/crius-wakewatch" restart demo:qa --from-ready --dry-run || code=$?
echo "exit $code"
echo "(fixture left in $tmp)"
