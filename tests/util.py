import os
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent


def git(cwd, *args):
    env = dict(os.environ, GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1")
    subprocess.run(["git", "-C", str(cwd), *args], check=True, capture_output=True, env=env)


def git_repo(path):
    path.mkdir(parents=True, exist_ok=True)
    git(path, "init", "-q", "-b", "main")
    git(path, "config", "user.email", "t@example.invalid")
    git(path, "config", "user.name", "t")
    return path


def commit_all(path, msg="c"):
    git(path, "add", "-A")
    git(path, "commit", "-q", "-m", msg)


class TempCase(unittest.TestCase):
    """Each test gets a fresh temp root that holds no git tree above it."""

    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.root = Path(os.path.realpath(self._tmp.name))
        self.home = self.root / "crius-home"
        self._old = os.environ.get("CRIUS_HOME")
        os.environ["CRIUS_HOME"] = str(self.home)

    def tearDown(self):
        if self._old is None:
            os.environ.pop("CRIUS_HOME", None)
        else:
            os.environ["CRIUS_HOME"] = self._old
        self._tmp.cleanup()
