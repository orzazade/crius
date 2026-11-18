#!/bin/sh
# Demo of crius-wakewatch restart --from-ready --dry-run on a made-up role demo:qa, in a temp CRIUS_HOME: nothing is sent
# or saved, and no real role, bus or template is read. Run from the repo root:
#   sh cmd/crius-wakewatch/testdata/from-ready-demo/demo.sh
# 1. READY-RESTART, a Crius ASK, the role's ANSWER, and its session resumed after the line (as a T3 thread is on each
#    line to it): the brief is built, step 6 holds the ASK and the ANSWER.
# 2. The same plus a DONE from the role: refused, naming the DONE.
# 3. No DONE, but a new session took the role after the line (a restart typed by hand): refused.
set -eu
tmp=$(mktemp -d)
# the disk runs near full: the temp folder (built binary and fixture) goes on exit, and only a mktemp folder
trap 'case "$tmp" in */tmp.*) rm -rf -- "$tmp" ;; esac' EXIT
GOTOOLCHAIN=local go build -o "$tmp/crius-wakewatch" ./cmd/crius-wakewatch
mkdir -p "$tmp/home/bus" "$tmp/home/roles"
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
record() { # session id, last start source, its time, when the id took the role (local wall times on 11-19)
	printf '{"session_id":"%s","project":"demo","role":"qa","event":"SessionStart","detail":"%s","time":"%s","started":"%s"}\n' \
		"$1" "$2" "$(date -j -f '%Y-%m-%d %H:%M' "2026-11-19 $3" +%Y-%m-%dT%H:%M:00%z | sed 's/\(..\)$/:\1/')" \
		"$(date -j -f '%Y-%m-%d %H:%M' "2026-11-19 $4" +%Y-%m-%dT%H:%M:00%z | sed 's/\(..\)$/:\1/')" > "$tmp/home/roles/demo.qa.json"
}
run() {
	code=0
	CRIUS_HOME="$tmp/home" "$tmp/crius-wakewatch" restart demo:qa --from-ready --dry-run || code=$?
	echo "exit $code"
}
record s-qa-1 resume 06:06 05:30
echo "== 1. an answer to Crius after READY-RESTART; the same session resumed at 06:06"
run
echo "== 2. then a DONE from the role"
cp "$tmp/home/bus/demo.log" "$tmp/bus.keep"
echo "2026-11-19T06:20 DONE demo:qa -> crius:lead — item 7 re-checked, PASS" >> "$tmp/home/bus/demo.log"
run
echo "== 3. no DONE, but a new session took the role at 06:10"
cp "$tmp/bus.keep" "$tmp/home/bus/demo.log"
record s-qa-2 clear 06:10 06:10
run
