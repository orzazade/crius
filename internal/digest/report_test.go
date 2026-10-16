package digest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/recall"
)

var now = time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)

func block(updated, needs string, ms ...string) string {
	return "# Status\n\n## GOAL (" + updated + ")\nGoal: ship the thing\n" + strings.Join(ms, "\n") +
		"\nDone: 50% (1 of 2 milestones)\nNext: the second part\nNeeds owner: " + needs + "\n"
}

// input is a report's input with a fresh quota sample and healthy sources, so a test sees only what it sets.
func input(leads []ingest.Lead, prev Snapshot) Input {
	return Input{Leads: leads, Prev: prev, Quota: &quota.Report{Status: "complete"}, Health: &recall.Health{Status: "complete"}}
}

func statusFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Step 5 P4 (Crius 02:29): what needs the owner on top, first marked; per lead the goal, claimed % against what was
// verified, the change since the last report and the next milestone; LOST, unreadable and unreadable-file leads
// are loud lines of their own, never blank.
func TestBuildReportsEveryLeadLoudly(t *testing.T) {
	db := open(t)
	if _, err := db.Exec("insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		"2026-10-17T06:58:00Z", now.Add(-2*time.Minute).UTC().Format(time.RFC3339), "complete", "{}"); err != nil {
		t.Fatal(err)
	}
	verified(t, db, now.Add(-time.Hour), "crius:lead", "axi:leader", "abc1234 · tests 9 ok")
	verified(t, db, now.Add(-time.Hour), "crius:lead", "crius:build", "F-9 · a ref no milestone has")
	dir := t.TempDir()
	leads := []ingest.Lead{
		{Name: "axi:leader", StatusFile: statusFile(t, dir, "axi.md", block("2026-10-17 08:00", "sandbox order needs the owner's tenant OK",
			"- [x] inventory · abc1234", "- [ ] migration"))},
		{Name: "crius:build", StatusFile: statusFile(t, dir, "crius.md", block("2026-10-17 08:30", "nothing",
			"- [x] step 4 · def5678", "- [~] step 5 · run 7"))},
		{Name: "falcon:lead", StatusFile: statusFile(t, dir, "falcon.md", "# Falcon\n\n**Goal:** prose only\n")},
		{Name: "quotes:leader", StatusFile: statusFile(t, dir, "quotes.md", "## GOAL (updated 2026-10-16)\nGoal: x\n")},
		{Name: "trading:lead", StatusFile: filepath.Join(dir, "gone.md")},
	}
	prev := Snapshot{"crius:build": {Done: 40, Verified: 0}, "axi:leader": {Done: 50, Verified: 1}}
	r, err := Build(db, input(leads, prev), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Goal report · 2026-10-17 09:00 · status partial\n! 1 lead status file cannot be read\n",
		"## Needs the owner\n▶ 1. axi:leader: sandbox order needs the owner's tenant OK\n",
		"axi:leader: 50% claimed · verified 1 of 1 done (2 milestones) · no change since the last report",
		"crius:build: 50% claimed · verified 0 of 1 done (2 milestones) · +10 since the last report · VERIFIED lines not counted: 1\n",
		"  Goal: ship the thing\n  Next: the second part\n",
		"falcon:lead: LOST: " + filepath.Join(dir, "falcon.md") + " has no GOAL block",
		"quotes:leader: UNREADABLE: " + filepath.Join(dir, "quotes.md") + ": GOAL block unreadable: line 1: heading needs (YYYY-MM-DD HH:MM)",
		"trading:lead: ERROR: open " + filepath.Join(dir, "gone.md") + ": no such file or directory",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "crius:build: nothing") || r.Snap["crius:build"] != (LeadSnap{Done: 50, Verified: 0}) ||
		r.Snap["axi:leader"] != (LeadSnap{Done: 50, Verified: 1}) {
		t.Fatalf("%+v", r.Snap)
	}
}

// Nothing needed is said, not left blank; a store that is not fresh says why in the header.
func TestBuildSaysNothingAndStaleness(t *testing.T) {
	db := open(t) // no ingest run recorded
	dir := t.TempDir()
	r, err := Build(db, input([]ingest.Lead{{Name: "crius:build", StatusFile: statusFile(t, dir, "c.md", block("2026-10-17 08:30", "Nothing", "- [ ] one"))}}, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Text, "· status partial\n! no ingest run recorded\n") || !strings.Contains(r.Text, "## Needs the owner\nNothing needs the owner.\n") ||
		!strings.Contains(r.Text, "crius:build: 50% claimed · verified 0 of 0 done (1 milestones) · first report") {
		t.Fatal(r.Text)
	}
	if r, _ := Build(db, input(nil, nil), now); !strings.Contains(r.Text, "No leads in projects.toml (leads = { role = \"file\" }).") {
		t.Fatal(r.Text)
	}
}

// At most 60 lines (Crius 02:29): every lead keeps its own line and every owner need stays; detail lines go first,
// with a note saying so.
func TestBuildKeepsSixtyLines(t *testing.T) {
	db := open(t)
	dir := t.TempDir()
	var leads []ingest.Lead
	for i := 0; i < 20; i++ {
		leads = append(leads, ingest.Lead{Name: fmt.Sprintf("p%02d:lead", i),
			StatusFile: statusFile(t, dir, fmt.Sprint(i, ".md"), block("2026-10-17 08:30", fmt.Sprint("need ", i), "- [ ] one"))})
	}
	r, err := Build(db, input(leads, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(r.Text, "\n")
	if n > 60 || !strings.Contains(r.Text, "p19:lead: 50% claimed") || !strings.Contains(r.Text, "need 19") ||
		!strings.Contains(r.Text, "(goal and next lines of 19 leads cut for length; see their status files)") {
		t.Fatalf("%d lines\n%s", n, r.Text)
	}
}

// Review of cbd8517: "nothing" in quotes (the RULES template copied) is nothing; a file that cannot be read makes
// the report partial so the schedule sees it; a lead that cannot be read keeps its last numbers for the next report.
func TestBuildReviewOfCbd8517(t *testing.T) {
	db := open(t)
	if _, err := db.Exec("insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		"2026-10-17T06:58:00Z", now.Add(-2*time.Minute).UTC().Format(time.RFC3339), "complete", "{}"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	leads := []ingest.Lead{{Name: "a:lead", StatusFile: statusFile(t, dir, "a.md", block("2026-10-17 08:30", `"nothing"`, "- [ ] one"))}}
	r, err := Build(db, input(leads, nil), now)
	if err != nil || r.Status != "complete" || !strings.Contains(r.Text, "Nothing needs the owner.") {
		t.Fatal(err, r.Text)
	}
	leads = append(leads, ingest.Lead{Name: "b:lead", StatusFile: filepath.Join(dir, "gone.md")},
		ingest.Lead{Name: "c:lead", StatusFile: statusFile(t, dir, "c.md", "no block\n")})
	r, err = Build(db, input(leads, Snapshot{"b:lead": {Done: 40, Verified: 2}, "c:lead": {Done: 10}}), now)
	if err != nil || r.Status != "partial" || !strings.Contains(r.Text, "· status partial\n! 1 lead status file cannot be read\n") {
		t.Fatal(err, r.Text)
	}
	if r.Snap["b:lead"] != (LeadSnap{Done: 40, Verified: 2}) || r.Snap["c:lead"] != (LeadSnap{Done: 10}) {
		t.Fatalf("%+v", r.Snap)
	}
}

// Step 5 P4b (design §8): after the leads, what the store saw since the last report (checks that passed, blocked
// lines, decisions; open waits), then quota pace and health, each list at most 3 items. A stamp line says when each
// source was last read. A partial quota or health makes the report partial.
func TestBuildAddsTheStoreSections(t *testing.T) {
	db := open(t)
	if _, err := db.Exec("insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		"2026-10-17T04:58:00Z", now.Add(-2*time.Minute).UTC().Format(time.RFC3339), "complete", "{}"); err != nil {
		t.Fatal(err)
	}
	since := now.Add(-9 * time.Hour)
	verified(t, db, since.Add(-time.Minute), "crius:lead", "crius:build", "0aa0000 · before the last report")
	verified(t, db, since.Add(time.Hour), "crius:lead", "crius:build", "b3a87cb · 31/34")
	verified(t, db, since.Add(2*time.Hour), "axi:qa-api", "axi:leader", "axi#7 · on env004")
	verified(t, db, since.Add(3*time.Hour), "crius:build", "crius:build", "2cc2222 · the builder's own claim")
	verified(t, db, since.Add(4*time.Hour), "axi:leader", "crius", "env004 is down, tests wait") // a line to crius
	if _, err := db.Exec("update event set kind = 'blocked' where text = 'env004 is down, tests wait'"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		line(t, db, since.Add(time.Duration(4+i)*time.Minute), "crius:lead", "decision", fmt.Sprint("decision ", i))
	}
	if _, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values (?,?,?,?,?,?)",
		now.Add(-30*time.Minute).UTC().Format("2006-01-02T15:04:05Z"), "falcon", "wait", "session",
		`wait idle_prompt "falcon" sid=3b660534`, "w1"); err != nil {
		t.Fatal(err)
	}
	reset := now.Add(5 * 24 * time.Hour)
	q := &quota.Report{Status: "partial", Reasons: []string{"account unknown for $1424 in 319 sessions (not measured)", "x"},
		Now: now, Reset: reset, SevenPct: 54, Day: 3, Passed: 2.0 / 7}
	h := &recall.Health{Status: "partial", Problems: []string{"no heartbeat (open hb: no such file)", "no backup in b", "p3", "p4"},
		Sources: []recall.SourceHealth{{Name: "bus", Newest: "2026-10-17T04:41:17Z"}, {Name: "git", Newest: "2026-10-17T04:40:00Z"}, {Name: "prices"}}}
	r, err := Build(db, Input{Since: since, Quota: q, Health: h}, now)
	if err != nil {
		t.Fatal(err)
	}
	bus := time.Date(2026, 10, 17, 4, 41, 0, 0, time.UTC).Local().Format("01-02 15:04")
	git := time.Date(2026, 10, 17, 4, 40, 0, 0, time.UTC).Local().Format("01-02 15:04")
	for _, want := range []string{
		"# Goal report · 2026-10-17 09:00 · status partial\nsources read: bus " + bus + " · git " + git + " · prices never\n",
		"\n## Since the last report (2026-10-17 00:00)\n" +
			"Verified by Crius or QA: 2\n" +
			"  crius:lead -> crius:build " + since.Add(time.Hour).Format("15:04") + " b3a87cb · 31/34\n" +
			"  axi:qa-api -> axi:leader " + since.Add(2*time.Hour).Format("15:04") + " axi#7 · on env004\n" +
			"Blocked: 1\n  axi:leader " + since.Add(4*time.Hour).Format("15:04") + " env004 is down, tests wait\n" +
			"Decisions: 4\n  crius:lead 00:05 decision 1\n  crius:lead 00:06 decision 2\n  crius:lead 00:07 decision 3\n  +1 more\n" +
			"Open waits (last 24 h): 1\n  " + now.Add(-30*time.Minute).Format("15:04") + ` wait idle_prompt "falcon" sid=3b660534` + "\n",
		"\n## Quota and health\nQuota: 7d 54% on day 3 of 7, 1.9x the time passed · runs out ~" +
			q.Runout().Local().Format("Mon 01-02 15:04") + " at this pace\n" +
			"Quota partial: account unknown for $1424 in 319 sessions (not measured) (+1 more: crius-rec quota)\n" +
			"Health: partial, 4 problems\n  no heartbeat (open hb: no such file)\n  no backup in b\n  p3\n  +1 more: crius-rec health\n",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
	if r.Status != "partial" {
		t.Fatal(r.Status)
	}
	// No last report: the last 24 h; nothing seen is said; a pace that lasts says so; no sample is said.
	r, err = Build(db, Input{Quota: &quota.Report{Status: "complete", Now: now, Reset: reset, SevenPct: 10, Day: 3, Passed: 2.0 / 7},
		Health: &recall.Health{Status: "complete"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	// Either a partial quota or a partial health makes the report partial.
	for _, in := range []Input{{Quota: &quota.Report{Status: "partial", Reasons: []string{"x"}}, Health: &recall.Health{Status: "complete"}},
		{Quota: &quota.Report{Status: "complete"}, Health: &recall.Health{Status: "partial"}}} {
		if r, err := Build(db, in, now); err != nil || r.Status != "partial" || !strings.Contains(r.Text, "· status partial\n") {
			t.Fatal(err, r.Text)
		}
	}
	if got := clip(strings.Repeat("a", 200), 110); got != strings.Repeat("a", 109)+"…" {
		t.Fatal(got)
	}
	for _, want := range []string{"## Since the last report (none; the last 24 h)\n", "\nsources read: none\n", "Quota: 7d 10% on day 3 of 7, 0.4x the time passed · lasts to the reset " +
		reset.Format("Mon 01-02 15:04") + "\nHealth: complete\n", "· status complete\n"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
	r, err = Build(db, Input{Quota: &quota.Report{Status: "complete", Now: now, Reset: reset, SevenPct: 100, Day: 3, Passed: 2.0 / 7},
		Health: &recall.Health{Status: "complete"}}, now)
	if err != nil || !strings.Contains(r.Text, "Quota: 7d used up (100%) · resets "+reset.Format("Mon 01-02 15:04")+"\n") {
		t.Fatal(err, r.Text)
	}
	db2 := open(t)
	r, err = Build(db2, Input{Quota: &quota.Report{Status: "partial", Reasons: []string{"no quota sample"}, Now: now},
		Health: &recall.Health{Status: "complete"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Verified by Crius or QA: none\nBlocked: none\nDecisions: none\nOpen waits (last 24 h): none\n",
		"Quota: no sample this epoch\nQuota partial: no quota sample\n"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
}
