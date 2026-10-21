# crius:build — RESUME (contract: docs/notes/COST-LEVER3-FRESH.md §2, max 120 lines)

Updated 2026-10-19 19:10 by session 9f01299b (P6b FINDINGS ingest shipped and proven). Last bus line read: 2026-10-19T18:43 ORDER crius:lead.
Last owner message seen: 2026-10-19 ~17:25 (child roll-up answer, via Crius).
main = origin/main = this RESUME commit (P6b). Local main ref is behind: it is checked out in Crius's worktree main-rules. History: docs/notes/RESUME-HISTORY.md.

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
  must be a loud 'cannot read', not 0%. Red check first." Status: done (main 97dc0d1; Crius NEED 15:06: included usage + spend limit, not 'monthly').
- 2026-10-19 15:25 (Crius ORDER x2, owner switched work folders to crius-statusline --persona work): "on an API-billed
  (Enterprise) session the statusline prints only 'quota: no plan limits (API-billed account)' ... Show the Enterprise
  fallback there instead: included usage % + expiry and spend limit, from the same read quota uses (cached, never
  blocks the line). Red check first." Addendum: "Make the work line show the same parts (model + account, branch,
  cost, context %) plus the fallback numbers. Old script kept: ~/.claude-work/hooks/statusline.js; backup
  ~/.claude-work/settings.json.bak-20261019-statusline." After P3. Status: done (main 47e7913).
- 2026-10-19 ~17:25 (owner, via Crius, answer to review P3-3): yes, a defect or revoke on an accepted child task
  counts against the parent outcome's week ("we need extensive data that will give us much better vision"). Build it
  after the work statusline, red first. Status: done (main c133717, 27c2a73; scorecard def v2).
- Standing (memory): personal repo, push to main after review; effort medium/high only; no AI attribution.

## GOAL (2026-10-19 19:10)

Goal: build the Crius record (design §9 steps 1-9) so Crius answers the owner's questions from its own store
- [x] 1 store layout, leak test, decisions, items, bus grammar · 060610f
- [x] 2 crius-ingest v1, backup, watchdog · ccf32e8
- [x] 3 quota sampling + quota --room · e4f35b8, aa38638, f57ddd8
- [x] 4 find/show/health/--deep, redaction, recall eval 31/34 · b3a87cb, bus 2026-10-17T02:27
- [x] 4b fail-closed key=value + gitleaks rules · 86a776a
- [x] 5 digest + Crius daily start (09:00/18:00 goal report) · 23052fe .. 50e2384
- [x] 3b Enterprise fallback: included usage + spend limit (owner 10-19 14:44, Crius NEED 15:06) · 72e9793, 97dc0d1
- [ ] 6 record accept + scorecard (P1-P6, P3c done; P3b, P7 open)
- [ ] 9 Falcon source
Done: 70% (7 of 10 required steps incl. 3b, equal weight; 7 and 8 only if needed)
Next: step 6 P7 descriptive read on 10-23 (after the 10-22 02:00 reset); P3b open
Needs owner: nothing

## NOW

P6b DONE (this session 9f01299b): main f095c93 (ingest), 7a38fdc + 063b163 + the commit after it (review fixes),
83c4278 (git reads origin's branch when ahead). Installed clean. Rows: evidence kind review ref
"<agent> P1=n P2=n P3=n <commit=sha|uncommitted|base=x|target=unknown> session=<id>", revision bound after git
ingest (bindReviews); kind review-uncounted = no FINDINGS line or lines that disagree. health line "reviews last 7 d".
Rule (after 3 review rounds of shell-regex cases): a review's commit is never guessed; unknown is visible, not wrong.
Real-run proof 2026-10-19: quick-review of 7a38fdc + 063b163 and codex of f095c93, 83c4278 all stored and bound;
a timed-out codex review of another session stored as review-uncounted (not zero).
Found + fixed: git ingest read local main only; crius local main sat at 50e2384 (10-17), so the store missed every
step 6 commit while health said complete. Now the newer of main and origin/main; diverged: local + partial warning.

Next: P7 on 10-23 (after the 10-22 02:00 reset); P3b open; step 9 paused with Falcon.

## Open work (step 6 checklist; each part red first, then build, codex review, one DONE with evidence + commit)

Spec: design §9 row 6, §3c tables (acceptance, defect, correction), §7 (M1, M6, M10, scorecard shape), §7b.
1. [x] P1 (3c86343) `record accept` with revision-bound checks/review (spec in design §4 "record accept as built").
2. [x] P2 (d4d9f61 + 4a102aa) `record defect` / `revoke` / `correct` + guard item + `show ITEM --history`.
3. [x] P3 (61fdb75 + 93957fc) `crius-rec scorecard [--week EPOCH] [--write]` -> state/scorecards/<week>.md.
3b. [ ] P3b per-week enterprise $ + project shares in the scorecard (needs a quota.Build for a past epoch).
3c. [x] P3c (c133717 + 27c2a73) child defect/revoke lowers the parent outcome's week (owner 10-19 ~17:25).
4. [x] P4 (e304c4d) parity with review-1016 REPORT §4: store numbers within 2%.
5. [x] P5 (3f93098) restore-after-expiry test, weekly launchd Thu 04:00, `health` shows it.
6. [x] P6 FINDINGS line: config (owner GO 18:40) + P6b ingest as evidence kind review bound to the reviewed sha
   (f095c93 .. 063b163 + next, 83c4278); real runs stored and bound (NOW).
7. [ ] P7 first descriptive read on 10-23 next to the review-1016 scripts (after the 10-22 02:00 reset).

## Waiting on

  Now they show on no scorecard. Also noticed: a parent correction cannot make a child top-level again (defect.go:312).
- Crius answers on ~/.crius/bus/crius.log lines `-> crius:build`.

## Do not redo

- Steps 1-5 are closed by Crius (shas in GOAL). Their checklists and evidence: RESUME-HISTORY.md, STATUS.md.
- Clean builds: a git clone in the scratchpad (a worktree build gets no vcs stamp, Go 1.25.6). GOTOOLCHAIN=local.
- Install: from the clone ~/Projects/scifi/crius-live (`git pull` after a push), bin/crius-install-launchd; it refuses
  a checkout that is dirty or not at origin/main. Check `go version -m ~/.crius/bin/crius-ingest` shows vcs.modified=false.
- Bus TYPE rule (Crius 16:58): CLEAR-READY is its own TYPE, `$(date +%FT%H:%M) CLEAR-READY crius:build -> crius:lead —
  text`, never INFO: INFO does not wake Crius (the 15:14 one left the builder idle ~2 h).
- Bus lines under ~240 chars (300 hard limit); format `$(date +%FT%H:%M) TYPE crius:build -> crius:lead — text`.
- P1 binding rules (codex 43067a2): every sha an event names must resolve uniquely to the revision; see design §4
  "record accept as built". Ship script pattern: <scratch f33ce11c>/ship.sh (push, clean clone tests, install).
- Mutation testing: never wrap mut4.py-style scripts in `timeout` (SIGTERM leaves the mutant in the repo).

## Running things

- Worktree <scratchpad 11838a67>/wt on fix/digest-stale (= main). No watchers, no background jobs. Ship script:
  <scratchpad 11838a67>/ship.sh (branch fix/digest-stale).
- launchd: space.scifilab.crius.{ingest,watchdog,digest,weekly,wait-sweep} run ~/.crius/bin (installed from the fix(digest) stale-RESUME commit;
  previous binaries kept as <scratchpad 11838a67>/crius-*.before-<sha>).
- Old worktrees from earlier sessions (git worktree list): not mine to remove; listed for Crius.

## Not in this file

- STATUS.md (project status), docs/MEMORY-AND-MEASUREMENT.md (design), docs/RULES.md, bus ~/.crius/bus/crius.log.
