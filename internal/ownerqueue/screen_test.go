package ownerqueue

import (
	"strings"
	"testing"
	"time"
)

// Turn ends of 10-30 (crius-build measurement over 1,523 of them): leads that write no status emoji ask with a label or
// end on a question; a label that says "nothing" asks nothing. Review round 1 of 2bca850 added the other shapes.
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
		{"❓ I'm still waiting for your answer on who serves files.\n\n- 1. File Service\n- 2. Each service\n\nReply with 1 or 2.", true},
		{"Build plan committed. Nothing needs you here. ✅", false},
		{"Started the build. Waiting on you: nothing.", false},
		{"**Needs owner:** none", false},
		{"Plan committed; the build starts at 18:00.", false},
		{"The test asks \"is it red?\" and it is.\n\n⏳", false},
		{"Holding until the owner answers A1. ⏳", false},
		{"- [x] tests ✅\n- [x] review ✅\nShall I push?", true},
		{"Needs owner: —", false},
		{"Needs owner: n/a ✅", false},
		{"Needs owner: not now ✅", false},
		{"Needs owner: <what, or \"nothing\">", false},
		{"The brief says:\n> Needs you: pick A or C\nI picked A as it says. ✅", false},
		{"```\nNeeds you: example\n```\nDone.", false},
		{"Needs you: nothing new, but A1 is still open", true},
		{"Tests pass.\n\n**Needs you:** nothing right now.\n\n⏳ CI running", false}, // review round 3 of 2bca850, P2-2
		{"**Needs owner:** none at the moment ⏳", false},
		{"Needs owner: nothing open.", false},
		{"Needs owner: nothing. go test ./... 34 ok ✅", false},
		{"Needs owner: nothing. Decided today: B92, B93 ✅", false},
		{"Needs you: nothing yet, unless the review finds a P1 ⏳", true},
		{"Needs you: nothing? A1 is open", true},
		{"Needs you: no rush, but pick A or C", true},
		{"Needs owner: nothing (parked) ✅", false},
		{"Needs owner: none.", false},
	} {
		if got := Asks(c.reply); got != c.asks {
			t.Errorf("Asks(%q) = %v, want %v", c.reply, got, c.asks)
		}
	}
}

// Review round 1 of 2bca850: a reply ending on "❓" alone was titled "❓", and one printing "Needs owner: nothing" was
// titled "nothing"; round 2 (P3-1): an empty label kept its list and heading marks.
func TestAScreenAskIsTitledWithWhatItAsks(t *testing.T) {
	for _, c := range []struct{ reply, title string }{
		{"Three things still need you before the build starts:\n1. A1", "Three things still need you before the build starts"},
		{"Should I deploy A1 now, or wait for the review?\n\n❓", "Should I deploy A1 now, or wait for the review?"},
		{"Needs owner: nothing\nShip it? ❓", "Ship it?"},
		{"Pushed.\nBlocked: the token for ADO.\n\n❓", "Blocked: the token for ADO."},
		{"- **Still needs you:**\n1. A1", "Still needs you"},
		{"### Still needs you:\n1. A1", "Still needs you"},
	} {
		if got := firstAsk(c.reply); got != c.title {
			t.Errorf("firstAsk(%q) = %q, want %q", c.reply, got, c.title)
		}
	}
}

func screenRows(t *testing.T, h string) []Row {
	t.Helper()
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	var out []Row
	for _, r := range q.Rows {
		if r.Screen != "" {
			out = append(out, r)
		}
	}
	return out
}

// One row per tab: read again, it makes nothing new; a newer ask in the same tab rewrites it, without the owner's
// question about the old words (review round 2 of 2bca850, P3-3); nothing on screen closes it (round 1, P1: "holding
// until the owner answers ⏳" withdrew a question still open).
func TestAScreenAskIsOneRowPerTabAndTheNewestWords(t *testing.T) {
	h := home(t, map[string]string{"marketplace.log": ""})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	ask := ScreenAsk{Lead: "marketplace:lead", Key: "surface:7:abc", Text: "Still needs you:\n1. A1\n2. A2", Project: "Marketplace",
		Since: now.Add(-11 * time.Minute)}
	for i := 0; i < 2; i++ {
		if held, err := SyncScreens(h, now.Add(time.Duration(i*5)*time.Minute), []ScreenAsk{ask}); err != nil || held != nil {
			t.Fatal(held, err)
		}
	}
	if _, err := SyncScreens(h, now.Add(10*time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if rs := screenRows(t, h); len(rs) != 1 || rs[0].State != Open || rs[0].Title != "Marketplace: Still needs you" || rs[0].From != "marketplace" {
		t.Fatalf("want one open row titled with its project and first ask, got %+v", rs)
	}
	if err := Answer(h, now.Add(12*time.Minute), "q1", "what is A1?", "owner"); err != nil || screenRows(t, h)[0].Asked == "" {
		t.Fatal("the owner's question back should keep q1 open with it", err)
	}
	newer := ask
	newer.Key, newer.Text = "surface:7:def", "Still open from you: A2 and go."
	other := ask
	other.Key, other.Text = "surface:9:abc", "Needs you: the sign-in app for the landing page"
	if _, err := SyncScreens(h, now.Add(15*time.Minute), []ScreenAsk{newer, other}); err != nil {
		t.Fatal(err)
	}
	rs := screenRows(t, h)
	if len(rs) != 2 || rs[0].ID != "q1" || rs[0].Title != "Marketplace: A2 and go" || rs[0].Line != "surface:7:def" || rs[1].Line != "surface:9:abc" {
		t.Fatalf("want q1 rewritten with the newest ask and a row for the other tab, got %+v", rs)
	}
	if rs[0].Asked != "" || rs[0].AskedAt != "" || rs[0].State != Open {
		t.Fatalf("the owner's question was about the old words, so the rewrite drops it: %+v", rs[0])
	}
	crius := ScreenAsk{Lead: "crius:build", Key: "surface:3:abc", Text: "Want me to also do X?", Since: now.Add(-11 * time.Minute)}
	if _, err := SyncScreens(h, now.Add(20*time.Minute), []ScreenAsk{crius}); err != nil {
		t.Fatal(err)
	}
	if rs := screenRows(t, h); len(rs) != 2 {
		t.Fatalf("Crius's own team asks Crius, not the owner: %+v", rs)
	}
}

// A lead's turn may post the NEED-OWNER, or write the GOAL ask, for what its screen asks (wakewatch reads every 5
// min): an open one holds the ask back, and the hold is kept for list. Nothing else holds: guessing that two texts ask
// the same held unrelated questions (review round 2 of 2bca850, P1-1: shared words "marketplace, microsoft, prototype"
// or an id such as P1), and an answered NEED-OWNER or one Crius added said nothing of the new ask (P2-1).
func TestAScreenAskIsHeldOnlyByWhatTheSameTurnPosted(t *testing.T) {
	since := now.Add(-11 * time.Minute)
	for _, c := range []struct {
		name, bus, answer, add, goal string
		held                         string
	}{
		{name: "an open need-owner as the screen began", bus: "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — pick the landing page\n", held: "its NEED-OWNER q1 came at 2026-10-30T13:40"},
		{name: "a need-owner answered before the screen began", bus: "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — renew the test tenant licence\n", answer: "13:45"},
		// review round 3 of 2bca850 (P2-1): the owner answered it after the screen began; the lead has not read it yet
		{name: "a need-owner answered after the screen began", bus: "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — pick the landing page\n", answer: "13:55", held: "its NEED-OWNER q1 came at 2026-10-30T13:40"},
		{name: "a need-owner long before the screen", bus: "2026-10-30T13:20 NEED-OWNER marketplace:lead -> crius — pick the landing page\n"},
		{name: "an old open row that asks the same", bus: "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1, the sign-in app for the landing page\n"},
		{name: "an old open row with the same words", bus: "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — Marketplace: the landing page sign-in prototype, P1 A1\n"},
		{name: "the owner answered the same just now", bus: "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1, the sign-in app\n", answer: "13:45"},
		{name: "a row Crius added as the screen began", add: "Marketplace: pick the landing page"},
		{name: "its GOAL ask as the screen began", goal: "marketplace:lead", held: "its GOAL ask q1 came at 2026-10-30T13:50"},
		{name: "another lead's GOAL ask", goal: "marketplace:qa"},
		{name: "another project's ask", bus: "2026-10-30T13:40 NEED-OWNER falcon:lead -> crius — A1 token\n"},
	} {
		h := home(t, map[string]string{"x.log": c.bus})
		if err := Init(h, "2026-10-30T00:00"); err != nil {
			t.Fatal(err)
		}
		if c.answer != "" {
			if _, err := List(h, now); err != nil {
				t.Fatal(err)
			}
			at, _ := time.ParseInLocation("2006-01-02T15:04", "2026-10-30T"+c.answer, time.Local)
			if err := Answer(h, at, "q1", "owner: go", "owner"); err != nil {
				t.Fatal(c.name, err)
			}
		}
		if c.add != "" {
			if _, err := Add(h, now.Add(-15*time.Minute), Row{Title: c.add, From: "marketplace"}); err != nil {
				t.Fatal(err)
			}
		}
		if c.goal != "" {
			if _, err := SyncGoals(h, now.Add(-10*time.Minute), func() ([]GoalNeed, error) {
				return []GoalNeed{{Lead: c.goal, Text: "the sign-in app", Stamp: "2026-10-30T13:45"}}, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		held, err := SyncScreens(h, now, []ScreenAsk{{Lead: "marketplace:lead", Key: "s:1", Text: "Needs you: A1, the sign-in app for the landing page", Since: since}})
		if err != nil {
			t.Fatal(c.name, err)
		}
		kept, err := ScreenHeld(h, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if c.held == "" && (held != nil || len(screenRows(t, h)) != 1) || c.held != "" && (len(held) != 1 || !strings.Contains(held[0], c.held) || len(screenRows(t, h)) != 0) {
			t.Errorf("%s: held %q, want %q; rows %+v", c.name, held, c.held, screenRows(t, h))
		}
		if strings.Join(kept, "|") != strings.Join(held, "|") {
			t.Errorf("%s: list would show %q, held %q", c.name, kept, held)
		}
	}
}

// The held list is as old as wakewatch's last look: one not rewritten for a while says so (review round 2 of 2bca850,
// P3-5: a stopped wakewatch left old holds that looked current).
func TestAnOldScreenHoldSaysHowOld(t *testing.T) {
	h := home(t, map[string]string{"x.log": "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — pick the landing page\n"})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncScreens(h, now, []ScreenAsk{{Lead: "marketplace:lead", Key: "s:1", Text: "Needs you: A1", Since: now.Add(-11 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	fresh, err := ScreenHeld(h, time.Now())
	if err != nil || len(fresh) != 1 || strings.Contains(fresh[0], "as of") {
		t.Fatalf("a fresh hold: %q %v", fresh, err)
	}
	old, err := ScreenHeld(h, time.Now().Add(20*time.Minute))
	if err != nil || len(old) != 1 || !strings.Contains(old[0], "wakewatch has not read the tabs since") {
		t.Fatalf("an old hold does not say so: %q %v", old, err)
	}
}

// An open screen row is no GOAL ask on show: the owner often answers in the lead's tab, which closes nothing, and
// every later GOAL ask of the project would wait unseen (review round 2 of 2bca850, P2-3).
func TestAnOpenScreenRowHoldsNoGoalAsk(t *testing.T) {
	h := home(t, map[string]string{"x.log": ""})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncScreens(h, now, []ScreenAsk{{Lead: "marketplace:lead", Key: "s:1", Text: "Needs you: A1", Since: now.Add(-11 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	held, err := SyncGoals(h, now.Add(time.Hour), func() ([]GoalNeed, error) {
		return []GoalNeed{{Lead: "marketplace:lead", Text: "renew the tenant licence", Stamp: "2026-10-30T14:55"}}, nil
	})
	q, _ := List(h, now.Add(time.Hour))
	if err != nil || held != nil || len(q.OpenRows()) != 2 {
		t.Fatalf("want the GOAL ask open beside the screen row: held %q, err %v, rows %+v", held, err, q.Rows)
	}
}
