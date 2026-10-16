"""record.db: one SQLite file in ~/.crius/state, WAL, one writer at a time (flock)."""
import fcntl
import sqlite3
from contextlib import contextmanager
from pathlib import Path

SCHEMA = """
create table if not exists item (
  id text primary key, project text not null, n integer not null, parent text, kind text not null,
  title text not null, criteria text not null, opened text not null, state text not null default 'open',
  taint integer not null default 0, unique (project, n));
"""


def connect(home):
    path = Path(home) / "state" / "record.db"
    conn = sqlite3.connect(path, isolation_level=None, timeout=30)
    conn.execute("pragma journal_mode=wal")
    conn.executescript(SCHEMA)
    return conn


@contextmanager
def writer_lock(home, blocking=True):
    """Held by the ingest and by `record` while they write. Non-blocking raises BlockingIOError if busy."""
    with open(Path(home) / "state" / "record.lock", "a") as f:
        fcntl.flock(f, fcntl.LOCK_EX | (0 if blocking else fcntl.LOCK_NB))
        yield
