#!/bin/bash
# pass-post.sh [--check] <pr> <verdict.md> <msgfile> [src files...]: posts the DONE line only if every check passes.
# --check (Crius 11-06 11:26): run every check, list ALL faults, post nothing. Read-only: run it first, fix everything it
# lists, then run without --check. rc 0 = all pass (posted, or "check ok"); else the rc of the first fault (6..11).
# SENDER=<qa role> and TO=<builder>,LEADER (env, both required): must be a qa-* name in board/members.tsv; no default (a default posted as the wrong tester).
# and the line names "build <id> green" (B105). Does not post a FAIL/BUG line (post those by hand).
B=$HOME/.crius/state/quotes/build
CHECK=""; [ "${1:-}" = --check ] && { CHECK=1; shift; }
PR="$1"; V="$2"; M="$3"; shift 3
faults=(); rc=0
fault() { faults+=("rc $1: $2"); [ $rc -ne 0 ] || rc=$1; }
cut -f1 "$B/board/members.tsv" | /usr/bin/grep -qx -- "${SENDER:-}" && [[ $SENDER == qa-* ]] || fault 8 "SENDER='${SENDER:-}' is not a qa-* name in board/members.tsv"
# rc 10 (lead 11-05 20:50: lines "DONE qa-backend -> PASS PR ..." had no recipients, so the builder's NEED-QA never showed
# as answered): TO=<builder>,LEADER (env, required): member names from board/members.tsv, LEADER included.
IFS=', ' read -r -a to <<< "${TO:-}"
[[ " ${to[*]} " == *" LEADER "* ]] || fault 10 "TO='${TO:-}' must name the builder and LEADER, e.g. TO='backend-orders, LEADER'"
for t in "${to[@]}"; do [ "$t" = LEADER ] || cut -f1 "$B/board/members.tsv" | /usr/bin/grep -qx -- "$t" || fault 10 "TO name '$t' is not in board/members.tsv"; done
[ ${#to[@]} -ge 2 ] || fault 10 "TO='${TO:-}' names no builder besides LEADER"
srcs=(); for f in "$@"; do srcs+=(--src "$f"); done
bun "$B/qa/tools/verdict-check.ts" --pr "$PR" --verdict "$V" "${srcs[@]}" || fault 6 "verdict-check refused (its faults are listed above)"
if /usr/bin/grep -q -E "build [0-9]+ green" "$M"; then
  # rc 9 (11-05 19:3x, 7785 named branch build 126884): the named build must be this PR's merge build and green.
  bid=$(/usr/bin/grep -o -E "build [0-9]+ green" "$M" | head -1 | /usr/bin/grep -o -E "[0-9]+")
  if b=$(az pipelines runs show --org https://dev.azure.com/Appxite --project "AppXite Platform" --id "$bid" --query "[result, sourceBranch]" -o tsv | tr '\n' ' '); then
    [ "$b" = "succeeded refs/pull/$PR/merge " ] || fault 9 "build $bid is '$b', not a green refs/pull/$PR/merge build"
  else fault 9 "cannot read build $bid"; fi
else fault 7 "message has no 'build <id> green' (B105)"; fi
# rc 11 (lead 11-05 21:40: 7798 and earlier 7789 got PASS with no review record, so merge.sh refused them): a
# notes/*-evidence/*/deep-review.md must hold "Reviewed: <base>..<PR head>" (same rule as the lead's review-gate.sh).
if head=$(az repos pr show --org https://dev.azure.com/Appxite --id "$PR" --query lastMergeSourceCommit.commitId -o tsv); then
  find "$B/notes" -path '*-evidence/*' -name deep-review.md -exec /usr/bin/grep -h -o -E '^Reviewed: [0-9a-f]{7,40}\.\.[0-9a-f]{8,40}' {} + | sed 's/.*\.\.//' | while read -r s; do [[ $head == "$s"* ]] && echo hit; done | /usr/bin/grep -q hit || fault 11 "no deep-review.md has 'Reviewed: <base>..${head:0:8}' for PR $PR; the builder's codex review writes it"
else fault 11 "cannot read PR $PR head"; fi
if [ ${#faults[@]} -gt 0 ]; then
  echo "REFUSED: ${#faults[@]} fault(s)${CHECK:+ (check only, nothing posted)}:"; printf '  - %s\n' "${faults[@]}"; exit $rc
fi
[ -n "$CHECK" ] && { echo "check ok: every check passes; run again without --check to post"; exit 0; }
# lead 11-06 11:15 (7833: a pass-post line without the word PASS was refused by merge.sh): the script writes "PASS PR <n> @ <head8>".
printf '%s DONE %s -> %s — PASS PR %s @ %s: %s [pass-post]\n' "$(date +%FT%H:%M)" "$SENDER" "$(IFS=,; echo "${to[*]}" | sed 's/,/, /g')" "$PR" "${head:0:8}" "$(cat "$M")" >> "$B/BUILD-LOG.md"; echo posted
