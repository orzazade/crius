# crius — repo notes

- Read the live store with `crius-rec` or `sqlite3 "file:$HOME/.crius/state/record.db?immutable=1" …`, never
  `sqlite3 -readonly`: record.db is in WAL mode with no -shm file when idle, so `-readonly`, `?mode=ro` and `.backup`
  fail with CANTOPEN (14).
- Max accounts: spend the one whose week resets soonest until 95%, then the next (owner 10-28). `quota.Pick`
  (crius-spawn) and the ingest 95% NEED enforce it; tests `TestPickTakesTheSoonestResetUnder95`,
  `TestQuotaAccountAtTheLimitTellsCriusOnce`.
