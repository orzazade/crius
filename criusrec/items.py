"""Items `<project>#<n>`: one owner outcome or a child task, with acceptance criteria fixed at open (design §7)."""
import json
from datetime import datetime, timezone

from . import bus

KINDS = ("code", "research", "falcon", "other")


def record_item(conn, project, title, kind, criteria, parent=None, taint=False):
    if not bus.SLUG.match(project):
        raise ValueError("bad project slug")
    if kind not in KINDS:
        raise ValueError(f"kind must be one of {KINDS}")
    title = (title or "").strip()
    if not title or "\n" in title or len(title) > bus.MAX_LEN:
        raise ValueError("title must be one line of 1-300 chars")
    criteria = [c.strip() for c in criteria or [] if c and c.strip()]
    if not criteria:
        raise ValueError("an item needs its acceptance criteria when it opens")
    conn.execute("begin immediate")
    try:
        if parent and not conn.execute("select 1 from item where id=?", (parent,)).fetchone():
            raise ValueError(f"parent {parent} is not a known item")
        n = conn.execute("select coalesce(max(n), 0) + 1 from item where project=?", (project,)).fetchone()[0]
        iid = f"{project}#{n}"
        conn.execute("insert into item (id, project, n, parent, kind, title, criteria, opened, taint) "
                     "values (?,?,?,?,?,?,?,?,?)",
                     (iid, project, n, parent, kind, title, json.dumps(criteria),
                      datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"), int(taint)))
        conn.execute("commit")
    except BaseException:
        conn.execute("rollback")
        raise
    return iid
