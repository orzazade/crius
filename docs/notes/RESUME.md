# crius:build — RESUME (contract: docs/notes/COST-LEVER3-FRESH.md §2, max 120 lines)

Updated 2026-10-19 18:42 by session 11838a67 (P6 config applied after owner GO 18:40; FINDINGS ingest next). Last bus line read: 2026-10-19T18:40 GO crius:lead.
Last owner message seen: 2026-10-19 ~17:25 (child roll-up answer, via Crius).
main = origin/main = 3f93098 (P5) + this RESUME commit. Local main ref is behind: it is checked out in Crius's worktree main-rules. History: docs/notes/RESUME-HISTORY.md.

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

## GOAL (2026-10-19 18:42)

Goal: build the Crius record (design §9 steps 1-9) so Crius answers the owner's questions from its own store
- [x] 1 store layout, leak test, decisions, items, bus grammar · 060610f
- [x] 2 crius-ingest v1, backup, watchdog · ccf32e8
- [x] 3 quota sampling + quota --room · e4f35b8, aa38638, f57ddd8
- [x] 4 find/show/health/--deep, redaction, recall eval 31/34 · b3a87cb, bus 2026-10-17T02:27
- [x] 4b fail-closed key=value + gitleaks rules · 86a776a
- [x] 5 digest + Crius daily start (09:00/18:00 goal report) · 23052fe .. 50e2384
- [x] 3b Enterprise fallback: included usage + spend limit (owner 10-19 14:44, Crius NEED 15:06) · 72e9793, 97dc0d1
- [ ] 6 record accept + scorecard (P1-P5, P3c done; P3b, P6, P7 open)
- [ ] 9 Falcon source
Done: 70% (7 of 10 required steps incl. 3b, equal weight; 7 and 8 only if needed)
Next: step 6 P6b: ingest FINDINGS as review evidence (red first), then prove with a real quick-review run
Needs owner: nothing

## NOW

Digest stale-RESUME fix DONE: main de68f7e, VERIFIED by Crius 18:35. ~/.crius/projects.toml crius build lead =
git:~/Projects/scifi/crius#origin/main:docs/notes/RESUME.md. Update this file's GOAL stamp on every write, before the DONE.

P6 config DONE (owner GO via Crius 18:40, bus line): ~/.claude/agents/codex.md + quick-review.md patched exactly with
~/.crius/state/p6-findings.diff (cmp equal); backups *.md.bak-20261019-p6 beside them. Real quick-review of de68f7e
(this session, 18:4x) printed `FINDINGS P1=0 P2=0 P3=1`, but BEFORE its list, not last: parse the line anywhere.

NEXT P6b (the GO also asks: "prove one real quick-review run ends with the FINDINGS line and the store counts it"):
- Nothing parses FINDINGS yet. evidence table (internal/store/db.go:46) takes kind 'review' with no DDL change.
- Source: subagent transcripts (internal/ingest/transcripts.go:244-272 transcriptFile; meta agentType codex or
  quick-review). Take the last line matching ^FINDINGS P1=(\d+) P2=(\d+) P3=(\d+)$ in the agent's final answer.
- Reviewed sha: quick-review `REVIEWED: commit <sha>` line; codex: `--commit <sha>` in its codex-ask Bash command.
  Resolve unique against git_commit (like accept.go bind). --uncommitted / --base / unresolved: revision NULL
  (counted for M10, never bound) - say so in the row's ref, not silently dropped.
- Row: evidence(kind review, ref "<agentType> P1=n P2=n P3=n session=<id>", revision, ts, source_key review:...).
- Red: a fixture subagent file with FINDINGS -> one review row bound to the sha; a list with no FINDINGS -> none,
  and a reviewer answer without the line is counted somewhere visible (health?) - decide, ask Crius if unsure.
- Then codex review, ship (<scratchpad 11838a67>/ship.sh, branch fix/digest-stale), real quick-review run,
  show the store row, DONE.


## Open work (step 6 checklist; each part red first, then build, codex review, one DONE with evidence + commit)

Spec: design §9 row 6, §3c tables (acceptance, defect, correction), §7 (M1, M6, M10, scorecard shape), §7b.
1. [x] P1 (3c86343) `record accept` with revision-bound checks/review (spec in design §4 "record accept as built").
2. [x] P2 (d4d9f61 + 4a102aa) `record defect` / `revoke` / `correct` + guard item + `show ITEM --history`.
3. [x] P3 (61fdb75 + 93957fc) `crius-rec scorecard [--week EPOCH] [--write]` -> state/scorecards/<week>.md.
3b. [ ] P3b per-week enterprise $ + project shares in the scorecard (needs a quota.Build for a past epoch).
3c. [x] P3c (c133717 + 27c2a73) child defect/revoke lowers the parent outcome's week (owner 10-19 ~17:25).
4. [x] P4 (e304c4d) parity with review-1016 REPORT §4: store numbers within 2%.
5. [x] P5 (3f93098) restore-after-expiry test, weekly launchd Thu 04:00, `health` shows it.
6. [~] P6 FINDINGS line: config applied after owner GO 18:40; P6b ingest `FINDINGS P1=n P2=n P3=n` as evidence
   kind review bound to the reviewed sha is open (plan in NOW).
7. [ ] P7 first descriptive read on 10-23 next to the review-1016 scripts (after the 10-22 02:00 reset).

## Waiting on

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

- Worktree <scratchpad 11838a67>/wt on fix/digest-stale (= main). No watchers, no background jobs. Ship script:
  <scratchpad 11838a67>/ship.sh (branch fix/digest-stale).
- launchd: space.scifilab.crius.{ingest,watchdog,digest,weekly,wait-sweep} run ~/.crius/bin (installed from the fix(digest) stale-RESUME commit;
  previous binaries kept as <scratchpad 11838a67>/crius-*.before-<sha>).
- Old worktrees from earlier sessions (git worktree list): not mine to remove; listed for Crius.

## Not in this file

- STATUS.md (project status), docs/MEMORY-AND-MEASUREMENT.md (design), docs/RULES.md, bus ~/.crius/bus/crius.log.
