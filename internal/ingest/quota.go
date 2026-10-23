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
	if err := r.pauseChecks(tx, rep); err != nil {
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
// pause_at gets one PAUSE to <project>:all on its own bus when the Max 7d share reaches it in the current window, and
// Crius one NEED, which wakes it (an ALERT does not). A stale sample while sessions are open means the guard cannot
// see the share: that is a NEED too, once per gap, as the stopgap script exits 1.
func (r *runner) pauseChecks(tx *sql.Tx, rep *quota.Report) error {
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
		if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) || rep.SevenPct < float64(p.pauseAt) {
			continue
		}
		key, err := window(tx, "quota:pause:"+p.slug+":", rep.Reset)
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
