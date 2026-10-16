import re
import unittest
from datetime import datetime

from criusrec import bus, home
from tests.util import REPO, TempCase

DATED = "2026-10-16T09:20 DONE quotes:leader -> crius — item=quotes#41 sha=abc1234 Inc 4 QA-verified; see quotes-D12"


class ParseTest(unittest.TestCase):
    def test_dated_line(self):
        p = bus.parse_line(DATED)
        self.assertEqual(p["date"], "2026-10-16")
        self.assertEqual(p["time"], "09:20")
        self.assertEqual(p["type"], "DONE")
        self.assertEqual((p["project"], p["role"]), ("quotes", "leader"))
        self.assertEqual(p["to"], "crius")
        self.assertTrue(p["text"].startswith("item=quotes#41"))
        self.assertEqual(p["refs"]["item"], ["quotes#41"])
        self.assertEqual(p["refs"]["sha"], ["abc1234"])
        self.assertEqual(p["refs"]["decision"], ["quotes-D12"])
        self.assertFalse(p["dated_by_guess"])

    def test_undated_line_still_parses(self):
        state = {"date": "2026-10-15", "time": "08:00"}
        p = bus.parse_line("09:20 BLOCKED axi:leader -> crius — waiting on CI", state)
        self.assertEqual((p["date"], p["time"], p["type"]), ("2026-10-15", "09:20", "BLOCKED"))
        self.assertTrue(p["dated_by_guess"])

    def test_undated_line_after_midnight_moves_to_next_day(self):
        state = {"date": "2026-10-15", "time": "23:50"}
        p = bus.parse_line("00:10 DONE axi:leader -> crius — x", state)
        self.assertEqual(p["date"], "2026-10-16")
        self.assertEqual(state, {"date": "2026-10-16", "time": "00:10"})

    def test_dated_line_resets_the_state(self):
        state = {"date": "2026-10-01", "time": "10:00"}
        bus.parse_line(DATED, state)
        self.assertEqual(state, {"date": "2026-10-16", "time": "09:20"})

    def test_malformed_lines(self):
        for line in ("hello", "09:20 DONE no arrow here", "2026-10-16T09:20 done quotes:leader -> crius — x", ""):
            self.assertIsNone(bus.parse_line(line), line)

    def test_impossible_date_or_time_is_malformed_and_keeps_state(self):
        state = {"date": "2026-10-15", "time": "08:00"}
        for line in ("2026-99-99T09:20 DONE quotes:leader -> crius — x", "25:61 DONE quotes:leader -> crius — x"):
            self.assertIsNone(bus.parse_line(line, state), line)
        self.assertEqual(state, {"date": "2026-10-15", "time": "08:00"})

    def test_unknown_type_strict_and_lenient(self):
        line = "09:20 NEED api -> leader — schema?"
        self.assertIsNone(bus.parse_line(line, {"date": "2026-10-16", "time": "09:00"}))
        p = bus.parse_line(line, {"date": "2026-10-16", "time": "09:00"}, strict=False)
        self.assertEqual((p["type"], p["project"], p["role"]), ("NEED", None, "api"))

    def test_new_types_are_accepted(self):
        for t in ("NEED-OWNER", "PAUSED", "LIMIT", "ITEM", "DECISION", "RESUMED"):
            self.assertIsNotNone(bus.parse_line(f"2026-10-16T09:20 {t} quotes:leader -> crius — x"), t)


class FormatTest(TempCase):
    def test_format_is_dated_and_parses_back(self):
        line = bus.format_line("DONE", "quotes", "leader", "item=quotes#41 sha=abc1234 done",
                               now=datetime(2026, 10, 16, 9, 5))
        self.assertEqual(line, "2026-10-16T09:05 DONE quotes:leader -> crius — item=quotes#41 sha=abc1234 done")
        self.assertIsNotNone(bus.parse_line(line))

    def test_format_refuses_bad_input(self):
        for args in (("DONE", "quotes", "leader", "x" * 301), ("NOPE", "quotes", "leader", "x"),
                     ("DONE", "Quotes!", "leader", "x"), ("DONE", "quotes", "leader", "two\nlines")):
            with self.assertRaises(ValueError, msg=args[:3]):
                bus.format_line(*args)

    def test_append_line_goes_to_project_log(self):
        home.init_layout(self.home, REPO / "projects.example.toml")
        line = bus.format_line("DONE", "quotes", "leader", "x")
        bus.append_line(self.home, "quotes", line)
        bus.append_line(self.home, "quotes", line)
        text = (self.home / "bus" / "quotes.log").read_text()
        self.assertEqual(text, line + "\n" + line + "\n")
        self.assertRegex(line, re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d DONE "))
