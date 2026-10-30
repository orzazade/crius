// Command crius-queue keeps the list of what waits on the owner (package ownerqueue): the owner-queue mod draws it
// beside the Crius chat.
//
//	crius-queue init [--since YYYY-MM-DDTHH:MM]   start the queue; older NEED-OWNER lines are history (default: now)
//	crius-queue list [--json]                     what waits, oldest first, and what was answered today
//	crius-queue add --title T --from F [--do D] [--why W] [--unblocks U] [--options "A|B"]   prints the new id
//	crius-queue answer <id|number> [--by who] <text…>   number = place in the list, as /waiting shows it
//
// Every NEED-OWNER bus line is a row without a call; a Crius DECISION/INFO/OWNER/ANSWER line whose text opens with
// "[queue qN]" answers row qN (the tag further on answers nothing). Exit codes: 0 done, 1 failed, 2 bad arguments.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
	Open     []Item `json:"open"`
	Answered []Item `json:"answered"`
	Now      string `json:"now"`
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
		q, err := ownerqueue.List(home, now)
		if err != nil {
			return err
		}
		v := View{Open: items(q.OpenRows(), now), Answered: items(q.AnsweredOn(now), now), Now: now.Format("2006-01-02T15:04")}
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
		}
		fmt.Fprintln(out, `Answer by number: "2 yes", or any text: "2 not before Monday".`)
	}
	if len(v.Answered) > 0 {
		fmt.Fprintln(out, "Answered today")
		for _, r := range v.Answered {
			fmt.Fprintf(out, "  ✓ %s · %s\n", r.Title, r.Answer)
		}
	}
}
