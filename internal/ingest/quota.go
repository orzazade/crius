package ingest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
)

// PickAccount is the account a session of project starts on, its folder and why: want is "max" for the Max one whose
// week resets soonest, or a Max row's name (quota.Pick); never the enterprise account (owner 11-07). The figures are the
// record store's, as crius-rec quota reads them, with each login's week read together with the other machines' spool
// lines the sign-in sweep copied (quota.HostLines): one answer per login, the sweep's (11-21: the sweep saw max3 at 96%
// on the Legion; Pick, on the Mac's store alone, called it unknown and kept max1 held). Why and the error name each
// machine's copy and its age, loudly when it is missing, old or its last read failed. crius-spawn and the playbook's
// claude -p calls use it.
func PickAccount(home, project, want string) (name, dir, why string, err error) {
	root, pauseAt, err := ForSpawn(home, project)
	if err != nil {
		return "", "", "", err
	}
	projects, err := QuotaProjects(home)
	if err != nil {
		return "", "", "", err
	}
	db, err := store.Open(home)
	if err != nil {
		return "", "", "", err
	}
	defer db.Close()
	now := clock()
	r, err := quota.Build(db, projects, now)
	if err != nil {
		return "", "", "", err
	}
	lines, notes, _ := quota.HostLines(home, quota.RemoteHosts, now)
	hosts := "other machines: " + strings.Join(notes, "; ")
	name, dir, why, err = quota.Pick(r.WithLines(lines, projects.Accounts), projects.Accounts, root, want, pauseAt)
	if err != nil {
		return "", "", "", fmt.Errorf("%w (%s)", err, hosts)
	}
	if why == "" {
		return name, dir, hosts, nil
	}
	return name, dir, why + "; " + hosts, nil
}

// clock is the time the quota and bus clock checks run at; tests set it.
var clock = time.Now

// ingestQuota reads new lines of spool/quota.jsonl (written by crius-statusline) into `quota` and `account_seen`,
// then runs the quota checks. A missing spool is not an error: the stale check alerts when sessions need one.
func (r *runner) ingestQuota() {
	path := filepath.Join(r.home, "spool", "quota.jsonl")
	if _, err := os.Stat(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		r.warn(true, "cannot read %s: %v", path, err)
	} else if err == nil {
		if err := r.inTx(func(tx *sql.Tx) error { return r.quotaFile(tx, path) }); err != nil {
			r.warn(true, "cannot ingest %s: %v", path, err)
		}
	}
	// Every session's account, also of sessions seen by the statusline before their transcript was read.
	if err := r.inTx(measuredAccount); err != nil {
		r.warn(true, "session accounts: %v", err)
	}
	if err := r.inTx(r.quotaChecks); err != nil {
		r.warn(true, "quota checks: %v", err)
	}
}

func (r *runner) quotaFile(tx *sql.Tx, path string) error {
	c, err := getCursor(tx, "quota", path)
	if err != nil {
		return err
	}
	next, n, err := scanLines(path, c, func(pos int64, raw []byte) error {
		var l quota.Line
		// A line with rate_limits but no seven-day share and reset measured nothing (the sampler refuses them).
		if err := json.Unmarshal(raw, &l); err != nil || l.TS == "" || l.Session == "" || l.Kind == "" ||
			l.RL && (l.SevenPct == nil || l.SevenReset == nil) {
			r.st.BadLines++
			r.warn(true, "bad quota line in %s at byte %d", path, pos) // a sample that measured nothing
			return nil
		}
		t, err := time.Parse(time.RFC3339, l.TS)
		if err != nil {
			r.st.BadLines++
			r.warn(false, "bad quota line in %s at byte %d", path, pos)
			return nil
		}
		l.TS = t.UTC().Format(time.RFC3339) // stored in one form, so ordering by text is ordering by time
		rl := 0
		key := fmt.Sprintf("quota:%d:%s", pos, sha1Hex(raw)[:12])
		var login, dir any // null when the line names none
		if l.Login != "" {
			login = r.text(l.Login)
		}
		if l.Dir != "" {
			dir = r.text(l.Dir)
		}
		if l.RL {
			rl = 1
			if _, err := tx.Exec("insert or ignore into quota (source_key, ts, account, session, persona, epoch,"+
				" five_hour_pct, five_reset, seven_day_pct, sample_kind, login) values (?,?,'max',?,?,?,?,?,?,?,?)", key,
				l.TS, r.text(l.Session), r.text(l.Persona), l.SevenReset, l.FivePct, l.FiveReset, l.SevenPct,
				r.text(l.Kind), login); err != nil {
				return err
			}
			if l.Responded != nil {
				if _, err := tx.Exec("insert or ignore into quota_response (source_key, responded) values (?,?)", key,
					*l.Responded); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec("insert or ignore into account_seen (source_key, session, persona, ts, rl, login, config_dir)"+
			" values (?,?,?,?,?,?,?)", key, r.text(l.Session), r.text(l.Persona), l.TS, rl, login, dir)
		return err
	})
	if err != nil {
		return err
	}
	if next == c {
		return nil
	}
	return saveCursor(tx, "quota", path, next, n)
}

// quotaChecks queues the alerts of design §5 and §8: the account guard (S1), a stale sampler, and the pace and
// used-up pushes. Each key is alerted once.
// newAlert: the rule holds and its key was never queued.
func (r *runner) newAlert(tx *sql.Tx, key string, holds bool) (bool, error) {
	if !holds {
		return false, nil
	}
	var n int
	err := tx.QueryRow("select count(*) from alert where key=?", key).Scan(&n)
	return n == 0, err
}

// recordRule adds a row to enterprise_rule for each project whose enterprise rule differs from its last row: from the
// epoch for a project it has none for, else from the projects.toml edit that changed it (its mtime; the run's time when
// that is not after the last row or is ahead of the clock). A project gone from projects.toml may no longer use it.
func (r *runner) recordRule(tx *sql.Tx, ok map[string]bool) error {
	type last struct {
		ok    bool
		since time.Time
	}
	before := map[string]last{}
	rows, err := tx.Query("select project, ok, since from enterprise_rule")
	if err != nil {
		return err
	}
	for rows.Next() {
		var p, since string
		var v bool
		if err := rows.Scan(&p, &v, &since); err != nil {
			rows.Close()
			return err
		}
		t, err := time.Parse(time.RFC3339, since)
		if err != nil {
			rows.Close()
			return fmt.Errorf("enterprise_rule %s since %q: %w", p, since, err)
		}
		if b, ok := before[p]; !ok || t.After(b.since) { // the newest by time, whatever the text's form
			before[p] = last{v, t}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	now := clock().UTC()
	edit := now
	if fi, err := os.Stat(filepath.Join(r.home, "projects.toml")); err != nil {
		return err
	} else if m := fi.ModTime().UTC(); m.Before(now) {
		edit = m
	}
	projects := map[string]bool{}
	for _, c := range r.cfg.projects {
		projects[c.slug] = true
	}
	for p := range before {
		projects[p] = true
	}
	for _, p := range slices.Sorted(maps.Keys(projects)) {
		b, seen := before[p]
		since := time.Unix(0, 0).UTC()
		if seen {
			if b.ok == ok[p] {
				continue
			}
			// after the last row, so it is the newest: the edit, else now, else a second past the last (a clock behind it)
			since = edit.Truncate(time.Second)
			if !since.After(b.since) {
				since = now.Truncate(time.Second)
			}
			if !since.After(b.since) {
				since = b.since.Add(time.Second)
			}
		}
		if _, err := tx.Exec("insert into enterprise_rule (project, ok, since) values (?, ?, ?)", p, ok[p], since.Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) quotaChecks(tx *sql.Tx) error {
	p, err := r.cfg.quotaProjects(r.home)
	if err != nil {
		// Without the accounts no check runs, the pause included: that wakes Crius, once a day.
		r.warn(true, "quota checks: %v", err)
		return r.queueLine(tx, "quota:off:"+clock().UTC().Format("2006-01-02"), "NEED", "crius", "crius:lead",
			fmt.Sprintf("quota guard is off, no pause or quota alert runs: %v", err))
	}
	if err := r.recordRule(tx, p.EnterpriseOK); err != nil {
		return err
	}
	rep, err := quota.Build(tx, p, clock())
	if err != nil {
		return err
	}
	for _, v := range rep.Violations {
		if err := r.queueAlert(tx, "account:"+v.Kind+":"+v.Session, v.AlertText()); err != nil {
			return err
		}
	}
	for _, st := range rep.Stale {
		if err := r.queueAlert(tx, "quota:stale:"+st, quota.StaleText(st)); err != nil {
			return err
		}
	}
	if err := r.pauseChecks(tx, rep, p.Accounts); err != nil {
		return err
	}
	if err := r.limitChecks(tx, rep, p.Accounts); err != nil {
		return err
	}
	if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) {
		return nil
	}
	if rep.SevenPct >= 100 {
		key, err := window(tx, "quota:usedup:", rep.Reset)
		if err != nil {
			return err
		}
		return r.queueAlert(tx, key, rep.UsedUpText())
	}
	// Pace and runout pushes have their own keys; when both are new in one run, the runout push (which carries the
	// pace facts) goes alone and the pace key is marked as said, so the owner gets one push (design §9).
	runout, err := window(tx, "quota:runout:", rep.Reset)
	if err != nil {
		return err
	}
	pace, err := window(tx, "quota:pace:", rep.Reset)
	if err != nil {
		return err
	}
	ro, err := r.newAlert(tx, runout, rep.RunsOut())
	if err != nil {
		return err
	}
	pc, err := r.newAlert(tx, pace, rep.PaceHigh())
	if err != nil {
		return err
	}
	switch {
	case ro && pc:
		if err := r.queueAlert(tx, runout, rep.RunoutText()); err != nil {
			return err
		}
		_, err := tx.Exec("insert or ignore into alert (key, ts, text, sent) values (?,?,?,?)", pace, utcNow(),
			"said in "+runout, utcNow())
		return err
	case ro:
		return r.queueAlert(tx, runout, rep.RunoutText())
	case pc:
		return r.queueAlert(tx, pace, rep.PaceText())
	}
	return nil
}

// window is prefix plus the weekly window that resets at reset. Samples of one window carry resets seconds apart
// (quota.avgPerDay), so a key already queued for a reset within an hour of it names the same window (review round 13).
func window(tx *sql.Tx, prefix string, reset time.Time) (string, error) {
	rows, err := tx.Query("select key from alert where substr(key, 1, ?) = ?", len(prefix), prefix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return "", err
		}
		epoch, _, _ := strings.Cut(strings.TrimPrefix(key, prefix), ":")
		if n, err := strconv.ParseInt(epoch, 10, 64); err == nil && math.Abs(float64(n-reset.Unix())) < 3600 {
			return prefix + epoch, nil
		}
	}
	return prefix + fmt.Sprint(reset.Unix()), rows.Err()
}

// pauseChecks is the quota guard (Crius 10-22 20:38; owner: Quotes pauses at 70% of the weekly quota): a project with
// pause_at gets a PAUSE on its own bus when a Max account's 7d share reaches it, once per account, limit and window:
// to each role on that account, or to <project>:all when every Max account is at the limit; and Crius one NEED, which
// wakes it (an ALERT does not). A stale sample while sessions are open means the guard cannot
// see the share: that is a NEED too, once per gap, as the stopgap script exits 1.
func (r *runner) pauseChecks(tx *sql.Tx, rep *quota.Report, as *quota.Accounts) error {
	for _, p := range r.cfg.projects {
		if p.pauseAt == 0 {
			continue
		}
		// A bus that is a copy of one on another machine takes no PAUSE from here (store.OpenBus): each NEED says so.
		copyOf, copied, err := store.RemoteCopy(filepath.Join(r.home, "bus", p.slug+".log"))
		if err != nil {
			return err
		}
		if !copied {
			copyOf = ""
		}
		for _, st := range rep.Stale {
			if projects, ok := rep.StaleProjects[st]; ok && !slices.Contains(projects, p.slug) && !slices.Contains(projects, "unknown") {
				continue // an account only other known projects run on (Crius 10-29); a session of no known project may be p's
			}
			if err := r.queueLine(tx, "quota:blind:"+p.slug+":"+st, "NEED", "crius", "crius:lead", fmt.Sprintf(
				"quota guard for %s is blind: the Max 7d share is stale (%s), so it cannot tell whether %s reached its "+
					"%d%% pause; check the statusline sampler", p.slug, st, p.slug, p.pauseAt)); err != nil {
				return err
			}
		}
		if len(rep.Accounts) > 0 {
			if err := r.pauseAccounts(tx, rep, as, p, copyOf); err != nil {
				return err
			}
			continue
		}
		if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) || rep.SevenPct < float64(p.pauseAt) {
			continue
		}
		key, err := window(tx, fmt.Sprintf("quota:pause:%s:%d:", p.slug, p.pauseAt), rep.Reset) // a raised limit pauses again
		if err != nil {
			return err
		}
		at, resets := rep.SevenAt.Local().Format("15:04"), rep.Reset.Local().Format("Mon 15:04")
		posted := "PAUSE posted on " + p.slug + ".log"
		if copyOf != "" {
			posted = notPosted(copyOf, p.slug+":all")
		} else if err := r.queueLine(tx, key, "PAUSE", p.slug, p.slug+":all", fmt.Sprintf(
			"quota: Max 7d at %g%% (%s) reached %s's pause at %d%%: stop all %s work now; Crius says when to resume "+
				"(the window resets %s)", rep.SevenPct, at, p.slug, p.pauseAt, p.slug, resets)); err != nil {
			return err
		}
		if err := r.queueLine(tx, key+":crius", "NEED", "crius", "crius:lead", fmt.Sprintf(
			"quota guard: Max 7d at %g%% (%s) reached %s's pause at %d%%; %s. Park %s in "+
				"~/.crius/parked and stop its tabs", rep.SevenPct, at, p.slug, p.pauseAt, posted, p.slug)); err != nil {
			return err
		}
	}
	return nil
}

// pauseAccounts is the guard with several Max accounts (owner 10-23): each login has its own window, and one at the
// project's pause_at pauses only the project's tabs on it, each by its role (a tab runs on the login of its newest
// statusline line within quota.Gap). Crius gets one NEED per account and window, and one more when a later run pauses a
// new role, naming the roles, the tabs with no role (no bus line reaches them) and the accounts with room. Every account at the limit pauses the whole team. Resuming
// stays Crius's word, per account.
func (r *runner) pauseAccounts(tx *sql.Tx, rep *quota.Report, as *quota.Accounts, p project, copyOf string) error {
	tabs, err := tabsByLogin(tx, p.slug, rep.Now)
	if err != nil {
		return err
	}
	var full []quota.AcctUse
	var room []string
	seen, hasRoom := map[string]bool{}, map[string]bool{}
	for _, a := range rep.Accounts {
		seen[a.Login] = true
		switch {
		case !a.Ended.IsZero():
			// A listed account whose week reset with no reading since is unknown (11-21: another machine may have spent
			// the new week); it counts as room here, as one never measured does. A login no row lists is one nobody can
			// start on.
			if r := as.Of(a.Login); r != nil && r.Plan == "max" {
				room = append(room, a.Name+" quota unknown since its week reset")
				hasRoom[a.Login] = true
			}
		case a.Reset.IsZero() || !a.Reset.After(rep.Now) || a.SevenPct < float64(p.pauseAt):
			room = append(room, fmt.Sprintf("%s %g%%", a.Name, a.SevenPct))
			hasRoom[a.Login] = true
		default:
			full = append(full, a)
		}
	}
	if as != nil {
		for _, a := range as.Rows {
			if a.Plan == "max" && a.Login != "" && !seen[a.Login] {
				room = append(room, a.Name+" not measured")
				hasRoom[a.Login] = true
			}
		}
	}
	// A role with a tab on an account with room, as the room list counts it: a PAUSE to the role would stop that tab too.
	elsewhere := map[string]string{}
	for login, roles := range tabs {
		for _, role := range roles {
			if hasRoom[login] && role != "unknown" {
				elsewhere[role] = as.Name(login)
			}
		}
	}
	rooms := "none"
	if len(room) > 0 {
		rooms = strings.Join(room, ", ")
	}
	for _, a := range full {
		key, err := window(tx, fmt.Sprintf("quota:pause:%s:%s:%d:", p.slug, a.Login, p.pauseAt), a.Reset)
		if err != nil {
			return err
		}
		at, resets := a.SevenAt.Local().Format("15:04"), a.Reset.Local().Format("Mon 15:04")
		var roles, split []string
		noRole, news := 0, false
		for _, role := range tabs[a.Login] {
			if role == "unknown" {
				noRole++
				continue
			}
			if n := len(roles); n > 0 && roles[n-1] == role {
				continue // two tabs of one role: one PAUSE reaches both
			}
			if other, ok := elsewhere[role]; ok {
				if n := len(split); n == 0 || !strings.HasPrefix(split[n-1], role+" ") {
					split = append(split, role+" also runs on "+other)
				}
				continue
			}
			roles = append(roles, role)
			if copyOf != "" {
				// No PAUSE on a copied bus: a row marked sent stands for it, so a new role is news as a posted one is.
				// Known gaps: a bus local again posts the PAUSE but its follow-up NEED key may be taken, so Crius's last
				// word stays "not posted"; a PAUSE queued while local and refused at flush follows a NEED saying "posted".
				res, err := tx.Exec(`insert or ignore into alert (key, ts, text, line_type, log, recipient, sent)
					values (?,?,?,?,?,?,?)`, key+":"+role+":copy", utcNow(), r.text(notPosted(copyOf, p.slug+":"+role)),
					"PAUSE", p.slug, p.slug+":"+role, utcNow())
				if err != nil {
					return err
				}
				n, err := res.RowsAffected()
				if err != nil {
					return err
				}
				news = news || n > 0
				continue
			}
			added, err := r.queueNew(tx, key+":"+role, "PAUSE", p.slug, p.slug+":"+role, fmt.Sprintf(
				"quota: account %s at %g%% of its 7d (%s) reached %s's pause at %d%%: you run on it, stop now; Crius "+
					"says when to resume or moves you to an account with room (it resets %s)", a.Name, a.SevenPct, at,
				p.slug, p.pauseAt, resets))
			if err != nil {
				return err
			}
			news = news || added
		}
		paused := "no tab of " + p.slug + " has a role on it"
		if len(roles) > 0 && copyOf != "" {
			paused = notPosted(copyOf, p.slug+":"+strings.Join(roles, ", "+p.slug+":"))
		} else if len(roles) > 0 {
			paused = "PAUSE posted to " + p.slug + ":" + strings.Join(roles, ", "+p.slug+":")
		} else if len(split) > 0 {
			paused = "no PAUSE posted"
		}
		if len(split) > 0 {
			paused += "; not paused, as a PAUSE would stop its tab with room too (stop its tab here by hand): " +
				strings.Join(split, ", ")
		}
		tabWord := "tabs"
		if noRole == 1 {
			tabWord = "tab"
		}
		need := fmt.Sprintf("quota guard: account %s at %g%% (%s) reached %s's pause at %d%%; %s; %d %s with no role on "+
			"it (stop by hand). Room: %s. Move its tabs there or park them", a.Name, a.SevenPct, at, p.slug, p.pauseAt,
			paused, noRole, tabWord, rooms)
		added, err := r.queueNew(tx, key+":crius", "NEED", "crius", "crius:lead", need)
		if err != nil {
			return err
		}
		// After the window's first NEED, only a new PAUSE is news (a role paused once its tab with room went quiet); its
		// key names the roles paused, among them the new one, so it is new too.
		if !added && news {
			if err := r.queueLine(tx, key+":crius:"+strings.Join(roles, ","), "NEED", "crius", "crius:lead", need); err != nil {
				return err
			}
		}
	}
	if len(full) == 0 || len(room) > 0 {
		return nil
	}
	first, shares := full[0].Reset, make([]string, len(full))
	for i, a := range full {
		shares[i] = fmt.Sprintf("%s %g%%", a.Name, a.SevenPct)
		if a.Reset.Before(first) {
			first = a.Reset
		}
	}
	key, err := window(tx, fmt.Sprintf("quota:pause:%s:all:%d:", p.slug, p.pauseAt), first)
	if err != nil {
		return err
	}
	posted := "PAUSE posted to " + p.slug + ":all"
	if copyOf != "" {
		posted = notPosted(copyOf, p.slug+":all")
	} else if err := r.queueLine(tx, key, "PAUSE", p.slug, p.slug+":all", fmt.Sprintf(
		"quota: every Max account reached %s's pause at %d%% (%s): stop all %s work now; Crius says when to resume "+
			"(the first window resets %s)", p.slug, p.pauseAt, strings.Join(shares, ", "), p.slug,
		first.Local().Format("Mon 15:04"))); err != nil {
		return err
	}
	return r.queueLine(tx, key+":crius", "NEED", "crius", "crius:lead", fmt.Sprintf(
		"quota guard: every Max account reached %s's pause at %d%% (%s); %s. Park %s in "+
			"~/.crius/parked and stop its tabs", p.slug, p.pauseAt, strings.Join(shares, ", "), posted, p.slug))
}

// limitChecks tells Crius when a Max account its sessions run on reaches quota.Limit of its week (owner 10-28 23:5x):
// one NEED per account and window, and one more when a later run finds a session on it not named before; each names
// the sessions working on it (sessionsByLogin) and the account each of their projects moves to by the rule
// (quota.Pick). Crius moves each, the same session, with crius-spawn -move at its next turn boundary; nothing is
// stopped here. Each NEED also shows, apart, the working sessions no record places, as any may be on this account (live
// 11-18 they were dropped), and those on folders accounts.toml does not list, with the folders (a Max folder not listed
// yet would hide its work). Both are mostly runs that come and go every few minutes (reviews of da1dcd3 and 85a7444), so
// they are counted, never named, never keyed and give no moves: an account at the limit whose only working sessions are
// unplaced gets one NEED per window, and unlisted ones alone get none. A login no row lists is checked too: its
// sessions spend a Max week all the same.
// Only a measured share counts (STATUS 11-17 15:09): the NEED reads and prints the highest share a measurement showed
// (SevenMeasuredPct), as a higher one on a line that measured nothing is a repeat that may be another login's window.
// Shares only rise within a window, so a measured one at the limit holds at any age. Wakewatch's weekly watch reads
// each login's week on its own and says what is unknown.
func (r *runner) limitChecks(tx *sql.Tx, rep *quota.Report, as *quota.Accounts) error {
	// Each login's week is read as PickAccount reads it, with the other machines' copies (quota.HostLines): a login at the
	// limit on another machine gets its NEED, and the move target is the one crius-spawn -move picks. A NEED made while a
	// copy is missing, old or unread names those machines; the full ages are said where the move is made.
	lines, _, blind := quota.HostLines(r.home, quota.RemoteHosts, rep.Now)
	rep = rep.WithLines(lines, as)
	hooks := map[string]sessionstate.State{}
	states, err := sessionstate.List(filepath.Join(r.home, "sessions"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.warn(true, "quota limit check: crius-hook session records: %v", err) // the others still place their sessions
	}
	for _, s := range states {
		hooks[s.SessionID] = s
	}
	open, unknown, unlisted, err := sessionsByLogin(tx, rep.Now, hooks, as)
	if err != nil {
		return err
	}
	for _, a := range rep.Accounts {
		if row := as.Of(a.Login); row != nil && row.Plan != "max" || len(open[a.Login])+len(unknown) == 0 ||
			a.Reset.IsZero() || !a.Reset.After(rep.Now) || a.SevenMeasuredPct < quota.Limit {
			continue
		}
		key, err := window(tx, "quota:limit:"+a.Login+":", a.Reset)
		if err != nil {
			return err
		}
		var names, news []string // news: full ids, as two sessions may share 8 characters
		next := map[string]string{}
		for _, s := range open[a.Login] {
			names = append(names, s.project+":"+s.role+" "+s.id[:min(8, len(s.id))])
			res, err := tx.Exec("insert or ignore into alert (key, ts, text, sent) values (?,?,?,?)", key+":"+s.id,
				utcNow(), "named in "+key, utcNow())
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n > 0 {
				news = append(news, s.id)
			}
			if _, ok := next[s.project]; ok {
				continue
			}
			to := "none (no Max room)" // short: the line is one bus line
			root, pauseAt, err := ForSpawn(r.home, s.project)
			if err == nil {
				var name string
				if name, _, _, err = quota.Pick(rep, as, root, "max", pauseAt); err == nil && name != "" {
					to = name
				}
			}
			if err != nil {
				to = "none (" + truncate(err.Error(), 40) + ")"
			}
			next[s.project] = to
		}
		need := key + ":need:" + strings.Join(news, ",")
		switch {
		case len(names) == 0:
			need = key + ":unplaced" // once per window, however many come and go
		case news == nil:
			continue
		}
		var moves []string
		for _, project := range slices.Sorted(maps.Keys(next)) {
			moves = append(moves, project+" to "+next[project])
		}
		if err := r.queueLine(tx, need, "NEED", "crius", "crius:lead", limitLine(a, moves, names, len(unknown), unlisted, blind)); err != nil {
			return err
		}
	}
	return nil
}

// limitLine is the move NEED, in one bus line (store.MaxLen runes; a longer one is cut at the end). The share, the
// first move by the rule (cut, never dropped) and the hint come first; then the sessions placed on the account, the
// count of unplaced ones, and the count of those on folders accounts.toml does not list with as many of the folders as
// fit (reviews of da1dcd3 and 85a7444: a long list cut the target and hint off). Each part has its least form reserved
// before an earlier one takes its share.
func limitLine(a quota.AcctUse, moves, names []string, unplaced int, unlisted, blind []string) string {
	head := fmt.Sprintf("quota: account %s at %g%% of its 7d (%s, resets %s) over the %d%% limit. Next by the rule: ",
		truncate(a.Name, 20), a.SevenMeasuredPct, a.SevenMeasuredAt.Local().Format("15:04"),
		a.Reset.Local().Format("Mon 15:04"), quota.Limit)
	hint := ". At a turn boundary: crius-spawn -move <project> <role>. On it: "
	movesText, on, namesMin := "no session placed to move", "no session placed", 0
	if len(names) > 0 {
		on, namesMin = "", 20
	}
	tail := ""
	if unplaced > 0 {
		tail = fmt.Sprintf("; %d working with no known account", unplaced)
	}
	var folders []string
	for _, d := range unlisted {
		if h, err := os.UserHomeDir(); err == nil && strings.HasPrefix(d, h+string(filepath.Separator)) {
			d = "~" + d[len(h):]
		}
		if !slices.Contains(folders, d) {
			folders = append(folders, d)
		}
	}
	if len(unlisted) > 0 {
		tail += fmt.Sprintf("; %d on unlisted folders", len(unlisted))
	}
	room := store.MaxLen - utf8.RuneCountInString(head+hint+on+tail)
	if len(moves) > 0 {
		movesText = fitList(moves, room-namesMin)
	}
	room -= utf8.RuneCountInString(movesText)
	if len(names) > 0 {
		on = fitList(names, room)
		room -= utf8.RuneCountInString(on)
	}
	if len(folders) > 0 && room >= 10 {
		// a folder's end names it, so a long first one keeps its end; the others fit whole or are counted
		keep := room - 3
		if len(folders) > 1 {
			keep -= utf8.RuneCountInString(fmt.Sprintf(" +%d more", len(folders)-1))
		}
		if r := []rune(folders[0]); keep > 0 && len(r) > keep {
			folders[0] = "…" + string(r[len(r)-max(1, keep-1):])
		}
		if s := " (" + fitList(folders, room-3) + ")"; utf8.RuneCountInString(s) <= room {
			tail += s
			room -= utf8.RuneCountInString(s)
		}
	}
	// Last, as room allows: the machines whose copies were missing, old or unread when the moves were picked (the
	// moves' own details are said where the move is made, crius-spawn -move).
	if s := "; blind to " + strings.Join(blind, ", "); len(blind) > 0 && utf8.RuneCountInString(s) <= room {
		tail += s
	}
	return head + movesText + hint + on + tail
}

// fitList joins items with ", " as far as they fit in room runes and counts the rest ("a, b +3 more"). The first item
// is cut ("…") rather than dropped.
func fitList(items []string, room int) string {
	out := ""
	for i, it := range items {
		more := ""
		if rest := len(items) - i - 1; rest > 0 {
			more = fmt.Sprintf(" +%d more", rest)
		}
		if i == 0 {
			if r := []rune(it); len(r)+utf8.RuneCountInString(more) > room {
				it = string(r[:max(0, min(len(r), room-utf8.RuneCountInString(more)-1))]) + "…"
			}
			out = it
			continue
		}
		s := out + ", " + it
		if utf8.RuneCountInString(s+more) > room {
			return out + fmt.Sprintf(" +%d more", len(items)-i)
		}
		out = s
	}
	return out
}

// openSession is a session open on a login: its project, role ("unknown" for none) and id.
type openSession struct{ project, role, id string }

// activeWithin: a session whose last work is older than this is idle; moving it off an account saves nothing.
const activeWithin = 24 * time.Hour

// sessionsByLogin is every project's working sessions by the login they run on, and the working sessions whose login
// is not known. A session works while its last work (lastWork) is within activeWithin; an idle tab heartbeats, so an
// open one is not a working one (STATUS 11-17 15:09: two tabs idle for four days were named). Its login and whether
// it is still open come from the newer of two records:
//   - its newest statusline line (account_seen): its login, open while the line is within quota.Gap, as tabsByLogin
//     reads it;
//   - its crius-hook record (hooks, ~/.crius/sessions/<id>.json): the login of the config folder its own process ran
//     in at its last hook event (CLAUDE_CONFIG_DIR, as the accounts file lists the folder), open until it ended.
//
// The newer wins, as a session moved to another account keeps its id (crius-spawn -move); on a tie the statusline
// line wins, as it read the login itself. Sessions with no statusline (T3 threads: 23 of 24 with requests on 11-18)
// are placed by the hook record alone. A hook record of a folder no accounts.toml row lists gives its folder in unlisted
// (the enterprise one, or a Max folder not listed yet; reviews of da1dcd3 and 85a7444): never placed, never unknown. A working
// session neither record places (no record, or a listed folder with no sign-in) is unknown: it may be on any account.
func sessionsByLogin(tx *sql.Tx, now time.Time, hooks map[string]sessionstate.State, as *quota.Accounts) (
	open map[string][]openSession, unknown []openSession, unlisted []string, err error) {
	// Sessions with a request or an answered statusline line in the window; lastWork then reads each exactly. The
	// minute of slack covers stored times with and without fractions of a second.
	since := now.Add(-activeWithin - time.Minute).UTC().Format(time.RFC3339)
	rows, err := tx.Query(`select coalesce(s.project, 'unknown'), s.role, s.id, a.login, a.ts from session s
		left join account_seen a on a.rowid = (
			select rowid from account_seen where session = s.id order by ts desc, rowid desc limit 1)
		where s.id in (select session from request where ts_last >= ?1)
			or s.id in (select q.session from quota q join quota_response r using (source_key)
				where r.responded = 1 and q.ts >= ?1)
		order by s.project, s.role, s.id`, since)
	if err != nil {
		return nil, nil, nil, err
	}
	type seen struct {
		s         openSession
		login, ts sql.NullString
	}
	var all []seen
	for rows.Next() {
		var v seen
		if err := rows.Scan(&v.s.project, &v.s.role, &v.s.id, &v.login, &v.ts); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		all = append(all, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	open = map[string][]openSession{}
	for _, v := range all {
		last, err := lastWork(tx, v.s.id)
		if err != nil {
			return nil, nil, nil, err
		}
		if last.IsZero() || now.Sub(last) > activeWithin {
			continue
		}
		var lineAt time.Time
		line := v.login.Valid && v.login.String != ""
		if line {
			if lineAt, err = time.Parse(time.RFC3339, v.ts.String); err != nil {
				return nil, nil, nil, fmt.Errorf("account_seen ts %q: %w", v.ts.String, err)
			}
		}
		h, hooked := hooks[v.s.id]
		hooked = hooked && h.Config != ""
		switch {
		case line && (!hooked || !h.Time.After(lineAt)):
			if now.Sub(lineAt) <= quota.Gap {
				open[v.login.String] = append(open[v.login.String], v.s)
			}
		case hooked && h.State == sessionstate.Ended:
			// closed: on whichever account, it spends nothing more
		case hooked && as.Dir(h.Config) == nil:
			unlisted = append(unlisted, h.Config)
		case hooked && as.Dir(h.Config).Login != "":
			login := as.Dir(h.Config).Login
			open[login] = append(open[login], v.s)
		default:
			unknown = append(unknown, v.s)
		}
	}
	return open, unknown, unlisted, nil
}

// lastWork is when the session last worked: its newest request, or its newest statusline line that came with a
// response. A line with no response is no work, whatever its kind: a resumed idle tab writes a session line, and its
// values can change with no request (review of 4c7f6bf, live: about half such lines had no request in the hour before).
// Zero when neither is known.
func lastWork(tx *sql.Tx, id string) (time.Time, error) {
	var req, line sql.NullString
	if err := tx.QueryRow(`select (select max(ts_last) from request where session = ?),
		(select max(q.ts) from quota q left join quota_response r using (source_key)
			where q.session = ? and r.responded = 1)`, id, id).Scan(&req, &line); err != nil {
		return time.Time{}, err
	}
	var last time.Time
	for _, v := range []sql.NullString{req, line} {
		if !v.Valid {
			continue
		}
		t, err := time.Parse(time.RFC3339, v.String)
		if err != nil {
			return time.Time{}, fmt.Errorf("session %s last work %q: %w", id, v.String, err)
		}
		if t.After(last) {
			last = t
		}
	}
	return last, nil
}

// tabsByLogin is the roles of the project's open tabs by the login they run on: a tab's login is that of its newest
// statusline line, and it is open while that line is within quota.Gap. One role per tab, sorted; "unknown" for a tab
// with no role.
func tabsByLogin(tx *sql.Tx, slug string, now time.Time) (map[string][]string, error) {
	rows, err := tx.Query(`select s.role, a.login, a.ts from session s join account_seen a on a.rowid = (
		select rowid from account_seen where session = s.id order by ts desc, rowid desc limit 1)
		where s.project = ? and a.login is not null and a.login != '' order by s.role, s.id`, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var role, login, ts string
		if err := rows.Scan(&role, &login, &ts); err != nil {
			return nil, err
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return nil, fmt.Errorf("account_seen ts %q: %w", ts, err)
		}
		if now.Sub(at) <= quota.Gap {
			out[login] = append(out[login], role)
		}
	}
	return out, rows.Err()
}

// notPosted is the NEED's words for a PAUSE to whom that was not queued: the bus here is a copy; use is its marker's line.
func notPosted(use, whom string) string {
	return "PAUSE not posted: its bus here is a copy (" + use + "); pause " + whom + " there"
}
