#!/bin/bash
# review-check.sh <pr>: builders run this before NEED-QA (lead 11-05 23:02
# Same test as pass-post.sh rc 11: some notes/*-evidence/*/deep-review.md must hold
# "Reviewed: <base>..<8+ hex of the PR head>". rc 0 = OK, 11 = no such line, 2 = cannot read the PR.
B=$HOME/.crius/state/quotes/build; PR="$1"
[[ $PR =~ ^[0-9]+$ ]] || { echo "usage: review-check.sh <pr>"; exit 2; }
head=$(az repos pr show --org https://dev.azure.com/example --id "$PR" --query lastMergeSourceCommit.commitId -o tsv) || { echo "LOUD: cannot read PR $PR"; exit 2; }
[ ${#head} -eq 40 ] || { echo "LOUD: PR $PR head '$head' is not a sha"; exit 2; }
hit=$(find "$B/notes" -path '*-evidence/*' -name deep-review.md -exec /usr/bin/grep -H -o -E '^Reviewed: [0-9a-f]{7,40}\.\.[0-9a-f]{8,40}' {} + | while IFS= read -r l; do s=${l##*..}; [[ $head == "$s"* ]] && echo "${l%%:Reviewed*}"; done | head -1)
[ -n "$hit" ] && { echo "review-check OK: PR $PR head ${head:0:8} reviewed in $hit"; exit 0; }
near=$(find "$B/notes" -path '*-evidence/*' -name '*.md' -exec /usr/bin/grep -l -E "^Reviewed: .*\.\.${head:0:7}" {} + | head -3)
echo "REFUSED review-check rc 11: PR $PR head ${head:0:8}: no deep-review.md line 'Reviewed: <base>..<8+ hex of ${head:0:8}>'.${near:+ Near misses (wrong file name or under 8 hex): $(echo $near)}"
exit 11
