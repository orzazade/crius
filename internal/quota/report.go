package quota

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
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
	Session, Project, Kind, Source string    // Kind: s1, unmapped, mismatch
	Ended                          time.Time // the session's last request when it is over Gap ago; zero while open
}

type Report struct {
	Now        time.Time
	Status     string   // complete or partial (design §4)
	Reasons    []string // why partial: no sample, stale personas, epoch ended, unknown account, unpriced
	Stale      []string // personas with open sessions and no statusline line within Gap, with their last line
	LastSample time.Time
	LastKind   string
	// SevenAt and FiveAt: the newest measurement showing SevenPct and FivePct. A share only rises within its window,
	// so a lower reading is an older one; a measurement is a line that came with a response (one from before that
	// mark: not a heartbeat, which repeats a session's last values).
	SevenAt, FiveAt    time.Time
	FiveKnown          bool      // a 5h window was sampled this epoch; FivePct 0 with it means the window has reset
	Reset, FiveReset   time.Time // seven-day epoch end; zero when unknown or already past
	Ended              time.Time // the newest sample's epoch end when it is already past (no sample since)
	SevenPct, FivePct  float64
	Day                int     // day of the epoch, 1..7
	Passed             float64 // share of the epoch's time passed, 0..1
	MaxUSD, Uncovered  float64 // list $ on the Max account in the epoch, and the part with no sample within Gap
	UnknownUSD         float64 // list $ of sessions whose account is not known
	UnknownSessions    int
	AvgPerDay          float64 // seven-day share used per day over the last 7 days of samples; 0 without 7 days
	Unpriced           int     // requests in the window with no list price (not in any $ above)
	Shares             []Share // Max list $ by project, largest first
	EntWeek, EntToday  []Share
	EntWeekUSD, EntDay float64
	Violations         []Violation
	EntUsage           *EntUsage // set by the `quota` command (a live read), not by Build
	Accounts           []AcctUse // each login measured, listed rows by name first, then unlisted logins by hash
}

// Querier is a *sql.DB or a *sql.Tx.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// Projects is what the report needs from projects.toml.
type Projects struct {
	EnterpriseOK map[string]bool                                   // projects that may run on the enterprise account
	ForCwd       func(cwd string, recorded *store.Worktree) string // the project of a folder, "" when none
	Accounts     *Accounts                                         // names each login; nil: logins go by hash
}

type obs struct {
	ts    time.Time
	rl    bool
	login string // "" on a line from before the field or from a folder with no login
}

// orgLine is a transcript line naming the session's organization, with the account learned for it ("" when none).
type orgLine struct {
	ts           time.Time
	org, account string
}

type asg struct {
	from, to         time.Time // to is zero while the assignment is open
	project, account string
}

// sess holds what decides a session's account and project at a given time: the statusline lines (obs), the
// launch-tag assignments (asg) and the organization lines (orgs), all sorted by time.
type sess struct {
	persona, cwdProject string
	first, last         time.Time // the session's first and last request
	obs                 []obs
	asg                 []asg
	orgs                []orgLine
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

// org is the session's organization line at t: the newest by then, else the first when it comes within Grace
// (it is written with the first prompt); nil when the transcript names none by then. A resumed session can name it
// days after its first requests: those do not take it.
func (s *sess) org(t time.Time) *orgLine {
	if len(s.orgs) == 0 || t.Before(s.orgs[0].ts.Add(-Grace)) {
		return nil
	}
	o := &s.orgs[0]
	for i := range s.orgs {
		if s.orgs[i].ts.After(t) {
			break
		}
		o = &s.orgs[i]
	}
	return o
}

// account at t: the statusline's newest line by then (a session's first line comes with its first response, so
// earlier requests take it too), else the account learned for its organization (measured, so it outranks a tag),
// else the launch tag active then, else unknown.
func (s *sess) account(t time.Time) string {
	if o := s.line(t); o != nil {
		if o.rl {
			return "max"
		}
		return "enterprise"
	}
	if o := s.org(t); o != nil && o.account != "" {
		return o.account
	}
	if a := s.assignment(t); a != nil && a.account != "unknown" {
		return a.account
	}
	return "unknown"
}

// line is the session's statusline line at t: the newest by then, else its first; nil when it has none.
func (s *sess) line(t time.Time) *obs {
	if len(s.obs) == 0 {
		return nil
	}
	o := &s.obs[0]
	for i := range s.obs {
		if s.obs[i].ts.Add(-Grace).After(t) {
			break
		}
		o = &s.obs[i]
	}
	return o
}

// login at t: the login its statusline line then names, "" when none does.
func (s *sess) login(t time.Time) string {
	if o := s.line(t); o != nil {
		return o.login
	}
	return ""
}

// guard lists the session's violations: every stretch billed to the enterprise account (by a statusline line
// until the next Max line, by an enterprise organization when no statusline line exists, or by an enterprise launch
// tag) is checked against each project active in it, and a tag
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
		if i == 0 && !s.first.IsZero() && s.first.Before(from) {
			from = s.first // requests before the first line take its account
		}
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
	// With no statusline line, an organization learned as enterprise bills the stretch until one that is not.
	for i, l := range s.orgs {
		if len(s.obs) > 0 || l.account != "enterprise" || i > 0 && s.orgs[i-1].account == "enterprise" {
			continue
		}
		from, to := l.ts, time.Time{}
		if i == 0 {
			from = l.ts.Add(-Grace) // org() gives the first line to requests this much before it
		}
		for _, x := range s.orgs[i+1:] {
			if x.account != "enterprise" {
				to = x.ts
				break
			}
		}
		check(s.project(from), "organization")
		for _, a := range s.asg {
			if a.from.After(from) && (to.IsZero() || a.from.Before(to)) {
				check(a.project, "organization")
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

// sessions loads every session the transcripts or the statusline know, with its lines and assignments; lastLine
// lists the personas whose statusline has written a line; both lists the organizations the statusline measured on
// both accounts.
func sessions(db Querier, p Projects) (out map[string]*sess, lastLine map[string]time.Time, both []string, err error) {
	out, lastLine = map[string]*sess{}, map[string]time.Time{}
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
		// Each session with its worktree record in one read: an ingest committing both between two reads would leave a
		// session without its record.
		{"select s.id, coalesce(s.persona, ''), coalesce(s.cwd, ''), w.path, w.main from session s" +
			" left join session_worktree w on w.session = s.id", func(r *sql.Rows) error {
			var id, persona, cwd string
			var path, main sql.NullString
			if err := r.Scan(&id, &persona, &cwd, &path, &main); err != nil {
				return err
			}
			var recorded *store.Worktree
			if path.Valid {
				recorded = &store.Worktree{Path: path.String, Main: main.String}
			}
			s := get(id)
			s.persona, s.cwdProject = persona, p.ForCwd(cwd, recorded)
			return nil
		}},
		{"select session, coalesce(persona, ''), ts, rl, coalesce(login, '') from account_seen order by ts", func(r *sql.Rows) error {
			var id, persona, ts, login string
			var rl bool
			if err := r.Scan(&id, &persona, &ts, &rl, &login); err != nil {
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
			if t.After(lastLine[persona]) {
				lastLine[persona] = t
			}
			s.obs = append(s.obs, obs{t, rl, login})
			return nil
		}},
		{"select session, min(coalesce(ts_first, ts_last)), max(coalesce(ts_last, ts_first)) from request" +
			" where coalesce(ts_first, ts_last) is not null group by session", func(r *sql.Rows) error {
			var id, first, last string
			if err := r.Scan(&id, &first, &last); err != nil {
				return err
			}
			s := get(id)
			var err error
			if s.first, err = parseTS(first); err != nil {
				return err
			}
			s.last, err = parseTS(last)
			return err
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
		{"select session, ts, org from org_seen order by ts", func(r *sql.Rows) error {
			var id, ts, org string
			if err := r.Scan(&id, &ts, &org); err != nil {
				return err
			}
			t, err := parseTS(ts)
			if err != nil {
				return err
			}
			s := get(id)
			s.orgs = append(s.orgs, orgLine{ts: t, org: org})
			return nil
		}},
	}
	for _, l := range loads {
		rows, err := db.Query(l.q)
		if err != nil {
			return nil, nil, nil, err
		}
		for rows.Next() {
			if err := l.scan(rows); err != nil {
				rows.Close()
				return nil, nil, nil, err
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, nil, nil, err
		}
	}
	// An organization's account is the one the statusline measured in its sessions; measured on both, it is none.
	// A line within Grace of a switch to another organization is not paired: after a new login the statusline can
	// write before the transcript names the new organization.
	measured := map[string]map[bool]bool{}
	for _, s := range out {
		for _, o := range s.obs {
			if l := s.org(o.ts); l != nil && s.org(o.ts.Add(Grace)).org == l.org {
				if measured[l.org] == nil {
					measured[l.org] = map[bool]bool{}
				}
				measured[l.org][o.rl] = true
			}
		}
	}
	for org, rl := range measured {
		if len(rl) > 1 {
			both = append(both, org)
		}
	}
	sort.Strings(both)
	for _, s := range out {
		for i := range s.orgs {
			if rl := measured[s.orgs[i].org]; len(rl) == 1 {
				s.orgs[i].account = "enterprise"
				if rl[true] {
					s.orgs[i].account = "max"
				}
			}
		}
	}
	return out, lastLine, both, nil
}

type sample struct {
	ts               time.Time
	login            string // "" on a line from before the field or from a folder with no login
	kind             string
	responded        sql.NullBool // null: a line from before the field
	seven, five      sql.NullFloat64
	reset, fiveReset sql.NullInt64
}

func parseTS(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) }

// AcctUse is one login's window as its own lines show it (the fields mean what Report's fields of the same name mean).
type AcctUse struct {
	Login, Name             string // Name: its accounts.toml row, or "login <hash>" when unlisted
	SevenPct, FivePct       float64
	SevenAt, FiveAt         time.Time
	Reset, FiveReset, Ended time.Time
	FiveKnown               bool
}

// reduce keeps the samples of the current epoch and reads its figures from them. Resets only move forward, and an
// idle session's statusline can repeat an older window (older reset, lower share), so the epoch is the latest reset
// sampled and the share is the highest sampled for it, never the newest line's. Only samples of that epoch count as
// samples: an idle session's old-epoch heartbeat measures nothing now.
func reduce(samples []sample, now time.Time) ([]sample, AcctUse) {
	var u AcctUse
	var reset int64
	for _, s := range samples {
		reset = max(reset, s.reset.Int64)
	}
	var cur []sample
	for _, s := range samples {
		if math.Abs(float64(s.reset.Int64-reset)) < 3600 {
			cur = append(cur, s)
		}
	}
	if len(cur) == 0 {
		return cur, u
	}
	var fiveReset int64
	for _, s := range cur {
		if s.five.Valid {
			fiveReset = max(fiveReset, s.fiveReset.Int64)
		}
	}
	for _, s := range cur {
		u.SevenPct = math.Max(u.SevenPct, s.seven.Float64)
		if s.five.Valid && math.Abs(float64(s.fiveReset.Int64-fiveReset)) < 600 {
			u.FivePct = math.Max(u.FivePct, s.five.Float64)
		}
	}
	for _, s := range cur {
		// A measurement is a line that came with a response; a line from before the field, one that is not a
		// heartbeat (Crius 23:11).
		if s.responded.Valid && !s.responded.Bool || !s.responded.Valid && s.kind == "heartbeat" {
			continue
		}
		if s.seven.Float64 == u.SevenPct {
			u.SevenAt = s.ts
		}
		if s.five.Valid && math.Abs(float64(s.fiveReset.Int64-fiveReset)) < 600 && s.five.Float64 == u.FivePct {
			u.FiveAt = s.ts
		}
	}
	u.Reset = time.Unix(reset, 0)
	if !u.Reset.After(now) {
		u.Ended, u.Reset, u.SevenPct = u.Reset, time.Time{}, 0
	}
	u.FiveKnown = fiveReset > 0
	if r := time.Unix(fiveReset, 0); fiveReset > 0 && r.After(now) {
		u.FiveReset = r
	} else {
		u.FivePct = 0
	}
	return cur, u
}

// Build makes the report as of now.
func Build(db Querier, p Projects, now time.Time) (*Report, error) {
	rep := &Report{Now: now}
	ss, lastLine, both, err := sessions(db, p)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(ss))
	for id := range ss {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		last := ss[id].last // a session known only from the statusline has no request: its newest line is the last
		if n := len(ss[id].obs); n > 0 && ss[id].obs[n-1].ts.After(last) {
			last = ss[id].obs[n-1].ts
		}
		for _, v := range ss[id].guard(id, p.EnterpriseOK) {
			if !last.IsZero() && now.Sub(last) > Gap {
				v.Ended = last
			}
			rep.Violations = append(rep.Violations, v)
		}
	}

	// A line without a seven-day share and reset measured nothing: it neither covers a request nor counts as a
	// sample (it would read as 0%).
	rows, err := db.Query("select ts, sample_kind, responded, seven_day_pct, epoch, five_hour_pct, five_reset, coalesce(login, '')" +
		" from quota left join quota_response using (source_key)" +
		" where account = 'max' and seven_day_pct is not null and epoch is not null order by ts")
	if err != nil {
		return nil, err
	}
	var samples []sample
	for rows.Next() {
		var s sample
		var ts string
		if err := rows.Scan(&ts, &s.kind, &s.responded, &s.seven, &s.reset, &s.five, &s.fiveReset, &s.login); err != nil {
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

	rep.AvgPerDay = avgPerDay(samples, now)
	start := now.Add(-week)
	// Each login has its own window: its figures come from its own lines alone. The figures above them mix every
	// login (lines from before the field name none).
	byLogin := map[string][]sample{}
	for _, s := range samples {
		if s.login != "" {
			byLogin[s.login] = append(byLogin[s.login], s)
		}
	}
	newest := map[string]time.Time{} // per login: its newest sample of its own current window
	for login, ls := range byLogin {
		cur, u := reduce(ls, now)
		if n := len(cur); n > 0 {
			newest[login] = cur[n-1].ts
		}
		u.Login, u.Name = login, p.Accounts.Name(login)
		rep.Accounts = append(rep.Accounts, u)
	}
	sort.Slice(rep.Accounts, func(i, j int) bool {
		a, b := rep.Accounts[i], rep.Accounts[j]
		if ua, ub := p.Accounts.Of(a.Login) == nil, p.Accounts.Of(b.Login) == nil; ua != ub {
			return ub // listed accounts first
		}
		return a.Name < b.Name
	})
	samples, u := reduce(samples, now)
	if n := len(samples); n > 0 {
		last := samples[n-1]
		rep.LastSample, rep.LastKind = last.ts, last.kind
		rep.SevenPct, rep.FivePct, rep.SevenAt, rep.FiveAt = u.SevenPct, u.FivePct, u.SevenAt, u.FiveAt
		rep.Reset, rep.FiveReset, rep.Ended, rep.FiveKnown = u.Reset, u.FiveReset, u.Ended, u.FiveKnown
		if !rep.Reset.IsZero() {
			start = rep.Reset.Add(-week)
			rep.Passed = math.Min(1, math.Max(0, float64(now.Sub(start))/float64(week)))
			rep.Day = int(now.Sub(start)/(24*time.Hour)) + 1
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
	newestOpen := map[string]time.Time{} // per persona: newest request of a session the sampler should cover
	newestMax := map[string]time.Time{}  // per login ("" when its line names none): newest Max request
	unknown := map[string]bool{}
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
			unknown[sid] = true
		}
		// Every session that is not known to be on enterprise needs statusline lines: a Max session for its share,
		// an unknown one for its account (a persona whose statusline never ran leaves all its sessions unknown).
		if account != "enterprise" && t.After(newestOpen[s.persona]) {
			newestOpen[s.persona] = t
		}
		if l := s.login(t); account == "max" && t.After(newestMax[l]) {
			newestMax[l] = t
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rep.Shares, rep.EntWeek, rep.EntToday = sorted(shares), sorted(entWeek), sorted(entDay)
	rep.UnknownSessions = len(unknown)
	for persona, t := range newestOpen {
		if now.Sub(t) > Gap {
			continue
		}
		switch last := lastLine[persona]; {
		case last.IsZero():
			rep.Stale = append(rep.Stale, persona+": no statusline line yet")
		case now.Sub(last) > Gap:
			rep.Stale = append(rep.Stale, persona+": last statusline line "+when(last))
		}
	}
	sort.Strings(rep.Stale)
	// Lines can keep coming while none measures the current epoch (an idle session repeating an old window), so
	// open Max sessions also need a current-epoch sample: of their own login's window, as each login has its own
	// (live 10-23: max1's later reset hid max2's samples). A session whose line names no login takes any sample.
	if len(rep.Stale) == 0 {
		var logins []string
		for l, t := range newestMax {
			if now.Sub(t) <= Gap {
				logins = append(logins, l)
			}
		}
		sort.Strings(logins)
		for _, l := range logins {
			name, last := "max account", newest[l]
			if l != "" {
				name += " " + p.Accounts.Name(l)
			} else {
				last = rep.LastSample
				for _, t := range newest {
					if t.After(last) {
						last = t
					}
				}
			}
			if now.Sub(last) <= Gap {
				continue
			}
			at := "none"
			if !last.IsZero() {
				at = when(last)
			}
			rep.Stale = append(rep.Stale, name+": newest current-epoch sample "+at)
		}
	}
	if rep.LastSample.IsZero() {
		rep.Reasons = append(rep.Reasons, "no quota sample yet")
	}
	for _, st := range rep.Stale {
		rep.Reasons = append(rep.Reasons, "stale sampler, open sessions of "+st)
	}
	if !rep.Ended.IsZero() {
		rep.Reasons = append(rep.Reasons, "epoch ended "+when(rep.Ended)+", no sample since")
	}
	for _, org := range both {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("organization %s seen on both accounts: its sessions stay unknown",
			org[:min(8, len(org))]))
	}
	if rep.UnknownUSD > 0 {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("account unknown for %s in %d sessions (not measured)",
			money(rep.UnknownUSD), rep.UnknownSessions))
	}
	if rep.Unpriced > 0 {
		rep.Reasons = append(rep.Reasons, fmt.Sprintf("%d requests with no list price", rep.Unpriced))
	}
	rep.Status = "complete"
	if len(rep.Reasons) > 0 {
		rep.Status = "partial"
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
// avgPerDay is the seven-day share used per day over the last 7 days of samples: per epoch, its highest share in
// the window less its highest share before the window (0 for an epoch that began inside it). It is 0 unless samples
// reach back 7 days, since a shorter history cannot give a 7-day average.
func avgPerDay(samples []sample, now time.Time) float64 {
	from := now.Add(-week)
	if len(samples) == 0 || samples[0].ts.After(from) {
		return 0
	}
	var keys []int64
	epoch := func(reset int64) int64 { // resets of one epoch differ by seconds
		for _, k := range keys {
			if math.Abs(float64(k-reset)) < 3600 {
				return k
			}
		}
		keys = append(keys, reset)
		return reset
	}
	before, in := map[int64]float64{}, map[int64]float64{}
	for _, s := range samples {
		e := epoch(s.reset.Int64)
		if s.ts.Before(from) {
			before[e] = math.Max(before[e], s.seven.Float64)
		} else if !s.ts.After(now) {
			in[e] = math.Max(in[e], s.seven.Float64)
		}
	}
	used := 0.0
	for e, v := range in {
		used += math.Max(0, v-before[e])
	}
	return used / 7
}

// PerDay is this epoch's seven-day share used per day so far.
func (r *Report) PerDay() float64 {
	if r.Passed <= 0 {
		return 0
	}
	return r.SevenPct / (r.Passed * 7)
}

// PaceHigh is the pace push rule (design §8): 60% or more by day 3, or from day 2 faster than the 7-day average.
func (r *Report) PaceHigh() bool {
	return !r.Reset.IsZero() && (r.SevenPct >= 60 && r.Day <= 3 ||
		r.Day >= 2 && r.AvgPerDay > 0 && r.PerDay() > r.AvgPerDay)
}

// RunsOut: from day 2, the share passes 100% before resets_at at this pace (design §8).
func (r *Report) RunsOut() bool {
	ro := r.Runout()
	return r.Day >= 2 && r.SevenPct < 100 && !ro.IsZero() && ro.Before(r.Reset)
}

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

// AccountsText is each login's seven-day share and reset: "max1 7d 40% (resets Fri 10-23 19:57) · max2 …".
func (r *Report) AccountsText() string {
	var parts []string
	for _, a := range r.Accounts {
		switch {
		case !a.Reset.IsZero():
			parts = append(parts, fmt.Sprintf("%s 7d %.0f%% (resets %s)", a.Name, a.SevenPct, when(a.Reset)))
		case !a.Ended.IsZero():
			parts = append(parts, fmt.Sprintf("%s epoch ended %s", a.Name, when(a.Ended)))
		}
	}
	return strings.Join(parts, " · ")
}

// Text is the `quota` command's answer (design §4: two blocks, ~200 tokens).
func (r *Report) Text() string {
	var b strings.Builder
	b.WriteString(r.Status + "\n")
	for _, why := range r.Reasons {
		b.WriteString("  " + why + "\n")
	}
	fmt.Fprintf(&b, "quota · built %s", r.Now.Local().Format("2006-01-02 15:04"))
	if r.LastSample.IsZero() {
		b.WriteString("\n")
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
		if r.AvgPerDay > 0 {
			fmt.Fprintf(&b, " · %.1f%%/day (7-day average %.1f%%/day)", r.PerDay(), r.AvgPerDay)
		}
		if ro := r.Runout(); !ro.IsZero() && ro.Before(r.Reset) {
			fmt.Fprintf(&b, " · runs out ~%s at this pace", when(ro))
		}
		if !r.FiveReset.IsZero() {
			fmt.Fprintf(&b, " · 5h %.0f%% (resets %s)", r.FivePct, r.FiveReset.Local().Format("15:04"))
		}
		b.WriteString("\n")
	}
	if len(r.Accounts) > 0 {
		b.WriteString("  by account: " + r.AccountsText() + "\n")
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
	b.WriteString("Enterprise (Appxite API billing, list price): ")
	switch {
	case r.UnknownUSD == 0:
		fmt.Fprintf(&b, "week %s (%s) · today %s (%s)\n",
			money(r.EntWeekUSD), list(r.EntWeek, 0, 4), money(r.EntDay), list(r.EntToday, 0, 4))
	case r.EntWeekUSD+r.EntDay > 0: // that spend may be enterprise: the known totals are a floor
		fmt.Fprintf(&b, "unknown %s (account not measured) · at least week %s (%s) · today %s (%s)\n",
			money(r.UnknownUSD), money(r.EntWeekUSD), list(r.EntWeek, 0, 4), money(r.EntDay), list(r.EntToday, 0, 4))
	default: // nothing known to be enterprise: no "$0.00 (none)" next to unmeasured spend
		fmt.Fprintf(&b, "unknown %s (account not measured)\n", money(r.UnknownUSD))
	}
	if r.EntUsage != nil {
		b.WriteString("  " + r.EntUsage.Text() + "\n")
	}
	if r.UnknownSessions > 0 {
		fmt.Fprintf(&b, "  guard: not measured for %d session%s (account unknown)\n", r.UnknownSessions,
			map[bool]string{true: "", false: "s"}[r.UnknownSessions == 1])
	} else if len(r.Violations) == 0 {
		b.WriteString("  violations: none\n")
	}
	for _, v := range r.Violations {
		fmt.Fprintf(&b, "  %s\n", v.Text())
	}
	return b.String()
}

// An alert must fit a bus line (300 runes, store.MaxLen; a longer one is cut at the end), so every variable part of
// it is bounded: a name to 10 runes in a push (40 in a guard or stale alert), a list to two names, $ to 7 runes.
// Full names, lists and $ are in `crius-rec quota`.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func short(name string) string { return clip(name, 10) }

// cash is $ in at most 7 runes: $9999 or less as money() says it, then $10.0k, $1.0M, $1.0B, $1.0T.
func cash(v float64) string {
	if v < 9999.5 {
		return money(v)
	}
	for _, unit := range []string{"k", "M", "B", "T"} {
		if v /= 1000; v < 999.95 {
			return fmt.Sprintf("$%.1f%s", v, unit)
		}
	}
	return ">$999T"
}

func names(ps []string) string {
	var out []string
	for i, p := range ps {
		if i == 2 {
			out = append(out, fmt.Sprintf("+%d more", len(ps)-2))
			break
		}
		out = append(out, short(p))
	}
	return strings.Join(out, ", ")
}

// Burners is the top n Max projects with their share of Max $ (when there is a Max $ to share).
func (r *Report) Burners(n int) string {
	var out []string
	for _, s := range r.Shares[:min(n, len(r.Shares))] {
		if r.MaxUSD > 0 {
			out = append(out, fmt.Sprintf("%s %.0f%%", short(s.Project), 100*s.USD/r.MaxUSD))
		} else { // all Max requests unpriced: no share to say
			out = append(out, short(s.Project))
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// pace is the speed part of the pace and runout pushes.
func (r *Report) pace() string {
	t := fmt.Sprintf("7d %.0f%% on day %d (%.1f%%/day", r.SevenPct, r.Day, r.PerDay())
	if r.AvgPerDay > 0 && r.PerDay() > r.AvgPerDay {
		t += fmt.Sprintf(", faster than the 7-day average %.1f%%/day", r.AvgPerDay)
	}
	return t + ")"
}

func (r *Report) suggestion() string {
	if len(r.Shares) == 0 {
		return ""
	}
	return " · suggestion: slow " + short(r.Shares[0].Project) + " until the reset"
}

// PaceText is the pace push (design §8): speed, top burners, one suggestion.
func (r *Report) PaceText() string {
	return "quota pace high: " + r.pace() + " · top burners: " + r.Burners(2) + r.suggestion() + ". " + r.entWeek()
}

// RunoutText is the push when the subscription will be used up before the reset at this pace (design §8). It also
// carries the pace facts and suggestion, so it can stand for the pace push when both are due in one run; it is the
// longest push, so its wording is the shortest.
func (r *Report) RunoutText(enterpriseOK map[string]bool) string {
	t := fmt.Sprintf("quota runs out ~%s: 7d %.0f%% day %d, %.1f%%/day", when(r.Runout()), r.SevenPct, r.Day, r.PerDay())
	if r.AvgPerDay > 0 && r.PerDay() > r.AvgPerDay {
		t += fmt.Sprintf(" (7-day avg %.1f)", r.AvgPerDay)
	}
	if len(r.Shares) > 0 {
		t += " · top burner " + r.Burners(1) + ", slow it"
	}
	return t + fmt.Sprintf(" · only Appxite work (%s) may continue, on enterprise; personal/scifi work waits until %s · %s",
		names(okList(enterpriseOK)), when(r.Reset), r.entWeek())
}

// UsedUpText is the push when the subscription is used up (design §8, D1).
func (r *Report) UsedUpText(enterpriseOK map[string]bool) string {
	return fmt.Sprintf("quota used up: 7d %.0f%% until %s. Only Appxite work (%s) may continue, on the Appxite"+
		" enterprise account; personal and scifi work waits until the reset. %s", r.SevenPct, when(r.Reset),
		names(okList(enterpriseOK)), r.entWeek())
}

// entWeek is the enterprise $ of the week for a push (the total: by project is in `crius-rec quota`, and a push must
// fit a bus line). Spend whose account is unknown may be enterprise, so it is said as unknown and the known $ as a
// floor, never "$0.00 (none)".
func (r *Report) entWeek() string {
	switch {
	case r.UnknownUSD == 0:
		return fmt.Sprintf("Enterprise this week %s.", cash(r.EntWeekUSD))
	case r.EntWeekUSD > 0:
		return fmt.Sprintf("Enterprise this week ≥ %s + unknown %s.", cash(r.EntWeekUSD), cash(r.UnknownUSD))
	}
	return fmt.Sprintf("Enterprise this week unknown %s (not measured).", cash(r.UnknownUSD))
}

func okList(enterpriseOK map[string]bool) []string {
	var ps []string
	for p := range enterpriseOK {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// Text is the violation as the `quota` report says it, names in full.
func (v Violation) Text() string { return v.text(1 << 30) }

// AlertText is the violation as an alert: names clipped so the instruction fits a bus line.
func (v Violation) AlertText() string { return v.text(40) }

func (v Violation) text(n int) string {
	id := v.Session
	if len(id) > 8 {
		id = id[:8]
	}
	ended := !v.Ended.IsZero()
	switch {
	case v.Kind == "s1" && ended:
		return fmt.Sprintf("S1 personal work on the enterprise account: session %s, project %s (account from %s),"+
			" ended %s; Appxite paid for it. Run this project on the Max account.", id, clip(v.Project, n),
			clip(v.Source, n), when(v.Ended))
	case v.Kind == "unmapped" && ended:
		return fmt.Sprintf("enterprise session %s (ended %s) has no known project (account from %s): add its folder"+
			" to projects.toml if it is Appxite work.", id, when(v.Ended), clip(v.Source, n))
	case v.Kind == "s1":
		return fmt.Sprintf("S1 personal work on the enterprise account: session %s, project %s (account from %s);"+
			" Appxite pays for it. Stop it or move it to the Max account.", id, clip(v.Project, n), clip(v.Source, n))
	case v.Kind == "unmapped":
		return fmt.Sprintf("enterprise session %s has no known project (account from %s): add its folder to"+
			" projects.toml, or stop it if it is not Appxite work.", id, clip(v.Source, n))
	}
	return fmt.Sprintf("account mismatch in session %s (project %s): %s.", id, clip(v.Project, n), clip(v.Source, 2*n))
}

// StaleText is the alert for one stale entry of Report.Stale (design §5).
func StaleText(entry string) string {
	return "quota/max STALE: sessions are open but the statusline sampler wrote nothing within 45 min (" +
		clip(entry, 80) + "); is crius-statusline in that persona's statusLine and working?"
}
