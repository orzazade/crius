package quota

import (
	"fmt"
	"math"
	"time"
)

// Room is the verdict for starting a session on the Max account (owner's rule, Crius 10-16): "room", "full" or
// "unknown", with a one-line reason that carries the numbers. A sample within Gap has room until a share reaches
// 100%; an older one only while both shares are under 90%, since they may have grown since. 7d at 100% is full at
// any age (it cannot drop within a week); a 5h window that has reset counts 0 (Build sets FivePct so). The age is
// that of the older of the two shares' own measurements (SevenAt, FiveAt), never a heartbeat's or another line's
// (reviews of 47842ed and 27cdaa1).
func Room(r *Report) (verdict, reason string) {
	switch {
	case !r.Ended.IsZero():
		return "unknown", "quota week ended " + day(r.Ended) + ", no sample since"
	case r.SevenAt.IsZero():
		return "unknown", "no quota sample yet"
	}
	age := r.Now.Sub(r.MeasuredAt())
	nums := fmt.Sprintf("7d %.0f%%, 5h %.0f%%, reset %s, sample %s old", math.Floor(r.SevenPct), math.Floor(r.FivePct),
		day(r.Reset), ago(age))
	switch {
	case r.SevenPct >= 100 || r.FivePct >= 100:
		return "full", nums
	case r.SevenAt.After(r.Now) || !r.FiveReset.IsZero() && r.FiveAt.After(r.Now): // each, not only the older one
		return "unknown", nums + " (sample ahead of the clock)"
	case !r.FiveKnown:
		return "unknown", nums + " (no 5h window sampled)"
	case age <= Gap:
		return "room", nums
	case r.SevenPct < 90 && r.FivePct < 90:
		return "room", nums + " (under 90%)"
	}
	return "unknown", nums + " (90% or more)"
}

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
