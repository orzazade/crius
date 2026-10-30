package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)

func setup(t *testing.T, bus string) string {
	t.Helper()
	h := t.TempDir()
	for _, d := range []string{"bus", "state"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "bus", "cortex.log"), []byte(bus), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), nil, 0o644); err != nil { // no leads
		t.Fatal(err)
	}
	if err := run(h, now, []string{"init", "--since", "2026-10-30T00:00"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	return h
}

func cli(t *testing.T, h string, args ...string) string {
	t.Helper()
	var b strings.Builder
	if err := run(h, now, args, &b); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return b.String()
}

// /waiting answers by number: "2 any text" answers the second row of the list, oldest first, and prints the line the
// mod submits to Crius.
func TestAnswerByNumberTakesTheListPlace(t *testing.T) {
	h := setup(t, "2026-10-30T09:00 NEED-OWNER cortex:lead -> crius — older one\n")
	cli(t, h, "add", "--title", "Restart Chrome", "--from", "Mac", "--options", "Done | Remind me at 18:00")
	if got := cli(t, h, "answer", "2", "not", "before", "Monday"); got != "[queue q2] not before Monday\n" {
		t.Fatalf("got %q", got)
	}
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 || v.Open[0].ID != "q1" || v.Open[0].Age != "5h" || len(v.Answered) != 1 || v.Answered[0].Answer != "not before Monday" {
		t.Fatalf("got %+v", v)
	}
	if err := run(h, now, []string{"answer", "5", "x"}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "no item 5: 1 wait on you") {
		t.Fatalf("want a refusal for item 5, got %v", err)
	}
}

func TestAnEmptyListSaysNothingWaits(t *testing.T) {
	h := setup(t, "")
	if got := cli(t, h, "list"); got != "✓ Nothing waits on you.\n" {
		t.Fatalf("got %q", got)
	}
}

// Crius 10-30 16:03: the pane said "Nothing waits on you" while the 16:00 goal report listed 9 GOAL "Needs owner"
// items. Each lead's GOAL block is one row: the 9 real fields of that report are 9 rows; "nothing" is none; a question
// Crius answered on the bus is left out as the report leaves it out; a block that cannot be read is named. Then: an
// owner answer stays, a lead whose block says nothing or asks another thing has its row withdrawn (and the new text
// opens one), an unreadable block keeps its row, a lead gone from projects.toml has its row withdrawn, and a
// projects.toml with no lead (read mid-write) changes nothing.
func TestEveryLeadsGoalNeedIsARow(t *testing.T) {
	h := setup(t, "")
	dir := t.TempDir()
	type lead struct{ name, stamp, needs string }
	asks := []lead{
		{"ado-94205:lead", "2026-10-30 11:02", "OK on the two comment drafts (never post without it)"},
		{"angular-91260:lead", "2026-10-28 19:40", "the 3 questions in STATUS-2026-10-28.md section 6"},
		{"appxite-gains:lead", "2026-10-30 10:15", "a walk-through of GAINS.md to pick which moves to build"},
		{"appxite-map:lead", "2026-10-30 13:55", `yes or no on the START ask 3 split (tenant rules, see "Open with owner"), then the walk-through`},
		{"axi:leader", "2026-10-30 13:12", "PROD-HANDOVER.md 12.2-12.8; phase-0: target date, v1 off for Microsoft only or all vendors (test orders answered: B84)"},
		{"crius:build", "2026-10-22 15:36", "OK to install hook (a) no-ai-attribution.py from ~/.crius/state/crius/proposals/p7-3/ (58/58 tests); after the observe day, ~/.crius/wakewatch-mode = decide (O3), brought by Crius"},
		{"falcon:lead", "2026-10-30 10:58", "Q1 CI as a source, Q2 mail folders · NEED 10-29; S2 mail root-fix scope · NEED 10-16T23:09; 10:58 work-config guard"},
		{"marketplace:lead", "2026-10-30 13:37", "A1 landing sign-in app and A2 listing fields home (docs/build/ARCHITECTURE.md §5); go on BUILD-PLAN.md."},
		{"upload-central:lead", "2026-10-30 09:40", "B4 — after a clean scan, who stores and serves the file: svc_file (1) or each service (2) (BUILD-DECISIONS B4, sha=8b5281b)"},
	}
	none := []lead{
		{"aitch:build", "2026-10-29 21:00", "nothing"},
		{"distiportal:lead", "2026-10-26 16:10", `"Nothing."`},
		// Answered on the bus after its stamp, by a Crius line that speaks for the owner and names the item.
		{"quotes:lead", "2026-10-29 03:20", `practice order 93785 (quote C94B1C, provisioned 10-28 16:24): "clean" (soft-delete the AppleCPQ subscription, delete billing row 21AFCB10) or "keep"`},
	}
	status := map[string]string{}
	write := func(name, text string) {
		t.Helper()
		if status[name] == "" {
			status[name] = filepath.Join(dir, strings.ReplaceAll(name, ":", "-")+".md")
		}
		if err := os.WriteFile(status[name], []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	block := func(l lead) string {
		return "# Status\n\n## GOAL (" + l.stamp + ")\nGoal: the goal\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n" +
			"Next: none\nNeeds owner: " + l.needs + "\n"
	}
	for _, l := range append(append([]lead{}, asks...), none...) {
		write(l.name, block(l))
	}
	write("trading:lead", "## GOAL (2026-10-30 13:4x)\nGoal: fx hunt\n")
	toml := func(leave string) {
		t.Helper()
		var b strings.Builder
		for _, name := range []string{"ado-94205:lead", "aitch:build", "angular-91260:lead", "appxite-gains:lead",
			"appxite-map:lead", "axi:leader", "crius:build", "distiportal:lead", "falcon:lead", "marketplace:lead",
			"quotes:lead", "trading:lead", "upload-central:lead"} {
			if name == leave {
				continue
			}
			p, role, _ := strings.Cut(name, ":")
			fmt.Fprintf(&b, "[projects.%s]\nleads = { %s = %q }\n\n", p, role, status[name])
		}
		if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	toml("")
	db, err := store.Open(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values (?,?,?,?,?,?)",
		"2026-10-29T08:38:00Z", "crius", "answer", "lead", `Owner 10:30 on practice order 93785: "keep" it.`, "bus:f:1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	type view struct {
		Open, Answered []struct{ ID, Title, From, Answer, AnsweredBy, Line string }
		Unread, Held   []string
	}
	list := func() view {
		t.Helper()
		var v view
		if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := list()
	got := map[string]string{}
	for _, r := range v.Open {
		got[r.From] = r.Line
	}
	for _, l := range asks {
		got[l.name] = got[project(l.name)]
	}
	if len(v.Open) != len(asks) {
		t.Errorf("want %d open rows, one per lead that asks, got %d: %+v", len(asks), len(v.Open), v.Open)
	}
	for _, l := range asks {
		if got[l.name] != l.needs {
			t.Errorf("%s: want a row for %q, got %q", l.name, l.needs, got[l.name])
		}
	}
	if len(v.Unread) != 1 || !strings.Contains(v.Unread[0], "trading:lead: ") ||
		!strings.Contains(v.Unread[0], "GOAL block unreadable: line 1: heading needs (YYYY-MM-DD HH:MM)") {
		t.Errorf("want trading:lead named as unreadable, got %q", v.Unread)
	}
	if text := cli(t, h, "list"); !strings.Contains(text, "GOAL block cannot be read, so what these leads ask is not listed (1):\n  ! trading:lead: ") {
		t.Errorf("the list does not say a block cannot be read:\n%s", text)
	}
	id := func(v view, from string) string {
		for _, r := range v.Open {
			if r.From == project(from) {
				return r.ID
			}
		}
		t.Fatalf("no open row from %s in %+v", from, v.Open)
		return ""
	}
	mapID, axiID, falconID, angularID, uploadID := id(v, "appxite-map:lead"), id(v, "axi:leader"), id(v, "falcon:lead"),
		id(v, "angular-91260:lead"), id(v, "upload-central:lead")
	cli(t, h, "answer", mapID, "yes, split it")
	write("angular-91260:lead", block(lead{"angular-91260:lead", "2026-10-30 14:00", "nothing"}))
	write("falcon:lead", block(lead{"falcon:lead", "2026-10-30 14:00", "Q2 mail folders"}))
	write("axi:leader", "## GOAL (2026-10-30 13:4x)\n")
	toml("upload-central:lead")
	v = list()
	if len(v.Answered) != 1 || v.Answered[0].ID != mapID || v.Answered[0].Answer != "yes, split it" {
		t.Errorf("want only the owner's answer of %s under answered today, got %+v", mapID, v.Answered)
	}
	state := rows(t, h)
	for _, id := range []string{angularID, falconID, uploadID} {
		if state[id] != "withdrawn the GOAL block no longer asks this" {
			t.Errorf("%s: want withdrawn by its lead, got %q", id, state[id])
		}
	}
	got = map[string]string{}
	for _, r := range v.Open {
		got[r.From] = r.ID + " " + r.Line
	}
	if len(v.Open) != len(asks)-3 || got["axi"] != axiID+" "+asks[4].needs || !strings.HasSuffix(got["falcon"], " Q2 mail folders") ||
		got["falcon"] == falconID+" Q2 mail folders" || got["appxite-map"] != "" {
		t.Errorf("want %d open rows: axi's kept while its block cannot be read, falcon's new text a new row, map's"+
			" answered; got %+v", len(asks)-3, got)
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if v2 := list(); len(v2.Open) != len(v.Open) {
		t.Errorf("a projects.toml with no lead changed the open rows: %+v then %+v", v.Open, v2.Open)
	}
	toml("upload-central:lead")
	if v2 := list(); len(v2.Open) != len(v.Open) || len(v2.Answered) != len(v.Answered) {
		t.Errorf("a second read changed the queue: %+v then %+v", v, v2)
	}
}

// project is the From of a lead's rows, as the bus rows name it.
func project(lead string) string {
	p, _, _ := strings.Cut(lead, ":")
	if p == "crius" {
		return "Crius"
	}
	return p
}

// rows gives each row's state and answer from the queue file, withdrawn ones included.
func rows(t *testing.T, h string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h, "state", "owner-queue.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r struct{ ID, State, Answer string }
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out[r.ID] = r.State + " " + r.Answer
	}
	return out
}

// Review of the queue gap, P1: a lead that asks the owner on the bus and in its GOAL block is asked once. Its GOAL ask
// is held while its bus row is open, and while its block is older than the owner's answer (10-30: the owner answered
// ado-94205's drafts at 15:46 and its block still asked them; a Crius relay "owner: go" is the owner's answer too); the
// list names it, and it opens once the block is written again. A row Crius closed itself ("settled: …", 10-30 15:14 on
// axi's disk row) holds nothing: axi's PROD-HANDOVER question stays on show.
func TestAGoalAskTheQueueShowsOrTheOwnerAnsweredIsHeld(t *testing.T) {
	h := setup(t, "2026-10-30T11:05 NEED-OWNER ado-94205:lead -> crius — AB#94205: OK the two new comment drafts\n"+
		"2026-10-30T11:06 NEED-OWNER upload-central:lead -> crius — B4: svc_file (1) or each service (2)\n")
	status := map[string]string{}
	goal := func(name, stamp, needs string) {
		t.Helper()
		status[name] = filepath.Join(h, strings.ReplaceAll(name, ":", "-")+".md")
		if err := os.WriteFile(status[name], []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1"+
			" milestones)\nNext: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goal("ado-94205:lead", "2026-10-30 11:02", "OK on the two comment drafts (never post without it)")
	goal("upload-central:lead", "2026-10-30 09:40", "B4 — who stores and serves the file")
	goal("axi:leader", "2026-10-30 13:02", "PROD-HANDOVER.md 12.2-12.8")
	goal("marketplace:lead", "2026-10-30 13:37", "go on BUILD-PLAN.md.")
	if err := os.WriteFile(filepath.Join(h, "bus", "mkt.log"), []byte("2026-10-30T12:00 NEED-OWNER axi:leader -> crius — disk 17 GiB free\n"+
		"2026-10-30T12:01 NEED-OWNER marketplace:lead -> crius — Go on the Microsoft marketplace build plan\n"+
		"2026-10-30T15:14 DECISION crius:lead -> axi:leader — [queue q3] settled 10-30 12:38: Crius removed the volumes\n"+
		"2026-10-30T15:18 DECISION crius:lead -> marketplace:lead — [queue q4] owner: Go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var toml strings.Builder
	for _, l := range []string{"ado-94205:lead", "axi:leader", "marketplace:lead", "upload-central:lead"} {
		p, role, _ := strings.Cut(l, ":")
		fmt.Fprintf(&toml, "[projects.%s]\nleads = { %s = %q }\n\n", p, role, status[l])
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(toml.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, h, "answer", "q1", "answered there")
	out := cli(t, h, "list")
	for _, want := range []string{"Waiting on you (2)", "B4: svc_file (1) or each service (2) · upload-central",
		"PROD-HANDOVER.md 12.2-12.8 · axi", "Held, as you may have answered them already (3):\n",
		"  · ado-94205:lead: the owner answered q1 at 2026-10-30T14:00, after its GOAL block (2026-10-30T11:02)\n",
		"  · marketplace:lead: the owner answered q4 at 2026-10-30T15:18, after its GOAL block (2026-10-30T13:37)\n",
		"  · upload-central:lead: its ask q2 is open in the queue\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	goal("ado-94205:lead", "2026-10-30 14:30", "OK on the reply to Andriy")
	if out := cli(t, h, "list"); !strings.Contains(out, "Waiting on you (3)") || !strings.Contains(out, "OK on the reply to Andriy · ado-94205") {
		t.Errorf("a block written after the answer does not open its ask:\n%s", out)
	}
}

// Review round 2 of the queue gap, P2s: an owner answer covers a GOAL ask only until the lead writes its block again
// (the same text asked again later opens a row), and a Crius relay "[queue qN] …" for a row its lead just withdrew is
// recorded on that row, never dropped.
func TestAGoalAskAgainOpensAndAnAnswerToAWithdrawnRowIsKept(t *testing.T) {
	h := setup(t, "")
	status := filepath.Join(h, "falcon.md")
	goal := func(stamp, needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.falcon]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 09:00", "approve the k3s deploy")
	cli(t, h, "list")
	cli(t, h, "answer", "q1", "yes")
	goal("2026-10-30 14:10", "nothing")
	cli(t, h, "list")
	goal("2026-10-30 14:30", "approve the k3s deploy")
	if out := cli(t, h, "list"); !strings.Contains(out, "Waiting on you (1), oldest first\n1 approve the k3s deploy · falcon · 0m · q2\n") {
		t.Errorf("the same ask in a block written after the answer opens no row:\n%s", out)
	}
	goal("2026-10-30 14:40", "approve the k3s deploy; the DNS change")
	cli(t, h, "list") // q2 withdrawn, q3 opens
	if err := os.WriteFile(filepath.Join(h, "bus", "crius.log"), []byte("2026-10-30T14:50 DECISION crius:lead -> falcon:lead — [queue q2] owner: go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, h, "list")
	if got := rows(t, h)["q2"]; got != "answered owner: go" {
		t.Errorf("q2: want the relayed answer kept, got %q", got)
	}
}

// Review round 3 of the queue gap, P2: "answer N" answers row N of the list last printed. A GOAL block rewritten
// since then (its row withdrawn, a new one added at the end) moves no number before the answer lands.
func TestAnswerByNumberIsTheListPrintedNotOneRereadFromTheGoals(t *testing.T) {
	h := setup(t, "2026-10-30T09:00 NEED-OWNER cortex:lead -> crius — approve the cortex merge\n")
	status := filepath.Join(h, "falcon.md")
	goal := func(needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL (2026-10-30 08:00)\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.falcon]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("Q1 CI as a source")
	cli(t, h, "list")
	cli(t, h, "add", "--title", "Restart Chrome", "--from", "Mac")
	// printed: 1 q1 cortex merge (09:00), 2 q2 falcon Q1 (14:00), 3 q3 Restart Chrome (14:00)
	if out := cli(t, h, "list"); !strings.Contains(out, "2 Q1 CI as a source · falcon · 0m · q2\n3 Restart Chrome · Mac · 0m · q3\n") {
		t.Fatalf("unexpected list:\n%s", out)
	}
	goal("Q1 CI as a source; Q2 mail folders")
	if got := cli(t, h, "answer", "3", "done"); got != "[queue q3] done\n" {
		t.Fatalf("answer 3 went to %q, want q3 (Restart Chrome)", got)
	}
}

// Crius 10-30 16:51: a parked lead wrote "nothing (owner …: parked; …)" and the pane showed it as row q16. That field
// asks nothing: no row opens, and the row its earlier ask opened is withdrawn by the lead.
func TestNothingWithANoteOpensNoRowAndWithdrawsTheAsk(t *testing.T) {
	h := setup(t, "")
	status := filepath.Join(h, "gains.md")
	goal := func(stamp, needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.appxite-gains]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 14:00", "walk-through of GAINS.md")
	cli(t, h, "list")
	goal("2026-10-30 16:44", "nothing (owner 2026-10-30 16:44: parked; GAINS.md walk-through when the owner resumes it)")
	if out := cli(t, h, "list"); !strings.Contains(out, "✓ Nothing waits on you.") {
		t.Errorf("a parked lead's nothing (note) still waits on the owner:\n%s", out)
	}
	if got := rows(t, h); len(got) != 1 || got["q1"] != "withdrawn the GOAL block no longer asks this" {
		t.Errorf("want only q1, withdrawn by the lead: %v", got)
	}
}

// Crius 10-30 16:58: the owner answered three cards with a question back, and the queue closed them as answered (q8,
// q13, q21). An owner answer that asks back (ends in "?", or opens with what/why/how) keeps the card open, marked with
// the question; the line for Crius is printed as for any answer, and the next answer that does not ask back closes it.
func TestAnAnswerThatAsksBackKeepsTheCardOpen(t *testing.T) {
	h := setup(t, "")
	asked := []string{ // the owner's three answers, 16:44-16:55
		`i dont know what is this,the ui just says"1 the 3 questions in STATUS-2026-10-28.md section 6" so what should i answer?`,
		"what you need?",
		"what does this means, we ahve post order etc that works in v2, isnt it correct?",
	}
	for i, a := range asked {
		id := cli(t, h, "add", "--title", fmt.Sprintf("card %d", i+1), "--from", "axi")
		if got := cli(t, h, "answer", strings.TrimSpace(id), a); got != "[queue "+strings.TrimSpace(id)+"] "+a+"\n" {
			t.Errorf("the line for Crius: got %q", got)
		}
	}
	cli(t, h, "answer", "q1", "how should I read section 6")
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 3 || len(v.Answered) != 0 {
		t.Fatalf("want 3 cards open, asked back: %+v", v)
	}
	for i, r := range v.Open {
		want := asked[i]
		if i == 0 {
			want = "how should I read section 6"
		}
		if r.Asked != want || r.AskedAt != "2026-10-30T14:00" || r.Answer != "" {
			t.Errorf("%s: asked %q at %q, answer %q; want the owner's last question", r.ID, r.Asked, r.AskedAt, r.Answer)
		}
	}
	if out := cli(t, h, "list"); !strings.Contains(out, "1 card 1 · axi · 0m · q1\n    You asked how should I read section 6 (14:00)\n") {
		t.Errorf("the list shows no question:\n%s", out)
	}
	cli(t, h, "answer", "q2", "Q1 yes, Q2 Inbox only")
	if got := rows(t, h)["q2"]; got != "answered Q1 yes, Q2 Inbox only" {
		t.Errorf("q2: an answer after the question closes it, got %q", got)
	}
	if err := run(h, now, []string{"answer", "q2", "why?"}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "q2 is already answered") {
		t.Errorf("an answered card takes no question: %v", err)
	}
}

// Review of 8cdefa3: P1, Crius forwarding the owner's question as "[queue qN] owner asks: …?" closed the card again;
// P2, an option button never asks back, a "?" before a closing bracket or quote still asks, and a GOAL row the lead
// rewrites after the owner asked back carries the question to its new row.
func TestAQuestionBackSurvivesCriusForwardingItAndALeadRewrite(t *testing.T) {
	h := setup(t, "")
	cli(t, h, "add", "--title", "Falcon: 3 choices", "--from", "falcon")
	cli(t, h, "add", "--title", "Release the tag", "--from", "aitch", "--options", "Ship it?|Not now")
	cli(t, h, "add", "--title", "Order shadow", "--from", "axi")
	cli(t, h, "answer", "q1", "what you need?")
	cli(t, h, "answer", "q2", "Ship it?")
	cli(t, h, "answer", "q3", "we have post order that works in v2 (isnt it correct?)")
	bus := "2026-10-30T14:05 INFO crius:lead -> falcon:lead — [queue q1] owner asks: what you need?\n"
	if err := os.WriteFile(filepath.Join(h, "bus", "crius.log"), []byte(bus), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, h, "list")
	got := rows(t, h)
	if got["q1"] != "open " || got["q2"] != "answered Ship it?" || got["q3"] != "open " {
		t.Fatalf("want q1 open after the forward, q2 answered by its button, q3 open: %v", got)
	}
	bus += "2026-10-30T14:20 DECISION crius:lead -> falcon:lead — [queue q1] owner: Q1 a, Q2 a\n"
	if err := os.WriteFile(filepath.Join(h, "bus", "crius.log"), []byte(bus), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, h, "list")
	if got := rows(t, h)["q1"]; got != "answered owner: Q1 a, Q2 a" {
		t.Errorf("q1: Crius's relayed decision closes it, got %q", got)
	}

	status := filepath.Join(h, "angular.md")
	goal := func(stamp, needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.angular-91260]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 13:00", "the 3 questions in STATUS-2026-10-28.md section 6")
	cli(t, h, "list") // q4
	cli(t, h, "answer", "q4", "so what should i answer?")
	goal("2026-10-30 14:30", "Q1 start now or after the release; Q2 fix the tests first")
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	var q5 *Item
	for i := range v.Open {
		if v.Open[i].ID == "q5" {
			q5 = &v.Open[i]
		}
	}
	if q5 == nil || q5.Asked != "so what should i answer?" || q5.AskedAt != "2026-10-30T14:00" {
		t.Errorf("the rewritten ask's row carries no question: %+v", q5)
	}
}

// Review round 2 of the question back: a Crius line asks back only when it says "owner asks" (with or without a "?");
// any other Crius line answers, even one that opens with How or ends in "?)". An older forward read again never
// replaces a newer question. A question on a GOAL row its lead rewrote while the new ask is held reaches the row that
// opens later, once: a question carried to a row is not carried again.
func TestOnlyAnOwnerAsksForwardAsksBackAndTheCarryOutlivesAHold(t *testing.T) {
	h := setup(t, "")
	for _, title := range []string{"Falcon: 3 choices", "Ship the release", "Merge order"} {
		cli(t, h, "add", "--title", title, "--from", "aitch")
	}
	busLog := filepath.Join(h, "bus", "crius.log")
	bus := "2026-10-30T13:10 INFO crius:lead -> falcon:lead — [queue q1] owner asks: what you need\n" +
		"2026-10-30T13:11 DECISION crius:lead -> aitch:lead — [queue q2] How to ship: owner says B\n" +
		"2026-10-30T13:12 DECISION crius:lead -> aitch:lead — [queue q3] owner: go (merge #12 first?)\n"
	if err := os.WriteFile(busLog, []byte(bus), 0o644); err != nil {
		t.Fatal(err)
	}
	cli(t, h, "list")
	got := rows(t, h)
	if got["q1"] != "open " || got["q2"] != "answered How to ship: owner says B" || got["q3"] != "answered owner: go (merge #12 first?)" {
		t.Fatalf("want q1 asked back, q2 and q3 answered: %v", got)
	}
	cli(t, h, "answer", "q1", "and Q2, which folders?")
	cli(t, h, "init", "--since", "2026-10-30T00:00") // the bus is read again from the start
	cli(t, h, "list")
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 || v.Open[0].Asked != "and Q2, which folders?" {
		t.Errorf("the 13:10 forward read again replaced the 14:00 question: %+v", v.Open)
	}

	status := filepath.Join(h, "falcon.md")
	goal := func(stamp, needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.falcon]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 12:00", "Q1 CI as a source · NEED 10-29")
	cli(t, h, "list") // q4
	cli(t, h, "answer", "q4", "what you need?")
	falconLog := filepath.Join(h, "bus", "falcon.log")
	if err := os.WriteFile(falconLog, []byte("2026-10-30T14:00 NEED-OWNER falcon:lead -> owner — Q1: CI as a source, (a) or (b)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 14:00", "Q1 CI as a source, (a) or (b)")
	cli(t, h, "list") // q4 withdrawn, the bus row q5 opens, the new ask is held
	cli(t, h, "answer", "q5", "--by", "crius", "settled: duplicate of the GOAL ask")
	goal("2026-10-30 14:00", "Q1 CI as a source, (a) or (b)")
	v = View{} // a decode into a used View keeps the fields a row leaves out
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	var row *Item
	for i := range v.Open {
		if v.Open[i].Goal == "falcon:lead" {
			row = &v.Open[i]
		}
	}
	if row == nil || row.Asked != "what you need?" {
		t.Fatalf("the ask that opened after the hold carries no question: %+v", v.Open)
	}
	goal("2026-10-30 14:00", "Q1 CI as a source: (a)")
	v = View{} // a decode into a used View keeps the fields a row leaves out
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	for _, r := range v.Open {
		if r.Goal == "falcon:lead" && r.Asked != "" {
			t.Errorf("a carried question is carried again: %+v", r)
		}
	}
}

// A question carried to the lead's next row and settled there does not come back on a later ask (review round 3 of
// de39210, P2: askedOn took the newest withdrawn row, so q1's "why?" came back on q3).
func TestASettledQuestionIsNotCarriedAgain(t *testing.T) {
	h := setup(t, "")
	status := filepath.Join(h, "axi.md")
	goal := func(stamp, needs string) {
		t.Helper()
		if err := os.WriteFile(status, []byte("## GOAL ("+stamp+")\nGoal: g\n- [x] one · abc1234\nDone: 100% (1 of 1 milestones)\n"+
			"Next: none\nNeeds owner: "+needs+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.axi]\nleads = { lead = %q }\n", status)), 0o644); err != nil {
		t.Fatal(err)
	}
	goal("2026-10-30 12:00", "OK to merge A")
	cli(t, h, "list") // q1
	cli(t, h, "answer", "q1", "why?")
	goal("2026-10-30 12:30", "OK to merge A: it fixes the 5 failing tests")
	cli(t, h, "list") // q1 withdrawn, q2 carries "why?"
	cli(t, h, "answer", "q2", "yes")
	goal("2026-10-30 13:00", "OK to deploy B")
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 || v.Open[0].Title != "OK to deploy B" || v.Open[0].Asked != "" {
		t.Errorf("q3 carries the question settled on q2: %+v", v.Open)
	}
}

// Crius 10-30 16:45: q8 showed only "the 3 questions in STATUS-2026-10-28.md section 6", with no project and no
// question. A GOAL row is titled with the project's plain name and carries the questions its ask points to, read in
// once when the row opens.
func TestAGoalRowNamesTheProjectAndCarriesTheQuestions(t *testing.T) {
	h := setup(t, "")
	repo := filepath.Join(h, "svc_sku_builder-91260")
	section := filepath.Join(repo, "docs/specs/91260-angular-22-upgrade/STATUS-2026-10-28.md")
	questions := "## 6. Questions for the owner\n\n1. **94489 has not started. Should the upgrade keep waiting for it?**\n" +
		"2. **Should Step 0 fix the failing tests, or only record them?**\n3. Close the spike and open the upgrade ticket?\n"
	status := filepath.Join(h, "state", "angular-91260", "STATUS.md")
	for path, text := range map[string]string{
		section: "# Angular 22 plan\n## 5. Steps\nStep 0 first.\n" + questions + "## 7. Next\nnone\n",
		status: "## GOAL (2026-10-30 12:00)\nGoal: Angular 22\n- [x] spike · abc1234\nDone: 20% (1 of 5 milestones)\n" +
			"Next: Step 0\nNeeds owner: the 3 questions in STATUS-2026-10-28.md section 6\n",
		filepath.Join(h, "projects.toml"): fmt.Sprintf("[projects.angular-91260]\nname = \"Angular 22 upgrade of the SKU builder (AB#91260)\"\n"+
			"cwd = [%q]\nleads = { lead = %q }\n", repo, status),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var v struct {
		Open []map[string]any `json:"open"`
	}
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 {
		t.Fatalf("want the q8 row: %+v", v.Open)
	}
	r := v.Open[0]
	if r["title"] != "Angular 22 upgrade of the SKU builder (AB#91260): the 3 questions in STATUS-2026-10-28.md section 6" || r["from"] != "angular-91260" {
		t.Errorf("q8 names no project in plain words: title %q, from %q", r["title"], r["from"])
	}
	detail, _ := r["detail"].(string)
	for _, want := range []string{"1. 94489 has not started. Should the upgrade keep waiting for it?",
		"2. Should Step 0 fix the failing tests, or only record them?", "3. Close the spike", "(from " + section + " section 6)"} {
		if !strings.Contains(detail, want) {
			t.Errorf("q8 does not carry %q: detail %q", want, detail)
		}
	}
	if strings.Contains(detail, "Step 0 first") || strings.Contains(detail, "## 7.") {
		t.Errorf("q8 carries more than section 6: %q", detail)
	}
	if got := cli(t, h, "list"); strings.Contains(got, "    Do ") || !strings.Contains(got, "    Details   ## 6. Questions for the owner\n") ||
		!strings.Contains(got, "\n              1. 94489 has not started.") {
		t.Errorf("list does not show the questions:\n%s", got)
	}
	if err := os.WriteFile(section, []byte("## 6. Questions for the owner\nall answered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cli(t, h, "list"); !strings.Contains(got, "1. 94489 has not started.") {
		t.Errorf("the open row read its file again on a later list:\n%s", got)
	}
}

// A lead whose status file is a git object (crius:build, "git:<repo>#origin/main:docs/notes/RESUME.md") has its labels
// read from that object, as its GOAL block is, not from a checkout on another branch (review of f1c57f3, P2).
// Reviews 1 and 2 of f1c57f3: the checkout of a git-object lead's repo is on another branch; its labels and the files its
// ask names are read from the object.
func TestAGitObjectLeadReadsItsLabelFromTheObject(t *testing.T) {
	h := setup(t, "")
	repo := filepath.Join(h, "crius")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	resume := filepath.Join(repo, "docs/notes/RESUME.md")
	if err := os.MkdirAll(filepath.Dir(resume), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resume, []byte("## GOAL (2026-10-30 12:00)\nGoal: g\n- [x] one · abc1234\nDone: 50% (1 of 2 milestones)\nNext: two\n"+
		"Needs owner: yes or no on the split (see \"Open with owner\"; PLAN.md section 3)\n\nOpen with owner: should the split ship today?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := filepath.Join(repo, "docs/notes/PLAN.md")
	if err := os.WriteFile(plan, []byte("## 3. Split\nShip the split today, or after the review?\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	git("add", ".")
	git("commit", "-q", "-m", "goal")
	if err := os.WriteFile(resume, []byte("## GOAL\nOpen with owner: an old question from another branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan, []byte("## 3. Split\nAn old plan from another branch.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h, "projects.toml"), []byte(fmt.Sprintf("[projects.crius]\nleads = { build = \"git:%s#main:docs/notes/RESUME.md\" }\n", repo)), 0o644); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Open []map[string]any `json:"open"`
	}
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if want := "## 3. Split\nShip the split today, or after the review?\n(from " + repo + " main:docs/notes/PLAN.md section 3)\n\n" +
		"Open with owner: should the split ship today?\n(from " + repo + " main:docs/notes/RESUME.md)"; len(v.Open) != 1 || v.Open[0]["detail"] != want {
		t.Errorf("want detail %q: %+v", want, v.Open)
	}
}
