"""The private home ~/.crius: layout and the git-leak test (design §3b)."""
import fnmatch
import os
import re
import shutil
import sqlite3
import subprocess
from pathlib import Path

SUBDIRS = ("bus", "state", "spool", "backup", "logs")
# Tracked paths in the crius checkout that would mean private state got committed.
LEAK_GLOBS = ("*.db", "*.db-wal", "*.db-shm", "state/*", "*/state/*", "bus/*.log", "*/bus/*.log", "spool/*",
              "*/spool/*")
DECISION_ID = re.compile(r"^## ([a-z0-9-]+-D\d+) ", re.M)


def crius_home():
    return Path(os.environ.get("CRIUS_HOME") or Path.home() / ".crius")


def init_layout(home, example):
    home = Path(home)
    home.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(home, 0o700)
    for sub in SUBDIRS:
        (home / sub).mkdir(mode=0o700, exist_ok=True)
    cfg = home / "projects.toml"
    if not cfg.exists():
        shutil.copyfile(example, cfg)
        os.chmod(cfg, 0o600)


def _git(cwd, *args):
    return subprocess.run(["git", "-C", str(cwd), *args], capture_output=True, text=True)


def _private_ids(home):
    ids = set()
    for f in (home / "state").glob("*/DECISIONS.md"):
        ids.update(DECISION_ID.findall(f.read_text(errors="replace")))
    dbf = home / "state" / "record.db"
    if dbf.exists():
        conn = sqlite3.connect(f"file:{dbf}?mode=ro", uri=True)
        try:
            if conn.execute("select 1 from sqlite_master where name='item'").fetchone():
                ids.update(r[0] for r in conn.execute("select id from item"))
        finally:
            conn.close()
    return ids


def _tracked(repo, *args):
    """NUL-separated `git ls-files` output (no quoting of unusual names), or None when git fails."""
    p = subprocess.run(["git", "-C", str(repo), "ls-files", "-z", *args], capture_output=True)
    return None if p.returncode else [x.decode("utf-8", "surrogateescape") for x in p.stdout.split(b"\0") if x]


def leak_problems(home, repo):
    """Every way private state could reach git. An empty list means the test passed."""
    home, repo = Path(home), Path(repo).resolve()
    problems = []

    # 0. The layout is real folders: a symlink could point the private data anywhere.
    for p in [home] + [home / sub for sub in SUBDIRS]:
        if p.is_symlink():
            problems.append(f"{p} is a symlink; private folders must be real folders")
    home = home.resolve()

    # 1. The home must not be committable: either outside every work tree, or ignored and untracked there.
    top = _git(home, "rev-parse", "--show-toplevel")
    if top.returncode == 0:
        top_dir = top.stdout.strip()
        rel = os.path.relpath(home, top_dir)
        if _git(top_dir, "check-ignore", "-q", "--no-index", rel + "/").returncode != 0:
            problems.append(f"{home} is inside the git work tree {top_dir} and not ignored there")
        tracked = _tracked(top_dir, "--", rel)
        if tracked is None:
            problems.append(f"cannot list tracked files in {top_dir}")
        elif tracked:
            problems.append(f"{len(tracked)} file(s) under {home} are tracked by {top_dir}, e.g. {tracked[0]}")
    elif "not a git repository" not in top.stderr:
        problems.append(f"cannot tell whether {home} is in a git work tree: {top.stderr.strip()[:200]}")

    # 2. No private-state file is tracked (or staged) in the crius checkout.
    paths = _tracked(repo)
    if paths is None:
        problems.append(f"cannot list tracked files in {repo}")
        return problems
    for path in paths:
        if any(fnmatch.fnmatch(path, g) for g in LEAK_GLOBS):
            problems.append(f"tracked private-state file in {repo}: {path}")

    # 3. No id that exists only in private files appears in a tracked file, in the work tree or the index.
    ids = sorted(_private_ids(home))
    if ids:
        pattern = [a for i in ids for a in ("-e", i)]
        for where in ([], ["--cached"]):
            found = subprocess.run(["git", "-C", str(repo), "grep", "-z", "-I", "-w", "-F", *where, *pattern],
                                   capture_output=True)
            if found.returncode not in (0, 1):
                problems.append(f"git grep failed in {repo}: {found.stderr.decode(errors='replace').strip()}")
                continue
            # With -z (and no -n) each match is "path NUL line LF".
            for rec in found.stdout.decode("utf-8", "replace").split("\n"):
                path, _, text = rec.partition("\0")
                hits = [i for i in ids if re.search(r"(?<![\w#-])" + re.escape(i) + r"(?![\w#])", text)]
                if hits:
                    msg = f"private id(s) {', '.join(hits)} appear in tracked file {path}" + (
                        " (staged)" if where else "")
                    if msg not in problems:
                        problems.append(msg)
    return problems
