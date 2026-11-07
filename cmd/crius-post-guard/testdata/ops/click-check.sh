#!/bin/bash
# click-check.sh <workspace> <surface>: one call per click. Runs ops-pins, reads the waiting prompt from the screen, prints
# the full command and the shape checks it matches. Output capped at 40 lines; the whole command is saved to
# ~/.crius/state/crius/ops-bin/cmd-<surface>.txt. Read-only: it never clicks. Kept here, not in a scratchpad (reboots wipe /tmp).
ws=$1; sf=$2; SP=$(cd "$(dirname "$0")" && pwd)
ORG=https://dev.azure.com/Appxite; PROJ="AppXite Platform"; B1=integration/96365-b1
BL=$HOME/.crius/state/quotes/build/BUILD-LOG.md; REPO=$HOME/Projects/Appxite/svc_quote/svc_quote
main() {
echo "PINS: $(~/.crius/state/crius/ops-pins.sh 2>&1 | cut -c1-110 | tr '\n' ' ')"
scr=$(cmux read-screen --workspace "$ws" --surface "$sf" --scrollback < /dev/null 2>&1 | tail -80)
printf '%s' "$scr" | tail -12 | /usr/bin/grep -aq 'Do you want to proceed\|❯ 1\. Yes' || echo "SCREEN: no prompt open now"
echo "GUARD: $(printf '%s' "$scr" | /usr/bin/grep -a -A3 'Hook PreToolUse' | tail -4 | tr -s ' \n│' ' ' | cut -c1-300)"
title=$(cmux tree --all < /dev/null 2>&1 | /usr/bin/grep -a "$sf " | /usr/bin/grep -ao '"[^"]*"' | head -1 | tr -d '"')
key="waits:$(echo "$title" | sed 's/ · /-/; s/ /-/g')"
sid=$(/usr/bin/grep -a "$key " ~/.crius/bus/waits.log | /usr/bin/grep -ao 'sid=[0-9a-f]*' | tail -1 | cut -d= -f2)
# The held tool_use reaches the transcript only after the click, so the screen box is the source; cwd from the transcript.
cmd=$(printf '%s\n' "$scr" | awk '/^ *(Bash command|Tool use)/{b=1;n=0;c="";next} b&&/Hook PreToolUse/{b=0;o=c} /╌/{next} b&&n++>0{sub(/^ *│ ?/,"");sub(/ +$/,"");c=c (c==""?"":" ") $0} END{print o}')
f=$(ls -t ~/.claude*/projects/*/"${sid:-none}"*.jsonl 2>/dev/null | head -1)
cwd=$([ -n "$f" ] && tail -c 200000 "$f" | /usr/bin/grep -ao '"cwd":"[^"]*"' | tail -1 | cut -d'"' -f4)
echo "TAB: $title sid=${sid:-?} cwd=${cwd:-?}"; [ -n "$cmd" ] || { echo "NO COMMAND BOX on screen: read the screen"; return; }
out="$SP/cmd-${sf#surface:}.txt"; printf '%s\n' "$cmd" > "$out"; n=$(printf '%s\n' "$cmd" | fold -w 230 | wc -l | tr -d ' ')
echo "--- CMD ($n lines; whole in $out)"; printf '%s\n' "$cmd" | fold -w 230 | head -18
[ "$n" -gt 18 ] && echo "--- CMD CUT at 18 of $n lines: read the file before approving"
c=$cmd; echo "--- SHAPES"
red() { printf '%s' "$c" | /usr/bin/grep -aqiE -- "$1" && echo "RED $2"; }
red 'push[^|;&]*\b(main|develop)\b|refs/heads/(main|develop)|--target-branch[= ]+(main|develop)' "main/develop: refuse"
red '--auto-complete( +true)?( |$)|autoCompleteSetBy[^,}"]*=' "auto-complete: refuse"; red 'git stash' "git stash: refuse"; red 'pkill -f' "pkill -f: refuse"
red 'co-authored|generated with|claude-session' "AI credit in command text"; red '\bprod|-dt\b|production' "names prod/DT: NEED crius:lead"
red 'rm -[a-z]*r[a-z]* +"?\$' "rm of a computed path: refuse"
for s in $(printf '%s' "$c" | /usr/bin/grep -aoE '[^ "'"'"']+\.(sh|ts|py)\b' | sort -u); do p=${s/#\~/$HOME}
  [ -f "$p" ] && ! /usr/bin/grep -aq "$(basename "$p")" ~/.crius/state/crius/ops-pins.sh && echo "UNPINNED $p $(shasum -a 256 "$p" | cut -c1-16) $(stat -f %Sm -t %H:%M "$p"): read it whole"; done
known=0; shp() { printf '%s' "$c" | /usr/bin/grep -aqE "$1" && { echo "SHAPE $2"; known=1; }; }
shp 'ado-suite\.ts report' "ado-suite report: read-only (POST is in run); pin ok -> approve"
shp 'ado-suite\.ts run' "ado-suite run: queues/posts tests; check PR+build args"
shp 'pr-build\.sh' "pr-build: no build queued/running on the current merge; rebuild only if lastMergeCommit moved"
shp 'build cancel' "cancel: build not on current lastMergeCommit and newer one queued, or b1 catch-up follows"
shp 'pass-post|vote' "QA vote/PASS: active, target b1, no auto-complete, BUILD-LOG PASS at head (or carry-over)"
shp 'merge\.sh' "lead merge: target b1, merge build succeeded, PASS at head"; shp 'MOK_SCAN=1|merge-ok\.sh' "merge-ok scan: read-only"
shp 'git[^|;&]* push' "push: own feature branch or b1 ff, 0 AI credit"; shp 'pr create' "PR create: own branch -> b1, read description"
shp 'review-check|verdict-check' "QA tool: pinned, args = PR and head"
pr() { az repos pr show --id "$1" --org $ORG --query "[status,targetRefName,lastMergeSourceCommit.commitId,lastMergeCommit.commitId,autoCompleteSetBy.displayName]" -o tsv 2>&1 | tr '\t\n' '  ' | cut -c1-200; echo; }
builds() { az pipelines build list --org $ORG --project "$PROJ" --branch "refs/pull/$1/merge" --top 50 --query "sort_by([], &queueTime)[-3:].[queueTime,id,status,result,sourceVersion]" -o tsv 2>&1 | cut -c1-120; }
pass() { /usr/bin/grep -a "PASS PR $1 @" "$BL" | tail -2 | cut -c1-110; }
prs=$(printf '%s' "$c" | /usr/bin/grep -aoE '\b7[0-9]{3}\b' | sort -u | head -3)
if printf '%s' "$c" | /usr/bin/grep -aqE 'build cancel'; then bid=$(printf '%s' "$c" | /usr/bin/grep -aoE 'build-id[= ]+[0-9]+' | /usr/bin/grep -oE '[0-9]+$')
  echo "cancel $bid: $(az pipelines build show --id "$bid" --org $ORG --project "$PROJ" --query "[sourceBranch,sourceVersion,status,queueTime]" -o tsv 2>&1 | tr '\t\n' '  ')"
  echo "b1 head time (local ref): $(git -C "$REPO" log -1 --format='%h %ci' origin/$B1 2>&1)"; prs=$(az pipelines build show --id "$bid" --org $ORG --project "$PROJ" --query sourceBranch -o tsv | /usr/bin/grep -oE '[0-9]+'); fi
for p in $prs; do echo "PR $p: $(pr $p)"; [ "$p" = 7778 ] && echo "RED 7778 auth fix: a merge needs codex P1=0 on its head"
  printf '%s' "$c" | /usr/bin/grep -aqE 'pr-build|build cancel|merge(-ok)?\.sh' && builds $p
  printf '%s' "$c" | /usr/bin/grep -aqE 'pass-post|vote|merge\.sh|verdict' && echo "PASS lines: $(pass $p | tr '\n' '|')"; done
if printf '%s' "$c" | /usr/bin/grep -aqE 'git[^|;&]* push'; then d=$(printf '%s' "$c" | /usr/bin/grep -aoE "(cd|git -C) +[^ ;&]+" | tail -1 | awk '{print $NF}'); d=${d/#\~/$HOME}; d=${d:-$cwd}
  br=$(git -C "$d" rev-parse --abbrev-ref HEAD 2>&1); echo "push from $d branch $br"
  echo "ff of origin/$br: $(git -C "$d" merge-base --is-ancestor "origin/$br" HEAD 2>&1 && echo yes || echo NO) · AI credit in b1..HEAD: $(git -C "$d" log --format=%B "origin/$B1..HEAD" 2>&1 | /usr/bin/grep -ciE 'co-authored|generated with|claude-session')"; fi
[ $known = 1 ] || echo "SHAPE none known: read the command whole; routine+safe -> approve and add the shape to BRIEF"
}
main 2>&1 | cut -c1-240 | head -40
