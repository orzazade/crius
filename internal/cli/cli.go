// Package cli is crius-rec, the door to the record store. Step 1 commands: init, leak-test, record bus|decision|item.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/store"
)

const (
	exitError       = 1
	exitUsage       = 2 // bad arguments, or input refused before anything was written
	exitPartial     = 3
	exitUnavailable = 4
)

const usage = `usage: crius-rec init
       crius-rec leak-test [--repo DIR]   (default: $CRIUS_REPO, set by bin/crius-rec)
       crius-rec record bus --project P --role R --type T --text TEXT
       crius-rec record decision --project P --title T --why W --by "owner|crius|lead [ref]" [--supersedes ID --reason R] [--refs R] [--taint]
       crius-rec record item --project P --title T --kind code|research|falcon|other --criteria C [--criteria C ...] [--parent ID] [--taint]
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Run runs one command and returns its exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	home := store.Home()
	cmd := ""
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		if fs.Parse(args) != nil || fs.NArg() > 0 {
			return exitUsage
		}
		if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "ok %s\n", home)
		return 0
	case "leak-test":
		fs := flag.NewFlagSet("leak-test", flag.ContinueOnError)
		fs.SetOutput(stderr)
		repo := fs.String("repo", os.Getenv("CRIUS_REPO"), "the crius checkout to check")
		if fs.Parse(args) != nil || fs.NArg() > 0 {
			return exitUsage
		}
		if *repo == "" {
			fmt.Fprintln(stderr, "leak-test needs --repo or $CRIUS_REPO (bin/crius-rec sets it)")
			return exitUsage
		}
		problems := store.LeakProblems(home, *repo)
		if len(problems) == 0 {
			fmt.Fprintln(stdout, "complete")
			return 0
		}
		fmt.Fprintln(stdout, "unavailable")
		for _, p := range problems {
			fmt.Fprintf(stdout, "  %s\n", p)
		}
		return exitUnavailable
	case "record":
		return record(home, args, stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return exitUsage
}

func record(home string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	what, args := args[0], args[1:]
	fs := flag.NewFlagSet("record "+what, flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "")
	var role, typ, text, title, why, by, supersedes, reason, refs, kind, parent *string
	var taint *bool
	var criteria multi
	required := []string{"project"}
	switch what {
	case "bus":
		role, typ, text = fs.String("role", "", ""), fs.String("type", "", ""), fs.String("text", "", "")
		required = append(required, "role", "type", "text")
	case "decision":
		title, why, by = fs.String("title", "", ""), fs.String("why", "", ""), fs.String("by", "", "")
		supersedes, reason, refs = fs.String("supersedes", "", ""), fs.String("reason", "", ""), fs.String("refs", "", "")
		taint = fs.Bool("taint", false, "")
		required = append(required, "title", "why", "by")
	case "item":
		title, kind, parent = fs.String("title", "", ""), fs.String("kind", "", ""), fs.String("parent", "", "")
		fs.Var(&criteria, "criteria", "")
		taint = fs.Bool("taint", false, "")
		required = append(required, "title", "kind", "criteria")
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitUsage
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	for _, name := range required {
		if !given[name] {
			fmt.Fprintf(stderr, "record %s needs --%s\n", what, name)
			return exitUsage
		}
	}
	if fi, err := os.Stat(filepath.Join(home, "state")); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "unavailable: %s is missing; run crius-rec init\n", home)
		return exitUnavailable
	}

	var out string
	var err error
	switch what {
	case "bus":
		out, err = store.FormatLine(*typ, *project, *role, *text, "crius", time.Now())
		if err == nil {
			err = store.AppendLine(home, *project, out)
		}
	case "decision":
		out, err = store.RecordDecision(home, store.DecisionInput{Project: *project, Title: *title, Why: *why,
			DecidedBy: *by, Supersedes: *supersedes, Reason: *reason, Refs: *refs, Taint: *taint})
	case "item":
		out, err = recordItem(home, *project, *title, *kind, criteria, *parent, *taint)
	}
	switch {
	case err == nil:
		fmt.Fprintln(stdout, out)
		return 0
	case store.IsRefused(err):
		fmt.Fprintf(stderr, "refused: %v\n", err)
		return exitUsage
	case store.IsPartial(err):
		fmt.Fprintf(stderr, "partial: %v\n", err)
		return exitPartial
	}
	fmt.Fprintf(stderr, "error: %v\n", err)
	return exitError
}

func recordItem(home, project, title, kind string, criteria []string, parent string, taint bool) (string, error) {
	unlock, err := store.WriterLock(home, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	db, err := store.Open(home)
	if err != nil {
		return "", err
	}
	id, err := store.RecordItem(db, project, title, kind, criteria, parent, taint)
	return id, errors.Join(err, db.Close())
}
