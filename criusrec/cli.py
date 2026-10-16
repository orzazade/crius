"""crius-rec: the door to the record store. Step 1 commands: init, leak-test, record bus|decision|item."""
import argparse
import sys
from pathlib import Path

from . import bus, db, decisions, home as H, items

REPO = Path(__file__).resolve().parent.parent
EXIT_UNAVAILABLE = 4


def _parser():
    p = argparse.ArgumentParser(prog="crius-rec")
    sub = p.add_subparsers(dest="cmd", required=True)
    sub.add_parser("init", help="create ~/.crius with mode 700")
    lk = sub.add_parser("leak-test", help="fail if private state could reach git")
    lk.add_argument("--repo", default=str(REPO))
    rec = sub.add_parser("record").add_subparsers(dest="what", required=True)
    b = rec.add_parser("bus")
    b.add_argument("--project", required=True)
    b.add_argument("--role", required=True)
    b.add_argument("--type", required=True)
    b.add_argument("--text", required=True)
    d = rec.add_parser("decision")
    d.add_argument("--project", required=True)
    d.add_argument("--title", required=True)
    d.add_argument("--why", required=True)
    d.add_argument("--by", required=True, help="owner | crius | lead, optionally with a ref: 'owner (task #54)'")
    d.add_argument("--supersedes")
    d.add_argument("--reason")
    d.add_argument("--refs", default="")
    d.add_argument("--taint", action="store_true")
    i = rec.add_parser("item")
    i.add_argument("--project", required=True)
    i.add_argument("--title", required=True)
    i.add_argument("--kind", required=True, choices=items.KINDS)
    i.add_argument("--criteria", action="append", required=True)
    i.add_argument("--parent")
    i.add_argument("--taint", action="store_true")
    return p


def main(argv=None):
    args = _parser().parse_args(argv)
    home = H.crius_home()
    if args.cmd == "init":
        H.init_layout(home, REPO / "projects.example.toml")
        print(f"ok {home}")
        return 0
    if args.cmd == "leak-test":
        problems = H.leak_problems(home, args.repo)
        print("unavailable" if problems else "complete")
        for prob in problems:
            print(f"  {prob}")
        return EXIT_UNAVAILABLE if problems else 0
    if not (home / "state").is_dir():
        print(f"unavailable: {home} is missing; run crius-rec init", file=sys.stderr)
        return EXIT_UNAVAILABLE
    try:
        if args.what == "bus":
            line = bus.format_line(args.type, args.project, args.role, args.text)
            bus.append_line(home, args.project, line)
            print(line)
        elif args.what == "decision":
            print(decisions.record_decision(home, args.project, args.title, args.why, args.by, args.supersedes,
                                            args.reason, args.refs, args.taint))
        elif args.what == "item":
            with db.writer_lock(home):
                conn = db.connect(home)
                try:
                    print(items.record_item(conn, args.project, args.title, args.kind, args.criteria, args.parent,
                                            args.taint))
                finally:
                    conn.close()
    except ValueError as e:
        print(f"refused: {e}", file=sys.stderr)
        return 2
    except RuntimeError as e:
        print(f"partial: {e}", file=sys.stderr)
        return 3
    return 0


if __name__ == "__main__":
    sys.exit(main())
