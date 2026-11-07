#!/bin/bash
# ops-pins.sh: run on every wake before any click. Prints PIN-DRIFT for each script whose sha256 no longer matches what
# ops last read in full; rc 1 if any drift or /tmp path (PIN-TMP, PIN-TMP-REF), 2 if a file is missing. Update a pin here only after reading the new file whole.
B=$HOME/.crius/state/quotes/build
# Lead scripts (merge.sh, merge-ok.sh, postmerge-scan.sh, carry-chain.sh) were in a scratchpad wiped by the 11-07 reboot: pin them at their new path after reading them whole.
pins=(
  "$B/bin/pr-build.sh 6cc64f0f1c166ea1"
  "$B/bin/ado-suite.ts 996b3e432e6e0f4f"
  "$B/qa/tools/verdict-check.ts 1b2b46613f493590"
  "$B/qa/tools/pass-post.sh 78e4266433e9a6c7"
  "$B/qa/tools/review-check.sh 0d876bd2c06456bb"
  "$B/bin/lead/carry-chain.sh 264ab6328559848a"
  "$B/bin/pr-text-check.sh af97ffb72f996e08"
  "$B/bin/lead/merge.sh c4fffdbfe468b14c"
  "$B/bin/lead/merge-ok.sh bff227d8b7b4a5eb"
  "$B/bin/lead/postmerge-scan.sh 067a2374b8349eba"
  "$B/notes/platform-db-bin/need-qa.sh 164ac9cf67172e87"
)
rc=0
for p in "${pins[@]}"; do
  f=${p% *}; want=${p##* }
  # Crius 11-07 19:50: the reboot wiped the lead's scratchpad scripts. A pinned script under /tmp, or one that calls a
  # path under /tmp, breaks at the next reboot: RED (rc 1) before any other check.
  case "$f" in /tmp/*|/private/tmp/*) echo "PIN-TMP $f: under /tmp, a reboot wipes it; move it to a kept folder"; [ $rc -eq 2 ] || rc=1; continue;; esac
  [ -f "$f" ] || { echo "PIN-MISSING $f"; rc=2; continue; }
  t=$(/usr/bin/grep -aoE '(/private)?/tmp/[^ "'"'"')]+\.(sh|ts|py)' "$f" | sort -u | head -3 | tr '\n' ' ')
  [ -z "$t" ] || { echo "PIN-TMP-REF $(basename "$f") calls $t: a reboot wipes it"; [ $rc -eq 2 ] || rc=1; }
  got=$(shasum -a 256 "$f" | cut -c1-16)
  [ "$got" = "$want" ] || { echo "PIN-DRIFT $(basename "$f") now $got, read was $want ($(stat -f %Sm -t %H:%M "$f"))"; [ $rc -eq 2 ] || rc=1; }
done
[ $rc -eq 0 ] && echo "pins ok (${#pins[@]})"
exit $rc
