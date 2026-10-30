package needref

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// at is a status file on disk, read as Read reads it.
func at(path string) Status { return Status{Path: path, Dir: filepath.Dir(path)} }

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
	got := Read("the 3 questions in STATUS-2026-10-28.md section 6", at(angular), []string{repo})
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
	got = Read("PROD-HANDOVER.md 12.2-12.8; phase-0: target date", at(axiStatus), nil)
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
	if got := Read("Alert receiver (PROD-HANDOVER 12.3)", at(axiStatus), nil); !strings.HasPrefix(got, "3. Alert receiver. Which person") {
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
	got = Read("B4 — after a clean scan, who stores and serves the file (BUILD-DECISIONS B4, sha=8b5281b)", at(upStatus), []string{upload})
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
	got = Read(`yes or no on the START ask 3 split (tenant rules, see "Open with owner"), then the walk-through`, at(mapStatus), nil)
	if !strings.HasPrefix(got, "Open with owner (2026-10-30 ~11:10): START ask 3 split") || !strings.Contains(got, "no folds them in.\nQuota: max3 at 97%") ||
		!strings.HasSuffix(got, "set base=\n(from "+mapStatus+")") || strings.Contains(got, "Next line") {
		t.Errorf("appxite-map label, up to the blank line (a line that looks like a label may go on the paragraph):\n%s", got)
	}

	gains := filepath.Join(h, "appxite-gains")
	write(t, filepath.Join(gains, "GAINS.md"), "# Gains\n1. Cache the price list.\n")
	if got := Read("a walk-through of GAINS.md to pick which moves to build", at(write(t, filepath.Join(h, "state/gains/STATUS.md"), "")), []string{gains}); got != "# Gains\n1. Cache the price list.\n(from "+filepath.Join(gains, "GAINS.md")+")" {
		t.Errorf("GAINS.md, the whole file: %q", got)
	}
}

// "Found nothing" and "failed to look" differ: a named .md that is not there, or a section it does not have, is said;
// a doc name with no file of that name is just words; an ask with no file reads in nothing.
func TestAFileThatCannotBeReadIsSaid(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "STATUS.md"), "## GOAL\n")
	write(t, filepath.Join(h, "PLAN.md"), "## 1. One\nx\n")
	if got := Read("OK on GONE.md section 2", at(status), nil); got != "Could not read GONE.md: no such file under "+h+"." {
		t.Errorf("missing file: %q", got)
	}
	if got := Read("PLAN.md section 4", at(status), nil); got != "Could not read section 4 in "+filepath.Join(h, "PLAN.md")+": no heading starts with 4." {
		t.Errorf("missing section: %q", got)
	}
	if got := Read(`see "Nowhere label"`, at(status), nil); got != `Could not read "Nowhere label" in `+status+": no heading or line opens with it." {
		t.Errorf("missing label: %q", got)
	}
	for _, ask := range []string{"OK on the AXI-V2 rollout", "approve the k3s deploy", "Q1 CI as a source · NEED 10-29"} {
		if got := Read(ask, at(status), nil); got != "" {
			t.Errorf("%q names no file: %q", ask, got)
		}
	}
}

// A long section is cut, and the card still says which file holds the rest.
func TestALongSectionIsCutAndKeepsItsPath(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "STATUS.md"), "")
	path := write(t, filepath.Join(h, "LONG.md"), "## 1. Long\n"+strings.Repeat("words ", 1000))
	got := Read("LONG.md section 1", at(status), nil)
	if !strings.HasSuffix(got, " words … (cut: the rest is in the file)\n(from "+path+" section 1)") || len([]rune(got)) > MaxRunes+200 {
		t.Errorf("got %d runes, ending %q", len([]rune(got)), got[max(0, len(got)-120):])
	}
}

// Review round 1 of f1c57f3: each shape that read wrong or partial text with a confident "(from …)" line.
func TestNearbyShapesReadTheRightTextOrSaySo(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "state/x/STATUS.md"), "## GOAL\n")
	st := Status{Path: status, Dir: filepath.Dir(status)}

	// P2-2: a folder holding several repos has a file of that name in two of them: say so, never pick one.
	apps := filepath.Join(h, "Apps")
	write(t, filepath.Join(apps, "svc_a/docs/BUILD-DECISIONS.md"), "## B4 — a's B4\n")
	write(t, filepath.Join(apps, "svc_b/docs/BUILD-DECISIONS.md"), "## B4 — b's B4\n")
	if got := Read("BUILD-DECISIONS B4", st, []string{apps}); !strings.HasPrefix(got, "Could not read BUILD-DECISIONS.md: 2 files of that name under "+apps) ||
		strings.Contains(got, "'s B4") {
		t.Errorf("two repos: %q", got)
	}

	// P2-3: a label followed by a list keeps the list; a continuation line that looks like a label stays in.
	st.Text = []byte("## GOAL\nNeeds owner: see \"Open with owner\"\n\nOpen with owner:\n- Should A ship?\n- Should B wait?\nYes: keep it small; No: fold in.\n\nLater text\n")
	if got := Read(`see "Open with owner"`, st, nil); got != "Open with owner:\n- Should A ship?\n- Should B wait?\nYes: keep it small; No: fold in.\n(from "+status+")" {
		t.Errorf("label and list: %q", got)
	}

	// P2-1: labels are read from the text the GOAL block came from (a git object), not the file on disk.
	st.Text = []byte("Open with owner: the committed question?\n")
	if got := Read(`see "Open with owner"`, st, nil); got != "Open with owner: the committed question?\n(from "+status+")" {
		t.Errorf("git-object text: %q", got)
	}
	st.Text = nil

	// P2-4: a range reads the sub-sections it names, never list items of another one; P3-11: a range of sections.
	write(t, filepath.Join(h, "state/x/HAND.md"), "## 12. Decisions\n### 12.1 Context\n1. ctx one\n2. ctx two\n### 12.2 Gate\ngate text\n"+
		"### 12.3 Alerts\nalert text\n### 12.4 Other\nother\n## 13. Next\n")
	if got := Read("HAND.md 12.2-12.3", st, nil); !strings.Contains(got, "### 12.2 Gate\ngate text\n### 12.3 Alerts\nalert text") ||
		strings.Contains(got, "ctx") || strings.Contains(got, "12.4") {
		t.Errorf("sub-section range: %q", got)
	}
	write(t, filepath.Join(h, "state/x/PLAN.md"), "## 1. One\na\n## 2. Two\nb\n### 2.1 Sub\nc\n## 3. Three\nd\n## 4. Four\ne\n")
	if got := Read("PLAN.md 2-3", st, nil); !strings.Contains(got, "## 2. Two\nb\n### 2.1 Sub\nc\n## 3. Three\nd") || strings.Contains(got, "## 4.") || strings.Contains(got, "## 1.") {
		t.Errorf("section range: %q", got)
	}
	write(t, filepath.Join(h, "state/x/LIST.md"), "## 12. D\n1. a\n2. b\n### 12.9 Notes\n3. note item\n")
	if got := Read("LIST.md 12.2-12.3", st, nil); !strings.HasPrefix(got, "2. b\n(from ") {
		t.Errorf("items of a section, not of its sub-section: %q", got)
	}

	// P3-8: the shallowest heading that starts with the key wins.
	write(t, filepath.Join(h, "state/x/DEEP.md"), "## Intro\n### 1. sub step\n## 1. Real\nreal text\n")
	if got := Read("DEEP.md section 1", st, nil); !strings.HasPrefix(got, "## 1. Real\nreal text") {
		t.Errorf("deeper heading won: %q", got)
	}

	// P3-7: "Section 4" and "sections 2-3" name sections; a count after a comma or a date names none; a URL is no file.
	if got := Read("PLAN.md Section 4", st, nil); !strings.HasPrefix(got, "## 4. Four") {
		t.Errorf("Section 4: %q", got)
	}
	if got := Read("PLAN.md sections 2-3", st, nil); !strings.HasPrefix(got, "## 2. Two") {
		t.Errorf("sections 2-3: %q", got)
	}
	for _, ask := range []string{"PLAN.md, 3 open questions", "PLAN.md 2026-10-28"} {
		if got := Read(ask, st, nil); !strings.HasPrefix(got, "## 1. One") || !strings.Contains(got, "(from "+filepath.Join(h, "state/x/PLAN.md")+")") {
			t.Errorf("%q should read the whole file: %q", ask, got)
		}
	}
	if got := Read("see https://github.com/o/r/blob/main/README.md", st, nil); got != "" {
		t.Errorf("a URL: %q", got)
	}

	// P3-6: a path outside the project's folders is not read.
	outside := write(t, filepath.Join(h, "secret/keys.md"), "key\n")
	for _, ask := range []string{"see ../../secret/keys.md", "read " + outside} {
		if got := Read(ask, st, nil); strings.Contains(got, "key\n") || !strings.Contains(got, "outside the project folders") {
			t.Errorf("%q: %q", ask, got)
		}
	}
}

// P2-5: the search runs under the queue's lock, so it has a time budget; a search that runs out says so.
func TestASearchThatRunsOutOfTimeSaysSo(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "state/STATUS.md"), "")
	write(t, filepath.Join(h, "repo/a/b/FAR.md"), "far\n")
	defer func(b time.Duration) { walkBudget = b }(walkBudget)
	walkBudget = 0
	if got := Read("FAR.md", Status{Path: status, Dir: filepath.Dir(status)}, []string{filepath.Join(h, "repo")}); !strings.HasPrefix(got, "Could not read FAR.md: the search stopped after ") {
		t.Errorf("no budget: %q", got)
	}
}

// Review round 2 of ee67b2e (P2): a walk per name shared one budget, so a word that names no file (GO-LIVE walked all of
// ~/Projects/Appxite in 1.4 s) left no time for the file after it. One walk looks for every name.
func TestAWordThatIsNoFileLeavesTimeForTheFile(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "state/STATUS.md"), "")
	write(t, filepath.Join(h, "repo/a/STATUS-2026-10-28.md"), "## 6. Questions\n1. Wait for 94489?\n")
	for i := range 200 {
		write(t, filepath.Join(h, "repo/z", strconv.Itoa(i)+".txt"), "")
	}
	defer func(c func() time.Time, b time.Duration) { clock, walkBudget = c, b }(clock, walkBudget)
	t0 := time.Now()
	n := 0
	clock = func() time.Time { n++; return t0.Add(time.Duration(n) * time.Millisecond) } // 1 ms per look
	walkBudget = 100 * time.Millisecond
	got := Read("OK on GO-LIVE; the 3 questions in STATUS-2026-10-28.md section 6", at(status), []string{filepath.Join(h, "repo")})
	if !strings.HasPrefix(got, "## 6. Questions\n1. Wait for 94489?\n(from ") || !strings.HasSuffix(got, "\n\nNot looked for to the end (the search stopped after 100ms): GO-LIVE.md.") {
		t.Errorf("the file was not read first, with the word's search said after it:\n%s", got)
	}
}

// Review round 2 of ee67b2e (P3): the other shapes it probed.
func TestRoundTwoShapesReadTheRightTextOrSaySo(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "state/STATUS.md"), "")
	repo := filepath.Join(h, "repo")
	write(t, filepath.Join(repo, "PROD-HANDOVER.md"), "## 12. Open\n")
	write(t, filepath.Join(repo, "PART.md"), "# Part\n## 12.2 Gate\ngate?\n## 12.3 Alerts\nwho?\n# Next\n")
	write(t, filepath.Join(repo, "LIST.md"), "## 4. Steps\n1. a\n2. run\n   ```sh\n# install\n   ```\n3. c\n")
	outside := write(t, filepath.Join(h, "secret/hosts.md"), "127.0.0.1 secret\n")
	if err := os.Symlink(outside, filepath.Join(repo, "hosts.md")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ ask, want string }{
		{"see https://github.com/o/r/blob/main/PROD-HANDOVER.md", ""},
		{"PART.md 12.2-12.8", "## 12.2 Gate\ngate?\n## 12.3 Alerts\nwho?\n(from " + filepath.Join(repo, "PART.md") + ` section 12.2-12.8 (no heading 12.8: read to "12.3 Alerts"))`},
		{"LIST.md 4.1-4.3", "1. a\n2. run\n   ```sh\n# install\n   ```\n3. c\n(from " + filepath.Join(repo, "LIST.md") + " section 4.1-4.3)"},
		{"hosts.md", "Could not read hosts.md: " + filepath.Join(repo, "hosts.md") + " leads outside the project folders, to "},
	} {
		if got := Read(c.ask, at(status), []string{repo}); !strings.HasPrefix(got, c.want) || c.want == "" && got != "" {
			t.Errorf("%s:\n got %q\nwant %q", c.ask, got, c.want)
		}
	}
	if got := Read("../x.md", Status{}, nil); got != "Could not read ../x.md: no project folder to look in." {
		t.Errorf("no roots: %q", got)
	}
}

// Review round 3 of baf3a3e (P2): a walk cut short after the first of two files of a name read it with no word that
// another may be there.
func TestAFileFoundBeforeTheSearchStoppedSaysSo(t *testing.T) {
	h := t.TempDir()
	status := write(t, filepath.Join(h, "state/STATUS.md"), "")
	write(t, filepath.Join(h, "repo/a/BUILD-DECISIONS.md"), "## B4 other project\nwrong\n")
	for i := range 200 {
		write(t, filepath.Join(h, "repo/m", strconv.Itoa(i)+".txt"), "")
	}
	write(t, filepath.Join(h, "repo/z/BUILD-DECISIONS.md"), "## B4 this project\nright\n")
	defer func(c func() time.Time, b time.Duration) { clock, walkBudget = c, b }(clock, walkBudget)
	t0 := time.Now()
	n := 0
	clock = func() time.Time { n++; return t0.Add(time.Duration(n) * time.Millisecond) }
	walkBudget = 50 * time.Millisecond
	got := Read("BUILD-DECISIONS B4", at(status), []string{filepath.Join(h, "repo")})
	if !strings.HasSuffix(got, " section B4; the search stopped after 50ms, so another file of that name may be there)") {
		t.Errorf("a file found by a cut-short search is read as if it were the only one:\n%s", got)
	}
}

// Review round 3 of baf3a3e (P3): a git tree path with "é" is quoted by ls-tree without -z, and a missing file's text
// named the revision with a stray "..".
func TestATreeReadsEveryPathAndSaysWhatIsMissing(t *testing.T) {
	repo := t.TempDir()
	write(t, filepath.Join(repo, "café/NOTE.md"), "## 1. Ask\nship it?\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "n"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	st := Status{Path: repo + " main:STATUS.md", Git: &Tree{Repo: repo, Rev: "main", Dir: "."}}
	if got, want := Read("NOTE.md 1", st, nil), "## 1. Ask\nship it?\n(from "+repo+" main:café/NOTE.md section 1)"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Read("GONE.md", st, nil), "Could not read GONE.md: no such file in main of "+repo+"."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
