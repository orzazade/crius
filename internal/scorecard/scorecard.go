// Package scorecard is the weekly scorecard (design §7): one quota week's accepted outcomes, flow, quality and
// observed quota, from the store alone (0 model tokens). A revoked acceptance counts against the week it was accepted
// in, corrections apply through item_v and defect_v, and a written week is never rewritten: a changed number appends
// a `revised` line (design §7b).
package scorecard

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
)

// Def is the scorecard definition; a week written under another one is not compared with this one. v2 (owner 10-19,
// P3c): defects and revokes on the outcomes' accepted child tasks count against the outcome's week.
const Def = "v2"

const (
	week   = 7 * 24 * time.Hour
	mature = 14 * 24 * time.Hour // the reopen and defect window after a week ends (design §7 cohorts)
	stamp  = "2006-01-02T15:04:05Z"
)

// Week is [Start, End): End is a Max seven-day reset. Lo..Hi are the resets read for End (one reset read a few
// seconds apart); a week stepped from a sampled one has Lo = Hi = End and no samples.
type Week struct {
	Start, End time.Time
	Lo, Hi     int64
}

// Numbers are what a revised line compares. MaxPct is -1 when the week has no Max sample.
type Numbers struct {
	Code, Research, Falcon, Other, Revoked, Defects, Escapes, ChildRevoked int
	MaxPct                                                                 float64
}

type Card struct {
	Week     Week
	Text     string
	Numbers  Numbers
	Items    []string // the week's outcomes, revoked ones too
	Children []string // the child tasks under them, at any depth (as corrected)
	Event    int64    // the newest event id when it was built: later events explain a revision
}

var (
	ErrNoWeeks = errors.New("no Max quota sample: week boundaries unknown")
	ErrRunning = errors.New("the week has not ended")
	ErrNoWeek  = errors.New("no such week")
)

// WeekOf gives the week holding t. Week ends are the sampled Max epochs (quota.epoch, the seven-day resets_at);
// before the first, after the last and between two of them, weeks step 7 days from the nearest later end (from the
// last end forward), so a gap that is not a multiple of 7 days leaves one short week after the earlier end.
func WeekOf(db *sql.DB, t time.Time) (Week, error) {
	rows, err := db.Query("select distinct epoch from quota where account = 'max' and epoch is not null" +
		" and seven_day_pct is not null order by epoch")
	if err != nil {
		return Week{}, err
	}
	var ends []time.Time
	var span [][2]int64 // per end, the resets read for it
	for rows.Next() {
		var e int64
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return Week{}, err
		}
		// a reset read within an hour of the one before is the same reset (the quota report's tolerance)
		if n := len(span); n > 0 && e-span[n-1][1] < 3600 {
			span[n-1][1] = e
			continue
		}
		ends, span = append(ends, time.Unix(e, 0)), append(span, [2]int64{e, e})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Week{}, err
	}
	if len(ends) == 0 {
		return Week{}, ErrNoWeeks
	}
	if last := ends[len(ends)-1]; !t.Before(last) {
		end := last.Add((t.Sub(last)/week + 1) * week)
		return Week{end.Add(-week), end, end.Unix(), end.Unix()}, nil
	}
	i := sort.Search(len(ends), func(i int) bool { return ends[i].After(t) }) // the nearest later end
	k := (ends[i].Sub(t) - 1) / week
	end := ends[i].Add(-k * week)
	start := end.Add(-week)
	if i > 0 && start.Before(ends[i-1]) {
		start = ends[i-1]
	}
	if k == 0 {
		return Week{start, end, span[i][0], span[i][1]}, nil
	}
	return Week{start, end, end.Unix(), end.Unix()}, nil
}

// Name is a week's file name and --week value: the UTC day it ends, the same in every zone.
func Name(w Week) string { return w.End.UTC().Format("2006-01-02") }

// WeekEnding gives the week that ends on the UTC day (YYYY-MM-DD); with two, the later one.
func WeekEnding(db *sql.DB, day string) (Week, error) {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return Week{}, fmt.Errorf("--week must be YYYY-MM-DD, got %q", day)
	}
	w, err := WeekOf(db, d.Add(-time.Nanosecond)) // the week holding the day's start ends on it or later
	for err == nil {
		next, err := WeekOf(db, w.End)
		if err != nil {
			return Week{}, err
		}
		if Name(next) != day {
			break
		}
		w = next
	}
	if err == nil && Name(w) != day {
		err = fmt.Errorf("%w: no quota week ends on %s (UTC); the weeks around it end %s and %s (%s local)", ErrNoWeek,
			day, Name(Week{End: w.Start}), Name(w), w.Start.Local().Format("Mon 01-02 15:04"))
	}
	return w, err
}

type outcome struct {
	id, project, kind, state, revision, defect string
	opened, accepted, revoked                  time.Time
}

func parse(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, s)
}

// Build computes the week's card as the store holds it now.
func Build(db *sql.DB, w Week, now time.Time) (*Card, error) {
	from, to := w.Start.UTC().Format(stamp), w.End.UTC().Format(stamp)
	// An outcome is an item with no parent (as corrected); the work under it is not counted (design §7).
	rows, err := db.Query("select a.item, i.project, i.kind, a.state, a.revision, coalesce(a.defect, ''), i.opened,"+
		" a.ts, coalesce(a.revoked_ts, '') from acceptance a join item_v i on i.id = a.item"+
		" where coalesce(i.parent, '') = '' and a.ts >= ? and a.ts < ? order by a.ts, a.id", from, to)
	if err != nil {
		return nil, err
	}
	var all []outcome
	for rows.Next() {
		var o outcome
		var opened, accepted, revoked string
		if err := rows.Scan(&o.id, &o.project, &o.kind, &o.state, &o.revision, &o.defect, &opened, &accepted,
			&revoked); err != nil {
			rows.Close()
			return nil, err
		}
		for _, p := range []struct {
			s string
			t *time.Time
		}{{opened, &o.opened}, {accepted, &o.accepted}, {revoked, &o.revoked}} {
			if *p.t, err = parse(p.s); err != nil {
				rows.Close()
				return nil, fmt.Errorf("acceptance of %s: %w", o.id, err)
			}
		}
		all = append(all, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	c := &Card{Week: w}
	n := &c.Numbers
	byKind := map[string]*int{"code": &n.Code, "research": &n.Research, "falcon": &n.Falcon, "other": &n.Other}
	byProject := map[string]map[string]int{}
	cycle := map[string][]float64{}
	var counted, revoked []outcome
	for _, o := range all {
		c.Items = append(c.Items, o.id)
		if o.state == "revoked" {
			n.Revoked++
			revoked = append(revoked, o)
			continue
		}
		p, ok := byKind[o.kind]
		if !ok {
			return nil, fmt.Errorf("%s has kind %q, which the scorecard does not count", o.id, o.kind)
		}
		*p++
		if byProject[o.project] == nil {
			byProject[o.project] = map[string]int{}
		}
		byProject[o.project][o.kind]++
		cycle[o.kind] = append(cycle[o.kind], o.accepted.Sub(o.opened).Hours()/24)
		counted = append(counted, o)
	}

	// Owner 10-19 (P3c): a defect or revoke on an accepted child task, at any depth (as corrected), counts against
	// its outcome's week. The tree is walked here from the parents read once, and the queries take it as one JSON
	// parameter: a large tree is no list of parameters, and a recursive join on item_v's corrected parent has no index
	// (review of P3c).
	var childRevoked []outcome
	if c.Children, err = children(db, c.Items); err != nil {
		return nil, err
	}
	tree, err := json.Marshal(slices.Concat(c.Items, c.Children))
	if err != nil {
		return nil, err
	}
	kids, err := json.Marshal(c.Children)
	if err != nil {
		return nil, err
	}
	if len(c.Children) > 0 {
		rows, err := db.Query("select item, revision, coalesce(defect, ''), coalesce(revoked_ts, '') from acceptance"+
			" where state = 'revoked' and item in (select value from json_each(?)) order by ts, id", string(kids))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o outcome
			var revokedTS string
			if err := rows.Scan(&o.id, &o.revision, &o.defect, &revokedTS); err != nil {
				rows.Close()
				return nil, err
			}
			if o.revoked, err = parse(revokedTS); err != nil {
				rows.Close()
				return nil, fmt.Errorf("acceptance of %s: %w", o.id, err)
			}
			childRevoked = append(childRevoked, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		n.ChildRevoked = len(childRevoked)
	}

	// A defect counts when it was found at or after the acceptance: one found before is no escape (design §7b). Once,
	// however many acceptances came before it (review of P3c).
	foundBy := map[string]int{}
	if len(c.Items) > 0 {
		q := "select d.found_by, count(*) from defect_v d where d.item in (select value from json_each(?)) and exists" +
			" (select 1 from acceptance a where a.item = d.item and d.found_ts >= a.ts) group by d.found_by"
		rows, err := db.Query(q, string(tree))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var by string
			var k int
			if err := rows.Scan(&by, &k); err != nil {
				rows.Close()
				return nil, err
			}
			foundBy[by] = k
			n.Defects += k
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		n.Escapes = foundBy["owner"] + foundBy["others"]
	}

	var corrections, guards, samples int
	var maxPct sql.NullFloat64
	for _, q := range []struct {
		sql  string
		args []any
		dst  []any
	}{
		{"select count(*) from correction where ts >= ? and ts < ?", []any{from, to}, []any{&corrections}},
		{"select count(distinct g.id) from defect d join item g on g.id = d.guard_item where g.opened >= ?" +
			" and g.opened < ?", []any{from, to}, []any{&guards}},
		{"select count(*), max(seven_day_pct) from quota where account = 'max' and seven_day_pct is not null" +
			" and epoch between ? and ?", []any{w.Lo, w.Hi}, []any{&samples, &maxPct}},
		{"select coalesce(max(id), 0) from event", nil, []any{&c.Event}},
	} {
		if err := db.QueryRow(q.sql, q.args...).Scan(q.dst...); err != nil {
			return nil, err
		}
	}
	n.MaxPct = -1
	if maxPct.Valid {
		n.MaxPct = maxPct.Float64
	}

	status := "complete"
	if !maxPct.Valid {
		status = "partial (no Max sample in the week)"
	}
	head := fmt.Sprintf("# Quota week ending %s · built %s · status %s · def %s",
		w.End.Local().Format("2006-01-02 15:04"), now.Local().Format("2006-01-02 15:04"), status, Def)
	if d := w.End.Sub(w.Start); d != week {
		head += fmt.Sprintf(" · %.0f days", d.Hours()/24)
	}
	cohort := "running"
	switch {
	case !now.Before(w.End.Add(mature)):
		cohort = "matured"
	case !now.Before(w.End):
		cohort = "provisional, matures " + w.End.Add(mature).Local().Format("01-02")
	}
	lines := []string{head, fmt.Sprintf("Outcomes   code %d · research %d · falcon %d · other %d (%s)%s",
		n.Code, n.Research, n.Falcon, n.Other, cohort, perProject(byProject))}

	named := func(os []outcome) string {
		var ss []string
		for _, o := range os {
			ss = append(ss, fmt.Sprintf("%s on %s (%s, %s)", o.id, short(o.revision), o.defect,
				o.revoked.Local().Format("01-02")))
		}
		return strings.Join(ss, ", ")
	}
	rv := "Revoked    none"
	if len(revoked) > 0 {
		rv = fmt.Sprintf("Revoked    %d of %d: %s", len(revoked), len(all), named(revoked))
	}
	if len(childRevoked) > 0 {
		rv += fmt.Sprintf(" · child tasks %d: %s", len(childRevoked), named(childRevoked))
	}
	lines = append(lines, rv)

	qt := "Quota      Max (both personas): no sample in the week (not measured)"
	if maxPct.Valid {
		qt = fmt.Sprintf("Quota      Max (both personas): %.0f%% observed (%d samples)", maxPct.Float64, samples)
	}
	lines = append(lines, qt+" · enterprise $ and project shares: not in def v1 (crius-rec quota: running week)",
		"Waits      not measured (step 7)", "Flow       "+flow(cycle),
		fmt.Sprintf("Quality    defects on these outcomes and their child tasks %d (found by us %d, owner %d, others %d) · escapes %d ·"+
			" revoked %d of %d · corrections recorded %d · guards opened %d", n.Defects, foundBy["us"],
			foundBy["owner"], foundBy["others"], n.Escapes, n.Revoked, len(all), corrections, guards),
		"Spot-check "+spotCheck(w, counted))
	c.Text = strings.Join(lines, "\n") + "\n"
	return c, nil
}

// children is the child tasks under outcomes at any depth, as corrected, sorted.
func children(db *sql.DB, outcomes []string) ([]string, error) {
	rows, err := db.Query("select id, parent from item_v where coalesce(parent, '') != ''")
	if err != nil {
		return nil, err
	}
	under := map[string][]string{}
	for rows.Next() {
		var id, parent string
		if err := rows.Scan(&id, &parent); err != nil {
			rows.Close()
			return nil, err
		}
		under[parent] = append(under[parent], id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for next := slices.Clone(outcomes); len(next) > 0; {
		id := next[len(next)-1]
		next = next[:len(next)-1]
		for _, k := range under[id] {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
				next = append(next, k)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

func perProject(m map[string]map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	var ps []string
	for p := range m {
		ps = append(ps, p)
	}
	slices.Sort(ps)
	for i, p := range ps {
		var ks []string
		for _, k := range []string{"code", "research", "falcon", "other"} {
			if m[p][k] > 0 {
				ks = append(ks, fmt.Sprintf("%s %d", k, m[p][k]))
			}
		}
		ps[i] = p + ": " + strings.Join(ks, ", ")
	}
	return " | " + strings.Join(ps, " · ")
}

// flow is cycle time (item open -> acceptance) per kind, nearest-rank p50 and p90.
func flow(cycle map[string][]float64) string {
	var parts []string
	small := false
	for _, k := range []string{"code", "research", "falcon", "other"} {
		ds := cycle[k]
		if len(ds) == 0 {
			continue
		}
		slices.Sort(ds)
		rank := func(p float64) float64 { return ds[max(0, int(float64(len(ds))*p+0.999999)-1)] }
		parts = append(parts, fmt.Sprintf("%s p50 %.1f d, p90 %.1f d (n=%d)", k, rank(0.5), rank(0.9), len(ds)))
		small = small || len(ds) < 10
	}
	if len(parts) == 0 {
		return "no accepted outcomes"
	}
	s := "cycle time " + strings.Join(parts, " · ")
	if small {
		s += " · small sample"
	}
	return s
}

// spotCheck names 3 of the week's outcomes to open (design §7 Goodhart guards), the same 3 on every build.
func spotCheck(w Week, outs []outcome) string {
	if len(outs) == 0 {
		return "nothing to open"
	}
	key := func(o outcome) string {
		h := sha256.Sum256([]byte(w.End.UTC().Format(stamp) + " " + o.id))
		return string(h[:])
	}
	outs = slices.Clone(outs)
	slices.SortFunc(outs, func(a, b outcome) int { return strings.Compare(key(a), key(b)) })
	var ss []string
	for _, o := range outs[:min(3, len(outs))] {
		ss = append(ss, fmt.Sprintf("%s (%s)", o.id, short(o.revision)))
	}
	return strings.Join(ss, ", ") + ": open their evidence"
}

type written struct {
	Def      string
	Numbers  Numbers
	Items    []string
	Children []string `json:",omitempty"`
	Written  string   // when Numbers were last written
	Event    int64    // the newest event id of that build: events after it explain a revision
}

// Write saves an ended week as <dir>/<end date>.md with its numbers in .json. A week already written keeps its text;
// when a number changed, one line `revised <date>: <name> <old> -> <new>, … (<record events since>)` is appended.
// what is "wrote", "revised" or "unchanged".
func Write(db *sql.DB, dir string, c *Card, now time.Time) (path, what string, err error) {
	if now.Before(c.Week.End) {
		return "", "", fmt.Errorf("%w: it ends %s; print it without --write", ErrRunning,
			c.Week.End.Local().Format("Mon 01-02 15:04"))
	}
	base := filepath.Join(dir, Name(c.Week))
	path = base + ".md"
	save := func() error {
		b, err := json.MarshalIndent(written{Def, c.Numbers, c.Items, c.Children, now.UTC().Format(stamp), c.Event}, "", " ")
		if err != nil {
			return err
		}
		return os.WriteFile(base+".json", append(b, '\n'), 0o600)
	}
	b, err := os.ReadFile(base + ".json")
	if errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("%s exists without its numbers file %s.json: not overwritten", path, base)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", err
		}
		text, _ := redact.Redact(c.Text)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			return "", "", err
		}
		return path, "wrote", save()
	}
	if err != nil {
		return "", "", err
	}
	var old written
	if err := json.Unmarshal(b, &old); err != nil {
		return "", "", fmt.Errorf("%s.json: %w", base, err)
	}
	if old.Def != Def {
		return "", "", fmt.Errorf("%s was written with def %s, not %s: not compared", path, old.Def, Def)
	}
	diff := changes(old.Numbers, c.Numbers)
	if diff == "" {
		return path, "unchanged", nil
	}
	why, err := since(db, old.Event, slices.Concat(old.Items, old.Children, c.Items, c.Children))
	if err != nil {
		return "", "", err
	}
	line, _ := redact.Redact(fmt.Sprintf("revised %s: %s (%s)\n", now.UTC().Format("2006-01-02"), diff, why))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return "", "", err
	}
	if _, err := f.WriteString(line); err != nil {
		f.Close()
		return "", "", err
	}
	if err := f.Close(); err != nil {
		return "", "", err
	}
	// the line goes first: a crash before the numbers are saved repeats it on the next write, it never drops it
	return path, "revised", save()
}

func changes(a, b Numbers) string {
	var ss []string
	for _, f := range []struct {
		name string
		a, b int
	}{{"code", a.Code, b.Code}, {"research", a.Research, b.Research}, {"falcon", a.Falcon, b.Falcon},
		{"other", a.Other, b.Other}, {"revoked", a.Revoked, b.Revoked}, {"defects", a.Defects, b.Defects},
		{"escapes", a.Escapes, b.Escapes}, {"child revoked", a.ChildRevoked, b.ChildRevoked}} {
		if f.a != f.b {
			ss = append(ss, fmt.Sprintf("%s %d -> %d", f.name, f.a, f.b))
		}
	}
	if a.MaxPct != b.MaxPct {
		pct := func(v float64) string {
			if v < 0 {
				return "none"
			}
			return fmt.Sprintf("%.0f%%", v)
		}
		ss = append(ss, fmt.Sprintf("Max %s -> %s", pct(a.MaxPct), pct(b.MaxPct)))
	}
	return strings.Join(ss, ", ")
}

// since names the record events (defect, revoke, correction) after event id that touch one of items (outcomes and
// their child tasks).
func since(db *sql.DB, id int64, items []string) (string, error) {
	rows, err := db.Query("select kind, coalesce(item, ''), coalesce(refs, '') from event where id > ? and kind in"+
		" ('defect', 'revoked', 'correction') order by id", id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ss []string
	for rows.Next() {
		var kind, item, refs string
		if err := rows.Scan(&kind, &item, &refs); err != nil {
			return "", err
		}
		var r map[string][]string
		if err := json.Unmarshal([]byte(refs), &r); err != nil {
			return "", fmt.Errorf("a %s event has bad refs %q: %w", kind, refs, err)
		}
		if !slices.ContainsFunc(r["item"], func(id string) bool { return slices.Contains(items, id) }) {
			continue
		}
		switch kind {
		case "revoked":
			ss = append(ss, fmt.Sprintf("%s revoked %s", item, strings.Join(r["defect"], ",")))
		case "defect":
			ss = append(ss, fmt.Sprintf("%s on %s", strings.Join(r["defect"], ","), item))
		case "correction":
			ss = append(ss, fmt.Sprintf("%s on %s", strings.Join(r["correction"], ","), item))
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ss) == 0 {
		return "no record event names these outcomes: late or re-ingested data", nil
	}
	return strings.Join(ss, "; "), nil
}
