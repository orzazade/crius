# crius:build — RESUME (contract: docs/notes/COST-LEVER3-FRESH.md §2, max 120 lines)

Updated 2026-10-19 by session 6cfda64f (work statusline 47e7913 shipped; next P3c). Last bus line read: 2026-10-19T17:19 ORDER.
Last owner message seen: 2026-10-19 ~17:25 (child roll-up answer, via Crius).
main = origin/main = 47e7913 (work statusline) + this RESUME commit. Local main ref is behind: it is checked out in Crius's worktree main-rules. History: docs/notes/RESUME-HISTORY.md.

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
  after the work statusline, red first. Status: not started.
- Standing (memory): personal repo, push to main after review; effort medium/high only; no AI attribution.

## GOAL

Goal: build the Crius record (design §9 steps 1-9) so Crius answers the owner's questions from its own store
- [x] 1 store layout, leak test, decisions, items, bus grammar · 060610f
- [x] 2 crius-ingest v1, backup, watchdog · ccf32e8
- [x] 3 quota sampling + quota --room · e4f35b8, aa38638, f57ddd8
- [x] 4 find/show/health/--deep, redaction, recall eval 31/34 · b3a87cb, bus 2026-10-17T02:27
- [x] 4b fail-closed key=value + gitleaks rules · 86a776a
- [x] 5 digest + Crius daily start (09:00/18:00 goal report) · 23052fe .. 50e2384
- [x] 3b Enterprise fallback: included usage + spend limit (owner 10-19 14:44, Crius NEED 15:06) · 72e9793, 97dc0d1
- [ ] 6 record accept + scorecard
- [ ] 9 Falcon source
Done: 70% (7 of 10 required steps incl. 3b, equal weight; 7 and 8 only if needed)
Next: P3c child roll-up (owner 10-19 ~17:25), then P4
Needs owner: nothing

## NOW

Work statusline DONE: main 47e7913 (codex FINDINGS P1=0 P2=2 P3=1, all fixed red first). `crius-statusline --persona
work` = model + account (ENT/MAX from $CLAUDE_CONFIG_DIR/.claude.json) │ branch │ $cost │ bar ctx % │ quota, and on
an API-billed session the fallback from spool/ent-usage.json, refreshed by a detached `--refresh-ent` (same read as
quota: quota.ReadWorkEnt) at most once per 5 min (spool/ent-usage.try), errors to logs/ent-refresh.log, STALE after
15 min. Step 6 P3 (scorecard) done 93957fc.
Next: P3c child roll-up, red first. Plan: descendants of the week's outcomes (recursive on item_v.parent) add their
defects (found at/after the child's acceptance) to Defects/escapes and their revokes as a new count; Items for
`since` include them; bump scorecard Def v1 -> v2 (no scorecard file written yet, state/scorecards absent).

## Open work (step 6 checklist; each part red first, then build, codex review, one DONE with evidence + commit)

Spec: design §9 row 6, §3c tables (acceptance, defect, correction), §7 (M1, M6, M10, scorecard shape), §7b.
1. [x] P1 (3c86343, bus DONE 14:31) `record accept ITEM --revision SHA|SHA256 --check N=REF ... --review REF --by crius|owner`: acceptance
   table, ACCEPTED event, item state. A check/review ref (commit sha or event:<n>) must name the same revision.
   Red: a check naming another sha -> refused, no row; a criterion with no check -> refused; reviewer = the
   revision's own session -> refused. Code: internal/store (new accept.go), cli.go record switch, recall/show.go.
2. [x] P2 (d4d9f61 + 4a102aa) `record defect` / `revoke` / `correct` + guard item + `show ITEM --history`. Red: revoke keeps the row
   (state revoked, reason, defect) and opens a fix item; 2nd confirmed defect of one class opens a guard item;
   a correction leaves the target row as it was; history lists open -> accept -> defect -> revoke -> fix in order.
3. [x] P3 (61fdb75 + 93957fc) scorecard `crius-rec scorecard [--week EPOCH] [--write]` -> state/scorecards/<week>.md (~40 lines,
   stamped, 0 model tokens). Red: a revoke lowers the original week, not the week found; a correction changes the
   view; re-writing a changed week appends `revised <date>: …` and keeps the old value.
3b. [ ] P3b per-week enterprise $ + project shares in the scorecard (needs a quota.Build for a past epoch).
3c. [ ] P3c child roll-up (owner 10-19 ~17:25): a defect/revoke on an accepted child task lowers the parent
   outcome's week. Red: child revoke shows on the outcome week's scorecard.
4. [ ] P4 parity with review-1016 (tools/agent-cost/review-1016 REPORT §4: $/main call on weekdays, compactions,
   10-10..10-16 personal): store numbers within 2%. Red: the compare fails on a number moved by 3%.
5. [ ] P5 restore-after-expiry test (design §3c): newest backup -> scratch DB, transcripts > 30 d hidden,
   regenerate a matured week, compare with its file; weekly launchd; `health` shows the result. Red: a changed
   scorecard file fails the test.
6. [ ] P6 FINDINGS line (D3 config change): NEED to Crius with the exact diff of the codex + quick-review agent
   files BEFORE applying; ingest `FINDINGS P1=n P2=n P3=n` as evidence kind review bound to the reviewed sha.
7. [ ] P7 first descriptive read on 10-23 next to the review-1016 scripts (after the 10-22 02:00 reset).

## Waiting on

- Crius: review P3-3 ANSWERED 10-19 ~17:25 (OWNER DECISIONS): child defects/revokes roll up to the outcome week.
  Now they show on no scorecard. Also noticed: a parent correction cannot make a child top-level again (defect.go:312).
- Crius answers on ~/.crius/bus/crius.log lines `-> crius:build`.

## Do not redo

- Steps 1-5 are closed by Crius (shas in GOAL). Their checklists and evidence: RESUME-HISTORY.md, STATUS.md.
- Clean builds: a git clone in the scratchpad (a worktree build gets no vcs stamp, Go 1.25.6). GOTOOLCHAIN=local.
- Install: clean clone of the main commit, then bin/crius-install-launchd (or install-bins.sh); check
  `go version -m ~/.crius/bin/crius-ingest` shows vcs.modified=false.
- Bus TYPE rule (Crius 16:58): CLEAR-READY is its own TYPE, `$(date +%FT%H:%M) CLEAR-READY crius:build -> crius:lead —
  text`, never INFO: INFO does not wake Crius (the 15:14 one left the builder idle ~2 h).
- Bus lines under ~240 chars (300 hard limit); format `$(date +%FT%H:%M) TYPE crius:build -> crius:lead — text`.
- P1 binding rules (codex 43067a2): every sha an event names must resolve uniquely to the revision; see design §4
  "record accept as built". Ship script pattern: <scratch f33ce11c>/ship.sh (push, clean clone tests, install).
- Mutation testing: never wrap mut4.py-style scripts in `timeout` (SIGTERM leaves the mutant in the repo).

## Running things

- Worktree <scratchpad 6cfda64f>/wt on feat/work-statusline (= main). No watchers, no background jobs. Ship script:
  <scratchpad 6cfda64f>/ship.sh (branch feat/work-statusline). No scorecard launchd job yet (weekly run: with P5).
- launchd: space.scifilab.crius.{ingest,watchdog,digest,wait-sweep} run ~/.crius/bin (installed from 47e7913;
  previous binaries kept as <scratchpad 6cfda64f>/crius-*.before-47e7913).
- Old worktrees from earlier sessions (git worktree list): not mine to remove; listed for Crius.

## Not in this file

- STATUS.md (project status), docs/MEMORY-AND-MEASUREMENT.md (design), docs/RULES.md, bus ~/.crius/bus/crius.log.
