#!/bin/bash
# Fallback wait detector: read the visible screen of every cmux terminal surface and log prompts that are
# waiting, in case the Notification hook (wait-log.sh) never fired. Read only: sends no keys to any surface.
# Lines use the strict Crius bus grammar (TYPE WAIT; the first word says what it is):
#
#   <YYYY-MM-DDTHH:MM> WAIT waits:<slug of tab title> -> crius — wait screen_sweep "<tab title>" surface=<ref> at=<HH:MM:SS> — <question>
#   <YYYY-MM-DDTHH:MM> WAIT waits:<slug|sweep> -> crius — error sweep_error "<tab title|->" surface=<ref|-> at=<HH:MM:SS> — <why>
#
# Only a live prompt matches, never prose that quotes one. Both checks look at the last 15 non-blank lines:
#  - a Claude Code choice prompt: a numbered menu starting at "1." with the "❯" cursor on one option, right
#    under a question ending in "?" (a "Do you want to"/"Would you like to" question wrapped over up to 3
#    lines is joined), with no idle input box ("❯" right under a "─" rule) below it;
#  - a dropped prompt: a line starting with "Prompt dropped by a hook" (Claude Code's own notice, e.g. from
#    the cache-tax hook) below the input box, leading space/box/bullet glyphs allowed, quotes not.
# Dedup: a surface + text is logged when it appears, then again every 30 min while it stays on screen; once
# it is gone the next one is new. State: ~/.crius/state/wait-sweep.seen; a lock stops two sweeps overlapping.
# Each cmux call is capped at 2s and, when it times out, tried once more with 5s.
# Tests: bin/test/wait-sweep/test-wait-sweep.sh (run by launchd/launchd_test.go); ~/.claude/hooks/tests/test-wait-bus-grammar.sh.
# Installed by bin/crius-install-launchd to ~/.crius/bin/wait-sweep.sh (job space.scifilab.crius.wait-sweep); ~/.claude/hooks/wait-sweep.sh links there.
# CRIUS_WAITS_LOG / CRIUS_SWEEP_STATE override the paths for tests only.
set -u
log=${CRIUS_WAITS_LOG:-$HOME/.crius/bus/waits.log}
state=${CRIUS_SWEEP_STATE:-$HOME/.crius/state/wait-sweep.seen}
umask 077
mkdir -p "$(dirname "$log")" "$(dirname "$state")" || exit 1
if [ "${WAIT_SWEEP_LOCKED:-}" != 1 ]; then   # one sweep at a time; a second one exits 75 with a message
  WAIT_SWEEP_LOCKED=1 exec /usr/bin/lockf -t 0 "$state.lock" /bin/bash "$0" "$@"
fi
epoch=$(date +%s)
now=$(date +%Y-%m-%dT%H:%M:%S)

# run_limited <secs> <cmd...>: run cmd in its own process group, kill the group after <secs> (exit 124);
# a child killed by a signal exits 128+signal, never 0.
run_limited() {
  perl -MTime::HiRes=alarm -e '$t=shift; $p=fork; exit 125 unless defined $p;
    if (!$p) { setpgrp(0,0); exec @ARGV; exit 127 }
    $SIG{ALRM}=sub { kill "KILL", -$p; exit 124 }; alarm $t; waitpid($p,0);
    exit($? & 127 ? 128 + ($? & 127) : $? >> 8)' "$@"
}
# slug: the bus role grammar, [a-z0-9][a-z0-9-]*
slug() {
  local s
  s=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | LC_ALL=C sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//')
  printf '%s' "${s:-unknown}"
}

# State lines: "<epoch first logged>\t<surface>\t<text>". $old = entries younger than 30 min; $next = this run's.
old="$state.old" next="$state.next"
: > "$old" && : > "$next" || exit 1
if [ -f "$state" ]; then awk -F'\t' -v cut=$((epoch - 1800)) '$1 > cut' "$state" > "$old" || exit 1; fi
wrote_fail=0
has() { cut -f2- "$1" | grep -qxF -- "$2"$'\t'"$3"; }   # has <file> <surface> <text>
carry() { R=$1 awk -F'\t' '$2 == ENVIRON["R"]' "$old" >> "$next"; }   # keep a surface's entries we could not re-check
# (values go to awk via ENVIRON: awk -v would turn a "\n" in a question into a newline)

# emit <title> <kind> <type> <surface> <text>
emit() {
  has "$next" "$4" "$5" && return
  if has "$old" "$4" "$5"; then
    R=$4 T=$5 awk -F'\t' '$2 == ENVIRON["R"] && $3 == ENVIRON["T"]' "$old" >> "$next"; return
  fi
  local role=sweep title
  title=$(printf '%s' "$1" | perl -CSD -ne 'print substr($_,0,40)')
  [ "$1" = - ] || role=$(slug "$title")
  if printf '%s WAIT waits:%s -> crius — %s %s "%s" surface=%s at=%s — %s\n' \
      "${now%:*}" "$role" "$2" "$3" "$title" "$4" "${now#*T}" "$5" >> "$log"; then
    printf '%s\t%s\t%s\n' "$epoch" "$4" "$5" >> "$next"
  else
    wrote_fail=1
  fi
}
# cmux_call <what> <args...>: run cmux capped at 2s; a busy cmux answers late now and then, so a call that times out
# is tried once more with 5s (Crius 10-30 20:20: three tabs timed out at 2s and read fine seconds later; live 20:25:
# cmux tree did the same). Sets out and rc, and why when it failed: a retry that fails too is still an error.
cmux_call() {
  local what=$1 retried=0; shift
  out=$(run_limited 2 cmux "$@" 2>&1); rc=$?
  if [ $rc = 124 ]; then out=$(run_limited 5 cmux "$@" 2>&1); rc=$?; retried=1; fi
  why=
  if [ $rc = 124 ]; then why="$what timed out after 2s, and again after 5s"
  elif [ $rc -ne 0 ]; then
    why="failed (rc=$rc): $(printf '%s' "$out" | head -1)"
    [ $retried = 1 ] && why="timed out after 2s, then $why"
    why="$what $why"
  fi
}

cmux_call "cmux tree" tree --all; tree=$out
if [ $rc -ne 0 ]; then
  cat "$old" >> "$next"
  emit - error sweep_error - "$why"
else
  surfaces=$(printf '%s\n' "$tree" | sed -nE 's/.* surface (surface:[0-9]+) \[terminal\] "(.*)"( \[| ◀| tty=|$).*/\1	\2/p')
  [ -n "$surfaces" ] || { cat "$old" >> "$next"; emit - error sweep_error - "no terminal surfaces parsed from cmux tree (format changed?): $(printf '%s' "$tree" | head -1)"; }
  while IFS=$'\t' read -r ref title; do
    [ -n "$ref" ] || continue
    cmux_call read-screen read-screen --surface "$ref"; screen=$out
    if [ $rc -ne 0 ]; then
      carry "$ref"
      emit "$title" error sweep_error "$ref" "$why"
      continue
    fi
    while IFS= read -r hit; do
      [ -n "$hit" ] && emit "$title" wait screen_sweep "$ref" "$hit"
    done < <(printf '%s\n' "$screen" | perl -CSD -Mutf8 -ne '
      chomp; s/\r//g; push @L, $_;
      sub clean { my $s = shift; $s =~ s/^[\s│]+//; $s =~ s/[\s│]+$//; return $s }
      END {
        @L = grep { !/^\s*$/ } @L;                 # blank lines carry no meaning here
        my $n = @L; my $from = $n > 15 ? $n - 15 : 0;
        my $idle = -1;                            # idle input box: "❯" (+ no-break space) under a "─" rule
        for my $i (1 .. $n - 1) { $idle = $i if $L[$i] =~ /^❯/ && $L[$i - 1] =~ /^─/ }
        my $opt = qr/^[\s│]*(?:❯\s*)?(\d+)\.\s/;
        for my $i ($from .. $n - 1) {
          my $t = $L[$i];
          next unless $i > $idle;
          if ($t =~ /^[\s│⎿⏺✗✘⚠•·*-]*(Prompt dropped by a hook.*)$/) { print clean($1), "\n"; next }
          next unless $i > 0 && $t =~ $opt && $1 == 1 && $L[$i - 1] !~ $opt;
          my $cursor = 0;                         # the "❯" cursor on an option, wrapped options allowed
          for my $j ($i .. ($n - 1 < $i + 10 ? $n - 1 : $i + 10)) {
            last if $j > $i && ($L[$j] =~ /^[\s│]*[─╌╭╰┌└]/ || $L[$j] =~ /^[\s│]*(?:❯\s*)?1\.\s/
                                || clean($L[$j]) =~ /\?$/);   # a rule, box border, new menu or new question ends it
            if ($L[$j] =~ /^[\s│]*❯\s*\d+\.\s/) { $cursor = 1; last }
          }
          next unless $cursor;
          my $q = clean($L[$i - 1]);
          next unless $q =~ /\?$/;
          for my $k (2 .. 3) {                    # join a wrapped "Do you want to ..." question
            last if $i - $k <= $idle || $i - $k < 0;
            if (clean($L[$i - $k]) =~ /^(?:Do you want to|Would you like to)\b/) {
              $q = join " ", map { clean($L[$_]) } ($i - $k .. $i - 1); last
            }
          }
          print "$q\n";
        }
      }')
  done <<<"$surfaces"
fi

mv -f "$next" "$state" || exit 1
[ $wrote_fail = 0 ] || { echo "wait-sweep: could not append to $log" >&2; exit 1; }
exit 0
