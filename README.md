# Crius

The owner's lead agent ("Jarvis"): one main Claude session that the owner talks to, which supervises the project
lead sessions (quotes, AXI, trading, falcon; staffy paused), remembers what happened, measures output and cost, and
only asks the owner what only the owner can decide. Falcon is the data collector; Crius decides.

Restarted from scratch on 2026-10-16. The March 2026 harness prototype is on branch `archive/harness-v0`.

- `docs/DESIGN.md` — the 2026-10-14 design (to be revised by the memory and measurement decision)
- `STATUS.md` — the running checklist; read this first after any break
- `PARKED-SESSIONS.md` — closed sessions and how to resume them
- `research/` — research reports behind decisions
- `bus/` — old bus folder (not in git); leads now write to `~/.crius/bus/<project>.log`
- `cmd/`, `internal/`, `bin/crius-rec` — the record store, in Go (design: `docs/MEMORY-AND-MEASUREMENT.md`). Private
  state lives in `~/.crius/` (mode 700, outside every git tree); `bin/crius-rec init` creates it,
  `bin/crius-rec leak-test` checks it. `bin/crius-rec` builds the binary on demand (needs `go` on PATH)
- Tests: `GOTOOLCHAIN=local go test ./...` (Go 1.25.6; one dependency, `modernc.org/sqlite`, pure Go; fixtures are
  synthetic). The Python version of step 1 is on branch `archive/step1-python`

## T3 thread move check

Moving a T3 Code thread to another account (`mcp__t3-code__t3_thread_configure` with a `modelSelection`) restarts the
thread's provider session and kills its running background shells. `crius-spawn -move-check` is a PreToolUse hook that
denies such a move, with the reason, while the thread has an open run, an open request, a message after its last reply
waiting for a turn, or a last reply whose newest status emoji is ⏳; for a call with no `threadId` (the caller's own
thread, mid-turn by definition); and when the thread's state cannot be read (t3 missing, no answer in 30 s, bad JSON, a
thread the local t3 does not know, such as one on another machine). A call with no `modelSelection` only reads and is
let through. To move a thread anyway, on purpose: `mkdir -p ~/.crius/state/move-ok && touch
~/.crius/state/move-ok/<threadId>` (`self` for the caller's own thread); the check deletes the file, so it is good for
one move.

Install (not done by the build): put the binary at `~/.crius/bin/crius-spawn` and add to the `hooks` of the settings.json
of each config folder whose sessions call the tool. The hook timeout (45 s) is larger than the check's 30 s t3 timeout:

```json
"PreToolUse": [
  {
    "matcher": "mcp__t3-code__t3_thread_configure",
    "hooks": [{ "type": "command", "command": "~/.crius/bin/crius-spawn -move-check", "timeout": 45 }]
  }
]
```

Demo, with a fake t3 (one deny, one allow): `sh cmd/crius-spawn/testdata/demo-move-check.sh`.
