# Crius — memory and measurement (design, 2026-10-16, rev 2 after the Codex critique)

Design only. No code, no config changes. It answers the owner's question: how Crius remembers and finds what
happened, fast and reliably; whether Claude Code memory is enough; what standard teams use to report; and how we
measure output, cost and waits. Inputs: `research/memory-and-measurement.md`, `research/claude-code-telemetry.md`,
`docs/DESIGN.md` (7b overrides), `STATUS.md`, cmux-team skill §6, `tools/agent-cost/review-1016/REPORT.md`.
Marks: **FACT** = checked in a file or by a test today. **ESTIMATE** = my reasoning, to be checked.
Rev 2 applies all 12 Codex findings (verdict "ship after fixes"). Rev 3 adds the owner's answers of 2026-10-16
(§10) and §7b on mistakes and corrections.

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
7. Headline numbers, weekly: accepted owner outcomes (code / research / Falcon apart), subscription quota pace plus
   Appxite enterprise $ spend, and time the owner kept agents waiting. Quality numbers stay candidates until confirmed.
8. Every answer says `complete`, `partial`, `unavailable` or `expired`, names what it searched, and fails loudly.
9. Crius starts fresh each day from a script-built digest (zero model tokens). Mistakes are fixed forward with
   linked records, never by rewriting history (§7b).
10. Everything stays on the Mac (owner, 2026-10-16). One notification channel (Remote Control push), few and
    high-signal. No graph DB, no memory product, no dashboard, no OTel until a need is shown.

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

Must survive: a lead forgets to report ("lead silent", derived from git and team logs); a session crashes (closed
at its last record, end `unknown`); a hook fails silently (expected-event check, like the 10-09 exit 127); the ingest
dies (local watchdog, §5); evidence expires (`expired`, not "not found").

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

Auto-memory is not the timeline: 200-line cap, loaded into every session, not loaded by subagents, no dates or
supersession, self-reported, no query. Existing pieces and how each is used:

| Piece | Use | Limit |
|---|---|---|
| Transcripts (`~/.claude/projects` = `~/.claude-personal/projects`, 4.4 GB; `~/.claude-work/projects`, 94 MB; FACT) | main source of requests, sessions, prompts (human vs peer), compactions, commits made, AskUserQuestion, hook errors | deleted after 30 days (default `cleanupPeriodDays`; oldest file 09-16; FACT). **Decided: keep 30 days** (disk); durable records carry what we need |
| `tools/agent-cost` `extract.py` (1,575 files, 159K requests, 20 s, no prompt or tool text; FACT) | core of `crius-ingest`: same fields, same max-usage rule per request, made incremental (§5) | full run writes 65 MB JSONL; the store keeps rows instead |
| context-mode FTS5 (`~/.claude/context-mode`, 841 MB; FACT) | stays the in-session recall tool | not the record: per-project DBs, private schema, raw tool output inside |
| Statusline `rate_limits` | the only source of real quota (FACT); first instrument after ingest (§5) | work persona has a statusline; personal has none (FACT). `rate_limits` exists only on Pro/Max, so a session with no `rate_limits` but a cost is on the enterprise account (ESTIMATE, checked in step 3) |
| Hooks | later, only for live waits (`find --waiting`) | a recorder, never a gate |
| OTel (works on the Max login, needs a collector; FACT) | deferred until permission-wait precision or hook diagnostics justify it | if added: allowlisted fields only |
| Falcon (`falcon-crius` MCP: 18 reads + 14 actions) | a source: Falcon run outcomes and cost (k3s runs' transcripts are not on the Mac), copied to the Mac | not a home for Crius rows (everything local, §10) |
| cmux-team bus `crius/bus/<project>.log` (exists, empty, gitignored; FACT) | the leads' channel to Crius, strict grammar (§6) | moves to `~/.crius/bus/` (§3b) |
| Explore / quick-review (Haiku), codex | Explore runs `find` and the bounded transcript search for Crius; reviews are acceptance evidence | no new agent type |

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
| Data boundary | 5 | 5: stays on the Mac | owner chose Mac only (2026-10-16) |
| Phone / voice reach | 2 | 3: through Crius (Remote Control) | 5 |
| Weighted (§1 weights; last two not weighted) | 3.8 | **4.8** | 4.0 |

**Pick: SQLite on the Mac.** Sources are on the Mac, so the extractor runs there anyway.

**Everything stays on the Mac (owner, 2026-10-16).** Nothing from the store leaves it, metadata included. A move
(to Falcon Postgres, or a remote Crius later; the owner holds the `crius.ai` domain, note only) needs BOTH an
operational need, such as a voice connector answering without a Crius session, AND a new explicit owner permission.
**Path, if that day comes:** same tables in Postgres; one more backend in `crius-rec` (not built now); the ingest
can become a job of Falcon's Mac daemon. Callers do not change.

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
| `session` | id, account (`max` = the one subscription, `enterprise` = Appxite API billing, `unknown`), persona, project, role, parent_id, item, each with `attrib_source` (`registered`, `cwd`, `unknown`), title, model_main, started, ended, end_reason, peak_ctx, compactions, human_prompts, peer_prompts, interrupts, hook_blocks, hook_errors, taint |
| `assignment` | session, item, role, from_ts, to_ts, source (launch or task switch) |
| `wait` | wait_id, session, kind, request_ref (notification or tool_use_id), opened, resolution_ref, resolved, resolved_by (`human/peer/timeout/none`), state `open/resolved` |
| `quota` | ts, account, session, persona, epoch (seven-day `resets_at`), five_hour_pct, five_reset, seven_day_pct, sample_kind `session/change/heartbeat` |
| `account_seen` | source_key, session, persona, ts, rl: one row per statusline line, rl 1 = `rate_limits` present (Max), 0 = a response came without them (enterprise). A request's account is its session's newest line by the request time (2 min grace; requests before the first line take it), else its launch tag, else `unknown` |
| `item` | id `<project>#<n>`, parent (owner outcome), kind `code/research/falcon/other`, title, criteria (set at open), opened, state, taint |
| `acceptance` | item, revision (sha or artifact sha256), checks (refs + results), reviewer + review_ref, accepted_by, ts, state `accepted/revoked`, revoked_ts, revoked_by, revoke_reason, defect, fix_item |
| `evidence` | id, kind (`commit/ci/gate/review/artifact/bus`), ref, revision, ts, source_key |
| `defect` | id, item, revision, found_by (`us/owner/others`), found_ts, severity `S1-S3`, cause_class, signal refs (revert, BUG line, owner report), incident id (dedupe), confirmed, locked_output (bool) |
| `correction` | id, target (row id), field, old_value, new_value, reason, by, ts. Views apply it; the target row is never edited |
| `event` | id, ts UTC, project, kind, actor, session, item, refs JSON, text (≤ 300, filtered), taint, verified, source_key UNIQUE |
| `decision` | id, project, ts, title, decided_by, supersedes, file, line, session, taint |
| `commit` | sha, repo, project, ts, session (from the transcript's `[branch sha]` result), files, is_revert, reverts_sha, ci |
| `source_run`, `coverage` | per run and per resource: status, cursor, rows, expected vs seen |

**Derived views** (rebuildable from durable rows + files): `event_fts`, `doc_fts` (FTS5 over event text, decisions,
artifact titles and headings), daily and weekly rollups, `STATE.md`, digest, scorecard.

**Backup and restore.** Nightly `sqlite3 .backup` into `~/.crius/backup/` (Time Machine covers it if on). A
**restore-after-expiry test** runs weekly: restore the newest backup into a scratch DB, remove access to transcripts
older than 30 days, regenerate a matured week's scorecard, and compare it with that file's latest revision (§7b).
Any difference fails. **Disk budget:** `~/.crius` ≤ 1 GB; nightly prune of fine events and `request` rows after 90
days, after rollup.

**Links.** Item `<project>#<n>` (children point to their owner outcome); decision `<project>-D<n>`; team rulings
indexed as `<project>-B<n>`. Event -> decision -> commit -> session goes by refs in bus lines, decision entries and
transcript commit results; no commit trailers. Refs are validated; a bad ref is kept and flagged.

**No secrets, no bodies.** Text enters only from short fields written for the record (bus lines, decision entries,
titles, headings). Prompt text, tool output and assistant text are never stored: only lengths, counts, timestamps,
pointers, and allowlisted tags (§3d). **Every output sink is redacted** with the secret-scan patterns: stored text,
CLI output, error messages, logs, digests, alerts, and helper answers. A hit is replaced by `[redacted]` and counted.

**Known redaction gaps (step 4, 1eb7a34 and its review fix).** The patterns are hand-made; six review rounds each found a new edge of
key = value parsing, so step 4b swaps in a maintained ruleset (gitleaks rules) behind `redact.Redact`, tested on the
31k real lines and the leak cases of all six rounds. Until then these are known and not fixed:
- A YAML value on the next line (`password:` then the value below it) is printed.
- Short flags (`-p hunter2`) are printed; only long flags (`--password x`) are redacted.
- An unquoted value ends at a comma, space or backtick (`password=a,b` prints `,b`; `password=abc`def` prints
  `` `def``; a password with a space prints its tail).
- A plain value that ends in another key and its operator takes that key, and the key's value is printed
  (`password: hunter2/token: abc` prints `abc`).
- A backtick with a space after it is read as markdown, not a value (`` token = ` hunter2` `` is printed).
- Over-redaction: prose with `secret:`/`token:`, code with `==` or `authorization`, the name in
  `secrets.env: DB_PASSWORD = x`, and in `find --deep` tool input a field named `value`/`val` in the same object
  as a field whose value is a secret name (`{"filename":"password.txt","value":"x"}`).
- `find --deep` matches words before redaction, so a guessed secret confirms itself by being found (it is never
  printed). Only secrets that match a pattern are caught; a secret with no key, prefix or known shape is not.

### 3d. Provenance and taint

- **At launch** the session gets `CRIUS_ACCOUNT` (`max` or `enterprise`) and `CRIUS_PROJECT/ROLE/PARENT/ITEM` (cmux
  launch `--settings` env), and its first prompt carries one tag line, `[crius account=max project=quotes role=qa
  parent=<uuid> item=quotes#41]`. The ingest extracts only that tag (allowlisted regex). **At a task switch**
  (clear-per-item) the resume prompt carries a new tag and starts a new `assignment` row.
- Missing tag: project may come from cwd (`attrib_source=cwd`); role and item stay `unknown`. Never guessed.
- **Taint**: a session becomes tainted when it reads a tainted source (Falcon mail/ticket/PR records, web pages).
  Everything it writes inherits taint: decisions, artifacts, events, items. A `find` answer or helper answer is
  tainted if any cited row is. Only the owner's own words are clean (7b).

---

## 4. The interface: five commands

`crius-rec <command>` (Go; SQLite through modernc.org/sqlite, which has FTS5 built in). Text by default, `--json` for scripts, output capped (20 lines).
Every answer starts with a status: `complete` (exit 0: all sources for the scope fresh and searched), `partial`
(exit 3: names the stale or failed sources), `unavailable` (exit 4: DB locked, store missing, leak test failed),
`expired` (exit 5: evidence past retention; shows the durable rows left). A true "no" is `complete` with
`no match in <sources, project, since>`.

| Command | Covers | Users | Typical cost |
|---|---|---|---|
| `find <query> [--project] [--since] [--kind decision\|done\|blocked\|defect...] [--active] [--waiting] [--deep]` | search and timelines. Order: exact ID, then FTS5, then (knowledge questions) Falcon `memory_search`. `--kind decision --active` replaces a decisions command; `--since` gives a timeline; `--waiting` lists open waits. `--deep` = the bounded transcript search below | Crius via Explore, owner, helpers | 500-1,500 tokens |
| `show <id> [--history]` | one thing in full: an item (criteria, child items, evidence, acceptance, defects), a decision (and what supersedes it), a session, or a project (its STATE.md). `--history` gives the whole chain (§7b) | Crius, leads | 300-800 |
| `record <type> ...` | writes: `bus` (checks grammar), `decision` (append + bus line, returns id; `--supersedes <id> --reason`), `item` (new id + criteria), `accept`, `revoke`, `defect`, `correct` (§7b), `register` (assignment) | leads, Crius | ~50 |
| `quota` | two blocks. **Subscription** (one Max account, both personas): epoch, observed % used, pace vs time passed, uncovered usage, list-price shares by project. **Enterprise** (Appxite API billing, real employer money): $ this week and today by project (quotes, AXI), and any non-Appxite session on it as a violation | Crius daily check | ~200 |
| `health` | per source and per resource: last run, freshness, expected vs seen, heartbeat age, last backup and restore test | Crius, alerts | ~200 |

**Room on the Max account (`quota --room`).** For a launcher choosing the account of a new session: one JSON line
on stdout, `{"verdict":"room|full|unknown","reason":"7d 43%, 5h 9%, reset 22 Oct 02:00, sample 5 min old",...}`,
exit 0 room, 3 full, 4 unknown (the reason also on stderr). Owner's rule (Crius, 10-16): a current-week sample
within 45 min has room until 7d or 5h reaches 100%; an older one only while both are under 90%, else unknown; 7d at
100% is full at any age; a 5h window that has reset counts 0. Each share is aged by the newest measurement
showing it: a share only rises within its window, so a lower reading is an older one; a measurement is a line that
came with a response (the sampler marks it when the cost rose and the session rendered within the last 10 min, so
the response is that recent; a line from before the mark counts unless it is a heartbeat); a share at 100% is full
even when only unmeasured lines show it; the older
of the 7d and the running 5h share's ages counts. No sample, an ended week, a sample ahead of the clock, no
5h window sampled, or a failure is unknown, never room. It reads the store, so a sample is up to one ingest run (10 min) older than the spool.  A session
first seen long after its last response shows an old reading, so its line is not a measurement (Crius 23:11).

**`find` as built (step 4).** Exact ids first (`<project>#<n>`, `<project>-D<n>`, a commit sha prefix, a session id
prefix, `event:<n>`, and the events whose refs cite them), then FTS5 (porter stemming, best match first) over event
text, decision titles, item titles + criteria and artifact titles + headings. The index is a temp table built per
query from the durable rows, so it can never be stale (0.1 s on the live store, 2026-10-16). With no query it lists
newest first: rows with text only, or every row of the `--kind` asked. Status comes from the newest ingest run:
`complete` only when that run is complete and at most 20 min old (as the watchdog). An unknown `--kind` is refused
with the known list, not answered "no match". `--waiting` lists the WAIT lines of the last 24 h (or `--since`) that are
still open: a wait naming `sid=<8 hex>` closes once that session makes an API call a minute or more after it; one
naming no session shows as `unknown`. `record` refuses a text that looks like a secret (exit 2) instead of rewriting
it, so prose that only looks like one is never changed unseen. `--json` is redacted value by value (JSON escaping
hides quotes from the patterns).

**`show` as built (step 4).** An item (criteria, children, every event whose refs name it), a decision (the full entry
read from its DECISIONS.md; `Status: SUPERSEDED by <id> (<date>): <title>` or `active`), a session (with its request
count and list $), a commit, an `event:<n>`, or a project's STATE.md (`unavailable` until the digest step builds it).
A hex prefix that matches more than one commit or session is refused with the matches. `--history` gives a decision's
supersede chain, oldest first; acceptance, revokes and corrections join it in step 6.

**`health` as built (step 4).** The last ingest run and the runs of the last 24 h, heartbeat age, the newest backup,
and the restore test (`never run` until step 6; shown, not a problem). Per source, expected vs seen: the ingest lists
what a run should read by its own rules (transcripts under each root, `bus/*.log`, team logs, repos), and a file older
than the last run's start is a problem when it has no coverage row (`never read`) or holds complete lines past its
cursor (`behind`; a partial last line waits by design). A team is a project with a non-empty bus file or team logs;
its lead is `SILENT` when the team's sessions made an API call within 3 h and no bus or team-log line came in that
time. Any problem makes the answer `partial` (exit 3), each named on a `PROBLEM` line.

**Bounded transcript search (`find --deep`).** Used only when the store has no answer and the question is about the
last 30 days. A Haiku helper runs `rg` over the transcript folders of that project and time window only (max 50
files, max 2 minutes), and returns at most 10 citations (transcript path, message uuid, time) with a one-line
redacted paraphrase each. Older than 30 days -> `expired`. Crius never reads the slices itself.

**`find --deep` as built (step 4).** The CLI does the search itself, in Go, with no `rg` on PATH needed. The caller
is the Haiku helper (the Explore agent): it runs `crius-rec find QUERY --deep [--project P] [--since …]` and passes
the citations on. Every project's transcripts are searched, sessions filed under none included, with their subagent
files at any depth; `--project` only ranks that project first, because an asker does not know where a talk was filed
and a filing error must not hide an answer (Crius, 10-16; this replaces the 50-file cap, which made "all projects"
meaningless: about 2,500 files and 5 GB in 30 days, read in about 10 s for a rare phrase and 45 s for a single letter, at 36 MB). The asking session
(`CLAUDE_CODE_SESSION_ID`) ranks last, since it holds the question itself. A message is cited when its own text
(user or assistant; tool input, decoded, and results count) holds every query word; within a rank the shortest stretch holding
them all comes first, then the newest. At most 10 citations (`more` says there are others): path, message uuid, time,
and a one-line snippet around that stretch, redacted before it is cut. The 2-minute limit makes the answer `partial`
with the count of files not read; so does a missing transcript of a session active in the window, a folder
that cannot be read, or a file gone before it is read. Only the best 10 are held while reading, so a common word
costs no more memory than a rare one. An unknown
`--project` is refused. A `--since` before the 30 days gives `expired` (exit 5), even when the time limit also cut it (that stays among the reasons): the retained part is still
searched, and the durable rows `find` has for the whole window follow, marked `durable`. projects.toml is the source
of truth for filing: each ingest run re-files a session filed by its folder, and a registered session's events from
before its first tag, when the config now files that folder differently.

**Generated files, not commands:** digest (`state/digest/`), scorecard (`state/scorecards/`), STATE.md. Each starts
with a freshness stamp: `built <ts> · status partial · bus 09:20 ok · git 09:30 ok · quota/max STALE 6 h`.

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

**Incremental rules (transcripts, spools, bus and team logs).**
- A request is streamed as several records with the same request id; usage grows. Upsert by request identity and keep
  the **max** of each token field over all its records (the `extract.py` rule).
- **Partial last line**: never parse a line without its newline; the cursor stops before it.
- **Truncation** (file shorter than the cursor) or **replacement** (inode changed): rescan the whole file; keys dedupe.
- **New bus type list**: a bus cursor keeps a hash of the type list it was read with; when the list changes, the file
  is read again and a row stored as `malformed` takes the line's real type. An undated line after it takes its new
  date only when a dated line earlier in the file anchors it; a date that rests on the file's birth date stays.
  `coverage.rows` adds only the lines past the old cursor, so a re-read does not count the file twice.
- **Late arrival**: a record older than the newest seen still upserts; its day's rollup is recomputed.
- Every row has a `source_key`; a cursor moves only after its transaction commits.
- Unknown line shapes (new Claude Code version) are counted and alerted, never skipped quietly.

**Account guard.** A request's account is the statusline's word at that time (`rate_limits` present = Max), else
the launch tag; a tag the statusline contradicts is a mismatch alert. Each enterprise stretch (from an enterprise line,
or from a session's first request when that line is its first, until the next Max line) and each enterprise tag is
checked against every project active in it. A non-Appxite project (`projects.toml` marks which may use `enterprise`)
seen on the enterprise account is an S1 `health` problem and goes to the owner at once: personal work must never bill
the employer. Spend whose account is unknown is not measured: `quota` says "unknown $X" and "not measured", never $0.
Enterprise $ is list price from tokens (ESTIMATE of the employer's bill; no billing source is read).

**Instrumentation order** (each only when its need is real):
1. **Ingest + quota sampling**: a sample on each change plus a **heartbeat sample every 30 min** while a session is
   open, so "no sample" means "not measured", not "no use". `crius-statusline --persona P` is the statusLine command
   (`refreshInterval` 300 s, so idle sessions re-run it): it writes a line when a session is first seen after a
   response, when that session's own 5h/7d values change, and a heartbeat when that persona wrote no current-epoch
   line for 30 min. A known enterprise session (its response came without `rate_limits`) heartbeats when its persona
   wrote no line at all for 30 min, so an enterprise-only persona is not stale while its sessions are open; a session
   whose account is not known yet (resumed, no response) writes none (Crius, 10-16). An idle session repeating its own older values writes no change line, but may write that
   heartbeat with an older share of the same epoch (harmless: the report takes the epoch's highest share); a line of a
   past epoch never heartbeats. `rate_limits` without
   the seven-day share and reset is an error, not a sample. It never touches the store, and on an error it prints
   `FAILED` in the statusline (exit 0: Claude Code hides the output of a non-zero exit). Stale = a persona with a
   session open (not known to be enterprise) and no line within 45 min, including one that never wrote a line, or
   open Max sessions with no current-epoch sample within 45 min.
2. **Minimal hooks** (Notification, Stop, SessionStart/End) only for live waits; until then waits come from transcripts.
3. **OTel** only if permission-wait precision or hook diagnostics justify it: local collector, attribute allowlist
   (drop `user.email`, account ids, prompts, tool details), files kept 7 days.

**Schedule.** launchd every 10 minutes (ESTIMATE 1-3 s per incremental run; a full pass is 20 s, FACT). 03:00:
re-count the last 2 days from scratch (> 1% difference = alert), prune, backup, restore test (weekly). 06:30: digest.
After each quota reset: scorecard. No model tokens in any of these.

**Failure loudness, end to end.**
- **Independent heartbeat, on the Mac.** Each ingest run stamps `~/.crius/heartbeat`. A separate launchd watchdog
  (own job, own schedule, no shared code with the ingest) alerts when the stamp is older than 30 minutes while
  transcripts are growing. If the whole Mac is down, no session runs either, so nothing goes unseen (Falcon's k3s
  runs keep their own records and are read back later).
- **Per-resource coverage**: every transcript root, repo, bus file and account has an expected-vs-seen row. Expected
  events: a session with commits in its transcript must show those commits in git; an account with open sessions must
  have a quota sample within 45 minutes; a team with active sessions must have a bus or BUILD-LOG line within 3 h
  ("lead silent"); once hooks exist, every session must have a SessionStart line.
- Freshness is stamped in every generated file; every answer carries a status; failures exit nonzero.

**Notifications: few, high-signal, one channel (owner rule, 2026-10-16).** Scripts never notify the owner directly.
They append a redacted line to `~/.crius/bus/crius.log`; Crius reads it and decides. Only two things reach the phone,
by Remote Control push (Crius asks with AskUserQuestion or assign_task): something the owner must act on now (S1, an
enterprise-account violation, NEED-OWNER that blocks a team), and at most one pace reminder a day. Everything else
waits for the digest. No Telegram or Discord until the system is mature; a push the owner learns to ignore costs
more than a late one.

---

## 6. Reporting standard for teams

**Bus line grammar** (cmux-team §6 with three changes: a date, the `~/.crius/bus/` path, more types):
```
2026-10-16T09:20 DONE quotes:leader -> crius — item=quotes#41 sha=abc1234 Inc 4 QA-verified; evidence docs/build/STATUS.md
```
- `<YYYY-MM-DDTHH:MM> TYPE <project>:<role> -> crius — text [refs]`, from `$(date +%FT%H:%M)`, ≤ 300 chars, append
  with `>>` (or `crius-rec record bus`). Old `HH:MM` lines still parse (date from file order and mtime).
- Types (the one list; `internal/store` tests check the Go list equals it): `DONE`, `BLOCKED`, `NEED-OWNER`,
  `PAUSED`, `LIMIT`, `ITEM`, `DECISION`, `RESUMED`, `ALERT`, `INFO`, `NEED`, `WAIT`, `PARKED`.
- Who writes them: ALERT only Crius's own scripts (ingest, watchdog), WAIT the wait detector, PARKED the park mod
  (via the leader). Team-only types (ACK, CLEAR-READY, QUEUE) never go on the Crius bus; any type not in the list
  above is malformed.
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
| bus lines, one per event, in the §6 types (not ALERT or WAIT: scripts write those) | commits, CI, reviews, waits, requests, sessions, quota, STATE.md, links |
| LEAD.md when goal, next, owner-wait or risks change | "lead silent", "stale hand fields", "claim not verified", attribution gaps |
| decisions that outlive the task; acceptance criteria when opening an item | acceptance checks against the criteria |

The provenance tag (§3d) goes into the cmux-team launch and resume prompt templates. Reviewers: codex and quick-review
end with `FINDINGS P1=<n> P2=<n> P3=<n>` (an agent-definition edit, approved in D3).

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
- A confirmed defect, at any time, revokes the acceptance and opens a linked fix item (§7b).

| # | Metric | Definition | Source |
|---|---|---|---|
| M1 | Accepted outcomes | count per quota week, split code / research / Falcon, by project | `acceptance` |
| M2 | Observed subscription quota | the one Max account (both personas), per reset epoch: the highest `seven_day_pct` sampled in the epoch; plus **uncovered usage** = list-price $ of `max` requests with no sample on either side within 45 min | `quota`, `request` |
| M2e | Enterprise $ | Appxite enterprise account: list-price $ per day and week, by project; non-Appxite sessions on it listed as violations. Real employer money, never merged with M2 | `request`, `session.account` |
| M2b | Project share (separate) | each project's share of list-price $ per account and epoch. Never merged into M2 | `usage_day` |
| M2c | Quota per outcome (**experimental**) | M2 x M2b share / M1 for that project and kind. **Undefined when M1 = 0** | derived |
| M3 | Agent-wait minutes | sum over sessions of resolved wait durations (`blocked` kinds), by kind; overlapping waits add up | `wait` |
| M4 | Owner wait-elapsed time | union of intervals when ≥ 1 agent was blocked on the owner (overlap counted once) | `wait` |
| M5 | Open waits | unresolved waits with age, never dropped | `wait` |
| M6 | Cycle time | item open -> acceptance, p50 / p90, per kind | `item`, `acceptance` |
| M7 | Owner interventions and effort | only where observed: human prompts into non-Crius sessions, interrupts, owner overrides, per outcome. Keyboard time only if OTel is added | transcripts, decisions |
| M8 | Crius response | owner prompt -> Crius Stop, p50 / p90 | transcripts |
| M9 | Defects (**candidate** -> confirmed) | candidate signals: reverts, fix commits on the same files within 72 h, BUG after QA, reopen, owner report. Confirmed = linked to an item and its revision, deduped into incidents | `defect` |
| M10 | Review findings (**candidate**) | P1/P2 per reviewed revision; share later confirmed as defects | `evidence`, `defect` |
| M11 | Context cost drivers | $ per main call, share of main $ above 267K, compactions per 100 calls, mean member context | `usage_day`, `session` |
| M12 | Crius cost | Crius sessions' list-price share; observed quota when attributable | `session.role=crius` |
| M13 | Memory recall | % right in top 5, seconds, negative answers that name scope | `eval/recall.jsonl` |
| M14 | Record health | coverage %, heartbeat gaps, unknown attribution share, malformed lines, claims unverified | `coverage`, `session` |

**M2 changed in step 3 (2026-10-16), for the owner:** it was "sum of positive `seven_day_pct` deltas between
samples". The statusline re-runs every 300 s in every open session, and an idle session shows the window it last
received, which can be older and lower. Deltas between interleaved sessions then count the same rise twice (40 -> 30
-> 41 adds 11 instead of 1). The share only grows within an epoch, so the highest value sampled in it is the observed
use; samples of a past epoch measure nothing now.

**Cohorts.** A week's outcomes are shown **provisional** at first and **matured** 14 days later (after the reopen and
defect window). Quality numbers are judged on matured cohorts only.

**Weekly scorecard** (`state/scorecards/<quota-week>.md`, ~40 lines; numbers below show the shape, not data):
```
# Quota week ending 2026-10-23 · built 2026-10-23 07:00 · status complete · def v1
Outcomes   code 14 · research 5 · Falcon 4 (provisional)  | wk 10-09 matured: code 11, 1 reopened
Quota      Max (both personas): 71% observed, uncovered 3% · enterprise: $412 (quotes 260, axi 152), 0 violations
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
- **Sonnet members** (from 10-16, judged 10-23, re-read on the matured cohort): per role, BUG after QA, candidate
  defects, cost per child task, vs the same roles on Opus the week before.
- **Clear-per-item**: member mean context, tasks per session, team $ per team prompt (P2 $21), candidate defects.

**Goodhart guards.** Criteria fixed before work; one outcome per owner goal (splitting adds child tasks, not units);
independent review bound to the revision; output always next to quality; leads never get metrics as targets;
definitions versioned; 4-week trends; n < 10 marked "small sample"; 3 random outcomes opened each week.

---

## 7b. Mistakes, corrections and locked output

We and the agents will make mistakes that tests miss, and some will sit in output that is already out. The rule:
**never rewrite history.** Every fix is a new record linked to what it fixes, and old numbers are updated in the open.

| Kind | Example | What is recorded | Fix path |
|---|---|---|---|
| Escaped defect | a bug found after acceptance | confirmed `defect`; the acceptance is **revoked** (row kept, with reason and defect id); a fix item linked by `fixes=` | the fix item gets its own criteria and acceptance |
| Bad decision | `quotes-D7` turns out wrong | a new numbered decision `Supersedes: quotes-D7` with `Reason:` | work already done under D7 gets a check item if it shipped |
| Wrong data in the store | wrong project or account, a double count, a claim taken as fact | a `correction` row (target, field, old, new, reason, by) | views apply it; the original row stays |
| Locked output already out | merged or deployed code, a sent mail or ADO/PR comment, a released doc | a `locked_output` event linked to the item | fix forward with a new linked item (below) |

**Locked output.** Code: a new fix item; a revert is a normal new commit, never a history rewrite or force-push.
Outward text (mail, ADO or PR comment, PR description): a visible follow-up that says what was wrong, drafted by Crius
or the lead, approved by the owner before it goes out (T3); no silent edit or delete of what others already read. A
released doc: a new version with a dated "Corrected" note at the top. Production is never touched by Crius or leads;
a fix that needs production goes to the owner and the team's normal release path.

**Severity and who decides.**

| Severity | Meaning | Crius does alone | Needs the owner |
|---|---|---|---|
| S1 | others saw it (colleagues, customers, users), or production, money, data loss, security, or personal work on the enterprise account | contain inside its rights (pause the lead, stop runs), open the fix item, draft the follow-up | the fix path and every outward step; this is one of the few pushes |
| S2 | accepted work is wrong but nothing went out (personal repo, internal doc, store data) | revoke, open and assign the fix item, correct store rows, supersede its own decisions | sees it in the digest; may override |
| S3 | cosmetic, or metric noise | a correction record | weekly review only |

Only the owner supersedes an owner decision; Crius may propose.

**Measurement stays honest.** A revoked acceptance counts against the original outcome's week, not the week it was
found. Matured cohorts are recomputed, and a scorecard gets an appended line `revised <date>: code 11 -> 10 (axi#12
revoked, defect D-31)`; the old value stays visible. Finding a mistake is never punished:
- defects are counted by who found them: **found-by-us** (Crius, lead, QA, review, monitors) and **found-by-owner**
  or **found-by-others** (colleagues, users), plus time to detect;
- the bad signals are escapes (found by owner or others) and revocations per accepted outcome, not the count of
  defects we found ourselves;
- a week where found-by-us drops while found-by-others rises is flagged as a possible reporting gap.

**Learning loop.**
- Each confirmed mistake gets one cause class from a short list that may grow: missing test, wrong assumption, stale
  context, silent failure, misread request, harness or tool, review gap, bad data.
- The second confirmed mistake of the same class opens a guard item: a mechanical check (test, hook, health check)
  that goes red on both past cases (the owner's "diagnose repeats" rule). Guards that change config or hooks are
  proposals the owner approves.
- The weekly scorecard run includes Crius's review of corrections, revocations and overrides, with at most 3 guard
  proposals to the owner.

**Owner overrides of Crius.** When the owner reverses a Crius decision, it is recorded as a superseding decision
(`Decided by: owner`, `Overrides: crius`). Override rate per week and per decision class is a Crius-quality number on
the scorecard. It also feeds the autonomy ratchet (DESIGN §4): an override resets that class's approval count.

**Interface.** `record decision --supersedes <id> --reason`; `record defect <item> --found-by us|owner|others
--severity S2 --class "stale context"`; `record revoke <item> --defect <id> --reason` (opens the fix item); `record
correct --corrects <row-id> --field project --value axi --reason`; `show <id> --history` (the whole chain in order);
`find --kind correction|revoked|override --since 30d`.

---

## 8. How Crius works day to day

- **06:30** script: ingest, STATE.md, digest (≤ 60 lines, stamped): needs the owner (top, four lines each), accepted
  yesterday, blocked, open waits, decisions, quota pace, health.
- **Morning start**: a fresh Crius session with CRIUS.md + the digest only (target ≤ 15K tokens). If pace is high
  (≥ 60% observed by day 3 of the epoch, or from day 2 this epoch's %/day above the average %/day of the last 7
  days of samples, which needs 7 days of samples), ONE Remote Control push per epoch: speed, top burners, one
  suggestion. No stop. When the subscription is used up, or from day 2 will be before `resets_at` at the current
  pace (its own push, once per epoch; when it and the pace push are both due in one run, it goes alone and carries
  the pace facts), the push says: only Appxite work (quotes, AXI) may continue, on the Appxite
  enterprise account; personal and scifi work waits until the reset time. It also shows enterprise $ so far this
  week. Every alert fits one bus line (≤ 300 chars) by construction: in a push a name is cut to 10 chars, a list to
  two names and "+N more", $ to 7 chars ($12.3k); in a guard or stale alert a name to 40. `quota` has them in full.
- **During the day**: wakes on bus events (Monitor in the pilot, the Falcon bus later); reads the line and `show
  <project>` (≤ 1K tokens); acts through leads.
- **"What happened with X?"**: Explore runs `find`; if needed `find --deep`; Crius keeps only the cited answer and
  its status. A `partial` or `expired` status is said to the owner as it is.
- **Deciding**: `find --kind decision --active --project X` first, then `record decision`. **Accepting**: `show
  <item>`, then `record accept` only with evidence for every criterion on one revision. Restart at ~150K.
- **Cost of Crius** (ESTIMATE): ~$0.02-0.03 list price per call at ~50K context (review-1016: $0.12 at ~235K);
  150-300 calls/day = ~$4-9/day, a few % of a week. Scripts: 0 tokens. Haiku helpers < 1%. M12 replaces the guess.

---

## 9. Rollout (each step useful alone, each test fails first)

| Step | What | Test |
|---|---|---|
| 1 | `~/.crius/` layout, git-leak test, decision files, item ids with criteria, dated bus grammar + provenance tag in cmux-team templates | leak test fails when a `.db` is tracked or `~/.crius` is in a work tree; 3 decisions incl. a supersede give the right active set; an undated line still parses |
| 2 | `crius-ingest` v1 (transcripts, git, bus, files) -> durable rows; backup; heartbeat + local watchdog | fixture where one request spans two runs gives one row with max usage; partial line, truncation, replacement and late-arrival fixtures; second run adds 0 rows; stop the ingest -> alert in ≤ 30 min |
| 3 | Quota sampling (personal statusline with 30-min heartbeat sample) + `quota` | samples at 60% on day 3 -> one push; at 100% the push names Appxite-only work and the enterprise account; a 2-hour gap with open sessions -> "uncovered", not 0; a scifi session on the enterprise account -> S1 |
| 4 | `find`, `show`, `health`, `find --deep`, redaction on every sink | recall v0 (20 questions from STATUS.md: "when did mail stop" -> 09-21; "which commit armed k3s hooks" -> cf62176) ≥ 80% top 5; a silent lead appears in `health`; a 40-day-old question -> `expired`; a superseded decision is marked; a true "no" names its scope; a planted secret in a bus line never reaches output, error text or an alert |
| 5 | Digest + Crius daily start | start ≤ 15K tokens; 5 owner questions answered with no lead tab opened |
| 6 | `record accept` + scorecard; first descriptive read 10-23 next to the review-1016 scripts | accept refuses evidence on a different sha; numbers match review-1016 within 2%; restore-after-expiry passes; a revoke keeps the acceptance row and lowers the original week; a correction changes the view, not the row; `show --history` lists the chain; a second mistake of one class opens a guard item |
| 7 | Minimal hooks for live waits (if needed) | a scratch permission prompt -> open wait -> resolved with paired ids; a session with no SessionStart line -> alert |
| 8 | OTel (only if justified) | allowlist drops `user.email`; session count matches transcripts |
| 9 | Falcon source (read back to the Mac) | k3s run count in the store = Falcon's list for a day |

**Do not build:** a graph DB or Graphiti; Mem0, Letta, Zep; Langfuse, Phoenix; embeddings over transcripts; LLM calls
at ingest; a dashboard UI; a trials engine or second backend now; storing transcript text; an MCP server before the
voice connector; size classes; any metric with no action attached.

---

## 10. Risks and decisions

**Risks.**
1. Silent gaps. Guard: local watchdog, per-resource coverage, statuses, nightly re-count.
2. Transcript format changes. Guard: unknown shapes alerted; fixtures per Claude Code version.
3. Quota is observed only where sampled; per-project quota and enterprise $ are list-price estimates.
4. Small samples and Goodhart: matured cohorts, 4-week trends, descriptive trials.
5. Personal work billed to the employer by mistake. Guard: account tag + statusline check, S1 push.
6. Lost durable rows if the Mac disk dies: nightly backup + restore test; no offsite copy while everything is local.
7. Over-building: drop any part nobody reads for 4 weeks.

**Owner decisions, 2026-10-16.**
- **D1. One Max account for both personas = one weekly quota.** When it is used up, only Appxite work (quotes, AXI)
  may continue, on the Appxite enterprise account, which bills API cost to the employer. So: enterprise $ is tracked
  apart (M2e), personal and scifi work never runs on it (account guard, §5), and `quota` and the pace push show both.
- **D2. Everything local (Mac only) for now.** A remote Crius may come later (the owner holds `crius.ai`; note only).
- **D3. Config changes approved, one at a time as each step comes:** personal statusline with quota samples (step 3),
  the `FINDINGS` line in the codex and quick-review agents (step 6), minimal hooks (step 7, if needed), OTel with an
  allowlist (step 8, if justified). Each is still announced before it is applied.
- **D4. Notifications by Remote Control push only, few and high-signal.** No Telegram or Discord until the system is
  mature. General rule: one channel; push only what needs the owner now; the rest goes to the digest (§5).

**Decided by me (say if any is wrong):** keep transcripts at 30 days; acceptance for non-code = Crius after an
independent review bound to the artifact hash, or the owner when the criteria name them; no size classes; private
state in `~/.crius/` outside git; OTel deferred; CLI before MCP; trials stay descriptive; pace threshold ≥ 60% by
day 3 of the epoch or faster than the 7-day average.
