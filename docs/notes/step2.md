# Step 2 in Go — RESUME HERE

Branch `wip/step2-go` (local, not pushed). Work on this branch; merge to main only when step 2 is DONE.
Main is at 060610f (step 1 in Go, DONE and proven on a clean worktree). Brief: `docs/BRIEF-build-1-2.md`.
Design: `docs/MEMORY-AND-MEASUREMENT.md` §5 and §9 step 2. Python reference: branch `wip/step2-python` (f59c94a).
Always use `GOTOOLCHAIN=local` (Go 1.25.6). Deps pinned: modernc.org/sqlite v1.59.0, BurntSushi/toml v1.5.0.

## Done
- Tests written first, red recorded: 43/45 failed on stubs (2 "must error" tests passed on stubs; after the
  build, break the backup temp cleanup once to show `TestAFailedBackupLeavesNoTempFiles` fails).
  Files: `internal/ingest/{fixtures,transcripts,sources,ops}_test.go`, `internal/watchdog/watchdog_test.go`.
- Schema for step 2 in `internal/store/db.go` (request, session, assignment, event, decision, git_commit,
  evidence, artifact, coverage, source_run, alert). `store.URI` (escaped SQLite path) is now exported.
- Written: `internal/ingest/{ingest,transcripts,config,redact,prices,backup}.go`.
  Fixes for the codex review of the Python WIP are already in this code:
  - P1 JSON-quoted secret keys (`"api_key": "..."`) and whole PEM key bodies: `redact.go` (both patterns).
  - P1 heartbeat stamped on partial runs: `Main` stamps only when status is complete.
  - P1 refs stored unredacted: `runner.event` redacts every ref value.
  - P2 cursor before roll-up/alerts: roll-up and alerts (durable `alert` queue, `flushAlerts`) are in the
    file's transaction.
  - P2 events took the latest project: `rollUp` uses the assignment active at the event's time.
  - P2 os.walk hid unreadable folders: WalkDir errors warn and make the run partial.
  - P3 title-only append: the session upsert runs when only a title arrived.
  - P2 backup deletes future-dated copies / leaves temp files: `keepBackups`, `BackupIfDue` defer cleanup.

## Next
1. Write `internal/ingest/git.go` (`ingestGit`): resolve the branch to a sha FIRST, call `afterHeadHook()` if set
   (test seam), then `git log <last>..<sha>`; cursor = that sha (codex P1). Port from python `ingest_git`
   (default branch: origin HEAD, then main, master; commits + evidence rows; reverts; session link SQL).
2. Write `internal/ingest/logs.go` (`ingestLogs`): bus `~/.crius/bus/*.log` strict, team logs lenient; key
   `source:sha1(full path)[:12]:pos:sha1(line)[:12]` (codex P2: not the base name); undated lines start from
   the file's birth date on a rescan (use the `rescanned` flag from `scanLines`), else the saved state.
3. Write `internal/ingest/files.go` (`ingestFiles`): DECISIONS.md via ReadDir (loud errors), artifacts via WalkDir
   (skip dot paths, stamp mtime_ns:size).
4. Write `internal/watchdog/watchdog.go` (`Check(home, now, wake, maxAge)`; roots from projects.toml
   `[transcripts]`, codex P2) and `cmd/crius-watchdog` (wake from `unix.SysctlTimeval("kern.waketime")`, warn on
   error, never exit silently; post an ALERT once per distinct error). No import of internal/store.
5. `cmd/crius-ingest/main.go`; `launchd/*.plist.in` pointing at `~/.crius/bin/crius-{ingest,watchdog}`
   (ingest env: HOME, PATH=/opt/homebrew/bin:/usr/bin:/bin, CRIUS_REPO); `bin/crius-install-launchd` builds both
   into ~/.crius/bin, `mkdir -p ~/Library/LaunchAgents` (codex P2), plutil -lint, bootout/bootstrap, prints plists.
6. Add the `[transcripts]` section to `projects.example.toml` (see python branch).
7. `go mod tidy`, `go vet`, `go test ./...` until green. Then a real first run: move the Python-era
   `~/.crius/state/record.db` (+ its backup gz) aside first (schema differs; it is rebuildable). Compare with
   tools/agent-cost review-1016 calls.jsonl (python got 159,169/159,174, +0.22% cost).
8. Codex deep review in small parts (whole-diff reviews time out). Fix, red first.
9. Commit, prove on a clean worktree, install launchd (approved, Q3), live drill (stop ingest, backdate heartbeat
   > 21 min, kickstart watchdog, see ALERT in ~/.crius/bus/crius.log, bootstrap again). DONE line, then STOP.
