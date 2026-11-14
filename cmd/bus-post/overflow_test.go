package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/dispatch"
	"github.com/orzazade/crius/internal/store"
)

// Over 10 days (11-05 to 11-15) agents hit "text is N chars; the limit is 300" about 150 times in 21 sessions, each a
// retry round trip. A long text is now posted cut, with the whole text in a note file (overflow.go).

var pointerRe = regexp.MustCompile(`full: (\S+)$`)

// words gives a text of n runes or more made of short words, so a cut can land on a space.
func words(prefix string, n int) string {
	var b strings.Builder
	b.WriteString(prefix)
	for i := 0; utf8.RuneCountInString(b.String()) < n; i++ {
		b.WriteString(" word")
		b.WriteString(string(rune('a' + i%26)))
	}
	return b.String()
}

// postedText parses the one line on the bus strictly and gives its text, the note it points to and the note's content.
func postedText(t *testing.T, log string) (text, note, full string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(busText(t, log), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one bus line, got %d: %q", len(lines), lines)
	}
	l := store.ParseLine(lines[0], nil, true)
	if l == nil {
		t.Fatalf("the posted line does not parse back: %q", lines[0])
	}
	m := pointerRe.FindStringSubmatch(l.Text)
	if m == nil {
		t.Fatalf("no note pointer at the end of %q", l.Text)
	}
	b, err := os.ReadFile(m[1])
	if err != nil {
		t.Fatal(err)
	}
	return l.Text, m[1], string(b)
}

func TestALongTextIsPostedCutWithTheWholeTextInANote(t *testing.T) {
	_, log := setup(t)
	text := words("DONE row F-12: the long report", 420)
	rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text)
	if rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, note, full := postedText(t, log)
	if n := utf8.RuneCountInString(got); n > store.MaxLen {
		t.Fatalf("posted text is %d runes, over %d: %q", n, store.MaxLen, got)
	}
	if strings.TrimSuffix(full, "\n") != text {
		t.Fatalf("the note does not hold the whole text:\n%q\n%q", full, text)
	}
	if filepath.Dir(note) != filepath.Join(filepath.Dir(log), "notes") ||
		!regexp.MustCompile(`^crius-20261115-0312-[a-z2-7]{6}\.txt$`).MatchString(filepath.Base(note)) {
		t.Fatalf("note path %s", note)
	}
	if !strings.Contains(errs, "overflow") || !strings.Contains(errs, note) {
		t.Fatalf("stderr does not say it overflowed and where the note is: %q", errs)
	}
}

func TestACutLandsOnAWordBoundary(t *testing.T) {
	_, log := setup(t)
	text := words("start", 500)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, _ := postedText(t, log)
	head, _, ok := strings.Cut(got, " … ")
	if !ok || !strings.HasPrefix(text, head) || len(head) == len(text) || text[len(head)] != ' ' {
		t.Fatalf("head %q is not the text cut before a space", head)
	}
	// The words are 6 runes with their space, so a cut on a boundary leaves less than that unused.
	if n := utf8.RuneCountInString(got); n <= store.MaxLen-6 {
		t.Fatalf("posted text is %d runes; the cut wastes room: %q", n, got)
	}
}

// A VERIFIED is read whole: bus-post finds its sha from the word after its ref and its run log from any path in the
// text. The owner queue builds a NEED-OWNER's row, why and option buttons from its text, and the playbook takes a
// LEARNED's text as the lesson. A cut would break each, so they stay refused.
func TestLongStrictTypesAreStillRefused(t *testing.T) {
	for typ, text := range map[string]string{
		"VERIFIED":   words("row F-12 · abc1234 · ~/.crius/runs/x.log", 400),
		"NEED-OWNER": words("Pick the cache. Why: long reason", 380) + " Options: keep | drop | defer",
		"LEARNED":    words("when a lesson is long", 400),
	} {
		_, log := setup(t)
		rc, errs := post(t, at("2026-11-15T03:12"), log, typ, "crius:lead", "crius:build", text)
		if rc != 2 || !strings.Contains(errs, "the limit is 300") || !strings.Contains(errs, typ+" is never cut") ||
			busText(t, log) != "" {
			t.Fatalf("%s: rc=%d errs=%q bus=%q", typ, rc, errs, busText(t, log))
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(log), "notes")); !os.IsNotExist(err) {
			t.Fatalf("a refused %s left a notes folder: %v", typ, err)
		}
	}
}

func TestALongQueueAnswerKeepsItsTagFirst(t *testing.T) {
	_, log := setup(t)
	text := words("[queue q7] owner: go with the second option", 400)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "DECISION", "crius:lead", "quotes:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	if got, _, _ := postedText(t, log); !strings.HasPrefix(got, "[queue q7] owner: go with the second option") {
		t.Fatalf("tag lost: %q", got)
	}
}

func TestTwoLongPostsInOneMinuteGetTwoNotes(t *testing.T) {
	_, log := setup(t)
	text := words("same text", 400)
	for range 2 {
		if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
			t.Fatalf("rc=%d: %s", rc, errs)
		}
	}
	notes, _ := filepath.Glob(filepath.Join(filepath.Dir(log), "notes", "crius-20261115-0312-*.txt"))
	if len(notes) != 2 {
		t.Fatalf("want 2 notes, got %v (bus %q)", notes, busText(t, log))
	}
	lines := strings.Split(strings.TrimSpace(busText(t, log)), "\n")
	if len(lines) != 2 || lines[0] == lines[1] {
		t.Fatalf("want two lines with different pointers: %q", lines)
	}
}

// Refs the bus parser reads and shas that fall in the cut part are carried after the head, in order: ingest links items
// and events by them, and a READY's first sha must stay its first.
func TestRefsAndShasInTheCutPartAreCarried(t *testing.T) {
	_, log := setup(t)
	text := words("first", 350) + " item=crius#41 sha=abc1234 then pr=77 and quotes-D12 at deadbeef42 path=/tmp/x.log end"
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "DONE", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, _ := postedText(t, log)
	want, have := store.RefsOf(text), store.RefsOf(got)
	for k, v := range want {
		if !slices.Equal(have[k], v) {
			t.Fatalf("ref %s: want %v, got %v in %q", k, v, have[k], got)
		}
	}
	if !slices.Equal(hexWords(got), hexWords(text)) {
		t.Fatalf("hex words: want %v, got %v in %q", hexWords(text), hexWords(got), got)
	}
}

func TestATextAtTheLimitIsPostedWhole(t *testing.T) {
	_, log := setup(t)
	text := strings.Repeat("é", store.MaxLen)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	if !strings.HasSuffix(busText(t, log), "— "+text+"\n") {
		t.Fatalf("changed: %q", busText(t, log))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(log), "notes")); !os.IsNotExist(err) {
		t.Fatalf("a text at the limit made a note: %v", err)
	}
}

// dispatch moves a board row on a DONE's "PR 4567" and blocks it on PARTIAL (dispatch.TextTokens); both are carried.
func TestADonesPRAndPartialInTheCutPartAreCarried(t *testing.T) {
	_, log := setup(t)
	text := words("item done", 330) + " ended PARTIAL, opened PR 4567 for review"
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "DONE", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, _ := postedText(t, log)
	for _, re := range dispatch.TextTokens {
		if !slices.Equal(re.FindAllString(got, -1), re.FindAllString(text, -1)) {
			t.Fatalf("%s: want %v, got %v in %q", re, re.FindAllString(text, -1), re.FindAllString(got, -1), got)
		}
	}
}

// Many refs past the cut that do not all fit: the line keeps those that fit and says how many more the note holds,
// never a refusal.
func TestTooManyRefsToCarryArePostedWithACount(t *testing.T) {
	_, log := setup(t)
	text := words("batch gate", 320)
	for i := range 8 {
		text += fmt.Sprintf(" sha=%040x", 0xabc0+i)
	}
	rc, errs := post(t, at("2026-11-15T03:12"), log, "DONE", "crius:build", "crius:lead", text)
	if rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, full := postedText(t, log)
	if utf8.RuneCountInString(got) > store.MaxLen || !regexp.MustCompile(`\+\d+ more in the note`).MatchString(got) ||
		strings.TrimSuffix(full, "\n") != text {
		t.Fatalf("got %q", got)
	}
}

// A path= ref that ends the head is not glued to the cut mark: it parses back as the same path.
func TestARefThatEndsTheHeadParsesBackWhole(t *testing.T) {
	_, log := setup(t)
	head := words("x", 200)
	text := head + " path=/tmp/run.log " + strings.Repeat("y", 200)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, _ := postedText(t, log)
	if p := store.RefsOf(got)["path"]; !slices.Equal(p, []string{"/tmp/run.log"}) {
		t.Fatalf("path refs %v in %q", p, got)
	}
}

// Invalid UTF-8 counts one rune a byte, as the limit check does; the cut is on bytes, so it cannot slice out of range.
func TestALongTextWithInvalidBytesIsCutSafely(t *testing.T) {
	_, log := setup(t)
	text := strings.Repeat("\xff", 150) + " sha=abc1234 " + strings.Repeat("\xfe", 200)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, full := postedText(t, log)
	if utf8.RuneCountInString(got) > store.MaxLen || !slices.Equal(store.RefsOf(got)["sha"], []string{"abc1234"}) ||
		strings.TrimSuffix(full, "\n") != text {
		t.Fatalf("got %q", got)
	}
}

// A refusal after the note is written takes the note back: no note without its line. A log path that does not exist
// makes no notes folder (a typo would start one nobody reads).
func TestARefusedLongLineLeavesNoNote(t *testing.T) {
	_, log := setup(t)
	text := words("long", 400)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "BOGUS", "crius:build", "crius:lead", text); rc != 2 ||
		!strings.Contains(errs, "unknown bus type") {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	if notes, _ := filepath.Glob(filepath.Join(filepath.Dir(log), "notes", "*")); len(notes) != 0 {
		t.Fatalf("notes left: %v", notes)
	}
	typo := filepath.Join(filepath.Dir(log), "bsu", "crius.log")
	if rc, errs := post(t, at("2026-11-15T03:12"), typo, "INFO", "crius:build", "crius:lead", text); rc != 2 ||
		!strings.Contains(errs, "no bus log") {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	if _, err := os.Stat(filepath.Dir(typo)); !os.IsNotExist(err) {
		t.Fatalf("a typo path got a folder: %v", err)
	}
}

// A long text with no space in reach is cut hard and still fits.
func TestALongTextWithNoSpaceIsCutHard(t *testing.T) {
	_, log := setup(t)
	text := strings.Repeat("ж", 400)
	if rc, errs := post(t, at("2026-11-15T03:12"), log, "INFO", "crius:build", "crius:lead", text); rc != 0 {
		t.Fatalf("rc=%d: %s", rc, errs)
	}
	got, _, _ := postedText(t, log)
	head, _, _ := strings.Cut(got, " … ")
	if utf8.RuneCountInString(got) > store.MaxLen || !strings.HasPrefix(text, head) || len(head) == 0 {
		t.Fatalf("got %q", got)
	}
}
