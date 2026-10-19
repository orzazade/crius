package recall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func show(t *testing.T, home, id string, history bool) *Shown {
	t.Helper()
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := Show(db, home, id, history, now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// decisions writes three decisions through record (D2 supersedes D1) and loads them as the ingest does.
func decisions(t *testing.T) string {
	t.Helper()
	db := open(t)
	home := os.Getenv("CRIUS_HOME")
	if err := os.MkdirAll(filepath.Join(home, "bus"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, in := range []store.DecisionInput{
		{Project: "demo", Title: "poll every hour", Why: "w1", DecidedBy: "owner", Today: "2026-10-14"},
		{Project: "demo", Title: "poll every 10 min", Why: "w2", DecidedBy: "owner", Supersedes: "demo-D1", Reason: "too slow",
			Today: "2026-10-15"},
		{Project: "demo", Title: "keep mail on", Why: "w3", DecidedBy: "owner", Today: "2026-10-15"},
	} {
		if _, err := store.RecordDecision(home, in); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(home, "state", "demo", "DECISIONS.md")
	entries, problems := store.ParseDecisions(testutil.Read(t, file))
	if len(entries) != 3 || len(problems) > 0 {
		t.Fatal(entries, problems)
	}
	for _, d := range entries {
		exec(t, db, "insert into decision (id, project, ts, title, decided_by, supersedes, file, line) values (?,?,?,?,?,?,?,?)",
			d.ID, d.Project, d.Date, d.Title, d.DecidedBy, d.Supersedes, file, d.Line)
	}
	return home
}

func TestShowMarksASupersededDecisionAndGivesTheChain(t *testing.T) {
	home := decisions(t)
	s := show(t, home, "demo-D1", false)
	if txt := s.Text(); !s.Found || !strings.Contains(txt, "SUPERSEDED by demo-D2 (2026-10-15): poll every 10 min") ||
		!strings.Contains(txt, "Why: w1") || s.Status != "complete" {
		t.Fatal(txt)
	}
	if txt := show(t, home, "demo-D2", false).Text(); !strings.Contains(txt, "Supersedes: demo-D1") ||
		!strings.Contains(txt, "Reason: too slow") || strings.Contains(txt, "SUPERSEDED") {
		t.Fatal(txt)
	}
	txt := show(t, home, "demo-D2", true).Text()
	i1, i2 := strings.Index(txt, "demo-D1 2026-10-14"), strings.Index(txt, "demo-D2 2026-10-15")
	if i1 < 0 || i2 < i1 || strings.Contains(txt, "demo-D3") {
		t.Fatal(txt)
	}
}

func TestShowAnItemWithChildrenAndTheLinesCitingIt(t *testing.T) {
	db := open(t)
	home := os.Getenv("CRIUS_HOME")
	exec(t, db, `insert into item (id, project, n, kind, title, criteria, opened) values ('demo#1','demo',1,'code','mail fix','["CI pass","codex review"]','2026-10-15T09:00:00Z')`)
	exec(t, db, `insert into item (id, project, n, parent, kind, title, criteria, opened) values ('demo#2','demo',2,'demo#1','code','child','["x"]','2026-10-15T09:10:00Z')`)
	event(t, db, "2026-10-15T10:00:00Z", "demo", "done", "item=demo#1 sha=abc1234 done", `{"item":["demo#1"]}`)
	event(t, db, "2026-10-15T10:00:00Z", "demo", "done", "item=demo#11 other", `{"item":["demo#11"]}`)
	txt := show(t, home, "demo#1", false).Text()
	for _, want := range []string{"Title: mail fix", "1. CI pass", "2. codex review", "demo#2 open child", "item=demo#1 sha=abc1234"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %q in\n%s", want, txt)
		}
	}
	if strings.Contains(txt, "demo#11") {
		t.Fatal(txt)
	}
}

func TestShowSessionCommitEventAndProject(t *testing.T) {
	db := open(t)
	home := os.Getenv("CRIUS_HOME")
	exec(t, db, "insert into session (id, project, persona, account, title, started) values ('7dce2bdf-7b14-464f-a18a-c263010a0a8c','crius','personal','max','build','2026-10-16T08:00:00Z')")
	exec(t, db, "insert into request (request_id, session, cost_list_usd) values ('r1','7dce2bdf-7b14-464f-a18a-c263010a0a8c',1.5)")
	exec(t, db, "insert into git_commit (sha, repo, project, ts, files) values ('cf62176aaaa','/r/falcon','falcon','2026-10-15T22:40:00Z','[\"a.ts\"]')")
	id := event(t, db, "2026-10-15T10:00:00Z", "demo", "info", "hello", "")
	for q, want := range map[string]string{"7dce2bdf": "Account: max", "cf62176": "Files: [\"a.ts\"]",
		"event:" + fmt.Sprint(id): "Text: hello"} {
		if s := show(t, home, q, false); !s.Found || !strings.Contains(s.Text(), want) {
			t.Fatalf("%s: %s", q, s.Text())
		}
	}
	if s := show(t, home, "7dce2bdf", false); !strings.Contains(s.Text(), "Requests: 1 · $1.50 list") {
		t.Fatal(s.Text())
	}
	if s := show(t, home, "demo", false); s.Status != "unavailable" || !strings.Contains(s.Text(), "STATE.md") {
		t.Fatal(s.Text())
	}
	testutil.Write(t, filepath.Join(home, "state", "demo", "STATE.md"), "# demo — state\nall good\n")
	if s := show(t, home, "demo", false); s.Status != "complete" || !strings.Contains(s.Text(), "all good") {
		t.Fatal(s.Text())
	}
}

func TestShowNotFoundNamesWhereItLookedAndABadIDIsRefused(t *testing.T) {
	db := open(t)
	home := os.Getenv("CRIUS_HOME")
	if s := show(t, home, "demo#9", false); s.Found || !strings.Contains(s.Text(), "no match in items (id demo#9)") {
		t.Fatal(s.Text())
	}
	if _, err := Show(db, home, "Hello World", false, now); !errors.Is(err, ErrQuery) {
		t.Fatal(err)
	}
	if _, err := Show(db, home, "event:1", true, now); !errors.Is(err, ErrQuery) {
		t.Fatal("--history covers decisions and items only", err)
	}
}

// Review 626e532: when two decisions supersede one, the first (lowest number) is its successor, whatever the row order.
func TestTwoSupersedesPickTheFirst(t *testing.T) {
	home := decisions(t)
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec(t, db, "update decision set supersedes = 'demo-D1' where id = 'demo-D3'")
	by, err := supersededBy(context.Background(), db)
	if err != nil || by["demo-D1"] != "demo-D2" {
		t.Fatal(by, err)
	}
}

// Review 5d5fee4: a project known only by its commits or artifacts is a project (its STATE.md is not built yet).
func TestShowAProjectKnownByCommitsOnly(t *testing.T) {
	db := open(t)
	exec(t, db, "insert into git_commit (sha, project) values (?, 'infra')", strings.Repeat("a", 40))
	s, err := Show(db, os.Getenv("CRIUS_HOME"), "infra", false, now)
	if err != nil || s.Status != "unavailable" {
		t.Fatal(s, err)
	}
}

// §9: show ITEM --history lists the chain in order: open, accept, defect, revoke and the fix item, its acceptance,
// and a correction.
func TestShowItemHistoryListsTheChain(t *testing.T) {
	db := open(t)
	home := os.Getenv("CRIUS_HOME")
	a, b := "abc1234"+strings.Repeat("0", 33), "def5678"+strings.Repeat("0", 33)
	if _, err := store.RecordItem(db, "demo", "mail fix", "code", []string{"CI pass"}, "", false); err != nil {
		t.Fatal(err)
	}
	exec(t, db, "insert into git_commit (sha, repo) values (?, 'r'), (?, 'r')", a, b)
	ci := event(t, db, "2026-10-15T10:00:00Z", "demo", "done", "CI green on abc1234", "")
	rev := event(t, db, "2026-10-15T10:01:00Z", "demo", "verified", "abc1234 review clean", "")
	ciB := event(t, db, "2026-10-15T11:00:00Z", "demo", "done", "CI green on def5678", "")
	revB := event(t, db, "2026-10-15T11:01:00Z", "demo", "verified", "def5678 review clean", "")
	ev := func(n int64) string { return fmt.Sprint("event:", n) }
	steps := []func() (string, error){
		func() (string, error) {
			return store.RecordAccept(db, store.AcceptInput{Item: "demo#1", Revision: "abc1234",
				Checks: []string{"1=" + ev(ci)}, Review: ev(rev), By: "crius"})
		},
		func() (string, error) {
			return store.RecordDefect(db, store.DefectInput{Item: "demo#1", Title: "mail sent twice", FoundBy: "owner",
				Severity: "S2", Class: "missing test"})
		},
		func() (string, error) {
			return store.RecordRevoke(db, store.RevokeInput{Item: "demo#1", Defect: "D-1", Reason: "sends twice",
				By: "crius", Criteria: []string{"CI pass"}})
		},
		func() (string, error) {
			return store.RecordAccept(db, store.AcceptInput{Item: "demo#2", Revision: "def5678",
				Checks: []string{"1=" + ev(ciB)}, Review: ev(revB), By: "owner"})
		},
		func() (string, error) {
			return store.RecordCorrect(db, store.CorrectInput{Target: "demo#1", Field: "title", Value: "mail dedupe",
				Reason: "named the cause", By: "crius"})
		},
	}
	for i, f := range steps {
		if out, err := f(); err != nil {
			t.Fatal(i, out, err)
		}
	}
	s := show(t, home, "demo#1", true)
	text := s.Text()
	at := 0
	for _, want := range []string{"opened demo#1", "accepted demo#1 on abc1234", "defect D-1 on demo#1",
		"revoked demo#1", "opened fix demo#2", "accepted demo#2 on def5678", "correction C-1"} {
		i := strings.Index(text[at:], want)
		if i < 0 {
			t.Fatalf("%q not after offset %d in:\n%s", want, at, text)
		}
		at += i + len(want)
	}
	if !strings.Contains(text, "Title: mail dedupe") || strings.Contains(text, "Title: mail fix") {
		t.Fatal("the view applies the correction:\n" + text)
	}
	if s := show(t, home, "demo#1", false); !strings.Contains(s.Text(), "revoked on abc123400000") {
		t.Fatal(s.Text())
	}
}

// Codex review of d4d9f61: find reads items as corrected.
func TestFindReadsCorrectedItems(t *testing.T) {
	db := open(t)
	if _, err := store.RecordItem(db, "demo", "mail fix", "code", []string{"c"}, "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordCorrect(db, store.CorrectInput{Target: "demo#1", Field: "project", Value: "axi",
		Reason: "r", By: "crius"}); err != nil {
		t.Fatal(err)
	}
	if r := find(t, db, Query{Kinds: []string{"item"}, Project: "axi"}); len(r.Hits) != 1 {
		t.Fatal(r)
	}
}
