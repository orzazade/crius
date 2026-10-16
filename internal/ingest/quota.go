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
		if err := json.Unmarshal(raw, &l); err != nil || l.TS == "" || l.Session == "" || l.Kind == "" {
			r.st.BadLines++
			r.warn(false, "bad quota line in %s at byte %d", path, pos)
			return nil
		}
		if _, err := time.Parse(time.RFC3339, l.TS); err != nil {
			r.st.BadLines++
			r.warn(false, "bad quota line in %s at byte %d", path, pos)
			return nil
		}
		rl := 0
		if l.RL {
			rl = 1
			key := fmt.Sprintf("quota:%d:%s", pos, sha1Hex(raw)[:12])
			if _, err := tx.Exec("insert or ignore into quota (source_key, ts, account, session, persona, epoch,"+
				" five_hour_pct, five_reset, seven_day_pct, sample_kind) values (?,?,'max',?,?,?,?,?,?,?)", key, l.TS,
				r.text(l.Session), r.text(l.Persona), l.SevenReset, l.FivePct, l.FiveReset, l.SevenPct,
				r.text(l.Kind)); err != nil {
				return err
			}
		}
		// Once a session showed rate_limits it is on Max: their absence before is the moment before the first
		// response, not another account.
		_, err := tx.Exec("insert into account_seen (session, persona, rl, first_ts, last_ts) values (?,?,?,?,?)"+
			" on conflict(session) do update set rl=max(rl, excluded.rl), first_ts=min(first_ts, excluded.first_ts),"+
			" last_ts=max(last_ts, excluded.last_ts)", r.text(l.Session), r.text(l.Persona), rl, l.TS, l.TS)
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
func (r *runner) quotaChecks(tx *sql.Tx) error {
	rep, err := quota.Build(tx, r.cfg.enterpriseOK(), clock())
	if err != nil {
		return err
	}
	for _, v := range rep.Violations {
		if err := r.queueAlert(tx, "account:"+v.Kind+":"+v.Session, v.Text()); err != nil {
			return err
		}
	}
	if rep.Status == "stale" {
		last := "none"
		if !rep.LastSample.IsZero() {
			last = rep.LastSample.UTC().Format(time.RFC3339)
		}
		if err := r.queueAlert(tx, "quota:stale:"+last, fmt.Sprintf("quota/max STALE: sessions are open but the"+
			" newest quota sample is %s; is crius-statusline in the statusLine setting and working?", last)); err != nil {
			return err
		}
	}
	if rep.Reset.IsZero() || !rep.Reset.After(rep.Now) {
		return nil
	}
	epoch := fmt.Sprint(rep.Reset.Unix())
	switch {
	case rep.SevenPct >= 100:
		return r.queueAlert(tx, "quota:usedup:"+epoch, rep.UsedUpText(r.cfg.enterpriseOK()))
	case rep.SevenPct >= 60 && rep.Day <= 3:
		return r.queueAlert(tx, "quota:pace:"+epoch, rep.PaceText())
	}
	return nil
}
