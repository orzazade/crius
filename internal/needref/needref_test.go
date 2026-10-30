package needref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// The 10-30 GOAL asks that pointed elsewhere (Crius 16:45: q8 showed only "the 3 questions in STATUS-2026-10-28.md
// section 6"): each reads in the questions themselves and says where they came from.
func TestReadInTheQuestionsAnAskPointsTo(t *testing.T) {
	h := t.TempDir()
	repo := filepath.Join(h, "svc_sku_builder-91260")
	write(t, filepath.Join(repo, "docs/specs/91260-angular-22-upgrade/STATUS-2026-10-28.md"), `# Angular 22 plan
## 5. Steps
Step 0 first.
## 6. Questions for the owner

1. **94489 has not started. Should the upgrade keep waiting for it?**
   - Keep waiting (current decision).
2. **Should Step 0 fix the failing tests, or only record them?**
3. Close the spike and open the upgrade ticket, or keep it on hold?

## 7. Next
none
`)
	write(t, filepath.Join(repo, "node_modules/x/STATUS-2026-10-28.md"), "## 6. not this one\n")
	angular := write(t, filepath.Join(h, "state/angular-91260/STATUS.md"), "## GOAL\n")
	got := Read("the 3 questions in STATUS-2026-10-28.md section 6", angular, []string{repo})
	for _, want := range []string{"## 6. Questions for the owner", "1. 94489 has not started. Should the upgrade keep waiting for it?",
		"2. Should Step 0 fix the failing tests", "3. Close the spike", "(from " + filepath.Join(repo, "docs/specs/91260-angular-22-upgrade/STATUS-2026-10-28.md") + " section 6)"} {
		if !strings.Contains(got, want) {
			t.Errorf("q8: want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "## 7.") || strings.Contains(got, "Step 0 first") || strings.Contains(got, "not this one") {
		t.Errorf("q8: only section 6 of the file outside node_modules:\n%s", got)
	}

	axi := filepath.Join(h, "AXI/first-order")
	write(t, filepath.Join(axi, "docs/build/PROD-HANDOVER.md"), `## 11. Runbook
x
## 12. Decisions for the owner (each stands alone)

1. ~~Who owns production~~ — decided.
2. **Reseller gate in production.** Options: (a) list every reseller ID;
   (b) build a production mode.
3. **Alert receiver.** Which person or channel gets the alerts.
4. ~~"Shadow first" for orders~~ — decided (B98): no order shadow.
   Each order type switches once proven.
8. **Data loss and restore.** RPO and RTO?
9. Not asked.
Closing note at the margin.
`)
	axiStatus := write(t, filepath.Join(axi, "docs/build/STATUS.md"), "## GOAL\n")
	got = Read("PROD-HANDOVER.md 12.2-12.8; phase-0: target date", axiStatus, nil)
	for _, want := range []string{"2. Reseller gate in production. Options: (a) list every reseller ID;\n   (b) build a production mode.",
		"3. Alert receiver.", "8. Data loss and restore.", "section 12.2-12.8)"} {
		if !strings.Contains(got, want) {
			t.Errorf("axi: want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "1. ~~Who owns") || strings.Contains(got, "9. Not asked") || strings.Contains(got, "Closing note") ||
		strings.Contains(got, "Shadow first") || strings.Contains(got, "switches once proven") {
		t.Errorf("axi: only the open items 2 to 8 (a struck one is decided):\n%s", got)
	}
	if got := Read("Alert receiver (PROD-HANDOVER 12.3)", axiStatus, nil); !strings.HasPrefix(got, "3. Alert receiver. Which person") {
		t.Errorf("axi 12.3, named without .md: %q", got)
	}

	upload := filepath.Join(h, "upload-central")
	write(t, filepath.Join(upload, "docs/build/BUILD-DECISIONS.md"), `## B3 — older
x
## B4 — Owner: File Service does upload + review only (2026-10-30)
- Settled: not option A.
### B4 amendment
- Open: who serves clean files: svc_file or each service?
## B40 — later
y
`)
	upStatus := write(t, filepath.Join(h, "state/upload-central/STATUS.md"), "## GOAL\n")
	got = Read("B4 — after a clean scan, who stores and serves the file (BUILD-DECISIONS B4, sha=8b5281b)", upStatus, []string{upload})
	if !strings.Contains(got, "## B4 — Owner") || !strings.Contains(got, "who serves clean files") || strings.Contains(got, "B40") || strings.Contains(got, "B3") {
		t.Errorf("upload-central B4 with its amendment, not B40:\n%s", got)
	}

	mapStatus := write(t, filepath.Join(h, "state/appxite-map/STATUS.md"), `## GOAL (2026-10-30 11:10)
Needs owner: yes or no on the START ask 3 split (tenant rules, see "Open with owner")

Open with owner (2026-10-30 ~11:10): START ask 3 split: should tenant rules be a separate pilot? Yes keeps
phase 1 small; no folds them in.
Quota: max3 at 97% (crius 13:56), resets Sat 12:00; no new helpers.
Mirror re-fetch (if a later phase needs the code again): in appxite-map at 9b039d1+, set base=

- Next line
`)
	got = Read(`yes or no on the START ask 3 split (tenant rules, see "Open with owner"), then the walk-through`, mapStatus, nil)
	if !strings.HasPrefix(got, "Open with owner (2026-10-30 ~11:10): START ask 3 split") || !strings.Contains(got, "no folds them in.\n(from "+mapStatus+")") {
		t.Errorf("appxite-map label:\n%s", got)
	}

	gains := filepath.Join(h, "appxite-gains")
	write(t, filepath.Join(gains, "GAINS.md"), "# Gains\n1. Cache the price list.\n")
	if got := Read("a walk-through of GAINS.md to pick which moves to build", write(t, filepath.Join(h, "state/gains/STATUS.md"), ""), []string{gains}); got != "# Gains\n1. Cache the price list.\n(from "+filepath.Join(gains, "GAINS.md")+")" {
		t.Errorf("GAINS.md, the whole file: %q", got)
	}
}

// "Found nothing" and "failed to look" differ: a named .md that is not there, or a section it does not have, is said;
// a doc name with no file of that name is just words; an ask with no file reads in nothing.
func TestAFileThatCannotBeReadIsSaid(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "STATUS.md"), "## GOAL\n")
	write(t, filepath.Join(h, "PLAN.md"), "## 1. One\nx\n")
	if got := Read("OK on GONE.md section 2", status, nil); got != "Could not read GONE.md: no such file under "+h+"." {
		t.Errorf("missing file: %q", got)
	}
	if got := Read("PLAN.md section 4", status, nil); got != "Could not read section 4 in "+filepath.Join(h, "PLAN.md")+": no heading starts with 4." {
		t.Errorf("missing section: %q", got)
	}
	if got := Read(`see "Nowhere label"`, status, nil); got != `Could not read "Nowhere label" in `+status+": no heading or line opens with it." {
		t.Errorf("missing label: %q", got)
	}
	for _, ask := range []string{"OK on the AXI-V2 rollout", "approve the k3s deploy", "Q1 CI as a source · NEED 10-29"} {
		if got := Read(ask, status, nil); got != "" {
			t.Errorf("%q names no file: %q", ask, got)
		}
	}
}

// A long section is cut, and the card still says which file holds the rest.
func TestALongSectionIsCutAndKeepsItsPath(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "STATUS.md"), "")
	path := write(t, filepath.Join(h, "LONG.md"), "## 1. Long\n"+strings.Repeat("words ", 1000))
	got := Read("LONG.md section 1", status, nil)
	if !strings.HasSuffix(got, " words … (cut: the rest is in the file)\n(from "+path+" section 1)") || len([]rune(got)) > MaxRunes+200 {
		t.Errorf("got %d runes, ending %q", len([]rune(got)), got[max(0, len(got)-120):])
	}
}
