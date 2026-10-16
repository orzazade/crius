package store

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

type decCase struct {
	t    *testing.T
	home string
}

func newDecCase(t *testing.T) decCase {
	_, home := testutil.Temp(t)
	initHome(t, home)
	return decCase{t, home}
}

func (c decCase) rec(title string, in DecisionInput) (string, error) {
	in.Project, in.Title, in.Why, in.Today = "demo", title, "because", "2026-10-16"
	if in.DecidedBy == "" {
		in.DecidedBy = "crius"
	}
	return RecordDecision(c.home, in)
}

func (c decCase) must(title string, in DecisionInput) string {
	c.t.Helper()
	id, err := c.rec(title, in)
	if err != nil {
		c.t.Fatal(err)
	}
	return id
}

func (c decCase) entries() ([]Decision, []string) {
	return ParseDecisions(testutil.Read(c.t, filepath.Join(c.home, "state", "demo", "DECISIONS.md")))
}

func set(ids ...string) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func TestThreeDecisionsWithASupersedeGiveTheRightActiveSet(t *testing.T) {
	c := newDecCase(t)
	a := c.must("use v1", DecisionInput{})
	b := c.must("cache prices", DecisionInput{})
	d := c.must("use v2", DecisionInput{Supersedes: a, Reason: "v1 drops currency"})
	if [3]string{a, b, d} != [3]string{"demo-D1", "demo-D2", "demo-D3"} {
		t.Fatal(a, b, d)
	}
	entries, problems := c.entries()
	if len(problems) != 0 {
		t.Fatal(problems)
	}
	if got := ActiveIDs(entries); !reflect.DeepEqual(got, set("demo-D2", "demo-D3")) {
		t.Fatal(got)
	}
	if entries[2].Supersedes != "demo-D1" || entries[2].Reason != "v1 drops currency" {
		t.Fatalf("%+v", entries[2])
	}
}

func TestSupersedeChainLeavesOnlyTheNewest(t *testing.T) {
	c := newDecCase(t)
	a := c.must("one", DecisionInput{})
	b := c.must("two", DecisionInput{Supersedes: a, Reason: "r"})
	d := c.must("three", DecisionInput{Supersedes: b, Reason: "r"})
	entries, _ := c.entries()
	if got := ActiveIDs(entries); !reflect.DeepEqual(got, set(d)) {
		t.Fatal(got)
	}
}

func TestDecisionFileIsAppendOnly(t *testing.T) {
	c := newDecCase(t)
	c.must("one", DecisionInput{})
	path := filepath.Join(c.home, "state", "demo", "DECISIONS.md")
	before := testutil.Read(t, path)
	c.must("two", DecisionInput{})
	if !strings.HasPrefix(testutil.Read(t, path), before) {
		t.Fatal("earlier text changed")
	}
}

func TestSupersedeNeedsAKnownIDAndAReason(t *testing.T) {
	c := newDecCase(t)
	c.must("one", DecisionInput{})
	if _, err := c.rec("two", DecisionInput{Supersedes: "demo-D9", Reason: "r"}); !IsRefused(err) {
		t.Fatal(err)
	}
	if _, err := c.rec("two", DecisionInput{Supersedes: "demo-D1"}); !IsRefused(err) {
		t.Fatal(err)
	}
}

func TestOnlyTheOwnerSupersedesAnOwnerDecision(t *testing.T) {
	c := newDecCase(t)
	a := c.must("owner call", DecisionInput{DecidedBy: "owner"})
	if _, err := c.rec("crius changes it", DecisionInput{Supersedes: a, Reason: "r"}); !IsRefused(err) {
		t.Fatal(err)
	}
	if id := c.must("owner changes it", DecisionInput{Supersedes: a, Reason: "r", DecidedBy: "owner"}); id != "demo-D2" {
		t.Fatal(id)
	}
}

func TestBadRefInFileIsKeptAndFlagged(t *testing.T) {
	text := "## demo-D1 · 2026-10-16\nDecision: a\nWhy: b\nDecided by: crius\n\n" +
		"## demo-D2 · 2026-10-16\nDecision: c\nWhy: d\nDecided by: crius\nSupersedes: demo-D7\nReason: r\n"
	entries, problems := ParseDecisions(text)
	if len(entries) != 2 || entries[0].ID != "demo-D1" || entries[1].ID != "demo-D2" {
		t.Fatalf("%+v", entries)
	}
	if !anyContains(problems, "demo-D7") {
		t.Fatal(problems)
	}
	if got := ActiveIDs(entries); !reflect.DeepEqual(got, set("demo-D1", "demo-D2")) {
		t.Fatal(got)
	}
}

func TestRefsCannotInjectFields(t *testing.T) {
	c := newDecCase(t)
	a := c.must("owner call", DecisionInput{DecidedBy: "owner"})
	if _, err := c.rec("sneaky", DecisionInput{Refs: "x\nSupersedes: " + a + "\nReason: r"}); !IsRefused(err) {
		t.Fatal(err)
	}
	entries, _ := c.entries()
	if got := ActiveIDs(entries); !reflect.DeepEqual(got, set(a)) {
		t.Fatal(got)
	}
}

func TestFailedBusLineNamesTheSavedDecision(t *testing.T) {
	c := newDecCase(t)
	bus := filepath.Join(c.home, "bus")
	if err := os.Chmod(bus, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(bus, 0o700)
	_, err := c.rec("one", DecisionInput{})
	if !IsPartial(err) || !strings.Contains(err.Error(), "demo-D1 was saved") {
		t.Fatal(err)
	}
	entries, _ := c.entries()
	if len(entries) != 1 || entries[0].ID != "demo-D1" {
		t.Fatalf("%+v", entries)
	}
}

func TestDecisionPostsABusLine(t *testing.T) {
	c := newDecCase(t)
	a := c.must("one", DecisionInput{})
	log := strings.Split(strings.TrimRight(testutil.Read(t, filepath.Join(c.home, "bus", "demo.log")), "\n"), "\n")
	if len(log) != 1 || !strings.Contains(log[0], " DECISION demo:crius -> crius — ") || !strings.Contains(log[0], a) {
		t.Fatal(log)
	}
}
