package ingest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

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

func (r *runner) quotaChecks(tx *sql.Tx) error {
	p, err := r.cfg.quotaProjects(r.home)
	if err != nil {
		// Without the accounts no check runs, the pause included: that wakes Crius, once a day.
		r.warn(true, "quota checks: %v", err)
		return r.queueLine(tx, "quota:off:"+clock().UTC().Format("2006-01-02"), "NEED", "crius", "crius:lead",
			fmt.Sprintf("quota guard is off, no pause or quota alert runs: %v", err))
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
	if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) {
		return nil
	}
	if rep.SevenPct >= 100 {
		key, err := window(tx, "quota:usedup:", rep.Reset)
		if err != nil {
			return err
		}
		return r.queueAlert(tx, key, rep.UsedUpText(r.cfg.enterpriseOK()))
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
		if err := r.queueAlert(tx, runout, rep.RunoutText(r.cfg.enterpriseOK())); err != nil {
			return err
		}
		_, err := tx.Exec("insert or ignore into alert (key, ts, text, sent) values (?,?,?,?)", pace, utcNow(),
			"said in "+runout, utcNow())
		return err
	case ro:
		return r.queueAlert(tx, runout, rep.RunoutText(r.cfg.enterpriseOK()))
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
		for _, st := range rep.Stale {
			if err := r.queueLine(tx, "quota:blind:"+p.slug+":"+st, "NEED", "crius", "crius:lead", fmt.Sprintf(
				"quota guard for %s is blind: the Max 7d share is stale (%s), so it cannot tell whether %s reached its "+
					"%d%% pause; check the statusline sampler", p.slug, st, p.slug, p.pauseAt)); err != nil {
				return err
			}
		}
		if len(rep.Accounts) > 0 {
			if err := r.pauseAccounts(tx, rep, as, p); err != nil {
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
		if err := r.queueLine(tx, key, "PAUSE", p.slug, p.slug+":all", fmt.Sprintf(
			"quota: Max 7d at %g%% (%s) reached %s's pause at %d%%: stop all %s work now; Crius says when to resume "+
				"(the window resets %s)", rep.SevenPct, at, p.slug, p.pauseAt, p.slug, resets)); err != nil {
			return err
		}
		if err := r.queueLine(tx, key+":crius", "NEED", "crius", "crius:lead", fmt.Sprintf(
			"quota guard: Max 7d at %g%% (%s) reached %s's pause at %d%%; PAUSE posted on %s.log. Park %s in "+
				"~/.crius/parked and stop its tabs", rep.SevenPct, at, p.slug, p.pauseAt, p.slug, p.slug)); err != nil {
			return err
		}
	}
	return nil
}

// pauseAccounts is the guard with several Max accounts (owner 10-23): each login has its own window, and one at the
// project's pause_at pauses only the project's tabs on it, each by its role (a tab runs on the login of its newest
// statusline line within quota.Gap). Crius gets one NEED per account and window naming the roles, the tabs with no role
// (no bus line reaches them) and the accounts with room. Every account at the limit pauses the whole team. Resuming
// stays Crius's word, per account.
func (r *runner) pauseAccounts(tx *sql.Tx, rep *quota.Report, as *quota.Accounts, p project) error {
	tabs, err := tabsByLogin(tx, p.slug, rep.Now)
	if err != nil {
		return err
	}
	var full []quota.AcctUse
	var room []string
	seen := map[string]bool{}
	for _, a := range rep.Accounts {
		seen[a.Login] = true
		switch {
		case !a.Ended.IsZero():
			room = append(room, a.Name+" new window")
		case a.Reset.IsZero() || !a.Reset.After(rep.Now) || a.SevenPct < float64(p.pauseAt):
			room = append(room, fmt.Sprintf("%s %g%%", a.Name, a.SevenPct))
		default:
			full = append(full, a)
		}
	}
	if as != nil {
		for _, a := range as.Rows {
			if a.Plan == "max" && a.Login != "" && !seen[a.Login] {
				room = append(room, a.Name+" not measured")
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
		var roles []string
		noRole := 0
		for _, role := range tabs[a.Login] {
			if role == "unknown" {
				noRole++
				continue
			}
			if n := len(roles); n > 0 && roles[n-1] == role {
				continue // two tabs of one role: one PAUSE reaches both
			}
			roles = append(roles, role)
			if err := r.queueLine(tx, key+":"+role, "PAUSE", p.slug, p.slug+":"+role, fmt.Sprintf(
				"quota: account %s at %g%% of its 7d (%s) reached %s's pause at %d%%: you run on it, stop now; Crius "+
					"says when to resume or moves you to an account with room (it resets %s)", a.Name, a.SevenPct, at,
				p.slug, p.pauseAt, resets)); err != nil {
				return err
			}
		}
		paused := "no tab of " + p.slug + " has a role on it"
		if len(roles) > 0 {
			paused = "PAUSE posted to " + p.slug + ":" + strings.Join(roles, ", "+p.slug+":")
		}
		tabWord := "tabs"
		if noRole == 1 {
			tabWord = "tab"
		}
		if err := r.queueLine(tx, key+":crius", "NEED", "crius", "crius:lead", fmt.Sprintf(
			"quota guard: account %s at %g%% (%s) reached %s's pause at %d%%; %s; %d %s with no role on it (stop by "+
				"hand). Room: %s. Move its tabs there or park them", a.Name, a.SevenPct, at, p.slug, p.pauseAt, paused,
			noRole, tabWord, rooms)); err != nil {
			return err
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
	if err := r.queueLine(tx, key, "PAUSE", p.slug, p.slug+":all", fmt.Sprintf(
		"quota: every Max account reached %s's pause at %d%% (%s): stop all %s work now; Crius says when to resume "+
			"(the first window resets %s)", p.slug, p.pauseAt, strings.Join(shares, ", "), p.slug,
		first.Local().Format("Mon 15:04"))); err != nil {
		return err
	}
	return r.queueLine(tx, key+":crius", "NEED", "crius", "crius:lead", fmt.Sprintf(
		"quota guard: every Max account reached %s's pause at %d%% (%s); PAUSE posted to %s:all. Park %s in "+
			"~/.crius/parked and stop its tabs", p.slug, p.pauseAt, strings.Join(shares, ", "), p.slug, p.slug))
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
