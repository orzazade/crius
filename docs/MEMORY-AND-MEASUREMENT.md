# Crius — memory and measurement (design, 2026-10-16, rev 2 after the Codex critique)

Design only. No code, no config changes. It answers the owner's question: how Crius remembers and finds what
happened, fast and reliably; whether Claude Code memory is enough; what standard teams use to report; and how we
measure output, cost and waits. Inputs: `research/memory-and-measurement.md`, `research/claude-code-telemetry.md`,
`docs/DESIGN.md` (7b overrides), `STATUS.md`, cmux-team skill §6, `tools/agent-cost/review-1016/REPORT.md`.
Marks: **FACT** = checked in a file or by a test today. **ESTIMATE** = my reasoning, to be checked.
Rev 2 applies all 12 Codex findings (verdict "ship after fixes"); §10 lists what I decided without the owner.

---

## 0. Recommendation in 10 lines

1. Claude Code memory is enough for *how to behave* (preferences, rules). It is not a record of *what happened*.
2. Truth comes from what already exists: transcripts, git, CI, bus logs, team logs, decision files, quota samples.
3. One local SQLite store in `~/.crius/state/`, outside every git checkout. It has two parts: **durable records**
   (compact and content-free: requests, sessions, waits, quota, acceptance, evidence) that are backed up, and
   **derived views** (search index, rollups, STATE.md) that can be rebuilt.
4. Numbers come from ground truth. Agent reports are "claims" until a check or an independent review confirms them.
5. Five commands are the only door: `find`, `show`, `record`, `quota`, `health`. The home behind them can change.
6. Leads write by hand only bus lines, four fields in `LEAD.md`, and decisions. Everything else is derived.
7. Headline numbers, weekly: accepted owner outcomes (code / research / Falcon apart), observed quota used, and time
   the owner kept agents waiting. Quality numbers are candidates until a defect is confirmed.
8. Every answer says `complete`, `partial`, `unavailable` or `expired`, names what it searched, and fails loudly.
9. Crius starts fresh each day from a script-built digest (zero model tokens), not from raw data.
10. Nothing leaves the Mac except allowlisted metadata, unless there is an operational need AND the owner's explicit
    permission. No graph DB, no memory product, no dashboard, no OTel until a need is shown.

---

## 1. Requirements, scored

Weight 1-5 = how much it decides the design. Each target is checked by a rollout test (§9).

| Requirement | Weight | Target | How we check |
|---|---|---|---|
| **Quality** (ground truth) | 5 | every scorecard number traces to durable records; claims shown as claims | 3 random accepted outcomes opened each week |
| **Robustness** (loud failure) | 5 | a silent source or a dead ingest is flagged within 30 min by a check that does not depend on the ingest | drills (§9) |
| **Precision** (right fact) | 4 | recall test ≥ 90% right in top 5; exact IDs 100%; negative answers name their scope | 30-question set, weekly |
| **Owner effort** | 4 | 0 routine minutes a day; ≤ 1 reminder a day; ≤ 5 min a week for the scorecard | owner prompts spent on "where are we" |
| **Cost** (quota) | 3 | ingest, digest, scorecard: 0 model tokens; Crius ≤ 5% of weekly quota; helpers on Haiku | measured from its own sessions |
| **Speed** | 2 | `find`/`show` ≤ 2 s; a helper answer ≤ 60 s | recall test timing |

Cases the design must survive: a lead forgets to report (derived from git and team logs, flagged "lead silent"); a
session crashes (transcript stays; session closed at its last record, end reason `unknown`); a hook fails silently
(expected-event check, like the 10-09 `check-secrets.sh` exit 127); the ingest dies or the Mac sleeps (independent
heartbeat check, §5); evidence expires (answers say `expired`, not "not found").

---

## 2. Information kinds and their single home

| Kind | Single home | Who writes | Who reads | Format | Retention | Size |
|---|---|---|---|---|---|---|
| **Owner preferences, rules** | auto-memory (`MEMORY.md` + topic files) and CLAUDE.md, as today | Crius, after the owner confirms | every session at start | markdown | forever, pruned | 200 lines / 25 KB index cap (FACT) |
| **Project state** | `~/.crius/state/<project>/STATE.md`, generated; hand part in `LEAD.md` | script; lead (4 fields) | Crius, lead at start | markdown with freshness stamp | current only | ≤ 40 lines |
| **Decisions** | `~/.crius/state/<project>/DECISIONS.md`, append-only, `<project>-D<n>` with `Supersedes:` | lead, Crius, owner via Crius | Crius before deciding; leads at start | 5-8 line entry | forever, backed up | ~1 KB each |
| **Events / timeline** | `event` rows (durable) | ingest; `record` | `find`, digest, helpers | rows, ≤ 300-char filtered text | done/decision/accept/defect forever; fine events 90 d | ESTIMATE 50-150 MB/yr |
| **Metrics** | durable `request`, `session`, `wait`, `quota`, `acceptance` rows; weekly files | ingest; scorecard script | Crius, owner | rows + markdown | requests 90 d then daily rollup forever | ESTIMATE ≤ 250 MB/yr |
| **Artifacts** | where written today (repos, `research/`, `docs/`) | authors | anyone | markdown | as the repo keeps it | pointer row: path, sha256, title, headings, taint |

Why not auto-memory for the timeline: 200-line cap, loaded into every session, not loaded by subagents, no dates or
supersession, written by agents (self-report), no query. Nothing here writes events into it.

Existing pieces and how each is used:

| Piece | Use | Limit |
|---|---|---|
| Transcripts (`~/.claude/projects` = `~/.claude-personal/projects`, 4.4 GB; `~/.claude-work/projects`, 94 MB; FACT) | main source of requests, sessions, prompts (human vs peer), compactions, commits made, AskUserQuestion, hook errors | deleted after 30 days (default `cleanupPeriodDays`; oldest file 09-16; FACT). **Decided: keep 30 days** (disk); durable records carry what we need |
| `tools/agent-cost` `extract.py` (1,575 files, 159K requests, 20 s, no prompt or tool text; FACT) | core of `crius-ingest`: same fields, same max-usage rule per request, made incremental (§5) | full run writes 65 MB JSONL; the store keeps rows instead |
| context-mode FTS5 (`~/.claude/context-mode`, 841 MB; FACT) | stays the in-session recall tool | not the record: per-project DBs, private schema, raw tool output inside |
| Auto-memory | preferences, pointers | caps, no time structure |
| Statusline `rate_limits` | the only source of real quota (FACT); first instrument after ingest (§5) | work persona has a statusline; personal has none (FACT) |
| Hooks | later, only for live waits (`find --waiting`) | a recorder, never a gate |
| OTel (works on the Max login, needs a collector; FACT) | deferred until permission-wait precision or hook diagnostics justify it | if added: allowlisted fields only |
| Falcon (`falcon-crius` MCP: 18 reads + 14 actions) | a source: Falcon run outcomes and cost (k3s runs' transcripts are not on the Mac); receives the heartbeat | not a home for private rows without the two conditions in §3 |
| cmux-team bus `crius/bus/<project>.log` (exists, empty, gitignored; FACT) | the leads' channel to Crius, strict grammar (§6) | moves to `~/.crius/bus/` (§3b) |
| Explore / quick-review (Haiku), codex | Explore runs `find` and the bounded transcript search for Crius; reviews are acceptance evidence | no new agent type |
| Telegram / Discord | alerts, pace reminder fallback | redacted text only |

---

## 3. The store

### 3a. Three options compared (1 = poor, 5 = best)

| | Files only (JSONL + md + grep) | **SQLite on the Mac** | Falcon Postgres on k3s |
|---|---|---|---|
| Precision | 3: IDs fine, joins need scripts | 5: IDs, FTS5, joins | 5, plus vectors |
| Speed | 3 | 5: local, ms | 4: network; hub CPU pinned at 500m on 10-15 (FACT) |
| Cost | 5 | 5 | 4: cluster load, owner OK per deploy |
| Quality | 4: no unique keys, double counts | 5: keys and constraints | 5 |
| Owner effort | 5 | 5 | 3: tokens, SOPS secret, deploys |
| Robustness | 3 | 4: one machine; backups + outside heartbeat | 3 today: Falcon bus not built |
| Data boundary | 5 | 5: stays on the Mac | needs the owner's explicit permission |
| Phone / voice reach | 2 | 3: through Crius (Remote Control) | 5 |
| Weighted (§1 weights; last two not weighted) | 3.8 | **4.8** | 4.0 |

**Pick: SQLite on the Mac.** Sources are on the Mac, so the extractor runs there anyway.

**Move to Falcon Postgres only when BOTH hold:** (1) an operational need, such as the voice connector answering
without a Crius session, or the Falcon bus stable for 2 weeks and needing these rows; and (2) the owner's explicit
permission for that data on k3s (Q2). Until then only **allowlisted metadata** leaves the Mac: heartbeat time,
per-source status and counts, quota percentages, outcome counts. No text, paths, titles or Appxite names.
**Path:** same tables in Postgres; one more backend in `crius-rec` (deferred, not built now); the ingest can become a
job of Falcon's Mac daemon. Callers do not change.

### 3b. File layout

```
~/Projects/scifi/crius/              git checkout; remote orzazade/crius (today's commits not pushed, FACT)
  docs/, scripts, projects.example.toml      no private data, ever
~/.crius/                            NOT a git work tree, mode 700, excluded from cloud sync
  projects.toml                      slug, persona, account, cwd prefixes, repos, team log paths
  bus/<project>.log                  leads append here (cmux-team §6 path changes to this)
  state/record.db                    SQLite, WAL, one writer (ingest + record via flock)
  state/<project>/LEAD.md, STATE.md, DECISIONS.md
  state/digest/YYYY-MM-DD.md         30 days
  state/scorecards/<quota-week>.md   forever
  state/eval/recall.jsonl            recall test set
  spool/quota.jsonl                  statusline samples, 90 days
  spool/hooks-YYYY-MM-DD.jsonl       later, 14 days
  backup/                            nightly SQLite backup, 7 daily + 8 weekly
```

**Git-leak test before any ingest run** (ingest refuses to start if it fails): `~/.crius` is not inside any git work
tree; no tracked file in the crius checkout matches `*.db`, `state/`, `bus/*.log`, `spool/`; a scan of tracked files
finds no `<project>-D<n>` or `<project>#<n>` id that exists only in private files.

### 3c. Tables

**Durable records** (facts that cannot be rebuilt after transcripts expire at 30 d and hook logs at 14 d; backed up):

| Table | Key fields |
|---|---|
| `request` | request_id (PK), session, ts_first, ts_last, model, query_source, tok_in, tok_out (max over streamed records), tok_cache_read, tok_cache_write, cost_list_usd, transcript_path, line_uuid. 90 days, then rolled into `usage_day` |
| `usage_day` | session, day, model, query_source, calls, tokens by type, cost (forever) |
| `session` | id, account, persona, project, role, parent_id, item, each with `attrib_source` (`registered`, `cwd`, `unknown`), title, model_main, started, ended, end_reason, peak_ctx, compactions, human_prompts, peer_prompts, interrupts, hook_blocks, hook_errors, taint |
| `assignment` | session, item, role, from_ts, to_ts, source (launch or task switch) |
| `wait` | wait_id, session, kind, request_ref (notification or tool_use_id), opened, resolution_ref, resolved, resolved_by (`human/peer/timeout/none`), state `open/resolved` |
| `quota` | ts, account, epoch (`resets_at`), five_hour_pct, seven_day_pct, sample_kind `change/heartbeat` |
| `item` | id `<project>#<n>`, parent (owner outcome), kind `code/research/falcon/other`, title, criteria (set at open), opened, state, taint |
| `acceptance` | item, revision (sha or artifact sha256), checks (refs + results), reviewer + review_ref, accepted_by, ts, verdict |
| `evidence` | id, kind (`commit/ci/gate/review/artifact/bus`), ref, revision, ts, source_key |
| `defect` | id, item, found_by, signal refs (revert, BUG line, owner report), incident id (dedupe), confirmed (bool) |
| `event` | id, ts UTC, project, kind, actor, session, item, refs JSON, text (≤ 300, filtered), taint, verified, source_key UNIQUE |
| `decision` | id, project, ts, title, decided_by, supersedes, file, line, session, taint |
| `commit` | sha, repo, project, ts, session (from the transcript's `[branch sha]` result), files, is_revert, reverts_sha, ci |
| `source_run`, `coverage` | per run and per resource: status, cursor, rows, expected vs seen |

**Derived views** (rebuildable from durable rows + files): `event_fts`, `doc_fts` (FTS5 over event text, decisions,
artifact titles and headings), daily and weekly rollups, `STATE.md`, digest, scorecard.

**Backup and restore.** Nightly `sqlite3 .backup` into `~/.crius/backup/` (Time Machine covers it if on). A
**restore-after-expiry test** runs weekly: restore the newest backup into a scratch DB, remove access to transcripts
older than 30 days, regenerate a matured week's scorecard, and compare it with the frozen file. Any difference fails.

**Disk budget:** `~/.crius` ≤ 1 GB. Nightly prune of fine events and `request` rows after 90 days, after rollup.

**Links.** Item = `<project>#<n>`, child items point to their owner outcome. Decision = `<project>-D<n>`; team rulings
stay `B<n>` and are indexed as `<project>-B<n>` by pointer. Event -> decision -> commit -> session goes by refs in bus
lines, decision entries and transcript commit results. No trailers in commits (Appxite colleagues see nothing new).
Refs are validated (a sha must exist); a bad ref is kept and flagged.

**No secrets, no bodies.** Text enters only from short fields written for the record (bus lines, decision entries,
titles, headings). Prompt text, tool output and assistant text are never stored: only lengths, counts, timestamps,
pointers, and allowlisted tags (§3d). **Every output sink is redacted** with the secret-scan patterns: stored text,
CLI output, error messages, logs, digests, alerts, and helper answers. A hit is replaced by `[redacted]` and counted.

### 3d. Provenance and taint

- **At launch** the session gets `CRIUS_ACCOUNT/PROJECT/ROLE/PARENT/ITEM` (cmux launch `--settings` env) and its first
  prompt carries one tag line, `[crius project=quotes role=qa parent=<uuid> item=quotes#41]`. The ingest extracts only
  that tag from the prompt (allowlisted regex), nothing else. **At a task switch** (clear-per-item) the resume prompt
  carries a new tag and starts a new `assignment` row.
- Missing tag: project may come from cwd (`attrib_source=cwd`); role and item stay `unknown`. Never guessed.
- **Taint**: a session becomes tainted when it reads a tainted source (Falcon mail/ticket/PR records, web pages).
  Everything it writes inherits taint: decisions, artifacts, events, items. A `find` answer or helper answer is
  tainted if any cited row is. Only the owner's own words are clean (7b).

---

## 4. The interface: five commands

`crius-rec <command>` (Python stdlib + sqlite3). Text by default, `--json` for scripts, output capped (default 20
lines). Every answer starts with a status line and exits with a code:

| Status | Meaning | Exit |
|---|---|---|
| `complete` | all sources for the scope were fresh and searched | 0 |
| `partial` | some sources stale or failed; names them; results may be missing | 3 |
| `unavailable` | could not search (DB locked, store missing, leak test failed) | 4 |
| `expired` | the evidence existed but is past retention (e.g. transcript > 30 d); shows the durable rows that remain | 5 |

A true "no" is `complete` with `no match in <sources, project, since>`.

| Command | Covers | Users | Typical cost |
|---|---|---|---|
| `find <query> [--project] [--since] [--kind decision\|done\|blocked\|defect...] [--active] [--waiting] [--deep]` | search and timelines. Order: exact ID, then FTS5, then (knowledge questions) Falcon `memory_search`. `--kind decision --active` replaces a decisions command; `--since` gives a timeline; `--waiting` lists open waits. `--deep` = the bounded transcript search below | Crius via Explore, owner, helpers | 500-1,500 tokens |
| `show <id>` | one thing in full: an item (criteria, child items, evidence, acceptance, defects), a decision (and what supersedes it), a session, or a project (its STATE.md) | Crius, leads | 300-800 |
| `record <type> ...` | writes: `bus` (checks grammar), `decision` (append + bus line, returns id), `item` (new id + criteria), `accept` (acceptance record), `register` (assignment) | leads, Crius | ~50 |
| `quota` | per account: epoch, observed % used, pace vs time passed, uncovered usage, list-price shares by project (separate) | Crius daily check | ~150 |
| `health` | per source and per resource: last run, freshness, expected vs seen, heartbeat age, last backup and restore test | Crius, alerts | ~200 |

**Bounded transcript search (`find --deep`).** Used only when the store has no answer and the question is about the
last 30 days. A Haiku helper runs `rg` over the transcript folders of that project and time window only (max 50
files, max 2 minutes), and returns at most 10 citations (transcript path, message uuid, time) with a one-line
redacted paraphrase each. Older than 30 days -> `expired`. Crius never reads the slices itself.

**Generated files, not commands:** digest (`state/digest/`), scorecard (`state/scorecards/`), STATE.md. Each starts
with a freshness stamp: `built <ts> · status partial · bus 09:20 ok · git 09:30 ok · quota/personal STALE 6 h`.

**Deferred:** a trials table and command, a Postgres backend, an MCP server (only for the Week 4 voice connector, and
then hosted where §3a allows). A CLI costs nothing until called and works from the phone through Remote Control.

---

## 5. Ingestion

`crius-ingest` = `extract.py` made incremental, plus readers for git, CI, bus, team logs, quota, Falcon, files.

| Source | Reads | Produces |
|---|---|---|
| Transcripts (both roots) | new bytes per file | `request` (upsert), `session`, prompts, compactions, interrupts, commits made, AskUserQuestion, hook errors, provenance tags |
| Git (repos in `projects.toml`) | `git log` on default branch since cursor; `git fetch` read-only | `commit`, reverts, `evidence` |
| CI | `gh run list`; `az pipelines runs list` read-only (Appxite; never production systems) | CI results as `evidence` |
| Bus + team logs | `~/.crius/bus/*.log`; active teams' BUILD-LOG, BUILD-DECISIONS headers, QUESTIONS `NEED:` | events, QA `DONE`/`BUG`, defect signals; malformed lines -> `malformed` event + warning |
| Quota spool | `spool/quota.jsonl` | `quota` |
| Falcon | `falcon-crius` reads: runs (skill, model, cost, outcome), cards acted on | Falcon events, outcomes, cost |
| Files | DECISIONS.md, LEAD.md, artifact folders, by mtime + sha256 | `decision`, hand fields, `artifact` |

**Incremental rules (transcripts and spools).**
- A request is streamed as several records with the same request id; usage grows. Upsert by request identity and keep
  the **max** of each token field over all its records (the `extract.py` rule).
- **Partial last line**: never parse a line without its newline; the cursor stops before it.
- **Truncation** (file shorter than the cursor) or **replacement** (inode changed): rescan the whole file; keys dedupe.
- **Late arrival**: a record older than the newest seen still upserts; its day's rollup is recomputed.
- Every row has a `source_key`; a cursor moves only after its transaction commits.
- Unknown line shapes (new Claude Code version) are counted and alerted, never skipped quietly.

**Instrumentation order** (each only when its need is real):
1. **Ingest + quota sampling.** The statusline script appends a sample when a value changes, plus a **heartbeat sample
   every 30 minutes** while a session is open, so "no sample" means "not measured", not "no use".
2. **Minimal hooks** (Notification, Stop, SessionStart/End) only when live waits are needed (`find --waiting`). Until
   then waits come from transcripts after the fact.
3. **OTel** only if permission-wait precision or hook diagnostics justify it. Then a local collector with an
   attribute allowlist (drop `user.email`, account ids, prompts, tool details) and file output kept 7 days.

**Schedule.** launchd every 10 minutes (ESTIMATE 1-3 s per incremental run; a full pass is 20 s, FACT). 03:00:
re-count the last 2 days from scratch (> 1% difference = alert), prune, backup, restore test (weekly). 06:30: digest.
After each quota reset: scorecard. No model tokens in any of these.

**Failure loudness, end to end.**
- **Independent heartbeat.** Each ingest run sends allowlisted metadata (time, per-source status, row counts) to the
  Falcon hub. A k3s CronJob alerts on Telegram/Discord when the heartbeat is older than 30 minutes during hours when
  sessions ran. It works even when the Mac, launchd or the ingest is dead.
- **Per-resource coverage**: every transcript root, repo, bus file and account has an expected-vs-seen row. Expected
  events: a session with commits in its transcript must show those commits in git; an account with open sessions must
  have a quota sample within 45 minutes; a team with active sessions must have a bus or BUILD-LOG line within 3 h
  ("lead silent"); once hooks exist, every session must have a SessionStart line.
- Freshness is stamped in every generated file; every answer carries a status; failures exit nonzero.
- Alerts: one redacted line per problem per day, plus the top of the digest.

---

## 6. Reporting standard for teams

**Bus line grammar** (cmux-team §6 with three changes: a date, the `~/.crius/bus/` path, three new types):
```
2026-10-16T09:20 DONE quotes:leader -> crius — item=quotes#41 sha=abc1234 Inc 4 QA-verified; evidence docs/build/STATUS.md
```
- `<YYYY-MM-DDTHH:MM> TYPE <project>:<role> -> crius — text [refs]`, from `$(date +%FT%H:%M)`, ≤ 300 chars, append
  with `>>` (or `crius-rec record bus`). Old `HH:MM` lines still parse (date from file order and mtime).
- Types: `DONE`, `BLOCKED`, `NEED-OWNER`, `PAUSED`, `LIMIT` + `ITEM`, `DECISION`, `RESUMED`.
- Refs: `item=`, `sha=`, `pr=`, `path=`, `<project>-D<n>`. A `DONE` without `item=` and one evidence ref is a claim.

**LEAD.md** — the only hand-written state, four fields, changed only when they change:
```
Goal: <one line>
Next: <up to 3 lines>
Waiting on owner: none | <what / what to do / why only the owner / what it unblocks>
Risks: <up to 3 lines>
```

**STATE.md** — generated, never hand-edited:
```
# quotes — state · built 2026-10-16 09:40 UTC · status complete · bus 09:20 · git 09:30 · transcripts 09:38
Goal / Next / Waiting on owner / Risks          <- LEAD.md (changed 2026-10-16 08:10)
Open outcomes: quotes#41 (3 child tasks, 2 d), quotes#43 (4 h)
Accepted since last digest: quotes#40 (rev 9f1e2d3: CI pass, codex review clean, accepted by crius)
Blocked: none · Open waits on owner: 1 (NEED-OWNER 08:55, 45 min)
Active decisions (last 5): quotes-D12 ..., quotes-D11 ...
Today: 14 commits, 0 reverts · sessions 4 busy, 1 idle · attribution unknown for 1 session
```

**DECISIONS.md** — append-only via `crius-rec record decision`, never edited:
```
## quotes-D12 · 2026-10-16
Decision: use svc_quote v2 for CPQ pricing calls
Why: v1 drops currency on bundles (evidence below)
Decided by: owner (task #54) | crius | lead
Supersedes: quotes-D7
Refs: pr=412 sha=abc1234 path=research/cpq-v2.md · session 5f3c... · taint: no
```
Supersession is read from the newer entry only. A correction is a new entry that supersedes the wrong one.

| Lead writes (manual) | Derived (no one writes) |
|---|---|
| bus lines at DONE / BLOCKED / NEED-OWNER / PAUSED / LIMIT / ITEM / DECISION / RESUMED | commits, CI, reviews, waits, requests, sessions, quota, STATE.md, links |
| LEAD.md when goal, next, owner-wait or risks change | "lead silent", "stale hand fields", "claim not verified", attribution gaps |
| decisions that outlive the task; acceptance criteria when opening an item | acceptance checks against the criteria |

The provenance tag (§3d) goes into the cmux-team launch and resume prompt templates. Reviewers: codex and quick-review
end with `FINDINGS P1=<n> P2=<n> P3=<n>` (an agent-definition edit, part of Q3).

---

## 7. Measurement

**Unit of output: an accepted owner outcome.** One thing the owner wants = one item; the work under it are child
tasks, which are not counted. Code, research and Falcon outcomes are reported apart, never summed.
- **Criteria set before work.** `record item` stores the acceptance criteria when the item opens: which machine checks
  (CI, gate, tests, browser-check) and which independent review (codex, quick-review, team QA, owner).
- **Bound to the exact revision**: checks and review must point at the same commit sha or artifact sha256. A review
  that failed to run is not a review. The reviewer is never the session that did the work.
- **Explicit acceptance record**: Crius (or the owner, when the criteria say so) writes `record accept`, which checks
  the evidence against the criteria and stores the `acceptance` row and an `ACCEPTED` event.
- A revert or reopen within 14 days marks the outcome `reopened` and links a candidate defect.

| # | Metric | Definition | Source |
|---|---|---|---|
| M1 | Accepted outcomes | count per quota week, split code / research / Falcon, by project | `acceptance` |
| M2 | Observed quota | per account and reset epoch: sum of positive `seven_day_pct` deltas between samples; plus **uncovered usage** = list-price $ of requests with no sample on either side within 45 min | `quota`, `request` |
| M2b | Project share (separate) | each project's share of list-price $ per account and epoch. Never merged into M2 | `usage_day` |
| M2c | Quota per outcome (**experimental**) | M2 x M2b share / M1 for that project and kind. **Undefined when M1 = 0** | derived |
| M3 | Agent-wait minutes | sum over sessions of resolved wait durations (`blocked` kinds), by kind; overlapping waits add up | `wait` |
| M4 | Owner wait-elapsed time | union of intervals when ≥ 1 agent was blocked on the owner (overlap counted once) | `wait` |
| M5 | Open waits | unresolved waits with age, never dropped | `wait` |
| M6 | Cycle time | item open -> acceptance, p50 / p90, per kind | `item`, `acceptance` |
| M7 | Owner interventions | human prompts into non-Crius sessions, interrupts, owner-reversed decisions, per outcome | transcripts, decisions |
| M8 | Owner effort | only where observed: human prompts and their count per outcome. Active keyboard time only if OTel is added | transcripts |
| M9 | Crius response | owner prompt -> Crius Stop, p50 / p90 | transcripts |
| M10 | Defects (**candidate** -> confirmed) | candidate signals: reverts, fix commits on the same files within 72 h, BUG after QA, reopen, owner report. Confirmed = linked to an item and its revision, deduped into incidents | `defect` |
| M11 | Review findings (**candidate**) | P1/P2 per reviewed revision; share later confirmed as defects | `evidence`, `defect` |
| M12 | Context cost drivers | $ per main call, share of main $ above 267K, compactions per 100 calls, mean member context | `usage_day`, `session` |
| M13 | Crius cost | Crius sessions' list-price share; observed quota when attributable | `session.role=crius` |
| M14 | Memory recall | % right in top 5, seconds, negative answers that name scope | `eval/recall.jsonl` |
| M15 | Record health | coverage %, heartbeat gaps, unknown attribution share, malformed lines, claims unverified | `coverage`, `session` |

**Cohorts.** A week's outcomes are shown **provisional** at first and **matured** 14 days later (after the reopen and
defect window). Quality numbers are judged on matured cohorts only.

**Weekly scorecard** (`state/scorecards/<quota-week>.md`, ~40 lines; numbers below show the shape, not data):
```
# Quota week ending 2026-10-23 · built 2026-10-23 07:00 · status complete · def v1
Outcomes   code 14 · research 5 · Falcon 4 (provisional)  | wk 10-09 matured: code 11, 1 reopened
Quota      personal: 71% observed, uncovered 3% · work: 40% observed, uncovered 0%
Shares     list-price: quotes 26% · axi 22% · falcon 14% · trading 9% · Crius 4% · unknown 6% · other 19%
Per outcome (experimental) quotes code 2.1% · axi code 3.4% · trading: undefined (0 outcomes)
Waits      agent-wait 212 min (permission 40, NEED-OWNER 150, AskUser 22) · owner elapsed 94 min · open 1 (3 h)
Flow       cycle time code p50 1.6 d · interventions 0.8/outcome · Crius response p50 40 s
Quality    candidates: 6 fix-after, 3 BUG after QA · confirmed defects (matured wk): 2 in 1 incident
Health     coverage 99% · heartbeat gaps 0 · unknown attribution 6% · restore test PASS 10-20
Spot-check quotes#38, axi#12, falcon#7: evidence opened, all real
```

**Judging changes (descriptive, not causal).** Before/after comparisons describe what changed; with one owner and
mixed work they cannot prove cause. Start dates are staggered; savings are never added.
- **300K window** (in force in both personas; FACT): judged on 10-23 with the review-1016 scripts and its stop rule
  (baseline 0.126 $/main call on weekdays, 69 compactions; ≥ 3 weekdays and 30 compactions; back to 500K if $/main
  call falls < 5% or pushback after compaction > 2x the same-week rate). The store takes over only once it
  reproduces those numbers within 2%.
- **Sonnet members** (from 10-16, judged 10-23): per role, BUG after QA, candidate defects, cost per child task,
  vs the same roles on Opus the week before. Re-read on the matured cohort two weeks later.
- **Clear-per-item**: member mean context, tasks per session, team $ per team prompt (P2 $21), candidate defects.
  The task-switch tag makes per-task cost exact for members.

**Goodhart guards.** Criteria fixed before work; one outcome per owner goal (splitting adds child tasks, not units);
independent review bound to the revision; output always next to quality; leads never get metrics as targets;
definitions versioned; 4-week trends; n < 10 marked "small sample"; 3 random outcomes opened each week.

---

## 8. How Crius works day to day

- **06:30** script: ingest, STATE.md, digest (≤ 60 lines, stamped): needs the owner (top, four lines each), accepted
  yesterday, blocked, open waits, decisions, quota pace, health.
- **Morning start**: a fresh Crius session with CRIUS.md + the digest only (target ≤ 15K tokens). If pace is high
  (default ≥ 60% observed by day 3 of the epoch; Q4), ONE reminder: speed, top burners, one suggestion. No stop.
- **During the day**: wakes on bus events (Monitor in the pilot, the Falcon bus later); reads the line and `show
  <project>` (≤ 1K tokens); acts through leads.
- **"What happened with X?"**: Explore runs `find`; if needed `find --deep`; Crius keeps only the cited answer and
  its status. A `partial` or `expired` status is said to the owner as it is.
- **Deciding**: `find --kind decision --active --project X` first, then `record decision`.
- **Accepting**: `show <item>`, then `record accept` only when every criterion has evidence on the same revision.
- **Hygiene**: capped outputs, no raw dumps, restart at ~150K, stable prefix (DESIGN §6).
- **Cost of Crius** (ESTIMATE): ~$0.02-0.03 list price per call at ~50K context (review-1016: $0.12 at ~235K);
  150-300 calls/day = ~$4-9/day, a few % of a week. Scripts: 0 tokens. Haiku helpers < 1%. M13 replaces the guess.

---

## 9. Rollout (each step useful alone, each test fails first)

| Step | What | Test |
|---|---|---|
| 1 | `~/.crius/` layout, git-leak test, decision files, item ids with criteria, dated bus grammar + provenance tag in cmux-team templates | leak test fails when a `.db` is tracked or `~/.crius` is in a work tree; 3 decisions incl. a supersede give the right active set; an undated line still parses |
| 2 | `crius-ingest` v1 (transcripts, git, bus, files) -> durable rows; backup; heartbeat + k3s CronJob | fixture where one request spans two runs gives one row with max usage; partial line, truncation, replacement and late-arrival fixtures; second run adds 0 rows; stop the ingest -> alert in ≤ 30 min |
| 3 | Quota sampling (personal statusline with 30-min heartbeat sample) + `quota` | samples at 60% on day 3 -> one reminder; a 2-hour gap with open sessions -> "uncovered" shown, not 0 |
| 4 | `find`, `show`, `health`, `find --deep`, redaction on every sink | recall v0 (20 questions from STATUS.md: "when did mail stop" -> 09-21; "which commit armed k3s hooks" -> cf62176) ≥ 80% top 5; a silent lead appears in `health`; a 40-day-old question -> `expired`; a superseded decision is marked; a true "no" names its scope; a planted secret in a bus line never reaches output, error text or an alert |
| 5 | Digest + Crius daily start | start ≤ 15K tokens; 5 owner questions answered with no lead tab opened |
| 6 | `record accept` + scorecard; first descriptive read 10-23 next to the review-1016 scripts | accept refuses evidence on a different sha; numbers match review-1016 within 2%; restore-after-expiry test passes |
| 7 | Minimal hooks for live waits (if needed) | a scratch permission prompt -> open wait -> resolved with paired ids; a session with no SessionStart line -> alert |
| 8 | OTel (only if justified) | allowlist drops `user.email`; session count matches transcripts |
| 9 | Falcon source; move to Postgres only on both §3a conditions | k3s run count in the store = Falcon's list for a day |

**Do not build:** a graph DB or Graphiti; Mem0, Letta, Zep; Langfuse, Phoenix; embeddings over transcripts; LLM calls
at ingest; a dashboard UI; a trials engine or second backend now; storing transcript text; an MCP server before the
voice connector; size classes; any metric with no action attached.

---

## 10. Risks, decisions I made, and questions for the owner

**Risks.**
1. Silent gaps. Guard: outside heartbeat, per-resource coverage, statuses, nightly re-count.
2. Transcript format changes. Guard: unknown shapes alerted; fixtures per Claude Code version.
3. Quota is observed only where sampled; per-project quota is an estimate (marked experimental).
4. Small samples and Goodhart: matured cohorts, 4-week trends, descriptive trials.
5. Appxite data: content-free rows, local store, allowlisted metadata only off the Mac.
6. Lost durable rows if the Mac disk dies without backups: nightly backup + restore test; offsite only per Q2.
7. Over-building: drop any part nobody reads for 4 weeks.

**Decided without the owner (say if any is wrong):** keep transcripts at 30 days; acceptance for non-code = Crius
after an independent review bound to the artifact hash, or the owner when the criteria name them; no size classes;
private state in `~/.crius/` outside git; OTel deferred; CLI before MCP; trials stay descriptive.

**Questions only the owner can answer.**
- **Q1. Do the personal and work personas use one Max account (one weekly quota) or two?** One -> one pace check and
  one quota line. Two -> two of each. Until answered, `quota` reports per config dir and marks the account `unknown`.
- **Q2. Where may Appxite-derived data live?** (a) Mac only, the default. (b) also Falcon Postgres on k3s -> allows
  the §3a move when there is also an operational need, and an offsite backup. (c) also the private GitHub repo. Only
  allowlisted metadata (heartbeat, counts, quota %) leaves the Mac until you choose (b) or (c).
- **Q3. Approve these config changes, one at a time when their step comes?** (a) a statusline in the personal persona
  that writes quota samples (step 3; without it, personal quota is uncovered); (b) the `FINDINGS` line in the codex
  and quick-review agent definitions (step 6; without it, findings are not counted); (c) minimal hooks in both
  personas (step 7, only if live waits are needed); (d) OTel env with an allowlist (step 8, only if justified).
- **Q4. Pace reminder: what threshold (default ≥ 60% observed by day 3 of the epoch, or burning faster than the
  7-day average) and which channel (Remote Control push to the phone, or Telegram/Discord)?**
