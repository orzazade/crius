#!/bin/bash
# postmerge-scan.sh [target]: Crius DECISION 11-06 13:28. Right after each merge into the batch branch, check every open PR
# to it that has a QA PASS at its current head against the new branch head (merge-ok file rules, MOK_SCAN=1, no trial),
# so rebuilds are ordered at once instead of found at merge time.
# Prints one line per PR: "rebuild <pr> <reason>", "ok <pr> ...", or "skip <pr> <why>". rc 0 = scan ran (any result),
# rc 2 = lookup error. Never merges or posts.
O=https://dev.azure.com/Appxite
S=/private/tmp/claude-501/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/3584a7f4-fcb0-4260-9e8d-e7749735c202/scratchpad
TB=${1:-integration/96365-b1}
log=~/.crius/state/quotes/build/BUILD-LOG.md
prs=$(az repos pr list --org $O --project "AppXite Platform" --repository svc_quote --status active --target-branch "$TB" --query "[].[pullRequestId, lastMergeSourceCommit.commitId]" -o tsv) || { echo "LOUD postmerge-scan: cannot list PRs to $TB"; exit 2; }
[ -n "$prs" ] || { echo "postmerge-scan: no active PR to $TB"; exit 0; }
n=0
while read -r pr head; do
  [ -n "$pr" ] || continue
  qa=$(/usr/bin/grep -a -E "^[0-9T:-]+ DONE qa-[a-z0-9-]+ .*PR $pr\b.*${head:0:8}" "$log" | /usr/bin/grep -a -E "\bPASS\b" | /usr/bin/grep -a -v -E "\b(NOT|no) PASS\b" | /usr/bin/grep -a -F '[pass-post]' | tail -1)
  [ -n "$qa" ] || { echo "skip $pr no PASS at ${head:0:8}"; continue; }
  bid=$(echo "$qa" | /usr/bin/grep -o -i -E "build ([0-9.]+ \()?[0-9]+\)? green" | /usr/bin/grep -o -E "[0-9]+\)? green" | /usr/bin/grep -o -E "^[0-9]+" | tail -1)
  [ -n "$bid" ] || { echo "skip $pr PASS names no 'build <id> green'"; continue; }
  out=$(MOK_SCAN=1 TB="$TB" bash $HOME/.crius/state/quotes/build/bin/lead/merge-ok.sh "$pr" "$bid" 2>&1); rc=$?; n=$((n+1))
  case $rc in
    0) echo "ok $pr build $bid: $(echo "$out" | tail -1 | cut -c1-160)";;
    4) echo "rebuild $pr build $bid: $(echo "$out" | tail -1 | sed 's/^LOUD merge-ok: //' | cut -c1-200)";;
    *) echo "LOUD postmerge-scan: merge-ok rc $rc on PR $pr: $(echo "$out" | tail -1 | cut -c1-200)";;
  esac
done <<< "$prs"
echo "postmerge-scan: $n PR(s) with a PASS at head checked against $TB"
exit 0
