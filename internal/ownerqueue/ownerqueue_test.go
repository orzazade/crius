package ownerqueue

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)

func home(t *testing.T, logs map[string]string) string {
	t.Helper()
	h := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, "bus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range logs {
		if err := os.WriteFile(filepath.Join(h, "bus", name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func appendTo(t *testing.T, h, name, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(h, "bus", name), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, q *Queue) []Row {
	t.Helper()
	var out []Row
	for _, r := range q.Rows {
		if r.State == Open {
			out = append(out, r)
		}
	}
	return out
}

// The owner's rule (10-30): every NEED-OWNER bus line adds a row by itself, so an empty pane means nothing waits.
func TestANeedOwnerLineAddsARow(t *testing.T) {
	h := home(t, map[string]string{"upload-central.log": "2026-10-30T13:40 NEED-OWNER upload-central:lead -> crius — Q1(c) decision page ready: pick A or C. why: the team splits on it. unblocks: phase 2. options: A | C\n"})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	rows := open(t, q)
	if len(rows) != 1 {
		t.Fatalf("want 1 open row, got %d: %+v", len(rows), q.Rows)
	}
	r := rows[0]
	if r.ID != "q1" || r.From != "upload-central" || r.Opened != "2026-10-30T13:40" || r.Title != "Q1(c) decision page ready" ||
		r.Why != "the team splits on it." || r.Unblocks != "phase 2." || strings.Join(r.Options, "|") != "A|C" {
		t.Fatalf("row fields wrong: %+v", r)
	}
	if !strings.HasPrefix(r.Do, "Q1(c) decision page ready: pick A or C.") {
		t.Fatalf("Do = %q", r.Do)
	}
	// a second sync adds nothing new, and a line appended later is picked up
	appendTo(t, h, "upload-central.log", "2026-10-30T13:50 NEED-OWNER upload-central:lead -> crius — second one\n")
	q, err = List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if rows := open(t, q); len(rows) != 2 || rows[1].ID != "q2" || rows[1].Title != "second one" {
		t.Fatalf("want q1 and q2 open, got %+v", q.Rows)
	}
}

// Lines that only mention NEED-OWNER, lines before the start, and a half-written last line add nothing.
func TestOnlyNeedOwnerLinesFromTheStartOn(t *testing.T) {
	h := home(t, map[string]string{"tamada.log": strings.Join([]string{
		"2026-10-19T03:05 NEED crius:lead -> tamada:leader — Post as NEED-OWNER with a yes/no recommendation.",
		"2026-10-29T16:53 ACK axi:leader -> crius:lead — Card 3 stays NEED-OWNER with you",
		"2026-10-29T23:59 NEED-OWNER tamada:leader -> crius — before the start",
		"23:15 NEED-OWNER tamada:leader -> crius — undated, so before the start",
		"- 2026-10-30T09:15 NEED-OWNER tamada:leader -> crius — written as a list item",
		"2026-10-30T09:16 NEED-OWNER tamada:leader -> crius — half written",
	}, "\n")})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if rows := open(t, q); len(rows) != 1 || rows[0].Title != "written as a list item" {
		t.Fatalf("want only the list item, got %+v", q.Rows)
	}
	appendTo(t, h, "tamada.log", " now\n")
	q, err = List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if rows := open(t, q); len(rows) != 2 || rows[1].Title != "half written now" {
		t.Fatalf("the finished line should be a row now, got %+v", q.Rows)
	}
}

// A Crius DECISION/INFO/OWNER line tagged [queue qN] answers that row; one from a lead, or untagged, does not.
func TestACriusLineTaggedWithTheRowAnswersIt(t *testing.T) {
	h := home(t, map[string]string{"falcon.log": "2026-10-30T10:00 NEED-OWNER falcon:lead -> crius — k3s change OK?\n2026-10-30T10:01 NEED-OWNER falcon:lead -> crius — second\n"})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := List(h, now); err != nil {
		t.Fatal(err)
	}
	appendTo(t, h, "falcon.log", strings.Join([]string{
		"2026-10-30T10:05 INFO falcon:lead -> crius:lead — [queue q2] a lead cannot close it",
		"2026-10-30T10:06 DECISION crius:lead -> falcon:lead — owner said yes, untagged",
		"2026-10-30T10:07 DECISION crius:lead -> falcon:lead — [queue q1] Yes, apply it",
		"",
	}, "\n"))
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Rows) != 2 || q.Rows[0].State != Answered || q.Rows[0].Answer != "Yes, apply it" || q.Rows[0].AnsweredAt != "2026-10-30T10:07" ||
		q.Rows[0].AnsweredBy != "crius:lead" || q.Rows[1].State != Open {
		t.Fatalf("want q1 answered by Crius and q2 open, got %+v", q.Rows)
	}
}

// Crius adds a row itself and then posts the NEED-OWNER with its tag: one row, not two.
func TestATaggedNeedOwnerLineIsTheSameRow(t *testing.T) {
	h := home(t, map[string]string{"crius.log": ""})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	id, err := Add(h, now, Row{Title: "Restart Chrome", Do: "Quit Chrome fully, open it again.", Why: "Crius never drives your Chrome.", Unblocks: "Browser checks", From: "Mac", Options: []string{"Done", "Remind me at 18:00"}})
	if err != nil || id != "q1" {
		t.Fatalf("add: %q %v", id, err)
	}
	appendTo(t, h, "crius.log", "2026-10-30T14:01 NEED-OWNER crius:lead -> owner — [queue q1] Restart Chrome\n")
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Rows) != 1 || q.Rows[0].Opened != "2026-10-30T14:00" {
		t.Fatalf("want the one row Crius added, got %+v", q.Rows)
	}
}

// The owner's answer from the pane: recorded once; a second answer to the same row is refused, so two presses never
// send Crius two answers.
func TestAnAnswerIsRecordedOnce(t *testing.T) {
	h := home(t, map[string]string{"crius.log": ""})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	id, _ := Add(h, now, Row{Title: "Start a build lead", From: "Crius"})
	if err := Answer(h, now.Add(time.Minute), id, "Yes, start it", "owner"); err != nil {
		t.Fatal(err)
	}
	if err := Answer(h, now.Add(2*time.Minute), id, "Not now", "owner"); err == nil || !strings.Contains(err.Error(), "already answered") {
		t.Fatalf("a second answer must be refused, got %v", err)
	}
	if err := Answer(h, now, "q9", "x", "owner"); err == nil || !strings.Contains(err.Error(), "no row q9") {
		t.Fatalf("an unknown row must be refused, got %v", err)
	}
	q, _ := List(h, now)
	if q.Rows[0].Answer != "Yes, start it" || q.Rows[0].AnsweredAt != "2026-10-30T14:01" {
		t.Fatalf("got %+v", q.Rows[0])
	}
}

// No start point means the queue was never set up: that is an error, never an empty list ("found nothing" is not
// "failed to look").
func TestAQueueNeverSetUpIsAnError(t *testing.T) {
	h := home(t, nil)
	if _, err := List(h, now); err == nil || !strings.Contains(err.Error(), "crius-queue init") {
		t.Fatalf("want a set-up error, got %v", err)
	}
}

// The rule over every real bus log (owner 10-30: test it over every log, not one example). Every line whose type is
// NEED-OWNER, by a pattern written apart from the parser, is one row; no other line is.
func TestEveryNeedOwnerLineInEveryBusLogIsARow(t *testing.T) {
	u, _ := os.UserHomeDir()
	logs, _ := filepath.Glob(filepath.Join(u, ".crius", "bus", "*.log"))
	if len(logs) == 0 {
		t.Skip("no bus logs on this machine")
	}
	typ := regexp.MustCompile(`^(?:[-*] )?(?:\d{4}-\d\d-\d\d[T ])?\d\d:\d\dZ? NEED-OWNER `)
	h := home(t, nil)
	want := map[string]bool{}
	for _, l := range logs {
		b, err := os.ReadFile(l)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h, "bus", filepath.Base(l)), b, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			if typ.MatchString(line) && !tagRe.MatchString(line) {
				want[filepath.Base(l)+"\n"+line] = true
			}
		}
	}
	if len(want) == 0 {
		t.Fatal("positive control: the bus logs hold no NEED-OWNER line, so this test proves nothing")
	}
	if err := Init(h, ""); err != nil { // no start point: every line, the undated ones too
		t.Fatal(err)
	}
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range q.Rows {
		got[r.Log+"\n"+r.Line] = true
		if r.Title == "" || r.From == "" {
			t.Errorf("row %s has no title or sender: %q", r.ID, r.Line)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("no row for %q", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("a row for a line that is no NEED-OWNER: %q", k)
		}
	}
	t.Logf("%d NEED-OWNER lines in %d bus logs, %d rows", len(want), len(logs), len(q.Rows))
}

// A title is the first clause, not a label: real NEED-OWNER texts of 10-29 gave "What", "4th" and "owner OK asked (RULES 2.6".
func TestTitleIsTheFirstClauseNotALabel(t *testing.T) {
	for in, want := range map[string]string{
		"What: ArgoCD app env004 has not deployed. Argo is stuck":                                         "ArgoCD app env004 has not deployed",
		"4th: CPQ files go public. more":                                                                  "4th: CPQ files go public",
		"owner OK asked (RULES 2.6: guards need it): at my request qa …":                                  "owner OK asked (RULES 2.6: guards need it)",
		"OK to download yolo11m-pose.pt? It is 42 MB":                                                     "OK to download yolo11m-pose.pt?",
		"Options: (A, my pick) keep exit 0, and make the reading mechanical; (B) let the Job fail loudly": "Options: (A, my pick) keep exit 0, and make the reading mechanical",
	} {
		if got := parse(in).Title; got != want {
			t.Errorf("title(%q) = %q, want %q", in, got, want)
		}
	}
}

// Review of 9bdb6c8: only a line that opens with the tag answers a row; one that names it in passing ("still waiting
// on the owner for [queue q1]") leaves it open.
func TestATagInPassingAnswersNothing(t *testing.T) {
	h := home(t, map[string]string{"falcon.log": "2026-10-30T10:00 NEED-OWNER falcon:lead -> crius — k3s change OK?\n"})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	appendTo(t, h, "falcon.log", "2026-10-30T10:05 INFO crius:lead -> falcon:lead — still waiting on the owner for [queue q1]\n")
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(open(t, q)) != 1 {
		t.Fatalf("a tag in passing closed the row: %+v", q.Rows)
	}
}

// Review of 9bdb6c8: a bus stamp in UTC ("10:00Z") is that time in UTC, so a NEED-OWNER at 23:30Z the day before is
// after a local since of 00:00 wherever local time is ahead of UTC, and its row opens at the local time.
func TestAUTCStampIsReadAsUTC(t *testing.T) {
	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local).UTC()
	h := home(t, map[string]string{"falcon.log": at.Format("2006-01-02T15:04") + "Z NEED-OWNER falcon:lead -> crius — k3s change OK?\n"})
	if err := Init(h, "2026-10-30T08:00"); err != nil {
		t.Fatal(err)
	}
	q, err := List(h, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.Rows) != 1 || q.Rows[0].Opened != "2026-10-30T08:00" {
		t.Fatalf("want one row opened 2026-10-30T08:00 local, got %+v", q.Rows)
	}
}

// Review of 9bdb6c8: with the rows file gone but the bus already read, ids would start again at q1 and old
// "[queue q1]" lines would close new rows; the queue says the rows are lost instead.
func TestLostRowsAreAnErrorNotAFreshQueue(t *testing.T) {
	h := home(t, map[string]string{"falcon.log": "2026-10-30T10:00 NEED-OWNER falcon:lead -> crius — k3s change OK?\n"})
	if err := Init(h, "2026-10-30T00:00"); err != nil {
		t.Fatal(err)
	}
	if _, err := List(h, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h, "state", "owner-queue.jsonl")); err != nil {
		t.Fatal(err)
	}
	if _, err := List(h, now); err == nil || !strings.Contains(err.Error(), "rows are lost") {
		t.Fatalf("want a rows-lost error, got %v", err)
	}
}
