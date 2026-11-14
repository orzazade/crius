package playbook

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, _ := time.ParseInLocation("2006-01-02", s, time.Local)
	return t.Add(12 * time.Hour)
}

// Followed by two teams and never hit again: promote. Hit again after it was
// shown: needs a check. Nothing for 30 days: archived.
func TestReviewPromotesFlagsAndArchives(t *testing.T) {
	b := &Book{Entries: []Entry{
		{ID: "split-tests", Kind: "tactic", Created: "2026-11-10", Evidence: []Evidence{
			{Team: "shop", Date: "2026-11-10", Ref: "bus shop.log 2026-11-10T10:00"},
			{Team: "shop", Date: "2026-11-12", Ref: "/p/sA.jsonl#u1"},
			{Team: "staff", Date: "2026-11-13", Ref: "/p/sB.jsonl#u2"}}},
		{ID: "t3-wake", Kind: "trap", Created: "2026-11-10", Evidence: []Evidence{
			{Team: "shop", Date: "2026-11-10", Ref: "bus shop.log 2026-11-10T11:00"},
			{Team: "billing", Date: "2026-11-14", Ref: "bus billing.log 2026-11-14T09:00"}}},
		{ID: "stale", Kind: "tool-fact", Created: "2026-10-01", Evidence: []Evidence{
			{Team: "shop", Date: "2026-10-01", Ref: "bus shop.log 2026-10-01T09:00"}}},
	}}
	shown := []Shown{
		{Time: day("2026-11-11"), Session: "sA", ID: "split-tests"},
		{Time: day("2026-11-12"), Session: "sB", ID: "split-tests"},
		{Time: day("2026-11-11"), Session: "sC", ID: "t3-wake"},
	}
	r := ReviewBook(b, shown, day("2026-11-14"))
	if len(r.Promote) != 1 || r.Promote[0] != "split-tests" {
		t.Errorf("promote = %v, want [split-tests]", r.Promote)
	}
	if len(r.NeedsCheck) != 1 || r.NeedsCheck[0] != "t3-wake" {
		t.Errorf("needs check = %v, want [t3-wake]", r.NeedsCheck)
	}
	if len(r.Archived) != 1 || r.Archived[0] != "stale" || !b.Find("stale").Archived {
		t.Errorf("archived = %v, want [stale] and the entry marked", r.Archived)
	}
	if u := r.Uses[0]; u.Shown != 2 || u.Followed != 2 {
		t.Errorf("split-tests use = %+v, want shown 2 followed 2", u)
	}
}

func TestShownLogRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := day("2026-11-14")
	if err := LogShown(dir, "s1", "/x", []Entry{{ID: "a"}, {ID: "b"}}, now); err != nil {
		t.Fatal(err)
	}
	got, err := LoadShown(dir)
	if err != nil || len(got) != 2 || got[1].ID != "b" || got[0].Session != "s1" {
		t.Fatalf("LoadShown = %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shown.log"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadShown(dir); err == nil {
		t.Fatal("a broken shown.log read as fine")
	}
}
