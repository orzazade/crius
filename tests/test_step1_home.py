import os
import stat

from criusrec import home as H
from tests.util import REPO, TempCase, commit_all, git, git_repo


class LayoutTest(TempCase):
    def test_init_creates_private_layout(self):
        H.init_layout(self.home, REPO / "projects.example.toml")
        for sub in ("bus", "state", "spool", "backup", "logs"):
            self.assertTrue((self.home / sub).is_dir(), sub)
        self.assertEqual(stat.S_IMODE(os.stat(self.home).st_mode), 0o700)
        self.assertTrue((self.home / "projects.toml").is_file())

    def test_init_keeps_existing_projects_file(self):
        self.home.mkdir(mode=0o700)
        (self.home / "projects.toml").write_text("# mine\n")
        H.init_layout(self.home, REPO / "projects.example.toml")
        self.assertEqual((self.home / "projects.toml").read_text(), "# mine\n")

    def test_crius_home_follows_env(self):
        self.assertEqual(str(H.crius_home()), str(self.home))


class LeakTest(TempCase):
    def setUp(self):
        super().setUp()
        self.repo = git_repo(self.root / "checkout")
        (self.repo / "README.md").write_text("code only\n")
        commit_all(self.repo)
        H.init_layout(self.home, REPO / "projects.example.toml")

    def test_clean_setup_passes(self):
        self.assertEqual(H.leak_problems(self.home, self.repo), [])

    def test_tracked_db_fails(self):
        (self.repo / "state").mkdir()
        (self.repo / "state" / "record.db").write_bytes(b"x")
        commit_all(self.repo)
        probs = H.leak_problems(self.home, self.repo)
        self.assertTrue(any("record.db" in p for p in probs), probs)

    def test_tracked_bus_log_fails(self):
        (self.repo / "bus").mkdir()
        (self.repo / "bus" / "quotes.log").write_text("x\n")
        commit_all(self.repo)
        self.assertTrue(H.leak_problems(self.home, self.repo))

    def test_home_inside_work_tree_fails(self):
        outer = git_repo(self.root / "outer")
        home = outer / ".crius"
        H.init_layout(home, REPO / "projects.example.toml")
        probs = H.leak_problems(home, self.repo)
        self.assertTrue(any("work tree" in p for p in probs), probs)

    def test_home_inside_work_tree_that_ignores_it_passes(self):
        outer = git_repo(self.root / "outer")
        (outer / ".gitignore").write_text("*\n")
        git(outer, "add", "-f", ".gitignore")
        git(outer, "commit", "-q", "-m", "ignore all")
        home = outer / ".crius"
        H.init_layout(home, REPO / "projects.example.toml")
        self.assertEqual(H.leak_problems(home, self.repo), [])

    def test_tracked_file_under_ignored_home_fails(self):
        outer = git_repo(self.root / "outer")
        (outer / ".gitignore").write_text("*\n")
        home = outer / ".crius"
        H.init_layout(home, REPO / "projects.example.toml")
        git(outer, "add", "-f", ".gitignore", ".crius/projects.toml")
        git(outer, "commit", "-q", "-m", "oops")
        self.assertTrue(H.leak_problems(home, self.repo))

    def test_private_decision_id_in_tracked_file_fails(self):
        d = self.home / "state" / "demo"
        d.mkdir(parents=True)
        (d / "DECISIONS.md").write_text("## demo-D3 · 2026-10-16\nDecision: x\n")
        (self.repo / "NOTES.md").write_text("see demo-D3 for why\n")
        commit_all(self.repo)
        probs = H.leak_problems(self.home, self.repo)
        self.assertTrue(any("demo-D3" in p for p in probs), probs)

    def test_longer_id_is_not_a_match(self):
        d = self.home / "state" / "demo"
        d.mkdir(parents=True)
        (d / "DECISIONS.md").write_text("## demo-D3 · 2026-10-16\nDecision: x\n")
        (self.repo / "NOTES.md").write_text("see demo-D31\n")
        commit_all(self.repo)
        self.assertEqual(H.leak_problems(self.home, self.repo), [])

    def test_staged_private_id_fails_even_if_worktree_is_clean(self):
        d = self.home / "state" / "demo"
        d.mkdir(parents=True)
        (d / "DECISIONS.md").write_text("## demo-D3 · 2026-10-16\nDecision: x\n")
        (self.repo / "README.md").write_text("see demo-D3\n")
        git(self.repo, "add", "README.md")
        (self.repo / "README.md").write_text("code only\n")
        self.assertTrue(any("demo-D3" in p for p in H.leak_problems(self.home, self.repo)))

    def test_symlinked_layout_folder_fails(self):
        (self.home / "bus").rmdir()
        (self.root / "elsewhere").mkdir()
        (self.home / "bus").symlink_to(self.root / "elsewhere")
        self.assertTrue(any("symlink" in p for p in H.leak_problems(self.home, self.repo)))

    def test_unusual_and_nested_private_file_names_fail(self):
        for rel in ("caf\u00e9.db", "nested/bus/demo.log", "state/.gitkeep"):
            path = self.repo / rel
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("x\n")
        commit_all(self.repo)
        probs = H.leak_problems(self.home, self.repo)
        for name in ("caf\u00e9.db", "nested/bus/demo.log", "state/.gitkeep"):
            self.assertTrue(any(name in p for p in probs), (name, probs))

    def test_broken_index_of_the_enclosing_tree_is_a_problem_not_a_pass(self):
        outer = git_repo(self.root / "outer")
        (outer / ".gitignore").write_text("*\n")
        git(outer, "add", "-f", ".gitignore")
        git(outer, "commit", "-q", "-m", "ignore all")
        (outer / ".git" / "index").write_bytes(b"garbage")
        home = outer / ".crius"
        H.init_layout(home, REPO / "projects.example.toml")
        self.assertTrue(H.leak_problems(home, self.repo))

    def test_this_checkout_passes_with_a_fresh_home(self):
        self.assertEqual(H.leak_problems(self.home, REPO), [])
