package launchd

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Step 5 P5 (Crius 02:29): the goal report runs at 09:00 and 18:00, and does not run at load.
func TestDigestJobRunsAtNineAndEighteen(t *testing.T) {
	dir := t.TempDir()
	in, err := os.ReadFile("space.scifilab.crius.digest.plist.in")
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(dir, "digest.plist")
	text := strings.NewReplacer("@HOME@", "/Users/x", "@REPO@", "/r").Replace(string(in))
	if err := os.WriteFile(plist, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", plist).Output()
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		Label            string
		ProgramArguments []string
		RunAtLoad        bool
		Calendar         []map[string]int `json:"StartCalendarInterval"`
	}
	if err := json.Unmarshal(out, &job); err != nil {
		t.Fatal(err)
	}
	want := []map[string]int{{"Hour": 9, "Minute": 0}, {"Hour": 18, "Minute": 0}}
	if job.Label != "space.scifilab.crius.digest" || job.RunAtLoad || !reflect.DeepEqual(job.Calendar, want) ||
		!reflect.DeepEqual(job.ProgramArguments, []string{"/Users/x/.crius/bin/crius-digest-run"}) {
		t.Fatalf("%+v", job)
	}
}

// Review of f0f0039: the report job runs crius-ingest, so it needs the ingest job's environment (CRIUS_REPO: without
// it the ingest exits 2 and every scheduled run alerts). One list: the digest job's equals the ingest job's.
func TestDigestJobHasTheIngestEnvironment(t *testing.T) {
	env := func(name string) map[string]string {
		in, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		plist := filepath.Join(t.TempDir(), "p.plist")
		if err := os.WriteFile(plist, in, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("plutil", "-convert", "json", "-o", "-", plist).Output()
		if err != nil {
			t.Fatal(err)
		}
		var job struct{ EnvironmentVariables map[string]string }
		if err := json.Unmarshal(out, &job); err != nil {
			t.Fatal(err)
		}
		return job.EnvironmentVariables
	}
	ingest, digest := env("space.scifilab.crius.ingest.plist.in"), env("space.scifilab.crius.digest.plist.in")
	if ingest["CRIUS_REPO"] == "" || !reflect.DeepEqual(ingest, digest) {
		t.Fatalf("ingest %v\ndigest %v", ingest, digest)
	}
}

// run runs bin/crius-digest-run with stub binaries: crius-ingest exits with the codes in ingest, one per call;
// crius-rec digest exits with digest, writes a report and prints its path (prints a path but writes nothing when
// digest ends in "-nofile"); crius-rec record exits
// with record. It returns the exit code, the calls and the output.
func run(t *testing.T, ingest []string, digest, record string) (int, []string, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".crius", "bin")
	path := filepath.Join(home, "path")
	calls := filepath.Join(home, "calls")
	for _, d := range []string{bin, path} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stub := func(p, body string) {
		if err := os.WriteFile(p, []byte("#!/bin/bash\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stub(filepath.Join(bin, "crius-ingest"), `n=$(grep -c '^crius-ingest' "`+calls+`" 2>/dev/null); codes=(`+
		strings.Join(ingest, " ")+`); echo crius-ingest >> "`+calls+`"; exit ${codes[$n]}`+"\n")
	stub(filepath.Join(bin, "crius-rec"), `echo "crius-rec $*" >> "`+calls+`"
d=`+digest+`
case $1 in
digest) r="$HOME/.crius/state/digest/2026-10-17-0900.md"
  [ "$d" = "${d%-nofile}" ] && mkdir -p "${r%/*}" && echo r > "$r"
  echo "$r"; exit "${d%-nofile}";;
record) exit `+record+`;;
esac
exit 9
`)
	stub(filepath.Join(path, "sleep"), `echo "sleep $*" >> "`+calls+`"`+"\n")
	cmd := exec.Command("bash", "../bin/crius-digest-run")
	cmd.Env = []string{"HOME=" + home, "PATH=" + path + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	return rc, strings.Split(strings.TrimSpace(string(b)), "\n"), string(out)
}

// Step 5 P5: ingest first, then the report; exit 0 or 3 (partial: the report says why) is a run. Any other exit
// posts an ALERT on the crius bus and the job exits 1; a failed ingest still gets a report (it says the data is old);
// the ingest is retried while the 10-minute run holds the lock; an ALERT that cannot be posted is loud on stderr.
func TestDigestRunIngestsReportsAndAlerts(t *testing.T) {
	alert := func(text string) string {
		return "crius-rec record bus --project crius --role digest --type ALERT --text " + text
	}
	done := "crius-rec record bus --project crius --role digest --type DONE --to crius:lead --text goal report " +
		"~/.crius/state/digest/2026-10-17-0900.md"
	for _, c := range []struct {
		name           string
		ingest         []string
		digest, record string
		rc             int
		calls          []string
	}{
		{"both run", []string{"0"}, "0", "0", 0, []string{"crius-ingest", "crius-rec digest --write --print-path", done}},
		{"partial is a run", []string{"3"}, "3", "0", 0, []string{"crius-ingest", "crius-rec digest --write --print-path",
			done + " (partial: the report says why)"}},
		{"ingest fails", []string{"1"}, "0", "0", 1, []string{"crius-ingest",
			alert("goal report: crius-ingest exit 1; the report reads the last good run; see ~/.crius/logs/digest.log"),
			"crius-rec digest --write --print-path", done}},
		{"no report written", []string{"0"}, "0-nofile", "0", 1, []string{"crius-ingest", "crius-rec digest --write --print-path",
			alert("goal report: crius-rec digest --write exit 0 but its report ~/.crius/state/digest/2026-10-17-0900.md is missing")}},
		{"done cannot be posted", []string{"0"}, "0", "2", 1, []string{"crius-ingest", "crius-rec digest --write --print-path", done}},
		{"digest fails", []string{"0"}, "1", "0", 1, []string{"crius-ingest", "crius-rec digest --write --print-path",
			alert("goal report: crius-rec digest --write exit 1, no report; see ~/.crius/logs/digest.log")}},
		{"busy, then runs", []string{"75", "75", "0"}, "0", "0", 0, []string{"crius-ingest", "sleep 10", "crius-ingest",
			"sleep 10", "crius-ingest", "crius-rec digest --write --print-path", done}},
		{"busy for a minute", []string{"75", "75", "75", "75", "75", "75"}, "0", "0", 1, []string{"crius-ingest", "sleep 10",
			"crius-ingest", "sleep 10", "crius-ingest", "sleep 10", "crius-ingest", "sleep 10", "crius-ingest", "sleep 10",
			"crius-ingest", alert("goal report: crius-ingest busy for a minute (lock held); the report reads the last good run"),
			"crius-rec digest --write --print-path", done}},
		{"alert cannot be posted", []string{"0"}, "1", "2", 1, []string{"crius-ingest", "crius-rec digest --write --print-path",
			alert("goal report: crius-rec digest --write exit 1, no report; see ~/.crius/logs/digest.log")}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc, calls, out := run(t, c.ingest, c.digest, c.record)
			if rc != c.rc || !reflect.DeepEqual(calls, c.calls) {
				t.Fatalf("rc %d, calls\n%s\noutput\n%s", rc, strings.Join(calls, "\n"), out)
			}
			if lost := strings.Contains(out, "cannot post "); lost != (c.record != "0") {
				t.Fatalf("output\n%s", out)
			}
		})
	}
}

// Step 6 P5: the weekly job runs Thursday 04:00 (after the Max reset at 02:00 and the 03:00 backup), not at load, with
// the ingest job's environment.
func TestWeeklyJobRunsThursdayAtFour(t *testing.T) {
	in, err := os.ReadFile("space.scifilab.crius.weekly.plist.in")
	if err != nil {
		t.Fatal(err)
	}
	plist := filepath.Join(t.TempDir(), "weekly.plist")
	text := strings.NewReplacer("@HOME@", "/Users/x", "@REPO@", "/r").Replace(string(in))
	if err := os.WriteFile(plist, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("plutil", "-convert", "json", "-o", "-", plist).Output()
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		Label                string
		ProgramArguments     []string
		RunAtLoad            bool
		Calendar             map[string]int `json:"StartCalendarInterval"`
		EnvironmentVariables map[string]string
	}
	if err := json.Unmarshal(out, &job); err != nil {
		t.Fatal(err)
	}
	if job.Label != "space.scifilab.crius.weekly" || job.RunAtLoad ||
		!reflect.DeepEqual(job.Calendar, map[string]int{"Weekday": 4, "Hour": 4, "Minute": 0}) ||
		!reflect.DeepEqual(job.ProgramArguments, []string{"/Users/x/.crius/bin/crius-weekly-run"}) ||
		job.EnvironmentVariables["HOME"] != "/Users/x" {
		t.Fatalf("%+v", job)
	}
}

// longOut is appended to the stub's `scorecard --write` output.
var longOut string

// weekly runs bin/crius-weekly-run with a stub crius-rec: `scorecard --write` exits with write[0] and prints
// "wrote <path>", `scorecard --write --week W` exits with write[1] and prints "unchanged <path>", restore-test exits
// with restore and prints its two lines, record exits with record. weeks are the written weeks (.json files).
func weekly(t *testing.T, write [2]string, restore, record string, weeks ...string) (int, []string, string) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".crius", "bin")
	cards := filepath.Join(home, ".crius", "state", "scorecards")
	calls := filepath.Join(home, "calls")
	for _, d := range []string{bin, cards} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, w := range weeks {
		if err := os.WriteFile(filepath.Join(cards, w+".json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stub := `#!/bin/bash
echo "crius-rec $*" >> "` + calls + `"
case "$1 $2 $3" in
"scorecard --write ") echo "wrote $HOME/.crius/state/scorecards/2026-10-22.md` + longOut + `"; exit ` + write[0] + `;;
"scorecard --write --week") echo "unchanged $HOME/.crius/state/scorecards/$4.md"; exit ` + write[1] + `;;
"restore-test  ") echo "restore test pass: 1 matured week same as its file (2026-10-08)"; echo "2026-10-08 same"; exit ` + restore + `;;
"record bus --project") exit ` + record + `;;
esac
exit 9
`
	if err := os.WriteFile(filepath.Join(bin, "crius-rec"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "../bin/crius-weekly-run")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	return rc, strings.Split(strings.TrimSpace(string(b)), "\n"), string(out)
}

// Step 6 P5: write the ended week, write every written week again, run the restore test; one DONE names the new
// scorecard and the result. A step that fails posts an ALERT (and no DONE) and the job exits 1.
func TestWeeklyRunWritesRestoresAndReports(t *testing.T) {
	post := "crius-rec record bus --project crius --role weekly --type "
	done := post + "DONE --to crius:lead --text weekly: wrote ~/.crius/state/scorecards/2026-10-22.md · restore test " +
		"pass: 1 matured week same as its file (2026-10-08)"
	for _, c := range []struct {
		name         string
		write        [2]string
		restore, rec string
		weeks        []string
		rc           int
		calls        []string
	}{
		{"all run", [2]string{"0", "0"}, "0", "0", []string{"2026-10-08", "2026-10-15"}, 0, []string{
			"crius-rec scorecard --write", "crius-rec scorecard --write --week 2026-10-08",
			"crius-rec scorecard --write --week 2026-10-15", "crius-rec restore-test", done}},
		{"no Max sample is a run", [2]string{"3", "3"}, "0", "0", nil, 0, []string{
			"crius-rec scorecard --write", "crius-rec restore-test", done}},
		{"write fails", [2]string{"1", "0"}, "0", "0", nil, 1, []string{"crius-rec scorecard --write",
			post + "ALERT --text weekly: crius-rec scorecard --write exit 1: wrote " +
				"~/.crius/state/scorecards/2026-10-22.md; see ~/.crius/logs/weekly.log", "crius-rec restore-test"}},
		{"rewrite fails", [2]string{"0", "1"}, "0", "0", []string{"2026-10-08"}, 1, []string{
			"crius-rec scorecard --write", "crius-rec scorecard --write --week 2026-10-08",
			post + "ALERT --text weekly: crius-rec scorecard --write --week 2026-10-08 exit 1: unchanged " +
				"~/.crius/state/scorecards/2026-10-08.md; see ~/.crius/logs/weekly.log", "crius-rec restore-test"}},
		{"restore fails", [2]string{"0", "0"}, "1", "0", nil, 1, []string{"crius-rec scorecard --write",
			"crius-rec restore-test", post + "ALERT --text weekly: restore test exit 1: restore test pass: 1 matured " +
				"week same as its file (2026-10-08); see crius-rec health"}},
		{"done cannot be posted", [2]string{"0", "0"}, "0", "2", nil, 1, []string{"crius-rec scorecard --write",
			"crius-rec restore-test", done}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rc, calls, out := weekly(t, c.write, c.restore, c.rec, c.weeks...)
			if rc != c.rc || !reflect.DeepEqual(calls, c.calls) {
				t.Fatalf("rc %d, calls\n%s\noutput\n%s", rc, strings.Join(calls, "\n"), out)
			}
			if lost := strings.Contains(out, "cannot post "); lost != (c.rec != "0") {
				t.Fatalf("output\n%s", out)
			}
		})
	}
}

// Review of P5: a long output still fits the bus (300 characters, cut on a character, not a byte), and a failed
// step's whole output lands in the job's log, not only the cut ALERT.
func TestWeeklyRunCutsLongTextsAndLogsWholeOutput(t *testing.T) {
	longOut = " " + strings.Repeat("·é", 200) + " END"
	defer func() { longOut = "" }()
	for _, write := range []string{"0", "1"} {
		_, calls, out := weekly(t, [2]string{write, "0"}, "0", "0")
		post := calls[len(calls)-1]
		if write == "1" {
			post = calls[1]
			if !strings.Contains(out, "·é END") {
				t.Fatalf("whole output not logged:\n%s", out)
			}
		}
		text := post[strings.Index(post, "--text ")+len("--text "):]
		if n := utf8.RuneCountInString(text); n > 290 || !utf8.ValidString(text) {
			t.Fatalf("write %s: %d runes, valid %v: %q", write, n, utf8.ValidString(text), text)
		}
	}
}

// Live 10-22: the jobs ran binaries built from a work-in-progress worktree, and read it as CRIUS_REPO. The installer
// installs only from a clean checkout at origin/main (what was pushed), such as ~/Projects/scifi/crius-live.
func TestInstallOnlyFromPushedMain(t *testing.T) {
	repo, home := t.TempDir(), t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	script, err := os.ReadFile("../bin/crius-install-launchd")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "bin", "crius-install-launchd"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one")
	git("update-ref", "refs/remotes/origin/main", "HEAD")
	install := func() string {
		cmd := exec.Command(filepath.Join(repo, "bin", "crius-install-launchd"))
		cmd.Env = append(os.Environ(), "HOME="+home)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("installed into a test home: %s", out)
		}
		return string(out)
	}
	// At origin/main and clean: past the check, stopped by the missing ~/.crius (nothing installed).
	if out := install(); !strings.Contains(out, "~/.crius is missing") {
		t.Fatal(out)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if out := install(); !strings.Contains(out, "has changes not pushed") {
		t.Fatal("untracked file:", out)
	}
	git("add", "new.go")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "two")
	if out := install(); !strings.Contains(out, "is not at origin/main") {
		t.Fatal("commit not pushed:", out)
	}
	// Review of d11705b: a failed git read printed nothing, and two empty answers passed both checks.
	git("update-ref", "-d", "refs/remotes/origin/main")
	if out := install(); !strings.Contains(out, "has no origin/main") {
		t.Fatal("no origin/main:", out)
	}
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}
	if out := install(); !strings.Contains(out, "is not a git checkout") {
		t.Fatal("not a checkout:", out)
	}
}

// Crius 10-22 14:19: the installer built a hand-typed list, so crius-spawn (and crius-bash-guard before it) was never
// built. It builds every command under cmd/.
func TestInstallBuildsEveryCommand(t *testing.T) {
	repo, home, fakes := t.TempDir(), t.TempDir(), t.TempDir()
	script, err := os.ReadFile("../bin/crius-install-launchd")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"bin/crius-install-launchd", "cmd/alpha/main.go", "cmd/beta/main.go"} {
		if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, f), script, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "one"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".crius", "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	// go records each build; launchctl refuses, so nothing can reach the real launchd.
	log := filepath.Join(fakes, "builds")
	for name, body := range map[string]string{"go": `echo "$@" >> ` + log, "launchctl": "exit 1"} {
		if err := os.WriteFile(filepath.Join(fakes, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(repo, "bin", "crius-install-launchd"))
	cmd.Env = append(os.Environ(), "HOME="+home, "PATH="+fakes+":"+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput() // stops at the missing run scripts, after the builds
	builds, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err, string(out))
	}
	var built []string
	for _, l := range strings.Split(strings.TrimSpace(string(builds)), "\n") {
		built = append(built, l[strings.LastIndex(l, " ")+1:])
	}
	if want := []string{"./cmd/alpha", "./cmd/beta"}; !reflect.DeepEqual(built, want) {
		t.Fatalf("built %v, want %v", built, want)
	}
}
