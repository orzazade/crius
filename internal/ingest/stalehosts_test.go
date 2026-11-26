package ingest

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

// 11-26 05:38 and 09:22: "quota/max STALE ... max account max1: newest current-epoch sample 04:41" while max1 ran all
// the time on the Legion and the ThinkPad, whose statuslines measured it every few minutes. Their lines reach the Mac only
// as the sign-in sweep's copies (spool/hosts/, quota.HostLines), which Pick and the guards read but the stale check did
// not. A login measured on another machine within the gap is not stale; with no such line it still is.
func TestALoginMeasuredOnAnotherMachineIsNotStale(t *testing.T) {
	const max1 = "d94b4cce88d8"
	reset := qNow.Add(4 * 24 * time.Hour)
	for _, c := range []struct {
		name   string
		remote bool
		stale  int
	}{{"no remote line", false, 1}, {"legion measured it 5 min ago", true, 0}} {
		t.Run(c.name, func(t *testing.T) {
			x := quotaCase(t)
			x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
			old := sampleAt(x.sid, qNow.Add(-60*time.Minute), 20, reset)
			old.Login = max1
			other := sampleAt("other-session", qNow.Add(-5*time.Minute), 30, reset) // the persona's sampler runs
			other.Login = "aaaaaaaaaaaa"
			x.spool(old, other)
			if c.remote {
				l := sampleAt("legion-session", qNow.Add(-5*time.Minute), 21, reset)
				l.Login, l.Kind = max1, "heartbeat"
				b, err := json.Marshal(l)
				if err != nil {
					t.Fatal(err)
				}
				if err := quota.SaveHostSpool(x.home, "legion", append(b, '\n'), nil, qNow.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
			}
			x.run()
			stale := grepAlerts(x.alerts(), "quota/max STALE")
			if len(stale) != c.stale || c.stale == 1 && !strings.Contains(stale[0], "max account login "+max1+": newest current-epoch sample") {
				t.Fatalf("want %d STALE for login %s, got %q", c.stale, max1, stale)
			}
		})
	}
}
