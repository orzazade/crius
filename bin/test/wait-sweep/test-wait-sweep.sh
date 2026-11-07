#!/bin/bash
# Tests for bin/wait-sweep.sh (reads every cmux terminal screen, logs waiting prompts).
# Uses a fake cmux on PATH, CRIUS_WAITS_LOG and CRIUS_SWEEP_STATE in a temp dir. Run: bash test-wait-sweep.sh
# fixtures/live-*.txt are real `cmux read-screen` captures from 2026-10-16 (paths made generic).
set -u
here=$(cd "$(dirname "$0")" && pwd)
sweep=${WAIT_SWEEP:-$here/../../wait-sweep.sh}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/test-wait-sweep.XXXXXX") || exit 1
trap 'case $tmp in */test-wait-sweep.??????) rm -rf -- "$tmp" ;; esac' EXIT
pass=0 fail=0
ts_re='^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2} WAIT '
ok()  { pass=$((pass+1)); echo "PASS $1"; }
bad() { fail=$((fail+1)); echo "FAIL $1"; printf '     got: %s\n' "${2:-<empty>}"; [ -n "${3:-}" ] && printf '    want: %s\n' "$3"; }

rule='────────────────────────────────────────'
nbsp=$'\xc2\xa0'   # the real idle prompt is "❯" + a no-break space
fx="$tmp/cmux"; mkdir -p "$fx"
cat > "$fx/tree.txt" <<'EOF'
window window:1 [current] ◀ active
├── workspace workspace:1 "Crius"
│   └── pane pane:1 [focused]
│       ├── surface surface:1 [terminal] "first-order · qa" tty=ttys001
│       ├── surface surface:2 [terminal] "crius" ◀ here tty=ttys002
│       ├── surface surface:3 [terminal] "quotes" [selected] tty=ttys003
│       ├── surface surface:4 [terminal] "broken tab" tty=ttys004
│       └── surface surface:6 [browser] "docs page"
├── workspace workspace:2 "AXI"
│   └── pane pane:2 [focused]
│       ├── surface surface:5 [terminal] "axi" [selected] tty=ttys005
│       └── surface surface:7 [terminal] "slow tab" tty=ttys007
└── workspace workspace:3 "More"
    └── pane pane:3 [focused]
        ├── surface surface:8 [terminal] "first-order · ms-flow" tty=ttys008
        ├── surface surface:9 [terminal] "crius (old, retiring)" tty=ttys009
        ├── surface surface:10 [terminal] "prose list" tty=ttys010
        ├── surface surface:11 [terminal] "busy" tty=ttys011
        ├── surface surface:12 [terminal] "quoted drop" tty=ttys012
        └── surface surface:13 [terminal] "late tab" tty=ttys013
EOF
# 1: a permission dialog at the bottom -> wait
cat > "$fx/surface_1.txt" <<EOF
⏺ Bash(git push origin main)
$rule
 Bash command

   git push origin main
   Push to remote

 Do you want to proceed?
 ❯ 1. Yes
   2. Yes, and don't ask again for git push commands
   3. No, and tell Claude what to do differently (esc)
EOF
# 2: the phrase only in conversation text, idle input box below -> nothing
cat > "$fx/surface_2.txt" <<EOF
  3. A backup sweep of all cmux tabs for "Do you want to proceed", in case a hook fails.
  ✅
$rule
❯$nbsp
$rule
  ⏵⏵ bypass permissions on (shift+tab to cycle)
EOF
# 3: Claude Code's own drop notice (cache-tax hook), at the bottom -> wait
cat > "$fx/surface_3.txt" <<EOF
  ✅
$rule
❯ resume the parity check
$rule
  Prompt dropped by a hook: cache-tax: cache is cold (71m idle). Send it again to pay it
EOF
# 4: read fails -> sweep_error
echo "Error: surface not found" > "$fx/surface_4.fail"
# 5: boxed dialog with a different question -> wait, box chars trimmed
cat > "$fx/surface_5.txt" <<EOF
╭──────────────────────────────╮
│ Edit file                    │
│ Do you want to make this edit to main.go? │
│ ❯ 1. Yes                     │
│   2. No                      │
╰──────────────────────────────╯
EOF
# 7: read hangs -> sweep_error, sweep still finishes fast
touch "$fx/surface_7.hang"
# 13: the first read times out, the retry reads a permission dialog (Crius 10-30 20:20: three tabs timed out at 2s
# and read fine seconds later)
touch "$fx/surface_13.slow"
cat > "$fx/surface_13.txt" <<EOF
 Do you want to proceed?
 ❯ 1. Yes
   2. No
EOF
# 8: the live ms-flow permission prompt -> wait
cp "$here/fixtures/live-permission-prompt.txt" "$fx/surface_8.txt"
# 9: the live false positive: prose quoting the phrase -> nothing
cp "$here/fixtures/live-prose-quote.txt" "$fx/surface_9.txt"
# 10: prose question followed by a numbered list, idle box below -> nothing
cat > "$fx/surface_10.txt" <<EOF
⏺ Two ways to go. Do you want to keep the old format?
  1. Keep it and teach Crius a second grammar.
  2. Switch to the bus grammar.
$rule
❯$nbsp
$rule
EOF
# 11: busy session streaming prose with the question, no menu under it -> nothing
cat > "$fx/surface_11.txt" <<EOF
⏺ The sweep matches "Do you want to proceed?" only with a menu.
  It also reads the footer.

✻ Working… (12s · esc to interrupt)
EOF
# 12: prose quoting the drop notice -> nothing
cat > "$fx/surface_12.txt" <<EOF
  After a quiet hour, look for "Prompt dropped by a hook" and resend once.
$rule
❯$nbsp
$rule
EOF

log="$tmp/waits.log"; state="$tmp/state/seen"
runsweep() {
  stdout=$(env PATH="$here/fake-cmux:$PATH" FAKE_CMUX_DIR="${FIX:-$fx}" CRIUS_WAITS_LOG="$log" \
    CRIUS_SWEEP_STATE="$state" bash "$sweep" 2>"$tmp/err"); rc=$?
}
# norm: drop the leading timestamp, write at=HH:MM:SS as at=AT
norm() { sed -E 's/^[^ ]+ //; s/ at=[0-9]{2}:[0-9]{2}:[0-9]{2} / at=AT /' "$log" 2>/dev/null; }
has() { # has <name> <line without timestamp, at=AT>
  if norm | grep -qxF -- "$2"; then ok "$1"; else bad "$1" "$(norm)" "$2"; fi
}
has_re() { # has_re <name> <ERE on the normalized line>
  if norm | grep -qE -- "$2"; then ok "$1"; else bad "$1" "$(norm)" "/$2/"; fi
}
none() { # none <name> <surface>
  if grep -q "surface=$2 " "$log" 2>/dev/null; then bad "$1" "$(grep "surface=$2 " "$log")"; else ok "$1"; fi
}
count() { grep -c '' "$log" 2>/dev/null || echo 0; }

runsweep
[ "$rc" = 0 ] && ok "exit 0" || bad "exit 0" "rc=$rc $(cat "$tmp/err")"
has permission_dialog 'WAIT waits:first-order-qa -> crius — wait screen_sweep "first-order · qa" surface=surface:1 at=AT — Do you want to proceed?'
has prompt_dropped 'WAIT waits:quotes -> crius — wait screen_sweep "quotes" surface=surface:3 at=AT — Prompt dropped by a hook: cache-tax: cache is cold (71m idle). Send it again to pay it'
has boxed_dialog 'WAIT waits:axi -> crius — wait screen_sweep "axi" surface=surface:5 at=AT — Do you want to make this edit to main.go?'
has live_permission_prompt 'WAIT waits:first-order-ms-flow -> crius — wait screen_sweep "first-order · ms-flow" surface=surface:8 at=AT — Do you want to proceed?'
has_re read_error '^WAIT waits:broken-tab -> crius — error sweep_error "broken tab" surface=surface:4 at=AT — read-screen failed \(rc=1\): Error: surface not found$'
has_re read_timeout '^WAIT waits:slow-tab -> crius — error sweep_error "slow tab" surface=surface:7 at=AT — read-screen timed out after 2s, and again after 5s$'
has late_read_retried 'WAIT waits:late-tab -> crius — wait screen_sweep "late tab" surface=surface:13 at=AT — Do you want to proceed?'
none_error() { if grep -q "sweep_error .*surface=$2 " "$log" 2>/dev/null; then bad "$1" "$(grep "surface=$2 " "$log")"; else ok "$1"; fi; }
none_error late_read_no_error surface:13
none prose_above_idle_box surface:2
none live_prose_quote surface:9
none prose_numbered_list surface:10
none prose_while_busy surface:11
none prose_quoted_drop surface:12
if grep -q 'surface:6' "$fx/calls.log" "$log" 2>/dev/null; then bad browser_skipped "$(grep 'surface:6' "$fx/calls.log" "$log")"; else ok browser_skipped; fi
n1=$(count); [ "$n1" = 7 ] && ok "seven lines" || bad "seven lines" "$n1 lines: $(cat "$log" 2>/dev/null)"
bad_ts=$(grep -vcE "$ts_re" "$log" 2>/dev/null); [ "${bad_ts:-1}" = 0 ] && ok timestamps || bad timestamps "$bad_ts lines without a bus timestamp"
[ -z "$stdout" ] && ok "quiet stdout" || bad "quiet stdout" "$stdout"

runsweep
n2=$(count); [ "$n1" -gt 0 ] && [ "$n2" = "$n1" ] && ok dedup_second_run || bad dedup_second_run "$n2 lines after rerun, had $n1: $(tail -n +$((n1+1)) "$log")"

# state older than 30 min no longer suppresses
old=$(( $(date +%s) - 1801 ))
sed -E "s/^[0-9]+/$old/" "$state" > "$tmp/state.old" && cat "$tmp/state.old" > "$state"
runsweep
n3=$(count); [ "$n1" -gt 0 ] && [ "$n3" = $((n1*2)) ] && ok dedup_expires || bad dedup_expires "$n3 lines, want $((n1*2))"

# a new question on the same surface is a new line
cat > "$fx/surface_1.txt" <<EOF
 Do you want to allow Claude to fetch this content?
   1. Yes
 ❯ 2. No
EOF
runsweep
has new_text_same_surface 'WAIT waits:first-order-qa -> crius — wait screen_sweep "first-order · qa" surface=surface:1 at=AT — Do you want to allow Claude to fetch this content?'

# cmux tree itself fails -> one sweep_error line, exit 0
fx2="$tmp/cmux2"; mkdir -p "$fx2"; echo "Error: socket not found" > "$fx2/tree.fail"
log="$tmp/waits2.log"; state="$tmp/state2/seen"
FIX="$fx2" runsweep
[ "$rc" = 0 ] && has_re tree_error '^WAIT waits:sweep -> crius — error sweep_error "-" surface=- at=AT — cmux tree failed \(rc=1\): Error: socket not found$' || bad tree_error "rc=$rc"

# cmux tree answers late: the retry reads the tabs, no error (live 10-30 20:25: "cmux tree timed out after 2s")
fx6="$tmp/cmux6"; mkdir -p "$fx6"; touch "$fx6/tree.slow"
printf '%s\n' '│       └── surface surface:31 [terminal] "late tree" tty=ttys031' > "$fx6/tree.txt"
printf '%s\n' ' Do you want to proceed?' ' ❯ 1. Yes' '   2. No' > "$fx6/surface_31.txt"
log="$tmp/waits6.log"; state="$tmp/state6/seen"
FIX="$fx6" runsweep
has late_tree_retried 'WAIT waits:late-tree -> crius — wait screen_sweep "late tree" surface=surface:31 at=AT — Do you want to proceed?'
if grep -q sweep_error "$log" 2>/dev/null; then bad late_tree_no_error "$(cat "$log")"; else ok late_tree_no_error; fi

# cmux tree answers but no terminal surface can be parsed (format changed) -> loud, not "nothing waiting"
fx3="$tmp/cmux3"; mkdir -p "$fx3"; echo '{"windows":[]}' > "$fx3/tree.txt"
log="$tmp/waits3.log"; state="$tmp/state3/seen"
FIX="$fx3" runsweep
has_re tree_unparsed '^WAIT waits:sweep -> crius — error sweep_error "-" surface=- at=AT — no terminal surfaces parsed from cmux tree'

# --- review fixes (2026-10-16) ---
fx4="$tmp/cmux4"; mkdir -p "$fx4"
cat > "$fx4/tree.txt" <<'EOF'
│       ├── surface surface:21 [terminal] "narrow pane" tty=ttys021
│       ├── surface surface:22 [terminal] "planner" tty=ttys022
│       ├── surface surface:23 [terminal] "asker" tty=ttys023
│       ├── surface surface:24 [terminal] "crashy" tty=ttys024
│       └── surface surface:25 [terminal] "repeat" tty=ttys025
EOF
# 21: a question wrapped over two lines in a narrow pane
cat > "$fx4/surface_21.txt" <<EOF
 Do you want to make this edit to
 internal/ingest/transcripts.go?
 ❯ 1. Yes
   2. Yes, allow all edits during this session
   3. No
EOF
# 22: plan-mode approval
cat > "$fx4/surface_22.txt" <<EOF
 Would you like to proceed?
 ❯ 1. Yes, and auto-accept edits
   2. Yes, and manually approve edits
   3. No, keep planning
EOF
# 23: a question menu (AskUserQuestion shape), cursor moved to option 2
cat > "$fx4/surface_23.txt" <<EOF
 Which matcher should the Notification hook use?
   1. No matcher (all types)
 ❯ 2. Only the wait types
   3. Type something.
EOF
touch "$fx4/surface_24.crash"
prompt25=" Do you want to proceed?
 ❯ 1. Yes
   2. No"
printf '%s\n' "$prompt25" > "$fx4/surface_25.txt"
log="$tmp/waits4.log"; state="$tmp/state4/seen"
FIX="$fx4" runsweep
has wrapped_question 'WAIT waits:narrow-pane -> crius — wait screen_sweep "narrow pane" surface=surface:21 at=AT — Do you want to make this edit to internal/ingest/transcripts.go?'
has plan_mode_prompt 'WAIT waits:planner -> crius — wait screen_sweep "planner" surface=surface:22 at=AT — Would you like to proceed?'
has question_menu 'WAIT waits:asker -> crius — wait screen_sweep "asker" surface=surface:23 at=AT — Which matcher should the Notification hook use?'
has_re crash_is_error '^WAIT waits:crashy -> crius — error sweep_error "crashy" surface=surface:24 at=AT — read-screen failed \(rc=139\)'
# the same prompt answered, then back again on the next sweep -> logged again (dedup only while on screen)
printf '%s\n' "$rule" "❯$nbsp" "$rule" > "$fx4/surface_25.txt"; FIX="$fx4" runsweep
printf '%s\n' "$prompt25" > "$fx4/surface_25.txt"; FIX="$fx4" runsweep
r=$(grep -c 'surface=surface:25 ' "$log"); [ "$r" = 2 ] && ok repeat_prompt_logged_again || bad repeat_prompt_logged_again "$r lines for surface:25" "2"
c=$(grep -c 'surface=surface:24 ' "$log"); [ "$c" = 1 ] && ok persistent_error_deduped || bad persistent_error_deduped "$c lines for surface:24" "1"

# two sweeps at once: the second exits 75 and nothing is logged twice
fx5="$tmp/cmux5"; mkdir -p "$fx5"
printf '%s\n' '│       ├── surface surface:31 [terminal] "slow" tty=ttys031' '│       └── surface surface:32 [terminal] "asks" tty=ttys032' > "$fx5/tree.txt"
touch "$fx5/surface_31.hang"; printf '%s\n' "$prompt25" > "$fx5/surface_32.txt"
log="$tmp/waits5.log"; state="$tmp/state5/seen"
env PATH="$here/fake-cmux:$PATH" FAKE_CMUX_DIR="$fx5" CRIUS_WAITS_LOG="$log" CRIUS_SWEEP_STATE="$state" bash "$sweep" 2>/dev/null &
first=$!
perl -MTime::HiRes=sleep -e 'sleep 0.5'
FIX="$fx5" runsweep; second_rc=$rc
wait $first
[ "$second_rc" = 75 ] && grep -q 'lockf' "$tmp/err" && ok overlap_refused || bad overlap_refused "rc=$second_rc $(cat "$tmp/err")" "rc=75 with a lockf message"
d=$(sed -E 's/^[^ ]+ //; s/ at=[^ ]+ / /' "$log" | sort | uniq -d | grep -c ''); [ "$d" = 0 ] && ok overlap_no_duplicates || bad overlap_no_duplicates "$(cat "$log")"

# log cannot be written -> exit 1, and the hit is not marked seen, so the next good run logs it
log="$tmp/not-a-file"; mkdir -p "$log"; state="$tmp/state6/seen"
FIX="$fx5" runsweep
[ "$rc" = 1 ] && ok write_fail_exit_1 || bad write_fail_exit_1 "rc=$rc"
log="$tmp/waits6.log"; FIX="$fx5" runsweep
has write_fail_not_marked_seen 'WAIT waits:asks -> crius — wait screen_sweep "asks" surface=surface:32 at=AT — Do you want to proceed?'

# --- final review fixes (2026-10-16) ---
fx7="$tmp/cmux7"; mkdir -p "$fx7"
printf '%s\n' '│       ├── surface surface:41 [terminal] "backslash" tty=ttys041' \
  '│       ├── surface surface:42 [terminal] "wrapped options" tty=ttys042' \
  '│       ├── surface surface:43 [terminal] "fenced drop" tty=ttys043' \
  '│       └── surface surface:44 [terminal] "prose then box" tty=ttys044' > "$fx7/tree.txt"
# 44: a prose question with a plain list, then a separate boxed prompt: only the box is a wait
cat > "$fx7/surface_44.txt" <<EOF
  Should we keep the old format?
  1. Keep it.
  2. Switch.
╭──────────────────────────────╮
│ Do you want to make this edit to main.go? │
│ ❯ 1. Yes                     │
│   2. No                      │
╰──────────────────────────────╯
EOF
cat > "$fx7/surface_41.txt" <<'EOF'
 Do you want to run printf 'a\nb'?
 ❯ 1. Yes
   2. No
EOF
cat > "$fx7/surface_42.txt" <<EOF
 Do you want to proceed?
   1. Yes
   2. Yes, and don't ask again for commands like this one in
      /tmp/a/very/long/project/path
 ❯ 3. No
EOF
cat > "$fx7/surface_43.txt" <<EOF
  For example the notice reads:
  Prompt dropped by a hook: example
$rule
❯$nbsp
$rule
EOF
log="$tmp/waits7.log"; state="$tmp/state7/seen"
FIX="$fx7" runsweep; FIX="$fx7" runsweep; FIX="$fx7" runsweep
b=$(grep -c 'surface=surface:41 ' "$log"); [ "$b" = 1 ] && ok backslash_dedup || bad backslash_dedup "$b lines after 3 sweeps" "1"
has wrapped_options 'WAIT waits:wrapped-options -> crius — wait screen_sweep "wrapped options" surface=surface:42 at=AT — Do you want to proceed?'
none drop_example_above_input_box surface:43
has box_after_prose 'WAIT waits:prose-then-box -> crius — wait screen_sweep "prose then box" surface=surface:44 at=AT — Do you want to make this edit to main.go?'
p=$(grep -c 'surface=surface:44 ' "$log"); [ "$p" = 1 ] && ok prose_list_not_borrowing_cursor || bad prose_list_not_borrowing_cursor "$(grep 'surface:44' "$log")" "1 line, the box only"
# tree output unparseable for one run: existing entries survive, so recovery does not re-log them
printf '%s\n' '{"windows":[]}' > "$tmp/tree.bad"; cp "$fx7/tree.txt" "$tmp/tree.good"
cat "$tmp/tree.bad" > "$fx7/tree.txt"; FIX="$fx7" runsweep
cat "$tmp/tree.good" > "$fx7/tree.txt"; FIX="$fx7" runsweep
b=$(grep -c 'surface=surface:41 ' "$log"); [ "$b" = 1 ] && ok tree_unparsed_keeps_state || bad tree_unparsed_keeps_state "$b lines for surface:41" "1"

echo "wait-sweep: $pass passed, $fail failed"
[ "$fail" = 0 ]
