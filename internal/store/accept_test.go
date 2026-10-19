package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var (
	shaA = "abc1234" + strings.Repeat("0", 33)
	shaB = "def5678" + strings.Repeat("0", 33)
)

// acceptFixture: item demo#1 with two criteria, commits A (by session s-work) and B, and four bus events.
func acceptFixture(t *testing.T) (db *sql.DB, ev map[string]string) {
	t.Helper()
	_, _, db = openStore(t)
	if _, err := RecordItem(db, "demo", "price fix", "code", []string{"CI pass", "codex review"}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into git_commit (sha, repo, session) values (?, 'r', 's-work'), (?, 'r', null)",
		shaA, shaB); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into evidence (kind, ref, revision, ts, source_key) values"+
		" ('commit', ?, ?, '2026-10-19T10:00:00Z', 'git:a'), ('commit', ?, ?, '2026-10-19T10:00:00Z', 'git:b')",
		shaA, shaA, shaB, shaB); err != nil {
		t.Fatal(err)
	}
	ev = map[string]string{}
	for _, e := range []struct{ name, kind, session, refs, text string }{
		{"ciA", "done", "", `{}`, "CI green on abc1234, 253 pass"},
		{"ciB", "done", "", `{"sha":["def5678"]}`, "CI green, 253 pass"},
		{"none", "done", "", `{}`, "CI green, 1234567 pass"},
		{"revA", "verified", "s-review", `{}`, "abc1234 codex review clean P1=0"},
		{"revSelf", "verified", "s-work", `{}`, "abc1234 review clean"},
		{"doneA", "done", "s-review", `{}`, "abc1234 review clean"},
	} {
		r, err := db.Exec("insert into event (ts, project, kind, actor, session, refs, text, source_key)"+
			" values ('2026-10-19T11:00:00Z', 'crius', ?, 'lead', nullif(?, ''), ?, ?, ?)",
			e.kind, e.session, e.refs, e.text, "t:"+e.name)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := r.LastInsertId()
		ev[e.name] = fmt.Sprint("event:", n)
	}
	return db, ev
}

func acceptRows(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("select count(*) from acceptance").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func itemState(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow("select state from item where id = ?", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// The §9 test: accept refuses evidence on a different sha, and writes nothing.
func TestAcceptRefusesEvidenceOnAnotherRevision(t *testing.T) {
	db, ev := acceptFixture(t)
	for name, checks := range map[string][]string{
		"event on B":       {"1=" + ev["ciB"], "2=" + ev["ciA"]},
		"commit B":         {"1=def5678", "2=" + ev["ciA"]},
		"event no sha":     {"1=" + ev["none"], "2=" + ev["ciA"]},
		"unknown ref":      {"1=nonsense", "2=" + ev["ciA"]},
		"unknown event":    {"1=event:999", "2=" + ev["ciA"]},
		"criterion 3":      {"1=" + ev["ciA"], "2=" + ev["ciA"], "3=" + ev["ciA"]},
		"criterion 2 none": {"1=" + ev["ciA"]},
		"no N=":            {ev["ciA"], "2=" + ev["ciA"]},
	} {
		_, err := RecordAccept(db, AcceptInput{Item: "demo#1", Revision: "abc1234", Checks: checks,
			Review: ev["revA"], By: "crius"})
		if !IsRefused(err) {
			t.Errorf("%s: want refused, got %v", name, err)
		}
	}
	if n := acceptRows(t, db); n != 0 {
		t.Fatalf("%d acceptance rows after refusals", n)
	}
	if s := itemState(t, db, "demo#1"); s != "open" {
		t.Fatal(s)
	}
}

func TestAcceptReviewIsAVerifiedLineOnTheRevisionByAnotherSession(t *testing.T) {
	db, ev := acceptFixture(t)
	checks := []string{"1=" + ev["ciA"], "2=abc1234"}
	for name, in := range map[string]AcceptInput{
		"author reviews":  {Review: ev["revSelf"], By: "crius"},
		"claim, no check": {Review: ev["doneA"], By: "crius"},
		"review on B":     {Review: ev["ciB"], By: "crius"},
		"bad by":          {Review: ev["revA"], By: "lead"},
		"unknown rev":     {Review: ev["revA"], By: "crius", Revision: "fff9999"},
		"unknown item":    {Review: ev["revA"], By: "crius", Item: "demo#9"},
	} {
		if in.Revision == "" {
			in.Revision = "abc1234"
		}
		if in.Item == "" {
			in.Item = "demo#1"
		}
		in.Checks = checks
		if _, err := RecordAccept(db, in); !IsRefused(err) {
			t.Errorf("%s: want refused, got %v", name, err)
		}
	}
	if n := acceptRows(t, db); n != 0 {
		t.Fatalf("%d acceptance rows after refusals", n)
	}
}

func TestAcceptWritesRowEventAndState(t *testing.T) {
	db, ev := acceptFixture(t)
	in := AcceptInput{Item: "demo#1", Revision: "abc1234", Checks: []string{"1=" + ev["ciA"], "2=abc1234",
		"2=" + ev["revA"]}, Review: ev["revA"], By: "crius"}
	out, err := RecordAccept(db, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "demo#1") || !strings.Contains(out, "abc1234") {
		t.Fatal(out)
	}
	var rev, checks, reviewRef, by, state string
	if err := db.QueryRow("select revision, checks, review_ref, accepted_by, state from acceptance where item = 'demo#1'").
		Scan(&rev, &checks, &reviewRef, &by, &state); err != nil {
		t.Fatal(err)
	}
	if rev != shaA || reviewRef != ev["revA"] || by != "crius" || state != "accepted" {
		t.Fatal(rev, reviewRef, by, state)
	}
	var cs []AcceptCheck
	if err := json.Unmarshal([]byte(checks), &cs); err != nil || len(cs) != 3 || cs[1].Criterion != 2 ||
		cs[1].Ref != shaA {
		t.Fatal(checks, err)
	}
	var kind, refs, item string
	if err := db.QueryRow("select kind, refs, item from event where kind = 'accepted'").Scan(&kind, &refs, &item); err != nil {
		t.Fatal(err)
	}
	if item != "demo#1" || !strings.Contains(refs, `"demo#1"`) || !strings.Contains(refs, shaA) {
		t.Fatal(item, refs)
	}
	if s := itemState(t, db, "demo#1"); s != "accepted" {
		t.Fatal(s)
	}
	if _, err := RecordAccept(db, in); !IsRefused(err) {
		t.Fatalf("second accept: want refused, got %v", err)
	}
	if n := acceptRows(t, db); n != 1 {
		t.Fatal(n)
	}
}
