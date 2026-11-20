package quota

import (
	"database/sql"
	"slices"
	"time"
)

// Week is one login's seven-day window as statusline spool lines show it (Weeks). Measured is the newest line of the
// current window that came with a response (zero when only repeats were seen); Seen is the login's newest line of any kind.
type Week struct {
	AcctUse
	Measured, Seen time.Time
}

// Weeks reads each login's current seven-day window from spool lines of any machine, by the rule Build uses on the store
// (reduce): the share is the window's highest reading, never the newest line's. A line without rate limits, a login, a
// seven-day share or a reset measured nothing and is left out; so is a line whose time cannot be read. Pick reads its
// windows by the same weekOf (Report.WithLines), so the sweep and Pick give one answer per login.
func Weeks(lines []Line, now time.Time) map[string]Week {
	out := map[string]Week{}
	for login, ss := range lineSamples(lines) {
		out[login] = weekOf(ss, now)
	}
	return out
}

// lineSamples are the spool lines that measured a window, as samples by login.
func lineSamples(lines []Line) map[string][]sample {
	byLogin := map[string][]sample{}
	for _, l := range lines {
		if !l.RL || l.Login == "" || l.SevenPct == nil || l.SevenReset == nil {
			continue
		}
		ts, err := parseTS(l.TS)
		if err != nil {
			continue
		}
		s := sample{ts: ts, login: l.Login, kind: l.Kind,
			seven: sql.NullFloat64{Float64: *l.SevenPct, Valid: true}, reset: sql.NullInt64{Int64: *l.SevenReset, Valid: true}}
		if l.FivePct != nil && l.FiveReset != nil {
			s.five, s.fiveReset = sql.NullFloat64{Float64: *l.FivePct, Valid: true}, sql.NullInt64{Int64: *l.FiveReset, Valid: true}
		}
		if l.Responded != nil {
			s.responded = sql.NullBool{Bool: *l.Responded, Valid: true}
		}
		byLogin[l.Login] = append(byLogin[l.Login], s)
	}
	return byLogin
}

// weekOf is one login's window from its samples of any machine and the store.
func weekOf(ss []sample, now time.Time) Week {
	// lines of several machines come in file order; reduce reads them in time order
	ss = slices.Clone(ss)
	slices.SortStableFunc(ss, func(a, b sample) int { return a.ts.Compare(b.ts) })
	cur, u := reduce(ss, now)
	w := Week{AcctUse: u}
	if len(ss) > 0 {
		w.Login = ss[0].login
	}
	for _, s := range ss {
		if s.ts.After(w.Seen) {
			w.Seen = s.ts
		}
	}
	if u.Ended.IsZero() {
		for _, s := range cur {
			if s.measured() && s.ts.After(w.Measured) {
				w.Measured = s.ts
			}
		}
	}
	return w
}
