#!/bin/bash
# Test data: the quotes lead's bus wrapper as it ran on 10-30 (crius-post-guard asked the owner for it, Crius 20:27).
# Lead's only way to post on the quotes bus. Sender is fixed to quotes:lead.
# Usage: say.sh TYPE "rcpt1,rcpt2" "text"
# rc 2: bad type, empty or over 300 chars. rc 3: an 8-12 char hex word that is not a commit (nor a UUID prefix in scripts).
# rc 4: START while someone's START of the last 60 min has no later DONE/CORRECTION from them. rc 5: START under 25 GB free.
main() {
  local LOG="$HOME/.crius/bus/quotes.log" REPO="$HOME/Projects/quotes/channels"
  local TYPE="$1" TO="$2" TEXT="$3"
  case " INFO ORDER READY ANSWER PASS DONE NEED FAIL LANDED ASK BLOCKED CORRECTED CORRECTION ACK GO START OWNER PAUSE NEED-OWNER WAIT " in
    *" $TYPE "*) ;;
    *) echo "say.sh: type '$TYPE' not in the list" >&2; return 2 ;;
  esac
  [[ -n "$TO" && -n "$TEXT" ]] || { echo "say.sh: empty recipients or text" >&2; return 2; }
  local n
  n=$(printf %s "$TEXT" | wc -m | tr -d ' ')
  (( n <= 300 )) || { echo "say.sh: text is $n chars, limit 300" >&2; return 2; }
  # rc 6: the header time ("Lead [to X] HH:MM") is more than 2 minutes off the clock (typed times drifted 10-23, 10-24, 10-25)
  local hdr='^Lead( to [^ ]+)? ([0-9]{1,2}):([0-9]{2})'
  if [[ "$TEXT" =~ $hdr ]]; then
    local said=$((10#${BASH_REMATCH[2]} * 60 + 10#${BASH_REMATCH[3]})) clock
    clock=$((10#$(date +%H) * 60 + 10#$(date +%M)))
    local d=$(( said - clock )); (( d < 0 )) && d=$(( -d )); (( d > 720 )) && d=$(( 1440 - d ))
    (( d <= 2 )) || { echo "say.sh: header time ${BASH_REMATCH[2]}:${BASH_REMATCH[3]} is ${d} min off the clock $(date +%H:%M)" >&2; return 6; }
  fi
  local w
  for w in $(printf %s "$TEXT" | /usr/bin/grep -o -E '(^|[^0-9A-Za-z])[0-9a-f]{8,12}([^0-9A-Za-z]|$)' | tr -cd '0-9a-f\n'); do
    [[ "$w" =~ ^[0-9]+$ ]] && continue
    git -C "$REPO" cat-file -e "${w}^{commit}" 2>/dev/null && continue
    git -C "$REPO" grep -q -i -E "$w-[0-9a-f]{4}-" main -- poc/ui-app/scripts 2>/dev/null && continue
    echo "say.sh: '$w' is not a commit here nor a UUID prefix in main's scripts" >&2; return 3
  done
  if [[ "$TYPE" == START ]]; then
    local free=${SAY_FREE_GB:-$(df -k / | awk 'NR==2{print int($4/1048576)}')}
    (( free >= 25 )) || { echo "say.sh: START refused, ${free} GB free < 25" >&2; return 5; }
    /usr/bin/lockf -k -t 30 "$HOME/.crius/state/quotes/live-start.lock" bash -c '
      since=$(date -v-60M +%Y-%m-%dT%H:%M)
      open=$(awk -v s="$since" "\$1>=s && (\$2==\"START\"||\$2==\"DONE\"||\$2==\"CORRECTION\") {o[\$3]=(\$2==\"START\")} END{for(k in o) if(o[k]) print k}" "$1")
      if [[ -n "$open" ]]; then echo "say.sh: START refused, open START from: $open" >&2; exit 4; fi
      "$HOME/.crius/bin/bus-post" "$1" START quotes:lead "$2" "$3"' _ "$LOG" "$TO" "$TEXT"
    return $?
  fi
  "$HOME/.crius/bin/bus-post" "$LOG" "$TYPE" quotes:lead "$TO" "$TEXT"
}
main "$@"; exit $?
