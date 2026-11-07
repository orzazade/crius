#!/bin/bash
# merge.sh <pr> <build>: the only way the lead completes a PR into develop (Crius 11-05 20:0x, after two misread gate rcs).
# Runs every B13 check itself and calls "az repos pr update --status completed" only when all of them pass:
#   review-gate.sh rc 0 (NO_AC_REASON passes through the environment), merge-ok.sh rc 0, <build> is a green
#   refs/pull/<pr>/merge build, Build policy approved, 0 text threads, 0 attribution trailers, my approve vote present,
#   head unchanged since the gate. The vote is a separate write before this script (one ADO write per call).
# rc 0 = completed, 10 gate, 11 merge-ok, 12 build, 13 policy, 14 threads, 15 trailers, 16 vote, 17 head moved,
# 18 complete failed or not completed in 5 min, 19 target is not develop, 2 usage or lookup error.
NS=/private/tmp/claude-501/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/3584a7f4-fcb0-4260-9e8d-e7749735c202/scratchpad
O=https://dev.azure.com/Appxite; P="AppXite Platform"; R=d88331ee-60dc-4f15-b236-b1790c6fdeaa; G=~/Projects/Appxite/svc_quote/wt/platform
pr="$1"; bid="$2"
[[ $pr =~ ^[0-9]+$ && $bid =~ ^[0-9]+$ ]] || { echo "usage: merge.sh <pr> <build>"; exit 2; }
no() { echo "REFUSED merge.sh rc $1: PR $pr: $2"; exit "$1"; }

head=$(az repos pr show --org $O --id "$pr" --query lastMergeSourceCommit.commitId -o tsv) || no 2 "cannot read PR"
[ ${#head} -eq 40 ] || no 2 "head '$head' is not a sha"
# Crius 11-05 20:1x: never complete into anything but develop (main included); Crius 11-06 05:55 (batch gate): also
# integration/96365-bN, where chunk PRs complete on their own build + QA before one batch PR bN -> develop.
tgt=$(az repos pr show --org $O --id "$pr" --query targetRefName -o tsv) || no 2 "cannot read target branch"
[[ $tgt == refs/heads/develop || $tgt =~ ^refs/heads/integration/96365-b[0-9]+$ ]] || no 19 "target is '$tgt', not develop or integration/96365-bN"
export TB=${tgt#refs/heads/}

# SYNC_OK=<pr> (Crius DECISION 11-06 11:27, sync PRs develop -> bN only): skips review-gate alone, and only when the PR head
# is already on origin/develop (every commit reviewed there); else rc 10. Every other check runs.
if [ "${SYNC_OK:-}" = "$pr" ]; then
  stok=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv) || no 2 "no ADO token"
  git -C $G -c http.extraHeader="Authorization: Bearer $stok" fetch -q origin develop || no 2 "fetch develop failed"
  git -C $G merge-base --is-ancestor "$head" origin/develop || no 10 "SYNC_OK: head ${head:0:8} is not on origin/develop, so this is not a sync PR"
  echo "review-gate skipped: SYNC_OK=$pr, head ${head:0:8} is on origin/develop"
else
  o=$(MERGE_BUILD="$bid" bash $HOME/.crius/state/quotes/build/bin/lead/review-gate.sh "$pr" 2>&1); g=$?
  echo "$o"; [ $g -eq 0 ] || no 10 "review-gate rc $g"
fi

o=$(bash $HOME/.crius/state/quotes/build/bin/lead/merge-ok.sh "$pr" "$bid" 2>&1); m=$?
echo "$o"; [ $m -eq 0 ] || no 11 "merge-ok rc $m"

b=$(az pipelines runs show --org $O --project "$P" --id "$bid" --query "[result, sourceBranch]" -o tsv | tr '\n' ' ') || no 2 "cannot read build $bid"
[ "$b" = "succeeded refs/pull/$pr/merge " ] || no 12 "build $bid is '$b', not a green refs/pull/$pr/merge build"

pol=$(az repos pr policy list --org $O --id "$pr" --query "[?configuration.type.displayName=='Build'].status | [0]" -o tsv) || no 2 "cannot read policies"
# An integration branch may have no Build policy; then the green refs/pull/<pr>/merge build above is the build check.
[ "$pol" = approved ] || { [ "$TB" != develop ] && [ -z "$pol" ]; } || no 13 "Build policy is '$pol', not approved"

t=$(az devops invoke --org $O --area git --resource pullRequestThreads --route-parameters project="$P" repositoryId=$R pullRequestId="$pr" --api-version 7.1 --query "length(value[?isDeleted!=\`true\`] | [?comments[?commentType=='text']])" -o tsv) || no 2 "cannot read threads"
[ "$t" = 0 ] || no 14 "$t text thread(s) on the PR"

git -C $G cat-file -e "$head^{commit}" || no 2 "head ${head:0:8} not fetched in $G"
n=$(git -C $G log --format=%B "origin/$TB..$head" | /usr/bin/grep -ciE 'co-authored-by|claude-session|\bgenerated\b')  # \b: "Migration regenerated" in 7829 was a false rc 15 (11-06)
[ "$n" = 0 ] || no 15 "$n attribution trailer line(s) in origin/$TB..${head:0:8}"

v=$(az repos pr show --org $O --id "$pr" --query "reviewers[?uniqueName=='Orkhan.Rzazade@appxite.com'].vote | [0]" -o tsv) || no 2 "cannot read votes"
[ "$v" = 10 ] || no 16 "no approve vote from me (vote '$v'): run az repos pr set-vote --vote approve first"

h2=$(az repos pr show --org $O --id "$pr" --query lastMergeSourceCommit.commitId -o tsv) || no 2 "cannot re-read PR"
[ "$h2" = "$head" ] || no 17 "head moved ${head:0:8} -> ${h2:0:8} during the checks"

# rc 20 (lead 11-05 22:03, second slip: 7798 merged during 7772's rebuild, 7799 during 7785's): refuse while another PR
# that requeue-all.sh queued (lead/inflight.txt) still has its Build policy running and shares a project folder with this
# PR, because this merge would make that rebuild stale. ORDER_OK=<that pr> overrides for one named PR.
inf=$HOME/.crius/state/quotes/build/bin/lead/inflight.txt
proj() { awk -F/ '{ if ($1=="src"||$1=="tests") print $1"/"$2; else if ($1=="web") print "web"; else print $1 }' | sort -u; }
tok=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv) || no 2 "no ADO token"
git -C $G -c http.extraHeader="Authorization: Bearer $tok" fetch -q origin "$TB" || no 2 "fetch $TB failed"
mf=$(git -C $G diff --name-only "origin/$TB...$head" | proj)
for q in $(sort -u "$inf" 2>/dev/null); do
  [ "$q" = "$pr" ] && continue
  # lead 11-06 10:43 (7832 into b1 refused over 7809's develop rebuild): only a PR on the same target can go stale.
  [ "$(az repos pr show --org $O --id "$q" --query targetRefName -o tsv < /dev/null)" = "$tgt" ] || continue
  st=$(az repos pr policy list --id "$q" --org $O --query "[?configuration.type.displayName=='Build'].status | [0]" -o tsv < /dev/null)
  case "$st" in running|queued) ;; *) continue;; esac
  qh=$(az repos pr show --org $O --id "$q" --query lastMergeSourceCommit.commitId -o tsv) || no 2 "cannot read PR $q"
  git -C $G -c http.extraHeader="Authorization: Bearer $tok" fetch -q origin "$qh" || no 2 "fetch of PR $q head failed"
  sh=$(comm -12 <(echo "$mf") <(git -C $G diff --name-only "origin/$TB...$qh" | proj))
  [ -z "$sh" ] || [ "${ORDER_OK:-}" = "$q" ] || no 20 "PR $q rebuild is $st and shares $(echo $sh) with this PR: merging now makes it stale (wait, or ORDER_OK=$q)"
done
# rc 21 (Crius 11-05 23:24, 7820 AC31 page before any server part): a PR that changes only web/ may not merge while a
# board row named in its title (ACnn) has server other than done or na. A title with no ACnn passes (NO_AC_REASON PRs).
if [ "$mf" = web ]; then
  ttl=$(az repos pr show --org $O --id "$pr" --query title -o tsv) || no 2 "cannot read title"
  for ac in $(echo "$ttl" | /usr/bin/grep -o -E 'AC[0-9]+' | sort -u); do
    sv=$(awk -F'\t' -v a="$ac" '$1==a {print $6}' ~/.crius/state/quotes/build/board/board.tsv)
    [ -n "$sv" ] || no 2 "$ac from the title is not on the board"
    # SERVER_LATER_OK=<pr>: the page reads the server's fields as optional (B74) and its QA PASS says so.
    case "$sv" in done|na) ;; *) [ "${SERVER_LATER_OK:-}" = "$pr" ] || no 21 "page PR names $ac whose server part is '$sv' on the board: merge the server PR first (or SERVER_LATER_OK=$pr when the page reads it as optional, B74)";; esac
  done
fi

az repos pr update --org $O --id "$pr" --status completed --squash false --delete-source-branch true -o none || no 18 "az repos pr update failed"
for i in $(seq 30); do
  s=$(az repos pr show --org $O --id "$pr" --query "[status, lastMergeCommit.commitId]" -o tsv | tr '\n' ' ')
  case "$s" in completed\ *) [ -f "$inf" ] && { /usr/bin/grep -v -x "$pr" "$inf" > "$inf.new"; mv "$inf.new" "$inf"; }; echo "merge.sh: PR $pr completed, merge commit ${s#completed }"; [ "${TB:-develop}" = develop ] || bash $HOME/.crius/state/quotes/build/bin/lead/postmerge-scan.sh "$TB"; exit 0;; esac
  sleep 10
done
no 18 "PR still '$s' after 5 min"
