// Package cli is crius-rec, the door to the record store. Commands: init, leak-test, record bus|decision|item (step 1),
// quota (step 3).
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
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
       crius-rec quota [--json | --room]
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
	case "quota":
		fs := flag.NewFlagSet("quota", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "print the report as JSON")
		room := fs.Bool("room", false, "print whether the Max account has room: one JSON line; exit 0 room, 3 full, 4 unknown")
		if fs.Parse(args) != nil || fs.NArg() > 0 || *asJSON && *room {
			return exitUsage
		}
		return quotaCmd(home, time.Now(), *asJSON, *room, stdout, stderr)
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

// quotaCmd prints the two quota blocks (or the report as JSON), starting with the status and its reasons; anything
// but complete exits partial. Everything it prints is redacted: errors can quote a config value.
func quotaCmd(home string, now time.Time, asJSON, room bool, stdout, stderr io.Writer) int {
	stdout, stderr = ingest.Redacting{W: stdout}, ingest.Redacting{W: stderr}
	// fail says why on stderr; with --room every failure is also an "unknown" verdict on stdout, the one line a
	// launcher reads, with a short reason (the full one is on stderr).
	fail := func(rc int, reason, msg string) int {
		fmt.Fprintln(stderr, msg)
		if room {
			roomLine(stdout, "unknown", reason, nil)
			return exitUnavailable
		}
		return rc
	}
	if fi, err := os.Stat(filepath.Join(home, "state")); err != nil || !fi.IsDir() {
		return fail(exitUnavailable, "quota store missing", fmt.Sprintf("unavailable: %s is missing; run crius-rec init", home))
	}
	projects, err := ingest.QuotaProjects(home)
	if err != nil {
		return fail(exitError, "quota config error", fmt.Sprintf("error: %v", err))
	}
	db, err := store.Open(home)
	if err != nil {
		return fail(exitError, "quota store error", fmt.Sprintf("error: %v", err))
	}
	defer db.Close()
	rep, err := quota.Build(db, projects, now)
	if err != nil {
		return fail(exitError, "quota report error", fmt.Sprintf("error: %v", err))
	}
	if room {
		v, reason := quota.Room(rep)
		roomLine(stdout, v, reason, rep)
		switch v {
		case "room":
			return 0
		case "full":
			return exitPartial
		}
		fmt.Fprintf(stderr, "unknown: %s\n", reason)
		return exitUnavailable
	}
	if asJSON {
		b, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "%s\n", b)
	} else {
		fmt.Fprint(stdout, capLines(rep.Text(), maxLines))
	}
	if rep.Status != "complete" {
		return exitPartial
	}
	return 0
}

// roomLine is the --room answer: one JSON line. Launchers read verdict and reason; the numbers are null when there
// is no report.
func roomLine(w io.Writer, verdict, reason string, r *quota.Report) {
	v := struct {
		Verdict    string   `json:"verdict"`
		Reason     string   `json:"reason"`
		Seven      *float64 `json:"seven"`
		Five       *float64 `json:"five"`
		Reset      string   `json:"reset"`
		SampleAgeS *int64   `json:"sample_age_s"`
	}{Verdict: verdict, Reason: reason}
	if r != nil && !r.SevenAt.IsZero() {
		age := int64(r.Now.Sub(r.MeasuredAt()).Seconds())
		v.Seven, v.Five, v.SampleAgeS = &r.SevenPct, &r.FivePct, &age
	}
	if r != nil && !r.Reset.IsZero() {
		v.Reset = r.Reset.UTC().Format(time.RFC3339)
	}
	b, _ := json.Marshal(v) // a struct of strings and numbers always marshals
	fmt.Fprintf(w, "%s\n", b)
}

// maxLines caps text output (design §4); --json has everything.
const maxLines = 20

// capLines keeps the first n-1 lines of s and says how many it left out, when s has more than n lines.
func capLines(s string, n int) string {
	lines := strings.SplitAfter(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[:n-1], "") + fmt.Sprintf("... %d more lines (use --json)\n", len(lines)-n+1)
}
