# crius:build — RESUME (contract: docs/notes/COST-LEVER3-FRESH.md §2, max 120 lines)

Written 2026-10-19 14:45 by session f33ce11c. Bus read to ~/.crius/bus/crius.log line 34 (14:16 ORDER).
Last owner message seen: 2026-10-19 ~14:30 (via Crius brief ~/.crius/state/crius/BRIEF-build-2026-10-19.md).
Branch feat/step6 (worktree <scratchpad>/wt) off main 50e2384. History: docs/notes/RESUME-HISTORY.md.

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
- Standing (memory): personal repo, push to main after review; effort medium/high only; no AI attribution.

## GOAL

Goal: build the Crius record (design §9 steps 1-9) so Crius answers the owner's questions from its own store
- [x] 1 store layout, leak test, decisions, items, bus grammar · 060610f
- [x] 2 crius-ingest v1, backup, watchdog · ccf32e8
- [x] 3 quota sampling + quota --room · e4f35b8, aa38638, f57ddd8
- [x] 4 find/show/health/--deep, redaction, recall eval 31/34 · b3a87cb, bus 2026-10-17T02:27
- [x] 4b fail-closed key=value + gitleaks rules · 86a776a
- [x] 5 digest + Crius daily start (09:00/18:00 goal report) · 23052fe .. 50e2384
- [ ] 6 record accept + scorecard
- [ ] 9 Falcon source
Done: 75% (6 of 8 required steps, equal weight; 7 and 8 only if needed)
Next: step 6, plan below (red check first per part)
Needs owner: nothing

## NOW

14:45 fresh session. RESUME slimmed; step 6 plan being written into the checklist below.

## Open work (step 6 checklist; each part red first, then build, one DONE with evidence and commit)

1. [ ] Plan step 6 parts here (design §9 row 6, §3c tables, §7, §7b), then post the STATUS line on the bus.

## Waiting on

- Nothing. Crius answers on ~/.crius/bus/crius.log lines `-> crius:build`.

## Do not redo

- Steps 1-5 are closed by Crius (shas in GOAL). Their checklists and evidence: RESUME-HISTORY.md, STATUS.md.
- Clean builds: a git clone in the scratchpad (a worktree build gets no vcs stamp, Go 1.25.6). GOTOOLCHAIN=local.
- Install: clean clone of the main commit, then bin/crius-install-launchd (or install-bins.sh); check
  `go version -m ~/.crius/bin/crius-ingest` shows vcs.modified=false.
- Bus lines under ~240 chars (300 hard limit); format `$(date +%FT%H:%M) TYPE crius:build -> crius:lead — text`.
- Mutation testing: never wrap mut4.py-style scripts in `timeout` (SIGTERM leaves the mutant in the repo).

## Running things

- Worktree: <scratchpad f33ce11c>/wt on feat/step6. No watchers, no background jobs.
- launchd: space.scifilab.crius.{ingest,watchdog,digest} run ~/.crius/bin (installed from main 50e2384).
- Old worktrees from earlier sessions (git worktree list): not mine to remove; listed for Crius.

## Not in this file

- STATUS.md (project status), docs/MEMORY-AND-MEASUREMENT.md (design), docs/RULES.md, bus ~/.crius/bus/crius.log.
