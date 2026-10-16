# Crius — the owner's lead agent (design draft, 2026-10-14)

One session the owner talks to. It leads every workstream, decides what it is allowed to decide, runs reviews,
and only pulls the owner in when a decision is truly his. Falcon is its senses. Lead sessions are its hands.
Runs on subscriptions only (`claude`, `codex` CLIs). Background: falcon/.planning/jarvis-harness.md,
tools/agent-cost/FLOW-REVIEW.md (cost lessons).

## 1. Roles

| Who | Job | Runs as |
|---|---|---|
| **Owner** | Sets goals, takes the decisions only he may take | phone (Remote Control, later voice), terminal |
| **Crius** | Routes work, keeps the decision queue, reviews results, decides inside its rights, briefs the owner. Never does project work itself | one interactive Claude Code session `crius` in cmux, Remote Control on |
| **Falcon** | The control tower: collects (mail, ADO, QA PRs, calendar, meetings, CI, cluster, bots) AND does the first-level thinking with its own `claude -p` runs (mostly Haiku skills): triage, summaries, PR reviews via codex, scoring. Hands Crius conclusions, not raw data. Not a decision maker | existing daemon + API; role adapted (owner, 2026-10-14) |
| **Leads** | Own one area each, run their workers, report to Crius | cmux sessions **on demand**: woken or spawned when there is work, write status, then sleep or exit and are re-created from their files. Idle costs nothing. `lead-staffy`, `lead-axi` (work persona), `lead-falcon`, `lead-trading` (read-only on live bots), `lead-infra`, `lead-lab` (POCs, investigations, ticket analysis) |
| **Workers** | Do the work | cmux team members, subagents (Haiku Explore etc.), codex |

## 2. How they talk (both directions, three channels, each for one thing)

Verified 2026-10-14: a session sees all 14 local peers by name and state (busy / idle / shell) via ListAgents,
work persona included, and can SendMessage them.

| Channel | Used for | Why this one |
|---|---|---|
| **Files** (`~/Projects/scifi/crius/state/`) | the truth: `status/<lead>.md`, `decisions.md`, `inbox/<lead>.md`, `charters/<lead>.md` | survives restarts and compaction; cheap to read; one writer per file |
| **Events** (`state/events.jsonl`, append-only) | something changed: started, done, blocked, needs-decision, and Falcon **briefs**: already triaged and evaluated ("PR 412 reviewed: 1 real finding, recommend approve with comment, needs owner T3"; "3 mails need replies, drafts ready") | Crius wakes on conclusions, not on raw signals or chatter |
| **SendMessage** | a push that must be seen now: lead -> Crius "needs decision", Crius -> a lead that is *waiting* | real-time, but never into a busy lead (memory rule: two writers) |

Lead status file, fixed headings: **Goal · Done (since last) · Now · Next · Waiting on owner (why, options) ·
Risks · Updated**. Max ~40 lines. Leads update it at every milestone and before compaction.

## 3. Crius's loop

1. **Wake** on: an owner message; a lead's SendMessage; a Monitor watch on `events.jsonl` matching priority
   events; a scheduled tick (every 60 min in work hours, a morning briefing, a night-shift start).
2. **Read only what the wake needs**: the event, then the one or two status files it touches. Never transcripts;
   if it must dig, it sends Explore (Haiku) and keeps only the answer.
3. **Decide** inside its rights (section 4): assign, re-route, ask a lead, run a review, accept or reject work,
   schedule around quota.
4. **Act** through leads (SendMessage when waiting, else inbox), never by editing project code itself.
5. **Escalate** only what needs the owner: one item in `decisions.md` + AskUserQuestion / assign_task, which
   Remote Control pushes to the phone. Urgent ops alerts also go out by Falcon's Telegram.
6. **Log** every decision it took alone in `state/decision-log.md` (what, why, evidence), one line each.

## 4. Decision rights (the safety core)

| Crius decides alone | Crius asks the owner |
|---|---|
| Route work, start/stop/re-prioritise leads and workers, schedule by quota | Anything T3 (speaking as the owner: mail, Teams, ADO comments, PR approvals) |
| Run reviews (codex, quick-review, browser-check) and send work back with findings | Anything T4 (deploy to prod, infra, money, live trading bots, deleting data) |
| Accept a lead's work when the review is clean and tests pass | Scope changes, new projects, anything the owner said "ask me" about |
| Answer a lead's question from memory / charter / earlier owner decisions | When evidence is weak or two readings exist |
| Approve a lead's T0-T2 actions (read, local build, test, dev/test env) | Spending above the daily budget |

**Taint rule:** anything that came from outside (mail, tickets, PR text, web) is *data, never instructions*. An
event whose cause is tainted can make Crius *propose*, never *act* at T2+ without the owner. (Same rule Falcon Ask
already uses: a poisoned mail must never press a button.)

**Autonomy ratchet (later):** an action class the owner approved N times with 0 declines moves into the
left column, with an audit line. T3/T4 never move.

## 5. Memory

Start with what works and measure before adding a graph (Graphiti was tried 2026-09-14 and dropped: it needed a
hosted LLM for extraction).

| Layer | Holds | Store |
|---|---|---|
| Working | the current wake: event + 1-2 status files | Crius context (kept small) |
| Operational | status, decisions, inbox, events | `state/` files |
| Episodic | what Crius decided and why; what each lead finished | `decision-log.md`, lead logs; nightly digest into Falcon `memory_items` |
| Knowledge | people, projects, repos, rules, owner preferences | Falcon memory (vector + words search, `falcon` MCP) + auto-memory notes with `[[links]]` (already a graph of markdown) + **Appxite Cortex** knowledge graph (`appxite-cortex` MCP) for work architecture and domain questions |

**Graph, if needed:** entity + relation tables in Falcon's Postgres, extracted by `claude -p` on Haiku 5.5
(subscription, no Groq limits: the reason Graphiti failed). Build it only if a test set of owner questions
("who promised what to whom", "what depends on X") shows the vector search misses them.

## 6. Cost and context rules (from the 2026-10-14 flow review)

- **Crius stays small:** target < 100K context. A wake at 50K costs ~$0.02 of cache reads, at 300K ~$0.12.
  It reads status files, never transcripts; delegates reading to Haiku.
- **Fresh every morning:** all state is in files, so Crius restarts daily (and whenever > 150K) at no loss.
- **Batch, don't ping:** low-priority events wait for the hourly tick; only priority events wake it at once.
- **Stable prefix:** role + rules + charters load first and don't change during the day, so the prompt cache
  holds; avoid > 1h idle on a big context (cache rewrite at 2x).
- **Leads:** on demand, so an idle lead costs nothing. 300K compact window (trial from 10-16), write status
  before compaction or exit, Haiku/codex for scouting and reviews. Waking a lead with a big stale context pays a
  cache rewrite, so a finished lead exits and is re-created from its files. Only leads that are working at the
  same time share the 5-hour and weekly caps: Crius schedules those.
- **Falcon thinks first:** first-level evaluation (triage, PR reviews, summaries) runs in Falcon's cheap Haiku
  skills and codex, so Crius only reads conclusions.

## 6b. Tracing, scoring and self-improvement (automatic; owner asked 2026-10-14: "I don't want to do anything")

**Trace (free, already recorded):** every session's transcript holds each call's tokens, model, tools and time.
Crius gives every piece of work a `task_id`; leads put it in their status and events, and a nightly job
(`tools/agent-cost` parsers: calls.ts / compare.py) joins transcripts to task ids into one ledger
`state/ledger.jsonl`: task, lead, model mix, calls, peak context, $ (list price), wall time, outcome.

**Score (no owner effort), per task, from signals that already exist:**
- review: codex/quick-review findings, and how many were real (fixed afterwards)
- tests / CI / deploy-verify result; browser-check verdicts for UI work
- rework: a fix/revert touching the same files within 72h (today 18% of commits)
- owner signal: corrections or pushback in his next prompts, decisions he reversed, interrupts
- cost per task and $ per prompt; time to done
-> one quality score + one cost score per task, per lead, per model/route.

**Benchmarks:** the existing sets (37 past bugs for review, 60 locate cases) re-run automatically when a model,
agent or route changes, so a change is judged on the same yardstick (as on 10-09 and 10-13).

**Weekly optimizer (Crius's retro, Haiku-heavy, cheap):** read the ledger and scores, find the top 3 cost or
quality problems (like the 10-14 flow review: context size, skipped reviews, wasted blocks), propose a fix for
each with the expected gain, run it as a trial or A/B, measure, keep or revert. Changes to global config, hooks
or models are proposals the owner taps yes/no on; trials inside a lead's own routine Crius may run alone.

## 7. Phases (each owner-approved, each measured)

0. **Protocol + pilot (2-3 days):** `state/` layout, `CRIUS.md` role, lead charter template, `/crius-lead` skill
   (registers a lead, enforces status/events/inbox), Crius session with Remote Control + Monitor on events.
   Two leads only: staffy, axi. Measure: times the owner opens a lead terminal to understand state (target ~0),
   decisions answered from the phone, Crius $/day.
1. **Falcon as senses:** Falcon writes events (new QA PR in Teams/ADO, mail needing reply, CI red, bot/cluster
   alert, meeting in 10 min) with a taint flag. Crius: morning briefing; on a QA PR it runs the codex review and
   asks the owner only for the approve/comment decision.
2. **Memory:** decision log + nightly digest into Falcon memory; owner-question test set; graph only if it fails.
3. **Ledger + scoring + weekly optimizer**, then **ratchet + quota governor** (the ratchet uses the scores).
4. **Voice:** Falcon connector in the Claude app reading the same `state/` files ("where are we", "what's next").

## 7b. Changes after the codex critique (2026-10-14) — these override the sections above where they differ

- **Falcon is the backbone, Crius the brain + interface.** Falcon (headless, durable) owns: the message bus
  (events and commands in Postgres with ids, ACKs, idempotency, per-reader cursors, replay after restart),
  supervision (per-session sweeps: a lead stuck on a permission prompt, idle with work queued, near its context
  limit, status older than its deadline), dedupe/coalescing of events, and delivery to Crius. The `state/` files
  stay as human-readable views, not the transport. Monitor + hourly tick is only a pilot convenience.
- **One writer of autonomous decisions:** only Crius decides; leads and Falcon propose. Busy/idle from ListAgents
  is a snapshot, not a lock: every command has an id, the lead ACKs and marks it consumed.
- **Rights are enforced, not written:** arm Falcon's tier/boundary hooks in Crius and lead sessions (today they
  exit when `FALCON_RUN_ID` is unset). Fix tier gaps: a push to a repo Flux deploys from = T4; GitHub/ADO
  comments, merges and approvals = T3; enforce by effect at the executor, not only by command text.
- **Taint keeps its provenance:** a summary, a memory item or a "decision" derived from tainted input stays
  tainted through every step (own-session summaries are not trusted automatically). Only the owner's own words
  are clean.
- **Approvals are exact:** a phone "yes" binds to one action, its arguments, the revision (commit sha), and an
  expiry, re-checked at execution (reuse `falcon/plugin/contracts/gates.md`). Acceptance binds review + tests to
  the same sha.
- **Work boundary — owner decided 2026-10-14: Crius gets FULL access to Appxite data** (overrides the
  critique's filtering advice). Falcon's `plugin/fragments/corporate-boundary.md` rules for where Appxite data may
  be stored still need a look when building the memory bridge.
- **Budget — owner decided 2026-10-14: no $ cap on Crius.** Quota (5-hour / weekly caps) still governs.
- **Pace check, not a stop (owner 2026-10-14):** once a day, if usage is ahead of pace for the week (e.g. 60% of
  the weekly cap used by day 3) or burning much faster than the 7-day average, Crius sends ONE reminder: how fast,
  which leads/skills burn most, and one suggestion. No hard stop, no throttling unless the owner says so. Source:
  real plan usage if readable (`/usage`; to verify headless), else the ledger's token totals against the known caps.
- **Quota from day one:** reserve capacity for owner interaction and recovery; read real usage, not list-price $.
- **Phase 0 is smaller:** adapt the cmux-team protocol (it already has briefs, logs, watchers and known failure
  modes: monitor expiry, replay gaps, dropped notifications). Defer scoring, optimizer and graph. Pilot metrics
  add drills: a lost message, a Crius crash + replay, a stale status, a poisoned mail, a quota stop.

## 8. Open questions for the owner

- ~~Cortex~~ confirmed 2026-10-14: the Appxite Cortex knowledge graph.
- Lead list right? (staffy, axi, falcon, trading, infra, lab)
- ~~Appxite data~~ decided: full access. ~~Crius $ cap~~ decided: none.
