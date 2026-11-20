package wakewatch

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// orderAfter is how long a role may stay silent after Crius told it to act. Replayed every 10 min over the watched
// buses 11-14..11-21 this gives 1 wake: 13 Crius lines to quotes:lead on 11-18, a role that no longer posted. (A
// rougher count in the first commit said 66 of 1518 lines; it was wrong, review of ef15655.)
const orderAfter = 2 * time.Hour

// actTypes are the lines from Crius that a role must act on.
var actTypes = map[string]bool{"DECISION": true, "ORDER": true, "GO": true}

// openOrders gives [key, text] for each DECISION, ORDER or GO from crius:lead to a role of the bus's own team that the
// role has posted nothing after, orderAfter to window old. A decision is an open item until its role shows it saw it,
// as OpenRig keeps a queue item open until its seat closes it and its stuck sweep wakes the owner (OpenRig trial test 6,
// ~/Projects/yolo/openrig-trial/RESULTS.md). The other way round, a line to Crius nobody answered, is missed().
// Any line from the role counts as seen: whether it applied the decision stays Crius's check. A whole team ("all"),
// a copy ("cc …"), another team's role, a parked role or project, and a name that never posted on this bus (a group
// such as "builders", a program, a typo) owe nothing.
func openOrders(home string, now time.Time, watch projects, park parked) ([][2]string, error) {
	logs, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(logs)
	var out [][2]string
	for _, log := range logs {
		team := strings.ToLower(strings.TrimSuffix(filepath.Base(log), ".log"))
		if !watch.has(team) || park[team] {
			continue
		}
		b, err := os.ReadFile(log)
		if err != nil {
			return nil, err
		}
		text := string(b[:bytes.LastIndexByte(b, '\n')+1]) // whole lines only
		type order struct {
			line, role string
			at         time.Time
		}
		var open []order
		spoke := map[string]bool{}
		for _, line := range strings.Split(text, "\n") {
			m := lineRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			typ, from, to := m[2], strings.ToLower(m[3]), m[4]
			spoke[from] = true
			open = slices.DeleteFunc(open, func(o order) bool { return o.role == from })
			if from != "crius:lead" || !actTypes[typ] {
				continue
			}
			at, err := lineTime(m[1], now)
			if err != nil {
				continue
			}
			main, _, _ := strings.Cut(to, "(")
			for _, r := range recipients(main) {
				r = strings.ToLower(r)
				if r == "cc" {
					break
				}
				if !strings.Contains(r, ":") {
					r = team + ":" + r
				}
				p, role, _ := strings.Cut(r, ":")
				if p != team || role == "all" || park.has(r, team) {
					continue
				}
				open = append(open, order{line, r, at})
			}
		}
		// One wake per role and silence, for its oldest open line: a role that missed one line most likely missed the
		// next too. Once that oldest line is past the window the silence is history, and no later line wakes Crius anew
		// for it (review P2-3).
		later := map[string]int{}
		var first []order
		for _, o := range open {
			if later[o.role]++; later[o.role] == 1 {
				first = append(first, o)
			}
		}
		for _, o := range first {
			age := now.Sub(o.at)
			if age < orderAfter || age > window || !spoke[o.role] {
				continue
			}
			more := ""
			if n := later[o.role] - 1; n > 0 {
				more = fmt.Sprintf(" (and %d later line(s) to it)", n)
			}
			out = append(out, [2]string{"order:" + hash(log+"\n"+o.line+"\n"+o.role),
				fmt.Sprintf("%s: %s has posted nothing since this line %d h ago%s; did it see it? %s",
					filepath.Base(log), o.role, int(age.Hours()), more, o.line)})
		}
	}
	return out, nil
}
