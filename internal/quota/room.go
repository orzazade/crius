package quota

import (
	"fmt"
	"math"
	"time"
)

// StaleAfter is how long after a sample a request may run and still count as measured by it (11-21: every role moved to
// T3 threads, which run no statusline, and Room kept reading a 6 h old share as room while those threads spent it). It is
// Gap, the age at which ingest calls the sampler stale.
const StaleAfter = Gap

// Room is the verdict for starting a session on the Max account (owner's rule, Crius 10-16): "room", "full" or
// "unknown", with a one-line reason that carries the numbers. A sample within StaleAfter has room until a share reaches
// 100%. An older one is unknown, never room, when a request that may be on its login ran over StaleAfter after it
// (Unmeasured: spent unmeasured, by any amount; 11-21: a 6 h old 12% read as room). An older one of an idle login has room
// while both shares are under 90%, since other machines may have spent it a little. 7d at 100% is full at
// any age (it cannot drop within a week); a 5h window that has reset counts 0 (Build sets FivePct so). The age is
// that of the older of the two shares' own measurements (SevenAt, FiveAt), never a heartbeat's or another line's
// (reviews of 47842ed and 27cdaa1). With no 5h window reported this epoch the 7d share decides alone (live 11-08: the
// statusline has sent none since 10-31, so "unknown" put every Appxite launch on aitch's fallback account).
func Room(r *Report) (verdict, reason string) {
	switch {
	case !r.Ended.IsZero():
		return "unknown", "quota week ended " + day(r.Ended) + ", no sample since"
	case r.Reset.IsZero():
		return "unknown", "no quota sample yet"
	}
	five := fmt.Sprintf("5h %.0f%%", math.Floor(r.FivePct))
	if !r.FiveKnown {
		five = "5h none reported"
	}
	nums := fmt.Sprintf("7d %.0f%%, %s, reset %s", math.Floor(r.SevenPct), five, day(r.Reset))
	age := r.Now.Sub(r.MeasuredAt())
	if !r.SevenAt.IsZero() {
		nums += ", sample " + ago(age) + " old"
	}
	switch {
	case r.SevenPct >= 100 || r.FivePct >= 100: // full at any age, measured or not (review of d25f4f2)
		return "full", nums
	case r.SevenAt.IsZero():
		return "unknown", nums + " (top share not measured)"
	case r.SevenAt.After(r.Now) || !r.FiveReset.IsZero() && r.FiveAt.After(r.Now): // each, not only the older one
		return "unknown", nums + " (sample ahead of the clock)"
	case age <= StaleAfter:
		return "room", nums
	case r.unmeasured():
		return "unknown", nums + " (in use since, unmeasured)" // under 100 runes (TestRoom)
	case r.SevenPct < 90 && r.FivePct < 90:
		return "room", nums + " (under 90%)"
	}
	return "unknown", nums + " (90% or more)"
}

// Unmeasured says whether the shares are over StaleAfter old and a request that may be on the login ran over StaleAfter
// after their measurement, so they may have grown by any amount since (SpentAt); since is that measurement.
func (r *Report) Unmeasured() (since time.Time, ok bool) {
	m := r.MeasuredAt()
	if m.IsZero() { // a 5h share no line measured: the 7d one's age
		m = r.SevenAt
	}
	return m, !m.IsZero() && r.Now.Sub(m) > StaleAfter && r.SpentAt.Sub(m) > StaleAfter
}

func (r *Report) unmeasured() bool { _, ok := r.Unmeasured(); return ok }

// MeasuredAt is when the shares Room uses were measured: the older of SevenAt and, while its window runs, FiveAt (a
// zero FiveAt is oldest of all).
func (r *Report) MeasuredAt() time.Time {
	if !r.FiveReset.IsZero() && r.FiveAt.Before(r.SevenAt) {
		return r.FiveAt
	}
	return r.SevenAt
}

func day(t time.Time) string { return t.Local().Format("02 Jan 15:04") }

func ago(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%d min", int(d.Minutes()))
	}
	return fmt.Sprintf("%d h", int(d.Hours()))
}

// Account is the report as one login sees it: that login's figures in place of the mix of every login's. Nil when no
// line measured the login.
func (r *Report) Account(login string) *Report {
	for _, a := range r.Accounts {
		if a.Login == login {
			v := *r
			v.SevenPct, v.FivePct, v.SevenAt, v.FiveAt = a.SevenPct, a.FivePct, a.SevenAt, a.FiveAt
			v.Reset, v.FiveReset, v.Ended, v.FiveKnown = a.Reset, a.FiveReset, a.Ended, a.FiveKnown
			v.SpentAt = r.spentAny
			if t := r.spentBy[login]; t.After(v.SpentAt) {
				v.SpentAt = t
			}
			return &v
		}
	}
	return nil
}
