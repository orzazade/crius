package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// The fixtures are a made-up project "acme" whose templates live on a box "box1".

const frTemplate = `You are **qa** on the **acme** team.

## Start here, in order
1. The team file.
5. Run one quick smoke check.
6. Then continue with: your 09:10 READY-RESTART line (notes=old.md, next=old item)
   Crius since then: nothing
7. Post START.

## Hard limits
- none`

const frBus = `2026-11-17T09:10 INFO acme:qa -> crius:lead — READY-RESTART notes=old.md next=old item
2026-11-17T09:20 DECISION crius:lead -> acme:qa — before the newest ready line: left out
2026-11-17T10:00 DONE acme:qa -> crius:lead — READY-RESTART notes=~/w/acme/NOTES.md next=item 42 re-check, then the board walk
2026-11-17T10:01 INFO acme:dev -> crius:lead — READY-RESTART notes=dev.md next=dev item
2026-11-17T10:02 DECISION crius:lead -> acme:qa — take item 43 before the walk
2026-11-17T10:03 INFO crius:lead -> acme:dev — to another role: left out
2026-11-17T10:04 ALERT crius:lead -> acme:qa — not a DECISION or INFO: left out
2026-11-17T10:05 INFO crius:lead -> acme:dev,acme:qa — box1 restarts at 11:00
2026-11-17T10:06 INFO acme:dev -> acme:qa — not from crius: left out`

// frWant is frTemplate with step 6 (both its lines) rebuilt from the 10:00 line and the two crius lines after it.
const frWant = `You are **qa** on the **acme** team.

## Start here, in order
1. The team file.
5. Run one quick smoke check.
6. Then continue with: your 10:00 READY-RESTART line (notes=~/w/acme/NOTES.md, next=item 42 re-check, then the board walk)
   Crius since then: 10:02 DECISION take item 43 before the walk; 10:05 INFO box1 restarts at 11:00
7. Post START.

## Hard limits
- none`

const frPath = "~/briefs/acme/RESTART-qa.md"

type frFake struct {
	*restartFake
	files   map[string]string // host + ":" + path -> text
	saved   []string          // host, path, prev and body of each Save
	saveErr error
}

func newFRFake(t *testing.T, bus, template string) (*frFake, string) {
	r, home := newRestartFake(t)
	r.tabs = []RemoteTab{{"box1", "acme:qa"}}
	r.screen["acme:qa"], r.procs["acme:qa"] = readyTab, paneTree()
	testutil.Write(t, RestartBriefsFile(home), "# project host path\nzeta mac ~/z/RESTART-{role}.md\nacme box1 ~/briefs/acme/RESTART-{role}.md\n")
	if bus != "" {
		testutil.Write(t, filepath.Join(home, "bus", "acme.log"), bus+"\n")
	}
	f := &frFake{restartFake: r, files: map[string]string{}}
	if template != "" {
		f.files["box1:"+frPath] = template
	}
	f.files["box1:"+frRecPath] = frRec("s-qa-1", "startup", "09:30", "09:30") // the session that posted the 10:00 line
	return f, home
}

// frRecPath is where crius-hook keeps acme:qa's current session on its box.
const frRecPath = "~/.crius/roles/acme.qa.json"

// frRec is crius-hook's record of acme:qa's session id, its last start's source and time (hh:mm on 11-17) and, unless
// "", when the id took the role (started; a record written before crius-hook kept it has none).
func frRec(id, detail, at, started string) string {
	ts := func(hm string) string {
		t, _ := time.ParseInLocation("2006-01-02 15:04", "2026-11-17 "+hm, time.Local)
		return t.Format(time.RFC3339)
	}
	s := `{"session_id":"` + id + `","project":"acme","role":"qa","event":"SessionStart","detail":"` + detail + `","time":"` + ts(at) + `"`
	if started != "" {
		s += `,"started":"` + ts(started) + `"`
	}
	return s + "}"
}

// frBound is a record crius-hook back-filled: started is only the latest the session can have taken the role by, read
// from a record written before it kept a start time.
func frBound(id, detail, at, started string) string {
	return strings.TrimSuffix(frRec(id, detail, at, started), "}") + `,"started_bound":true}`
}

// newFRMac is newFRFake with acme's templates on this machine and the role run here too (a T3 thread, no tab), so its
// record is the one under CRIUS_HOME.
func newFRMac(t *testing.T, bus, template, record string) (*frFake, string) {
	f, home := newFRFake(t, bus, "")
	testutil.Write(t, RestartBriefsFile(home), "acme mac ~/briefs/acme/RESTART-{role}.md\n")
	delete(f.files, "box1:"+frRecPath)
	f.tabs = nil
	f.files["mac:"+frPath] = template
	if record != "" {
		testutil.Write(t, filepath.Join(home, "roles", "acme.qa.json"), record)
	}
	return f, home
}

func (f *frFake) deps(home string) FromReadyDeps {
	return FromReadyDeps{RestartDeps: f.restartFake.deps(home), Now: time.Date(2026, 11, 17, 23, 5, 0, 0, time.Local),
		Read: func(host, path string) ([]byte, error) {
			s, ok := f.files[host+":"+path]
			if !ok {
				return nil, fmt.Errorf("open %s: %w", path, fs.ErrNotExist)
			}
			return []byte(s), nil
		},
		Save: func(host, path, prev string, body []byte) error {
			if f.saveErr != nil {
				return f.saveErr
			}
			f.saved = append(f.saved, host, path, prev, string(body))
			return nil
		}}
}

// The brief is the template with step 6 rebuilt from the role's newest READY-RESTART line (a DONE or INFO line) and the
// crius:lead DECISION and INFO lines to the role after it; every other line is left out and the rest of the template kept.
func TestFromReadyBuildsStep6FromTheNewestReadyLine(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	b, err := BuildFromReady(f.deps(home), "acme:qa")
	if err != nil {
		t.Fatal(err)
	}
	if b.Text != frWant {
		t.Errorf("brief:\n%s\nwant:\n%s", b.Text, frWant)
	}
	if b.Host != "box1" || b.Path != frPath || b.Prev != frPath+".prev-2305" {
		t.Errorf("place %s %s %s, want box1 %s %s.prev-2305", b.Host, b.Path, b.Prev, frPath, frPath)
	}
}

// With no crius line after the READY-RESTART line, step 6 says so instead of an empty list.
func TestFromReadySaysNothingWhenCriusSaidNothingSince(t *testing.T) {
	f, home := newFRFake(t, "2026-11-17T10:00 INFO acme:qa -> crius:lead — READY-RESTART notes=n.md next=item 1", frTemplate)
	b, err := BuildFromReady(f.deps(home), "acme:qa")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Text, "6. Then continue with: your 10:00 READY-RESTART line (notes=n.md, next=item 1)\n   Crius since then: nothing\n7. Post START.") {
		t.Errorf("brief:\n%s", b.Text)
	}
}

// Every way the brief cannot be built is a loud NO BRIEF that names the reason and the file, with nothing typed or saved.
func TestFromReadyRefusesLoudly(t *testing.T) {
	once := strings.Replace(frTemplate, "6. Then continue with:", "6. Go on with:", 1)
	twice := frTemplate + "\n6. Then continue with: again"
	used := strings.Replace(frTemplate, "your 09:10 READY-RESTART line (notes=old.md, next=old item)",
		"your 10:00 READY-RESTART line. item 42 then the walk", 1)
	for name, c := range map[string]struct {
		bus, template, role string
		has                 []string
	}{
		"no ready line": {"2026-11-17T10:00 INFO acme:dev -> crius:lead — READY-RESTART notes=d.md next=x", frTemplate, "acme:qa",
			[]string{"no READY-RESTART line from acme:qa", "acme.log"}},
		"no bus":       {"", frTemplate, "acme:qa", []string{"could not read the bus", "acme.log"}},
		"no template":  {frBus, "", "acme:qa", []string{"no brief template at " + frPath + " on box1"}},
		"anchor none":  {frBus, once, "acme:qa", []string{frPath, "has 0 lines starting \"6. Then continue with:\", want exactly 1"}},
		"anchor twice": {frBus, twice, "acme:qa", []string{frPath, "has 2 lines starting \"6. Then continue with:\", want exactly 1"}},
		"already used": {frBus, used, "acme:qa", []string{frPath, "already continues from the 10:00 READY-RESTART line",
			"no READY-RESTART line newer than the last restart"}},
		"posted since": {frBus + "\n2026-11-17T10:30 ACK acme:qa -> acme:dev — item 44 criteria read", frTemplate, "acme:qa",
			[]string{"acme:qa posted 1 line(s) after its 10:00 READY-RESTART line", "acme.log",
				"its 2026-11-17 10:30 ACK line is no answer to Crius", "it goes to acme:dev",
				"no READY-RESTART line newer than the last restart"}},
		"no next": {"2026-11-17T10:00 INFO acme:qa -> crius:lead — READY-RESTART notes=n.md, back soon", frTemplate, "acme:qa",
			[]string{"10:00", "has no notes=<path> followed by next=<text>"}},
		"no place":  {frBus, frTemplate, "other:qa", []string{"restart-briefs.txt names no template place for project other"}},
		"bad role":  {frBus, frTemplate, "acme", []string{"give the role as <project>:<role>"}},
		"bad role2": {frBus, frTemplate, "acme:q'a", []string{"give the role as <project>:<role>"}},
	} {
		f, home := newFRFake(t, c.bus, c.template)
		code := RestartFromReady(f.deps(home), c.role, false)
		if code != RestartNoBrief {
			t.Errorf("%s: exit %d, want %d; said %q", name, code, RestartNoBrief, f.out.String())
		}
		for _, h := range append([]string{"NO BRIEF", "Nothing was sent or saved."}, c.has...) {
			if !strings.Contains(f.out.String(), h) {
				t.Errorf("%s: said %q, lacks %q", name, f.out.String(), h)
			}
		}
		if len(f.typed) != 0 || len(f.saved) != 0 {
			t.Errorf("%s: typed %q, saved %q", name, f.typed, f.saved)
		}
	}
}

// A missing template table, or a bad row in it, is a refusal too, never a guessed place.
func TestFromReadyRefusesABadTemplateTable(t *testing.T) {
	for name, table := range map[string]string{
		"missing":    "",
		"no role":    "acme box1 ~/briefs/acme/RESTART.md\n",
		"shell code": "acme box1 ~/briefs/$(id)/RESTART-{role}.md\n",
		"parent":     "acme box1 ~/briefs/../RESTART-{role}.md\n",
		"relative":   "acme box1 briefs/RESTART-{role}.md\n",
		"two fields": "acme box1\n",
	} {
		f, home := newFRFake(t, frBus, frTemplate)
		if table == "" {
			if err := os.Remove(RestartBriefsFile(home)); err != nil {
				t.Fatal(err)
			}
		} else {
			testutil.Write(t, RestartBriefsFile(home), table)
		}
		code := RestartFromReady(f.deps(home), "acme:qa", false)
		if code != RestartNoBrief || !strings.Contains(f.out.String(), "could not read the template table") {
			t.Errorf("%s: exit %d; said %q", name, code, f.out.String())
		}
	}
}

// --dry-run prints the whole brief and where it would go, and types and saves nothing.
func TestFromReadyDryRunSendsAndSavesNothing(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	code := RestartFromReady(f.deps(home), "acme:qa", true)
	if code != RestartReady {
		t.Errorf("exit %d; said %q", code, f.out.String())
	}
	for _, h := range []string{"DRY RUN acme:qa", frPath + " on box1", "RESTART-qa.md.prev-2305", "Nothing was sent or saved.", frWant} {
		if !strings.Contains(f.out.String(), h) {
			t.Errorf("said %q, lacks %q", f.out.String(), h)
		}
	}
	if len(f.typed) != 0 || len(f.saved) != 0 {
		t.Errorf("typed %q, saved %q", f.typed, f.saved)
	}
}

// A real run sends the built brief the way Restart does (/clear, then the brief), then keeps the old template as
// .prev-HHMM and saves the brief over it.
func TestFromReadySendsThenSavesTheBrief(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	code := RestartFromReady(f.deps(home), "acme:qa", false)
	wantOut(t, f.restartFake, code, RestartReady, "SENT acme:qa", "SAVED acme:qa", "RESTART-qa.md.prev-2305")
	if want := []string{"acme:qa /clear", "acme:qa " + frWant}; strings.Join(f.typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", f.typed, want)
	}
	if want := []string{"box1", frPath, frPath + ".prev-2305", frWant}; strings.Join(f.saved, "|") != strings.Join(want, "|") {
		t.Errorf("saved %q, want %q", f.saved, want)
	}
}

// When Restart does not send (a busy tab), the template is not touched, so the next run can use the same line.
func TestFromReadySavesNothingWhenTheRestartDidNotGo(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	f.screen["acme:qa"] = busyTab
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", false), RestartBusy, "BUSY acme:qa")
	if len(f.typed) != 0 || len(f.saved) != 0 {
		t.Errorf("typed %q, saved %q", f.typed, f.saved)
	}
}

// A brief that went but could not be saved is its own exit, never a quiet success.
func TestFromReadySaysUnsavedWhenTheSaveFails(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	f.saveErr = errors.New("RESTART-qa.md.prev-2305 exists")
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", false), RestartUnsaved,
		"SENT acme:qa", "UNSAVED acme:qa", "prev-2305 exists", "still holds the old step 6")
}

// A brief that may have gone (crius-tsend MAYBE SENT) is saved too, so a later run refuses the same line instead of
// sending it twice; the run says how to undo that if it did not go.
func TestFromReadySavesAMaybeSentBrief(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	f.sendErr[frWant] = errors.New("crius-tsend: exit status 3: MAYBE SENT box1 acme:qa")
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", false), RestartMaybe,
		"MAYBE SENT acme:qa", "SAVED acme:qa", "may not have gone", "put the old copy back")
	if len(f.saved) != 4 {
		t.Errorf("saved %q, want one save", f.saved)
	}
}

// A T3 role: Restart only prints the send, which is Crius's; the saved brief says it is not sent yet.
func TestFromReadyOnAT3ThreadSaysTheSendIsPending(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	f.tabs = nil
	testutil.Write(t, filepath.Join(home, "roles", "acme.qa.json"),
		`{"session_id":"s-acme-qa","project":"acme","role":"qa","t3_thread":"thr-acme-qa","time":"2026-11-17T13:48:00Z",`+
			`"started":"`+time.Date(2026, 11, 17, 9, 30, 0, 0, time.Local).Format(time.RFC3339)+`"}`) // took the role before its 10:00 line
	f.f.threads["thr-acme-qa"] = T3Thread{Status: "completed", Updated: t0}
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", false), RestartReady,
		"READY acme:qa", "t3_thread_send", "SAVED acme:qa", "It is not sent yet")
	if len(f.typed) != 0 || len(f.saved) != 4 {
		t.Errorf("typed %q, saved %q", f.typed, f.saved)
	}
}

// The old-copy name is checked before anything is sent: a taken one refuses, never a send that cannot be saved.
func TestFromReadyRefusesATakenOldCopyName(t *testing.T) {
	f, home := newFRFake(t, frBus, frTemplate)
	f.files["box1:"+frPath+".prev-2305"] = "an older copy"
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", false), RestartNoBrief,
		"NO BRIEF acme:qa", "the old-copy name "+frPath+".prev-2305 on box1 is taken")
	if len(f.typed) != 0 || len(f.saved) != 0 {
		t.Errorf("typed %q, saved %q", f.typed, f.saved)
	}
}

// A bare sender on the project's own bus is that project's role; a role line that only mentions READY-RESTART is work
// after the request, never a new request.
func TestFromReadyReadsBareSendersAndOnlyRealRequests(t *testing.T) {
	f, home := newFRFake(t, frBus+"\n2026-11-17T11:00 INFO qa -> crius:lead — READY-RESTART notes=b.md next=bare item", frTemplate)
	b, err := BuildFromReady(f.deps(home), "acme:qa")
	if err != nil || !strings.Contains(b.Text, "your 11:00 READY-RESTART line (notes=b.md, next=bare item)\n   Crius since then: nothing") {
		t.Errorf("bare sender: %v\n%s", err, b.Text)
	}
	f, home = newFRFake(t, frBus+"\n2026-11-17T11:00 INFO acme:qa -> crius:lead — resumed per READY-RESTART notes=x next=y", frTemplate)
	_, err = BuildFromReady(f.deps(home), "acme:qa")
	if err == nil || !strings.Contains(err.Error(), "posted 1 line(s) after its 10:00 READY-RESTART line") ||
		!strings.Contains(err.Error(), `its 2026-11-17 11:00 INFO line is no answer to Crius (it does not open with "Re HH:MM"`) {
		t.Errorf("a mention: %v", err)
	}
}

// frAsked is frBus with a crius:lead ASK to acme:qa after its 10:00 READY-RESTART line.
const frAsked = frBus + "\n2026-11-17T10:20 ASK crius:lead -> acme:qa — is the item 42 gate green?"

// An answer to Crius after the READY-RESTART line does not block it (11-18: refused twice for a role's answer to a
// question Crius asked after the line). The answer goes into step 6 after the line it answers, so the fresh session sees
// both: here an ANSWER to the ASK, and a reply-all INFO to a crius line that also went to acme:dev.
func TestFromReadyLetsAnAnswerToCriusThrough(t *testing.T) {
	bus := frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — Re 10:20: green, run 12 (40/40)" +
		"\n2026-11-17T10:24 INFO acme:qa -> crius:lead,acme:dev — re 10:05: noted, nothing of mine runs on box1"
	f, home := newFRFake(t, bus, frTemplate)
	b, err := BuildFromReady(f.deps(home), "acme:qa")
	if err != nil {
		t.Fatal(err)
	}
	want := "6. Then continue with: your 10:00 READY-RESTART line (notes=~/w/acme/NOTES.md, next=item 42 re-check, then the board walk)\n" +
		"   Crius since then: 10:02 DECISION take item 43 before the walk; 10:05 INFO box1 restarts at 11:00; " +
		"10:20 ASK is the item 42 gate green?; 10:22 your ANSWER Re 10:20: green, run 12 (40/40); " +
		"10:24 your INFO re 10:05: noted, nothing of mine runs on box1\n7. Post START."
	if !strings.Contains(b.Text, want) {
		t.Errorf("brief:\n%s\nwant step 6:\n%s", b.Text, want)
	}
}

// A role line after the READY-RESTART line that reports work, changes state, talks to anyone but Crius, or answers no
// crius line since still refuses, naming that line; so does a restart since the line, even with only answers after it.
func TestFromReadyRefusesWorkAndRestartsAfterTheLine(t *testing.T) {
	answer := "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — Re 10:20: green, run 12 (40/40)"
	used := strings.Replace(frTemplate, "your 09:10 READY-RESTART line (notes=old.md, next=old item)",
		"your 10:00 READY-RESTART line (notes=~/w/acme/NOTES.md, next=item 42)", 1)
	none := "no READY-RESTART line newer than the last restart"
	for name, c := range map[string]struct {
		bus, template string
		record        string // crius-hook's record of the role on box1, "" for the one of the session that posted the line
		has           []string
	}{
		"done after an ask": {frAsked + "\n2026-11-17T10:22 DONE acme:qa -> crius:lead — Re 10:20: item 42 done, gate green",
			frTemplate, "", []string{"acme:qa posted 1 line(s) after its 10:00 READY-RESTART line",
				"its 2026-11-17 10:22 DONE line is no answer to Crius", "a DONE line reports work or a state", none}},
		"work after an answer": {frAsked + answer + "\n2026-11-17T10:30 READY acme:qa -> crius:lead — item 43 ready",
			frTemplate, "", []string{"acme:qa posted 2 line(s) after its 10:00 READY-RESTART line",
				"its 2026-11-17 10:30 READY line is no answer to Crius", none}},
		"to another role": {frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead,acme:ops — Re 10:20: green",
			frTemplate, "", []string{"its 2026-11-17 10:22 ANSWER line is no answer to Crius", "it goes to acme:ops", none}},
		"to the whole bus": {frAsked + "\n2026-11-17T10:22 INFO acme:qa — Re 10:20: green", frTemplate, "",
			[]string{"its 2026-11-17 10:22 INFO line is no answer to Crius", "it goes to the whole bus", none}},
		"names no crius line": {frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — green, run 12", frTemplate, "",
			[]string{"its 2026-11-17 10:22 ANSWER line is no answer to Crius", `it does not open with "Re HH:MM"`, none}},
		"answers a line before it": {frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — Re 09:20: done", frTemplate, "",
			[]string{"its 2026-11-17 10:22 ANSWER line is no answer to Crius", `it does not open with "Re HH:MM"`, none}},
		"answers a line not in step 6": {frAsked + "\n2026-11-17T10:22 ACK acme:qa -> crius:lead — Re 10:04: seen", frTemplate, "",
			[]string{"its 2026-11-17 10:22 ACK line is no answer to Crius", `it does not open with "Re HH:MM"`, none}},
		"restarted: a START line": {frAsked + "\n2026-11-17T10:12 START acme:qa -> crius:lead — fresh session, brief read" + answer,
			frTemplate, "", []string{"acme:qa posted 2 line(s)", "its 2026-11-17 10:12 START line is no answer to Crius", none}},
		"restarted: a new session on the box": {frAsked + answer, frTemplate, frRec("s-qa-2", "startup", "10:15", "10:15"),
			[]string{"a new session of acme:qa (s-qa-2) took the role at 2026-11-17 10:15", frRecPath + " on box1",
				"after its 10:00 READY-RESTART line", none}},
		"restarted by hand, nothing posted since": {frBus, frTemplate, frRec("s-qa-2", "clear", "10:15", "10:15"),
			[]string{"a new session of acme:qa (s-qa-2) took the role at 2026-11-17 10:15", none}},
		"a new session in the line's minute": {frAsked + answer, frTemplate, frRec("s-qa-2", "startup", "10:00", "10:00"),
			[]string{"a new session of acme:qa (s-qa-2) took the role at 2026-11-17 10:00", none}},
		"an old record rewritten after the line": {frAsked + answer, frTemplate, frRec("s-qa-1", "resume", "10:21", ""),
			[]string{"cannot tell when the session that holds the role (s-qa-1) took it", "last written at 2026-11-17 10:21",
				"no start time", none}},
		"a back-filled start after the line": {frAsked + answer, frTemplate, frBound("s-qa-1", "resume", "10:30", "10:21"),
			[]string{"cannot tell when the session that holds the role (s-qa-1) took it", "at 2026-11-17 10:21 or before", none}},
		"a damaged record": {frAsked + answer, frTemplate, "{not json",
			[]string{"could not read crius-hook's record " + frRecPath + " on box1"}},
		"restarted: the template": {frAsked + answer, used, "",
			[]string{"already continues from the 10:00 READY-RESTART line", none}},
	} {
		f, home := newFRFake(t, c.bus, c.template)
		if c.record != "" {
			f.files["box1:"+frRecPath] = c.record
		}
		code := RestartFromReady(f.deps(home), "acme:qa", false)
		if code != RestartNoBrief {
			t.Errorf("%s: exit %d, want %d; said %q", name, code, RestartNoBrief, f.out.String())
		}
		has := append([]string{"NO BRIEF", "Nothing was sent or saved."}, c.has...)
		if slices.Contains(c.has, none) { // each refusal of a used line says what to do
			has = append(has, ": ask acme:qa to post a new READY-RESTART line.")
		}
		for _, h := range has {
			if !strings.Contains(f.out.String(), h) {
				t.Errorf("%s: said %q, lacks %q", name, f.out.String(), h)
			}
		}
		if len(f.typed) != 0 || len(f.saved) != 0 {
			t.Errorf("%s: typed %q, saved %q", name, f.typed, f.saved)
		}
	}
}

// The record is read where the role runs, not where its template lives: a role whose template is on this machine but
// which runs as a tmux tab on box1 is checked against box1's record, never a stale one here.
func TestFromReadyReadsTheRecordWhereTheRoleRuns(t *testing.T) {
	bus := frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — Re 10:20: green, run 12 (40/40)"
	f, home := newFRMac(t, bus, frTemplate, frRec("s-old", "startup", "08:00", "08:00")) // weeks-old style record here
	f.tabs = []RemoteTab{{"box1", "acme:qa"}}
	f.files["box1:"+frRecPath] = frRec("s-qa-2", "clear", "10:15", "10:15")
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", true), RestartNoBrief,
		"a new session of acme:qa (s-qa-2) took the role at 2026-11-17 10:15 (crius-hook's record "+frRecPath+" on box1)")
	f, home = newFRMac(t, bus, frTemplate, "")
	f.tabs = []RemoteTab{{"box1", "acme:qa"}}
	f.files["box1:"+frRecPath] = frRec("s-qa-1", "resume", "10:21", "09:30")
	code := RestartFromReady(f.deps(home), "acme:qa", true)
	if out := f.out.String(); code != RestartReady || strings.Contains(out, "NOTE acme:qa") || !strings.Contains(out, "your ANSWER Re 10:20") {
		t.Errorf("box record of the same session: exit %d, said %q", code, out)
	}
}

// The record check asks whether a different session holds the role than the one that posted the line, not whether the
// record was written since: a T3 thread's session is resumed by each line to it, and a long turn compacts, and both
// rewrite the record's time while keeping the session id and its start. Here on this machine, where the record is the
// one under CRIUS_HOME.
func TestFromReadyLetsTheSameSessionResumeOrCompact(t *testing.T) {
	bus := frAsked + "\n2026-11-17T10:22 ANSWER acme:qa -> crius:lead — Re 10:20: green, run 12 (40/40)"
	for name, record := range map[string]string{
		"resumed after the line":          frRec("s-qa-1", "resume", "10:21", "09:30"),
		"compacted after the line":        frRec("s-qa-1", "compact", "10:21", "09:30"),
		"took the role a minute before":   frRec("s-qa-1", "startup", "09:59", "09:59"),
		"old record, last written before": frRec("s-qa-1", "resume", "09:50", ""),
	} {
		f, home := newFRMac(t, bus, frTemplate, record)
		code, out := RestartFromReady(f.deps(home), "acme:qa", true), f.out.String()
		if code != RestartReady || !strings.Contains(out, "10:20 ASK is the item 42 gate green?; 10:22 your ANSWER Re 10:20: green") ||
			strings.Contains(out, "NOTE acme:qa") {
			t.Errorf("%s: exit %d, said %q", name, code, out)
		}
	}
	f, home := newFRMac(t, bus, frTemplate, frRec("s-qa-2", "clear", "10:15", "10:15"))
	if _, err := BuildFromReady(f.deps(home), "acme:qa"); err == nil ||
		!strings.Contains(err.Error(), "a new session of acme:qa (s-qa-2) took the role at 2026-11-17 10:15") {
		t.Errorf("a new session on this machine: %v", err)
	}
}

// With no record of the role's session, the brief is built but the run says it could not check for a newer session, in
// the dry run and in a real run alike.
func TestFromReadySaysWhenNoRecordCouldBeChecked(t *testing.T) {
	note := "NOTE acme:qa: no record of the role's session at " + frRecPath + " on box1: could not check that no newer " +
		"session took the role after its 10:00 READY-RESTART line"
	for _, dry := range []bool{true, false} {
		f, home := newFRFake(t, frBus, frTemplate)
		delete(f.files, "box1:"+frRecPath)
		code := RestartFromReady(f.deps(home), "acme:qa", dry)
		wantOut(t, f.restartFake, code, RestartReady, note)
	}
	f, home := newFRMac(t, frBus, frTemplate, "")
	wantOut(t, f.restartFake, RestartFromReady(f.deps(home), "acme:qa", true), RestartReady,
		"NOTE acme:qa: no record of the role's session at "+filepath.Join(home, "roles", "acme.qa.json")+" on mac")
}
