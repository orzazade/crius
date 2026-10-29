# Learnings: how we work with AI

The owner's log of what we learn about working with AI agents: what we tried, what the data said, what we
changed, and whether it worked. One entry per lesson, newest first. Each lesson names its evidence; the
"Result" line is filled in once the change has run long enough to measure. Crius adds an entry whenever a
day teaches something about working with AI (owner, 2026-10-25).

## Experiments and their measurements

Every change we try is an experiment: one number before, the same number after, measured the same way.
The scorecard is built from Crius's record (record.db: every API call with its tokens and cost) and the bus
(timestamps of every READY, PASS and LANDED), so it needs no one to remember to measure. Baselines are from
10-23 19:08 to 10-25 14:46 (study A) unless noted. Crius reads the scorecard into the 09:00 report.

| # | Experiment (started) | Metric | Baseline | Target | Latest |
|---|---|---|---|---|---|
| E1 | Smaller team: 11 -> 5 sessions (10-25 14:10) | $ per landed item; $ per day; CPU load | $63; ~$1,920/24 h; load 16-27 | under $30; under $800; under 10 | load ~6 (10-25 17:00) |
| E2 | Speed-up: test once, gate when ready, faster gate (10-25 14:35) | owner bug report -> on 7090; READY -> PASS median; gate minutes | 3 h 40 min; 17-19 min; ~85 min (live part 39) | under 1 h; under 10 min; under 25 min | 10-26 09:30: gate 22 min (batch 3, 10-25 23:01) met. Owner bug B-2: reported ~23:56, fix 00:24, on main 03:07 = ~3 h 10 min (waited for the batch gate) missed |
| E3 | Fresh context per item, QA1 (10-25 17:55) | tokens per QA item; bugs found after a QA1 PASS | ~94K; 0 known | under 50K; 0 | 39K, 0 misses (1 item). 10-26: 39K-190K per session; slipped 18:35-22:00 (no resets); QA1 lost its start prompt twice (23:16, 00:52): qa1-reset sent it right after /clear; fixed 01:08 (reset waits and checks, else rc 4) |
| E4 | Compaction window 300K -> 150K (10-25 17:03) | share of calls over 150K context; average context per call | 60-66%; 158-177K | under 20%; under 90K | |
| E5 | Filtered bus waiter for lead, rig, QA3 (10-25 14:55) | wakes with no action per day | 4,058 in 43.6 h (lead 1,574 in 2 days) | under 200 a day | |
| E6 | Guards instead of rules (crius:build, 10-25 18:01) | repeats of a known mistake per day (edit during a test run, typed wrong time) | 4 and 5 (10-25) | 0 | |
| E7 | Owner time: standing rules, no re-sent questions (10-25) | owner questions per day; questions re-sent after an answer | not counted; 4 re-sent (10-25 09:00) | only owner-only questions; 0 | |
| E8 | Runs clean up after themselves (10-25) | worktrees and GB; leftover test quotes on 7090 | 28 and 30 GB (10-25 17:26); 414 | under 10 and 10 GB; under 20 | 10-25 23:35: 42 worktrees, 52 GB (worse); P2 removed 13 at 23:38, disk free 42 -> 69 GB. Test quotes not counted |

## 2026-10-29 (crius:build, item 6: scorecard after a definition change, 9 review fixes)

### 1. A dry run that skips the write path does not prove the write
- **Seen:** the scorecard dry run of the two v2 weeks passed (rc 3, rc 0); 19 commits, 8 review rounds and a full
  suite later, the live `--write` failed rc 1 on both weeks ("a correction event has bad refs"). Only the write
  path explains a changed week, and it read 96 bus CORRECTION lines that share the store's event kind and have no
  refs. No test had a bus line of that kind in the store.
- **Changed:** the explanation reads only events the store wrote (by source key). Before the second push, the
  write ran on a scratch home (`CRIUS_HOME`, a snapshot of the live store): rc 3 and rc 0, then the same live.
- **Lesson:** the live check before a push runs the real command, writes included, on a copy of the live data.
  A shared name for two kinds of record (bus line kind, store event kind) is a trap; a check should keep them apart.
- **Result:** (fill in: next weekly run, Sunday 02:00)

### 2. Reviewing a hand-written text parser never ends; measure it on real data instead
- **Seen:** two free-text parsers (which sha in a FAIL line is the base commit; which background command only waits
  on the bus) took 8 Codex rounds. Rounds 4-7 each found 1-3 new invented spellings ("base main-<sha>",
  "echo Ticket\ #3"), 7 marked P1. None changed a real number: the live digest stayed the same, and 0 of 15,549
  real Quotes background commands changed class after round 5.
- **Changed:** from round 8 the review ask said: P1 only for a shape found in the real bus logs or transcripts,
  an invented one is P2. Round 8 had P1=0 and checked 19,472 real bus lines.
- **Lesson:** give the reviewer the real data and a severity bar tied to it. Where a parser guesses at free text,
  the lasting fix is a fixed field in what is written (a "base:" field on QA lines), not a better guess.
- **Result:** (fill in: review rounds per item on the next parser change)

## 2026-10-29 (crius:build, config-search guard)

### 1. A guard that asks the disk refuses three times less than a guard that asks for a habit
- **Seen:** 10-28 an Explore subagent printed two committed test secrets from RethinkBack config files. The
  excludes were in its prompt; nothing checked them. A hook that refuses every grep -r / rg / git grep without the
  four excludes would have refused 2,108 of 13,515 real Bash and Grep calls from 10-27 to 10-29 (15.6%). Most were
  safe: a grep of one scratch log, a search of a src folder with no config file in it.
- **Changed:** `crius-search-guard` lists the files a search can read (the way each tool reads: every file for
  grep -r, tracked files for git grep, files git does not ignore for rg and the Grep tool, `git ls-tree` for a
  revision) and refuses only when one is a config file, or when it cannot tell. Same 13,515 calls: 660 refused
  (4.9%), the slowest at the 1.5 s listing cap. Two Codex review rounds found 12 silent passes first (cd options,
  wrappers around bash -c, rg -f -, --json after -l, case and brace rules of each tool's globs).
- **Lesson:** a rule that agents must follow on every call costs a round trip per call; check the property that
  matters (can this print a secret?) and refuse only on that. And a guard is only as good as its model of the tool:
  each tool's glob rules (case, braces, which one wins) were where the leaks hid.
- **Result:** (fill in: refusals per day from the hook, and any config leak after 10-29)

## 2026-10-25 (Quotes, two days before the Riga demo)

### 1. A bigger AI team was not faster, only more expensive
- **Seen:** 11 sessions on 14 CPU cores. Load 16-27, every run slow. 10-25: ~$980 spent, 3 items landed.
  Coordination (bus wakes, reads, re-arms) was ~17% of all spend; the lead woke 1,574 times for nothing in 2 days.
  Parallel builders edited the same file (10 items in App.jsx), so one bad item could not be taken out.
- **Changed:** owner cut the team to 5 at 14:10 ("not after the demo, now"). Load fell to ~6.
  Proposed next: 3 sessions, then 2 (one builds, one checks), with scripts doing gates, landing and restarts.
- **Lesson:** the useful minimum is two agents: one that builds and one that checks. Beyond that, add an agent
  only when there is parallel work that does not touch the same files, and the machine has room.
- **Result:** (fill in)

### 2. Work time is not delivery time
- **Seen:** the owner's tenant bug took minutes to fix and 3 h 40 min to reach the app. 60% was waiting for and
  running the batch gate; the same tests ran 3 times (builder, QA, gate).
- **Changed:** speed-up brief (~/.crius/state/quotes/SPEEDUP-BRIEF.md): time every step, test each code
  version once and reuse the record, gate as soon as an item is ready, parallel Python suite, no fixed sleeps.
- **Lesson:** measure where the time goes before speeding anything up; with AI the waiting, not the typing, is slow.
- **Result:** (fill in: time from bug report to app, before 3 h 40 min)

### 3. Context size is the real cost, not the model
- **Seen:** 57% of spend was re-reading old conversation. Two thirds of the money went to calls carrying
  >150K tokens. A compacted session restarts at ~73K, a fresh one at ~31K. Sonnet costs the same as Opus for
  cached context, so switching models saves little. (docs/notes/COST-STUDY-2026-10-25-A.md, -B.md)
- **Changed:** compaction window 300K -> 150K; QA1 trial: fresh context per item, medium effort.
- **Lesson:** give an agent one job, a short handover file, and a clean start for the next job. A long-lived
  agent that "remembers everything" pays for that memory on every single call.
- **Result:** QA1 item 1: 39K tokens vs ~94K before, nothing missed. (more items to come)

### 4. A rule does not stop a repeat; a design does
- **Seen:** builders edited files while their own test run was going, 4 times in one day, despite the rule.
- **Changed:** the owner asked "why is this guard needed?"; that question led to a better fix: tests run on a clean
  copy of the commit, so an edit cannot reach them. No blocking hook needed.
- **Lesson:** when the AI repeats a mistake, change the setup so the mistake cannot happen. Asking "why?" about
  a proposed guard often finds the simpler design.
- **Result:** (fill in)

### 5. Silent failures hide in the plumbing
- **Seen:** Crius sent orders to a session that no longer existed (after a restart) and nothing complained;
  helpers died at each reboot unnoticed; agents (Crius too) typed guessed times into messages.
- **Changed:** 6 guard fixes ordered (crius:build): refuse wrong times, warn on orders to dead sessions,
  after-boot helper check, READY only with a test record, no re-sent settled questions, no repeated alarms.
- **Lesson:** an AI's "sent" or "done" is a claim. The system needs checks that shout when something did not happen.
- **Result:** (fill in)

### 6. The owner's time is the scarcest resource
- **Seen:** Crius re-sent 4 questions the owner had already answered; a "what remains" answer was too vague
  and had to be asked twice; one decision (match Rethink) took 33 min of back-and-forth.
- **Changed:** standing rule "behave like Rethink unless the owner says otherwise"; concrete numbered lists
  of what remains; check before re-asking.
- **Lesson:** turn each owner decision into a standing rule the agents apply themselves. Ask only what only the
  owner can answer, once, in plain words.
- **Result:** (fill in)

### 7. Repeating the demo walkthroughs during the build wasted time
- **Changed:** owner 14:58: no demo walkthroughs while building; finish all tasks, then walk the demo together.
- **Lesson:** the AI checks the parts; the human and the AI rehearse the whole thing together at the end.

### 8. AI tests leave mess behind
- **Seen:** 414 leftover test quotes on the demo app; 1.8 GB worktrees never removed (disk fell to 0.1 GB, then 28 GB).
- **Changed:** cleanup ordered; the clean copy per test run must delete itself; worktree count in the hourly line.
- **Lesson:** every automated run cleans up after itself, and the cleanup is tested like any other code.

## 2026-10-29 (crius:build, item A, cmux restore): a cause read from code went to the lead before a replay tested it
- I read cmux's claude wrapper and reported that it drops CLAUDE_CONFIG_DIR on a restore. crius:lead built the fix on
  that (start claude with cmux's preserve flag). A replay of cmux's own restore command on one test tab took 2 minutes
  and showed the opposite: without the flag it resumed on the right account. The flag was a no-op; the claim had sat
  on the bus for 30 minutes and cost one design round.
- Rule I follow now: a cause that names another program's behaviour gets one reproduction before it goes to the lead.
  A test with an empty session proves nothing: claude writes a transcript only after the first message.
