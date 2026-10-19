// Package cli is crius-rec, the door to the record store. Commands: init, leak-test, record bus|decision|item (step 1),
// quota (step 3), find, show and health (step 4), digest (step 5), record accept|defect|revoke|correct and scorecard
// (step 6).
package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/digest"
	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/recall"
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/scorecard"
	"github.com/orzazade/crius/internal/store"
)

const (
	exitError       = 1
	exitUsage       = 2 // bad arguments, or input refused before anything was written
	exitPartial     = 3
	exitUnavailable = 4
	exitExpired     = 5 // evidence past retention; the durable rows left are shown
)

const usage = `usage: crius-rec init
       crius-rec leak-test [--repo DIR]   (default: $CRIUS_REPO, set by bin/crius-rec)
       crius-rec record bus --project P --role R --type T --text TEXT [--to crius | P:R]
       crius-rec record decision --project P --title T --why W --by "owner|crius|lead [ref]" [--supersedes ID --reason R] [--refs R] [--taint]
       crius-rec record item --project P --title T --kind code|research|falcon|other --criteria C [--criteria C ...] [--parent ID] [--taint]
       crius-rec record accept ITEM --revision SHA|SHA256 --check N=REF [--check N=REF ...] --review event:N --by crius|owner
                 (every criterion N needs a check; each REF, a commit sha or event:N, must name the revision)
       crius-rec record defect ITEM --title T --found-by us|owner|others --severity S1|S2|S3 --class C
                 (C: missing test, wrong assumption, stale context, silent failure, misread request,
                 harness or tool, review gap, bad data; the 2nd defect of a class opens a guard item)
       crius-rec record revoke ITEM --defect D-N --reason R --criteria C [--criteria C ...] --by crius|owner
                 (the acceptance row stays, revoked; a fix item opens with these criteria)
       crius-rec record correct --corrects ITEM|D-N --field F --value V --reason R --by crius|owner
                 (the row stays; show and the views read the new value)
       crius-rec quota [--json | --room]
       crius-rec find [QUERY] [--project P] [--since YYYY-MM-DD|Nd|Nh] [--kind K ...] [--active] [--waiting] [--json]
       crius-rec find QUERY --deep --project P [--since …] [--json]
       crius-rec show ID|PROJECT [--history] [--json]
       crius-rec health [--json]
       crius-rec digest [--write [--print-path]]   (the goal report; --write saves it under state/digest/,
                 --print-path prints the saved path instead of the report)
       crius-rec scorecard [--week YYYY-MM-DD] [--write]   (the quota week ending that UTC day, default the running one;
                 --write saves an ended week under state/scorecards/, a changed one gets a revised line)
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ", ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Run runs one command and returns its exit code. Everything it prints is redacted: output and error text can quote
// an argument, a config value or a stored line (design §3c).
func Run(args []string, stdout, stderr io.Writer) int {
	stdout, stderr = redact.Writer{W: stdout}, redact.Writer{W: stderr}
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
	case "find":
		return findCmd(home, args, time.Now(), stdout, stderr)
	case "show":
		return showCmd(home, args, time.Now(), stdout, stderr)
	case "health":
		fs := flag.NewFlagSet("health", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "")
		if fs.Parse(args) != nil || fs.NArg() > 0 {
			return exitUsage
		}
		return healthCmd(home, time.Now(), *asJSON, stdout, stderr)
	case "digest":
		fs := flag.NewFlagSet("digest", flag.ContinueOnError)
		fs.SetOutput(stderr)
		write := fs.Bool("write", false, "save the report and its numbers under state/digest/")
		printPath := fs.Bool("print-path", false, "with --write: print the saved report's path, not the report")
		if fs.Parse(args) != nil || fs.NArg() > 0 || *printPath && !*write {
			return exitUsage
		}
		return digestCmd(home, time.Now(), *write, *printPath, stdout, stderr)
	case "scorecard":
		fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
		fs.SetOutput(stderr)
		day := fs.String("week", "", "the UTC day the quota week ends")
		write := fs.Bool("write", false, "save the ended week under state/scorecards/")
		if fs.Parse(args) != nil || fs.NArg() > 0 {
			return exitUsage
		}
		return scorecardCmd(home, time.Now(), *day, *write, stdout, stderr)
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
	var role, typ, text, to, title, why, by, supersedes, reason, refs, kind, parent, revision, review *string
	var foundBy, severity, class, defect, corrects, field, value *string
	var taint *bool
	var criteria, checks multi
	required := []string{"project"}
	item := ""
	switch what {
	case "bus":
		role, typ, text = fs.String("role", "", ""), fs.String("type", "", ""), fs.String("text", "", "")
		to = fs.String("to", "crius", "")
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
	case "accept", "defect", "revoke":
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			fmt.Fprintf(stderr, "record %s needs the item first: record %s ITEM --…\n", what, what)
			return exitUsage
		}
		item, args = args[0], args[1:]
		switch what {
		case "accept":
			revision, review, by = fs.String("revision", "", ""), fs.String("review", "", ""), fs.String("by", "", "")
			fs.Var(&checks, "check", "")
			required = []string{"revision", "check", "review", "by"}
		case "defect":
			title, foundBy = fs.String("title", "", ""), fs.String("found-by", "", "")
			severity, class = fs.String("severity", "", ""), fs.String("class", "", "")
			required = []string{"title", "found-by", "severity", "class"}
		case "revoke":
			defect, reason, by = fs.String("defect", "", ""), fs.String("reason", "", ""), fs.String("by", "", "")
			fs.Var(&criteria, "criteria", "")
			required = []string{"defect", "reason", "criteria", "by"}
		}
	case "correct":
		corrects, field, value = fs.String("corrects", "", ""), fs.String("field", "", ""), fs.String("value", "", "")
		reason, by = fs.String("reason", "", ""), fs.String("by", "", "")
		required = []string{"corrects", "field", "value", "reason", "by"}
	default:
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return exitUsage
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if required[0] != "project" && given["project"] {
		fmt.Fprintf(stderr, "record %s takes no --project: the item names it\n", what)
		return exitUsage
	}
	for _, name := range required {
		if !given[name] {
			fmt.Fprintf(stderr, "record %s needs --%s\n", what, name)
			return exitUsage
		}
	}
	// A text that looks like a secret is refused, not rewritten: nothing secret is written to the bus, DECISIONS.md
	// or the store, and prose that only looks like one ("password: reset by mail") is not changed without the caller
	// seeing it (design §3c).
	var secret []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "to" {
			return // a name: the name rule below
		}
		if _, n := redact.Redact(f.Value.String()); n > 0 {
			secret = append(secret, "--"+f.Name)
		}
	})
	if given["to"] {
		known, err := ingest.KnownProjects(home)
		if err != nil {
			fmt.Fprintf(stderr, "unavailable: --to needs the projects in projects.toml: %v\n", err)
			return exitUnavailable
		}
		red := func(s string) string { s, _ = redact.Redact(s); return s }
		if ingest.RedactName(*to, known, red) != *to {
			secret = append(secret, "--to")
		}
	}
	if len(secret) > 0 {
		fmt.Fprintf(stderr, "refused: %s looks like a secret; remove it or rephrase\n", strings.Join(secret, ", "))
		return exitUsage
	}
	if fi, err := os.Stat(filepath.Join(home, "state")); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "unavailable: %s is missing; run crius-rec init\n", home)
		return exitUnavailable
	}

	var out string
	var err error
	switch what {
	case "bus":
		if p, r, pair := strings.Cut(*to, ":"); !store.Slug.MatchString(p) || pair && !store.Slug.MatchString(r) {
			fmt.Fprintf(stderr, "refused: --to must be a project or project:role, got %q\n", *to)
			return exitUsage
		}
		out, err = store.FormatLine(*typ, *project, *role, *text, *to, time.Now())
		if err == nil {
			err = store.AppendLine(home, *project, out)
		}
	case "decision":
		out, err = store.RecordDecision(home, store.DecisionInput{Project: *project, Title: *title, Why: *why,
			DecidedBy: *by, Supersedes: *supersedes, Reason: *reason, Refs: *refs, Taint: *taint})
	case "item":
		out, err = recordItem(home, *project, *title, *kind, criteria, *parent, *taint)
	case "accept":
		out, err = recordAccept(home, store.AcceptInput{Item: item, Revision: *revision, Checks: checks, Review: *review,
			By: *by})
	case "defect":
		out, err = recordWith(home, func(db *sql.DB) (string, error) {
			return store.RecordDefect(db, store.DefectInput{Item: item, Title: *title, FoundBy: *foundBy,
				Severity: *severity, Class: *class})
		})
	case "revoke":
		out, err = recordWith(home, func(db *sql.DB) (string, error) {
			return store.RecordRevoke(db, store.RevokeInput{Item: item, Defect: *defect, Reason: *reason, By: *by,
				Criteria: criteria})
		})
	case "correct":
		out, err = recordWith(home, func(db *sql.DB) (string, error) {
			return store.RecordCorrect(db, store.CorrectInput{Target: *corrects, Field: *field, Value: *value,
				Reason: *reason, By: *by})
		})
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

func recordAccept(home string, in store.AcceptInput) (string, error) {
	unlock, err := store.WriterLock(home, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	db, err := store.Open(home)
	if err != nil {
		return "", err
	}
	out, err := store.RecordAccept(db, in)
	return out, errors.Join(err, db.Close())
}

// recordWith runs one store write under the writer lock.
func recordWith(home string, f func(*sql.DB) (string, error)) (string, error) {
	unlock, err := store.WriterLock(home, true)
	if err != nil {
		return "", err
	}
	defer unlock()
	db, err := store.Open(home)
	if err != nil {
		return "", err
	}
	out, err := f(db)
	return out, errors.Join(err, db.Close())
}

// readEnt reads the Enterprise account with the ~/.claude-work login (a variable so tests never reach the keychain
// or the network).
var readEnt = func(now time.Time) quota.EntUsage {
	u, err := os.UserHomeDir()
	if err != nil {
		return quota.EntUsage{Reason: fmt.Sprintf("home: %v", err)}
	}
	return quota.WithDeadline(func() quota.EntUsage {
		return quota.ReadEntUsage(quota.KeychainLogin(filepath.Join(u, ".claude-work")), quota.UsageGet, now)
	}, quota.EntBudget)
}

// quotaCmd prints the two quota blocks (or the report as JSON), starting with the status and its reasons; anything
// but complete exits partial. The Enterprise account not read makes the report partial.
func quotaCmd(home string, now time.Time, asJSON, room bool, stdout, stderr io.Writer) int {
	ent := readEnt(now)
	// fail says why on stderr; with --room every failure is also an "unknown" verdict on stdout, the one line a
	// launcher reads, with a short reason (the full one is on stderr).
	fail := func(rc int, reason, msg string) int {
		fmt.Fprintln(stderr, msg)
		if room {
			roomLine(stdout, "unknown", reason, nil, ent)
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
	rep.EntUsage = &ent
	if !ent.Read {
		rep.Status = "partial"
		rep.Reasons = append(rep.Reasons, "enterprise fallback: cannot read ("+ent.Reason+")")
	}
	if room {
		v, reason := quota.Room(rep)
		roomLine(stdout, v, reason, rep, ent)
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
		b, err := redact.JSON(rep)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		writeJSON(stdout, b)
	} else {
		fmt.Fprint(stdout, capLines(rep.Text(), maxLines))
	}
	if rep.Status != "complete" {
		return exitPartial
	}
	return 0
}

// roomLine is the --room answer: one JSON line. Launchers read verdict and reason; the numbers are null when there
// is no report. Fallback is the Enterprise account's own verdict (included usage and spend limit null when not read); the exit code stays
// the Max verdict's.
func roomLine(w io.Writer, verdict, reason string, r *quota.Report, m quota.EntUsage) {
	type fallback struct {
		Verdict  string            `json:"verdict"`
		Reason   string            `json:"reason"`
		Included *quota.Included   `json:"included"`
		Spend    *quota.SpendLimit `json:"spend_limit"`
	}
	v := struct {
		Verdict    string   `json:"verdict"`
		Reason     string   `json:"reason"`
		Seven      *float64 `json:"seven"`
		Five       *float64 `json:"five"`
		Reset      string   `json:"reset"`
		SampleAgeS *int64   `json:"sample_age_s"`
		Fallback   fallback `json:"fallback"`
	}{Verdict: verdict, Reason: reason}
	v.Fallback.Verdict, v.Fallback.Reason = m.Room()
	if m.Read {
		v.Fallback.Included, v.Fallback.Spend = &m.Included, &m.Spend
	}
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

// parseAll parses flags given before, between and after the positional words, and returns the words.
func parseAll(fs *flag.FlagSet, args []string) ([]string, error) {
	var words []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return words, nil
		}
		words, args = append(words, fs.Arg(0)), fs.Args()[1:]
	}
}

// parseSince takes YYYY-MM-DD (local midnight), an RFC 3339 time, or N days / hours back from now.
func parseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && strings.HasSuffix(s, "d") && n >= 0 {
		return now.AddDate(0, 0, -n), nil
	}
	if d, err := time.ParseDuration(s); err == nil && strings.HasSuffix(s, "h") && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("--since takes YYYY-MM-DD, an RFC 3339 time, Nd or Nh, not %q", s)
}

// openStore opens the store for reading; a missing home or a store that cannot be opened is unavailable (exit 4).
func openStore(home string, stderr io.Writer) (*sql.DB, int) {
	if fi, err := os.Stat(filepath.Join(home, "state", "record.db")); err != nil || !fi.Mode().IsRegular() {
		fmt.Fprintf(stderr, "unavailable: %s has no record store; run crius-rec init and the ingest\n", home)
		return nil, exitUnavailable
	}
	db, err := store.Open(home)
	if err != nil {
		fmt.Fprintf(stderr, "unavailable: %v\n", err)
		return nil, exitUnavailable
	}
	return db, 0
}

// findCmd searches the store (design §4): exact ids, then full text, or a list with no query.
func findCmd(home string, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project, since := fs.String("project", "", ""), fs.String("since", "", "")
	var kinds multi
	fs.Var(&kinds, "kind", "")
	active, waiting, asJSON := fs.Bool("active", false, ""), fs.Bool("waiting", false, ""), fs.Bool("json", false, "")
	deep := fs.Bool("deep", false, "")
	words, err := parseAll(fs, args)
	if err != nil {
		return exitUsage
	}
	q := recall.Query{Text: strings.Join(words, " "), Project: *project, Active: *active, Waiting: *waiting, Now: now}
	for _, k := range kinds {
		q.Kinds = append(q.Kinds, strings.Split(k, ",")...)
	}
	if q.Since, err = parseSince(*since, now); err != nil {
		fmt.Fprintf(stderr, "refused: %v\n", err)
		return exitUsage
	}
	if q.Text == "" && q.Project == "" && *since == "" && len(q.Kinds) == 0 && !q.Waiting {
		fmt.Fprint(stderr, "find needs a query, or --project, --since, --kind or --waiting\n"+usage)
		return exitUsage
	}
	db, rc := openStore(home, stderr)
	if db == nil {
		return rc
	}
	defer db.Close()
	search := recall.Find
	if *deep {
		search, q.Self = recall.Deep, os.Getenv("CLAUDE_CODE_SESSION_ID")
	}
	res, err := search(db, q, store.Types)
	if err != nil {
		if errors.Is(err, recall.ErrQuery) {
			fmt.Fprintf(stderr, "refused: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	// The commit block ends the text and keeps its lines inside the cap; the hits before it give up the room.
	text := res.Text()
	if block := res.Block(); block != "" {
		text = capLines(strings.TrimSuffix(text, block), maxLines-strings.Count(block, "\n")) + block
	}
	return answer(res.Status, res, text, *asJSON, stdout, stderr)
}

// showCmd prints one thing in full (design §4): item, decision, session, commit, event or project.
func showCmd(home string, args []string, now time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	history, asJSON := fs.Bool("history", false, ""), fs.Bool("json", false, "")
	words, err := parseAll(fs, args)
	if err != nil {
		return exitUsage
	}
	if len(words) != 1 {
		fmt.Fprint(stderr, "show needs one id\n"+usage)
		return exitUsage
	}
	db, rc := openStore(home, stderr)
	if db == nil {
		return rc
	}
	defer db.Close()
	res, err := recall.Show(db, home, words[0], *history, now)
	if err != nil {
		if errors.Is(err, recall.ErrQuery) {
			fmt.Fprintf(stderr, "refused: %v\n", err)
			return exitUsage
		}
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	return answer(res.Status, res, res.Text(), *asJSON, stdout, stderr)
}

// healthCmd reports per source and resource, heartbeat, backup and silent leads (design §4); a problem is partial.
func healthCmd(home string, now time.Time, asJSON bool, stdout, stderr io.Writer) int {
	db, rc := openStore(home, stderr)
	if db == nil {
		return rc
	}
	defer db.Close()
	exp, err := ingest.Expected(home)
	if err != nil {
		fmt.Fprintf(stderr, "error: cannot list what the ingest should read: %v\n", err)
		return exitError
	}
	h, err := recall.CheckHealth(db, home, exp, now)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	return answer(h.Status, h, h.Text(), asJSON, stdout, stderr)
}

// answer prints a recall answer (text capped, or all of it as JSON) and returns the exit code of its status.
// writeJSON prints JSON that redact.JSON already redacted value by value, past Run's raw redacting Writer: a raw
// pattern over encoded text can cut a closing quote (a value ending in "password:").
func writeJSON(stdout io.Writer, b []byte) {
	if w, ok := stdout.(redact.Writer); ok {
		stdout = w.W
	}
	fmt.Fprintf(stdout, "%s\n", b)
}

func answer(status string, v any, text string, asJSON bool, stdout, stderr io.Writer) int {
	if asJSON {
		b, err := redact.JSON(v)
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		writeJSON(stdout, b)
	} else {
		fmt.Fprint(stdout, capLines(text, maxLines))
	}
	switch status {
	case "complete":
		return 0
	case "partial":
		return exitPartial
	case "unavailable":
		return exitUnavailable
	case "expired":
		return exitExpired
	}
	fmt.Fprintf(stderr, "error: unknown status %q\n", status)
	return exitError
}

// digestCmd prints the goal report (step 5), compared with the last saved one. With write it saves the report as
// state/digest/YYYY-MM-DD-HHMM.md, redacted like every sink, and its numbers beside it as .json.
func digestCmd(home string, now time.Time, write, printPath bool, stdout, stderr io.Writer) int {
	if fi, err := os.Stat(filepath.Join(home, "state")); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "unavailable: %s is missing; run crius-rec init\n", home)
		return exitUnavailable
	}
	leads, err := ingest.Leads(home)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	db, err := store.Open(home)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	defer db.Close()
	dir := filepath.Join(home, "state", "digest")
	in := digest.Input{Leads: leads}
	in.Prev, in.Since, err = lastSnapshot(dir)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	projects, err := ingest.QuotaProjects(home)
	if err == nil {
		in.Quota, err = quota.Build(db, projects, now)
	}
	var exp recall.Expected
	if err == nil {
		exp, err = ingest.Expected(home)
	}
	if err == nil {
		in.Health, err = recall.CheckHealth(db, home, exp, now)
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	r, err := digest.Build(db, in, now)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	text, _ := redact.Redact(r.Text)
	if write {
		snap, err := redact.JSON(r.Snap)
		if err == nil {
			err = os.MkdirAll(dir, 0o700)
		}
		base := filepath.Join(dir, now.Format("2006-01-02-1504"))
		if err == nil {
			err = os.WriteFile(base+".md", []byte(text), 0o600)
		}
		if err == nil {
			err = os.WriteFile(base+".json", snap, 0o600)
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
	}
	if printPath {
		fmt.Fprintln(stdout, filepath.Join(dir, now.Format("2006-01-02-1504"))+".md")
	} else {
		fmt.Fprint(stdout, text)
	}
	if r.Status != "complete" {
		return exitPartial
	}
	return 0
}

func scorecardCmd(home string, now time.Time, day string, write bool, stdout, stderr io.Writer) int {
	db, rc := openStore(home, stderr)
	if db == nil {
		return rc
	}
	defer db.Close()
	var w scorecard.Week
	var err error
	if day != "" {
		w, err = scorecard.WeekEnding(db, day)
	} else {
		w, err = scorecard.WeekOf(db, now)
	}
	switch {
	case errors.Is(err, scorecard.ErrNoWeeks):
		fmt.Fprintf(stderr, "unavailable: %v\n", err)
		return exitUnavailable
	case errors.Is(err, scorecard.ErrNoWeek):
		fmt.Fprintln(stderr, err)
		return exitUsage
	case err != nil:
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	c, err := scorecard.Build(db, w, now)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}
	if !write {
		fmt.Fprint(stdout, c.Text)
	} else {
		path, what, err := scorecard.Write(db, filepath.Join(home, "state", "scorecards"), c, now)
		switch {
		case errors.Is(err, scorecard.ErrRunning):
			fmt.Fprintf(stderr, "refused: %v\n", err)
			return exitUsage
		case err != nil:
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitError
		}
		fmt.Fprintf(stdout, "%s %s\n", what, path)
	}
	if c.Numbers.MaxPct < 0 {
		return exitPartial
	}
	return 0
}

var reportJSONRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-\d{4}\.json$`)

// lastSnapshot reads the numbers and time of the newest saved report (YYYY-MM-DD-HHMM.json); no saved report yet is an empty snapshot, any other
// failure an error.
func lastSnapshot(dir string) (digest.Snapshot, time.Time, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return digest.Snapshot{}, time.Time{}, nil
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	var names []string // sorted by name, so by time
	for _, e := range entries {
		if reportJSONRe.MatchString(e.Name()) {
			names = append(names, filepath.Join(dir, e.Name()))
		}
	}
	if len(names) == 0 {
		return digest.Snapshot{}, time.Time{}, nil
	}
	last := names[len(names)-1]
	at, err := time.ParseInLocation("2006-01-02-1504.json", filepath.Base(last), time.Local)
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := os.ReadFile(last)
	if err != nil {
		return nil, time.Time{}, err
	}
	var s digest.Snapshot
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, time.Time{}, fmt.Errorf("%s: %w", last, err)
	}
	return s, at, nil
}
