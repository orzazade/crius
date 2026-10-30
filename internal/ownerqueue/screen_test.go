package ownerqueue

import (
	"strings"
	"testing"
	"time"
)

// Turn ends of 10-30 (crius-build measurement over 1,523 of them): leads that write no status emoji ask with a label or
// end on a question; a label that says "nothing" asks nothing.
func TestAReplyAsksByEmojiLabelOrClosingQuestion(t *testing.T) {
	for _, c := range []struct {
		reply string
		asks  bool
	}{
		{"Three things still need you before the build starts:\n1. A1\n2. A2\n3. \"Go\" on BUILD-PLAN.md", true},
		{"**Needs you:** pick A or C for the upload page.", true},
		{"What I need from you: the alert receiver.", true},
		{"Still open from you: A1, A2 and go.", true},
		{"Pushed 3c1f2e0.\n\nShould the fix go to main or develop?", true},
		{"Done, pushed.\n\nWant the same on develop?\n\n✅", false},
		{"Pushed.\nBlocked: the token for ADO.\n\n❓", true},
		{"Build plan committed. Nothing needs you here. ✅", false},
		{"Started the build. Waiting on you: nothing.", false},
		{"**Needs owner:** none", false},
		{"Plan committed; the build starts at 18:00.", false},
		{"The test asks \"is it red?\" and it is.\n\n⏳", false},
	} {
		if got := Asks(c.reply); got != c.asks {
			t.Errorf("Asks(%q) = %v, want %v", c.reply, got, c.asks)
		}
	}
}

func TestAScreenAskIsOneRowUntilTheLeadMovesOn(t *testing.T) {
	h := home(t, map[string]string{"marketplace.log": ""})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	ask := ScreenAsk{Lead: "marketplace:lead", Key: "surface:7:abc", Text: "Still needs you:\n1. A1\n2. A2", Project: "Marketplace",
		Since: now.Add(-11 * time.Minute)}
	for i := 0; i < 2; i++ { // read again 5 min later: the same screen makes no second row
		if held, err := SyncScreens(h, now.Add(time.Duration(i*5)*time.Minute), []ScreenAsk{ask}, nil); err != nil || held != nil {
			t.Fatal(held, err)
		}
	}
	other := ask
	other.Key, other.Text = "surface:7:def", "Needs you: A1 in other words?"
	held, err := SyncScreens(h, now.Add(10*time.Minute), []ScreenAsk{other}, nil)
	if err != nil || len(held) != 1 || !strings.Contains(held[0], "is open in the queue") {
		t.Fatalf("an ask in other words while the first is open: held %q, %v", held, err)
	}
	q, err := List(h, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if o := open(t, q); len(o) != 1 || o[0].Title != "Marketplace: Still needs you" || o[0].Screen != "marketplace:lead" || o[0].From != "marketplace" {
		t.Fatalf("want one row titled with its project and first ask, got %+v", o)
	}
	if _, err := SyncScreens(h, now.Add(15*time.Minute), nil, []string{"marketplace:qa"}); err != nil {
		t.Fatal(err)
	}
	if q, _ = List(h, now.Add(15*time.Minute)); len(open(t, q)) != 1 {
		t.Fatalf("another role going quiet withdrew the lead's row: %+v", q.Rows)
	}
	if _, err := SyncScreens(h, now.Add(20*time.Minute), nil, []string{"marketplace:lead"}); err != nil {
		t.Fatal(err)
	}
	if q, _ = List(h, now.Add(20*time.Minute)); len(open(t, q)) != 0 || q.Rows[0].State != Withdrawn || q.Rows[0].Answer != noLongerOnScreen {
		t.Fatalf("the lead moved on but its row stays: %+v", q.Rows)
	}
}

// A lead's turn may post the NEED-OWNER for what its screen asks (wakewatch reads every 5 min), and an owner answer
// since the screen began has already been given: either holds the ask back. Older ones do not.
func TestAScreenAskIsHeldWhenTheBusOrTheOwnerHasIt(t *testing.T) {
	since := now.Add(-11 * time.Minute)
	for _, c := range []struct {
		name, bus, answer string
		held              string
	}{
		{"need-owner just before the screen", "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — A1 and A2\n", "", "is open in the queue"},
		{"answered just before", "2026-10-30T12:00 NEED-OWNER marketplace:lead -> crius — A1 and A2\n", "owner: go", "the owner answered"},
		{"answered long before", "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1 and A2\n", "old", ""},
		{"another project's ask", "2026-10-30T13:40 NEED-OWNER falcon:lead -> crius — token\n", "", ""},
	} {
		h := home(t, map[string]string{"x.log": c.bus})
		if err := Init(h, "2026-10-30T00:00"); err != nil {
			t.Fatal(err)
		}
		if c.answer != "" {
			at := now.Add(-20 * time.Minute)
			if c.answer == "old" {
				at = now.Add(-4 * time.Hour)
			}
			if err := Answer(h, at, "q1", "owner: go", "owner"); err != nil {
				t.Fatal(c.name, err)
			}
		}
		held, err := SyncScreens(h, now, []ScreenAsk{{Lead: "marketplace:lead", Key: "s:1", Text: "Needs you: A1", Since: since}}, nil)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if c.held == "" && held != nil || c.held != "" && (len(held) != 1 || !strings.Contains(held[0], c.held)) {
			t.Errorf("%s: held %q, want %q", c.name, held, c.held)
		}
	}
}
