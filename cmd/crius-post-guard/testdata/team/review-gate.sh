#!/bin/bash
# review-gate.sh <PR id>: B13 review check. Passes only when a notes/*-evidence/*/deep-review.md
# has a line "Reviewed: <base>..<sha>" whose end sha is the PR head (8+ hex, prefix match).
# rc 0 = record found (prints its path), rc 3 = no record names the head, rc 2 = usage or lookup error,
# rc 6 = the QA PASS line lacks a green PR build at the head (B105); rc 4 = the record names a date-time later than its own save time; rc 5 = a frontend record lacks the UI-outside-the-mockup line or its design note; rc 7 = an active PR still targets this PR's source branch. rc 8 = the PR text names no AC id. rc 9 = it carries another open PR's head not yet in develop.
pr="$1"
[ -n "$pr" ] || { echo "usage: review-gate.sh <PR id>"; exit 2; }
head=$(az repos pr show --org https://dev.azure.com/Appxite --id "$pr" --query lastMergeSourceCommit.commitId -o tsv) || { echo "LOUD review-gate: cannot read PR $pr"; exit 2; }
[ ${#head} -eq 40 ] || { echo "LOUD review-gate: PR $pr head '$head' is not a sha"; exit 2; }
# rc 7 (11-05, 7741 left on 7733's deleted branch): an active PR still targets this PR's source branch; retarget it to develop first.
src=$(az repos pr show --org https://dev.azure.com/Appxite --id "$pr" --query sourceRefName -o tsv) || { echo "LOUD review-gate: cannot read source branch of PR $pr"; exit 2; }
deps=$(az repos pr list --org https://dev.azure.com/Appxite --project "AppXite Platform" --repository svc_quote --status active --target-branch "$src" --query "[].pullRequestId" -o tsv) || { echo "LOUD review-gate: cannot list PRs that target $src"; exit 2; }
[ -z "$deps" ] || { echo "LOUD review-gate: PR(s) $(echo $deps) target $src; retarget them to develop before completing PR $pr"; exit 7; }
# rc 8 (Crius audit 11-05 18:14: 85 of 97 merged PRs named no AC): the PR title or description must name an AC id (AC<n>) of Feature 93825.
txt=$(az repos pr show --org https://dev.azure.com/Appxite --id "$pr" --query "join(' ', [title, description || ''])" -o tsv) || { echo "LOUD review-gate: cannot read text of PR $pr"; exit 2; }
[[ $txt =~ (^|[^A-Za-z0-9])AC[0-9]{1,3}([^0-9]|$) ]] || [[ ${NO_AC_REASON:-} == platform:* || ${NO_AC_REASON:-} == owner:* ]] || { echo "LOUD review-gate: PR $pr names no AC id (AC<n>) in title or description; platform PRs pass with NO_AC_REASON='platform: <why>', owner-ruling PRs with NO_AC_REASON='owner: <ruling>'"; exit 8; }
# rc 9 (11-05 18:16: 7754 carried 7746's commits into develop, so 7746 shipped without its own AC review): refuse while this PR's head
# contains the head of another active PR that is not in develop yet. Merge that PR first, or rebuild this one without its commits.
G=~/Projects/Appxite/svc_quote/wt/platform
tok=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv) || { echo "LOUD review-gate: no ADO token"; exit 2; }
git -C $G -c http.extraHeader="Authorization: Bearer $tok" fetch -q origin develop "$src" || { echo "LOUD review-gate: fetch develop/$src failed"; exit 2; }
others=$(az repos pr list --org https://dev.azure.com/Appxite --project "AppXite Platform" --repository svc_quote --status active --target-branch develop --query "[?pullRequestId!=\`$pr\`].[pullRequestId, sourceRefName, lastMergeSourceCommit.commitId]" -o tsv) || { echo "LOUD review-gate: cannot list active PRs"; exit 2; }
carried=""
while read -r opr osrc ohead; do
  [ -n "$opr" ] || continue
  git -C $G cat-file -e "$ohead^{commit}" 2>/dev/null || git -C $G -c http.extraHeader="Authorization: Bearer $tok" fetch -q origin "$osrc" || { echo "LOUD review-gate: fetch $osrc of PR $opr failed"; exit 2; }
  git -C $G merge-base --is-ancestor "$ohead" origin/develop && continue
  git -C $G merge-base --is-ancestor "$ohead" "$head" && carried="$carried $opr"
done <<< "$others"
[ -z "$carried" ] || { echo "LOUD review-gate: PR $pr carries the head of open PR(s)$carried that is not in develop; merge those first"; exit 9; }
notes=${REVIEW_NOTES:-~/.crius/state/quotes/build/notes}
found=""
while IFS= read -r f; do
  while IFS= read -r end; do
    [ ${#end} -ge 8 ] && [ "${head#"$end"}" != "$head" ] && found="$f"
  done < <(/usr/bin/grep -o -E '^Reviewed: [0-9a-f]{7,40}\.\.[0-9a-f]{8,40}' "$f" | sed 's/.*\.\.//')
done < <(find "$notes" -path '*-evidence/*' -name deep-review.md)
if [ -n "$found" ]; then
  # A record may not name a time later than its own save time (typed times ran ahead 11-03 07:39).
  # lead 11-06 12:1x: a time with a zone ("2026-11-06 00:16 +0200", as review-pass.sh writes it) is compared in epoch
  # seconds; one without a zone is read in the machine's zone. A value with seconds or Z (a quoted test value such as
  # 2026-11-09T23:59:59Z, 7842) is data, not a review time, and is skipped.
  saved=$(stat -f '%Sm' -t '%Y-%m-%d %H:%M %z' "$found"); se=$(( $(stat -f %m "$found") / 60 * 60 ))
  late=$(/usr/bin/grep -o -E '20[0-9]{2}-[0-9]{2}-[0-9]{2}[ T]~?[0-2][0-9]:[0-5][0-9](:[0-5][0-9])?Z?( [+-][0-9]{4})?' "$found" | tr -d '~' | tr 'T' ' ' | while read -r d tm z; do
    [[ $tm =~ ^[0-2][0-9]:[0-5][0-9]$ ]] || continue
    if [ -n "$z" ]; then te=$(date -j -f '%Y-%m-%d %H:%M:%S %z' "$d $tm:00 $z" +%s 2>/dev/null); else te=$(date -j -f '%Y-%m-%d %H:%M:%S' "$d $tm:00" +%s 2>/dev/null); fi
    [ -n "$te" ] || { echo "$d $tm${z:+ $z}(unreadable)"; continue; }
    [ "$te" -gt "$se" ] && echo "$d $tm${z:+ $z}"
  done | head -3)
  if [ -n "$late" ]; then echo "LOUD review-gate: $found names $(echo $late) after its save time $saved: take times from date"; exit 4; fi
  guess=$(/usr/bin/grep -o -E "~ ?[0-2][0-9]:[0-5][0-9]" "$found" | head -3)
  if [ -n "$guess" ]; then echo "LOUD review-gate: $found names guessed times $(echo $guess): take them from the review output file stamp"; exit 4; fi
  # Owner 11-03 11:21: UI the mockup lacks (or a changed mockup part) is designed with /frontend-design and checked with /ux-review.
  case "$found" in */frontend-evidence/*)
    ui=$(/usr/bin/grep -m1 -E '^UI outside the mockup: ' "$found" | sed 's/^UI outside the mockup: //')
    [ -n "$ui" ] || { echo "LOUD review-gate: $found has no 'UI outside the mockup: none|<design note>' line (TEAM.md UI rule)"; exit 5; }
    # lead 11-06 00:12 (7805): "none (tests only, no UI change)" is a none with its reason, not a file name.
    if [ "$ui" != none ] && [[ ! $ui =~ ^none\ \( ]]; then
      note=${ui/#\~/$HOME}
      [ -f "$note" ] || { echo "LOUD review-gate: design note '$ui' named in $found does not exist"; exit 5; }
      /usr/bin/grep -q 'frontend-design' "$note" && /usr/bin/grep -q 'ux-review' "$note" || { echo "LOUD review-gate: design note $note does not name both the frontend-design and ux-review passes"; exit 5; }
    fi;;
  esac
  # B105 (11-04, 7711 Mac PASS, Linux build red): the QA PASS line names a green PR build at the same head.
  log=${BUILD_LOG:-~/.crius/state/quotes/build/BUILD-LOG.md}
  qa=$(/usr/bin/grep -a -E "^[0-9T:-]+ DONE qa-[a-z0-9-]+ .*PR $pr\b.*${head:0:8}" "$log" | /usr/bin/grep -a -E "\bPASS\b" | /usr/bin/grep -a -v -E "\b(NOT|no) PASS\b" | tail -1)  # PASS before or after the sha (7732 23:54)
  [ -n "$qa" ] || { echo "LOUD review-gate: no qa DONE ... PASS line names PR $pr at ${head:0:8}"; exit 6; }
  # Lead ORDER 11-05 19:4x (two hand PASS lines named branch builds): from 19:45 a PASS counts only when pass-post.sh wrote it.
  [[ "${qa:0:16}" < "2026-11-05T19:45" ]] || [[ $qa == *"[pass-post]"* ]] || { echo "LOUD review-gate: the PASS for PR $pr was not posted by qa/tools/pass-post.sh (no [pass-post] mark)"; exit 6; }
  bid=$(echo "$qa" | /usr/bin/grep -o -i -E "build ([0-9.]+ \()?[0-9]+\)? green" | /usr/bin/grep -o -E "[0-9]+\)? green" | /usr/bin/grep -o -E "^[0-9]+")
  [ -n "$bid" ] || { echo "LOUD review-gate: the QA PASS for PR $pr names no 'build <id> green' (B105)"; exit 6; }
  b=$(az pipelines runs show --org https://dev.azure.com/Appxite --project "AppXite Platform" --id "$bid" --query "[result, sourceBranch, sourceVersion]" -o tsv | tr "\n" " ") || { echo "LOUD review-gate: cannot read build $bid"; exit 2; }
  set -- $b
  # A PR build runs refs/pull/<n>/merge; that merge commit's second parent is the PR head it built.
  p2=$(az devops invoke --org https://dev.azure.com/Appxite --area git --resource commits --route-parameters project="AppXite Platform" repositoryId=d88331ee-60dc-4f15-b236-b1790c6fdeaa commitId="$3" --api-version 7.1 --query "parents[1]" -o tsv) || { echo "LOUD review-gate: cannot read merge commit of build $bid"; exit 2; }
  [ "$1" = succeeded ] && [ "$2" = "refs/pull/$pr/merge" ] && [ "$p2" = "$head" ] || { echo "LOUD review-gate: build $bid is .$1. on .$2. of head .${p2:0:8}., not a green build of PR $pr at ${head:0:8} (B105)"; exit 6; }
  echo "review-gate OK: PR $pr head ${head:0:8} reviewed in $found; QA PASS with green build $bid"; exit 0
fi
echo "LOUD review-gate: no deep-review.md has 'Reviewed: <base>..${head:0:8}' for PR $pr"
exit 3
