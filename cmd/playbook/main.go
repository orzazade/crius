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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/playbook"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "run":
		err = run(os.Args[2:])
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
	_ = fl.Parse(args)

	cfg := playbook.Config{BusDirs: split(*bus), BookDir: *book, StateDir: *state, DryRun: *dry}
	if *tx != "" {
		cfg.TxRoots = split(*tx)
	} else {
		roots, _ := filepath.Glob(filepath.Join(home, ".claude*", "projects"))
		cfg.TxRoots = roots
	}
	if *since != "" {
		t, err := time.ParseInLocation("2006-01-02", *since, time.Local)
		if err != nil {
			return fmt.Errorf("-since: %w", err)
		}
		cfg.Since = t
	} else if _, err := os.Stat(filepath.Join(*state, "cursor.json")); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no cursor in %s yet: the first run needs -since", *state)
	}
	start := time.Now()
	res, err := playbook.Run(context.Background(), cfg, playbook.ClaudeCLI{})
	if res != nil {
		report(res, time.Since(start))
		if *candOut != "" {
			data, _ := json.MarshalIndent(res.Candidates, "", " ")
			if werr := os.WriteFile(*candOut, data, 0o644); werr != nil && err == nil {
				err = werr
			}
		}
	}
	return err
}

func report(res *playbook.Result, took time.Duration) {
	why := map[string]int{}
	for _, c := range res.Candidates {
		why[c.Source+"/"+c.Why]++
	}
	fmt.Printf("read: %s\n", res.Read)
	for _, e := range res.Read.Errors {
		fmt.Printf("  read error: %s\n", e)
	}
	fmt.Printf("candidates: %d %v\n", len(res.Candidates), why)
	fmt.Printf("kept: %d; new entries: %d; evidence added: %d; dropped answers: %d\n",
		res.Kept, res.New, res.Evidence, len(res.Dropped))
	for _, d := range res.Dropped {
		fmt.Printf("  dropped: %s\n", d)
	}
	fmt.Printf("model calls: pick (haiku) %d, write (sonnet) %d; took %s\n", res.Calls.Pick, res.Calls.Write, took.Round(time.Second))
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
	es := b.Match(*cwd)
	fmt.Print(playbook.ShowText(es, *book))
	if err := playbook.LogShown(*state, in.SessionID, *cwd, es, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "playbook show:", err)
	}
}
