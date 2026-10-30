// Package decisions reviews how the owner decides (owner 10-30: "each of my decisions should be logged, for later how
// I decide and how to reduce my involvement"): the questions put to the owner through the owner queue (owner_decision,
// copied by the ingest), how long they waited, who asks, and the kinds of question answered the same way again and
// again, which Crius can ask the owner to make a standing rule once instead of asking each time.
package decisions

import (
	"database/sql"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
)

// A kind of question with at least repeatMin answers, repeatShare of them the same, is a standing-rule candidate.
const (
	repeatMin   = 3
	repeatShare = 0.8
)

type row struct {
	id, by, title, answer, state string
	asked, answered              time.Time // answered: zero when the answer had no stamp
}

// Review gives the review of the questions asked in [since, now) as text.
func Review(db *sql.DB, since, now time.Time) (string, error) {
	out := []string{fmt.Sprintf("# How the owner decided, %s to %s", since.Local().Format("2006-01-02 15:04"),
		now.Local().Format("2006-01-02 15:04"))}
	var read int
	if err := db.QueryRow("select count(*) from coverage where source = 'owner-queue'").Scan(&read); err != nil {
		return "", err
	}
	if read == 0 {
		return strings.Join(append(out, "NOT READ: the store never read the owner queue (~/.crius/state/owner-queue.jsonl;"+
			" run crius-queue init, then the ingest copies it)"), "\n") + "\n", nil
	}
	// the row's state says open or answered; the stamps only time it. A row with no date (an undated bus line) has no
	// week and is listed apart, never dropped. A row asked before the week and answered in it is in this review: the
	// long waits are the ones that cross a weekly run
	from, to := since.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)
	// A withdrawn row (a GOAL ask its lead stopped asking) is no owner decision and is left out.
	rs, err := query(db, "state != 'withdrawn' and ((asked >= ? and asked < ?) or (answered_at >= ? and answered_at < ?))"+
		" order by asked, id",
		from, to, from, to)
	if err != nil {
		return "", err
	}
	var all []row // asked in the week
	before := 0
	for _, r := range rs {
		if r.asked.Before(since) {
			before++
		} else {
			all = append(all, r)
		}
	}
	undated, err := query(db, "state != 'withdrawn' and coalesce(asked, '') = '' order by id")
	if err != nil {
		return "", err
	}
	var answered, open, timed []row
	for _, r := range rs {
		if r.state == "open" {
			open = append(open, r)
			continue
		}
		answered = append(answered, r)
		if !r.answered.IsZero() {
			timed = append(timed, r)
		}
	}
	var outside int // OWNER lines but those that relay a queue answer ("[queue qN] …"), which the queue counts
	if err := db.QueryRow("select count(*) from event where kind = 'owner' and ts >= ? and ts < ? and text not like '[queue q%'",
		since.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339)).Scan(&outside); err != nil {
		return "", err
	}

	head := fmt.Sprintf("Asked %d · answered %d", len(all), len(answered))
	if before > 0 {
		head += fmt.Sprintf(" (%d asked before the week)", before)
	}
	head += fmt.Sprintf(" · still open %d", len(open))
	if len(timed) > 0 {
		waits := make([]time.Duration, len(timed))
		longest := timed[0]
		for i, r := range timed {
			waits[i] = r.answered.Sub(r.asked)
			if waits[i] > longest.answered.Sub(longest.asked) {
				longest = r
			}
		}
		slices.Sort(waits)
		median := waits[len(waits)/2]
		if len(waits)%2 == 0 {
			median = (waits[len(waits)/2-1] + waits[len(waits)/2]) / 2
		}
		head += fmt.Sprintf(" · median wait %s · longest %s: %s (%s)", dur(median), dur(longest.answered.Sub(longest.asked)),
			longest.title, longest.by)
	}
	out = append(out, head, "Asked by: "+askers(all),
		fmt.Sprintf("Decided outside the queue (OWNER lines on the bus): %d", outside),
		fmt.Sprintf("Could become standing rules (one kind of question, the same answer %d+ times, %.0f%% or more):",
			repeatMin, repeatShare*100))
	if c := candidates(answered); len(c) > 0 {
		out = append(out, c...)
	} else {
		out = append(out, "  none yet")
	}
	out = append(out, "Answers, oldest first")
	for _, r := range answered {
		wait := "no answer time"
		if !r.answered.IsZero() {
			wait = dur(r.answered.Sub(r.asked))
		}
		out = append(out, fmt.Sprintf("  %s %s · %s → %s (%s)", r.asked.Local().Format("01-02 15:04"), r.by, r.title,
			r.answer, wait))
	}
	if len(answered) == 0 {
		out = append(out, "  none")
	}
	if len(open) > 0 {
		out = append(out, "Still open")
		for _, r := range open {
			out = append(out, fmt.Sprintf("  %s %s · %s (%s so far)", r.asked.Local().Format("01-02 15:04"), r.by, r.title,
				dur(now.Sub(r.asked))))
		}
	}
	if len(undated) > 0 {
		out = append(out, fmt.Sprintf("Undated (the bus line had no date; not in the counts above): %d", len(undated)))
		for _, r := range undated {
			if r.state == "open" {
				out = append(out, fmt.Sprintf("  %s · %s (open)", r.by, r.title))
			} else {
				out = append(out, fmt.Sprintf("  %s · %s → %s", r.by, r.title, r.answer))
			}
		}
	}
	return strings.Join(out, "\n") + "\n", nil
}

// query gives the owner_decision rows matching where (an SQL condition with its order).
func query(db *sql.DB, where string, args ...any) ([]row, error) {
	rows, err := db.Query("select id, coalesce(asked, ''), coalesce(asked_by, ''), coalesce(title, ''),"+
		" coalesce(answer, ''), state, coalesce(answered_at, '') from owner_decision where "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		var r row
		var asked, at string
		if err := rows.Scan(&r.id, &asked, &r.by, &r.title, &r.answer, &r.state, &at); err != nil {
			return nil, err
		}
		if asked != "" {
			if r.asked, err = time.Parse(time.RFC3339, asked); err != nil {
				return nil, fmt.Errorf("%s: asked %q: %w", r.id, asked, err)
			}
		}
		if at != "" {
			if r.answered, err = time.Parse(time.RFC3339, at); err != nil {
				return nil, fmt.Errorf("%s: answered %q: %w", r.id, at, err)
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func askers(rs []row) string {
	n := map[string]int{}
	for _, r := range rs {
		n[r.by]++
	}
	if len(n) == 0 {
		return "none"
	}
	names := make([]string, 0, len(n))
	for k := range n {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if n[names[i]] != n[names[j]] {
			return n[names[i]] > n[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, k := range names {
		parts[i] = fmt.Sprintf("%s %d", k, n[k])
	}
	return strings.Join(parts, ", ")
}

// candidates groups the answered rows by asker and kind of question (kind) and lists each group answered the same way
// (verdict) often enough to become a standing rule.
func candidates(rs []row) []string {
	type group struct {
		by, kind string
		rows     []row
	}
	var groups []*group
	at := map[string]*group{}
	for _, r := range rs {
		k := kind(r.title)
		if k == "" {
			continue
		}
		g := at[r.by+"\x00"+k]
		if g == nil {
			g = &group{by: r.by, kind: k}
			at[r.by+"\x00"+k] = g
			groups = append(groups, g)
		}
		g.rows = append(g.rows, r)
	}
	var out []string
	for _, g := range groups {
		if len(g.rows) < repeatMin {
			continue
		}
		n := map[string]int{}
		top := ""
		for _, r := range g.rows {
			v := verdict(r.answer)
			n[v]++
			if n[v] > n[top] {
				top = v
			}
		}
		if float64(n[top]) < repeatShare*float64(len(g.rows)) {
			continue
		}
		titles := make([]string, len(g.rows))
		for i, r := range g.rows {
			titles[i] = r.title
		}
		out = append(out, fmt.Sprintf("  %s · %s: %s %d of %d (%s)", g.by, g.kind, top, n[top], len(g.rows),
			strings.Join(titles, "; ")))
	}
	return out
}

// stop words leave the kind of a question: "Approve the merge of F-12" and "approve merge F-14" are one kind.
var stop = map[string]bool{"the": true, "and": true, "for": true, "this": true, "that": true, "with": true, "can": true,
	"may": true, "should": true, "now": true, "please": true, "you": true, "our": true, "its": true, "from": true,
	"into": true, "what": true, "which": true, "who": true, "how": true, "are": true, "was": true, "will": true}

// kind is a question's first two words of three or more letters that are no stop word, lower case: ids, shas and
// numbers drop out.
func kind(title string) string {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len([]rune(w)) >= 3 && !stop[w] {
			words = append(words, w)
			if len(words) == 2 {
				break
			}
		}
	}
	return strings.Join(words, " ")
}

// verdict is an answer's first word, with the usual words for yes and no made one.
func verdict(answer string) string {
	w := strings.FieldsFunc(strings.ToLower(answer), func(r rune) bool { return !unicode.IsLetter(r) })
	if len(w) == 0 {
		return ""
	}
	switch w[0] {
	case "yes", "ok", "okay", "go", "approve", "approved", "sure", "yep", "do":
		return "yes"
	case "no", "not", "nope", "don", "stop", "hold", "wait", "later":
		return "no"
	}
	return w[0]
}

func dur(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
