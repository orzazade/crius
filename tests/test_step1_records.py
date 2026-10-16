import io
import os
import json
from contextlib import redirect_stdout

from criusrec import cli, db, decisions as D, home as H, items as I
from tests.util import REPO, TempCase, commit_all, git_repo


class DecisionTest(TempCase):
    def setUp(self):
        super().setUp()
        H.init_layout(self.home, REPO / "projects.example.toml")

    def rec(self, title, **kw):
        kw.setdefault("decided_by", "crius")
        return D.record_decision(self.home, "demo", title, "because", today="2026-10-16", **kw)

    def entries(self):
        return D.parse_decisions((self.home / "state" / "demo" / "DECISIONS.md").read_text())

    def test_three_decisions_with_a_supersede_give_the_right_active_set(self):
        a = self.rec("use v1")
        b = self.rec("cache prices")
        c = self.rec("use v2", supersedes=a, reason="v1 drops currency")
        self.assertEqual((a, b, c), ("demo-D1", "demo-D2", "demo-D3"))
        entries, problems = self.entries()
        self.assertEqual(problems, [])
        self.assertEqual(D.active_ids(entries), {"demo-D2", "demo-D3"})
        self.assertEqual(entries[2]["supersedes"], "demo-D1")
        self.assertEqual(entries[2]["reason"], "v1 drops currency")

    def test_supersede_chain_leaves_only_the_newest(self):
        a = self.rec("one")
        b = self.rec("two", supersedes=a, reason="r")
        c = self.rec("three", supersedes=b, reason="r")
        self.assertEqual(D.active_ids(self.entries()[0]), {c})

    def test_file_is_append_only(self):
        self.rec("one")
        path = self.home / "state" / "demo" / "DECISIONS.md"
        before = path.read_bytes()
        self.rec("two")
        self.assertTrue(path.read_bytes().startswith(before))

    def test_supersede_needs_a_known_id_and_a_reason(self):
        self.rec("one")
        with self.assertRaises(ValueError):
            self.rec("two", supersedes="demo-D9", reason="r")
        with self.assertRaises(ValueError):
            self.rec("two", supersedes="demo-D1")

    def test_only_the_owner_supersedes_an_owner_decision(self):
        a = self.rec("owner call", decided_by="owner")
        with self.assertRaises(ValueError):
            self.rec("crius changes it", supersedes=a, reason="r")
        self.assertEqual(self.rec("owner changes it", supersedes=a, reason="r", decided_by="owner"), "demo-D2")

    def test_bad_ref_in_file_is_kept_and_flagged(self):
        text = ("## demo-D1 · 2026-10-16\nDecision: a\nWhy: b\nDecided by: crius\n\n"
                "## demo-D2 · 2026-10-16\nDecision: c\nWhy: d\nDecided by: crius\nSupersedes: demo-D7\nReason: r\n")
        entries, problems = D.parse_decisions(text)
        self.assertEqual([e["id"] for e in entries], ["demo-D1", "demo-D2"])
        self.assertTrue(any("demo-D7" in p for p in problems), problems)
        self.assertEqual(D.active_ids(entries), {"demo-D1", "demo-D2"})

    def test_refs_cannot_inject_fields(self):
        a = self.rec("owner call", decided_by="owner")
        with self.assertRaises(ValueError):
            self.rec("sneaky", refs=f"x\nSupersedes: {a}\nReason: r")
        self.assertEqual(D.active_ids(self.entries()[0]), {a})

    def test_failed_bus_line_names_the_saved_decision(self):
        os.chmod(self.home / "bus", 0o500)
        try:
            with self.assertRaisesRegex(RuntimeError, "demo-D1 was saved"):
                self.rec("one")
        finally:
            os.chmod(self.home / "bus", 0o700)
        self.assertEqual([e["id"] for e in self.entries()[0]], ["demo-D1"])

    def test_decision_posts_a_bus_line(self):
        a = self.rec("one")
        log = (self.home / "bus" / "demo.log").read_text().splitlines()
        self.assertEqual(len(log), 1)
        self.assertIn(" DECISION demo:crius -> crius — ", log[0])
        self.assertIn(a, log[0])


class ItemTest(TempCase):
    def setUp(self):
        super().setUp()
        H.init_layout(self.home, REPO / "projects.example.toml")
        self.conn = db.connect(self.home)

    def tearDown(self):
        self.conn.close()
        super().tearDown()

    def test_ids_count_per_project_and_keep_criteria(self):
        a = I.record_item(self.conn, "demo", "price fix", "code", ["CI pass", "codex review"])
        b = I.record_item(self.conn, "demo", "child", "code", ["tests"], parent=a)
        c = I.record_item(self.conn, "other", "x", "research", ["owner reads it"])
        self.assertEqual((a, b, c), ("demo#1", "demo#2", "other#1"))
        row = self.conn.execute("select criteria, parent, state from item where id=?", (b,)).fetchone()
        self.assertEqual(json.loads(row[0]), ["tests"])
        self.assertEqual(row[1:], ("demo#1", "open"))

    def test_item_needs_criteria_known_kind_and_known_parent(self):
        with self.assertRaises(ValueError):
            I.record_item(self.conn, "demo", "x", "code", [])
        with self.assertRaises(ValueError):
            I.record_item(self.conn, "demo", "x", "size-l", ["t"])
        with self.assertRaises(ValueError):
            I.record_item(self.conn, "demo", "x", "code", ["t"], parent="demo#9")
        with self.assertRaises(ValueError):
            I.record_item(self.conn, "demo\n", "x", "code", ["t"])

    def test_private_item_id_in_tracked_file_fails_the_leak_test(self):
        I.record_item(self.conn, "demo", "x", "code", ["t"])
        repo = git_repo(self.root / "checkout")
        (repo / "NOTES.md").write_text("closes demo#1\n")
        commit_all(repo)
        self.assertTrue(any("demo#1" in p for p in H.leak_problems(self.home, repo)))


class CliTest(TempCase):
    def run_cli(self, *args):
        out = io.StringIO()
        with redirect_stdout(out):
            rc = cli.main(list(args))
        return rc, out.getvalue()

    def test_init_record_and_leak_test(self):
        # Built at runtime: the leak test (rightly) fails if a tracked file holds a literal id from the store.
        slug = "zz" + "cli"
        self.assertEqual(self.run_cli("init")[0], 0)
        rc, out = self.run_cli("record", "item", "--project", slug, "--title", "t", "--kind", "code",
                               "--criteria", "CI pass", "--criteria", "codex review")
        self.assertEqual((rc, out.strip()), (0, slug + "#1"))
        rc, out = self.run_cli("record", "decision", "--project", slug, "--title", "t", "--why", "w", "--by", "crius")
        self.assertEqual((rc, out.strip()), (0, slug + "-D1"))
        rc, out = self.run_cli("record", "bus", "--project", slug, "--role", "leader", "--type", "DONE",
                               "--text", f"item={slug}#1 sha=abc1234 done")
        self.assertEqual(rc, 0)
        self.assertEqual(len((self.home / "bus" / f"{slug}.log").read_text().splitlines()), 2)
        rc, out = self.run_cli("leak-test", "--repo", str(REPO))
        self.assertEqual(rc, 0, out)

    def test_bad_bus_line_exits_nonzero(self):
        self.run_cli("init")
        rc, _ = self.run_cli("record", "bus", "--project", "demo", "--role", "leader", "--type", "WAT", "--text", "x")
        self.assertNotEqual(rc, 0)
