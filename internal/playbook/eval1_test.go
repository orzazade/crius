package playbook

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Crius M3 eval (11-14): 2 of 7 "n" were stale, e.g. an entry about a guard
// that was removed on 11-08. A later record that says an entry no longer holds
// retires it: it leaves the live book and keeps the record as evidence.
func TestRetireTakesAStaleEntryOutOfTheLiveBook(t *testing.T) {
	b := &Book{}
	c := Candidate{ID: "bus:a:1", Team: "crius", Time: time.Date(2026, 11, 5, 9, 0, 0, 0, time.Local), Ref: "bus a 11-05"}
	if _, err := b.Apply(Op{Candidate: c.ID, Entry: Entry{ID: "post-guard-stops-scripts", Topic: "harness-and-accounts", Kind: "trap",
		When: "a script posts to ADO", Do: "approve the guard prompt", Because: "the guard asks"}}, c, "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	later := Candidate{ID: "bus:a:2", Team: "crius", Time: time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local), Ref: "bus a 11-08"}
	got, err := b.Apply(Op{Candidate: later.ID, SameAs: "post-guard-stops-scripts", Retire: true, Note: "guards removed 11-08"}, later, "2026-11-14")
	if err != nil || got != Retired {
		t.Fatalf("apply = %v, %v; want Retired", got, err)
	}
	e := b.Find("post-guard-stops-scripts")
	if len(b.Live()) != 0 || !e.Archived || e.Retired != "2026-11-08" || len(e.Evidence) != 2 || e.Evidence[1].Note != "guards removed 11-08" {
		t.Fatalf("entry %+v, live %d; want it retired on 11-08 with the record kept", e, len(b.Live()))
	}
}

type orderModel struct {
	oneEach
	picked []string
}

func (m *orderModel) Pick(ctx context.Context, cs []Candidate) ([]Verdict, error) {
	for _, c := range cs {
		m.picked = append(m.picked, c.Text)
	}
	return m.oneEach.Pick(ctx, cs)
}

// A retire only works when the record that retires comes after the entry it
// retires, so candidates go to the models oldest first, across all sources.
func TestCandidatesGoOldestFirst(t *testing.T) {
	cfg, log := busConfig(t)
	now := time.Now()
	appendLine(t, log, now.Add(-time.Hour).Format("2006-01-02T15:04")+" LEARNED shop:a -> crius:lead — testing: newer finding, 26 min to 9 min")
	appendLine(t, filepath.Join(filepath.Dir(log), "zzz.log"), now.Add(-20*time.Hour).Format("2006-01-02T15:04")+" LEARNED zzz:a -> crius:lead — testing: older finding, 30 s to 3 s")
	m := &orderModel{}
	if _, err := Run(context.Background(), cfg, m); err != nil {
		t.Fatal(err)
	}
	if len(m.picked) != 2 || !strings.Contains(m.picked[0], "older") {
		t.Fatalf("pick order %q, want the older finding first", m.picked)
	}
}

// Review (11-14): the reply is {"items": [...]}; read that field by name, so a
// "[" in another field cannot be taken for the list.
func TestRepliesAreReadByTheItemsField(t *testing.T) {
	var vs []Verdict
	if err := decodeItems(`{"note":"see [1]","items":[{"id":"a","keep":true,"topic":"testing"}]}`, &vs); err != nil || len(vs) != 1 || vs[0].ID != "a" {
		t.Fatalf("decodeItems = %+v, %v", vs, err)
	}
	vs = nil
	if err := decodeItems("[{\"id\":\"b\",\"keep\":true}]", &vs); err != nil || len(vs) != 1 || vs[0].ID != "b" {
		t.Fatalf("a bare array no longer decodes: %+v, %v", vs, err)
	}
}

// Review (11-14): an answer with a candidate and nothing else became "bad id".
// The write schema now needs a same_as or an entry, and knows retire.
func TestWriteSchemaNeedsSameAsOrEntry(t *testing.T) {
	var s struct {
		AdditionalProperties *bool `json:"additionalProperties"`
		Properties           struct {
			Items struct {
				Items struct {
					AnyOf      []map[string]any          `json:"anyOf"`
					If         json.RawMessage           `json:"if"`
					Then       json.RawMessage           `json:"then"`
					Properties map[string]map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(writeSchema([]Candidate{{ID: "c1"}}), &s); err != nil {
		t.Fatal(err)
	}
	it := s.Properties.Items.Items
	var cond struct {
		If struct {
			Properties struct {
				Retire struct {
					Const *bool `json:"const"`
				} `json:"retire"`
			} `json:"properties"`
		} `json:"if"`
		Then struct {
			Required []string `json:"required"`
		} `json:"then"`
	}
	raw, _ := json.Marshal(map[string]json.RawMessage{"if": it.If, "then": it.Then})
	_ = json.Unmarshal(raw, &cond)
	if c := cond.If.Properties.Retire.Const; c == nil || !*c || len(cond.Then.Required) != 1 || cond.Then.Required[0] != "same_as" {
		t.Fatalf("schema: a retire must need same_as (if/then), got %s", raw)
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties || len(it.AnyOf) != 2 || it.Properties["retire"] == nil {
		t.Fatalf("schema: additionalProperties %v, anyOf %v, retire %v", s.AdditionalProperties, it.AnyOf, it.Properties["retire"])
	}
}

func retireBook(t *testing.T, kind string) (*Book, Candidate) {
	t.Helper()
	b := &Book{}
	c := Candidate{ID: "bus:a:1", Team: "crius", Time: time.Date(2026, 11, 5, 9, 0, 0, 0, time.Local), Ref: "bus a 11-05"}
	if _, err := b.Apply(Op{Candidate: c.ID, Entry: Entry{ID: "some-entry", Topic: "testing", Kind: kind,
		When: "a thing happens", Do: "do the thing", Because: "it worked"}}, c, "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	return b, c
}

func at(day int, ref string) Candidate {
	return Candidate{ID: ref, Team: "crius", Time: time.Date(2026, 11, day, 9, 0, 0, 0, time.Local), Ref: ref}
}

// Review (11-14): a sighting older than the retire must not bring it back.
func TestAnOlderSightingDoesNotUnretire(t *testing.T) {
	b, _ := retireBook(t, "trap")
	if got, err := b.Apply(Op{Candidate: "r", SameAs: "some-entry", Retire: true, Note: "gone"}, at(8, "r"), "2026-11-14"); got != Retired || err != nil {
		t.Fatalf("retire: %v %v", got, err)
	}
	if _, err := b.Apply(Op{Candidate: "o", SameAs: "some-entry", Outcome: "hit"}, at(6, "o"), "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	if e := b.Find("some-entry"); !e.Archived || e.Retired != "2026-11-08" || e.Evidence[1].Outcome != "retired" {
		t.Fatalf("entry %+v: want still retired on 11-08, retire evidence marked retired", e)
	}
	if _, err := b.Apply(Op{Candidate: "n", SameAs: "some-entry", Outcome: "hit"}, at(10, "n"), "2026-11-14"); err != nil || b.Find("some-entry").Archived {
		t.Fatalf("a newer sighting should bring it back: %v %+v", err, b.Find("some-entry"))
	}
}

// Review (11-14): limits on retire, so one answer cannot wipe good entries.
func TestRetireIsRefusedWhenItCannotBeTrusted(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		op   Op
		day  int
		want string
	}{
		"older than the last sighting": {"trap", Op{SameAs: "some-entry", Retire: true}, 4, "older than the entry's last sighting"},
		"owner preference":             {"owner-preference", Op{SameAs: "some-entry", Retire: true}, 8, "owner preference"},
		"no same_as": {"trap", Op{Retire: true, Entry: Entry{ID: "some-entry", Topic: "testing", Kind: "trap",
			When: "x happens", Do: "do y", Because: "z"}}, 8, "retire needs same_as"},
	} {
		b, _ := retireBook(t, tc.kind)
		tc.op.Candidate = "r"
		if _, err := b.Apply(tc.op, at(tc.day, "r"), "2026-11-14"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
		if len(b.Live()) != 1 {
			t.Errorf("%s: the entry was retired anyway", name)
		}
	}
}

// Review (11-14): a new entry with no topic takes the topic the pick gave.
func TestNewEntryWithoutTopicTakesThePickTopic(t *testing.T) {
	b := &Book{}
	c := at(5, "c")
	c.Topic = "databases"
	if _, err := b.Apply(Op{Candidate: "c", Entry: Entry{ID: "no-topic", Kind: "trap", When: "a happens", Do: "do b", Because: "c"}}, c, "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	if got := b.Find("no-topic").Topic; got != "databases" {
		t.Fatalf("topic %q, want databases", got)
	}
}

type retireAll struct{ oneEach }

func (m *retireAll) Write(_ context.Context, cs []Candidate, ex []Entry) ([]Op, error) {
	var ops []Op
	for i, c := range cs {
		if i < len(ex) {
			ops = append(ops, Op{Candidate: c.ID, SameAs: ex[i].ID, Retire: true, Note: "gone"})
		}
	}
	return ops, nil
}

// Review (11-14): at most maxRetires per run; the rest are dropped loudly.
func TestRetiresPerRunAreCapped(t *testing.T) {
	cfg, log := busConfig(t)
	b := &Book{}
	for i := 0; i < maxRetires+2; i++ {
		c := at(1, "seed"+string(rune('a'+i)))
		c.Time = time.Now().AddDate(0, 0, -30)
		if _, err := b.Apply(Op{Candidate: c.ID, Entry: Entry{ID: "entry-" + string(rune('a'+i)), Topic: "testing", Kind: "trap",
			When: "a happens", Do: "do b", Because: "c"}}, c, "2026-11-14"); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Save(cfg.BookDir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxRetires+2; i++ {
		appendLine(t, log, time.Now().Add(-time.Duration(60-i)*time.Minute).Format("2006-01-02T15:04")+
			" LEARNED shop:a -> crius:lead — testing: guard number "+string(rune('a'+i))+" was removed, 0 prompts now")
	}
	res, err := Run(context.Background(), cfg, &retireAll{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Retired) != maxRetires || len(res.Dropped) != 2 || !strings.Contains(res.Dropped[0], "retire cap") || res.Healthy() {
		t.Fatalf("retired %d, dropped %q, healthy %v; want %d retired and 2 dropped at the cap", len(res.Retired), res.Dropped, res.Healthy(), maxRetires)
	}
}
