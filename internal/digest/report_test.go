package digest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/recall"
	"github.com/orzazade/crius/internal/testutil"
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
	line(t, db, since.Add(4*time.Hour), "axi:leader", "blocked", "env004 is down, tests wait")
	verified(t, db, since.Add(150*time.Minute), "crius:lead", "crius", "F-1 · sent to no lead")
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
			"Verified by Crius or QA: 3\n" +
			"  crius:lead -> crius:build " + since.Add(time.Hour).Format("15:04") + " b3a87cb · 31/34\n" +
			"  axi:qa-api -> axi:leader " + since.Add(2*time.Hour).Format("15:04") + " axi#7 · on env004\n" +
			"  crius:lead -> crius " + since.Add(150*time.Minute).Format("15:04") + " F-1 · sent to no lead\n" +
			"Blocked: 1\n  axi:leader " + since.Add(4*time.Hour).Format("15:04") + " env004 is down, tests wait\n" +
			"Decisions: 4\n  crius:lead 00:05 decision 1\n  crius:lead 00:06 decision 2\n  crius:lead 00:07 decision 3\n  +1 more\n" +
			"Open waits (last 24 h): 1\n  " + now.Add(-30*time.Minute).Format("15:04") + ` wait idle_prompt "falcon" sid=3b660534` + "\n",
		"\n## Quota and health\nQuota: 7d 54% on day 3 of 7, 1.9x the time passed · runs out ~" +
			q.Runout().Local().Format("Mon 01-02 15:04") + " at this pace\n" +
			"Quota partial: account unknown for $1424 in 319 sessions (not measured) (+1 more: crius-rec quota)\n" +
			"Health: partial, 4 problems · lines not read (malformed): none\n  no heartbeat (open hb: no such file)\n  no backup in b\n  p3\n  +1 more: crius-rec health\n",
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
		reset.Format("Mon 01-02 15:04") + "\nHealth: complete · lines not read (malformed): none\n", "· status complete\n"} {
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

// Review of 4b68c11: the store lists give up items before the report passes 60 lines; open waits past recall's cap
// say so; a decision saved with no DECISION bus line is listed and says so; a clipped text is redacted before the cut
// (a cut token no longer looks like one); a 0% quota lasts to the reset.
func TestBuildReviewOf4b68c11(t *testing.T) {
	db := open(t)
	dir := t.TempDir()
	var leads []ingest.Lead
	for i := 0; i < 14; i++ { // with 3 checks, 60 lines before details at 3 items: no room for the cut note
		leads = append(leads, ingest.Lead{Name: fmt.Sprintf("p%02d:lead", i),
			StatusFile: statusFile(t, dir, fmt.Sprint(i, ".md"), block("2026-10-17 08:30", fmt.Sprint("need ", i), "- [ ] one"))})
	}
	since := now.Add(-9 * time.Hour)
	for i := 1; i <= 5; i++ {
		at := since.Add(time.Duration(i) * time.Minute)
		if i <= 3 {
			verified(t, db, at, "crius:lead", "p00:lead", fmt.Sprint("F-", i, " · ok"))
		}
		line(t, db, at, "p01:lead", "blocked", fmt.Sprint("blocked ", i))
		line(t, db, at, "p01:lead", "decision", fmt.Sprintf("p01-D%d title %d", i, i))
	}
	for i := 0; i < 501; i++ {
		if _, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values (?,?,?,?,?,?)",
			now.Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z"), "waits", "wait", "session",
			fmt.Sprintf("wait idle_prompt \"p\" sid=%08d", i), fmt.Sprint("w", i)); err != nil {
			t.Fatal(err)
		}
	}
	in := input(leads, nil)
	in.Since = since
	r, err := Build(db, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r.Text, "\n"); n > 60 || !strings.Contains(r.Text, "Blocked: 5\n  p01:lead 00:01 blocked 1\n  p01:lead 00:02 blocked 2\n  +3 more\n") ||
		!strings.Contains(r.Text, "Open waits (last 24 h): 500+\n") ||
		!strings.Contains(r.Text, "(goal and next lines of 13 leads cut for length; see their status files)") {
		t.Fatalf("%d lines\n%s", n, r.Text)
	}

	db = open(t)
	id := line(t, db, since.Add(time.Minute), "p01:lead", "decision", "p01-D1 title 1")
	if _, err := db.Exec(`update event set refs = '{"decision":["p01-D1"]}' where id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into decision (id, project, ts, title) values ('p01-D1', 'p01', '2026-10-17', 'title 1')," +
		" ('p02-D9', 'p02', '2026-10-17', '" + strings.Repeat("t", 100) + "'), ('p02-D8', 'p02', '2026-10-16', 'before the last report')"); err != nil {
		t.Fatal(err)
	}
	reset := now.Add(5 * 24 * time.Hour)
	r, err = Build(db, Input{Since: since, Quota: &quota.Report{Status: "complete", Now: now, Reset: reset, Day: 3, Passed: 2.0 / 7},
		Health: &recall.Health{Status: "partial", Problems: []string{strings.Repeat("a", 95) + " ghp_" + strings.Repeat("E", 36)}}}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Decisions: 2\n  p01:lead 00:01 p01-D1 title 1\n  p02 2026-10-17 p02-D9 (no DECISION bus line) " +
		strings.Repeat("t", 64) + "…\n",
		"Quota: 7d 0% on day 3 of 7, 0.0x the time passed · lasts to the reset " + reset.Format("Mon 01-02 15:04") + "\n"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
	if strings.Contains(r.Text, "ghp_") {
		t.Errorf("a cut token reaches the report:\n%s", r.Text)
	}
}

// Review of 05fa5b5: with no list items left, a list is its count line alone, so 21 leads that each need the owner
// and two decisions saved off the bus still fit 60 lines.
func TestBuildEmptiesListsToFit(t *testing.T) {
	db := open(t)
	dir := t.TempDir()
	var leads []ingest.Lead
	for i := 0; i < 21; i++ {
		leads = append(leads, ingest.Lead{Name: fmt.Sprintf("p%02d:lead", i),
			StatusFile: statusFile(t, dir, fmt.Sprint(i, ".md"), block("2026-10-17 08:30", fmt.Sprint("need ", i), "- [ ] one"))})
	}
	if _, err := db.Exec("insert into decision (id, project, ts, title) values ('p01-D1', 'p01', '2026-10-17', 'a'), ('p01-D2', 'p01', '2026-10-17', 'b')"); err != nil {
		t.Fatal(err)
	}
	in := input(leads, nil)
	in.Since = now.Add(-9 * time.Hour)
	r, err := Build(db, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(r.Text, "\n"); n > 60 || !strings.Contains(r.Text, "\nDecisions: 2\nOpen waits (last 24 h): none\n") {
		t.Fatalf("%d lines\n%s", n, r.Text)
	}
}

// Crius 10-17 03:39: lines the ingest could not read are counted per log in the report, never dropped silently.
func TestBuildCountsMalformedLines(t *testing.T) {
	db := open(t)
	for i, row := range [][2]string{{"axi", "2026-10-17T05:00:00Z"}, {"axi", "2026-10-17T05:01:00Z"},
		{"quotes", "2026-10-17T05:02:00Z"}, {"falcon", "2026-10-16T01:00:00Z"}} { // falcon: before the last report
		if _, err := db.Exec("insert into event (ts, project, kind, source_key) values (?, ?, 'malformed', ?)",
			row[1], row[0], fmt.Sprint("k", i)); err != nil {
			t.Fatal(err)
		}
	}
	in := input(nil, nil)
	in.Since = now.Add(-9 * time.Hour)
	r, err := Build(db, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Text, "\nHealth: complete · lines not read (malformed): 3 (axi 2, quotes 1)\n") {
		t.Fatal(r.Text)
	}
	empty, err := Build(open(t), in, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty.Text, "\nHealth: complete · lines not read (malformed): none\n") {
		t.Fatal(empty.Text)
	}
}

// Crius 10-17 04:23, 04:58 and 05:34: a lead whose Done line counts rows ("rows: 33 of 130") is shown against the
// rows Crius or its QA verified, "verified rows N of claimed 33": the distinct ids of VERIFIED lines to it whose ref
// is "row <id>" (RULES 3.1), the id one token with a letter and a digit (review of 38598e4: "F-1," or "`F-1`" made a second
// row), in any case. Any other ref is no row: no row is guessed from a
// ref's shape or read from the row file (four review rounds found guesses that made a false MISMATCH). A row line
// also counts for the milestone it matches; any other line that counts for no milestone is not counted. A lead that
// counts milestones keeps the milestone form.
func TestBuildShowsVerifiedRowsWhenDoneCountsRows(t *testing.T) {
	db := open(t)
	at := now.Add(-time.Hour)
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-126 · ticks seen")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-110 · npm 120/120")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-126 · checked again: one row")
	verified(t, db, at, "quotes:qa", "quotes:lead", "row F-222 · QA of its own project")
	verified(t, db, at, "crius:lead", "quotes:lead", "row POC-47 · any form of id")
	verified(t, db, at, "crius:lead", "quotes:lead", "row INV-7731 · an inventory id")
	verified(t, db, at, "crius:lead", "quotes:lead", "row f-110 · the same row in lower case")
	verified(t, db, at, "crius:lead", "quotes:lead", "F-127 · an id without row is no row")
	verified(t, db, at, "crius:lead", "quotes:lead", "Row F-128 · nor with Row")
	verified(t, db, at, "crius:lead", "quotes:lead", "rows F-129 · nor with rows")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-1 (again) · nor more than one id")
	verified(t, db, at, "crius:lead", "quotes:lead", "row · nor no id")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-126, · nor an id with a comma")
	verified(t, db, at, "crius:lead", "quotes:lead", "row `F-126` · nor one in backticks")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-126\u00a0x · nor one with a no-break space")
	verified(t, db, at, "crius:lead", "quotes:lead", "c84d78c · a sha is no row")
	verified(t, db, at, "crius:lead", "quotes:lead", "run 7 · nor a run")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-9 · ")
	verified(t, db, at, "crius:lead", "axi:leader", "row F-1 · to another lead")
	verified(t, db, at, "crius:lead", "axi:leader", "row F-2 · a row to a lead that counts milestones")
	dir := t.TempDir()
	verified(t, db, at, "crius:lead", "quotes:lead", "poc/rig/STATUS.md · a milestone check in rows mode")
	quotes := "## GOAL (2026-10-17 04:10)\nGoal: PROOF\n- [x] Rig · poc/rig/STATUS.md\n- [~] Pricing · F-110\n" +
		"Done: 25% (rows: 33 of 130 in M1-BUILD.md; grep -c)\nNext: F-94\nNeeds owner: nothing\n"
	leads := []ingest.Lead{
		{Name: "quotes:lead", StatusFile: statusFile(t, dir, "q.md", quotes)},
		{Name: "axi:leader", StatusFile: statusFile(t, dir, "a.md", block("2026-10-17 08:00", "nothing", "- [x] inventory · F-2x"))},
	}
	// The claim is read only in the RULES 3.1 form (Crius 10-17 04:58): the Done text starts "rows: X of Y", then ends,
	// or goes on with " in <row file>" and/or "; <command>", and holds no other "N of M". Anything else is not read
	// (-1), never taken from another count (reviews of dfb79ba, f3dc6be, 0f05a4e and 79a5655).
	for how, want := range map[string]int{
		"rows: 33 of 130 in poc/ui-app/M1-BUILD.md; `/usr/bin/grep -c '^- \\[x\\] F-'`": 33, "rows: 33 of 130": 33,
		"Rows: 0 of 130 in M1-BUILD.md": 0, "rows: 3 of 42 in docs/build/ROWS.md; python3 count.py": 3, "rows: 5 of 9; make count": 5,
		// not the form
		"33 of 130 M1 rows ticked": -1, "rows 33 of 130": -1, "2 of 5 seed rows": -1, "M1 rows: 33 of 130 ticked": -1, "M1 rows: 33 of 130": -1,
		"rows: 2 of 5 milestones done": -1, "rows: 1 of 2 of 3": -1, "1 of 2 of 3 rows": -1, "rows: 007 of 130": -1,
		"rows: 33 of 130 in M1-BUILD.md; 2 of 5 milestones": -1, "milestones 2 of 5 and rows not started": -1,
		"Milestones: 2 of 5 and rows pending": -1, "rows 2 of 5 milestones done": -1, "phase 2 of 3 M1 rows": -1,
		"rows: 1,033 of 1,300": -1, "rows: 99999999999999999999 of 99999999999999999999": -1,
		"3 of 6 milestones and 40 rows left": -1, "1.033 of 1300 rows": -1, "1\u202f033 of 1\u202f300 rows": -1,
		"rows: 033 of 130": -1, "rows ticked: 33 of 130 in M1-BUILD.md; 1 of 4 blocked rows re-opened": -1,
		"rows: 3 of 42 in ROWS.md; count.py --top 5 of 10": -1, "rows: 1 033 of 1 300": -1, "rows: 1, 234 of 2000": -1, "rows: 1\u02bc234 of 2000": -1,
		"Rows ticked: 33 of 130": -1, "rows in phase 2: 33 of 130": -1,
		"M1 rows ticked with evidence: 33 of 130 in poc/ui-app/M1-BUILD.md, `/usr/bin/grep -c '^- \\[x\\] F-'`; Crius has verified F-110, F-112 since": -1,
		"3 of 42 offer type x operation rows PROVEN with a run id of the same operation; PARTIAL 15, NONE 24, n/a 3":                                   -1,
		"5 of 8 required steps": -1, "6 of 8 required steps, equal weight; 7 and 8 only if needed": -1, "rows, 1 of": -1,
		"1,033 of 1,300 rows": -1, "2 of 5 milestones done but rows not started": -1, "rows come next. Milestones: 2 of 5": -1,
	} {
		if n, ok := (&Goal{DoneHow: how}).Rows(); ok != (want >= 0) || ok && n != want {
			t.Errorf("%q: %d %v, want %d", how, n, ok, want)
		}
	}
	r, err := Build(db, input(leads, Snapshot{"quotes:lead": {Done: 25, Verified: 1}}), now) // a milestone count
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"quotes:lead: 25% claimed · verified rows 5 of claimed 33, milestones 1 of 1 · verified now counts rows since the last report · VERIFIED lines not counted: 11\n",
		"axi:leader: 50% claimed · verified 0 of 1 done (1 milestones) · first report · VERIFIED lines not counted: 2\n",
	} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
	if r.Snap["quotes:lead"] != (LeadSnap{Done: 25, Verified: 5, Rows: true}) {
		t.Fatalf("%+v", r.Snap)
	}
	if r, err = Build(db, input(leads, r.Snap), now); err != nil || !strings.Contains(r.Text,
		"quotes:lead: 25% claimed · verified rows 5 of claimed 33, milestones 1 of 1 · no change since the last report") {
		t.Fatal(err, r.Text)
	}
}

// Crius 10-17 04:41: more verified rows than claimed is never quiet: a verified row was un-ticked or the claim shrank,
// a withdrawn acceptance (RULES 2.3).
func TestBuildSaysMoreVerifiedRowsThanClaimed(t *testing.T) {
	db := open(t)
	for _, f := range []string{"F-1", "F-2", "F-3"} {
		verified(t, db, now.Add(-time.Hour), "crius:lead", "quotes:lead", "row "+f+" · ok")
		verified(t, db, now.Add(-time.Hour), "crius:lead", "axi:lead", "row "+f+" · ok")
	}
	q := "## GOAL (2026-10-17 04:10)\nGoal: PROOF\n- [x] Rig · run 7\nDone: 2% (rows: 2 of 130 in M1-BUILD.md)\nNext: F-94\nNeeds owner: nothing\n"
	dir := t.TempDir()
	r, err := Build(db, input([]ingest.Lead{{Name: "quotes:lead", StatusFile: statusFile(t, dir, "q.md", q)},
		{Name: "axi:lead", StatusFile: statusFile(t, dir, "a.md", strings.Replace(q, "2 of 130", "3 of 130", 1))}}, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if want := "quotes:lead: 2% claimed · verified rows 3 of claimed 2, milestones 0 of 1 · MISMATCH: 1 verified row more than " +
		"claimed (a verified row was un-ticked or the claim shrank) · first report\n"; !strings.Contains(r.Text, want) {
		t.Fatalf("missing %q in\n%s", want, r.Text)
	}
	if want := "axi:lead: 2% claimed · verified rows 3 of claimed 3, milestones 0 of 1 · first report\n"; !strings.Contains(r.Text, want) {
		t.Fatalf("missing %q in\n%s", want, r.Text)
	}
}

// Review of 178deae: a Done line that names rows but whose claim is not read ("rows: N of M", "N of M rows") is said,
// not quietly counted as milestones.
func TestBuildSaysARowsClaimItCannotRead(t *testing.T) {
	db := open(t)
	q := "## GOAL (2026-10-17 04:10)\nGoal: PROOF\n- [x] Rig · run 7\nDone: 25% (1,033 of 1,300 rows)\nNext: F-94\nNeeds owner: nothing\n"
	r, err := Build(db, input([]ingest.Lead{{Name: "quotes:lead", StatusFile: statusFile(t, t.TempDir(), "q.md", q)}}, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	if want := "quotes:lead: 25% claimed · verified 0 of 1 done (1 milestones) · the Done line names rows but its claim " +
		"was not read: write \"rows: X of Y in <row file>; <command>\" (RULES 3.1) and no other \"N of M\" · first report\n"; !strings.Contains(r.Text, want) {
		t.Fatalf("missing %q in\n%s", want, r.Text)
	}
}

// Crius 10-17 07:17 (P6 b): the quota line answers "how much is left and who burns it" alone: the 5h share with its
// reset and the top project on Max; each is left out when not known.
func TestBuildQuotaLineHasTheFiveHourShareAndTheTopProject(t *testing.T) {
	db := open(t)
	reset, five := now.Add(5*24*time.Hour), now.Add(90*time.Minute)
	q := &quota.Report{Status: "complete", Now: now, Reset: reset, SevenPct: 54, Day: 3, Passed: 2.0 / 7, FivePct: 33,
		FiveReset: five, MaxUSD: 100, Shares: []quota.Share{{Project: "crius", USD: 32}, {Project: "axi", USD: 24}}}
	r, err := Build(db, Input{Quota: q, Health: &recall.Health{Status: "complete"}}, now)
	if err != nil || !strings.Contains(r.Text, " at this pace · 5h 33% (resets "+five.Format("15:04")+") · top crius 32%\n") {
		t.Fatal(err, r.Text)
	}
	q.FiveReset, q.Shares = time.Time{}, nil
	r, err = Build(db, Input{Quota: q, Health: &recall.Health{Status: "complete"}}, now)
	if err != nil || !strings.Contains(r.Text, " at this pace\n") {
		t.Fatal(err, r.Text)
	}
}

// Crius 10-19 18:00: a lead commits its status file somewhere other than the checkout projects.toml pointed at
// (crius:build on origin/main, the main checkout on another branch with an old copy). A lead named by a git object
// is read from that object, never from the working tree; an object that cannot be read is an ERROR, not LOST.
func TestBuildReadsALeadFromItsCommittedObject(t *testing.T) {
	db := open(t)
	repo := testutil.GitRepo(t, filepath.Join(t.TempDir(), "repo"))
	testutil.Write(t, filepath.Join(repo, "docs", "RESUME.md"), block("2026-10-17 08:30", "nothing", "- [x] step 4 · def5678", "- [ ] step 5"))
	testutil.CommitAll(t, repo)
	testutil.Write(t, filepath.Join(repo, "docs", "RESUME.md"), "old copy, no block\n") // the checkout's stale copy
	leads := []ingest.Lead{
		{Name: "crius:build", Repo: repo, StatusFile: "main:docs/RESUME.md"},
		{Name: "crius:lead", Repo: repo, StatusFile: "main:docs/GONE.md"},
	}
	r, err := Build(db, input(leads, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"crius:build: 50% claimed · verified 0 of 1 done (2 milestones)",
		"crius:lead: ERROR: " + repo + " main:docs/GONE.md: ", "! 1 lead status file cannot be read"} {
		if !strings.Contains(r.Text, want) {
			t.Errorf("missing %q in\n%s", want, r.Text)
		}
	}
}

// busLine stores a Crius bus line as the ingest does (source key bus:...).
func busLine(t *testing.T, db *sql.DB, ts time.Time, sender, kind string) {
	t.Helper()
	id := line(t, db, ts, sender, kind, "x")
	if _, err := db.Exec("update event set source_key = ? where id = ?", fmt.Sprint("bus:f:", id), id); err != nil {
		t.Fatal(err)
	}
}

// Crius GO 10-19 18:25: a lead's status file is STALE when the lead has a DONE or CLEAR-READY on the Crius bus newer
// than its GOAL stamp. An INFO or NEED is no milestone, so it never makes the file stale; a team log line is not the bus.
func TestBuildMarksAStaleGoal(t *testing.T) {
	stamp := time.Date(2026, 10, 17, 8, 30, 0, 0, time.Local)
	for _, c := range []struct {
		name  string
		lines func(db *sql.DB)
		stale string
	}{
		{"done newer", func(db *sql.DB) { busLine(t, db, stamp.Add(10*time.Minute), "crius:build", "done") },
			"crius:build: STALE: GOAL (2026-10-17 08:30) is older than its DONE 2026-10-17 08:40 on the bus · 50% claimed"},
		{"clear-ready newer", func(db *sql.DB) { busLine(t, db, stamp.Add(time.Minute), "crius:build", "clear-ready") },
			"crius:build: STALE: GOAL (2026-10-17 08:30) is older than its CLEAR-READY 2026-10-17 08:31 on the bus · 50% claimed"},
		{"same minute", func(db *sql.DB) { busLine(t, db, stamp, "crius:build", "done") }, ""},
		{"done older", func(db *sql.DB) { busLine(t, db, stamp.Add(-time.Minute), "crius:build", "done") }, ""},
		{"info newer", func(db *sql.DB) {
			busLine(t, db, stamp.Add(time.Hour), "crius:build", "info")
			busLine(t, db, stamp.Add(time.Hour), "crius:build", "need")
		}, ""},
		{"other lead", func(db *sql.DB) {
			busLine(t, db, stamp.Add(time.Hour), "crius:lead", "done")
			busLine(t, db, stamp.Add(time.Hour), "axi:build", "done")
		}, ""},
		{"team log", func(db *sql.DB) { line(t, db, stamp.Add(time.Hour), "crius:build", "done", "x") }, ""},
	} {
		db := open(t)
		c.lines(db)
		dir := t.TempDir()
		leads := []ingest.Lead{{Name: "crius:build", StatusFile: statusFile(t, dir, "c.md", block("2026-10-17 08:30", "nothing", "- [ ] one"))}}
		r, err := Build(db, input(leads, nil), now)
		if err != nil {
			t.Fatal(err)
		}
		if c.stale == "" && strings.Contains(r.Text, "STALE") || c.stale != "" && !strings.Contains(r.Text, c.stale+" · ") {
			t.Errorf("%s: want %q in\n%s", c.name, c.stale, r.Text)
		}
	}
}

// Review of 4364094 (P3): a git object from projects.toml that starts with '-' is an object name, never a git option
// (--batch made git print its usage into the report), and git's error is one bounded line of the report.
func TestBuildPassesTheGitObjectAsAnObject(t *testing.T) {
	db := open(t)
	repo := testutil.GitRepo(t, filepath.Join(t.TempDir(), "repo"))
	testutil.Write(t, filepath.Join(repo, "R.md"), "x\n")
	testutil.CommitAll(t, repo)
	r, err := Build(db, input([]ingest.Lead{{Name: "a:lead", Repo: repo, StatusFile: "--batch=x:R.md"}}, nil), now)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(r.Text, "a:lead: ERROR: ")
	if i < 0 {
		t.Fatal(r.Text)
	}
	l, _, _ := strings.Cut(r.Text[i:], "\n")
	if !strings.Contains(l, "invalid object name") || len(l) > 400 || strings.Contains(r.Text, "usage:") {
		t.Fatalf("%q\n%s", l, r.Text)
	}
}

// Review of 4364094 (P2): in a repeated hour (DST fall-back) a GOAL stamp has two readings; a DONE is newer only
// when it is newer than the later one, so a GOAL written in the second hour is not called stale.
func TestStaleUsesTheLaterReadingOfARepeatedHour(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = ny
	t.Cleanup(func() { time.Local = old })
	g, err := ParseGoal(block("2026-11-01 01:30", "nothing", "- [ ] one")) // 05:30Z (EDT) or 06:30Z (EST)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		done  string
		stale bool
	}{{"2026-11-01T06:20:00Z", false}, {"2026-11-01T06:30:00Z", false}, {"2026-11-01T06:31:00Z", true}} {
		db := open(t)
		at, _ := time.Parse(time.RFC3339, c.done)
		busLine(t, db, at, "crius:build", "done")
		s, err := staleness(db, "crius:build", g)
		if err != nil || (s != "") != c.stale {
			t.Errorf("DONE %s: %q %v", c.done, s, err)
		}
	}
}
