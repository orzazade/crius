"""Bus line grammar (design §6): `<YYYY-MM-DDTHH:MM> TYPE <project>:<role> -> crius — text [refs]`.

Old `HH:MM` lines still parse: their date comes from a running state (the previous line's date, plus one day
when the clock goes backwards), which the caller seeds from the file.
"""
import os
import re
from datetime import date, datetime, timedelta
from pathlib import Path

# Lead types (cmux-team §6) plus ALERT, which only Crius's own scripts write.
TYPES = {"DONE", "BLOCKED", "NEED-OWNER", "PAUSED", "LIMIT", "ITEM", "DECISION", "RESUMED", "ALERT"}
MAX_LEN = 300
SLUG = re.compile(r"^[a-z0-9][a-z0-9-]*\Z")
LINE = re.compile(r"^(?:(\d{4}-\d\d-\d\d)T)?(\d\d:\d\d) ([A-Z][A-Z-]*) (\S+) -> (\S+) — (.*)$")
REFS = {
    "item": re.compile(r"\bitem=([a-z0-9-]+#\d+)"),
    "sha": re.compile(r"\bsha=([0-9a-f]{7,40})\b"),
    "pr": re.compile(r"\bpr=(\d+)\b"),
    "path": re.compile(r"\bpath=(\S+)"),
    "decision": re.compile(r"(?<![\w-])([a-z0-9][a-z0-9-]*-D\d+)\b"),
}


def refs_of(text):
    return {k: rx.findall(text) for k, rx in REFS.items() if rx.findall(text)}


def parse_line(line, state=None, strict=True):
    """Parse one line, or return None when it does not fit the grammar.

    `state` ({"date", "time"}) dates undated lines and is updated in place.
    `strict` limits TYPE to the bus types and the sender to <project>:<role>; team logs use strict=False.
    """
    m = LINE.match(line.rstrip("\n"))
    if not m:
        return None
    day, hhmm, type_, sender, to, text = m.groups()
    if strict and type_ not in TYPES:
        return None
    project, sep, role = sender.rpartition(":")
    if not sep:
        if strict:
            return None
        project = None
    elif not SLUG.match(project) or not role:
        return None
    try:
        datetime.strptime(f"{day or '2000-01-01'}T{hhmm}", "%Y-%m-%dT%H:%M")
    except ValueError:
        return None
    guessed = day is None
    if guessed:
        if not state:
            return None
        day = state["date"]
        if hhmm < state["time"]:
            day = (date.fromisoformat(day) + timedelta(days=1)).isoformat()
    if state is not None:
        state["date"], state["time"] = day, hhmm
    return {"date": day, "time": hhmm, "type": type_, "project": project, "role": role, "to": to,
            "text": text, "refs": refs_of(text), "dated_by_guess": guessed}


def format_line(type_, project, role, text, to="crius", now=None):
    if type_ not in TYPES:
        raise ValueError(f"unknown bus type {type_!r}; one of {sorted(TYPES)}")
    if not SLUG.match(project) or not SLUG.match(role):
        raise ValueError("project and role must be lower-case slugs")
    if "\n" in text or "\r" in text:
        raise ValueError("one line only")
    now = now or datetime.now()
    line = f"{now:%Y-%m-%dT%H:%M} {type_} {project}:{role} -> {to} — {text}"
    if len(text) > MAX_LEN:
        raise ValueError(f"text is {len(text)} chars; the limit is {MAX_LEN}")
    if parse_line(line) is None:
        raise ValueError("line does not parse back")
    return line


def append_line(home, project, line):
    if not SLUG.match(project):
        raise ValueError("bad project slug")
    path = Path(home) / "bus" / f"{project}.log"
    fd = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    try:
        os.write(fd, (line + "\n").encode())
    finally:
        os.close(fd)
