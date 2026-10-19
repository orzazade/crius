// Package drivers is M11, the context cost drivers (design §7), with the definitions of the review-1016 scripts
// (tools/agent-cost/review-1016/scratch/cost: extract.py, a1.py) so the 10-23 read of the 300K window can come from
// the store: a main call is a request with query_source main, its $ is the list price, its context the prompt
// (fresh input + cache read + cache write), a weekday is a UTC date Mon-Fri, a compaction is a compact_boundary of a
// main transcript.
package drivers

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"
)

// Above is the context size whose share of main $ M11 reports (design §7).
const Above = 267000

type Row struct {
	Persona           string  `json:"persona"`
	Days              string  `json:"days"` // all | weekdays
	MainCalls         int     `json:"main_calls"`
	MainUSD           float64 `json:"main_usd"`
	USDPerMainCall    float64 `json:"usd_per_main_call"`
	Compactions       int     `json:"compactions"`
	CompactionsPer100 float64 `json:"compactions_per_100"`
	ShareAbove267K    float64 `json:"share_usd_above_267k"`
	MeanCtx           float64 `json:"mean_ctx"`
}

type sums struct {
	calls, compactions int
	usd, usdAbove, ctx float64
}

// Build returns two rows per persona (all days, weekdays) for main calls and compactions in [from, to). Stamps are
// compared as times (julianday), so a stored stamp without milliseconds or with an offset lands where its time says.
// A main call with no session persona counts under "unknown"; a main call or compaction whose stamp is missing or
// unreadable, or a main call with no list price, fails the build instead of being dropped.
func Build(db *sql.DB, from, to time.Time) ([]Row, error) {
	lo, hi := from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano)
	for _, q := range []struct{ what, sql string }{
		{"main call", "select count(*) from request where query_source = 'main' and julianday(ts_first) is null"},
		{"compaction", "select count(*) from event where kind = 'compaction' and julianday(ts) is null"},
	} {
		var n int
		if err := db.QueryRow(q.sql).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, fmt.Errorf("%d %s(s) in the store have no readable time", n, q.what)
		}
	}
	agg := map[string]*[2]sums{}
	get := func(p string) *[2]sums {
		if agg[p] == nil {
			agg[p] = &[2]sums{}
		}
		return agg[p]
	}
	rows, err := db.Query("select coalesce(s.persona, 'unknown'), strftime('%w', r.ts_first) not in ('0', '6'),"+
		" count(*), count(r.cost_list_usd), coalesce(sum(r.cost_list_usd), 0),"+
		" coalesce(sum(case when r.tok_in + r.tok_cache_read + r.tok_cache_write > ? then r.cost_list_usd end), 0),"+
		" sum(r.tok_in + r.tok_cache_read + r.tok_cache_write)"+
		" from request r left join session s on s.id = r.session"+
		" where r.query_source = 'main' and julianday(r.ts_first) >= julianday(?) and julianday(r.ts_first) < julianday(?)"+
		" group by 1, 2", Above, lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unpriced := 0
	for rows.Next() {
		var p string
		var wd bool
		var n, priced int
		var usd, above, ctx float64
		if err := rows.Scan(&p, &wd, &n, &priced, &usd, &above, &ctx); err != nil {
			return nil, err
		}
		unpriced += n - priced
		for i, in := range []bool{true, wd} {
			if in {
				s := &get(p)[i]
				s.calls, s.usd, s.usdAbove, s.ctx = s.calls+n, s.usd+usd, s.usdAbove+above, s.ctx+ctx
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if unpriced > 0 {
		return nil, fmt.Errorf("%d main call(s) in %s..%s have no list price", unpriced, lo, hi)
	}
	rows, err = db.Query("select coalesce(s.persona, 'unknown'), strftime('%w', e.ts) not in ('0', '6'), count(*)"+
		" from event e left join session s on s.id = e.session"+
		" where e.kind = 'compaction' and julianday(e.ts) >= julianday(?) and julianday(e.ts) < julianday(?) group by 1, 2", lo, hi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		var wd bool
		var n int
		if err := rows.Scan(&p, &wd, &n); err != nil {
			return nil, err
		}
		get(p)[0].compactions += n
		if wd {
			get(p)[1].compactions += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	personas := make([]string, 0, len(agg))
	for p := range agg {
		personas = append(personas, p)
	}
	sort.Strings(personas)
	var out []Row
	for _, p := range personas {
		for i, days := range []string{"all", "weekdays"} {
			s := agg[p][i]
			r := Row{Persona: p, Days: days, MainCalls: s.calls, MainUSD: s.usd, Compactions: s.compactions}
			if s.calls > 0 {
				r.USDPerMainCall, r.CompactionsPer100, r.MeanCtx = s.usd/float64(s.calls), 100*float64(s.compactions)/float64(s.calls), s.ctx/float64(s.calls)
			}
			if s.usd > 0 {
				r.ShareAbove267K = s.usdAbove / s.usd
			}
			out = append(out, r)
		}
	}
	return out, nil
}

var metrics = []struct {
	name string
	v    func(Row) float64
}{
	{"main_calls", func(r Row) float64 { return float64(r.MainCalls) }},
	{"main_usd", func(r Row) float64 { return r.MainUSD }},
	{"usd_per_main_call", func(r Row) float64 { return r.USDPerMainCall }},
	{"compactions", func(r Row) float64 { return float64(r.Compactions) }},
	{"compactions_per_100", func(r Row) float64 { return r.CompactionsPer100 }},
	{"share_usd_above_267k", func(r Row) float64 { return r.ShareAbove267K }},
	{"mean_ctx", func(r Row) float64 { return r.MeanCtx }},
}

// Compare checks every metric of every reference row against got, as a relative difference; ok only if none is off by
// more than tol (a NaN is off) and both hold the same rows.
func Compare(ref, got []Row, tol float64) (lines []string, ok bool) {
	ok = true
	byKey := map[string]Row{}
	for _, r := range got {
		byKey[r.Persona+" "+r.Days] = r
	}
	for _, r := range ref {
		key := r.Persona + " " + r.Days
		g, found := byKey[key]
		delete(byKey, key)
		if !found {
			lines, ok = append(lines, "FAIL "+key+": missing from the store"), false
			continue
		}
		for _, m := range metrics {
			a, b := m.v(r), m.v(g)
			d := 0.0
			if a != 0 {
				d = b/a - 1
			} else if b != 0 {
				d = math.Inf(1)
			}
			mark := "ok  "
			if !(math.Abs(d) <= tol) {
				mark, ok = "FAIL", false
			}
			lines = append(lines, fmt.Sprintf("%s %s %s %.6g / %.6g (%+.2f%%)", mark, key, m.name, a, b, 100*d))
		}
	}
	for key := range byKey {
		lines, ok = append(lines, "FAIL "+key+": in the store, not in the reference"), false
	}
	return lines, ok
}
