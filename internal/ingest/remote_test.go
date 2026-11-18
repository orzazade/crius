package ingest

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/testutil"
)

// farProject has a pause share; its bus log here is a copy of a bus on another machine (farCopy).
const farProject = `
[projects.far]
persona = "work"
accounts = ["max"]
cwd = ["/work/far"]
pause_at = 70
`

const farPulled = "2026-10-16T09:00 INFO far:lead -> crius — the last pulled line\n"

// farCopy makes far's bus log a copy: the pulled log and its <log>.remote marker.
func farCopy(t *testing.T, home string) string {
	t.Helper()
	log := filepath.Join(home, "bus", "far.log")
	testutil.Write(t, log, farPulled)
	testutil.Write(t, log+".remote", "use /opt/wrap/bus-post-far (live log is on another machine)\n")
	return log
}

// A queued line for a bus that is a copy can never be delivered from here: it is not retried every run (each run partial,
// so no heartbeat and a false "ingest heartbeat old" alert). Crius is told once on its own bus, naming the line and the
// wrapper, the row is done and the run stays complete.
func TestAQueuedLineForACopiedBusIsSaidToCriusOnceAndTheRunIsComplete(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	log := farCopy(t, x.home)
	db, err := sql.Open("sqlite", filepath.Join(x.home, "state", "record.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`insert into alert (key, ts, text, line_type, log, recipient) values ('quota:pause:far:x', ?, ?, 'PAUSE',
		'far', 'far:all')`, utcNow(), "quota: stop now")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if st := x.run(); st.Status != "complete" {
			t.Fatalf("run %d: status %s, warnings %q", run, st.Status, st.Warnings)
		}
	}
	if got := testutil.Read(t, log); got != farPulled {
		t.Errorf("the copy was written:\n%s", got)
	}
	said := grepAlerts(x.alerts(), "not delivered")
	if len(said) != 1 || !strings.Contains(said[0], " ALERT crius:ingest -> crius:lead — ") ||
		!strings.Contains(said[0], "PAUSE to far:all") || !strings.Contains(said[0], "/opt/wrap/bus-post-far") ||
		!strings.Contains(said[0], "quota: stop now") {
		t.Fatalf("want one ALERT to Crius naming the PAUSE, its text and the wrapper, got %q", x.alerts())
	}
	t.Logf("crius.log: %s", said[0])
	if rows := x.q("select count(*) from alert where sent is null"); rows[0][0] != "0" {
		t.Errorf("rows still pending: %v", rows)
	}
}

// The quota guard's NEED never says a PAUSE was posted on a copied bus: it says none was, and names the wrapper; the copy
// is not written.
func TestTheQuotaGuardSaysAPauseOnACopiedBusWasNotPosted(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: farProject})
	log := farCopy(t, x.home)
	reset := qNow.Add(4 * 24 * time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 71, reset))
	for run := 1; run <= 2; run++ {
		if st := x.run(); st.Status != "complete" {
			t.Fatalf("run %d: status %s, warnings %q", run, st.Status, st.Warnings)
		}
	}
	if got := testutil.Read(t, log); got != farPulled {
		t.Errorf("the copy was written:\n%s", got)
	}
	need := grepAlerts(x.alerts(), " NEED crius:ingest -> crius:lead — ")
	if len(need) != 1 || !strings.Contains(need[0], "far") || strings.Contains(need[0], "PAUSE posted") ||
		!strings.Contains(need[0], "not posted") || !strings.Contains(need[0], "/opt/wrap/bus-post-far") {
		t.Fatalf("want one NEED saying far's PAUSE was not posted and naming the wrapper, got %q", x.alerts())
	}
	t.Logf("crius.log: %s", need[0])
}

// With several Max accounts the same holds for the PAUSE to a role on a full account and for the one to <project>:all
// when every account is full.
func TestTheQuotaGuardWithAccountsSaysAPauseOnACopiedBusWasNotPosted(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: strings.Replace(farProject, "pause_at = 70", "pause_at = 90", 1)})
	log := farCopy(t, x.home)
	d1, d2 := filepath.Join(x.home, "cfg1"), filepath.Join(x.home, "cfg2")
	l1, l2 := x.signIn(d1, "u-1"), x.signIn(d2, "u-2")
	testutil.Write(t, filepath.Join(x.home, "accounts.toml"), "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+
		d1+"\"]\n[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+d2+"\"]\n")
	r1, r2 := qNow.Add(4*24*time.Hour), qNow.Add(24*time.Hour)
	s1, s2 := "aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"
	x.session("work", s1, "/work/far", "[crius account=max project=far role=p1]", qNow.Add(-20*time.Minute))
	x.session("work", s2, "/work/far", "[crius account=max project=far role=p2]", qNow.Add(-20*time.Minute))
	on := func(l quota.Line, login, dir string) quota.Line {
		l.Login, l.Dir, l.Persona = login, dir, "work"
		return l
	}
	x.spool(on(sampleAt(s1, qNow.Add(-10*time.Minute), 91, r1), l1, d1), on(sampleAt(s2, qNow.Add(-8*time.Minute), 40, r2), l2, d2))
	x.run()
	x.spool(on(sampleAt(s2, qNow.Add(-5*time.Minute), 92, r2), l2, d2))
	for run := 1; run <= 2; run++ {
		if st := x.run(); st.Status != "complete" {
			t.Fatalf("run %d: status %s, warnings %q", run, st.Status, st.Warnings)
		}
	}
	if got := testutil.Read(t, log); got != farPulled {
		t.Errorf("the copy was written:\n%s", got)
	}
	need := grepAlerts(x.alerts(), " NEED crius:ingest -> crius:lead — ")
	var role, all bool
	for _, n := range need {
		if strings.Contains(n, "PAUSE posted") || strings.Contains(n, "not delivered") {
			t.Errorf("a NEED says a PAUSE went out: %q", n)
		}
		role = role || strings.Contains(n, "PAUSE not posted") && strings.Contains(n, "pause far:p1 there") &&
			strings.Contains(n, "/opt/wrap/bus-post-far")
		all = all || strings.Contains(n, "every Max account") && strings.Contains(n, "pause far:all there")
	}
	if !role || !all {
		t.Fatalf("want a NEED for far:p1 and one for far:all, each saying not posted: %q", x.alerts())
	}
}
