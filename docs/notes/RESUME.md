# crius:build — RESUME (contract: docs/notes/COST-LEVER3-FRESH.md §2, max 120 lines)

Updated 2026-10-19T14:49 by session ff09d323 (CLEAR-READY after P2). Bus read to crius.log line 51 (my P2 DONE).
Last owner message seen: 2026-10-19 ~14:30 (via Crius brief ~/.crius/state/crius/BRIEF-build-2026-10-19.md).
main = origin/main = 4a102aa (P2) + this RESUME commit. Local main ref is behind: it is checked out in Crius's worktree main-rules. History: docs/notes/RESUME-HISTORY.md.

## OWNER DECISIONS (append-only, verbatim, dated)

- 2026-10-16 design §10 D1-D4 (docs/MEMORY-AND-MEASUREMENT.md §10): one Max account = one weekly quota; everything
  local; config changes approved one at a time as each step comes, step 6 = "the `FINDINGS` line in the codex and
  quick-review agents", "still announced before it is applied"; push notifications only. Status: in force.
- 2026-10-16 22:43 MORNING RULE (owner, via Crius): "fresh (<=45 min) current-epoch sample: room if 7d<100 and
  5h<100. Stale current-epoch sample: room if 7d<90 and 5h<90." Status: done (main f57ddd8).
- 2026-10-17 11:44 (bus): "Pause rest only quotes work". Status: lifted for crius:build by the 10-19 order below.
- 2026-10-18 (COST-LEVERS-STATUS.md): "GO all" on cost levers 1-4. Crius's work; this RESUME follows lever 3.
- 2026-10-19 ~14:30 (owner, via Crius brief): quiet time while Quotes waits for Maris: start step 6 "record accept
  + scorecard" (design §9). Max quota 83% (resets Thu 2026-10-22 02:00): work alone, no team fan-out, one review
  pass (codex agent) before DONE. Other teams stay paused. Status: in progress.
- 2026-10-19 14:44 (owner picked "2", via Crius ORDER bus line 49): "also track the Enterprise account's MONTHLY
  usage limit (~/.claude-work, org eef9f4e6-9a9c-4120-8c7f-bcc4fca7d047 = aitch fallback), next to the Max 5h/7d
  sampling (step 3). Show used vs this month's limit and the reset date in quota output, and let 'quota --room' report
  whether the fallback has room. Add it to the RESUME checklist as step 3b, after step 6 P2. Read-only: reuse the
  session's existing login, no new sign-in; the Enterprise login expires ~2026-10-21, so a missing or expired login
  must be a loud 'cannot read', not 0%. Red check first." Status: queued after P2.
- Standing (memory): personal repo, push to main after review; effort medium/high only; no AI attribution.

## GOAL

Goal: build the Crius record (design §9 steps 1-9) so Crius answers the owner's questions from its own store
- [x] 1 store layout, leak test, decisions, items, bus grammar · 060610f
- [x] 2 crius-ingest v1, backup, watchdog · ccf32e8
- [x] 3 quota sampling + quota --room · e4f35b8, aa38638, f57ddd8
- [x] 4 find/show/health/--deep, redaction, recall eval 31/34 · b3a87cb, bus 2026-10-17T02:27
- [x] 4b fail-closed key=value + gitleaks rules · 86a776a
- [x] 5 digest + Crius daily start (09:00/18:00 goal report) · 23052fe .. 50e2384
- [ ] 3b Enterprise monthly limit (owner 10-19 14:44): used vs limit + reset in `quota`, fallback room in
      `quota --room`; read-only with the existing ~/.claude-work login; missing/expired login = loud "cannot read",
      never 0%. Red first: an expired login gives "cannot read" and no room verdict. Build after P2, before P3.
- [ ] 6 record accept + scorecard
- [ ] 9 Falcon source
Done: 67% (6 of 9 required steps, equal weight; 7 and 8 only if needed)
Next: step 3b (Enterprise monthly limit), then step 6 P3 scorecard
Needs owner: nothing

## NOW

P2 DONE: main 4a102aa pushed + installed clean (vcs.modified=false). d4d9f61 = feature, 4a102aa = codex fixes (7 P2,
all red first in internal/store/defect_test.go TestReviewOfD4d9f61 + recall TestFindReadsCorrectedItems). Live:
tables defect/correction + views item_v/defect_v built, `record defect nope#1` refused rc 2, 0 rows.
Next session: step 3b from a fresh worktree off origin/main. Start by finding where Claude Code keeps the
~/.claude-work login and how the monthly Enterprise limit can be read with it (read-only, no new sign-in); hand
that search to Explore. Red first: expired/missing login -> "cannot read", no room verdict, never 0%.

## Open work (step 6 checklist; each part red first, then build, codex review, one DONE with evidence + commit)

Spec: design §9 row 6, §3c tables (acceptance, defect, correction), §7 (M1, M6, M10, scorecard shape), §7b.
1. [x] P1 (3c86343, bus DONE 14:31) `record accept ITEM --revision SHA|SHA256 --check N=REF ... --review REF --by crius|owner`: acceptance
   table, ACCEPTED event, item state. A check/review ref (commit sha or event:<n>) must name the same revision.
   Red: a check naming another sha -> refused, no row; a criterion with no check -> refused; reviewer = the
   revision's own session -> refused. Code: internal/store (new accept.go), cli.go record switch, recall/show.go.
2. [x] P2 (d4d9f61 + 4a102aa) `record defect` / `revoke` / `correct` + guard item + `show ITEM --history`. Red: revoke keeps the row
   (state revoked, reason, defect) and opens a fix item; 2nd confirmed defect of one class opens a guard item;
   a correction leaves the target row as it was; history lists open -> accept -> defect -> revoke -> fix in order.
3. [ ] P3 scorecard `crius-rec scorecard [--week EPOCH] [--write]` -> state/scorecards/<week>.md (~40 lines,
   stamped, 0 model tokens). Red: a revoke lowers the original week, not the week found; a correction changes the
   view; re-writing a changed week appends `revised <date>: …` and keeps the old value.
4. [ ] P4 parity with review-1016 (tools/agent-cost/review-1016 REPORT §4: $/main call on weekdays, compactions,
   10-10..10-16 personal): store numbers within 2%. Red: the compare fails on a number moved by 3%.
5. [ ] P5 restore-after-expiry test (design §3c): newest backup -> scratch DB, transcripts > 30 d hidden,
   regenerate a matured week, compare with its file; weekly launchd; `health` shows the result. Red: a changed
   scorecard file fails the test.
6. [ ] P6 FINDINGS line (D3 config change): NEED to Crius with the exact diff of the codex + quick-review agent
   files BEFORE applying; ingest `FINDINGS P1=n P2=n P3=n` as evidence kind review bound to the reviewed sha.
7. [ ] P7 first descriptive read on 10-23 next to the review-1016 scripts (after the 10-22 02:00 reset).

## Waiting on

- Nothing. Crius answers on ~/.crius/bus/crius.log lines `-> crius:build`.

## Do not redo

- Steps 1-5 are closed by Crius (shas in GOAL). Their checklists and evidence: RESUME-HISTORY.md, STATUS.md.
- Clean builds: a git clone in the scratchpad (a worktree build gets no vcs stamp, Go 1.25.6). GOTOOLCHAIN=local.
- Install: clean clone of the main commit, then bin/crius-install-launchd (or install-bins.sh); check
  `go version -m ~/.crius/bin/crius-ingest` shows vcs.modified=false.
- Bus lines under ~240 chars (300 hard limit); format `$(date +%FT%H:%M) TYPE crius:build -> crius:lead — text`.
- P1 binding rules (codex 43067a2): every sha an event names must resolve uniquely to the revision; see design §4
  "record accept as built". Ship script pattern: <scratch f33ce11c>/ship.sh (push, clean clone tests, install).
- Mutation testing: never wrap mut4.py-style scripts in `timeout` (SIGTERM leaves the mutant in the repo).

## Running things

- Worktree <scratchpad ff09d323>/wt on feat/step6-p2 (= main). No watchers, no background jobs. Ship script:
  <scratchpad ff09d323>/ship.sh (branch feat/step6-p2). Mutation script: <scratchpad ff09d323>/mut.py (12/12 red on P2).
- launchd: space.scifilab.crius.{ingest,watchdog,digest,wait-sweep} run ~/.crius/bin (installed from 4a102aa;
  previous binaries kept as <scratchpad ff09d323>/crius-*.before-4a102aa).
- Old worktrees from earlier sessions (git worktree list): not mine to remove; listed for Crius.

## Not in this file

- STATUS.md (project status), docs/MEMORY-AND-MEASUREMENT.md (design), docs/RULES.md, bus ~/.crius/bus/crius.log.
