# Memory and measurement for Crius

Research date: 2026-10-16. Scope: web research only, no code or account changes.
Marks: **CONFIRMED** = I read it in the primary source. **INFERRED** = my conclusion, or a
claim seen only in secondary sources. Vendor numbers are marked as vendor numbers.

---

## Recommendation (read this first)

**Short answer:** keep memory as plain files with exact IDs and full-text search. Add one
derived layer that is computed from ground truth (git, CI, transcripts, hooks), not from what
agents say. Measure with Claude Code's own telemetry and transcripts. Do not add a new memory
product or a graph database.

### Adopt

1. **Event log as the source of truth, status as a view of it.** Each lead and team keeps an
   append-only log of typed events (`decision`, `done`, `blocked`, `handoff`, `superseded`),
   one line each, with date, session id and a reference (commit sha, PR, file). STATUS.md is a
   snapshot made from that log plus git, not hand-written prose that drifts. This is the
   pattern Anthropic uses in its long-running harness (progress file + git log + feature list)
   and the pattern of the 2026 event-sourced agent-memory papers. (INFERRED from sections 1 and 3.)
2. **Numbered decision records with "supersedes".** One short file per decision, with a date
   and a `supersedes: D-012` line. This gives you the one thing temporal graphs (Zep/Graphiti)
   are really good at, "what is true now and what was true before", for free. (INFERRED.)
3. **Machine-readable status fields that agents can only flip.** Anthropic found the model is
   less likely to wrongly overwrite JSON than Markdown, and told agents to change only a
   `passes` field. Use the same idea for task state. (CONFIRMED source; fit INFERRED.)
4. **Reports derived from ground truth.** Crius builds the daily/weekly digest from git log,
   `gh` PR and CI state, transcript JSONL and hook timestamps. Agent self-reports are notes, not
   facts. Reason: models grade their own work too kindly (Anthropic, CONFIRMED) and humans
   misjudge AI speed-up by ~40 points (METR, CONFIRMED).
5. **Search order: exact key, then full-text, then embeddings.** Exact IDs and grep first,
   context-mode FTS5 second, Falcon's MiniLM vectors only for fuzzy "something like this"
   questions. Run searches in a subagent that returns a 1-2k token answer, so Crius stays
   small. (Anthropic pattern CONFIRMED; order INFERRED.)
6. **Claude Code OpenTelemetry export** (metrics + events) to a small collector. It already
   emits tokens by type, estimated cost, active time (user vs CLI), commits, PRs, lines changed,
   and per-request events with `session.id`, `query_source` and agent/skill names.
   (CONFIRMED.) Store it in Falcon's Postgres or Prometheus on the k3s cluster.
7. **Hook timestamps for waiting time.** `Notification` (`idle_prompt`, `permission_prompt`,
   `agent_needs_input`) and `UserPromptSubmit`/`Stop` give "agent waits for owner" gaps.
   Approximate, but enough. (Hooks CONFIRMED via secondary docs; method INFERRED.)
8. **A small retrieval test set.** 30 real questions with known answers ("when did we decide
   X", "which PR fixed Y", "what is quotes blocked on"). Score hit-in-top-5 and time. Re-run
   when memory changes. Anthropic: start evals with a handful of cases, effects are large early.
   (CONFIRMED source; test set INFERRED.)

### Skip (and why)

| Skip | Why |
|---|---|
| **Letta (MemGPT) as a platform** | It is a full agent runtime. You already have one (Claude Code). Its own best-known result is that a plain file + grep agent scored 74.0% on LoCoMo, beating Mem0's reported 68.5%. Take the lesson, not the product. |
| **Mem0** | Built for chat personalisation (user facts). Needs an LLM call to extract and merge facts on every write, which on a Max plan means a separate API key or a local model. It rewrites facts, so you lose provenance. Its benchmark claims are disputed. |
| **Zep / Graphiti** | Real strength: time-aware facts (valid from / invalid from). Cost: a graph DB (Neo4j or FalkorDB) plus LLM calls per ingested episode (defaults to OpenAI). Decision records with `supersedes` give most of the value. |
| **LangGraph memory / checkpoints** | Only useful if you build agents in LangGraph. The idea (thread checkpoints + a cross-thread store) is the same as STATUS snapshot + memory files. |
| **Langfuse (for now)** | Good tool, but self-host wants ~4 cores / 16 GiB with Postgres + ClickHouse + Redis + S3. Heavy for a small k3s cluster and one owner. Revisit if you need trace-level debugging. |
| **Lines of code, commit counts, token totals as "productivity"** | Activity, not outcome. Classic Goodhart trap (section 5). |

### Is a graph worth it for a solo owner?

**No, not as the main store.** (INFERRED, from the evidence below.)

- On the public benchmarks, a file + grep agent matched or beat dedicated memory systems
  (Letta 74.0% vs Mem0 68.5% on LoCoMo). Mem0's graph variant was only ~2% better than its
  own non-graph version. (CONFIRMED.)
- Zep's gains are largest on temporal questions. You can get that with dated, superseding
  records. (INFERRED.)
- The benchmarks measure chat memory about a user, not "what did five coding teams do". And
  they are shaky: LoCoMo conversations fit in a modern context window, a full-context baseline
  beat Mem0's best score, and an audit claims ~6% of its answer key is wrong. (Partly CONFIRMED,
  audit is vendor-published.)
- Falcon's people/topic graph can stay where it already earns its keep (who is who, Appxite
  topics). Don't extend it to session history.

### Suggested order

1. Event log format + decision records with `supersedes` (cheap, biggest gain).
2. Derived digest from git/CI/transcripts (no infra).
3. Hook timestamps for waiting time.
4. OTel export to a collector; dashboards later.
5. Retrieval test set; only then consider changes to Falcon search.

---

## 1. Anthropic's own guidance

**Context engineering (Sep 29, 2025).** CONFIRMED.
- Context is a finite "attention budget". As tokens grow, recall drops ("context rot").
  Goal: "the smallest set of high-signal tokens".
- Prefer "just in time" retrieval: keep light references (file paths, stored queries, links)
  and load data with tools when needed. Claude Code is the example: CLAUDE.md loads up front,
  glob/grep fetch the rest.
- Three tools for long work: **compaction**, **structured note-taking** (agent writes notes
  outside the context and reads them back after a reset), and **sub-agents** that may use tens
  of thousands of tokens but return "often 1,000-2,000 tokens".
- "Do the simplest thing that works."

**Memory tool (API).** CONFIRMED.
- File-based and client-side: Claude asks to view/create/edit files under `/memories`; your
  code stores them. It checks memory before starting a task.
- Anthropic recommends pairing it with compaction: compaction keeps context small, memory keeps
  what must survive summarisation.
- Fit: this is an API feature. In Claude Code, auto-memory plays the same role. I did not
  find a way to use the memory tool itself from a Max-plan Claude Code session. (INFERRED.)

**Multi-agent research system (Jun 13, 2025).** CONFIRMED.
- Agents use ~4x the tokens of chat; multi-agent systems ~15x.
- "Most coding tasks involve fewer truly parallelizable tasks than research." Multi-agent pays
  off for wide, parallel, high-value work.
- The lead must give each subagent an objective, output format, tools and boundaries, or
  agents duplicate work.
- Save the plan to external memory before context runs out; spawn fresh subagents with clean
  context and careful handoffs.
- Judge **end state**, not each step. Start evals small.
- Fit: on a weekly quota, the 15x figure is the main cost lever. Teams of 3-8 sessions should
  be reserved for work that is truly parallel. (INFERRED.)

**Effective harnesses for long-running agents (Nov 26, 2025).** CONFIRMED.
- Two failure modes: trying to one-shot the whole job, and declaring victory too early.
- Fix: an initializer writes `init.sh`, `claude-progress.txt`, a feature list with every item
  `passes: false`, and a first git commit. Every later session reads `pwd`, the progress file,
  git log and the feature list, then picks one failing item.
- JSON for the feature list, because the model is less likely to wrongly change JSON than
  Markdown. Agents may only flip `passes`.
- Compaction alone is not enough: it "doesn't always pass perfectly clear instructions to the
  next agent".

**Harness design for long-running apps (Mar 24, 2026).** CONFIRMED.
- Planner / generator / evaluator. "Agents reliably skew positive when grading their own work";
  a separate, skeptical evaluator is easier to tune than a self-critical generator.
- A "sprint contract" agrees what "done" means before code is written.
- Cost is real: solo run 20 min / $9 vs full harness 6 h / $200; a later version 3 h 50 min /
  $124.70.
- "Every component in a harness encodes an assumption about what the model can't do on its
  own" — re-test and remove pieces when a new model lands.
- Fit: quality numbers must come from an independent checker (tests, codex review, browser
  check), never from the agent that did the work. (INFERRED.)

**Claude Code memory and best practices.** CONFIRMED.
- Auto-memory: the first 200 lines or 25 KB of MEMORY.md load each session; topic files load
  on demand. Claude Code warns when MEMORY.md nears the limit. Subagents do not load the main
  auto-memory (forks do).
- CLAUDE.md: target under 200 lines; imports do not reduce context cost; contradictions make
  Claude pick one at random. "Would removing this cause Claude to make mistakes? If not, cut
  it." CLAUDE.md is advisory; hooks are deterministic.
- Use `/clear` between unrelated tasks; use subagents to keep research out of the main context;
  verify with a Stop hook or a fresh verification subagent, not the worker itself.

---

## 2. Agent memory systems

| System | Best at | Retrieval evidence | Run cost | Self-host | Fit here |
|---|---|---|---|---|---|
| **Letta (MemGPT)** | An agent that edits its own memory blocks (in-context "core" + out-of-context "archival") | Own blog: file + grep agent on gpt-4o-mini = **74.0%** LoCoMo (CONFIRMED) | Server + Postgres; LLM calls for the agent itself | Yes (Docker; no auth unless `SECURE` set, per secondary guide) | Low. Replaces Claude Code rather than helping it. |
| **Mem0** | User-fact memory for chat apps; extract → merge → retrieve | Paper (arXiv 2504.19413): +26% relative over OpenAI memory on LoCoMo judge score, **91% lower p95 latency, >90% fewer tokens** than full context; graph variant ~2% over base (CONFIRMED). Zep says a correct Zep setup scores 75.14% vs Mem0's best ~68%, and full-context ~73% beat Mem0 (vendor rebuttal, CONFIRMED as their claim) | LLM call per write for extraction; vector DB | Yes, but extraction needs an LLM (API key or local model) | Low. Wrong shape (user facts), loses provenance. |
| **Zep / Graphiti** | Facts that change over time; bi-temporal edges with automatic invalidation; hybrid semantic + keyword + graph search | Paper (arXiv 2501.13956): DMR 94.8% vs MemGPT 93.4%; LongMemEval up to **+18.5% accuracy, -90% latency** vs baselines (CONFIRMED). Vendor blog: biggest gain on temporal questions, slightly behind full context on knowledge updates (secondary) | Graph DB (Neo4j / FalkorDB; Kuzu deprecated) + LLM per episode (OpenAI default; Anthropic, Gemini, local OpenAI-compatible supported); low default concurrency to avoid 429s | Yes (Graphiti is open source) | Medium-low. Good idea, heavy machinery. Copy the "valid from / superseded" idea into files. |
| **LangGraph memory** | Checkpoints per thread + cross-thread store with optional semantic search (Postgres) | No memory benchmark found | Cheap if you already use LangGraph | Yes | None, unless you build agents in LangGraph. |
| **Plain files + grep/FTS** | Exact, auditable, diffable, git-versioned memory; models are heavily trained on file tools | Letta result above; Anthropic's harness and memory tool both use files (CONFIRMED) | Near zero | Yes | **High.** Already in place (auto-memory, STATUS.md, logs, context-mode FTS5). |

**About the benchmarks.** CONFIRMED unless marked.
- LongMemEval (arXiv 2410.10813): 500 questions on five abilities — extraction, multi-session
  reasoning, temporal reasoning, knowledge updates, abstention. Commercial assistants and
  long-context LLMs drop ~30% in accuracy. Its design tips (split sessions, add facts to index
  keys, time-aware query expansion) map well onto files: one event per line, tags, dates.
- LoCoMo is weaker: conversations of ~16-26k tokens fit in context (Zep). An audit by a memory
  vendor claims 6.4% of answers are wrong and the LLM judge accepts up to 63% of deliberately
  wrong answers (vendor-published, not replicated — INFERRED reliability).
- None of these benchmarks test "supervise coding teams". Your own 30-question test set
  matters more than any of them. (INFERRED.)

**Event-sourced memory for coding agents (2026 preprints).** INFERRED (secondary summaries).
- PROJECTMEM (arXiv 2606.12329): local append-only log of typed events (issue, attempt, fix,
  decision), projected into summaries over MCP, plus a gate that warns before repeating a failed
  fix. Small self-study (10 projects, 207 events).
- ESAA-Conversational (arXiv 2606.23752): event log for handoff between different coding agents.
- Weak evidence, but they match Anthropic's harness pattern and your existing team logs.

---

## 3. Event log, snapshot, decision records, reliable reporting

**Snapshot vs log.** Use both, with one rule: the log is the truth, the snapshot is derived.
(INFERRED; matches event sourcing and the Anthropic harness.)
- **Log** (append-only, one line per event): `2026-10-16T09:20 | quotes | D-031 | decision |
  "use svc_quote v2 for CPQ" | session=… | ref=PR#…`. Never edited, only superseded.
- **Snapshot** (STATUS.md / JSON): current goal, open items, blockers, last verified state.
  Rebuilt from the log + git. If the snapshot and the log disagree, the log wins and a check
  flags it.
- **Decision records**: numbered, dated, 5-10 lines (context, decision, who decided — owner or
  agent — and what the agent knew at the time). `supersedes:` and `superseded-by:` links give a
  time line. A 2026 ADR write-up recommends recording who decided and the agent's context
  (secondary, INFERRED). Also noted there: ADR folders get forgotten — so Crius should surface
  the current ones in digests, not expect agents to browse them.

**Making reports reliable when agents forget.**
1. **Derive, don't ask.** Count done work from merged PRs, passing CI, closed tasks, commits —
   not from "I finished X". Anthropic: compaction loses detail; self-grading is too kind
   (CONFIRMED). METR: people think AI sped them up ~20% when it slowed them 19% (CONFIRMED).
2. **Self-reports are hypotheses.** Crius checks each claimed "done" against a ground-truth
   signal and marks it verified / unverified.
3. **Typed events beat prose.** A fixed small set of event types makes grep and FTS exact and
   lets the digest be computed. (INFERRED.)
4. **Session start ritual.** Like the harness: read snapshot, last N log lines, git log, then
   act. Cheap, and it stops sessions from working from a stale picture. (CONFIRMED pattern.)
5. **Hooks enforce, prompts suggest.** Writing a log line at `Stop` / `SubagentStop` /
   `PreCompact` via a hook is reliable; asking agents to "remember to log" is not. (CLAUDE.md
   advisory vs hooks deterministic — CONFIRMED.)

---

## 4. LLM observability

**Claude Code's own OpenTelemetry export.** CONFIRMED (official docs).
- Signals: metrics, events (logs protocol), and beta distributed traces that link each prompt
  to its API calls and tool runs.
- Metrics: `session.count`, `lines_of_code.count` (added/removed), `pull_request.count`,
  `commit.count`, `cost.usage` (USD), `token.usage` (input / output / cacheRead /
  cacheCreation, by model), `code_edit_tool.decision`, `active_time.total` (type `user` vs
  `cli`).
- `api_request` events carry `cost_usd`, `duration_ms`, token counts, `request_id`,
  `query_source` (main thread, compact, or subagent name), and agent / skill / plugin / MCP
  names. `session.id` can be attached to metrics.
- Privacy: prompt text and tool details are off by default (`OTEL_LOG_USER_PROMPTS`,
  `OTEL_LOG_TOOL_DETAILS`). Keep them off unless needed.
- Exporters: OTLP, Prometheus, console. Default metric interval 60 s.
- On a Max plan, `cost_usd` is an API-price estimate, not your bill. Use it as a common unit to
  compare sessions; the real limit is the weekly quota. (INFERRED. I did not find a doc line
  that says telemetry is disabled for subscription logins.)

**Transcripts (JSONL).** Already on disk; carry timestamps, token usage, tool calls. `ccusage`
reads them for daily / session / 5-hour-block reports; its author says cost figures are
estimates. Works for backfill with no infra. (CONFIRMED via secondary pages.)

**Hooks for waiting time.** `Notification` matchers `idle_prompt` (after ~60 s idle),
`permission_prompt`, and `agent_needs_input` (v2.1.198+, agent view open). Known gaps:
AskUserQuestion prompts and "idle but background agents still running" are hard to tell apart.
(Secondary docs and GitHub issues — INFERRED precision.)

**Self-hostable backends.**

| Tool | What it gives | Footprint | Fit |
|---|---|---|---|
| **OTel Collector + Prometheus + Grafana (+ Loki for events)** | Metrics dashboards, event search | Light; standard on k3s | **Best first step.** (INFERRED) |
| **Falcon Postgres** | Store events / digests next to existing memory; SQL for weekly report | Already running | **Good** for the derived ledger. (INFERRED) |
| **Arize Phoenix** | Traces over OTLP, LLM / code / human evals, datasets, experiments | Free self-host, no feature limits; Docker image and Helm chart (CONFIRMED) | Optional, for trace review and evals. Claude Code does not emit Phoenix's OpenInference format, so views may be thin (INFERRED). |
| **Langfuse** | Traces, sessions, evals, cost; Claude Code integration via a Stop hook that reads transcripts | Compose needs ≥4 cores / 16 GiB; Postgres + ClickHouse + Redis + S3 (CONFIRMED) | Later, if you need per-turn trace debugging. |

**OpenTelemetry GenAI semantic conventions.** Define spans like `invoke_agent`,
`execute_tool`, token usage attributes. As of mid-2026 all GenAI conventions are still
"Development" (not stable) and moved to their own repo in June 2026 (secondary sources —
INFERRED). Advice: pin versions, expect renames. Claude Code uses its own `claude_code.*`
names, so you don't depend on them.

---

## 5. Metrics

**What serious teams found.**
- **DORA 2025** (≈5,000 respondents, ~90% use AI): AI now correlates with higher throughput but
  also higher instability (more change failures and rework). AI is "an amplifier" of existing
  strengths and weaknesses. Only 2 of 7 team clusters gain throughput without more failures.
  (InfoQ + secondary summaries — CONFIRMED for "amplifier", rest INFERRED.)
- **METR 2025 RCT** (16 devs, 246 tasks): AI made experienced devs **19% slower**; they
  expected 24% faster and still believed 20% faster afterwards. (CONFIRMED.)
- **METR Feb 2026 update**: estimates now point to a speed-up (time change -18% for returning
  devs, CI -38% to +9%; -4% for new devs), but METR calls the data unreliable, partly because
  time-on-task is unreliable for people running several agents at once. (CONFIRMED.) That is
  exactly your setup — so measure outcomes and owner time, not session time.
- **DX AI Measurement Framework**: three dimensions — utilisation, impact, cost — with a
  baseline before change. (Vendor, CONFIRMED as their framework.)

**A metric set for Crius** (INFERRED; each tied to a ground-truth source).

| Dimension | Metric | Source |
|---|---|---|
| Output | Accepted units per week: merged PRs + tasks closed with a passing check | `gh`, CI, event log |
| Cost | Share of weekly quota; tokens by type (count cache reads separately) per lead/team; `cost_usd` as a common unit | OTel `token.usage`, `api_request`, transcripts |
| Output per cost | Accepted units per 10% of weekly quota, per lead | derived |
| Speed | Cycle time: task opened → accepted (median, p90) | event log, git |
| Owner waiting on agents / agents waiting on owner | Minutes from `Notification` to next `UserPromptSubmit`; count of interruptions per accepted unit | hooks |
| Owner effort | `active_time.total` type `user` per accepted unit | OTel |
| Quality | Change failure rate (reverts, hotfix within 7 days); first-pass CI rate; deep-review findings per change; rework (same lines changed again within 14 days) | git, CI, codex reports |
| Memory health | Retrieval test set: hit-in-top-5 and seconds per answer; stale-snapshot flags | test set, log vs snapshot check |

**Pitfalls.**
- **Goodhart:** once a number is a target, agents (and people) optimise it. Lines of code,
  commits and PR counts are easy to inflate. Pair every output number with a quality number.
- **Activity is not outcome.** Tokens, sessions and tool calls measure effort. Anthropic found
  tokens explain much of research-agent performance, but more tokens are not more value.
- **Self-report bias.** METR shows a ~40-point gap between felt and real speed. Never use
  "how productive did it feel" as the metric.
- **Concurrency breaks time metrics.** With many agents at once, wall-clock per task is
  misleading (METR 2026). Use owner minutes and cycle time instead.
- **Small n.** One owner, a few teams: week-to-week noise is big. Look at 4-week trends.

---

## Risks and pitfalls

1. **Silent memory loss.** Compaction and handoffs drop detail (Anthropic). A search that finds
   nothing must say what scope it searched; "not found" and "failed to search" must differ.
2. **Snapshot drift.** Hand-edited STATUS.md goes stale while the log moves on. Rebuild or
   check it against the log; flag disagreements.
3. **Index bloat.** MEMORY.md beyond 200 lines / 25 KB is silently not loaded; CLAUDE.md over
   ~200 lines lowers adherence. Prune regularly.
4. **Benchmark marketing.** Memory vendors dispute each other's numbers; LoCoMo is weak. Trust
   your own test set.
5. **Hidden LLM costs.** Mem0 and Graphiti call an LLM on every write. On a Max plan that means
   an API key or a local model — new cost or new quality risk.
6. **Multi-agent token burn.** ~15x chat tokens. Teams on non-parallel work waste weekly quota.
7. **Self-grading.** Agents praise their own output. Quality must come from tests, deep review
   or a separate evaluator.
8. **Telemetry privacy.** Prompt and tool-detail logging are off by default; turning them on
   puts code, paths and possibly secrets in the telemetry store.
9. **Unstable standards.** OTel GenAI conventions are not stable; pin versions.
10. **Cost estimate ≠ quota.** `cost_usd` and ccusage are API-price estimates; your real limit is
    the weekly quota.
11. **Measurement theatre.** A dashboard nobody acts on is cost without value. Start with one
    weekly digest and three numbers: accepted units, quota share, owner wait minutes.
12. **Over-building Crius.** Every harness part encodes an assumption about model limits that
    may go stale with the next model (Anthropic). Re-test and remove parts.

### What I could not confirm

- Whether the API memory tool can be used from Claude Code on a Max plan (searched Anthropic
  memory-tool docs and Claude Code memory docs; found no such path).
- Whether Claude Code OTel export has any limits for subscription logins (searched the
  monitoring docs; no restriction found).
- Official OTel GenAI status page (opentelemetry.io pages redirected; status taken from 2026
  secondary articles).
- Letta's current docs pages (404 / redirect); Letta facts come from its blog and secondary
  hosting guides.
- Mem0 OpenMemory's default LLM setup (only third-party Mem0-style MCP servers found).
- DORA 2025 primary report (used InfoQ and summaries).

---

## Sources

Anthropic
- Effective context engineering for AI agents (2025-09-29): https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- How we built our multi-agent research system (2025-06-13): https://www.anthropic.com/engineering/multi-agent-research-system
- Effective harnesses for long-running agents (2025-11-26): https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
- Harness design for long-running application development (2026-03-24): https://www.anthropic.com/engineering/harness-design-long-running-apps
- Memory tool docs: https://docs.claude.com/en/docs/agents-and-tools/tool-use/memory-tool
- Claude Code memory: https://docs.anthropic.com/en/docs/claude-code/memory
- Claude Code best practices: https://code.claude.com/docs/en/best-practices
- Claude Code monitoring (OpenTelemetry): https://docs.anthropic.com/en/docs/claude-code/monitoring-usage
- Claude Code hooks: https://docs.anthropic.com/en/docs/claude-code/hooks

Memory systems and benchmarks
- Letta, Benchmarking AI Agent Memory: Is a Filesystem All You Need? (2025-08-12): https://www.letta.com/blog/benchmarking-ai-agent-memory
- Mem0 paper, arXiv 2504.19413: https://arxiv.org/abs/2504.19413
- Zep paper, arXiv 2501.13956: https://arxiv.org/abs/2501.13956
- Zep rebuttal, "Lies, Damn Lies, & Statistics": https://blog.getzep.com/lies-damn-lies-statistics-is-mem0-really-sota-in-agent-memory/
- Graphiti repository: https://github.com/getzep/graphiti
- LongMemEval, arXiv 2410.10813: https://arxiv.org/abs/2410.10813
- LoCoMo audit (vendor): https://dev.to/penfieldlabs/we-audited-locomo-64-of-the-answer-key-is-wrong-and-the-judge-accepts-up-to-63-of-intentionally-33lg
- LangGraph stores: https://docs.langchain.com/oss/javascript/langgraph/stores.md
- LangGraph semantic search changelog: https://changelog.langchain.com/announcements/semantic-search-for-langgraph-s-long-term-memory
- Letta Docker guide: https://docs.letta.com/guides/docker/index.md

Event sourcing and decision records
- PROJECTMEM, arXiv 2606.12329: https://arxiv.org/pdf/2606.12329
- ESAA-Conversational, arXiv 2606.23752: https://arxiv.org/pdf/2606.23752
- ADRs with Codex CLI (blog): https://codex.danielvaughan.com/2026/04/28/codex-cli-architecture-decision-records-adr-automated-governance/

Observability
- Langfuse self-hosting: https://langfuse.com/self-hosting
- Langfuse Docker Compose requirements: https://langfuse.com/self-hosting/deployment/docker-compose
- Langfuse Claude Code integration: https://langfuse.com/integrations/developer-tools/claude-code
- Arize Phoenix self-hosting: https://arize.com/docs/phoenix/self-hosting
- OTel GenAI semantic conventions: https://opentelemetry.io/docs/specs/semconv/gen-ai/
- OTel GenAI status (secondary): https://www.dash0.com/knowledge/opentelemetry-genai-semantic-conventions-explained and https://klu.ai/glossary/llm-session-tracing-opentelemetry
- ccusage: https://npmjs.com/package/ccusage

Metrics
- METR early-2025 study: https://metr.org/blog/2025-07-10-early-2025-ai-experienced-os-dev-study/
- METR Feb 2026 update: https://metr.org/blog/2026-02-24-uplift-update/
- DORA 2025 (InfoQ): https://www.infoq.com/news/2025/09/dora-state-of-ai-in-dev-2025
- DORA 2025 summary: https://blog.robbowley.net/2025/10/01/dora-2025-ai-assisted-dev-report-some-benefit-most-dont/
- DX AI Measurement Framework: https://getdx.com/ai-measurement-framework/
