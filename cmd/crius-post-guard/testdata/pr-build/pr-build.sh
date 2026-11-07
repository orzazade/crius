#!/bin/bash
# pr-build.sh <pr>: the only way a member queues a PR merge build of pipeline 1119 (Crius DECISION 11-06 06:20: duplicate
# builds on 7804 and 7807 within 5 min, queued without checking the PR's current merge commit).
# Queues refs/pull/<pr>/merge only when no run of pipeline 1119 exists on the PR's lastMergeCommit; else prints that run.
# rc 0 = queued (prints "queued <id> on <sha8>"), 3 = a run already exists (prints "exists <id> <status> <result> on <sha8>"),
# 2 = usage or lookup error. Never call "az pipelines run" yourself.
set -o pipefail
O=https://dev.azure.com/Appxite; P="AppXite Platform"
pr="$1"
[[ $pr =~ ^[0-9]+$ ]] || { echo "usage: pr-build.sh <pr>"; exit 2; }
mc=$(az repos pr show --org $O --id "$pr" --query lastMergeCommit.commitId -o tsv) || { echo "LOUD pr-build: cannot read PR $pr"; exit 2; }
[ ${#mc} -eq 40 ] || { echo "LOUD pr-build: PR $pr merge commit '$mc' is not a sha (merge conflict or not computed yet)"; exit 2; }
run=$(az pipelines runs list --org $O --project "$P" --pipeline-ids 1119 --branch "refs/pull/$pr/merge" --top 50 \
  --query "[?sourceVersion=='$mc'] | [0].[id, status, result]" -o tsv | tr '\n' ' ') || { echo "LOUD pr-build: cannot list runs of PR $pr"; exit 2; }
# ADO does not recompute lastMergeCommit when only the target moves (7818 11-06 06:3x: two develop merges, merge commit
# unchanged). A merge commit whose first parent is not the target head is stale: its run no longer counts.
tgt=$(az repos pr show --org $O --id "$pr" --query targetRefName -o tsv) || { echo "LOUD pr-build: cannot read target of PR $pr"; exit 2; }
th=$(az repos ref list --org $O --project "$P" --repository svc_quote --filter "${tgt#refs/}" --query "[?name=='$tgt'].objectId | [0]" -o tsv) || { echo "LOUD pr-build: cannot read $tgt head"; exit 2; }
p1=$(az devops invoke --org $O --area git --resource commits --route-parameters project="$P" repositoryId=d88331ee-60dc-4f15-b236-b1790c6fdeaa commitId="$mc" --api-version 7.1 --query "parents[0]" -o tsv) || { echo "LOUD pr-build: cannot read merge commit ${mc:0:8}"; exit 2; }
[ ${#th} -eq 40 ] && [ ${#p1} -eq 40 ] || { echo "LOUD pr-build: target head '$th' or merge parent '$p1' is not a sha"; exit 2; }
if [ "$p1" = "$th" ] && [ -n "${run// /}" ]; then echo "exists ${run% } on ${mc:0:8} (PR $pr): not queued"; exit 3; fi
# Crius 11-06 15:05 (7778: 127322 on the old head kept running beside 127323): before queueing, cancel every queued or
# running build of this PR on another merge commit ($1 = the sha to keep; "" keeps none, for a stale merge commit).
cancel_old() {
  ids=$(az pipelines runs list --org $O --project "$P" --pipeline-ids 1119 --branch "refs/pull/$pr/merge" --top 50 \
    --query "[?(status=='inProgress' || status=='notStarted') && sourceVersion!='$1'].id" -o tsv) || { echo "LOUD pr-build: cannot list running builds of PR $pr"; exit 2; }
  for i in $ids; do
    az pipelines build cancel --org $O --project "$P" --build-id "$i" -o none || { echo "LOUD pr-build: cannot cancel build $i of PR $pr"; exit 2; }
    echo "cancelled $i (older merge commit of PR $pr)"
  done
}
if [ "$p1" != "$th" ]; then
  # The Build policy re-evaluation makes ADO compute a fresh merge commit; a bare run would build the stale one.
  e=$(az repos pr policy list --org $O --id "$pr" --query "[?configuration.type.displayName=='Build'].evaluationId | [0]" -o tsv) || { echo "LOUD pr-build: cannot read policies of PR $pr"; exit 2; }
  # A bN target has no Build policy, so nothing makes ADO compute a fresh merge commit and a bare run would build the stale
  # one. Crius 11-06 11:52: merge-ok runs the full rules on bN; when it says rebuild, merge bN into the branch and push (a
  # push makes the fresh merge commit), then run this again. Crius 10:42: with no run at all, the bare run below is the first build.
  [ -n "$e" ] || [ -z "${run// /}" ] || { echo "stale: merge ${mc:0:8} is behind ${tgt#refs/heads/} ${th:0:8}${run:+ (run ${run% })}; not queued. If merge-ok asks for a rebuild: merge ${tgt#refs/heads/} into the branch, push, run pr-build.sh $pr again (PR $pr)"; exit 3; }
  if [ -n "$e" ]; then
    cancel_old ""
    az repos pr policy queue --org $O --id "$pr" --evaluation-id "$e" -o none || { echo "LOUD pr-build: policy queue failed for PR $pr"; exit 2; }
    echo "queued Build policy (fresh merge on ${tgt#refs/heads/} ${th:0:8}; old merge ${mc:0:8} was stale) (PR $pr)"; exit 0
  fi
fi
cancel_old "$mc"
id=$(az pipelines run --org $O --project "$P" --id 1119 --branch "refs/pull/$pr/merge" --query id -o tsv) || { echo "LOUD pr-build: queue failed for PR $pr"; exit 2; }
[[ $id =~ ^[0-9]+$ ]] || { echo "LOUD pr-build: queue for PR $pr returned '$id'"; exit 2; }
echo "queued $id on ${mc:0:8} (PR $pr)"; exit 0
