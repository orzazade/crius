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
	} {
		if got := Asks(c.reply); got != c.asks {
			t.Errorf("Asks(%q) = %v, want %v", c.reply, got, c.asks)
		}
	}
}

// Review round 1 of 2bca850: a reply ending on "❓" alone was titled "❓", and one printing "Needs owner: nothing" was
// titled "nothing".
func TestAScreenAskIsTitledWithWhatItAsks(t *testing.T) {
	for _, c := range []struct{ reply, title string }{
		{"Three things still need you before the build starts:\n1. A1", "Three things still need you before the build starts"},
		{"Should I deploy A1 now, or wait for the review?\n\n❓", "Should I deploy A1 now, or wait for the review?"},
		{"Needs owner: nothing\nShip it? ❓", "Ship it?"},
		{"Pushed.\nBlocked: the token for ADO.\n\n❓", "Blocked: the token for ADO."},
	} {
		if got := firstAsk(c.reply); got != c.title {
			t.Errorf("firstAsk(%q) = %q, want %q", c.reply, got, c.title)
		}
	}
}

// The upload-central lead's ask on screen (10-30 17:44) and its GOAL row q14 ask the same in other words.
func TestTheSameAskInOtherWordsIsTheSame(t *testing.T) {
	q14 := Row{Title: "upload-central: who serves clean files: svc_file (as DESIGN option C) or each service on its own",
		Detail: "Open (asked in chat): who serves clean files (CDN for public media, short links for private files, delete/purge)"}
	screen := "❓ I'm still waiting for your answer on who serves files after a clean scan.\n" +
		"The open question: after a file passes the malware scan, who stores and serves it?"
	if !sameAsk(q14, screen) {
		t.Error("the same question in other words is not seen as the same")
	}
	if sameAsk(q14, "Needs you: renew the test tenant licence before Friday?") {
		t.Error("another question is seen as the same")
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

// One row per tab: read again, it makes nothing new; a newer ask in the same tab rewrites it; nothing on screen closes
// it (review round 1 of 2bca850, P1: "holding until the owner answers ⏳" withdrew a question still open).
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
	crius := ScreenAsk{Lead: "crius:build", Key: "surface:3:abc", Text: "Want me to also do X?", Since: now.Add(-11 * time.Minute)}
	if _, err := SyncScreens(h, now.Add(20*time.Minute), []ScreenAsk{crius}); err != nil {
		t.Fatal(err)
	}
	if rs := screenRows(t, h); len(rs) != 2 {
		t.Fatalf("Crius's own team asks Crius, not the owner: %+v", rs)
	}
}

// A lead's turn may post the NEED-OWNER for what its screen asks (wakewatch reads every 5 min), or a row may ask the
// same already: either holds the ask back, and the hold is kept for list. An open row about something else holds
// nothing (review round 1 of 2bca850, P1: upload-central's old q14 hid every new question).
func TestAScreenAskIsHeldOnlyByARowThatHasIt(t *testing.T) {
	since := now.Add(-11 * time.Minute)
	for _, c := range []struct {
		name, bus, answer string
		held              string
	}{
		{"need-owner as the screen began", "2026-10-30T13:40 NEED-OWNER marketplace:lead -> crius — pick the landing page\n", "", "came at 2026-10-30T13:40"},
		{"an old open row that asks the same", "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1, the sign-in app\n", "", "is open in the queue and asks the same"},
		{"an old open row about something else", "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — renew the test tenant licence\n", "", ""},
		{"the owner answered the same just now", "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1, the sign-in app\n", "now", "the owner answered q1"},
		{"the owner answered the same long ago", "2026-10-30T09:00 NEED-OWNER marketplace:lead -> crius — A1, the sign-in app\n", "old", ""},
		{"another project's ask", "2026-10-30T13:40 NEED-OWNER falcon:lead -> crius — A1 token\n", "", ""},
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
		held, err := SyncScreens(h, now, []ScreenAsk{{Lead: "marketplace:lead", Key: "s:1", Text: "Needs you: A1, the sign-in app for the landing page", Since: since}})
		if err != nil {
			t.Fatal(c.name, err)
		}
		kept, err := ScreenHeld(h)
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
