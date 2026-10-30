package main

import (
	"encoding/json"
	"fmt"
	"os"
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
