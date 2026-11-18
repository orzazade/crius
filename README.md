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
- Install: on the Mac, `bin/crius-install-launchd` from the clean checkout `~/Projects/scifi/crius-live` at origin/main
  builds every command under `cmd/` (crius-hook and crius-wakewatch too) into `~/.crius/bin`. The Linux team boxes
  (the playbook's `hosts.json`) run crius-hook, bus-post, crius-statusline and playbook from their own `~/.crius/bin`:
  build each with `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 GOTOOLCHAIN=local go build -o <file> ./cmd/<tool>`
  (`TestLinuxToolsBuildFromTheRepo` keeps that build working) and copy it there over the box's ssh; no script does
  that copy yet. A record one tool writes and another reads ships to the Mac and every box together: crius-hook's
  role record `started` is read by `crius-wakewatch restart --from-ready`, which until then refuses a READY-RESTART
  line older than the role's last resume or compact ("cannot tell when the session … took it").
- Tests: `GOTOOLCHAIN=local go test ./...` (Go 1.25.6; one dependency, `modernc.org/sqlite`, pure Go; fixtures are
  synthetic). The Python version of step 1 is on branch `archive/step1-python`
