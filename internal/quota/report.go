package quota

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Gap: a sample this far from a request (both sides) leaves the request unmeasured, and open sessions with no sample
// this recent make the quota stale (design §5, M2).
const Gap = 45 * time.Minute

const week = 7 * 24 * time.Hour

type Share struct {
	Project string
	USD     float64
}

// Violation is a session billed to the enterprise account that is not an Appxite project (S1), a session on it
// with no known project, or a launch tag the statusline contradicts.
type Violation struct {
	Session, Project, Kind, Source string // Kind: s1, unmapped, mismatch
}

type Report struct {
	Now                time.Time
	Status             string // complete; stale (open sessions, no sample within Gap); partial (epoch ended); none
	LastSample         time.Time
	LastKind           string
	Reset, FiveReset   time.Time // seven-day epoch end; zero when unknown or already past
	Ended              time.Time // the newest sample's epoch end when it is already past (no sample since)
	SevenPct, FivePct  float64
	Day                int     // day of the epoch, 1..7
	Passed             float64 // share of the epoch's time passed, 0..1
	MaxUSD, Uncovered  float64 // list $ on the Max account in the epoch, and the part with no sample within Gap
	UnknownUSD         float64 // list $ of sessions whose account is not known
	Unpriced           int     // requests in the window with no list price (not in any $ above)
	Shares             []Share // Max list $ by project, largest first
	EntWeek, EntToday  []Share
	EntWeekUSD, EntDay float64
	Violations         []Violation
}

// Querier is a *sql.DB or a *sql.Tx.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// Projects is what the report needs from projects.toml.
type Projects struct {
	EnterpriseOK map[string]bool         // projects that may run on the enterprise account
	ForCwd       func(cwd string) string // the project of a folder, "" when none
}

type obs struct {
	ts time.Time
	rl bool
}

type asg struct {
	from, to         time.Time // to is zero while the assignment is open
	project, account string
}

// sess holds what decides a session's account and project at a given time: the statusline lines (obs) and the
// launch-tag assignments (asg), both sorted by time.
type sess struct {
	persona, cwdProject string
	obs                 []obs
	asg                 []asg
}

// Grace: a statusline line is written just after the response it describes, so it applies from this much earlier.
const Grace = 2 * time.Minute

func (s *sess) assignment(t time.Time) *asg {
	for i := len(s.asg) - 1; i >= 0; i-- {
		if a := &s.asg[i]; !a.from.After(t) && (a.to.IsZero() || t.Before(a.to)) {
			return a
		}
	}
	return nil
}

func (s *sess) project(t time.Time) string {
	if a := s.assignment(t); a != nil {
		return a.project
	}
	return s.cwdProject
}

// account at t: the statusline's newest line by then (a session's first line comes with its first response, so
// earlier requests take it too), else the launch tag active then, else unknown.
func (s *sess) account(t time.Time) string {
	if len(s.obs) > 0 {
		o := s.obs[0]
		for _, x := range s.obs {
			if x.ts.Add(-Grace).After(t) {
				break
			}
			o = x
		}
		if o.rl {
			return "max"
		}
		return "enterprise"
	}
	if a := s.assignment(t); a != nil && a.account != "unknown" {
		return a.account
	}
	return "unknown"
}

// guard lists the session's violations: every stretch billed to the enterprise account (by a statusline line
// until the next Max line, or by an enterprise launch tag) is checked against each project active in it, and a tag
// that a statusline line contradicts is a mismatch.
func (s *sess) guard(id string, ok map[string]bool) []Violation {
	var vs []Violation
	seen := map[string]bool{}
	check := func(project, via string) {
		v := Violation{Session: id, Project: project, Kind: "s1", Source: via}
		switch {
		case project == "":
			v.Kind = "unmapped"
		case ok[project]:
			return
		}
		if k := v.Kind + project + via; !seen[k] {
			seen[k] = true
			vs = append(vs, v)
		}
	}
	// A tag is compared with the statusline at each line and at each tag's start.
	var when []time.Time
	for _, o := range s.obs {
		when = append(when, o.ts.Add(-Grace))
	}
	for _, a := range s.asg {
		if len(s.obs) > 0 {
			when = append(when, a.from)
		}
	}
	for _, t := range when {
		if a, got := s.assignment(t), s.account(t); a != nil && a.account != "unknown" && a.account != got {
			v := Violation{Session: id, Project: a.project, Kind: "mismatch", Source: "tag " + a.account + ", statusline " + got}
			if !seen["mismatch"+v.Source] {
				seen["mismatch"+v.Source] = true
				vs = append(vs, v)
			}
		}
	}
	for i, o := range s.obs {
		if o.rl {
			continue
		}
		from, to := o.ts.Add(-Grace), time.Time{}
		for _, x := range s.obs[i+1:] {
			if x.rl {
				to = x.ts.Add(-Grace)
				break
			}
		}
		check(s.project(from), "statusline")
		for _, a := range s.asg {
			if a.from.After(from) && (to.IsZero() || a.from.Before(to)) {
				check(a.project, "statusline")
			}
		}
	}
	for _, a := range s.asg {
		if a.account == "enterprise" {
			check(a.project, "tag")
		}
	}
	return vs
}

// sessions loads every session the transcripts or the statusline know, with its lines and assignments; sampled
// lists the personas whose statusline has written a line.
func sessions(db Querier, p Projects) (out map[string]*sess, sampled map[string]bool, err error) {
	out, sampled = map[string]*sess{}, map[string]bool{}
	get := func(id string) *sess {
		if out[id] == nil {
			out[id] = &sess{}
		}
		return out[id]
	}
	type load struct {
		q    string
		scan func(r *sql.Rows) error
	}
	loads := []load{
		{"select id, coalesce(persona, ''), coalesce(cwd, '') from session", func(r *sql.Rows) error {
			var id, persona, cwd string
			if err := r.Scan(&id, &persona, &cwd); err != nil {
				return err
			}
			s := get(id)
			s.persona, s.cwdProject = persona, p.ForCwd(cwd)
			return nil
		}},
		{"select session, coalesce(persona, ''), ts, rl from account_seen order by ts", func(r *sql.Rows) error {
			var id, persona, ts string
			var rl bool
			if err := r.Scan(&id, &persona, &ts, &rl); err != nil {
				return err
			}
			t, err := parseTS(ts)
			if err != nil {
				return err
			}
			s := get(id)
			if s.persona == "" {
				s.persona = persona
			}
			sampled[persona] = true
			s.obs = append(s.obs, obs{t, rl})
			return nil
		}},
		{"select session, from_ts, coalesce(to_ts, ''), coalesce(project, ''), coalesce(account, 'unknown')" +
			" from assignment order by from_ts", func(r *sql.Rows) error {
			var id, from, to string
			var a asg
			if err := r.Scan(&id, &from, &to, &a.project, &a.account); err != nil {
				return err
			}
			var err error
			if a.from, err = parseTS(from); err != nil {
				return err
			}
			if to != "" {
				if a.to, err = parseTS(to); err != nil {
					return err
				}
			}
			s := get(id)
			s.asg = append(s.asg, a)
			return nil
		}},
	}
	for _, l := range loads {
		rows, err := db.Query(l.q)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			if err := l.scan(rows); err != nil {
				rows.Close()
				return nil, nil, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
	}
	return out, sampled, nil
}

type sample struct {
	ts               time.Time
	kind             string
	seven, five      sql.NullFloat64
	reset, fiveReset sql.NullInt64
}

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// Build makes the report as of now.
func Build(db Querier, p Projects, now time.Time) (*Report, error) {
	rep := &Report{Now: now, Status: "none"}
	ss, sampled, err := sessions(db, p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ss))
	for id := range ss {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		rep.Violations = append(rep.Violations, ss[id].guard(id, p.EnterpriseOK)...)
	}

	// A line without a seven-day share and reset measured nothing: it neither covers a request nor counts as a
	// sample (it would read as 0%).
	rows, err := db.Query("select ts, sample_kind, seven_day_pct, epoch, five_hour_pct, five_reset from quota" +
		" where account = 'max' and seven_day_pct is not null and epoch is not null order by ts")
	if err != nil {
		return nil, err
	}
	var samples []sample
	for rows.Next() {
		var s sample
		var ts string
		if err := rows.Scan(&ts, &s.kind, &s.seven, &s.reset, &s.five, &s.fiveReset); err != nil {
			rows.Close()
			return nil, err
		}
		if s.ts, err = parseTS(ts); err != nil {
			rows.Close()
			return nil, err
		}
		samples = append(samples, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Resets only move forward, and an idle session's statusline can repeat an older window (older reset, lower
	// share), so the epoch is the latest reset sampled and the share is the highest sampled for it, never the
	// newest line's.
	// Only samples of that epoch count as samples: an idle session's old-epoch heartbeat measures nothing now.
	start := now.Add(-week)
	var reset int64
	for _, s := range samples {
		reset = max(reset, s.reset.Int64)
	}
	cur := samples[:0]
	for _, s := range samples {
		if math.Abs(float64(s.reset.Int64-reset)) < 3600 {
			cur = append(cur, s)
		}
	}
	samples = cur
	if n := len(samples); n > 0 {
		last := samples[n-1]
		rep.LastSample, rep.LastKind = last.ts, last.kind
		var fiveReset int64
		for _, s := range samples {
			if s.five.Valid {
				fiveReset = max(fiveReset, s.fiveReset.Int64)
			}
		}
		for _, s := range samples {
			rep.SevenPct = math.Max(rep.SevenPct, s.seven.Float64)
			if s.five.Valid && math.Abs(float64(s.fiveReset.Int64-fiveReset)) < 600 {
				rep.FivePct = math.Max(rep.FivePct, s.five.Float64)
			}
		}
		rep.Reset = time.Unix(reset, 0)
		if !rep.Reset.After(now) {
			rep.Ended, rep.Reset, rep.SevenPct = rep.Reset, time.Time{}, 0
		} else {
			start = rep.Reset.Add(-week)
			rep.Passed = math.Min(1, math.Max(0, float64(now.Sub(start))/float64(week)))
			rep.Day = int(now.Sub(start)/(24*time.Hour)) + 1
		}
		if r := time.Unix(fiveReset, 0); fiveReset > 0 && r.After(now) {
			rep.FiveReset = r
		} else {
			rep.FivePct = 0
		}
	}

	// Requests since the epoch start (or the last 7 days when no epoch is known), and enterprise since local
	// midnight even when the Max epoch started later.
	y, m, d := now.Local().Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	from := start
	if today.Before(from) {
		from = today
	}
	rows, err = db.Query("select session, coalesce(ts_last, ts_first), cost_list_usd from request"+
		" where coalesce(ts_last, ts_first) >= ?", from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	shares, entWeek, entDay := map[string]float64{}, map[string]float64{}, map[string]float64{}
	var newestOpen time.Time // newest request of a session the sampler should cover
	for rows.Next() {
		var sid, ts string
		var cost sql.NullFloat64
		if err := rows.Scan(&sid, &ts, &cost); err != nil {
			rows.Close()
			return nil, err
		}
		usd := cost.Float64
		t, err := parseTS(ts)
		if err != nil {
			rows.Close()
			return nil, err
		}
		s := ss[sid]
		if s == nil {
			s = &sess{}
		}
		project, account := s.project(t), s.account(t)
		if project == "" {
			project = "unknown"
		}
		entToday := account == "enterprise" && !t.Before(today)
		if entToday {
			rep.EntDay += usd
			entDay[project] += usd
		}
		if !cost.Valid && (entToday || !t.Before(start)) {
			rep.Unpriced++ // a model with no list price: its $ is missing, not 0
		}
		if t.Before(start) {
			continue
		}
		switch account {
		case "max":
			rep.MaxUSD += usd
			shares[project] += usd
			if !near(samples, t) {
				rep.Uncovered += usd
			}
		case "enterprise":
			rep.EntWeekUSD += usd
			entWeek[project] += usd
		default:
			rep.UnknownUSD += usd
		}
		// Max sessions must have a sample, and so must unknown ones of a persona whose statusline samples (personal
		// always): a sampler broken before a session's first line leaves its account unknown.
		if (account == "max" || account == "unknown" && (s.persona == "personal" || sampled[s.persona])) &&
			t.After(newestOpen) {
			newestOpen = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.Shares, rep.EntWeek, rep.EntToday = sorted(shares), sorted(entWeek), sorted(entDay)
	switch {
	case !newestOpen.IsZero() && now.Sub(newestOpen) <= Gap && now.Sub(rep.LastSample) > Gap:
		rep.Status = "stale"
	case !rep.LastSample.IsZero() && (!rep.Ended.IsZero() || rep.Unpriced > 0):
		rep.Status = "partial" // the epoch ended with no sample since, or some $ is missing
	case !rep.LastSample.IsZero():
		rep.Status = "complete"
	}
	return rep, nil
}

// near says whether a sample lies within Gap of t; samples are sorted by time.
func near(samples []sample, t time.Time) bool {
	i := sort.Search(len(samples), func(i int) bool { return !samples[i].ts.Before(t) })
	if i < len(samples) && samples[i].ts.Sub(t) <= Gap {
		return true
	}
	return i > 0 && t.Sub(samples[i-1].ts) <= Gap
}

func sorted(m map[string]float64) []Share {
	var out []Share
	for p, v := range m {
		out = append(out, Share{p, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].USD != out[j].USD {
			return out[i].USD > out[j].USD
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// Runout is when the seven-day quota runs out at the epoch's average pace; zero when it is not known.
func (r *Report) Runout() time.Time {
	if r.Reset.IsZero() || r.SevenPct <= 0 {
		return time.Time{}
	}
	start := r.Reset.Add(-week)
	return start.Add(time.Duration(float64(r.Now.Sub(start)) * 100 / r.SevenPct))
}

func money(v float64) string {
	if v < 10 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.0f", v)
}

func list(ss []Share, total float64, n int) string {
	if len(ss) == 0 {
		return "none"
	}
	var parts []string
	for i, s := range ss {
		if i == n {
			parts = append(parts, fmt.Sprintf("+%d more", len(ss)-n))
			break
		}
		if total > 0 {
			parts = append(parts, fmt.Sprintf("%s %s (%.0f%%)", s.Project, money(s.USD), 100*s.USD/total))
		} else {
			parts = append(parts, s.Project+" "+money(s.USD))
		}
	}
	return strings.Join(parts, ", ")
}

func when(t time.Time) string { return t.Local().Format("Mon 01-02 15:04") }

// Text is the `quota` command's answer (design §4: two blocks, ~200 tokens).
func (r *Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "quota · built %s · status %s", r.Now.Local().Format("2006-01-02 15:04"), r.Status)
	if r.LastSample.IsZero() {
		b.WriteString(" · no quota sample yet\n")
	} else {
		fmt.Fprintf(&b, " · last sample %s (%s, %.0f min ago)\n", when(r.LastSample), r.LastKind,
			r.Now.Sub(r.LastSample).Minutes())
	}
	b.WriteString("Subscription (Max, both personas):")
	if r.Reset.IsZero() {
		if !r.Ended.IsZero() {
			fmt.Fprintf(&b, " epoch ended %s, no sample in the new one yet\n", when(r.Ended))
		} else {
			b.WriteString(" epoch unknown\n")
		}
	} else {
		fmt.Fprintf(&b, " epoch ends %s · day %d of 7 · time passed %.0f%%\n", when(r.Reset), r.Day, 100*r.Passed)
		fmt.Fprintf(&b, "  observed 7d %.0f%%", r.SevenPct)
		if r.Passed > 0 {
			fmt.Fprintf(&b, " · pace %.2fx time passed", r.SevenPct/100/r.Passed)
		}
		if ro := r.Runout(); !ro.IsZero() && ro.Before(r.Reset) {
			fmt.Fprintf(&b, " · runs out ~%s at this pace", when(ro))
		}
		if !r.FiveReset.IsZero() {
			fmt.Fprintf(&b, " · 5h %.0f%% (resets %s)", r.FivePct, r.FiveReset.Local().Format("15:04"))
		}
		b.WriteString("\n")
	}
	pct := 0.0
	if r.MaxUSD > 0 {
		pct = 100 * r.Uncovered / r.MaxUSD
	}
	fmt.Fprintf(&b, "  list price on Max %s · uncovered %s (%.0f%%, no sample within 45 min) · account unknown %s\n",
		money(r.MaxUSD), money(r.Uncovered), pct, money(r.UnknownUSD))
	fmt.Fprintf(&b, "  by project: %s\n", list(r.Shares, r.MaxUSD, 6))
	if r.Unpriced > 0 {
		fmt.Fprintf(&b, "  unpriced: %d requests have a model with no list price; their $ is missing above\n", r.Unpriced)
	}
	fmt.Fprintf(&b, "Enterprise (Appxite API billing, list price): week %s (%s) · today %s (%s)\n",
		money(r.EntWeekUSD), list(r.EntWeek, 0, 4), money(r.EntDay), list(r.EntToday, 0, 4))
	if len(r.Violations) == 0 {
		b.WriteString("  violations: none\n")
	}
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "  %s\n", v.Text())
	}
	return b.String()
}

// PaceText is the pace push (design §8): speed, top burners, one suggestion.
func (r *Report) PaceText() string {
	t := fmt.Sprintf("quota pace high: 7d %.0f%% on day %d of 7 (time passed %.0f%%, %.1f%%/day)", r.SevenPct, r.Day,
		100*r.Passed, r.SevenPct/(r.Passed*7))
	if ro := r.Runout(); !ro.IsZero() && ro.Before(r.Reset) {
		t += "; runs out ~" + when(ro) + " at this pace, reset " + when(r.Reset)
	}
	t += " · top burners: " + list(r.Shares, r.MaxUSD, 3)
	if len(r.Shares) > 0 {
		t += " · suggestion: slow " + r.Shares[0].Project + " (fewer parallel sessions, cheaper helpers) until the reset"
	}
	return t + " · enterprise this week " + money(r.EntWeekUSD)
}

// UsedUpText is the push when the subscription is used up (design §8, D1).
func (r *Report) UsedUpText(enterpriseOK map[string]bool) string {
	var ps []string
	for p := range enterpriseOK {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return fmt.Sprintf("quota used up: 7d %.0f%% until %s. Only Appxite work (%s) may continue, on the Appxite"+
		" enterprise account; personal and scifi work waits until the reset. Enterprise this week %s (%s).",
		r.SevenPct, when(r.Reset), strings.Join(ps, ", "), money(r.EntWeekUSD), list(r.EntWeek, 0, 4))
}

func (v Violation) Text() string {
	id := v.Session
	if len(id) > 8 {
		id = id[:8]
	}
	switch v.Kind {
	case "s1":
		return fmt.Sprintf("S1 personal work on the enterprise account: session %s, project %s (account from %s);"+
			" Appxite pays for it. Stop it or move it to the Max account.", id, v.Project, v.Source)
	case "unmapped":
		return fmt.Sprintf("enterprise session %s has no known project (account from %s): add its folder to"+
			" projects.toml, or stop it if it is not Appxite work.", id, v.Source)
	}
	return fmt.Sprintf("account mismatch in session %s (project %s): %s.", id, v.Project, v.Source)
}
