package playbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// oneEach writes one new entry per candidate, its id taken from the candidate id.
type oneEach struct{ mu sync.Mutex }

func (m *oneEach) Pick(_ context.Context, cs []Candidate) ([]Verdict, error) {
	var out []Verdict
	for _, c := range cs {
		out = append(out, Verdict{ID: c.ID, Keep: true, Topic: "testing"})
	}
	return out, nil
}

func (m *oneEach) Write(_ context.Context, cs []Candidate, _ []Entry) ([]Op, error) {
	var ops []Op
	for _, c := range cs {
		id := "e-" + strings.NewReplacer(":", "-", "/", "-", ".", "-", "#", "-", "@", "-").Replace(c.ID)
		if len(id) > 60 {
			id = id[len(id)-60:]
			id = "e" + strings.TrimLeft(id, "-")
		}
		ops = append(ops, Op{Candidate: c.ID, Entry: Entry{ID: strings.ToLower(id), Topic: "testing", Kind: "tactic",
			When: "w", Do: "d", Because: "b"}})
	}
	return ops, nil
}

// Review P1 (11-14): a seed of a lesson file last changed 90 days ago archived
// its own entries in the same run, and the second seed offered them again.
func TestSeedOfAnOldFileIsNotArchivedOrSeededTwice(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lessons.md")
	if err := os.WriteFile(p, []byte("## Tests\n\nRun the API tests in 3 parts: the whole suite took 26 min against a 20 min cap.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -90)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	cfg := Config{DocFiles: []string{p}, BookDir: filepath.Join(dir, "book"), StateDir: filepath.Join(dir, "state")}
	res, err := Run(context.Background(), cfg, &oneEach{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := LoadBook(cfg.BookDir)
	if res.New != 1 || len(res.Review.Archived) != 0 || len(b.Live()) != 1 {
		t.Fatalf("new %d, archived %v, live %d; want 1 new entry, none archived, 1 live", res.New, res.Review.Archived, len(b.Live()))
	}
	res, err = Run(context.Background(), cfg, &oneEach{})
	if err != nil || len(res.Candidates) != 0 {
		t.Fatalf("second seed: %d candidates (%v), want 0", len(res.Candidates), err)
	}
}

func busConfig(t *testing.T) (Config, string) {
	dir := t.TempDir()
	bus := filepath.Join(dir, "bus")
	if err := os.MkdirAll(bus, 0o755); err != nil {
		t.Fatal(err)
	}
	return Config{BusDirs: []string{bus}, BookDir: filepath.Join(dir, "book"), StateDir: filepath.Join(dir, "state"),
		Since: time.Now().AddDate(0, 0, -2)}, filepath.Join(bus, "shop.log")
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

// Review P2 (11-14): a FAIL whose fix came after the night's read was never paired.
func TestFailPairsWithAFixPostedAfterTheRun(t *testing.T) {
	cfg, log := busConfig(t)
	at := time.Now().Add(-2 * time.Hour).Format("2006-01-02T15:04")
	appendLine(t, log, at+" FAIL shop:qa -> shop:lead — AC9 FAIL: the price is 0 on a copied quote")
	res, err := Run(context.Background(), cfg, &oneEach{})
	if err != nil || len(res.Candidates) != 0 {
		t.Fatalf("first run: %d candidates (%v), want 0 while the FAIL waits", len(res.Candidates), err)
	}
	fix := time.Now().Add(-time.Hour).Format("2006-01-02T15:04")
	appendLine(t, log, fix+" DONE shop:builder -> shop:lead — AC9 fixed: copy now carries the price")
	res, err = Run(context.Background(), cfg, &oneEach{})
	if err != nil || len(res.Candidates) != 1 || !strings.Contains(res.Candidates[0].Text, "THEN DONE") {
		t.Fatalf("second run: %+v (%v), want the FAIL paired with its fix", res.Candidates, err)
	}
}

// Review P2 (11-14): two lines in the same minute shared one evidence ref, so
// the second was dropped while the run counted it as evidence.
func TestTwoLinesInOneMinuteAreTwoSightings(t *testing.T) {
	b := &Book{Entries: []Entry{{ID: "split-tests", Kind: "tactic", When: "w", Do: "d", Because: "b"}}}
	cfg, log := busConfig(t)
	at := time.Now().Add(-time.Hour).Format("2006-01-02T15:04")
	appendLine(t, log, at+" LEARNED shop:a -> crius:lead — testing: split the suite, 26 min to 9 min")
	appendLine(t, log, at+" LEARNED shop:b -> crius:lead — testing: split the suite, 30 min to 10 min")
	cur, _ := loadCursor(cfg.StateDir)
	cs, _, err := Gather(cfg, cur, time.Now())
	if err != nil || len(cs) != 2 {
		t.Fatalf("gathered %d (%v), want 2", len(cs), err)
	}
	for _, c := range cs {
		if got, err := b.Apply(Op{Candidate: c.ID, SameAs: "split-tests"}, c, "2026-11-14"); err != nil || got != AddedEvidence {
			t.Fatalf("apply %s = %v, %v; want AddedEvidence", c.Ref, got, err)
		}
	}
	if got, _ := b.Apply(Op{Candidate: cs[0].ID, SameAs: "split-tests"}, cs[0], "2026-11-14"); got != Duplicate {
		t.Fatalf("the same line again = %v, want Duplicate", got)
	}
}

// Review P2 (11-14): with no lock, two runs at once each saved their own book
// and the last save won.
func TestTwoRunsAtOnceLoseNoEntry(t *testing.T) {
	cfg, log := busConfig(t)
	for i := 0; i < 20; i++ {
		at := time.Now().Add(-time.Duration(100-i) * time.Minute).Format("2006-01-02T15:04")
		appendLine(t, log, fmt.Sprintf("%s LEARNED shop:b%d -> crius:lead — testing: finding %d", at, i, i))
	}
	m := &oneEach{}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = Run(context.Background(), cfg, m) }()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, _ := LoadBook(cfg.BookDir)
	if len(b.Entries) != 20 {
		t.Fatalf("book has %d entries after two runs at once, want 20", len(b.Entries))
	}
}

// sameBatch answers the second candidate with the first one's candidate id.
type sameBatch struct{ oneEach }

func (m *sameBatch) Write(ctx context.Context, cs []Candidate, ex []Entry) ([]Op, error) {
	ops, _ := m.oneEach.Write(ctx, cs[:1], ex)
	for _, c := range cs[1:] {
		ops = append([]Op{{Candidate: c.ID, SameAs: cs[0].ID, Outcome: "same"}}, ops...)
	}
	return ops, nil
}

// Seen in the first real run (11-14): sonnet named a candidate of the same
// batch in same_as; that is the entry made from that candidate.
func TestSameAsACandidateOfTheBatch(t *testing.T) {
	cfg, log := busConfig(t)
	at := time.Now().Add(-time.Hour).Format("2006-01-02T15:04")
	appendLine(t, log, at+" LEARNED shop:a -> crius:lead — testing: split the suite, 26 min to 9 min")
	appendLine(t, log, at+" LEARNED shop:b -> crius:lead — testing: split the suite again, 30 min to 10 min")
	res, err := Run(context.Background(), cfg, &sameBatch{})
	if err != nil || res.New != 1 || res.Evidence != 1 || len(res.Dropped) != 0 {
		t.Fatalf("new %d evidence %d dropped %v (%v); want 1 entry with 2 sightings", res.New, res.Evidence, res.Dropped, err)
	}
}
