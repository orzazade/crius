# Crius rules — done, bugs, reports

One page for every lead, builder and Crius. Briefs link here instead of copying it. Details live in the linked
sections; if a detail and this page disagree, this page wins and the detail gets fixed.

## 1. Definition of done

A piece of work is done only when all of these hold:

1. **Red first.** A test that fails before the change and passes after it. For a bug, the test shows the bug.
2. **Reviewed.** A deep review (the `codex` agent; Opus when Codex is out) found no open P1. P2s are fixed or listed.
   After the third review round on one item, only a P1 keeps it open; P2/P3 become follow-up rows (owner, 2026-10-20).
   Small items (no money, auth or data code; under ~50 changed lines) may be reviewed and fully gated per batch: items merge
   into an integration branch after their own tests and a quick QA check, and one batch gate (full suites, demo sweep, one
   deep review of the batch diff) runs every 6-8 items or 3 hours; main moves to the gated sha (owner, 2026-10-25).
3. **Proof in the DONE line.** The bus line names the item, the commit sha, the test command and its counts.
   A DONE without that is a claim, not done.
4. **Checked by someone else.** QA, or Crius, re-runs the check on a clean checkout of that sha with real-shaped
   data before it is reported to the owner. A builder's own word never closes an item. With batch gates, the team's QA
   batch gate is that check and the lead merges on it; Crius does not re-run it (owner, 2026-10-25).
5. **Live where it is meant to work.** Installed or deployed and seen working there (one live check), not only in tests.
6. **Owner OK before anything outward-facing:** production, a public endpoint, messages as the owner, anything
   people outside the team see. Pushes: the owner's personal projects (scifi, aitch, …) push to main after the
   checks above, no OK needed; Appxite projects go through a PR branch, never straight to main. Test environments
   (env004, local, our own scifilab test deploys) follow the team's own rules in its TEAM.md; no owner OK per push.

Known gaps that were accepted are written down next to the work (a KNOWN-GAPS list) and do not block done.

Details: STATUS.md (rule for every step), [docs/BRIEF-build-1-2.md](BRIEF-build-1-2.md),
`~/.claude/skills/cmux-team/SKILL.md` §4 (verify before relaying), personas/qa-lead.md.

## 2. Bugs and mistakes

1. **Find the root cause before fixing.** Name the property that broke, not the symptom. A fix counts only if it
   also covers cases not seen yet; if a new variant would bring the bug back, it is a workaround: say so and open
   a tracked item for the proper fix.
2. **Red first.** The fix comes with a test that fails on the old code (see 1.1).
3. **Fix forward.** Never rewrite history. A bug found after acceptance withdraws that acceptance and opens a
   linked fix item.
4. **Severity decides who hears.** S1 = others saw it, or production or money is involved: tell the owner now.
   S2/S3: fix and report in the next summary.
5. **One cause per mistake:** missing test, wrong assumption, stale context, silent failure, misread request,
   harness or tool, review gap, bad data.
6. **Same cause twice = stop chasing.** The second mistake of one cause opens a guard: a mechanical check (test,
   hook, health check) that goes red on both past cases. Guards that change config or hooks need the owner's OK.
   Example: six redaction leaks of one cause led to step 4b (a maintained ruleset), not a seventh patch.
7. **"Found nothing" and "failed to look" are different states,** and the failure is loud. Silent success is the
   most expensive bug.
8. **Correct yourself in public.** Post `CORRECTED` on the bus and tell the owner in plain words. Finding your
   own mistake is never punished; mistakes found by us are counted apart from ones that escaped.

Details: [docs/MEMORY-AND-MEASUREMENT.md](MEMORY-AND-MEASUREMENT.md) §7b, `~/.claude/skills/cmux-team/reference/lessons.md`,
`~/.agents/AGENTS.md` (Diagnose repeats, Solve the root cause, Claim completion only when verified).

## 3. Goal and reports

1. Every lead keeps a **GOAL block at the top of its status file**, kept current per milestone, in exactly this
   form (Crius's digest reads it; anything else is reported as unreadable):
   ```
   ## GOAL (YYYY-MM-DD HH:MM)
   Goal: <the end goal in one line>
   - [x] <milestone> · <refs: sha, run id, bus line>
   - [~] <milestone in progress> · <refs>
   - [ ] <milestone not started>
   Done: N% (<how it was counted>)
   Next: <next milestone>
   Needs owner: <what, or "nothing">
   ```
   A lead that counts rows instead of milestones writes the Done line as
   `Done: N% (rows: X of Y in <row file>; <command that counts them>)`. That is the only "X of Y" on the line.
   `<row file>` is absolute, `~/…`, or relative to the folder of the status file.
   Each row has a stable id: one token with at least one letter and one digit (F-118, O1). A VERIFIED line for a row writes `row <id>` as its
   `<ref>` (e.g. `row F-118`); the digest counts distinct row ids from those lines and never parses the row file.
2. A **report** to the owner is: goal, % done, what is done, what remains. Not a ticket log.
3. A lead that cannot name its goal asks the owner (NEED-OWNER). A lead with no goal is lost.
4. **Crius tracks; the owner does not have to ask.** Crius tells the owner at once when a decision is needed, a
   milestone is verified done, a goal % changes, or an S1 happens. Twice a day (09:00 and 18:00) Crius sends one
   goal report: per lead the goal, % done, change since the last report, next milestone, and what needs the owner.
   Leads keep their GOAL block current; Crius reads it and checks every claimed milestone before the % goes up.
   A check counts only as one bus line per milestone:
   `VERIFIED crius:lead -> <project:role> — <ref> · <evidence>`, where `<ref>` is a sha, the milestone's own ref
   (e.g. F-126) or its exact text. The lead's QA may post these too. Prose like "checked PASS" does not count.
   bus-post checks the sha the line checked against the named run log in ~/.crius/runs: the one sha the ref names, else
   the first word after it (`row F-118 · <sha> · …`); other shas and numbers after the ref (a baseline, a CI run id)
   are free of it.
5. **A lead never stops while its goal has work it can do.** A session cannot continue after its turn ends, so
   "Next I'll do X" followed by ✅ is an idle lead. End a turn only on ❓ (owner needed, with the question) or ⏳
   (own background work running, named). ✅ is only for a finished goal. Crius's bus watch flags a lead that stops
   on ✅ (IDLE) and sends it back to its next milestone.
