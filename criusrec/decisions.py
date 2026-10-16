"""DECISIONS.md: append-only, `<project>-D<n>` entries, supersession read from the newer entry (design §6, §7b)."""
import fcntl
import re
from datetime import date
from pathlib import Path

from . import bus

DECIDERS = ("owner", "crius", "lead")
HEAD = re.compile(r"^## ([a-z0-9][a-z0-9-]*)-D(\d+) · (\d{4}-\d\d-\d\d)\s*$")
FIELDS = {"Decision": "title", "Why": "why", "Decided by": "decided_by", "Supersedes": "supersedes",
          "Reason": "reason", "Overrides": "overrides", "Refs": "refs"}


def _one_line(name, value):
    if not value or not value.strip() or "\n" in value:
        raise ValueError(f"{name} must be one non-empty line")
    return value.strip()


def _decider(by):
    return by.split()[0] if by else ""


def parse_decisions(text):
    """Return (entries, problems). Bad refs are kept and flagged, never dropped."""
    entries, problems, seen = [], [], set()
    cur = None
    for no, line in enumerate(text.splitlines(), 1):
        m = HEAD.match(line)
        if m:
            cur = {"id": f"{m.group(1)}-D{m.group(2)}", "project": m.group(1), "n": int(m.group(2)),
                   "date": m.group(3), "line": no, "taint": False}
            if cur["id"] in seen:
                problems.append(f"line {no}: duplicate id {cur['id']}")
            seen.add(cur["id"])
            entries.append(cur)
            continue
        if line.startswith("## "):
            problems.append(f"line {no}: malformed decision header")
            cur = None
            continue
        if cur is None or ": " not in line:
            continue
        key, _, val = line.partition(": ")
        if key in FIELDS:
            if key == "Refs":
                val, _, taint = val.rpartition("· taint: ") if "· taint: " in val else (val, "", "")
                cur["taint"] = taint.strip() == "yes"
                val = val.strip()
            cur[FIELDS[key]] = val.strip()
    earlier = set()
    for e in entries:
        sup = e.get("supersedes")
        if sup and sup not in earlier:
            problems.append(f"line {e['line']}: {e['id']} supersedes {sup}, which is not an earlier entry")
        earlier.add(e["id"])
    return entries, problems


def active_ids(entries):
    """Ids not superseded by any later entry. A supersede of an unknown or later id is ignored (it is flagged)."""
    ids, earlier, dead = [e["id"] for e in entries], set(), set()
    for e in entries:
        sup = e.get("supersedes")
        if sup in earlier:
            dead.add(sup)
        earlier.add(e["id"])
    return set(ids) - dead


def record_decision(home, project, title, why, decided_by, supersedes=None, reason=None, refs="", taint=False,
                    today=None):
    if not bus.SLUG.match(project):
        raise ValueError("bad project slug")
    title, why, decided_by = _one_line("title", title), _one_line("why", why), _one_line("decided by", decided_by)
    if _decider(decided_by) not in DECIDERS:
        raise ValueError(f"decided by must start with one of {DECIDERS}")
    if supersedes and not reason:
        raise ValueError("a supersede needs --reason")
    if "\n" in refs or "\r" in refs:
        raise ValueError("refs must be one line")
    d = Path(home) / "state" / project
    d.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = d / "DECISIONS.md"
    with open(path, "a+", encoding="utf-8") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        f.seek(0)
        old = f.read()
        entries, _ = parse_decisions(old)
        by_id = {e["id"]: e for e in entries}
        overrides = None
        if supersedes:
            target = by_id.get(supersedes)
            if target is None:
                raise ValueError(f"{supersedes} is not a decision in {path}")
            if _decider(target.get("decided_by", "")) == "owner" and _decider(decided_by) != "owner":
                raise ValueError(f"{supersedes} is an owner decision; only the owner supersedes it")
            if _decider(decided_by) == "owner" and _decider(target.get("decided_by", "")) == "crius":
                overrides = "crius"
        n = max([e["n"] for e in entries if e["project"] == project], default=0) + 1
        did = f"{project}-D{n}"
        lines = [f"## {did} · {today or date.today().isoformat()}", f"Decision: {title}", f"Why: {why}",
                 f"Decided by: {decided_by}"]
        if supersedes:
            lines += [f"Supersedes: {supersedes}", f"Reason: {_one_line('reason', reason)}"]
        if overrides:
            lines.append(f"Overrides: {overrides}")
        lines.append(f"Refs: {refs.strip() or '-'} · taint: {'yes' if taint else 'no'}")
        text = f"{did} {title}" + (f" (supersedes {supersedes})" if supersedes else "")
        line = bus.format_line("DECISION", project, _decider(decided_by), text[:bus.MAX_LEN])
        f.write(("\n" if old and not old.endswith("\n\n") else "") + "\n".join(lines) + "\n")
        f.flush()
    try:
        bus.append_line(home, project, line)
    except OSError as e:
        raise RuntimeError(f"{did} was saved in {path}, but its DECISION bus line failed ({e.strerror}); "
                           f"do not record it again, post the bus line by hand") from e
    return did
