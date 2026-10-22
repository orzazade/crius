# What Crius takes from OpenRig (2026-10-22)

The owner asked to "document the best parts of openrig and implement here in crius". We do not run OpenRig. We
copy the ideas that fix this week's misses and leave the rest. Sources: OpenRig v0.6.14 (commit df1c151; paths
below are from its repo root), the trial in `~/Projects/yolo/openrig-trial/RESULTS.md` ("test N" below), and
[RESEARCH-AGENT-TOOLS.md](RESEARCH-AGENT-TOOLS.md) (OpenRig section).

## The misses this fixes (all 2026-10-22)

| Miss | What happened | Why |
|------|---------------|-----|
| 8 lost wakes, 11:37-12:42 | wakewatch typed nothing: "Crius's prompt holds a draft that is not wakewatch's" | Grey suggestion text in Crius's box looked like a typed draft. `cmux read-screen` has no colours. |
| 2 false PROMPT alarms | promptwatch asked the owner about a dialog that was not open | Dialog words inside a code diff on screen |
| Fresh-start alerts for a busy lead | wakewatch told a working lead to restart | Decided from screen text and context %, not from what the session was doing |
| A missed READY line | Crius did not see a READY line | Word order on the line differed from the pattern |

The first three come from one cause: **we decide a session's state by reading its screen**. OpenRig's main lesson
is to let the session report its own state and keep the screen as a cross-check. The fourth is not a screen read:
team-wait.sh matched CLEAR-READY by word order on bus lines (bus 2026-10-22T00:18). State files do not fix it.

## What we take

| # | Idea | OpenRig source | Trial | How Crius does it |
|---|------|----------------|-------|-------------------|
| A | State from Claude Code hooks: prompt submitted or tool started = running; permission or question notification = needs input; idle notification, Stop, SessionEnd = idle | `packages/daemon/src/domain/agent-activity-store.ts:328-338` (hookEventState), `:346-357` | 3, 4, 5 | `crius-hook` (Go, item 2) writes one small file per session under `~/.crius/sessions/`. OpenRig maps PreToolUse and PermissionRequest but does not wire them (see N); crius-hook wires both, and maps SessionStart to idle where OpenRig says "unknown". |
| B | A hook script that never blocks the session | `packages/daemon/assets/plugins/openrig-core/hooks/scripts/activity-relay.cjs:9-31,126-147` (stdin JSON, 1500 ms timeout) | 5, 12 | Same, but errors are logged to a file wakewatch reads; OpenRig swallows them (`main().catch(() => {})`, `:295-313`). |
| C | Grey suggestion text is not a draft | `packages/daemon/src/adapters/tmux.ts:1332-1345` (`capture-pane -e`), `packages/daemon/src/domain/session-transport.ts:447-456` (promptTextIsFaint) | 1 | cmux cannot do this: `read-screen` and its `capture-pane` alias have no colour flag. See "Grey text" below for how Crius decides instead. |
| D | Hold a message while the box has typed text | `packages/daemon/src/domain/seat-delivery-guard.ts:361-378` (`draft_input_busy`) | 2 | Item 4: hold, then deliver once the box is empty. In the trial draft-aware also held twice with an empty box (RESULTS.md:32): a hold that can be wrong must not be silent, so a message held over 10 minutes is an alert that names it. |
| E | Store a message before typing it, and retry | `packages/daemon/src/db/migrations/024_queue_items.ts:22-47`, `packages/daemon/src/domain/queue-wake-ladder.ts:11,25,69,71` (retry a failed wake of a handed-off row every 300 s, at most 3), `packages/daemon/src/domain/queue-stuck-sweep.ts:5-24` | 6 (the first nudge itself is not retried; the watchdog does that) | Item 4: one file per message, retried until the session shows it arrived. OpenRig retries only a pointer ("you hold qitem-…"); we retry the message itself. |
| F | Restore by session id | `packages/daemon/src/adapters/claude-resume.ts:50-92,136,172` (`claude --resume <id>`, "no conversation found" = start fresh), `packages/daemon/src/domain/session-registry.ts:378-404` | 7, 8 | Item 5: keep `project:role -> session id` from SessionStart; `crius-spawn --resume <project> <role>`. |
| G | One state with a clear order of trust; screen as fallback | `packages/daemon/src/domain/seat-activity-service.ts:553-620,776` (self report > hooks > screen; screen wins when the hook row is over 15 s old), `packages/daemon/src/domain/activity-taxonomy.ts:83-87` | not tested | Item 3: state file first, screen as fallback and cross-check; when they disagree, log both. |
| H | A stable name per seat | `packages/daemon/src/domain/session-name.ts:12-18` (`{pod}-{member}@{rig}`) | not tested | We already have it: `project: role` tab names from crius-spawn (d4ee8ad). |

### Grey text (row C): how Crius decides

The status file and the hooks cannot see the input box, and cmux has no colours. Holding while "the box shows text"
only turns a lost wake into a wake held for as long as the grey text stays. Typing anyway can merge the message
with the owner's draft, which is the trial's FAIL (RESULTS.md:32). So item 4 does this, in order:

1. **Remove the grey text (design).** Claude Code 2.1.308 has a switch for prompt suggestions
   (`CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION`, setting `promptSuggestionEnabled`). Set to false in the launch env of
   Crius's tab only, its box never shows grey text, so any text there is typed and a hold is right. This is a launch
   change, not a settings file change, but it changes how Crius is started: it goes to the owner with the O2 diff.
   Item 4 proves it on a scratch session first (box empty after a turn with the switch off, grey text with it on).
2. **Prove what was sent.** crius-hook stores the hash of each submitted prompt. After typing, the sender checks that
   the session's last prompt hash equals the message's hash. A message merged with a draft does not match: that is a
   loud alert, not a quiet success.
3. **A hold is never silent.** A message held over 10 minutes is an alert that names it and what the box shows.

Without step 1 the rule would rest on timing (text first seen after Stop and unchanged = a suggestion; text seen
while the turn ran, or changed while idle = typed). We do not plan to rely on it.

## What we do not take

| # | What | OpenRig source | Trial | Why not |
|---|------|----------------|-------|---------|
| I | tmux as the session host | `packages/daemon/src/adapters/tmux-server.ts:3-6,26-41`; cmux only attaches: `packages/daemon/src/domain/cmux-layout-service.ts:1-12,35` | 8, 10 | Our sessions live in cmux tabs. A second terminal host doubles what can break. |
| J | A managed block in the repo's CLAUDE.md | `packages/daemon/src/domain/managed-blocks.ts:8-17`, `packages/daemon/src/adapters/claude-code-adapter.ts:683-692,832-850` | 11, 12 | 16 KB (about 4K tokens) in every seat's context, and it writes into repo files that other sessions share. |
| K | Writes to the home folder at start | `packages/daemon/src/adapters/claude-code-adapter.ts:896-917` (trust and onboarding flags in `~/.claude.json` when one is missing, `:900-901`), `packages/daemon/src/domain/plugin-vendor-service.ts:232-276` (copies skills to `~/.claude/skills`, `~/.agents/skills` when its bundled copy is newer, `:246-255`) | 12 | Changes the owner's Claude config without asking. Our rule: no `~/.claude` change without the owner's OK. |
| L | A GitHub call at every start | `packages/daemon/src/startup.ts:870-875,916`, `packages/daemon/src/domain/plugin-vendor-service.ts:284-316` | 11 | Network call with no off switch, and the result is not used. |
| M | Holding messages with no redelivery | `packages/daemon/src/domain/draft-aware-delivery.ts:113-129` ("No automatic retry is scheduled"), default guard off: `packages/daemon/src/domain/seat-delivery-guard.ts:49` | 2 | With the guard off (the default) a draft and a message are sent as one prompt; with it on, a held message never arrives. We need both hold and redeliver. |
| N | Its hook wiring as is | `packages/daemon/assets/plugins/openrig-core/hooks/claude.json:4-33,84-114` wires only SessionStart, UserPromptSubmit, Stop, Notification | 3, 4 | PermissionRequest is mapped but not wired, so needs-input came only from the later `permission_prompt` notification, 5.9 s late. PreToolUse is mapped (running) but not wired. After Esc on a dialog the state stayed "needs input" until the next prompt, because no hook fires on Esc. SessionStart maps to "unknown", so startup dialogs showed as idle. crius-hook wires PermissionRequest, PreToolUse and SessionEnd too; Notification has no matcher in either, so both get every type. For Esc, the readers (item 3) also read Claude Code's own status file (below): when it changed after the last hook event, it wins. |

## What we found in our own tools (not in OpenRig)

1. **Claude Code already writes a status file per session**, with no hook and no settings change:
   `~/.claude*/sessions/<pid>.json` (seen in versions 2.1.306 and 2.1.308). Fields include `sessionId`, `cwd`,
   `name`, `status`, `statusUpdatedAt` (ms), `waitingFor`. Values seen on 10-22 against each tab's screen:
   `busy` (mid-turn), `idle` (nothing running), `shell` (idle with background shells; the tab footer showed
   "3 shells"), `waiting` with `waitingFor` such as "permission prompt" (other strings in the binary: "dialog
   open", "input needed", "sandbox request"). It is not a documented interface: a reader must fail loud when the
   field is missing, not fall back quietly. wakewatch already reads this folder for session ids and names.
   It cannot see what is in the input box.
2. **cmux's own Claude hook store is not a state source.** cmux starts Claude with its own hooks
   (`~/.cmuxterm/claude-settings/*.json`) and keeps `~/.cmuxterm/claude-hook-sessions.json`, but at 14:30 every one
   of 50 records said `runtimeStatus` idle, including this session while it was running a command (at the review,
   all 109 said idle).
3. **cmux has no styled screen read**, so grey text cannot be told from typed text on screen (see C).
4. Claude Code sessions on the same config folder can message each other (`messagingSocketPath`, `peerFeatures`
   in the status file). Not documented; a follow-up to watch, not to build on now.

## Plan (items 2-5 of the brief)

The readers (item 3) can use the status file in point 1 now. `crius-hook` (item 2) adds what that file does not
have: proof that a prompt was submitted (for delivery, item 4), the Notification type, and the SessionStart
session id per `project: role` (for restore, item 5). It is built and tested with a scratch `CLAUDE_CONFIG_DIR`;
the live settings diff goes to the owner through Crius. After the owner's OK: one day observe only (state files
written, readers still decide from the screen, both logged side by side), then switch. Rollback: remove the hook
entries (backup kept).

Later, as follow-up rows, not built now: a work queue per role, and starting a whole team from one file.
