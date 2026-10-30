#!/usr/bin/env bash
# Test data: the quotes rig's bus wrapper as it was on 10-30 (exec bus-post and set -u asked before ace0c14's review).
# The rig's way onto the bus: the sender is always quotes:rig. usage: say.sh TYPE "to list" "text"
# Refuses (nothing posted) a text whose leading 'Rig HH:MM' is more than 2 min off the clock, rc 6 (lead ORDER 2026-10-25 17:43:
# typed times ran ahead in several roles' lines). bus-post's own checks stay: 300 chars rc 2, a short hex id not a commit rc 3,
# START while another live run is open rc 4.
set -u -o pipefail
[ $# -eq 3 ] || { echo "usage: say.sh TYPE \"to list\" \"text\" (exactly 3 args)" >&2; exit 2; }
if [[ "$3" =~ ^Rig\ ([0-9]{1,2}):([0-9]{2}) ]]; then
  said=$((10#${BASH_REMATCH[1]} * 60 + 10#${BASH_REMATCH[2]}))
  now=$((10#$(date +%H) * 60 + 10#$(date +%M)))
  d=$((said > now ? said - now : now - said)); [ $d -le 720 ] || d=$((1440 - d))   # across midnight
  [ $d -le 2 ] || { echo "LOUD: the text says Rig ${BASH_REMATCH[1]}:${BASH_REMATCH[2]}, the clock says $(date +%H:%M) ($d min off); not posted" >&2; exit 6; }
fi
exec "$HOME/.crius/bin/bus-post" "$HOME/.crius/bus/quotes.log" "$1" quotes:rig "$2" "$3"
