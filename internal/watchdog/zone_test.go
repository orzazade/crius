package watchdog

import (
	"strings"
	"testing"
	"time"
)

// Crius 10-31 09:46: the 09:00 goal report ran at 07:00. The zone was set to Riga 10-26 15:05; the agent that fires
// launchd's calendar jobs started 10-25 09:47, on the old zone, and kept it.
func TestAZoneSetAfterTheCalendarAgentStartedIsAnError(t *testing.T) {
	ps := []byte(`    0 Sun Oct 25 09:47:15 2026     /usr/libexec/UserEventAgent (System)
  502 Sun Oct 25 09:40:00 2026     /usr/libexec/UserEventAgent (Aqua)
  501 Sun Oct 25 09:47:25 2026     /usr/libexec/UserEventAgent (Aqua)
  501 Sat Oct 31 09:48:02 2026     /bin/zsh
`)
	set := time.Date(2026, 10, 26, 15, 5, 0, 0, time.Local)
	err := CalendarZone(ps, 501, set)
	if err == nil || !strings.Contains(err.Error(), "the time zone was set 2026-10-26 15:05, after UserEventAgent (Aqua) started 2026-10-25 09:47") {
		t.Fatalf("err %v", err)
	}
	// Set before the agent started (at boot): it fires on that zone.
	if err := CalendarZone(ps, 501, time.Date(2026, 10, 25, 9, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	// Review of T: another logged-in user's agent is not this user's. Set between the two starts: 502's is older than
	// the zone, 501's is not, and only 501's fires 501's jobs.
	if err := CalendarZone(ps, 501, time.Date(2026, 10, 25, 9, 45, 0, 0, time.Local)); err != nil {
		t.Fatal("read another user's agent:", err)
	}
	if err := CalendarZone(ps, 502, time.Date(2026, 10, 25, 9, 45, 0, 0, time.Local)); err == nil {
		t.Fatal("502's own old agent passed")
	}
}

// "Failed to look" is loud: no agent in ps, or a start it cannot read.
func TestNoCalendarAgentIsAnError(t *testing.T) {
	set := time.Date(2026, 10, 26, 15, 5, 0, 0, time.Local)
	if err := CalendarZone([]byte("  501 Sat Oct 31 09:48:02 2026     /bin/zsh\n"), 501, set); err == nil || !strings.Contains(err.Error(), "no /usr/libexec/UserEventAgent (Aqua) of uid 501 in ps") {
		t.Fatalf("err %v", err)
	}
	if err := CalendarZone([]byte("  501 Sun Okt 25 09:47:25 2026 /usr/libexec/UserEventAgent (Aqua)\n"), 501, set); err == nil {
		t.Fatal("an unreadable start passed")
	}
}
