# Agent team tools to research later

Started 2026-10-21. Tools that do parts of what Crius does by hand. Not now: try them after 2026-11-01, in
`~/Projects/yolo/`, one small project at a time. The aim is to take the best part of each into Crius, not to
move Crius onto one of them. Everything below is from each tool's own pages and write-ups; none was installed
or read at the code level yet.

## What Crius has today (the baseline)

- cmux tabs, one per session; `cmux send` types into a tab, `cmux read-screen` reads it.
- Bus logs `~/.crius/bus/*.log`: append-only lines (DONE, NEED, ASK, ANSWER, VERIFIED ...).
- `team-wait.sh`: wakes Crius on bus lines meant for it.
- `promptwatch.sh`: finds permission prompts by reading tab screens.
- Crius re-runs a lead's checks on a clean checkout before VERIFIED. No tool below does this part.
- Resume files (CRIUS-RESUME.md, BRIEF.md) carry work from one session to the next by hand.

## The tools

### OpenRig (github.com/mvschwarz/openrig)

A team of Claude Code and Codex sessions in tmux, described in one YAML file and started with one command.
Apache 2.0, npm `@openrig/cli`, Node 22/24 plus tmux.

**Code read 2026-10-22** (clone of main e124a74, v0.6.12; read only, nothing installed or run). Repo: created
2026-04-01, about 4,200 commits, 8K stars, 160 open issues, 69 contributors but one author wrote 2,136 commits
(next: 218). About 270K lines of TypeScript, 1,787 test files.

What it is: a local daemon (`packages/daemon`, Hono HTTP on port 7433, SQLite in WAL mode under `~/.openrig`),
a CLI (`@openrig/cli`), a TUI and a web UI. Agents are ordinary Claude Code or Codex processes in tmux panes.
Flow: CLI/TUI -> HTTP -> daemon -> SQLite queue -> tmux paste into the pane; Claude Code hooks post back to
the daemon. It routes and restores work; it does **not** check work (no test re-run, no VERIFIED-style gate).

How it does what we do by hand (paths under `packages/daemon/src/`):
- **Session state from Claude Code hooks**, not screen regex: `PermissionRequest` and `Notification`
  (`permission_prompt`, `elicitation_dialog`) -> needs input; `idle_prompt`, `Stop`, `SessionEnd` -> idle;
  `UserPromptSubmit`, `PreToolUse` -> running (`domain/agent-activity-store.ts:351-361`, checked by Crius).
  Screen sampling only as fallback (`domain/seat-activity-service.ts`).
- **Grey suggestion text** told apart from typed text by the faint text style (`domain/session-transport.ts:262-279`,
  checked). This is the cause of our 10-22 lost wakes (wakewatch read grey text as a draft, 8 times).
- **Draft guard:** refuses to type into a box with unsent text (`draft_input_busy`,
  `domain/seat-delivery-guard.ts:375`, checked) and keeps the message queued.
- **Messages as SQLite rows:** the row is stored once; the wake retries until delivered, and the retry state is
  rebuilt from a transition log, so a daemon restart does not reset or lose it (`domain/queue-wake-ladder.ts:12-19`).
- **Restore** with each session's exact `--resume` id (`adapters/claude-resume.ts`); managed `/compact`
  (`domain/claude-compaction-enforcer.ts`); usage and cost columns per session, Claude quota read from the
  statusline.
- **Adopts running sessions** (`rig discover`, `rig adopt`, `rig bind`), but only tmux sessions. Ours run in
  cmux: a live session moves by restarting it in tmux with `claude --resume <id>` (about a minute of pause).
- Cost: no model calls of its own (no Anthropic or OpenAI client in `packages/*/src`); outbound calls only for
  optional OpenRouter, Slack and ntfy. No telemetry library found.
- Not confirmed in code: the whole team coming back after a reboot (changelog only), an end-to-end test that
  drives real Claude Code, Codex handoff, the YAML field list.
- Risks: one main author; pre-1.0 and changing daily; tmux, not cmux; default permission floor is
  `acceptEdits`, and YOLO mode adds `--dangerously-skip-permissions` (`adapters/yolo-mode.ts`).

Verdict: far ahead of Crius on plumbing (watching, delivery, restore), absent on judgement (checking claims,
done rule, goal reports). Fit: OpenRig under Crius, not instead of it.

Best parts to take:
- **Seats:** fixed role addresses (`dev-owner@project`) that stay the same across sessions. Ours are names
  like `quotes:lead`, but the session behind a name is found by hand.
- **Queues per seat:** work items wait in a queue owned by the assignee, not only as bus lines.
- **Team in one file + one start command**, and the team comes back after a reboot. Our relaunch is manual.
- **Hook-based session state** and the **faint-text check**, even if we never run OpenRig.

#### OpenRig trial (owner asked 2026-10-22; waits for the owner's go)

Steps: (1) side trial in `~/Projects/yolo/`, two test sessions on Haiku, own tmux server (`tmux -L`), own
Claude config folder, pinned OpenRig version, 1-2 hours; (2) if it passes, move one Quotes builder (P6 or QA1)
with `--resume` and run it beside the others for half a day; (3) the whole team only after M1, never mid-push.
Its hook install into a session's settings needs the owner's OK for steps 2 and 3.

Tests for step 1 (each PASS/FAIL with command, output and timestamps):

| # | Test | Pass when |
|---|------|-----------|
| 1 | Idle session with grey suggestion text gets a message | delivered, not held |
| 2 | Unsent typed text in the box, then a message | held, no mixed text, delivered once the box is empty, nothing lost |
| 3 | Session hits an "ask" permission rule | "needs input" within 5 s, from the hook |
| 4 | A dialog is open (question box, menu) | "needs input"; nothing typed into the dialog |
| 5 | Session finishes its turn | idle within seconds, with a timestamp (our 10-minute rule becomes a query) |
| 6 | Daemon killed mid-delivery, restarted | message arrives exactly once |
| 7 | One session's process killed | restored with `--resume`; it still knows a code word told earlier |
| 8 | Daemon and tmux both stopped (a reboot) | both sessions back with their conversations |
| 9 | A script reads each session's state as data and sends with a delivery status | could replace team-wait, promptwatch and wakewatch's typing |
| 10 | tmux inside a cmux tab | tabs still visible; `cmux read-screen` still works |
| 11 | Overhead: CPU, memory, network connections, tokens added to sessions | no outside connections we did not turn on, no hidden model calls |
| 12 | Where its hooks are installed | only the test sessions' settings, never `~/.claude/settings.json` |
| 13 | Default permission mode | not YOLO |

A real usage limit cannot be triggered on demand: only check that it reads the quota numbers. The report
ends with what it can replace in our scripts, what it cannot (clean-checkout checks stay with Crius), and
whether to do step 2.

### Herdr / Herder (terminal agent multiplexer)

Rust terminal for many agents at once, pre-1.0, open source. Sees each agent's state: working, waiting on a
permission prompt, idle, finished. Herdr has a socket API. (A $6M seed / YC claim comes from one secondary
article; unconfirmed.)

Best parts to take:
- **Agent state from the terminal itself.** This is what `promptwatch.sh` does by reading screens, and what
  the IDLE rule tries to catch. The closest match to cmux; could replace most of our home-made watching.
- **Socket API**, so scripts can drive it the way we drive cmux.

### Ghostex (ghostex.dev, github.com/maddada/Ghostex)

Native Rust/GPUI desktop app on Ghostty terminals for 20+ agents (Claude Code, Codex, Gemini, Cursor ...).
macOS, Linux, Windows, iOS, Android; phone pairs by QR code; one computer can drive agents on others.
Release 10.23.0 on 2026-10-14. No price or licence seen on the site.

Best parts to take:
- **Handoff:** move a running chat to another agent (Claude to Codex) with the whole conversation. For us:
  a builder that hits its quota hands the task to Codex without a new brief.
- **Chats are real CLI sessions**, so they can be resumed in the normal CLI (no lock-in).
- **Phone app** to follow and answer sessions; may be easier for the owner than the Desktop import.
- Pin, tag and park sessions; kanban; plug-ins (Git, GitHub, Jira, Linear, Sentry, usage meters).
- Open question: can a script send text to a session and read its screen? Without that our waiters
  cannot run on it.

### Paperclip (github.com/paperclipai/paperclip)

Agents run as a company: org chart, goals, a monthly budget per agent, approvals, audit log, heartbeats that
start agents on a schedule. Self-hosted Node server with a React UI, MIT.

**Code read 2026-10-22** (clone of main 043f2514e; read only, nothing installed or run). Repo: 106K stars,
about 5,500 commits since 2026-02, 244 authors (top three: about 3,150, 791, 323 commits), 6,378 open issues,
about 3,000 test files, a promptfoo eval suite, about 29 release notes 2026-03 to 2026-09.

What it is: a control plane: an API server, a React board, a CLI, a Postgres database (Drizzle schema; an
embedded Postgres for local use) and one adapter package per agent tool (Claude Code, Codex, ...). About 170
schema files: companies, agents, goals, issues, budgets, cost events, approvals, routines, activity log,
completion contracts.

How it runs an agent: **not** long-lived interactive sessions. Each heartbeat starts the real `claude` CLI
headless (`--print --output-format stream-json`, `packages/adapters/claude-local/src/server/execute.ts:864`),
with `--resume` only when the saved session id, folder and prompt bundle still match (`execute.ts:799-839`);
the instruction file is sent only on a fresh session, to save 5-10K tokens per wake (`execute.ts:881-884`).
Work moves as issues in the database; agents wake on a wake-request queue and on cron routines.

- **Budgets with a hard stop** (strongest part): cost per run read from the CLI's own JSON (`total_cost_usd`,
  per-model usage, `parse.ts:57-64,144-152`); at the limit it returns `hard_stop`, pauses the scope and
  cancels queued runs (`server/src/services/budgets.ts:89,276-303`, `heartbeat/budget-cancellation.ts`).
  Works with a subscription login (`execute.ts:174`); whether subscription runs count against a budget is
  not confirmed.
- **Completion contracts:** each issue can carry a risk level, a "completion authority" and a rule for unmet
  criteria; a review step asks a bound reviewer to "review the persisted run evidence and confirm whether this
  issue may be completed" (`server/src/services/native-runtime/automatic-completion-reviews.ts:23,37,79`).
  That is a review of stored evidence, not a re-run on a clean checkout.
- **Leases** on live runs, renewed on a timer, so a dead run is found (`heartbeat/recovery.ts:1692`).
- **Defaults to watch:** the Claude adapter skips all permission prompts unless turned off
  (`dangerouslySkipPermissions` default true, `execute.ts:445`). Telemetry is on unless
  `PAPERCLIP_TELEMETRY_DISABLED=1`, `DO_NOT_TRACK=1`, a CI variable, or `enabled: false` in its config
  (`packages/shared/src/telemetry/config.ts:67-80`). Neither is acceptable for Appxite code as shipped.
- Not confirmed in code: wake intervals, hand-off between agents, the audit log, multi-user sign-in, own model
  calls outside the agent CLI.

Verdict: a different shape from Crius. It runs agents as short headless jobs inside a company model; Crius
runs long interactive sessions and checks their claims. Running it would mean giving up the visible sessions
and moving work into its database. Take ideas, do not run it (same as the trial plan said).

Best parts to take:
- **Cost per run from the CLI's JSON output**, a budget per lead with a hard stop that pauses and cancels.
- **Resume only when session id, folder and prompt still match;** send the brief only to a fresh session.
- **A done rule as data per item** (risk, who may close it, what happens to unmet criteria), next to our
  VERIFIED rule.
- **A lease per running lead**, so a dead lead is noticed without reading its screen.
- **Budget per agent** with a hard stop. We watch quota by hand today.
- **Approvals and audit log** as data, not only bus lines.
- **Goals tied to each agent**, close to our GOAL blocks.

### LiteLLM (github.com/BerriAI/litellm)

Open-source gateway (proxy) in front of 100+ model APIs (Anthropic, OpenAI, Gemini, local models via Ollama or
vLLM), all behind one OpenAI-style API. Python. Added by the owner 2026-10-21.

Best parts to take:
- **Virtual keys with budgets and spend tracking** per key, team or agent: a hard money stop per builder.
- **Fallbacks and load balancing:** when one model or account hits its limit, calls move to the next one.
- **One endpoint for local models**, for the local LLM trial: agents point at the gateway, not at each model.
- Can sit in front of Claude Code (`ANTHROPIC_BASE_URL` to the gateway). Does not apply to the Max plan
  login, only to API-key use.

Caution: on 2026-03-24 the PyPI package was hijacked (versions 1.82.7 and 1.82.8 stole credentials; last clean
1.82.6), through a compromised scanner in LiteLLM's CI. If we try it: pin an exact version checked against
GitHub tags, prefer the official container image, run it in a container with no access to `~/.secrets`, and
never give it production keys.

### Google Harness² (Harness2, Google Research)

Open-sourced with a paper on 2026-10-09, Apache 2.0, research code (not a supported Google product). An agent
improves its own harness, not its model: it edits the system prompt, guardrails, memory, tool guidance,
subagents, skills, scripts and middleware between runs, and keeps an edit only when it scores better.
Runs on OpenCode by default, can switch to Codex. Reported gain about 11.5 points on benchmarks (Claude Opus
as the editing model). Linux, Python 3.12/3.13 with uv. Added by the owner 2026-10-21.

Best parts to take:
- **Edits to rules and skills are tested, not trusted.** Today our AGENTS.md, skills and memory change by hand
  after a correction (the ladder in `~/.agents/AGENTS.md`). Harness² measures each edit against a test set and
  drops the ones that do not help. That is the missing check behind our memory cleanup.
- **The harness as eight named parts** gives us a checklist for where a fix lives.
- Risk to keep in mind: a harness that rewrites its own guardrails needs the verification to be strong;
  safety rules (secrets, production, outward-facing) stay outside what it may edit.

### LangGraph and LangSmith (LangChain)

Added by the owner 2026-10-21. Written from general knowledge, not checked against their latest releases.

- **LangGraph:** open-source library (Python and TypeScript) to build an agent flow as a graph of steps with
  saved state. Best parts: **checkpoints** after every step, so a run resumes where it stopped after a crash
  or a context reset (our resume files do this by hand); **interrupts** that pause for a human answer and
  continue (our NEED/ASK lines); a clear graph of who hands work to whom.
- **LangSmith:** tracing and evals for agent runs: every step, tool call, cost and time in one view, plus test
  sets that score runs. Best parts: **one trace per run** instead of reading bus lines and screens; **eval sets
  from past failures**, the same idea as Harness².
- Fit: both are built for agents we write in code, not for CLI sessions like Claude Code, so we would take
  ideas, not run them. LangSmith's hosted version sends traces to LangChain: never with Appxite code or data;
  self-hosting is an enterprise option.

### Smaller ones found on the way

- **Agent Herder** (MCP server): watches and inspects agent sessions across Claude Code, Codex, OpenCode.
- **Herd** (herd-os/herd, Go): splits a feature into tasks, runs them in parallel, opens one reviewed PR.

### Claude Dashboards and Motion (Anthropic, 2026-10)

- **Dashboards** (beta, paid plans): a dashboard over a connected data source that stays current, with the
  query behind each number. Idea: goal reports as a live page instead of text twice a day.
- **Motion** (beta, Team/Enterprise, `/motion`): prompt to a short animation built as code, MP4 export.

### Claude Duet (Anthropic, research preview 2026-10-21)

- A separate app (iOS, Android, claude.ai) where Claude acts first: a morning brief across connected apps, then
  quiet until something needs you; runs tasks in its own cloud VM and browser while your devices are off; shows
  the exact action and waits for OK before it sends, pays, signs or deletes. Uses your Claude memory, skills and
  connectors. Opus 5.5, Pro and Max in the US only, waitlist; Team and Enterprise not included; checks every few
  hours and counts against the plan's limits.
- Fit: it does the personal side of Falcon (morning brief, inbox, "what needs me"), not Crius's job of running
  coding sessions. Not for Appxite: a personal Max account must not read work mail or Teams.
- Ideas to take: **one brief plus messages only when needed** instead of Falcon's card queue (the cards pile-up
  item); **check every few hours** rather than nonstop, to save quota; **show the exact action before approval**.

## Trial plan (after 2026-11-01)

1. Herdr first: closest to cmux. Test: can our waiters and prompt watching move onto its state + socket API?
2. OpenRig: code read 2026-10-22; trial steps and the 13 tests are under "OpenRig trial" above. The owner asked
   about starting it now, before 11-01.
3. Ghostex: script control (send, read) and handoff Claude to Codex in the middle of a task.
4. Paperclip: only the budget and approval ideas; we probably do not run it.
5. LiteLLM: with the local LLM trial. One gateway for local and API models, a budget per key, fallback
   when a model is out. Pinned version, in a container, test keys only.
6. Harness²: read the paper and code first, then try the idea on one skill or AGENTS.md with a small test
   set of past mistakes: does a tested edit beat our hand-written fix? Linux only, so in a container.
7. LangGraph / LangSmith: ideas only. Checkpoint + interrupt shape for the bus; one trace per lead run for
   the goal reports. Revisit if we build our own agent service in TypeScript.

For each trial write down: what it replaces in our setup, what it cannot do (clean-checkout re-runs,
the bus rules), and the cost to move. Keep Crius's own checks either way.

## Sources

- OpenRig: https://github.com/mvschwarz/openrig/wiki, https://www.everydev.ai/tools/openrig,
  https://pyshine.com/OpenRig-Multi-Agent-Control-Plane-Deep-Dive/
- Herdr / Herder: https://www.mindstudio.ai/blog/herder-terminal-agent-multiplexer/,
  https://aiunderstanding.org/news/herdr-launches-open-source-terminal-for-managing-multiple-ai-coding-agents
- Ghostex: https://ghostex.dev/, https://github.com/maddada/Ghostex/releases
- Paperclip: https://abduzeedo.com/paperclip-open-source-ai-agent-orchestration-builders,
  https://betterstack.com/community/guides/ai/paperclip-multi-agent/
- LiteLLM hijack: https://simonwillison.net/2026/Mar/24/malicious-litellm/,
  https://www.comet.com/site/blog/litellm-supply-chain-attack/
- Harness²: https://www.marktechpost.com/2026/10/18/google-harness2-recursive-agent-harnessing-framework/,
  https://www.remio.ai/post/google-harness2-open-sources-agent-self-improvement-but-verification-remains-the
- Agent Herder: https://glama.ai/mcp/servers/megamen32/agent-herder
- Herd: https://pkg.go.dev/github.com/herd-os/herd
- Claude Dashboards and Motion: https://claude.com/resources/articles/dashboards-and-motion
