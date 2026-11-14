package playbook

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// scripted is the model for the made-up day: it keeps what the rules picked and
// writes one entry per finding, naming the existing entry for the repeat.
type scripted struct{ picks, writes int }

func (m *scripted) Pick(_ context.Context, cs []Candidate) ([]Verdict, error) {
	m.picks++
	var out []Verdict
	for _, c := range cs {
		out = append(out, Verdict{ID: c.ID, Keep: true, Topic: "testing"})
	}
	return out, nil
}

func (m *scripted) Write(_ context.Context, cs []Candidate, _ []Entry) ([]Op, error) {
	m.writes++
	var ops []Op
	for _, c := range cs {
		switch {
		case strings.Contains(c.Text, "heavy cap"):
			ops = append(ops, Op{Candidate: c.ID, Entry: Entry{ID: "split-shop-api-tests", Topic: "testing", Kind: "tactic",
				When: "Shop.Api.Tests runs past the 1200 s heavy cap", Do: "run it in 3 filtered parts", Because: "13m18s for Orders alone",
				Keys: []string{"dotnet", "shop"}}})
		case strings.Contains(c.Text, "TypeORM"):
			ops = append(ops, Op{Candidate: c.ID, Entry: Entry{ID: "typeorm-02-one-transaction", Topic: "databases", Kind: "tool-fact",
				When: "TypeORM 0.2 migrations hang", Do: "set transaction: each", Because: "600 s hang to 41 s",
				Keys: []string{"typeorm"}}})
		case strings.Contains(c.Text, "wake a T3 thread"):
			ops = append(ops, Op{Candidate: c.ID, SameAs: "bus-does-not-wake-t3-thread", Note: "shop lead idle 40 min"})
		}
	}
	return ops, nil
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A made-up day with three findings, one of them a repeat of an existing entry:
// the run adds two entries and adds evidence to the third.
func TestMadeUpDayAddsTwoEntriesAndEvidenceToTheRepeat(t *testing.T) {
	m := &scripted{}
	cfg, res := runDay(t, m)
	if m.picks != 1 || m.writes != 1 || res.Calls.Pick != 1 || res.Calls.Write != 1 {
		t.Fatalf("model calls: pick=%d write=%d (counted %+v), want one of each", m.picks, m.writes, res.Calls)
	}
	if b, _ := LoadBook(cfg.BookDir); b.Find("typeorm-02-one-transaction") == nil {
		t.Fatal("typeorm entry missing")
	}
	// A second run over the same day finds nothing new: the cursor moved.
	res2, err := Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Candidates) != 0 || m.picks != 1 {
		t.Fatalf("second run picked %d candidates (model picks %d), want 0 and no model call", len(res2.Candidates), m.picks)
	}
}

// The same day through the real models (one haiku and one sonnet call on the
// Max login). Run with PLAYBOOK_LIVE=1.
func TestMadeUpDayLive(t *testing.T) {
	if os.Getenv("PLAYBOOK_LIVE") != "1" {
		t.Skip("PLAYBOOK_LIVE=1 runs claude -p")
	}
	cfg, res := runDay(t, ClaudeCLI{})
	b, _ := LoadBook(cfg.BookDir)
	for _, e := range b.Entries {
		t.Logf("%s [%s/%s] when %s; do %s; because %s; evidence %d", e.ID, e.Topic, e.Kind, e.When, e.Do, e.Because, len(e.Evidence))
	}
	t.Logf("calls %+v dropped %v", res.Calls, res.Dropped)
}

// runDay runs the made-up day and checks the outcome every model must give.
func runDay(t *testing.T, m Model) (Config, *Result) {
	t.Helper()
	cfg, bookDir := dayConfig(t)
	res, err := Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	return checkDay(t, cfg, bookDir, res)
}

// dayConfig lays out the made-up day in a temp folder.
func dayConfig(t *testing.T) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	bookDir := filepath.Join(dir, "playbook")
	if err := os.MkdirAll(bookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, "testdata/day/book.json", filepath.Join(bookDir, "book.json"))
	// The transcript was written on the made-up day.
	tx := filepath.Join(dir, "projects", "-Users-x-Projects-scifi-svc_shop")
	if err := os.MkdirAll(tx, 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, "testdata/day/projects/-Users-x-Projects-scifi-svc_shop/s1.jsonl", filepath.Join(tx, "s1.jsonl"))
	day := time.Date(2026, 11, 20, 14, 0, 0, 0, time.Local)
	if err := os.Chtimes(filepath.Join(tx, "s1.jsonl"), day, day); err != nil {
		t.Fatal(err)
	}
	// Bus fixtures are kept as .txt (a tracked *.log under bus/ is private state to the leak check).
	bus := filepath.Join(dir, "bus")
	if err := os.MkdirAll(bus, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"shop", "noise"} {
		copyFile(t, "testdata/day/buslines/"+n+".txt", filepath.Join(bus, n+".log"))
	}
	cfg := Config{
		BusDirs:  []string{bus},
		TxRoots:  []string{filepath.Join(dir, "projects")},
		BookDir:  bookDir,
		StateDir: filepath.Join(dir, "state"),
		Since:    time.Date(2026, 11, 20, 0, 0, 0, 0, time.Local),
	}
	return cfg, bookDir
}

func checkDay(t *testing.T, cfg Config, bookDir string, res *Result) (Config, *Result) {
	t.Helper()
	var picked []string
	for _, c := range res.Candidates {
		picked = append(picked, c.Ref)
	}
	sort.Strings(picked)
	if len(res.Candidates) != 3 {
		t.Fatalf("rules picked %d candidates, want the 3 findings: %v", len(res.Candidates), picked)
	}
	if res.New != 2 || res.Evidence != 1 {
		t.Fatalf("want 2 new entries and 1 evidence, got new=%d evidence=%d (kept %d, unwritten %d, retired %v, dropped %q, failed %q)",
			res.New, res.Evidence, res.Kept, res.Unwritten, res.Retired, res.Dropped, res.Failed)
	}
	b, err := LoadBook(bookDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entries) != 3 {
		t.Fatalf("book has %d entries, want 3", len(b.Entries))
	}
	rep := b.Find("bus-does-not-wake-t3-thread")
	if rep == nil || len(rep.Evidence) != 2 || rep.Evidence[1].Team != "shop" || rep.Evidence[1].Date != "2026-11-20" {
		t.Fatalf("repeat entry evidence = %+v, want a second evidence from shop on 2026-11-20", rep)
	}
	tx1 := 0
	for _, e := range b.Entries {
		if len(e.Evidence) == 1 && e.Evidence[0].Team == "svc_shop" && strings.Contains(e.Evidence[0].Ref, "s1.jsonl#a2") {
			tx1++
		}
	}
	if tx1 != 1 {
		t.Fatalf("want one new entry from the svc_shop transcript (linked to s1.jsonl#a2), got %d: %+v", tx1, b.Entries)
	}
	// The index and the topic files are written.
	for _, e := range b.Entries {
		if _, err := os.Stat(filepath.Join(bookDir, e.Topic+".md")); err != nil {
			t.Errorf("missing topic file: %v", err)
		}
	}
	if _, err := os.Stat(filepath.Join(bookDir, "SKILL.md")); err != nil {
		t.Error(err)
	}
	return cfg, res
}
