// Command playbook keeps the teams' playbook: each night it reads what happened
// (bus logs, transcripts) since the last run, picks findings with cheap rules,
// has claude -p (Max login) write them up, and saves them to the playbook skill.
//
//	playbook run [-since 2026-11-01] [-dry] [-candidates out.json]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
	case "seed":
		// Old lesson files go through the same pick and write as a night's record.
		err = run(append([]string{"-seed"}, os.Args[2:]...))
	case "show":
		show(os.Args[2:])
		return
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "playbook:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: playbook run [-since YYYY-MM-DD] [-dry] [-candidates file.json] | playbook show [-cwd dir] (SessionStart hook: reads the hook JSON on stdin)")
	os.Exit(2)
}

func run(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fl := flag.NewFlagSet("run", flag.ExitOnError)
	since := fl.String("since", "", "read nothing older than this day (needed on the first run)")
	dry := fl.Bool("dry", false, "pick candidates and print counts; no model call, no write, cursor kept")
	candOut := fl.String("candidates", "", "write the picked candidates to this JSON file")
	book := fl.String("book", filepath.Join(home, ".claude", "skills", "playbook"), "the playbook skill folder")
	state := fl.String("state", filepath.Join(home, ".crius", "state", "playbook", "run"), "cursor folder")
	bus := fl.String("bus", filepath.Join(home, ".crius", "bus"), "bus log folders, comma separated")
	tx := fl.String("tx", "", "transcript roots, comma separated (default ~/.claude*/projects)")
	post := fl.String("post", "", "after a real run, post the report line to this bus log as playbook:watch -> crius:lead")
	seed := fl.Bool("seed", false, "seed from the lesson files named after the flags, not from the bus and transcripts")
	_ = fl.Parse(args)

	cfg := playbook.Config{BusDirs: split(*bus), BookDir: *book, StateDir: *state, DryRun: *dry}
	if *seed {
		if fl.NArg() == 0 {
			return errors.New("seed: name the lesson files")
		}
		cfg = playbook.Config{DocFiles: fl.Args(), BookDir: *book, StateDir: *state, DryRun: *dry}
		return finish(cfg, home, *state, *candOut, "", *dry)
	}
	if *tx != "" {
		cfg.TxRoots = split(*tx)
	} else {
		roots, _ := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
		if len(roots) == 0 {
			return alert(home, *post, fmt.Errorf("no transcript roots: nothing matches %s", filepath.Join(home, ".claude*", "projects")))
		}
		cfg.TxRoots = roots
	}
	if *since != "" {
		t, err := time.ParseInLocation("2006-01-02", *since, time.Local)
		if err != nil {
			return fmt.Errorf("-since: %w", err)
		}
		cfg.Since = t
	} else if ok, err := playbook.Started(*state); err != nil || !ok {
		return alert(home, *post, fmt.Errorf("no read position in %s yet (%v): the first run needs -since", *state, err))
	}
	po, err := projectOf()
	if err != nil {
		return alert(home, *post, err)
	}
	cfg.ProjectOf = po
	return finish(cfg, home, *state, *candOut, *post, *dry)
}

// finish runs, prints the report, and writes or posts its line. A run that
// failed or is not healthy (read errors, failed batches, lost candidates)
// posts an ALERT and exits non-zero, so it never looks like a quiet night.
func finish(cfg playbook.Config, home, state, candOut, post string, dry bool) error {
	start := time.Now()
	res, err := playbook.Run(context.Background(), cfg, playbook.ClaudeCLI{})
	if res == nil {
		res = &playbook.Result{}
	}
	line := report(res, time.Since(start), err)
	if err == nil && !dry && !res.Healthy() {
		err = errors.New("unhealthy run: " + health(res))
	}
	if !dry {
		if werr := os.MkdirAll(state, 0o755); werr != nil && err == nil {
			err = werr
		}
		if werr := os.WriteFile(filepath.Join(state, "last-report.txt"), []byte(line+"\n"), 0o644); werr != nil && err == nil {
			err = werr
		}
		if post != "" {
			typ := "INFO"
			if err != nil {
				typ = "ALERT"
			}
			out, perr := exec.Command(filepath.Join(home, ".crius", "bin", "bus-post"), post, typ, "playbook:watch", "crius:lead", cut(line, 300)).CombinedOutput()
			if perr != nil && err == nil {
				err = fmt.Errorf("bus-post: %v: %s", perr, out)
			}
		}
	}
	if candOut != "" {
		data, _ := json.MarshalIndent(res.Candidates, "", " ")
		if werr := os.WriteFile(candOut, data, 0o644); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// health names what made a run unhealthy, or "ok".
func health(res *playbook.Result) string {
	var p []string
	if n := len(res.Read.Errors); n > 0 {
		p = append(p, fmt.Sprintf("%d read errors", n))
	}
	if n := len(res.Failed); n > 0 {
		p = append(p, fmt.Sprintf("%d failed model batches (parked)", n))
	}
	if n := len(res.Dropped); n > 0 {
		p = append(p, fmt.Sprintf("%d answers dropped", n))
	}
	if res.Lost > 0 {
		p = append(p, fmt.Sprintf("%d candidates lost to dropped.jsonl", res.Lost))
	}
	if res.Read.ShownBad > 0 {
		p = append(p, fmt.Sprintf("%d bad shown.log lines", res.Read.ShownBad))
	}
	if len(p) == 0 {
		return "ok"
	}
	return strings.Join(p, ", ")
}

// report prints the run and returns its one line for Crius's 09:00/18:00 report.
func report(res *playbook.Result, took time.Duration, runErr error) string {
	why := map[string]int{}
	for _, c := range res.Candidates {
		why[c.Source+"/"+c.Why]++
	}
	fmt.Printf("read: %s\n", res.Read)
	for _, e := range res.Read.Errors {
		fmt.Printf("  read error: %s\n", e)
	}
	fmt.Printf("candidates: %d %v\n", len(res.Candidates), why)
	fmt.Printf("kept: %d; new entries: %d; evidence added: %d; same line again: %d; left out by the writer: %d\n",
		res.Kept, res.New, res.Evidence, res.Duplicates, res.Unwritten)
	for _, f := range res.Failed {
		fmt.Printf("  failed: %s\n", f)
	}
	for _, d := range res.Dropped {
		fmt.Printf("  dropped: %s\n", d)
	}
	fmt.Printf("model calls: pick (haiku) %d, write (sonnet) %d; took %s\n", res.Calls.Pick, res.Calls.Write, took.Round(time.Second))
	r := res.Review
	for _, u := range r.Uses {
		if u.Shown > 0 {
			fmt.Printf("  use %s: shown %d, followed %d %v, repeats %d\n", u.ID, u.Shown, u.Followed, u.Teams, u.Repeats)
		}
	}
	line := fmt.Sprintf("playbook %s: +%d entries, +%d evidence, %d live; promote %s; needs a check %s; archived %d; calls haiku %d sonnet %d; health %s",
		time.Now().Format("2006-01-02"), res.New, res.Evidence, len(r.Uses), list(r.Promote), list(r.NeedsCheck), len(r.Archived),
		res.Calls.Pick, res.Calls.Write, health(res))
	if runErr != nil {
		line += "; FAILED: " + runErr.Error()
	}
	fmt.Println(line)
	return line
}
func list(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

func cut(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// show is the SessionStart hook: it prints the entries that fit the session's
// folder, or nothing. It never fails the session: every error is one stderr line
// and exit 0.
func show(args []string) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "playbook show:", err)
		return
	}
	fl := flag.NewFlagSet("show", flag.ContinueOnError)
	cwd := fl.String("cwd", "", "folder to match (default: the hook input's cwd)")
	book := fl.String("book", filepath.Join(home, ".claude", "skills", "playbook"), "the playbook skill folder")
	state := fl.String("state", filepath.Join(home, ".crius", "state", "playbook", "run"), "where shown.log goes")
	if fl.Parse(args) != nil {
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
	}
	if *cwd == "" {
		if fi, _ := os.Stdin.Stat(); fi != nil && fi.Mode()&os.ModeCharDevice == 0 {
			_ = json.NewDecoder(os.Stdin).Decode(&in)
		}
		*cwd = in.Cwd
	}
	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}
	b, err := playbook.LoadBook(*book)
	if err != nil {
		fmt.Fprintln(os.Stderr, "playbook show:", err)
		return
	}
	project := ""
	if po, err := projectOf(); err != nil {
		fmt.Fprintln(os.Stderr, "playbook show:", err) // still show the key matches
	} else {
		project = po(*cwd)
	}
	es := b.Match(*cwd, project)
	fmt.Print(playbook.ShowText(es, *book))
	if err := playbook.LogShown(*state, in.SessionID, *cwd, es, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "playbook show:", err)
	}
}

// alert posts a run that could not start as an ALERT, then returns its error.
func alert(home, post string, err error) error {
	if post == "" {
		return err
	}
	line := cut("playbook "+time.Now().Format("2006-01-02")+": FAILED before reading: "+err.Error(), 300)
	if out, perr := exec.Command(filepath.Join(home, ".crius", "bin", "bus-post"), post, "ALERT", "playbook:watch", "crius:lead", line).CombinedOutput(); perr != nil {
		return fmt.Errorf("%w; and bus-post failed: %v: %s", err, perr, out)
	}
	return err
}

// projectOf maps a session folder to its projects.toml project, the name its
// bus log has. It reads projects.toml once and fails loudly when it cannot.
func projectOf() (func(cwd string) string, error) {
	h := store.Home()
	if _, _, err := ingest.Filing(h, "", ""); err != nil {
		return nil, fmt.Errorf("projects.toml: %w", err)
	}
	seen := map[string]string{}
	return func(cwd string) string {
		if p, ok := seen[cwd]; ok {
			return p
		}
		p, _, _ := ingest.Filing(h, cwd, "") // the file read fine above
		seen[cwd] = p
		return p
	}, nil
}
