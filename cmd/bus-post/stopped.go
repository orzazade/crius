package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
)

// silentFor is how long a role may post nothing before a line to it is said to go to a role that may be stopped.
const silentFor = 24 * time.Hour

// stoppedText is a line whose text opens with STOPPED ("STOPPED.", "STOPPED: …"), as roles wrote their stop on 11-11.
var stoppedText = regexp.MustCompile(`^STOPPED\b`)

// lastSeen is a role's last line on the buses read.
type lastSeen struct {
	at      time.Time
	stamp   string // as written, local: 2026-11-11T01:25
	stopped bool
}

// warnStopped says on stderr each role in to that looks stopped: its last line is a STOPPED (the type, or a text that
// opens with STOPPED), or it has posted no line for over silentFor. 11-21: three DECISIONs went to quotes:lead, stopped
// since 11-11, and nobody acted on them for 8 h. The line is posted already and the exit code stays as it is: a caller
// that reads a non-zero code as "not posted" would send it again.
//
// A role's lines are read from this bus and from its own project's bus next to it (quotes:lead posts on quotes.log).
// crius:lead reads every bus and posts on few, so its silence is no stop; a role with no line on either bus is a new
// one, not a stopped one. A bus that cannot be read is said too: "found nothing" is not "could not look".
func warnStopped(log, to string, now time.Time, stderr io.Writer) {
	project := logProject(log)
	var roles []string
	for _, n := range (&store.Line{To: to}).Recipients() {
		if r := busRole(n, project); r != "" && !slices.Contains(roles, r) {
			roles = append(roles, r)
		}
	}
	files := []string{log}
	for _, r := range roles {
		p, _, _ := strings.Cut(r, ":")
		if f := filepath.Join(filepath.Dir(log), p+".log"); !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	seen := map[string]lastSeen{}
	for i, f := range files {
		err := lastLines(f, roles, now.Location(), seen)
		if errors.Is(err, os.ErrNotExist) && i > 0 { // a role of a project with no bus of its own here
			continue
		}
		if err != nil {
			fmt.Fprintf(stderr, "bus-post: posted, but could not check whether %s is stopped: %v\n", strings.Join(roles, ", "), err)
		}
	}
	for _, r := range roles {
		l, ok := seen[r]
		switch {
		case !ok:
		case l.stopped:
			fmt.Fprintf(stderr, "bus-post: warning: posted, but %s is STOPPED (its last line, %s, is a STOPPED): nobody reads "+
				"this until it starts again; start it, or send it to someone running\n", r, l.stamp)
		case r != "crius:lead" && now.Sub(l.at) > silentFor:
			fmt.Fprintf(stderr, "bus-post: warning: posted, but %s has posted no line for %d h (its last line is %s): it may "+
				"be stopped; check that it runs, or send it to someone running\n", r, int(now.Sub(l.at).Hours()), l.stamp)
		}
	}
}

// lastLines reads the bus log f and keeps in seen the latest line of each role in roles: a later line on another bus
// wins. A bare sender is a role on f's own project.
func lastLines(f string, roles []string, loc *time.Location, seen map[string]lastSeen) error {
	fh, err := os.Open(f)
	if err != nil {
		return err
	}
	defer fh.Close()
	project := logProject(f)
	var st store.State
	rd := bufio.NewReader(fh)
	for {
		text, rerr := rd.ReadString('\n')
		if l := store.ParseLine(text, &st, false); l != nil {
			who := strings.ToLower(l.Role)
			if l.Project != "" {
				who = l.Project + ":" + who
			} else {
				who = project + ":" + who
			}
			if slices.Contains(roles, who) {
				at, perr := l.At, error(nil)
				if at.IsZero() {
					at, perr = time.ParseInLocation("2006-01-02T15:04", l.Date+"T"+l.Time, loc)
				}
				if perr == nil && !at.Before(seen[who].at) {
					seen[who] = lastSeen{at: at, stamp: l.Date + "T" + l.Time,
						stopped: l.Type == "STOPPED" || stoppedText.MatchString(l.Text)}
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return fmt.Errorf("%s: %w", f, rerr)
		}
	}
}
