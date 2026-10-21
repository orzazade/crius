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
Apache 2.0, npm `@openrig/cli`, Node 22/24 plus tmux. Trended late September 2026; very new.

Best parts to take:
- **Seats:** fixed role addresses (`dev-owner@project`) that stay the same across sessions. Ours are names
  like `quotes:lead`, but the session behind a name is found by hand.
- **Queues per seat:** work items wait in a queue owned by the assignee, not only as bus lines.
- **Team in one file + one start command**, and the team comes back after a reboot. Our relaunch is manual.

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
start agents on a schedule. Self-hosted Node server with a React UI, MIT, about 70k stars. Early: single
user, no cloud setup yet.

Best parts to take:
- **Budget per agent** with a hard stop. We watch quota by hand today.
- **Approvals and audit log** as data, not only bus lines.
- **Goals tied to each agent**, close to our GOAL blocks.

### Smaller ones found on the way

- **Agent Herder** (MCP server): watches and inspects agent sessions across Claude Code, Codex, OpenCode.
- **Herd** (herd-os/herd, Go): splits a feature into tasks, runs them in parallel, opens one reviewed PR.

### Claude Dashboards and Motion (Anthropic, 2026-10)

- **Dashboards** (beta, paid plans): a dashboard over a connected data source that stays current, with the
  query behind each number. Idea: goal reports as a live page instead of text twice a day.
- **Motion** (beta, Team/Enterprise, `/motion`): prompt to a short animation built as code, MP4 export.

## Trial plan (after 2026-11-01)

1. Herdr first: closest to cmux. Test: can our waiters and prompt watching move onto its state + socket API?
2. OpenRig: start a 2-seat team from YAML; reboot the machine; does it come back? Do seats + queues beat bus
   lines for us?
3. Ghostex: script control (send, read) and handoff Claude to Codex in the middle of a task.
4. Paperclip: only the budget and approval ideas; we probably do not run it.

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
- Agent Herder: https://glama.ai/mcp/servers/megamen32/agent-herder
- Herd: https://pkg.go.dev/github.com/herd-os/herd
- Claude Dashboards and Motion: https://claude.com/resources/articles/dashboards-and-motion
