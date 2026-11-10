// Command crius-board reads and changes a project's work board (package board): one row per acceptance criterion,
// in <home>/state/<project>/BOARD.toml, each change one line in BOARD.events.log.
//
//	crius-board <project> list [--state S]
//	crius-board <project> next                       the id of the next ready row free of others' files; exit 3 if none
//	crius-board <project> add <id> --text T [--files g1,g2] [--mockup M] [--story S] [--check C]
//	crius-board <project> set <id> <state> [--by who] [--why text] [--session s] [--pr n] [--question q] [--check C]
//
// States: open, check-written, ready, building, pr, merged, proven, blocked (needs --question; goes back only to the
// state it came from). Exit codes: 0 done, 1 failed (a refused move says why), 2 bad arguments, 3 nothing ready.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/board"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	err := run(store.Home(), time.Now(), os.Args[1:], os.Stdout)
	var u usage
	switch {
	case errors.Is(err, errNone):
		os.Exit(3)
	case errors.As(err, &u):
		fmt.Fprintln(os.Stderr, "crius-board:", err)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "crius-board:", err)
		os.Exit(1)
	}
}

type usage string

func (u usage) Error() string { return string(u) }

var errNone = errors.New("no ready row is free")

var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func run(home string, now time.Time, args []string, out io.Writer) error {
	if len(args) < 2 {
		return usage("usage: crius-board <project> list|next|add|set …")
	}
	project, cmd, rest := args[0], args[1], args[2:]
	if !projectRe.MatchString(project) {
		return usage(fmt.Sprintf("project %q is not one plain word", project))
	}
	path := filepath.Join(home, "state", project, "BOARD.toml")
	fs := flag.NewFlagSet("crius-board "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		state    = fs.String("state", "", "")
		text     = fs.String("text", "", "")
		files    = fs.String("files", "", "")
		mockup   = fs.String("mockup", "", "")
		story    = fs.String("story", "", "")
		check    = fs.String("check", "", "")
		by       = fs.String("by", os.Getenv("CRIUS_ROLE"), "")
		why      = fs.String("why", "", "")
		session  = fs.String("session", "", "")
		pr       = fs.Int("pr", 0, "")
		question = fs.String("question", "", "")
	)
	// positional words first, then flags
	var pos []string
	for len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		pos, rest = append(pos, rest[0]), rest[1:]
	}
	if err := fs.Parse(rest); err != nil {
		return usage(err.Error())
	}
	if fs.NArg() > 0 {
		return usage(fmt.Sprintf("unexpected %q", fs.Args()))
	}
	if *by == "" {
		*by = "crius-board"
	}
	switch {
	case cmd == "list" && len(pos) == 0:
		b, err := board.Load(path)
		if err != nil {
			return err
		}
		for _, r := range b.Rows {
			if *state == "" || r.State == *state {
				fmt.Fprintln(out, line(r))
			}
		}
		return nil
	case cmd == "next" && len(pos) == 0:
		b, err := board.Load(path)
		if err != nil {
			return err
		}
		r, ok := b.Next()
		if !ok {
			return errNone
		}
		fmt.Fprintln(out, r.ID)
		return nil
	case cmd == "add" && len(pos) == 1 && *text != "":
		r := board.Row{ID: pos[0], Text: *text, Mockup: *mockup, Story: *story, Check: *check}
		for _, f := range splitFiles(*files) {
			if f = strings.TrimSpace(f); f != "" {
				r.Files = append(r.Files, f)
			}
		}
		return board.Update(path, now, func(b *board.Board) error { return b.Add(r, now) })
	case cmd == "set" && len(pos) == 2:
		if *pr < 0 {
			return usage(fmt.Sprintf("--pr %d is not a PR number", *pr))
		}
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project %s has no board (add a row first)", project)
		}
		set := func(r *board.Row) {
			if *session != "" {
				r.Session = *session
			}
			if *pr != 0 {
				r.PR = *pr
			}
			if *question != "" {
				r.Question = *question
			}
			if *check != "" {
				r.Check = *check
			}
		}
		return board.Update(path, now, func(b *board.Board) error { return b.Move(pos[0], pos[1], *by, *why, now, set) })
	}
	return usage(fmt.Sprintf("crius-board %s %s: wrong words (see the usage at the top of cmd/crius-board/main.go)", project, cmd))
}

// line is one row as list prints it: id, state, session, PR, the question while blocked, then the text.
func line(r board.Row) string {
	parts := []string{r.ID, r.State}
	if r.Session != "" {
		parts = append(parts, r.Session)
	}
	if r.PR != 0 {
		parts = append(parts, fmt.Sprintf("PR %d", r.PR))
	}
	if r.Question != "" {
		parts = append(parts, "waits on: "+r.Question)
	}
	return strings.Join(parts, " ") + " · " + r.Text
}

// splitFiles splits --files on commas outside braces, so src/*.{ts,tsx} stays one glob.
func splitFiles(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) {
			switch s[i] {
			case '{':
				depth++
				continue
			case '}':
				depth--
				continue
			case ',':
				if depth > 0 {
					continue
				}
			default:
				continue
			}
		}
		if f := strings.TrimSpace(s[start:i]); f != "" {
			out = append(out, f)
		}
		start = i + 1
	}
	return out
}
