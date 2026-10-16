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
