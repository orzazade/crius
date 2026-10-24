package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// quotesBus gives a home whose bus.toml guards the quotes bus: shas must be commits in one repo, a hex word that starts a
// UUID in idDir is an id, and one live run at a time over 60 min. It returns the quotes log, the repo and its head sha.
func quotesBus(t *testing.T) (home, log, repo, head string) {
	home, _ = setup(t)
	log = filepath.Join(home, "bus", "quotes.log")
	testutil.Write(t, log, "")
	repo = testutil.GitRepo(t, filepath.Join(home, "..", "channels"))
	testutil.Write(t, filepath.Join(repo, "a.txt"), "a")
	testutil.CommitAll(t, repo)
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	idDir := filepath.Join(home, "..", "scripts")
	testutil.Write(t, filepath.Join(idDir, "seed.mjs"), "const customer = '837CFC7A-1111-2222-3333-444455556666';\n")
	testutil.Write(t, filepath.Join(home, "bus.toml"), fmt.Sprintf(
		"[projects.quotes]\nsha_repos = [%q]\nid_dirs = [%q]\nlive_window_min = 60\n", repo, idDir))
	return home, log, repo, strings.TrimSpace(string(out))
}

// 10-25 02:26: P5's wrapper had lost its guards (3rd time) and 'deadbee0', no commit, went out. bus-post refuses it
// itself, with the wrappers' exit code 3.
func TestAHexWordThatIsNoCommitIsRefused(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	for _, text := range []string{"P5 guard self-check deadbee0, ignore", "on sha=deadbee0", "(deadbee0).", "x/deadbee0",
		"crius:lead posting deadbeef00 on quotes"} {
		rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:p5", text)
		if rc != 3 || !strings.Contains(errs, "is not a commit") || busText(t, log) != "" {
			t.Errorf("%q: want rc 3 'is not a commit', got rc %d: %s", text, rc, errs)
		}
	}
	// keyed on the bus, not the sender: Crius posting on the quotes bus is checked too
	rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "crius:lead", "quotes:lead", "see deadbee0")
	if rc != 3 || busText(t, log) != "" {
		t.Errorf("crius:lead on quotes: want rc 3, got %d: %s", rc, errs)
	}
}

func TestACommitAndWordsThatAreNoShaArePosted(t *testing.T) {
	_, log, _, head := quotesBus(t)
	short := head[:8]
	for i := 9; !hasLetter.MatchString(short); i++ { // an all-digit prefix is never checked: take one with a letter
		short = head[:i]
	}
	for _, text := range []string{
		"READY on " + short + ", gates PASS",
		"full sha " + head,
		"run 20261025 at 20261024T222002Z", // all digits, and not hex
		"customer 837cfc7a-1111-2222-3333-444455556666 and state f9-riga-calllogs-767fdd3c",
		"customer 837cfc7a only", // the start of a UUID in id_dirs is an id
		"short 3622eda is under 8",
	} {
		if rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", text); rc != 0 {
			t.Errorf("%q: want posted, got rc %d: %s", text, rc, errs)
		}
	}
}

// The wrappers' way to post an id that is no commit: SAY_NOT_SHA="id,id" (the agent sets it; bus-post inherits it).
func TestSayNotShaLetsAnIdThrough(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	t.Setenv("SAY_NOT_SHA", "9177b8ea,abcdef12")
	if rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "session 9177b8ea"); rc != 0 {
		t.Fatal(rc, errs)
	}
	rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "session 9177b8eb")
	if rc != 3 || !strings.Contains(errs, "SAY_NOT_SHA") {
		t.Fatal(rc, errs)
	}
}

// A project with no entry in bus.toml has no sha check (Crius's own bus names session ids of 8 hex all the time).
func TestABusWithoutGuardsTakesAnyHexWord(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	if rc, errs := post(t, at("2026-10-25T02:26"), filepath.Join(filepath.Dir(log), "crius.log"), "INFO", "crius:lead",
		"crius:lead", "session 9177b8ea"); rc != 0 {
		t.Fatal(rc, errs)
	}
}

// Failed to look is not found nothing: a sha repo that is gone refuses the line.
func TestAMissingShaRepoIsLoud(t *testing.T) {
	home, log, _, _ := quotesBus(t)
	testutil.Write(t, filepath.Join(home, "bus.toml"), "[projects.quotes]\nsha_repos = [\"/nonexistent/repo\"]\n")
	rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "on deadbee0")
	if rc != 2 || !strings.Contains(errs, "could not check") || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

func TestABadBusTomlIsLoud(t *testing.T) {
	home, log, _, _ := quotesBus(t)
	testutil.Write(t, filepath.Join(home, "bus.toml"), "[projects.quotes]\nsha_repo = [\"x\"]\n")
	rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "x")
	if rc != 2 || !strings.Contains(errs, "bus.toml") || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// 10-25 01:44 and 02:20: two live STARTs crossed. A START is refused (exit 4, as the wrappers) while a sender's newest
// START of the last live_window_min minutes has no later DONE or CORRECTION from that sender, own START included.
func TestAStartWhileALiveRunIsOpenIsRefused(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	p6 := "2026-10-25T02:20 START quotes:p6 -> quotes:all — P6 live START: mockup-scan\n"
	testutil.Write(t, log, p6)
	rc, errs := post(t, at("2026-10-25T02:20"), log, "START", "quotes:p1", "quotes:all", "P1 live START: live-f55")
	if rc != 4 || !strings.Contains(errs, "a live run is open") || !strings.Contains(errs, "quotes:p6") || busText(t, log) != p6 {
		t.Fatalf("want rc 4 naming quotes:p6, got rc %d: %s", rc, errs)
	}
	// its own open START blocks a sender too
	rc, errs = post(t, at("2026-10-25T02:21"), log, "START", "quotes:p6", "quotes:all", "again")
	if rc != 4 {
		t.Fatalf("own open START: want rc 4, got %d: %s", rc, errs)
	}
	// other types pass while a run is open
	if rc, errs := post(t, at("2026-10-25T02:21"), log, "INFO", "quotes:p1", "quotes:all", "waiting"); rc != 0 {
		t.Fatal(rc, errs)
	}
}

func TestADoneOrCorrectionClosesTheRun(t *testing.T) {
	for _, closer := range []string{"DONE", "CORRECTION"} {
		_, log, _, _ := quotesBus(t)
		testutil.Write(t, log, "2026-10-25T02:20 START quotes:p6 -> quotes:all — run\n"+
			"2026-10-25T02:21 "+closer+" quotes:p6 -> quotes:all — P6 live END\n")
		if rc, errs := post(t, at("2026-10-25T02:22"), log, "START", "quotes:p1", "quotes:all", "P1 live START"); rc != 0 {
			t.Errorf("%s: want posted, got rc %d: %s", closer, rc, errs)
		}
	}
	// another sender's DONE does not close it
	_, log, _, _ := quotesBus(t)
	testutil.Write(t, log, "2026-10-25T02:20 START quotes:p6 -> quotes:all — run\n"+
		"2026-10-25T02:21 DONE quotes:p3 -> quotes:all — P3 done\n")
	if rc, errs := post(t, at("2026-10-25T02:22"), log, "START", "quotes:p1", "quotes:all", "P1 live START"); rc != 4 {
		t.Errorf("a DONE from another sender: want rc 4, got %d: %s", rc, errs)
	}
}

func TestAStartOlderThanTheWindowIsNotOpen(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	testutil.Write(t, log, "2026-10-25T01:20 START quotes:p6 -> quotes:all — run\n")
	if rc, errs := post(t, at("2026-10-25T02:21"), log, "START", "quotes:p1", "quotes:all", "go"); rc != 0 {
		t.Fatal(rc, errs)
	}
	_, log, _, _ = quotesBus(t)
	testutil.Write(t, log, "2026-10-25T01:21 START quotes:p6 -> quotes:all — run\n")
	if rc, errs := post(t, at("2026-10-25T02:21"), log, "START", "quotes:p1", "quotes:all", "go"); rc != 4 {
		t.Fatal("60 min old is still open:", rc, errs)
	}
}

// QA3 02:21: two same-moment STARTs both passed the check. The check and the write hold one lock on the bus log, so of
// many STARTs at once exactly one is written.
func TestSameMomentStartsPostOnlyOne(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	const n = 12
	var wg sync.WaitGroup
	rcs := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var errb strings.Builder
			rcs[i] = run([]string{log, "START", fmt.Sprintf("quotes:p%d", i), "quotes:all", "live START"}, at("2026-10-25T02:20"), &errb)
		}()
	}
	wg.Wait()
	posted, refused := 0, 0
	for _, rc := range rcs {
		switch rc {
		case 0:
			posted++
		case 4:
			refused++
		}
	}
	if lines := strings.Count(busText(t, log), "\n"); posted != 1 || refused != n-1 || lines != 1 {
		t.Fatalf("posted %d, refused %d, lines %d (rcs %v)", posted, refused, lines, rcs)
	}
}

// quotes:lead 04:58: a line to quotes:P5 went unread for 2 h (builders grep their own name in lower case).
func TestARecipientNameInUpperCaseIsRefused(t *testing.T) {
	_, log := setup(t)
	for _, to := range []string{"quotes:P5", "quotes:lead,quotes:P6", "quotes:all, Quotes:qa1"} {
		rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:lead", to, "x")
		if rc != 2 || !strings.Contains(errs, "lower case") || busText(t, log) != "" {
			t.Errorf("%q: want rc 2 'lower case', got %d: %s", to, rc, errs)
		}
	}
	if rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:lead", "quotes:all, quotes:rig", "x"); rc != 0 {
		t.Error(rc, errs)
	}
}

// Review: a quotes role posting on a bus with no guards is checked against its own project's repos.
func TestTheSendersProjectGuardsTooOnAnotherBus(t *testing.T) {
	_, log, _, _ := quotesBus(t)
	crius := filepath.Join(filepath.Dir(log), "crius.log")
	rc, errs := post(t, at("2026-10-25T02:26"), crius, "INFO", "quotes:p5", "crius:lead", "on deadbee0")
	if rc != 3 || busText(t, crius) != "" {
		t.Fatal(rc, errs)
	}
}

// Review: "a, b" names b too; an inherited GIT_DIR does not swap the repo checked.
func TestSayNotShaTrimsAndGitDirIsIgnored(t *testing.T) {
	_, log, _, head := quotesBus(t)
	t.Setenv("SAY_NOT_SHA", "abcdef12, 9177b8ea")
	if rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "session 9177b8ea"); rc != 0 {
		t.Fatal("spaced SAY_NOT_SHA:", rc, errs)
	}
	t.Setenv("GIT_DIR", t.TempDir()) // no repo there
	if rc, errs := post(t, at("2026-10-25T02:26"), log, "INFO", "quotes:p5", "quotes:lead", "full sha "+head); rc != 0 {
		t.Fatal("GIT_DIR from the caller:", rc, errs)
	}
}

// A bus whose table has no live_window_min takes any START.
func TestNoLiveWindowNoStartRule(t *testing.T) {
	home, log, repo, _ := quotesBus(t)
	testutil.Write(t, filepath.Join(home, "bus.toml"), fmt.Sprintf("[projects.quotes]\nsha_repos = [%q]\n", repo))
	testutil.Write(t, log, "2026-10-25T02:20 START quotes:p6 -> quotes:all — run\n")
	if rc, errs := post(t, at("2026-10-25T02:21"), log, "START", "quotes:p1", "quotes:all", "go"); rc != 0 {
		t.Fatal(rc, errs)
	}
}
