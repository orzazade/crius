// Command crius-queue keeps the list of what waits on the owner (package ownerqueue): the owner-queue mod draws it
// beside the Crius chat.
//
//	crius-queue init [--since YYYY-MM-DDTHH:MM]   start the queue; older NEED-OWNER lines are history (default: now)
//	crius-queue list [--json]                     what waits, oldest first, and what was answered today
//	crius-queue add --title T --from F [--do D] [--why W] [--unblocks U] [--options "A|B"]   prints the new id
//	crius-queue answer <id|number> [--by who] <text…>   number = place in the list, as /waiting shows it
//
// Every NEED-OWNER bus line is a row without a call; a Crius DECISION/INFO/OWNER/ANSWER line whose text opens with
// "[queue qN]" answers row qN (the tag further on answers nothing). list also reads every lead's
// GOAL block (projects.toml): what its "Needs owner" asks that the bus has not answered, as the goal report lists it,
// is one row per lead, titled with the project's plain name (projects.toml name), with what the ask points to read in
// (package needref). Exit codes: 0 done, 1 failed, 2 bad arguments.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/digest"
	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/needref"
	"github.com/orzazade/crius/internal/ownerqueue"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	err := run(store.Home(), time.Now(), os.Args[1:], os.Stdout)
	var u usage
	switch {
	case errors.As(err, &u):
		fmt.Fprintln(os.Stderr, "crius-queue:", err)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "crius-queue:", err)
		os.Exit(1)
	}
}

type usage string

func (u usage) Error() string { return string(u) }

const help = "usage: crius-queue init [--since T] | list [--json] | add --title T --from F [--do D] [--why W] [--unblocks U] [--options \"A|B\"] | answer <id|number> [--by who] <text…>"

// View is what list --json prints: the mod draws it.
type View struct {
	Open     []Item   `json:"open"`
	Answered []Item   `json:"answered"`
	Now      string   `json:"now"`
	Unread   []string `json:"unread,omitempty"` // leads whose GOAL block cannot be read: what they ask is not here
	Held     []string `json:"held,omitempty"`   // GOAL asks held back: the owner may have answered them already
}

// Item is a row with its age, as the pane shows it.
type Item struct {
	ownerqueue.Row
	Age string `json:"age"`
}

func run(home string, now time.Time, args []string, out io.Writer) error {
	if len(args) == 0 {
		return usage(help)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	switch args[0] {
	case "init":
		since := fs.String("since", now.Format("2006-01-02T15:04"), "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return usage(help)
		}
		if err := ownerqueue.Init(home, *since); err != nil {
			return err
		}
		fmt.Fprintf(out, "owner queue starts at %s\n", *since)
		return nil
	case "list":
		asJSON := fs.Bool("json", false, "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return usage(help)
		}
		q, unread, held, err := list(home, now)
		if err != nil {
			return err
		}
		v := View{Open: items(q.OpenRows(), now), Answered: items(q.AnsweredOn(now), now), Now: now.Format("2006-01-02T15:04"),
			Unread: unread, Held: held}
		if *asJSON {
			return json.NewEncoder(out).Encode(v)
		}
		printList(out, v)
		return nil
	case "add":
		var r ownerqueue.Row
		var options string
		fs.StringVar(&r.Title, "title", "", "")
		fs.StringVar(&r.From, "from", "", "")
		fs.StringVar(&r.Do, "do", "", "")
		fs.StringVar(&r.Why, "why", "", "")
		fs.StringVar(&r.Unblocks, "unblocks", "", "")
		fs.StringVar(&options, "options", "", "")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
			return usage(help)
		}
		for _, o := range strings.Split(options, "|") {
			if o = strings.TrimSpace(o); o != "" {
				r.Options = append(r.Options, o)
			}
		}
		id, err := ownerqueue.Add(home, now, r)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, id)
		return nil
	case "answer":
		by := fs.String("by", "owner", "")
		if len(args) < 3 {
			return usage(help)
		}
		if err := fs.Parse(args[2:]); err != nil || fs.NArg() == 0 {
			return usage(help)
		}
		id := args[1]
		if n, err := strconv.Atoi(id); err == nil {
			// the list last printed: GOAL blocks are not read again here, so a rewritten one moves no number (review
			// round 3 of the queue gap)
			q, err := ownerqueue.List(home, now)
			if err != nil {
				return err
			}
			open := q.OpenRows()
			if n < 1 || n > len(open) {
				return fmt.Errorf("no item %d: %d wait on you", n, len(open))
			}
			id = open[n-1].ID
		}
		text := strings.Join(fs.Args(), " ")
		if err := ownerqueue.Answer(home, now, id, text, *by); err != nil {
			return err
		}
		fmt.Fprintf(out, "[queue %s] %s\n", id, strings.TrimSpace(text))
		return nil
	}
	return usage(help)
}

// list reads every lead's GOAL block into the queue, then the queue. unread names each lead whose block cannot be
// read, held each ask held back (ownerqueue.SyncGoals, and the screen asks wakewatch held on its last run).
func list(home string, now time.Time) (q *ownerqueue.Queue, unread, held []string, err error) {
	leads, err := ingest.Leads(home)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(home)
	if err != nil {
		return nil, nil, nil, err
	}
	defer db.Close()
	byName := map[string]ingest.Lead{}
	for _, l := range leads {
		byName[l.Name] = l
	}
	held, err = ownerqueue.SyncGoals(home, now, func() ([]ownerqueue.GoalNeed, error) {
		needs, err := digest.OwnerNeeds(db, leads)
		if err != nil {
			return nil, err
		}
		goals := make([]ownerqueue.GoalNeed, 0, len(needs))
		for _, n := range needs {
			l := byName[n.Lead]
			g := ownerqueue.GoalNeed{Lead: n.Lead, Text: strings.Join(n.Items, "; "), Stamp: n.Updated.Format("2006-01-02T15:04"),
				Project: l.Project}
			g.Detail = func() string { return readIn(g.Text, l) }
			if n.Err != nil {
				g.Unread = n.Err.Error()
				unread = append(unread, n.Lead+": "+g.Unread)
			}
			goals = append(goals, g)
		}
		return goals, nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	onScreen, err := ownerqueue.ScreenHeld(home)
	if err != nil {
		return nil, nil, nil, err
	}
	q, err = ownerqueue.List(home, now)
	return q, unread, append(held, onScreen...), err
}

// readIn reads in what a lead's ask points to (needref). A git-object status file's labels and the files its ask names
// are read from that revision, as its GOAL block was.
func readIn(ask string, l ingest.Lead) string {
	st := needref.Status{Path: l.StatusFile, Dir: filepath.Dir(l.StatusFile)}
	if l.Repo != "" {
		rev, file, _ := strings.Cut(l.StatusFile, ":")
		b, err := digest.GitStatus(l)
		if err != nil {
			return "Could not read " + l.StatusFile + ": " + err.Error()
		}
		st = needref.Status{Path: l.Repo + " " + l.StatusFile, Text: b, Git: &needref.Tree{Repo: l.Repo, Rev: rev, Dir: path.Dir(file)}}
	}
	return needref.Read(ask, st, slices.Concat(l.Dirs, []string{l.Repo}))
}

func items(rows []ownerqueue.Row, now time.Time) []Item {
	out := make([]Item, 0, len(rows))
	for _, r := range rows {
		out = append(out, Item{Row: r, Age: age(r.Opened, now)})
	}
	return out
}

// age is how long a row has waited: 3m, 4h, 2d; "?" for an undated one.
func age(opened string, now time.Time) string {
	t, err := time.ParseInLocation("2006-01-02T15:04", opened, time.Local)
	if err != nil {
		return "?"
	}
	d := now.Sub(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(0, int(d.Minutes())))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// shortAt is a 2006-01-02T15:04 time as the list shows it: 15:04 on now's day, else 01-02 15:04.
func shortAt(at, now string) string {
	if len(at) < 16 || len(now) < 10 {
		return at
	}
	if at[:10] == now[:10] {
		return at[11:]
	}
	return at[5:10] + " " + at[11:]
}

func printList(out io.Writer, v View) {
	if len(v.Open) == 0 {
		fmt.Fprintln(out, "✓ Nothing waits on you.")
	} else {
		fmt.Fprintf(out, "Waiting on you (%d), oldest first\n", len(v.Open))
		for i, r := range v.Open {
			fmt.Fprintf(out, "%d %s · %s · %s · %s\n", i+1, r.Title, r.From, r.Age, r.ID)
			for _, p := range [][2]string{{"Do", r.Do}, {"Why you", r.Why}, {"Unblocks", r.Unblocks}} {
				if p[1] != "" && p[1] != r.Title {
					fmt.Fprintf(out, "    %-9s %s\n", p[0], p[1])
				}
			}
			if len(r.Options) > 0 {
				fmt.Fprintf(out, "    %-9s %s\n", "Options", strings.Join(r.Options, " | "))
			}
			if r.Detail != "" {
				fmt.Fprintf(out, "    %-9s %s\n", "Details", strings.ReplaceAll(r.Detail, "\n", "\n              "))
			}
			if r.Asked != "" {
				fmt.Fprintf(out, "    %-9s %s (%s)\n", "You asked", r.Asked, shortAt(r.AskedAt, v.Now))
			}
		}
		fmt.Fprintln(out, `Answer by number: "2 yes", or any text: "2 not before Monday".`)
	}
	if len(v.Unread) > 0 {
		fmt.Fprintf(out, "GOAL block cannot be read, so what these leads ask is not listed (%d):\n", len(v.Unread))
		for _, u := range v.Unread {
			fmt.Fprintln(out, "  ! "+u)
		}
	}
	if len(v.Held) > 0 {
		fmt.Fprintf(out, "Held, as you may have answered them already (%d):\n", len(v.Held))
		for _, h := range v.Held {
			fmt.Fprintln(out, "  · "+h)
		}
	}
	if len(v.Answered) > 0 {
		fmt.Fprintln(out, "Answered today")
		for _, r := range v.Answered {
			fmt.Fprintf(out, "  ✓ %s · %s\n", r.Title, r.Answer)
		}
	}
}
