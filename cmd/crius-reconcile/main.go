// Command crius-reconcile compares a team's board with git, read-only (package reconcile): it flags a row whose work is
// already on the integration branch while its state is not done (STALE), and a row whose state is done while none of
// its shas or PRs is on the branch (UNLANDED). Run it before Crius or a builder picks a next row.
//
//	crius-reconcile --board FILE --format tsv|md [--section TEXT] --git REPO:BRANCH [--git REPO:BRANCH …]
//	                [--done LIST] [--partial LIST] [--skip LIST] [--depth N]
//
// --git may repeat: a sha on any repo's branch counts as landed (a row may name a web and a backend sha). LISTs are
// comma-separated states, compared without case. It never fetches: fetch first if the local branch may be behind.
//
// Exit codes: 0 every row agrees with git, 4 mismatches found (one line each), 1 failed to look (an unreadable board,
// a board that parses to 0 rows, a missing branch, a git error: never reported as clean), 2 bad arguments.
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
		done    = fs.String("done", "merged,done,proven,landed,✅", "states that mean the work landed")
		partial = fs.String("partial", "🔶,🟡,partial", "states of rows that may have landed parts (noted, not judged)")
		skip    = fs.String("skip", "✖,dropped,cancelled", "states not judged")
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
	if *format == "tsv" {
		rows, err = reconcile.ParseTSV(f)
	} else {
		rows, err = reconcile.ParseMarkdown(f, *section)
	}
	if err != nil {
		return fail(fmt.Errorf("%s: %w", *board, err))
	}
	if len(rows) == 0 {
		return fail(fmt.Errorf("%s parsed to 0 rows (format %s, section %q): nothing was checked", *board, *format, *section))
	}
	rep, err := reconcile.Check(rows, repos, reconcile.Config{Done: list(*done), Partial: list(*partial), Skip: list(*skip), Depth: *depth})
	if err != nil {
		return fail(err)
	}
	stale, unlanded := 0, 0
	for _, fd := range rep.Findings {
		fmt.Fprintln(out, line(fd))
		if fd.Kind == "stale" {
			stale++
		} else {
			unlanded++
		}
	}
	fmt.Fprintf(out, "SUMMARY %s: %d rows (%d done, %d open, %d partial, %d skipped) · %d stale · %d unlanded\n",
		*board, rep.Rows, rep.Done, rep.Open, rep.Partial, rep.Skipped, stale, unlanded)
	if len(rep.PartialLanded) > 0 {
		fmt.Fprintf(out, "  partial rows with landed work (not judged): %s\n", strings.Join(rep.PartialLanded, " "))
	}
	if len(rep.Unchecked) > 0 {
		fmt.Fprintf(out, "  done rows naming no sha or PR, id in no scanned subject (not checked): %s\n", strings.Join(rep.Unchecked, " "))
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

// line is one mismatch: kind, row id, state and board line, then the evidence (stale) or what was missing (unlanded).
func line(fd reconcile.Finding) string {
	head := fmt.Sprintf("%s %s state=%s (line %d)", strings.ToUpper(fd.Kind), fd.Row.ID, fd.Row.State, fd.Row.Line)
	if fd.Kind != "stale" {
		return head + " but nothing on the branch: " + strings.Join(fd.Misses, "; ")
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
