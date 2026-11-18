// Command crius-reconcile compares a team's board with git, read-only (package reconcile): it flags a row whose work is
// already on the integration branch while its state is not done (STALE), and a row whose state is done while one of
// its own shas or PRs is not on the branch (UNLANDED). Run it before Crius or a builder picks a next row.
//
//	crius-reconcile --board FILE --format tsv|md [--section TEXT] --git REPO:BRANCH [--git REPO:BRANCH …]
//	                [--done LIST] [--partial LIST] [--skip LIST] [--open LIST] [--depth N]
//
// --git may repeat: a sha on any repo's branch counts as landed (a row may name a web and a backend sha). LISTs are
// comma-separated states, compared without case. It never fetches: BRANCH is read as the local repo has it, so for a
// clone pass REPO:origin/BRANCH (as fresh as that clone's last fetch).
//
// Besides the mismatches it prints, without changing the exit code: NOT-READ lines (in the section, they look like a
// row but give none), rows in a state no list names (unknown, not judged), partial rows with landed work, done rows
// naming no own sha or PR (not checked), and PRs merged in more than one repo (ambiguous, not counted).
//
// Exit codes: 0 no mismatch, 4 mismatches found (one line each), 1 failed to look (an unreadable board, a board that
// parses to 0 rows, a missing branch, a git error: never reported as clean), 2 bad arguments.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orzazade/crius/internal/reconcile"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type gitFlags []reconcile.Repo

func (g *gitFlags) String() string { return "" }
func (g *gitFlags) Set(v string) error {
	i := strings.LastIndex(v, ":")
	if i <= 0 || i == len(v)-1 {
		return fmt.Errorf("--git %q: want REPO:BRANCH", v)
	}
	*g = append(*g, reconcile.Repo{Path: v[:i], Branch: v[i+1:]})
	return nil
}

func list(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("crius-reconcile", flag.ContinueOnError)
	fs.SetOutput(errw)
	var repos gitFlags
	fs.Var(&repos, "git", "REPO:BRANCH to check against (repeatable)")
	var (
		board   = fs.String("board", "", "board file")
		format  = fs.String("format", "", "tsv or md")
		section = fs.String("section", "", "md: only rows under the heading that contains this text")
		done    = fs.String("done", "merged,done,proven,landed,✅,[x]", "states that mean the work landed")
		partial = fs.String("partial", "🔶,🟡,partial,[~]", "states of rows that may have landed parts (noted, not judged)")
		skip    = fs.String("skip", "✖,dropped,cancelled", "states not judged")
		open    = fs.String("open", "⬜,open,todo,ready,building,check-written,in-progress,review,pr,blocked,[ ]", "states that mean the work has not landed")
		depth   = fs.Int("depth", 5000, "commits per branch the subject searches read")
	)
	bad := func(msg string) int {
		fmt.Fprintln(errw, "crius-reconcile:", msg)
		return 2
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch {
	case fs.NArg() > 0:
		return bad(fmt.Sprintf("unexpected %q", fs.Args()))
	case *board == "":
		return bad("--board is required")
	case *format != "tsv" && *format != "md":
		return bad(fmt.Sprintf("--format %q: want tsv or md", *format))
	case len(repos) == 0:
		return bad("--git REPO:BRANCH is required")
	case *depth < 1:
		return bad("--depth must be at least 1")
	}
	fail := func(err error) int {
		fmt.Fprintln(errw, "crius-reconcile: FAILED TO LOOK:", err)
		return 1
	}
	f, err := os.Open(*board)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	var rows []reconcile.Row
	var notRead []reconcile.NotRead
	if *format == "tsv" {
		rows, err = reconcile.ParseTSV(f)
	} else {
		rows, notRead, err = reconcile.ParseMarkdown(f, *section)
	}
	if err != nil {
		return fail(fmt.Errorf("%s: %w", *board, err))
	}
	if len(rows) == 0 {
		return fail(fmt.Errorf("%s parsed to 0 rows (format %s, section %q, %d lines not read): nothing was checked", *board, *format, *section, len(notRead)))
	}
	rep, err := reconcile.Check(rows, repos, reconcile.Config{Done: list(*done), Partial: list(*partial), Skip: list(*skip),
		Open: list(*open), Depth: *depth})
	if err != nil {
		return fail(err)
	}
	count := map[string]int{}
	for _, fd := range rep.Findings {
		fmt.Fprintln(out, line(fd))
		count[fd.Kind]++
	}
	for _, n := range notRead {
		fmt.Fprintf(out, "NOT-READ line %d: %s\n", n.Line, n.Text)
	}
	fmt.Fprintf(out, "SUMMARY %s: %d rows (%d done, %d open, %d partial, %d skipped, %d unknown) · %d stale · %d unlanded · %d ambiguous · %d not read\n",
		*board, rep.Rows, rep.Done, rep.Open, rep.Partial, rep.Skipped, len(rep.Unknown), count["stale"], count["unlanded"],
		count["ambiguous"], len(notRead))
	if len(rep.Unknown) > 0 {
		fmt.Fprintf(out, "  unknown state: %s (not judged; name the state with --done/--open/--partial/--skip)\n", strings.Join(rep.Unknown, " "))
	}
	if len(rep.PartialLanded) > 0 {
		fmt.Fprintf(out, "  partial rows with landed work (not judged): %s\n", strings.Join(rep.PartialLanded, " "))
	}
	if len(rep.Unchecked) > 0 {
		fmt.Fprintf(out, "  done rows naming no own sha or PR (not checked): %s\n", strings.Join(rep.Unchecked, " "))
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(out, "  note: %s\n", n)
	}
	for _, s := range rep.Scans {
		fmt.Fprintf(out, "  read %s %s at %.10s: %d commits back to %s for subjects; shas checked against the whole branch\n",
			filepath.Base(s.Repo), s.Branch, s.Tip, s.Commits, s.Oldest)
	}
	if len(rep.Findings) > 0 {
		return 4
	}
	return 0
}

// line is one mismatch: kind, row id, state and board line, then the evidence (stale), what was missing (unlanded)
// or which PR merged in several repos (ambiguous).
func line(fd reconcile.Finding) string {
	head := fmt.Sprintf("%s %s state=%s (line %d)", strings.ToUpper(fd.Kind), fd.Row.ID, fd.Row.State, fd.Row.Line)
	switch fd.Kind {
	case "unlanded":
		return head + " but not on the branch: " + strings.Join(fd.Misses, "; ")
	case "ambiguous":
		return head + ", cannot tell which repo's PR is this row's: " + strings.Join(fd.Misses, "; ")
	}
	var ev []string
	for _, e := range fd.Evidence {
		where := e.Branch + " (" + filepath.Base(e.Repo) + ")"
		switch e.Kind {
		case "sha":
			ev = append(ev, fmt.Sprintf("sha %s is on %s", e.Ref, where))
		case "merge-subject":
			ev = append(ev, fmt.Sprintf("%s merged: %s %q on %s", e.Ref, e.Commit, e.Subject, where))
		default:
			ev = append(ev, fmt.Sprintf("row id in subject: %s %q on %s", e.Commit, e.Subject, where))
		}
	}
	return head + " but landed: " + strings.Join(ev, "; ")
}
