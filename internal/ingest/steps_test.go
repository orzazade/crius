package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// heavyRun is one line of a heavy runner's runs.log.
type heavyRun struct {
	At      string `json:"at"`
	Key     string `json:"key"`
	Cmd     string `json:"cmd"`
	Cwd     string `json:"cwd"`
	Seconds int    `json:"seconds"`
	WaitS   int    `json:"waitS"`
	Code    int    `json:"code"`
	Killed  bool   `json:"killed"`
}

func runsLog(t *testing.T, path string, runs []heavyRun) {
	t.Helper()
	var b strings.Builder
	for _, r := range runs {
		j, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	testutil.Write(t, path, b.String())
}

// stepConfig writes <home>/stepwatch.toml for one log named legion at path, plus extra.
func stepConfig(t *testing.T, home, path, extra string) {
	t.Helper()
	testutil.Write(t, filepath.Join(home, "stepwatch.toml"), fmt.Sprintf("[[log]]\nname = \"legion\"\npath = %q\n%s", path, extra))
}

func at(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// apiRuns gives the Legion's Api history: whole runs of tests/Quote.Api.Tests of mins minutes each, every 4 hours from
// start, in worktree wt-<i>, with a short filtered run between each pair (a different step that must not mix in).
func apiRuns(start time.Time, n int, cmd string, mins float64, cwd string) []heavyRun {
	var out []heavyRun
	for i := 0; i < n; i++ {
		ts := start.Add(time.Duration(i) * 4 * time.Hour)
		c := cwd
		if c == "" {
			c = fmt.Sprintf("/home/crius/Projects/Appxite/svc_quote/wt/b2-ac%d", 100+i)
		}
		out = append(out, heavyRun{At: at(ts), Cmd: cmd, Cwd: c, Seconds: int(mins * 60), WaitS: 30},
			heavyRun{At: at(ts.Add(time.Hour)), Cmd: "dotnet test tests/Quote.Api.Tests -p:NuGetAudit=false --filter FullyQualifiedName~SendQuote|FullyQualifiedName~Quote.Api.Tests.Contracts",
				Cwd: c, Seconds: 120, WaitS: 10})
	}
	return out
}

func stepNeeds(x *ic) []string {
	return grepAlerts(x.alerts(), "NEED crius:ingest -> crius:lead — step slower")
}

// The miss of 11-20: the Quotes Legion gate began running tests/Quote.Api.Tests whole (41 min) where it had taken 15;
// nothing said so. The step watch posts one NEED naming the step, before and after minutes, and the first slow run's
// time and folder, and only once however many runs read it again.
func TestApiFullGoingFrom15To41MinPostsOneNeed(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	before := apiRuns(now.Add(-5*24*time.Hour), 8, "bash -c cd /home/crius/Projects/Appxite/svc_quote/wt/b2-x && dotnet test tests/Quote.Api.Tests --nologo", 15, "")
	slow := apiRuns(now.Add(-20*time.Hour), 4, "dotnet test tests/Quote.Api.Tests/Quote.Api.Tests.csproj --nologo", 41,
		"/home/crius/Projects/Appxite/svc_quote/wt/legion-gate")
	runsLog(t, log, append(before, slow...))
	stepConfig(t, x.home, log, "")
	for i := 0; i < 2; i++ {
		if st := x.run(); st.Status != "complete" {
			t.Fatalf("run %d: %s %q", i, st.Status, st.Warnings)
		}
	}
	got := stepNeeds(x)
	first := now.Add(-20 * time.Hour).Local().Format("2006-01-02 15:04")
	if len(got) != 1 || !strings.Contains(got[0], "`dotnet test tests/Quote.Api.Tests`") ||
		!strings.Contains(got[0], "15 -> 41 min") || !strings.Contains(got[0], first) ||
		!strings.Contains(got[0], "wt/legion-gate") || !strings.Contains(got[0], "legion") {
		t.Fatalf("want one NEED naming the Api step 15 -> 41 min, first slow run %s in wt/legion-gate, got %q", first, got)
	}
	t.Logf("crius.log: %s", got[0])
}

// Steady times, and a filtered run that is short beside a whole run, post nothing.
func TestSteadyStepTimesPostNothing(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	runs := apiRuns(now.Add(-6*24*time.Hour), 30, "dotnet test tests/Quote.Api.Tests --nologo", 15, "")
	for i := range runs {
		runs[i].Seconds += (i % 5) * 20 // 15 to 16.3 min
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	if st := x.run(); st.Status != "complete" {
		t.Fatalf("%s %q", st.Status, st.Warnings)
	}
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("steady times posted %q", got)
	}
}

// The grouped runner (api-groups.ts, the Api suite in parallel groups) and a whole run are one step when the config says
// so: the Legion's real history (10 min grouped on 11-14, 40 min whole from 11-19).
func TestAnAliasedGroupedRunnerIsTheSameStep(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	before := apiRuns(now.Add(-6*24*time.Hour), 6, "bun /Users/o/Projects/Appxite/svc_quote/wt/b2-api-groups/poc/ui-app/scripts/api-groups.ts run 3", 10.5, "")
	slow := apiRuns(now.Add(-10*time.Hour), 3, "dotnet test tests/Quote.Api.Tests -p:NuGetAudit=false", 40, "")
	runsLog(t, log, append(before, slow...))
	stepConfig(t, x.home, log, "[alias]\n\"bun api-groups.ts run\" = \"dotnet test tests/Quote.Api.Tests\"\n")
	x.run()
	got := stepNeeds(x)
	if len(got) != 1 || !strings.Contains(got[0], "10 -> 40 min") || !strings.Contains(got[0], "bun api-groups.ts run 3") {
		t.Fatalf("want one NEED 10 -> 40 min that names the grouped runner it was before, got %q", got)
	}
	t.Logf("crius.log: %s", got[0])
}

// A failed or killed run is not a step time: three killed 20-min runs after 10-min runs post nothing.
func TestFailedRunsAreNotStepTimes(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	runs := append(apiRuns(now.Add(-5*24*time.Hour), 6, "dotnet test tests/Quote.Api.Tests", 10, ""),
		apiRuns(now.Add(-10*time.Hour), 3, "dotnet test tests/Quote.Api.Tests", 20, "")...)
	for i := len(runs) - 6; i < len(runs); i++ {
		runs[i].Code, runs[i].Killed = 124, true
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("killed runs posted %q", got)
	}
}

// Queue wait: a day whose jobs waited longer in all than they ran posts one NEED with both totals; a day under that, none.
func TestADayWhoseWaitExceedsItsRunTimePostsOneNeed(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local) // a fixed noon: no run of the test sits on midnight
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	log := filepath.Join(x.root, "legion-runs.log")
	var runs []heavyRun
	for i := 0; i < 6; i++ { // yesterday: 6 x 10 min run, 6 x 5 min wait
		runs = append(runs, heavyRun{At: at(day.Add(-24*time.Hour + time.Duration(i+1)*time.Hour)), Cmd: "dotnet test tests/A", Cwd: "/w", Seconds: 600, WaitS: 300})
	}
	for i := 0; i < 6 && day.Add(time.Duration(i)*time.Minute).Before(now); i++ { // today: 6 x 10 min run, 6 x 20 min wait
		runs = append(runs, heavyRun{At: at(day.Add(time.Duration(i) * time.Minute)), Cmd: "dotnet test tests/A", Cwd: "/w", Seconds: 600, WaitS: 1200})
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	x.run()
	x.run()
	got := grepAlerts(x.alerts(), "NEED crius:ingest -> crius:lead — heavy queue")
	if len(got) != 1 || !strings.Contains(got[0], day.Format("2006-01-02")) || !strings.Contains(got[0], "waited 2.0 h") ||
		!strings.Contains(got[0], "ran 1.0 h") {
		t.Fatalf("want one queue NEED for today (waited 2.0 h, ran 1.0 h), got %q", got)
	}
	t.Logf("crius.log: %s", got[0])
}

// A configured log that cannot be read is loud: the run is partial and says which.
func TestAMissingStepLogIsLoud(t *testing.T) {
	x := newIC(t, cfg{})
	stepConfig(t, x.home, filepath.Join(x.root, "nope.log"), "")
	st := x.run()
	if st.Status != "partial" || !strings.Contains(strings.Join(st.Warnings, " "), "nope.log") {
		t.Fatalf("want partial naming nope.log, got %s %q", st.Status, st.Warnings)
	}
}

// decisionRig: a Crius bus with one DECISION line at ago, a fake host command for "legion" that runs its argument in sh,
// and the settings file holding holds.
func decisionRig(t *testing.T, text string, ago time.Duration, holds string) (*ic, string) {
	x := newIC(t, cfg{})
	slots := filepath.Join(x.root, "legion-home", "AppxiteRig", "heavy", "slots")
	testutil.Write(t, slots, holds+"\n")
	host := filepath.Join(x.root, "L")
	testutil.Write(t, host, "#!/bin/sh\nexec sh -c \"$*\"\n")
	if err := os.Chmod(host, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-ago).Format("2006-01-02T15:04")
	text = strings.ReplaceAll(text, "SLOTS", slots)
	testutil.Write(t, filepath.Join(x.home, "bus", "quotes.log"), ts+" DECISION crius:lead -> quotes:lead — "+text+"\n")
	log := filepath.Join(x.root, "runs.log")
	runsLog(t, log, nil)
	stepConfig(t, x.home, log, fmt.Sprintf("[hosts]\nlegion = %q\n[settings]\n\"heavy slots\" = %q\n", host, slots))
	return x, ts
}

func decisionNeeds(x *ic) []string {
	return grepAlerts(x.alerts(), "NEED crius:ingest -> crius:lead — decision not applied")
}

// The miss of 11-20 17:55: "raise Legion heavy slots to 2" was never applied and nothing noticed. 30 minutes on, the
// file still holds 1: one NEED naming the decision, the file and both values, however many runs follow.
func TestADecisionNotAppliedAfter30MinPostsOneNeed(t *testing.T) {
	x, ts := decisionRig(t, "Legion heavy slots is 1 (SLOTS), so 8363 waited 1800 s and timed out. Raise it to 2 now.",
		45*time.Minute, "1")
	x.run()
	x.run()
	got := decisionNeeds(x)
	if len(got) != 1 || !strings.Contains(got[0], ts) || !strings.Contains(got[0], "legion:") ||
		!strings.Contains(got[0], "holds 1, the decision says 2") {
		t.Fatalf("want one NEED for the %s decision (holds 1, says 2), got %q", ts, got)
	}
	t.Logf("crius.log: %s", got[0])
}

// The named form ("heavy slots 1 -> 2") resolves through [settings]; applied, it posts nothing.
func TestAnAppliedDecisionPostsNothing(t *testing.T) {
	x, _ := decisionRig(t, "Legion heavy slots 1 -> 2 now (load 2/12).", 45*time.Minute, "2")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("an applied decision posted %q", got)
	}
}

// Younger than 30 minutes: not yet checked.
func TestAFreshDecisionIsNotCheckedYet(t *testing.T) {
	x, _ := decisionRig(t, "Legion heavy slots 1 -> 2 now.", 10*time.Minute, "1")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("a 10-min-old decision posted %q", got)
	}
}

// A later decision on the same file wins: "1 -> 2" then "2 -> 1" with the file at 1 posts nothing.
func TestALaterDecisionOnTheSameFileWins(t *testing.T) {
	x, _ := decisionRig(t, "Legion heavy slots 1 -> 2 now.", 2*time.Hour, "1")
	appendFile(t, filepath.Join(x.home, "bus", "quotes.log"),
		time.Now().Add(-time.Hour).Format("2006-01-02T15:04")+" DECISION crius:lead -> quotes:lead — Legion heavy slots 2 -> 1, two gates clash on ports.\n")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("the superseded decision posted %q", got)
	}
}

// The step kind: worktree paths, shas, wrappers and output flags are not part of it; a filter is.
func TestStepKind(t *testing.T) {
	for in, want := range map[string]string{
		"dotnet test tests/Quote.Api.Tests/Quote.Api.Tests.csproj --nologo":                                        "dotnet test tests/Quote.Api.Tests",
		"bash -c cd /home/crius/x/wt/b2-b155 && dotnet test tests/Quote.Api.Tests -p:NuGetAudit=false 2>&1":        "dotnet test tests/Quote.Api.Tests",
		"sh -c export PATH=$HOME/.dotnet:$PATH; dotnet test tests/Quote.Api.Tests -p:NuGetAudit=false | tail -4":   "dotnet test tests/Quote.Api.Tests",
		"dotnet test tests/Quote.Api.Tests --nologo --filter FullyQualifiedName~A|FullyQualifiedName~B":            "dotnet test tests/Quote.Api.Tests --filter FullyQualifiedName~A|FullyQualifiedName~B",
		`bash -c cd /x && dotnet test tests/Quote.Api.Tests --filter "FullyQualifiedName~Q R" -v q`:                "dotnet test tests/Quote.Api.Tests --filter FullyQualifiedName~Q R",
		"timeout 900 bash /home/crius/.crius/state/quotes/build/bin/train/e2e-tip.sh e3f8cca190550523948b968d41f6": "bash e2e-tip.sh",
		"bun /Users/o/Projects/x/wt/b2-api-groups/poc/ui-app/scripts/api-groups.ts run 3":                          "bun api-groups.ts run 3",
		"sh -c npm run typecheck && npm run lint && npx vitest run":                                                "npm run typecheck && npm run lint && npx vitest run",
		"env NuGetPackageSourceCredentials_AppXite.Default=Username=az;Password=SYNTHETIC-TOKEN":                   "",
		"bash -c cd /x/wt/a && dotnet test tests/A > /tmp/out.log 2>&1; echo rc=$?":                                "dotnet test tests/A",
		`perl -e alarm shift; exec @ARGV or die "exec: $!" 1200 bun test ./test/a.test.ts`:                         "bun test ./test/a.test.ts",
	} {
		if got := stepKind(in); got != want {
			t.Errorf("stepKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// Two filtered runs with different test sets are two steps: a long filter after many short ones is no change (one kind
// for every filter flagged 7 false changes on the Legion log of 11-21).
func TestDifferentFiltersAreDifferentSteps(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	var runs []heavyRun
	for i := 0; i < 20; i++ {
		runs = append(runs, heavyRun{At: at(now.Add(-time.Duration(100-i) * time.Hour)), Cmd: "dotnet test tests/Quote.Api.Tests --filter FullyQualifiedName~Contracts",
			Cwd: "/w", Seconds: 120})
	}
	for i := 0; i < 3; i++ {
		runs = append(runs, heavyRun{At: at(now.Add(-time.Duration(10-i) * time.Hour)), Cmd: "dotnet test tests/Quote.Api.Tests --filter FullyQualifiedName~SendQuote|FullyQualifiedName~Prices",
			Cwd: "/w", Seconds: 1200})
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("a different filter posted %q", got)
	}
}

// A step 6 minutes longer (3 -> 9 min, three times as long) is not worth a NEED: under the 10-minute gain.
func TestASmallGainPostsNothing(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	runs := append(apiRuns(now.Add(-5*24*time.Hour), 8, "bash s-orders.sh", 3, ""), apiRuns(now.Add(-12*time.Hour), 3, "bash s-orders.sh", 9, "")...)
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("a 6-min gain posted %q", got)
	}
}

// Only a path at a word edge is a path: the real lines of 11-18 and 11-20 that read as /heavy-max=1 and /M2=3.
func TestParseDecision(t *testing.T) {
	c := &stepConf{Hosts: map[string]string{"legion": "L"}, Settings: map[string]string{"heavy slots": "~/AppxiteRig/heavy/slots"}}
	for text, want := range map[string]string{
		"Load limit lifted: 15-min load 8.1 (limit 28), 55% memory free. qa/heavy-max = 2 now; lanes read it.": "",
		"then F5.E.f1-f3, then F5.M1/M2. Batch small ones to QA up to 3 at once.":                              "",
		"Legion heavy slots 1 -> 2 now (load 2/12).":                                                           "legion ~/AppxiteRig/heavy/slots 2",
		"Legion heavy slots is 1 (~/AppxiteRig/heavy/slots), so it timed out. Raise it to 2 now.":              "legion ~/AppxiteRig/heavy/slots 2",
		"heavy slots stay at 2; move the Api gate to 3 groups.":                                                "",
		"keep ~/.crius/state/quotes/STATUS.md current; QA batch up to 3.":                                      "",
		"Mac heavy slots 1 -> 2 (the Legion stays at 1).":                                                      " ~/AppxiteRig/heavy/slots 2",
		"Legion heavy slots: 1 -> 2.":                                                                          "legion ~/AppxiteRig/heavy/slots 2",
		"ȺȺȺȺȺȺȺȺȺȺȺȺ heavy slots 1 -> 3":                                                                      " ~/AppxiteRig/heavy/slots 3",
		"Set `~/x/conf.toml` 4 -> 6.":                                                                          " ~/x/conf.toml 6",
	} {
		got := ""
		if d, ok := parseDecision(text, c); ok {
			got = d.host + " " + d.path + " " + d.value
		}
		if got != want {
			t.Errorf("parseDecision(%q) = %q, want %q", text, got, want)
		}
	}
}

// A step that swings between slow and fast is one change, not one NEED per swing (review P2: bash s-tip-i.sh posted
// 1 -> 12 min three times in 7 hours on the Mac log).
func TestASwingingStepIsOneChange(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "runs.log")
	var runs []heavyRun
	add := func(from time.Duration, n int, mins int) {
		for i := 0; i < n; i++ {
			runs = append(runs, heavyRun{At: at(now.Add(-from + time.Duration(i)*20*time.Minute)), Cmd: "bash s-tip-i.sh", Cwd: "/w", Seconds: mins * 60, Code: 0})
		}
	}
	add(72*time.Hour, 10, 1)
	add(20*time.Hour, 3, 12)
	add(18*time.Hour, 3, 1)
	add(16*time.Hour, 3, 12)
	add(14*time.Hour, 3, 1)
	add(12*time.Hour, 3, 12)
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	x.run()
	if got := stepNeeds(x); len(got) != 1 {
		t.Fatalf("want one NEED for the swinging step, got %d: %q", len(got), got)
	}
}

// A host that cannot be read once posts nothing; three runs in a row post "not checked" once; when it can be read again
// and still holds the old value, "not applied" still goes out (review P2: one key for both used it up).
func TestAnUnreadableHostIsLoudOnlyAfterThreeRunsAndDoesNotHideNotApplied(t *testing.T) {
	x, _ := decisionRig(t, "Legion heavy slots 1 -> 2 now.", 45*time.Minute, "1")
	host := filepath.Join(x.root, "L")
	testutil.Write(t, host, "#!/bin/sh\necho 'ssh: connect to host: timed out' >&2\nexit 255\n")
	x.run()
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("two failed reads posted %q", got)
	}
	x.run()
	x.run()
	if got := grepAlerts(x.alerts(), "decision not checked"); len(got) != 1 || !strings.Contains(got[0], "timed out") {
		t.Fatalf("want one 'not checked' naming the error, got %q", got)
	}
	testutil.Write(t, host, "#!/bin/sh\nexec sh -c \"$*\"\n")
	x.run()
	x.run()
	if got := decisionNeeds(x); len(got) != 1 {
		t.Fatalf("want one 'not applied' once the host answers, got %q", got)
	}
}

// A decision about a file here (no host word) reads it here; a confirmed one is not read again.
func TestALocalDecisionIsReadHereAndOnce(t *testing.T) {
	x, _ := decisionRig(t, "Mac heavy slots 1 -> 2 now.", 45*time.Minute, "2")
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("an applied local decision posted %q", got)
	}
	slots := filepath.Join(x.root, "legion-home", "AppxiteRig", "heavy", "slots")
	testutil.Write(t, slots, "1\n") // changed after the check: a later change is not this decision's business
	x.run()
	if got := grepAlerts(x.alerts(), "NEED"); len(got) != 0 {
		t.Fatalf("a confirmed decision was read again: %q", got)
	}
}

// heavyCut is heavy.ts's record of a command: cmd cut at 300 characters, key the script's full path when a word names a
// file, else the full words and " in <cwd>".
func heavyCut(r heavyRun, script string) heavyRun {
	if script != "" {
		r.Key = script
	} else {
		r.Key = r.Cmd + " in " + r.Cwd
	}
	if len(r.Cmd) > 300 {
		r.Cmd = r.Cmd[:300]
	}
	return r
}

// The false NEED of 11-21 03:36: `bun all-suites.ts run rigport3.noindex -Users-…-Ap` went "3 -> 21 min", but the cut
// command mixed different suite parts. Runs of one script whose words were cut have no full name on record (the key is
// the script alone), so they are not watched, and the run says how many it left out.
func TestStepsThatShareTheFirst300CharsAreNotOneStep(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "mac-runs.log")
	script := "/Users/o/Projects/Appxite/AXI/first-order/docs/build/merge.noindex/all-suites.ts"
	dir := "/private/tmp/claude-501/-Users-o-Projects-Appxite-AXI-first-order/961aacd5-4a80-4c7f-8295-765d0e55af0c/scratchpad/rigport3.noindex"
	part := func(name string) string { // the suite part's name comes after the cut
		return "bun " + script + " run " + dir + " " + dir + "/" + strings.Repeat("x", 120) + "/" + name
	}
	var runs []heavyRun
	for i := 0; i < 10; i++ { // a 3-min part every 4 hours
		runs = append(runs, heavyCut(heavyRun{At: at(now.Add(-5*24*time.Hour + time.Duration(i)*4*time.Hour)), Cmd: part("unit"),
			Cwd: "/Users/o/Projects/Appxite/AXI/first-order", Seconds: 180}, script))
	}
	for i := 0; i < 3; i++ { // then a different, 21-min part
		runs = append(runs, heavyCut(heavyRun{At: at(now.Add(-10*time.Hour + time.Duration(i)*time.Hour)), Cmd: part("integration"),
			Cwd: "/Users/o/Projects/Appxite/AXI/first-order", Seconds: 21 * 60}, script))
	}
	if a, b := runs[0].Cmd, runs[len(runs)-1].Cmd; a != b {
		t.Fatalf("fixture: the two parts must share the cut command, got %q and %q", a, b)
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	st := x.run()
	if st.Status != "complete" {
		t.Fatalf("%s %q", st.Status, st.Warnings)
	}
	if got := stepNeeds(x); len(got) != 0 {
		t.Fatalf("two different parts under one cut command posted %q", got)
	}
	if st.StepRuns != 13 || st.StepUnwatched["legion:all-suites.ts"] != 13 || len(st.StepUnwatched) != 1 {
		t.Fatalf("want 13 runs read and 13 of all-suites.ts not watched (cut, no full name), got %d and %v", st.StepRuns, st.StepUnwatched)
	}
	if len(st.Warnings) != 0 {
		t.Fatalf("not watched is a count in the summary, not a warning that never clears: %q", st.Warnings)
	}
}

// A command with no script word is on record in full in its key: two filter sets that share the first 300 characters
// are two steps, and the one that slows down still posts, named in full.
func TestACutCommandWithItsFullWordsInTheKeyIsStillWatched(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	long := "dotnet test tests/Quote.Api.Tests --filter " + strings.Repeat("FullyQualifiedName~Quote.Api.Tests.Presenter|", 7)
	var runs []heavyRun
	for i := 0; i < 10; i++ {
		ts := now.Add(-5*24*time.Hour + time.Duration(i)*4*time.Hour)
		runs = append(runs,
			heavyCut(heavyRun{At: at(ts), Cmd: long + "FullyQualifiedName~Fast", Cwd: "/home/crius/wt/a", Seconds: 600}, ""),
			heavyCut(heavyRun{At: at(ts.Add(time.Hour)), Cmd: long + "FullyQualifiedName~Slow", Cwd: "/home/crius/wt/a", Seconds: 600}, ""))
	}
	for i := 0; i < 3; i++ {
		ts := now.Add(-10*time.Hour + time.Duration(i)*time.Hour)
		runs = append(runs,
			heavyCut(heavyRun{At: at(ts), Cmd: long + "FullyQualifiedName~Fast", Cwd: "/home/crius/wt/a", Seconds: 600}, ""),
			heavyCut(heavyRun{At: at(ts.Add(time.Minute)), Cmd: long + "FullyQualifiedName~Slow", Cwd: "/home/crius/wt/a", Seconds: 38 * 60}, ""))
	}
	if len(runs[0].Cmd) != 300 {
		t.Fatalf("fixture: the command must be cut, got %d chars", len(runs[0].Cmd))
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "")
	st := x.run()
	got := stepNeeds(x)
	if len(got) != 1 || !strings.Contains(got[0], "FullyQualifiedName~Slow`") || !strings.Contains(got[0], "10 -> 38 min") {
		t.Fatalf("want one NEED for the Slow filter set 10 -> 38 min, got %q", got)
	}
	if len(st.StepUnwatched) != 0 {
		t.Fatalf("every run had its full words, got %v not watched", st.StepUnwatched)
	}
}

// A NEED fits one bus line (300 characters) with its end: a long step, several commands it ran as before and a long
// scratchpad folder are shortened, and the line still names the step's end, its minutes and its first slow run.
func TestALongStepNeedFitsABusLineWithItsEnd(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Now()
	log := filepath.Join(x.root, "legion-runs.log")
	long := "dotnet test tests/Quote.Api.Tests --filter " + strings.Repeat("FullyQualifiedName~Quote.Api.Tests.Presenter|", 4)
	cwd := "/tmp/claude-1000/-Users-orkhanrzazade-Projects-Appxite-svc-quote-channels/58128bf4-551e-452a-bd69-e8369995f3f6/scratchpad/wt-gate"
	var runs []heavyRun
	for i := 0; i < 9; i++ { // before as three different commands
		runs = append(runs, heavyRun{At: at(now.Add(-5*24*time.Hour + time.Duration(i)*4*time.Hour)),
			Cmd: fmt.Sprintf("bun /x/api-groups.ts run %d %s", i%3, strings.Repeat("--group-file api-groups.json ", 3)), Cwd: cwd, Seconds: 600})
	}
	for i := 0; i < 3; i++ {
		runs = append(runs, heavyRun{At: at(now.Add(-10*time.Hour + time.Duration(i)*time.Hour)), Cmd: long + "FullyQualifiedName~Slow", Cwd: cwd, Seconds: 38 * 60})
	}
	runsLog(t, log, runs)
	stepConfig(t, x.home, log, "[alias]\n\"bun api-groups.ts run\" = \""+long+"FullyQualifiedName~Slow\"\n")
	x.run()
	got := stepNeeds(x)
	if len(got) != 1 {
		t.Fatalf("want one NEED, got %q", got)
	}
	_, text, _ := strings.Cut(got[0], "— ")
	if n := len([]rune(text)); n > 300 || !strings.Contains(text, "~Slow`") || !strings.Contains(text, "10 -> 38 min") ||
		!strings.Contains(text, "first slow run") || !strings.Contains(text, "scratchpad/wt-gate") {
		t.Fatalf("want a line of at most 300 characters naming ~Slow, 10 -> 38 min and the first slow run in scratchpad/wt-gate, got %d: %q", n, text)
	}
	t.Logf("crius.log: %s", text)
}

func TestShortNameKeepsStartAndEnd(t *testing.T) {
	k := strings.Repeat("ä", 50) + strings.Repeat("b", 50) + "Ende"
	got := shortName(k, 40)
	if n := len([]rune(got)); n != 40 || !strings.HasPrefix(got, "ää") || !strings.HasSuffix(got, "bEnde") || !strings.Contains(got, " … ") {
		t.Fatalf("shortName(%d runes, 40) = %q (%d runes)", len([]rune(k)), got, n)
	}
	if shortName("short", 40) != "short" {
		t.Fatal("a short name must stay as it is")
	}
}
