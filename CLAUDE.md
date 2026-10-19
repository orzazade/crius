# crius — repo notes

- Read the live store with `sqlite3 "file:$HOME/.crius/state/record.db?immutable=1" …`: record.db is in WAL mode
  with no -shm file when idle, so `-readonly`, `?mode=ro` and `.backup` fail with CANTOPEN (14).
