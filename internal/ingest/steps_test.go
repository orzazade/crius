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
	now := time.Now()
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
		"Set `~/x/conf.toml` 4 -> 6.": " ~/x/conf.toml 6",
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
