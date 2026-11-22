package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

const (
	docURL  = "https://learn.microsoft.com/en-us/partner-center/developer/create-an-order"
	docDate = "last updated 2026-11-03"
)

func axiLog(t *testing.T) (home, log string) {
	home, _ = setup(t)
	log = filepath.Join(home, "bus", "axi.log")
	testutil.Write(t, log, "")
	return home, log
}

// crius:lead decided from old Microsoft pages: a DECISION that leans on them names the page and its date.
func TestADecisionOnMicrosoftDocsWithNoPageAndDateIsDenied(t *testing.T) {
	for _, text := range []string{
		"J6: the Microsoft docs leave userId out, so leave it out",
		"J6: old MS page omits userId, so leave it out",
		"per learn.microsoft.com userId is optional",
		"per docs.microsoft.com userId is optional",
		"Partner Center docs say GET works",
		"the Partner Center API documentation says GET works",
		"Microsoft's documentation says GET works",
		"the documentation from Microsoft says GET works",
		"Microsoft documents userId as optional",
		"Microsoft Learn says GET works",
		"documentation for Partner Center says GET works",
		"docs for the Microsoft API say GET works",
		"docs: Microsoft says GET works",
		"the docs (Microsoft) say GET works",
		"Microsoft **docs** say GET works",
		"Microsoft `docs` say GET works",
		"Partner-Center docs say GET works",
		"Partner Center REST API reference documentation says GET works",
		"Microsoft API reference page says GET works",
		"MSFT docs say GET works",
		"the ms docs say GET works",
		"see https://aka.ms/pc-orders",
		"Microsoft docs say so, https://learn.microsoft.com/) " + docDate,
		"Microsoft docs say so: " + docURL,                              // no date
		"Microsoft docs say so, " + docDate,                             // no URL
		"Microsoft docs say so, https://learn.microsoft.com " + docDate, // a site, no page
		"Microsoft docs say so, https://example.com/x/y " + docDate,     // another site's page
		"Microsoft docs say so: " + docURL + " last updated recently",
	} {
		home, log := axiLog(t)
		rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", text)
		if rc != 2 || !strings.Contains(errs, "Microsoft documentation") || !strings.Contains(errs, "last updated <date>") ||
			!strings.Contains(errs, "page URL") || busText(t, log) != "" {
			t.Errorf("%q: rc %d, %q, bus %q", text, rc, errs, busText(t, log))
		}
		if b, _ := os.ReadFile(filepath.Join(home, "logs", "bus-post-refused.log")); !strings.Contains(string(b), "Microsoft documentation") {
			t.Errorf("%q: refusal not logged: %q", text, b)
		}
	}
}

func TestADecisionOnMicrosoftDocsWithPageAndDateIsPosted(t *testing.T) {
	for _, text := range []string{
		"Microsoft docs leave userId out (" + docURL + ", " + docDate + ")",
		"Partner Center docs " + docURL + " Last updated: 11/03/2026",
		"Partner Center docs " + docURL + " last updated on Nov 3, 2026",
		"Partner Center docs " + docURL + " last-updated 3 November 2026",
		"Partner Center docs " + docURL + " (last updated: **2026-11-03**)",
	} {
		_, log := axiLog(t)
		if rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", text); rc != 0 || busText(t, log) == "" {
			t.Errorf("%q: rc %d, %q", text, rc, errs)
		}
	}
}

// The whole text counts, the part a long line keeps only in its note too; and the note file the text names.
func TestANoteOfTheDecisionCounts(t *testing.T) {
	home, log := axiLog(t)
	notes := filepath.Join(home, "bus", "notes")
	long := strings.Repeat("word ", 80)
	if rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", long+"as Microsoft docs say"); rc != 2 ||
		!strings.Contains(errs, "Microsoft documentation") || busText(t, log) != "" {
		t.Errorf("long text: rc %d, %q", rc, errs)
	}
	if m, _ := filepath.Glob(filepath.Join(notes, "*")); len(m) != 0 {
		t.Errorf("a denied line left its note %v", m)
	}
	bad, good := filepath.Join(notes, "axi-j6-aaa.txt"), filepath.Join(notes, "axi-j6-bbb.txt")
	testutil.Write(t, bad, "J6.\nMicrosoft docs say userId is optional.\n")
	testutil.Write(t, good, "J6.\nMicrosoft docs say userId is optional:\n"+docURL+"\n"+docDate+"\n")
	for _, form := range []string{"full: %s.", "(%s)", `"%s"`, "`%s`", "full:%s", "[%s]"} {
		text := "J6 go, " + strings.Replace(form, "%s", bad, 1)
		if rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", text); rc != 2 ||
			!strings.Contains(errs, "Microsoft documentation") || busText(t, log) != "" {
			t.Errorf("bad note %s: rc %d, %q", form, rc, errs)
		}
	}
	if rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", "J6 go, full: "+filepath.Join(notes, "gone.txt")); rc != 2 ||
		!strings.Contains(errs, "gone.txt") || busText(t, log) != "" {
		t.Errorf("a note that cannot be read: rc %d, %q", rc, errs)
	}
	if rc, errs := post(t, at("2026-11-23T01:00"), log, "DECISION", "crius:lead", "axi:api-split", "J6 go, Microsoft docs, full: "+good); rc != 0 {
		t.Errorf("good note: rc %d, %q", rc, errs)
	}
}

func TestOtherLinesOnMicrosoftAreUntouched(t *testing.T) {
	const says = "Microsoft docs leave userId out"
	for _, c := range []struct{ bus, typ, from, text string }{
		{"quotes.log", "DECISION", "crius:lead", says},
		{"axi.log", "INFO", "crius:lead", says},
		{"axi.log", "DECISION", "axi:lead", says},
		{"axi.log", "DECISION", "crius:lead", "Microsoft fills userId; build A"},
		{"axi.log", "DECISION", "crius:lead", "a real Partner Center sandbox reply is the proof"},
		{"axi.log", "DECISION", "crius:lead", "Microsoft rows: record them in docs/build/BUILD-DECISIONS.md"},
		{"axi.log", "DECISION", "crius:lead", "update our docs, then the Microsoft row"},
		{"axi.log", "DECISION", "crius:lead", "ms-flow update docs, then GET /v1/notes/5 within 250 ms per doc"},
		{"axi.log", "DECISION", "crius:lead", "Microsoft page size 500, Microsoft pages results by 100"},
		{"axi.log", "DECISION", "crius:lead", "MS pages the list; Microsoft billing is documented in B105"},
		{"axi.log", "DECISION", "crius:lead", "Microsoft adapter docs: update; documented in the Microsoft adapter"},
		{"axi.log", "DECISION", "crius:lead", "see docs on Microsoft rows; Partner Center sandbox docs task J7"},
		{"axi.log", "DECISION", "crius:lead", "see https://x.example/notes/a.txt"},
		{"axi.log", "DECISION", "crius:lead", "rules: ~/.crius/state/axi/build/TEAM.md"},
	} {
		home, _ := axiLog(t)
		log := filepath.Join(home, "bus", c.bus)
		testutil.Write(t, log, "")
		if rc, errs := post(t, at("2026-11-23T01:00"), log, c.typ, c.from, "axi:api-split", c.text); rc == 2 || !strings.Contains(busText(t, log), c.text) {
			t.Errorf("%+v: rc %d, %q", c, rc, errs)
		}
	}
}
