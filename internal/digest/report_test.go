package digest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/ingest"
)

var now = time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)

func block(updated, needs string, ms ...string) string {
	return "# Status\n\n## GOAL (" + updated + ")\nGoal: ship the thing\n" + strings.Join(ms, "\n") +
		"\nDone: 50% (1 of 2 milestones)\nNext: the second part\nNeeds owner: " + needs + "\n"
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
	line(t, db, now.Add(-time.Hour), "crius:lead", "info", "abc1234 checked PASS")
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
	r, err := Build(db, leads, prev, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# Goal report · 2026-10-17 09:00 · status partial\n! 1 lead status file cannot be read\n",
		"## Needs the owner\n▶ 1. axi:leader: sandbox order needs the owner's tenant OK\n",
		"axi:leader: 50% claimed · verified 1 of 1 done (2 milestones) · no change since the last report",
		"crius:build: 50% claimed · verified 0 of 1 done (2 milestones) · +10 since the last report",
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
	r, err := Build(db, []ingest.Lead{{Name: "crius:build", StatusFile: statusFile(t, dir, "c.md", block("2026-10-17 08:30", "Nothing", "- [ ] one"))}}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Text, "· status partial\n! no ingest run recorded\n") || !strings.Contains(r.Text, "## Needs the owner\nNothing needs the owner.\n") ||
		!strings.Contains(r.Text, "crius:build: 50% claimed · verified 0 of 0 done (1 milestones) · first report") {
		t.Fatal(r.Text)
	}
	if r, _ := Build(db, nil, nil, now); !strings.Contains(r.Text, "No leads in projects.toml (leads = { role = \"file\" }).") {
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
	r, err := Build(db, leads, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(r.Text, "\n")
	if n > 60 || !strings.Contains(r.Text, "p19:lead: 50% claimed") || !strings.Contains(r.Text, "need 19") ||
		!strings.Contains(r.Text, "(goal and next lines of 14 leads cut for length; see their status files)") {
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
	r, err := Build(db, leads, nil, now)
	if err != nil || r.Status != "complete" || !strings.Contains(r.Text, "Nothing needs the owner.") {
		t.Fatal(err, r.Text)
	}
	leads = append(leads, ingest.Lead{Name: "b:lead", StatusFile: filepath.Join(dir, "gone.md")},
		ingest.Lead{Name: "c:lead", StatusFile: statusFile(t, dir, "c.md", "no block\n")})
	r, err = Build(db, leads, Snapshot{"b:lead": {Done: 40, Verified: 2}, "c:lead": {Done: 10}}, now)
	if err != nil || r.Status != "partial" || !strings.Contains(r.Text, "· status partial\n! 1 lead status file cannot be read\n") {
		t.Fatal(err, r.Text)
	}
	if r.Snap["b:lead"] != (LeadSnap{Done: 40, Verified: 2}) || r.Snap["c:lead"] != (LeadSnap{Done: 10}) {
		t.Fatalf("%+v", r.Snap)
	}
}
