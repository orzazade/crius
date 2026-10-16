package ingest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

// clock is the time the quota checks run at; tests set it.
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
		if l.RL {
			rl = 1
			if _, err := tx.Exec("insert or ignore into quota (source_key, ts, account, session, persona, epoch,"+
				" five_hour_pct, five_reset, seven_day_pct, sample_kind) values (?,?,'max',?,?,?,?,?,?,?)", key, l.TS,
				r.text(l.Session), r.text(l.Persona), l.SevenReset, l.FivePct, l.FiveReset, l.SevenPct,
				r.text(l.Kind)); err != nil {
				return err
			}
		}
		_, err = tx.Exec("insert or ignore into account_seen (source_key, session, persona, ts, rl) values (?,?,?,?,?)",
			key, r.text(l.Session), r.text(l.Persona), l.TS, rl)
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
	rep, err := quota.Build(tx, r.cfg.quotaProjects(), clock())
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
	if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) {
		return nil
	}
	epoch := fmt.Sprint(rep.Reset.Unix())
	if rep.SevenPct >= 100 {
		return r.queueAlert(tx, "quota:usedup:"+epoch, rep.UsedUpText(r.cfg.enterpriseOK()))
	}
	// Pace and runout pushes have their own keys; when both are new in one run, the runout push (which carries the
	// pace facts) goes alone and the pace key is marked as said, so the owner gets one push (design §9).
	runout, pace := "quota:runout:"+epoch, "quota:pace:"+epoch
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
