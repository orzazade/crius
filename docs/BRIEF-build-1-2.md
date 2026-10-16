# Brief — build rollout steps 1 and 2 (session "crius: build")

You build steps 1 and 2 of `docs/MEMORY-AND-MEASUREMENT.md` §9 in ~/Projects/scifi/crius. The owner said "go"
on 2026-10-16. The main Crius session (cmux tab "crius") supervises you; report to it through
`~/Projects/scifi/crius/bus/build.log` (create it; one line per event, `HH:MM TYPE crius:build -> crius — text`,
TYPE = DONE | BLOCKED | NEED-OWNER; time from `$(date +%H:%M)`; append with `>>` only).

## Read first
docs/MEMORY-AND-MEASUREMENT.md (the whole design; §3 store, §5 ingestion, §7b corrections, §9 rollout, §10
decisions D1-D4), STATUS.md, research/*.md. Reuse ~/Projects/scifi/tools/agent-cost/review-1016/scratch/cost/extract.py
logic (max usage over streamed records, requestId dedupe, skip -private-tmp dirs) — copy what you need into this repo.

## Scope
Step 1 and step 2 exactly as §9 says, each with its tests, red first (each new test must fail before the code).
Step 1 includes the cmux-team template change (dated bus grammar + provenance tag): edit
~/.claude/skills/cmux-team/ files minimally. Step 2's launchd job and local watchdog are approved config changes
(owner, 2026-10-16, Q3) — install them, and print exactly what you installed. No other settings, hooks or
statusline changes (that is step 3).

## Hard rules
- All private state in ~/.crius/ (outside every git tree). The repo holds code, tests, fixtures and docs only.
  Fixtures are synthetic — never real transcript text, never Appxite data, never secrets.
- Never print secrets; output named fields only. Never touch Appxite production or the Appxite enterprise account.
- Disk is tight: no big copies of transcripts; temp files in your scratchpad; clean up your own leftovers.
- Commit by path (`git commit -m "…" -- <paths>`), no AI attribution. Do NOT push (local only for now).
- Before calling a step done: run its tests, then the codex agent deep review on the diff; fix what holds up.
- Plain simple English; they/them for people. End each step with a DONE line on the bus with evidence
  (test command + counts, commit sha). Then stop and wait; do not start step 3.
