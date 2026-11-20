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

// orderAfter is how long a role may stay silent after Crius told it to act. Measured on the buses 11-10..11-21: of
// 1518 DECISION/ORDER/GO lines from crius:lead to a team role, 129 had no line from that role within 1 h, 66 within
// 2 h, 36 within 4 h; 2 h keeps the wakes near 6 a day.
const orderAfter = 2 * time.Hour

// actTypes are the lines from Crius that a role must act on.
var actTypes = map[string]bool{"DECISION": true, "ORDER": true, "GO": true}

// openOrders gives [key, text] for each DECISION, ORDER or GO from crius:lead to a role of the bus's own team that the
// role has posted nothing after, orderAfter to window old. A decision is an open item until its role shows it saw it,
// as OpenRig keeps a queue item open until its seat closes it and its stuck sweep wakes the owner (OpenRig trial test 6,
// ~/Projects/yolo/openrig-trial/RESULTS.md). The other way round, a line to Crius nobody answered, is missed().
// Any line from the role counts as seen: whether it applied the decision stays Crius's check. A whole team ("all"),
// a copy ("cc …"), another team's role and a parked role or project owe nothing.
func openOrders(home string, now time.Time, watch projects, park parked) ([][2]string, error) {
	logs, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(logs)
	var out [][2]string
	for _, log := range logs {
		team := strings.TrimSuffix(filepath.Base(log), ".log")
		if !watch.has(team) || park[strings.ToLower(team)] {
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
		for _, line := range strings.Split(text, "\n") {
			m := lineRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			typ, from, to := m[2], strings.ToLower(m[3]), m[4]
			open = slices.DeleteFunc(open, func(o order) bool { return o.role == from })
			if from != "crius:lead" || !actTypes[typ] {
				continue
			}
			at, err := lineTime(m[1], now)
			if err != nil || now.Sub(at) > window {
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
		// One wake per role, for its oldest open line: a role that missed one line most likely missed the next too.
		later := map[string]int{}
		var first []order
		for _, o := range open {
			if now.Sub(o.at) < orderAfter {
				continue
			}
			if later[o.role]++; later[o.role] == 1 {
				first = append(first, o)
			}
		}
		for _, o := range first {
			more := ""
			if n := later[o.role] - 1; n > 0 {
				more = fmt.Sprintf(" (and %d later line(s) to it)", n)
			}
			out = append(out, [2]string{"order:" + hash(log+"\n"+o.line+"\n"+o.role),
				fmt.Sprintf("%s: %s has posted nothing since this line %d h ago%s; did it see it? %s",
					filepath.Base(log), o.role, int(now.Sub(o.at).Hours()), more, o.line)})
		}
	}
	return out, nil
}
