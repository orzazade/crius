package ingest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// Crius 00:18: the cost run reads sessions by account, project and role, and all three were unknown.

// The account is what the statusline saw last (rl: Max; none after a response: enterprise), also when the line
// came before the transcript, and a later transcript line does not reset it.
func TestSessionAccountFromTheStatusline(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.run()
	acc := func(sid string) string { return x.one("select account from session where id=?", sid) }
	if a := acc(x.sid); a != "unknown" {
		t.Fatal(a)
	}
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 30, qNow.Add(5*24*time.Hour)))
	x.run()
	if a := acc(x.sid); a != "max" {
		t.Fatal(a)
	}
	x.spool(quota.Line{TS: iso(qNow.Add(-5 * time.Minute)), Kind: "session", Session: x.sid, Persona: "personal"})
	x.run()
	appendFile(t, filepath.Join(x.personal, "-p", x.sid+".jsonl"),
		lines(assistant(A{Req: "late", TS: iso(qNow.Add(-4 * time.Minute)), In: 1, Out: 1})))
	x.run()
	if a := acc(x.sid); a != "enterprise" {
		t.Fatal(a)
	}
	early := "22222222-2222-3333-4444-555555555555"
	x.spool(sampleAt(early, qNow.Add(-3*time.Minute), 30, qNow.Add(5*24*time.Hour)))
	x.run()
	x.session("personal", early, "/work/demo", "", qNow.Add(-2*time.Minute))
	x.run()
	if a := acc(early); a != "max" {
		t.Fatal(a)
	}
}

// The -n title files a session with no tag: "<project>[-build]: <role>" or "<project> · <role>"; the project only
// when it is configured, else the folder's; a tag still wins.
func TestSessionRoleAndProjectFromTheTitle(t *testing.T) {
	x := newIC(t, cfg{extra: axiProject + `
[projects.staffy]
persona = "personal"
accounts = ["max"]
cwd = ["/work/staffy"]
repos = []
team_logs = []
artifact_dirs = []
`})
	at := "2026-10-16T10:00:00Z"
	for i, c := range []struct{ cwd, tag, title, project, role, attrib string }{
		{"/elsewhere", "", "staffy-build: backend-dev", "staffy", "backend-dev", "title"},
		{"/work/demo", "", "first-order · QA", "demo", "qa", "title"},
		{"/work/demo", "", "staffy: build lead", "staffy", "build-lead", "title"},
		{"/elsewhere", "", "axi", "axi", "unknown", "title"},
		{"/work/demo", "[crius account=max project=demo role=lead]", "staffy: qa", "demo", "lead", "registered"},
		{"/work/demo", "", "just some words", "demo", "unknown", "cwd"},
		{"/elsewhere", "", "", "", "unknown", "unknown"},
	} {
		sid := string(rune('a'+i)) + "1111111-2222-3333-4444-555555555555"
		text := ""
		if c.tag != "" {
			text = c.tag + "\n"
		}
		recs := []map[string]any{prompt("p-"+sid, at, text+"hello", "", c.cwd)}
		if c.title != "" {
			recs = append(recs, map[string]any{"type": "custom-title", "customTitle": c.title, "sessionId": sid})
		}
		recs = append(recs, assistant(A{Req: sid + "-r", TS: at, In: 1, Out: 1}))
		testutil.Write(t, filepath.Join(x.personal, "-p", sid+".jsonl"), lines(recs...))
		x.run()
		got := x.q("select coalesce(project,''), role, attrib_source from session where id=?", sid)
		if len(got) != 1 || got[0][0] != c.project || got[0][1] != c.role || got[0][2] != c.attrib {
			t.Errorf("%q in %s: got %v, want %s %s %s", c.title, c.cwd, got, c.project, c.role, c.attrib)
		}
	}
}

// Sessions ingested before the rules (no new lines since) are filed by them on the next run, account included.
func TestSessionFieldsFillOldSessions(t *testing.T) {
	x := quotaCase(t)
	at := qNow.Add(-time.Hour)
	testutil.Write(t, filepath.Join(x.personal, "-p", x.sid+".jsonl"), lines(prompt("p", iso(at), "hello", "", "/work/demo"),
		map[string]any{"type": "custom-title", "customTitle": "demo: scout", "sessionId": x.sid},
		assistant(A{Req: "r", TS: iso(at), In: 1, Out: 1})))
	x.spool(sampleAt(x.sid, at, 30, qNow.Add(5*24*time.Hour)))
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update session set role='unknown', attrib_source='cwd', account='unknown'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.run()
	if got := x.q("select role, attrib_source, account from session"); len(got) != 1 || got[0][0] != "scout" ||
		got[0][1] != "title" || got[0][2] != "max" {
		t.Fatal(got)
	}
}
