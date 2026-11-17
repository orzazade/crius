package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
			[]string{"acme:qa posted 1 line(s) after its 10:00 READY-RESTART line (the newest 10:30 ACK)", "acme.log",
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
