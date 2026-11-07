#!/bin/bash
# merge-ok.sh <pr> <build>: Crius DECISION 11-05 19:01. A PR whose green build sat on an older develop may merge without a
# rebuild when (1) a trial merge of its head into current develop is clean, (2) the commits that reached develop since that
# build touch none of the PR's project folders (web/, src/<project>/, tests/<project>/), (3) neither side touches a shared file
# (contracts/*openapi*, any Migrations folder, *.props, *.targets, package.json, lockfiles, global.json, *.sln).
# rc 0 = OK (or the build already sits on current develop), rc 4 = rebuild needed (reason printed), rc 2 = lookup error.
# After each merge, watch develop's own build; a red develop stops merges until fixed.
G=~/Projects/Appxite/svc_quote/wt/platform; O=https://dev.azure.com/Appxite
# Crius DECISION 11-06 16:02: merge trials go to the front of the heavy queue. heavy.ts has no priority, so a trial step runs
# at once (niced) while the 15-min load is under 28, and through the heavy queue when it is not.
hv() { local l15; l15=$(sysctl -n vm.loadavg | awk '{print (int($3) > int($4) ? int($3) : int($4))}')
  if [ "$l15" -lt 28 ]; then echo "MOK direct: 15-min load $l15 < 28 (Crius 16:02, merge trials first)"; nice -n 5 bash -c "$1"
  else (cd ~/Projects/Appxite/svc_quote/channels/poc/ui-app && HEAVY_WAIT_S=3600 bun scripts/heavy.ts run -- bash -c "$1"); fi; }
pr="$1"; bid="$2"
[ -n "$pr" ] && [ -n "$bid" ] || { echo "usage: merge-ok.sh <pr> <build>"; exit 2; }
# TB (env): the target branch, develop or integration/96365-bN (Crius 11-06 05:55, batch gate); merge.sh passes it.
TB=${TB:-develop}
tok=$(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv) || { echo "LOUD merge-ok: no ADO token"; exit 2; }
head=$(az repos pr show --org $O --id "$pr" --query lastMergeSourceCommit.commitId -o tsv) || { echo "LOUD merge-ok: cannot read PR $pr"; exit 2; }
sv=$(az pipelines build show --org $O --project "AppXite Platform" --id "$bid" --query sourceVersion -o tsv) || { echo "LOUD merge-ok: cannot read build $bid"; exit 2; }
git -C $G -c http.extraHeader="Authorization: Bearer $tok" fetch -q origin "$TB" "$sv" "$head" || { echo "LOUD merge-ok: fetch failed"; exit 2; }
dev=$(git -C $G rev-parse "origin/$TB")
read -r base bhead < <(git -C $G log -1 --format=%P "$sv")
[ "$bhead" = "$head" ] || { echo "LOUD merge-ok: build $bid built ${bhead:0:8}, not PR head ${head:0:8}"; exit 4; }
[ "$base" = "$dev" ] && { echo "merge-ok: build $bid already on develop ${dev:0:8}"; exit 0; }
git -C $G merge-base --is-ancestor "$base" "$dev" || { echo "LOUD merge-ok: build base ${base:0:8} is not in develop"; exit 4; }
# (1) trial merge
mt=$(git -C $G merge-tree --write-tree --name-only "$dev" "$head" 2>&1); mrc=$?
[ $mrc -eq 0 ] || { echo "LOUD merge-ok: trial merge of ${head:0:8} into develop ${dev:0:8} fails (rc $mrc): $(echo "$mt" | /usr/bin/grep -E '^CONFLICT|^fatal|^error' | head -3)"; exit 4; }
# Crius 11-06 11:52 (CORRECTED its 10:32 stale-ok: 7826's trial merge into b1 was text-clean but failed CS7036): an
# integration/96365-bN target gets the same rules below as develop, with $TB in place of develop. On rc 4 the builder merges
# bN into the branch and pushes (ADO then computes a fresh merge commit), then pr-build.sh <pr>.
mine=$(git -C $G diff --name-only "$base...$head") ; since=$(git -C $G diff --name-only "$base" "$dev")
[ -n "$mine" ] || { echo "LOUD merge-ok: PR $pr changes no files"; exit 2; }
shared='(^contracts/.*openapi|/Migrations/|(^|/)Migrations/|\.props$|\.targets$|(^|/)package\.json$|(^|/)(package-lock\.json|bun\.lockb?|yarn\.lock|pnpm-lock\.yaml)$|(^|/)global\.json$|\.sln$)'
# (3) shared files on either side
s1=$(echo "$mine" | /usr/bin/grep -E "$shared"); s2=$(echo "$since" | /usr/bin/grep -E "$shared")
# (2) project folders: web/<first level>, src/<project>, tests/<project>; any other top-level path counts as its own folder
proj() { awk -F/ '{ if ($1=="src"||$1=="tests") print $1"/"$2; else if ($1=="web") print "web"; else print $1 }' | sort -u; }
pm=$(echo "$mine" | proj); ps=$(echo "$since" | proj)
# (3b) Crius 11-05 20:45, mirror of 3a: a web-only PR, develop since the build changed no web/ file and its only shared file
# is the openapi file: the web *-contract tests on the trial merge decide.
# (3d) Crius 11-05 20:58 (takes in 3b): same PR shape; develop's .NET shared files (Migrations, props, targets, sln,
# global.json) pass; any package.json or lockfile anywhere still refuses; the openapi file goes to the contract tests.
contract=""
# (3e) Crius 11-05 21:44: a web-only PR while develop since also changed web/: allowed when no web file changed on both
# sides and every web file on either side is under web/src/ but not web/src/api or web/src/lib (configs, package.json,
# lockfiles refuse). The trial merge then runs tsc -b and the full vitest unit suite, not only the contract tests.
# Crius 21:58: web/e2e/*.spec.ts pass too (still no file on both sides); after such a merge watch develop's own build, and
# if a red develop comes from an e2e spec, take this back out and tell Crius.
train=""
if [ "$pm" = web ] && echo "$ps" | /usr/bin/grep -q -x web; then
  mw=$(echo "$mine" | /usr/bin/grep '^web/' | sort); sw=$(echo "$since" | /usr/bin/grep '^web/' | sort)
  common=$(comm -12 <(echo "$mw") <(echo "$sw"))
  bad=$(printf '%s\n%s\n' "$mw" "$sw" | /usr/bin/grep -v '^$' | /usr/bin/grep -v -E '^web/src/|^web/e2e/[^/]+\.spec\.ts$' ; printf '%s\n%s\n' "$mw" "$sw" | /usr/bin/grep -E '^web/src/(api|lib)/')
  [ -z "$common" ] || { echo "LOUD merge-ok: develop since the build changed web file(s) this PR also changes: $(echo $common)"; exit 4; }
  [ -z "$bad" ] || { echo "LOUD merge-ok: web file(s) outside web/src or in web/src/api|lib on either side: $(echo $bad | head -c 300)"; exit 4; }
  train=yes; contract=yes
fi
# (3f) Crius 11-06 12:52: when the openapi file is the only shared file that moved since the build and the PR does not edit
# it, the trial merge decides: dotnet build, OpenApiDriftTests, web tsc -b and the web contract tests, all rc 0 (each rc in
# the mok log). Any other shared file that moved still refuses below. Only when the web rules (3b/3d) above would refuse:
# a web PR keeps the cheap contract-test path (7827 13:1x waited for the heavy slot it did not need).
oapi=""
# (3g) lead 11-06 16:0x (RULES §0; 7807, 7825, 7858, 7826 looped on rebuilds: each rebuild takes ~25 min and the next merge
# moves bN again): where a file rule below would refuse only because both sides moved, a full trial on the merged tree
# decides at merge time instead (bN cannot move meanwhile: the lead merges one PR at a time). It still refuses: package or
# lockfile changes on either side (the trial cannot install them), Migrations on both sides (two new migrations need a
# rebuilt snapshot), and any text conflict (above). full = "<reason>"; the trial runs dotnet build, all .NET test projects,
# and web tsc -b + full vitest.
full=""
pkall=$(printf '%s\n%s\n' "$s1" "$s2" | /usr/bin/grep -E '(^|/)(package\.json|package-lock\.json|bun\.lockb?|yarn\.lock|pnpm-lock\.yaml)$')
twomig=""; echo "$s1" | /usr/bin/grep -q -E '(^|/)Migrations/' && echo "$s2" | /usr/bin/grep -q -E '(^|/)Migrations/' && twomig=yes
canfull() { [ -z "$pkall" ] && [ -z "$twomig" ]; }
if [ -n "$s2" ]; then
  s2web=$(echo "$s2" | /usr/bin/grep -v -E '(/Migrations/|(^|/)Migrations/|\.props$|\.targets$|\.sln$|(^|/)global\.json$)')
  if [ "$pm" = web ] && { [ -n "$train" ] || ! echo "$ps" | /usr/bin/grep -q -x web; } && { [ -z "$s2web" ] || [ "$s2web" = contracts/quotes.openapi.yaml ]; }; then
    [ -n "$s2web" ] && contract=yes
  elif [ "$s2" = contracts/quotes.openapi.yaml ] && ! echo "$mine" | /usr/bin/grep -q -x contracts/quotes.openapi.yaml; then
    oapi=yes
  elif canfull; then
    full="develop since the build touched shared file(s): $(echo $s2)"
  else
    echo "LOUD merge-ok: develop since the build touched shared file(s): $(echo $s2)$( [ -n "$pkall" ] && echo "; package files: $(echo $pkall)")$( [ -n "$twomig" ] && echo '; Migrations on both sides')"; exit 4
  fi
fi
# (3a) Crius 11-05 20:26: web drift tests read the openapi file. When it is the PR's only shared file and develop since the
# build changed only web/, the web *-contract tests on the trial merge decide (green = no rebuild); any other shared file refuses.
# (3c) Crius 11-05 20:54 (takes in 3a): a PR with no web/ file, develop since the build changed only web/ and no shared
# file (s2 above): its own shared files cannot interact, so they pass; the openapi file still goes to the contract tests.
# (3c+) Crius 11-06 14:00 (7826: Migrations + openapi + one web test; b1 since changed one other web test): a PR with its
# own shared files and web files passes when every change since the build is under web/ and no web file changed on both
# sides; the trial on the merged tree then runs tsc -b and the full vitest (train). Not run or any fail = rc 4.
if [ -n "$s1" ] && [ "$ps" = web ] && echo "$pm" | /usr/bin/grep -q -x web; then
  cw=$(comm -12 <(echo "$mine" | /usr/bin/grep '^web/' | sort) <(echo "$since" | /usr/bin/grep '^web/' | sort))
  [ -z "$cw" ] || { echo "LOUD merge-ok: develop since the build changed web file(s) this PR also changes: $(echo $cw)"; exit 4; }
  # the trial links an existing node_modules, so a PR package or lockfile change would not be installed: refuse
  pk=$(echo "$s1" | /usr/bin/grep -E '(^|/)(package\.json|package-lock\.json|bun\.lockb?|yarn\.lock|pnpm-lock\.yaml)$')
  [ -z "$pk" ] || { echo "LOUD merge-ok: PR changes package file(s) the trial cannot install: $(echo $pk)"; exit 4; }
  train=yes; contract=yes
elif [ -n "$s1" ]; then
  if [ "$ps" = web ] && ! echo "$pm" | /usr/bin/grep -q -x web; then :
  elif canfull; then full="${full:+$full; }PR touches shared file(s): $(echo $s1)"
  else echo "LOUD merge-ok: PR touches shared file(s): $(echo $s1)$( [ -n "$pkall" ] && echo "; package files: $(echo $pkall)")$( [ -n "$twomig" ] && echo '; Migrations on both sides')"; exit 4; fi
  echo "$s1" | /usr/bin/grep -q -x contracts/quotes.openapi.yaml && contract=yes
fi
both=$(comm -12 <(echo "$pm") <(echo "$ps"))
if [ -n "$both" ] && ! { [ -n "$train" ] && [ "$both" = web ]; }; then
  canfull || { echo "LOUD merge-ok: develop since the build changed folder(s) this PR also changes: $(echo $both)"; exit 4; }
  full="${full:+$full; }develop since the build changed folder(s) this PR also changes: $(echo $both)"
fi
# MOK_SCAN=1 (postmerge-scan.sh, Crius 11-06 13:28): stop before any trial; rc 0 here means "no rebuild by the file rules,
# a trial still runs at merge time".
if [ -n "${MOK_SCAN:-}" ]; then echo "merge-ok scan: PR $pr passes the file rules on $TB ${dev:0:8}$( [ -n "$full" ] && echo "; full trial at merge time ($full)" || { [ -n "$oapi$contract" ] && echo '; trial at merge time'; })"; exit 0; fi
# Crius DECISION 11-08 02:13: reuse the green ADO PR build when it built exactly this merge: its sourceVersion is the PR's
# lastMergeCommit, whose target side is $dev and source side is $head. Pipeline 1119 runs dotnet test on the whole solution
# and web npm run check (lint, tsc -b, vitest, build): more than the local trial below. Both jobs must have succeeded.
if [ -n "$full$oapi" ] && [ -z "${MOK_NO_REUSE:-}" ]; then
  mm=$(az repos pr show --org $O --id "$pr" --query "[lastMergeCommit.commitId,lastMergeTargetCommit.commitId,lastMergeSourceCommit.commitId]" -o tsv 2>/dev/null)
  lmc=$(echo "$mm" | sed -n 1p); lmt=$(echo "$mm" | sed -n 2p); lms=$(echo "$mm" | sed -n 3p)
  why=""
  if [ -z "$lmc" ]; then why="cannot read the PR's merge commit"
  elif [ "$lmt" != "$dev" ] || [ "$lms" != "$head" ]; then why="ADO merge commit ${lmc:0:8} is PR ${lms:0:8} on ${lmt:0:8}, not ${head:0:8} on $TB ${dev:0:8}"
  else
    rb=$(az pipelines runs list --org $O --project "AppXite Platform" --pipeline-ids 1119 --branch "refs/pull/$pr/merge" --query "[?sourceVersion=='$lmc' && result=='succeeded'].id | [0]" -o tsv 2>/dev/null)
    if [ -z "$rb" ]; then why="no green pipeline 1119 build on ${lmc:0:8}"
    else
      jobs=$(az devops invoke --org $O --area build --resource timeline --route-parameters project="AppXite Platform" buildId="$rb" --api-version 7.1 --query "records[?type=='Job' && result=='succeeded'].name" -o tsv 2>/dev/null)
      if echo "$jobs" | /usr/bin/grep -q -x '.NET tests (Testcontainers)' && echo "$jobs" | /usr/bin/grep -q -x 'Web checks (lint, types, unit, build)'; then
        echo "merge-ok: PR $pr build $bid on ${base:0:8}; $TB ${dev:0:8}: ${full:-openapi change}; local trial skipped: ADO build $rb green on the merge commit ${lmc:0:8} (PR ${head:0:8} on $TB ${dev:0:8}), .NET tests and web checks jobs succeeded (Crius 11-08 02:13)"
        exit 0
      fi
      why="build $rb lacks a succeeded .NET tests or Web checks job"
    fi
  fi
  echo "MOK no ADO reuse ($why): local trial runs"
fi
if [ -n "$full" ]; then
  D=/private/tmp/claude-501/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/3584a7f4-fcb0-4260-9e8d-e7749735c202/scratchpad/mok-trial
  L=$D.log; H=~/Projects/Appxite/svc_quote/channels/poc/ui-app
  [ ! -e "$D" ] || { echo "LOUD merge-ok: $D exists (another trial running, or left over): remove it by hand"; exit 2; }
  c=$(git -C $G commit-tree "$(echo "$mt" | head -1)" -p "$dev" -p "$head" -m "merge-ok trial $pr") || { echo "LOUD merge-ok: cannot make the trial merge commit"; exit 2; }
  nm=$(ls -d ~/Projects/Appxite/svc_quote/wt/*/web/node_modules 2>/dev/null | head -1)
  [ -n "$nm" ] || { echo "LOUD merge-ok: no web/node_modules in any svc_quote worktree to run the web checks with"; exit 2; }
  git -C $G worktree add -q --detach "$D" "$c" || { echo "LOUD merge-ok: cannot add trial worktree"; exit 2; }
  ln -s "$nm" "$D/web/node_modules"
  : > "$L"; fail=""
  hv "cd $D && dotnet build svc_quote.slnx -m:2 -p:NuGetAudit=false" >> "$L" 2>&1; r=$?
  echo "MOK rc dotnet-build $r" >> "$L"; [ $r -eq 0 ] || fail="$fail dotnet-build:$r"
  for t in tests/Quote.Core.Tests tests/Quote.Worker.Tests tests/Quote.Api.Tests; do
    [ -z "$fail" ] || break
    : > "$L.t"
    hv "cd $D && dotnet test $t --no-build -- NUnit.NumberOfTestWorkers=2" > "$L.t" 2>&1; r=$?
    cat "$L.t" >> "$L"; echo "MOK rc $t $r" >> "$L"
    /usr/bin/grep -a -q -E '^(Passed|Failed)!.*Total: *[1-9]' "$L.t" || r=${r/#0/9}   # no test count = did not run
    [ $r -eq 0 ] || fail="$fail $t:$r"
  done
  rm -f "$L.t"
  if [ -z "$fail" ]; then
    (cd "$D/web" && ./node_modules/.bin/tsc -b && ./node_modules/.bin/vitest run) >> "$L" 2>&1; r=$?
    echo "MOK rc web $r" >> "$L"
    files=$(/usr/bin/grep -a -E 'Test Files +[0-9]+ passed' "$L" | tail -1)
    { [ $r -eq 0 ] && [ -n "$files" ] && ! /usr/bin/grep -a -q -E 'Test Files .*failed' "$L"; } || fail="$fail web:$r"
  fi
  rm "$D/web/node_modules"; git -C $G worktree remove --force "$D"
  [ -z "$fail" ] || { echo "LOUD merge-ok: full trial (3g) on the trial merge failed:$fail ($L); reason for the trial: $full"; exit 4; }
  echo "merge-ok: PR $pr build $bid on ${base:0:8}; $TB ${dev:0:8}: $full; full trial (3g) green on the merged tree: dotnet build, Core/Worker/Api tests, web tsc -b + vitest ($files)"
  exit 0
fi
if [ -n "$oapi" ]; then
  D=/private/tmp/claude-501/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/3584a7f4-fcb0-4260-9e8d-e7749735c202/scratchpad/mok-trial
  L=$D.log; H=~/Projects/Appxite/svc_quote/channels/poc/ui-app
  [ ! -e "$D" ] || { echo "LOUD merge-ok: $D exists (another trial running, or left over): remove it by hand"; exit 2; }
  c=$(git -C $G commit-tree "$(echo "$mt" | head -1)" -p "$dev" -p "$head" -m "merge-ok trial $pr") || { echo "LOUD merge-ok: cannot make the trial merge commit"; exit 2; }
  nm=$(ls -d ~/Projects/Appxite/svc_quote/wt/*/web/node_modules 2>/dev/null | head -1)
  [ -n "$nm" ] || { echo "LOUD merge-ok: no web/node_modules in any svc_quote worktree to run the web checks with"; exit 2; }
  git -C $G worktree add -q --detach "$D" "$c" || { echo "LOUD merge-ok: cannot add trial worktree"; exit 2; }
  ln -s "$nm" "$D/web/node_modules"
  : > "$L"
  hv "cd $D && dotnet build svc_quote.slnx -m:2 -p:NuGetAudit=false" >> "$L" 2>&1; r1=$?
  echo "MOK rc dotnet-build $r1" >> "$L"
  r2=9; [ $r1 -eq 0 ] && { hv "cd $D && dotnet test tests/Quote.Api.Tests --no-build --filter FullyQualifiedName~OpenApiDriftTests" >> "$L" 2>&1; r2=$?; }
  echo "MOK rc openapi-drift $r2" >> "$L"
  (cd "$D/web" && ./node_modules/.bin/tsc -b) >> "$L" 2>&1; r3=$?
  echo "MOK rc web-tsc $r3" >> "$L"
  (cd "$D/web" && ./node_modules/.bin/vitest run $(git ls-files 'src/*-contract.test.ts')) >> "$L" 2>&1; r4=$?
  echo "MOK rc web-contract $r4" >> "$L"
  rm "$D/web/node_modules"; git -C $G worktree remove --force "$D"
  drift=$(/usr/bin/grep -a -E '^(Passed|Failed)!.*Total: *[1-9]' "$L" | tail -1)
  files=$(/usr/bin/grep -a -E 'Test Files +[0-9]+ passed' "$L" | tail -1)
  [ $r1 -eq 0 ] && [ $r2 -eq 0 ] && [ -n "$drift" ] && [ $r3 -eq 0 ] && [ $r4 -eq 0 ] && [ -n "$files" ] || { echo "LOUD merge-ok: openapi trial (3f) on the trial merge: dotnet-build $r1, openapi-drift $r2 (${drift:-no test count}), web-tsc $r3, web-contract $r4 ($L)"; exit 4; }
  echo "merge-ok: PR $pr build $bid on ${base:0:8}; $TB ${dev:0:8} moved the openapi file only (shared); openapi trial (3f) green: dotnet build, drift ($drift), tsc -b, contract ($files)"
  exit 0
fi
if [ -n "$contract" ]; then
  D=/private/tmp/claude-501/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/3584a7f4-fcb0-4260-9e8d-e7749735c202/scratchpad/mok-trial
  L=$D.log
  [ ! -e "$D" ] || { echo "LOUD merge-ok: $D exists (another trial running, or left over): remove it by hand"; exit 2; }
  c=$(git -C $G commit-tree "$(echo "$mt" | head -1)" -p "$dev" -p "$head" -m "merge-ok trial $pr") || { echo "LOUD merge-ok: cannot make the trial merge commit"; exit 2; }
  nm=$(ls -d ~/Projects/Appxite/svc_quote/wt/*/web/node_modules 2>/dev/null | head -1)
  [ -n "$nm" ] || { echo "LOUD merge-ok: no web/node_modules in any svc_quote worktree to run the contract tests with"; exit 2; }
  git -C $G worktree add -q --detach "$D" "$c" || { echo "LOUD merge-ok: cannot add trial worktree"; exit 2; }
  ln -s "$nm" "$D/web/node_modules"
  if [ -n "$train" ]; then
    (cd "$D/web" && ./node_modules/.bin/tsc -b && ./node_modules/.bin/vitest run) > "$L" 2>&1; trc=$?
  else
    (cd "$D/web" && ./node_modules/.bin/vitest run $(git ls-files 'src/*-contract.test.ts')) > "$L" 2>&1; trc=$?
  fi
  rm "$D/web/node_modules"; git -C $G worktree remove --force "$D"
  files=$(/usr/bin/grep -a -E 'Test Files +[0-9]+ passed' "$L" | tail -1)
  [ $trc -eq 0 ] && [ -n "$files" ] && ! /usr/bin/grep -a -q -E 'Test Files .*failed' "$L" || { echo "LOUD merge-ok: web contract tests on the trial merge rc $trc ($L): $(/usr/bin/grep -a -E 'FAIL|failed' "$L" | head -3)"; exit 4; }
  echo "merge-ok: PR $pr build $bid on ${base:0:8}; develop ${dev:0:8} moved by $(echo $ps); PR folders $(echo $pm); shared: $(echo ${s1:-} ${s2:-}); $( [ -n "$train" ] && echo 'web train (3e): tsc -b and full vitest' || echo 'web contract tests') on the trial merge green ($files)"
  exit 0
fi
echo "merge-ok: PR $pr build $bid on ${base:0:8}; develop ${dev:0:8} moved by folders $(echo $ps); PR folders $(echo $pm); trial merge clean, PR shared files: ${s1:-none}"
exit 0
