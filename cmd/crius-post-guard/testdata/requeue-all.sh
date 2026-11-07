#!/bin/bash
# requeue-all.sh [pr ...]: re-queues the Build policy of every active svc PR into develop (or the named ones).
# Ends with "queued N of M"; rc 3 when N < M (a silent short loop missed 5 PRs 11-05 18:4x).
O=https://dev.azure.com/example
if [ $# -gt 0 ]; then prs="$*"; else prs=$(az repos pr list --org $O --project "Example Project" --repository svc --status active --target-branch develop --top 200 --query "[].pullRequestId" -o tsv) || { echo "LOUD requeue: cannot list PRs"; exit 2; }; fi
m=0; n=0
for p in $prs; do
  m=$((m+1))
  e=$(az repos pr policy list --id "$p" --org $O --query "[?configuration.type.displayName=='Build'].evaluationId | [0]" -o tsv < /dev/null)
  if [ -n "$e" ] && az repos pr policy queue --id "$p" --evaluation-id "$e" --org $O -o none < /dev/null; then n=$((n+1)); echo "queued $p"; else echo "LOUD requeue: PR $p not queued (eval '$e')"; fi
done
echo "queued $n of $m"
[ "$n" -eq "$m" ] && [ "$m" -gt 0 ] || exit 3
exit $?
