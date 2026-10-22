package quota

import (
	"database/sql"
	"sort"
	"time"
)

// Spend is the list-price $ of a stretch that has ended, counted as Build counts the running epoch (design §7, M2e
// and M2b): accounts are never merged.
type Spend struct {
	MaxUSD, Uncovered float64 // Max $, and the part with no sample within Gap
	Shares            []Share // Max $ by project, largest first
	EntUSD            float64
	Ent               []Share     // enterprise $ by project, largest first
	UnknownUSD        float64     // account not known: in neither
	Unpriced          int         // requests with no list price (not in any $ above)
	Violations        []Violation // shown by the stretch's own requests
}

// Spent counts the requests in [from, to). A Max request is covered by a sample of the epochs lo..hi (the resets read
// for the week's end) within Gap. Projects by folder come from p, so a changed projects.toml can move $ between
// projects of a week already written.
func Spent(db Querier, p Projects, from, to time.Time, lo, hi int64) (*Spend, error) {
	ss, _, _, err := sessions(db, p)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("select ts from quota where account = 'max' and seven_day_pct is not null"+
		" and epoch between ? and ? order by ts", lo, hi)
	if err != nil {
		return nil, err
	}
	var samples []sample
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return nil, err
		}
		t, err := parseTS(ts)
		if err != nil {
			rows.Close()
			return nil, err
		}
		samples = append(samples, sample{ts: t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].ts.Before(samples[j].ts) })

	// The text bounds are a minute wide on each side (a fraction of a second sorts before "Z"); the times decide. In
	// time order, so a mismatch kept once per text names the same project on every run.
	rows, err = db.Query("select session, coalesce(ts_last, ts_first), cost_list_usd from request"+
		" where coalesce(ts_last, ts_first) >= ? and coalesce(ts_last, ts_first) < ?"+
		" order by coalesce(ts_last, ts_first), request_id",
		from.Add(-time.Minute).UTC().Format(time.RFC3339), to.Add(time.Minute).UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	sp := &Spend{}
	shares, ent := map[string]float64{}, map[string]float64{}
	seen := map[string]bool{}
	// guard's checks and keys, so the counts match the quota report's
	check := func(sid, project, via string) {
		v := Violation{Session: sid, Project: project, Kind: "s1", Source: via}
		switch {
		case project == "":
			v.Kind = "unmapped"
		case p.EnterpriseOK[project]:
			return
		}
		if k := sid + " " + v.Kind + project + via; !seen[k] {
			seen[k] = true
			sp.Violations = append(sp.Violations, v)
		}
	}
	for rows.Next() {
		var sid, ts string
		var cost sql.NullFloat64
		if err := rows.Scan(&sid, &ts, &cost); err != nil {
			rows.Close()
			return nil, err
		}
		t, err := parseTS(ts)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if t.Before(from) || !t.Before(to) {
			continue
		}
		s := ss[sid]
		if s == nil {
			s = &sess{}
		}
		project, account, a := s.project(t), s.account(t), s.assignment(t)
		// guard's rules at this request, priced or not: a session's other stretches count in their own weeks
		if a != nil && len(s.obs) > 0 && a.account != "unknown" && a.account != account {
			v := Violation{Session: sid, Project: a.project, Kind: "mismatch", Source: "tag " + a.account + ", statusline " + account}
			if k := sid + " mismatch" + v.Source; !seen[k] {
				seen[k] = true
				sp.Violations = append(sp.Violations, v)
			}
		}
		if account == "enterprise" {
			switch {
			case len(s.obs) > 0:
				check(sid, project, "statusline")
			case s.org(t) != nil && s.org(t).account == "enterprise":
				check(sid, project, "organization")
			}
		}
		if a != nil && a.account == "enterprise" {
			check(sid, a.project, "tag")
		}
		if !cost.Valid {
			sp.Unpriced++
			continue
		}
		if project == "" {
			project = "unknown"
		}
		switch account {
		case "max":
			sp.MaxUSD += cost.Float64
			shares[project] += cost.Float64
			if !near(samples, t) {
				sp.Uncovered += cost.Float64
			}
		case "enterprise":
			sp.EntUSD += cost.Float64
			ent[project] += cost.Float64
		default:
			sp.UnknownUSD += cost.Float64
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sp.Shares, sp.Ent = sorted(shares), sorted(ent)
	return sp, nil
}
