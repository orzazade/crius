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
// crius-rec digest exits with digest and writes a report (none when digest ends in "-nofile"; only last week's when
// it ends in "-old"); crius-rec record exits
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
  [ "$d" = "${d%-old}" ] || touch -t 202610100900 "$r"
  d=${d%-nofile}; exit "${d%-old}";;
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
		{"both run", []string{"0"}, "0", "0", 0, []string{"crius-ingest", "crius-rec digest --write", done}},
		{"partial is a run", []string{"3"}, "3", "0", 0, []string{"crius-ingest", "crius-rec digest --write",
			done + " (partial: the report says why)"}},
		{"ingest fails", []string{"1"}, "0", "0", 1, []string{"crius-ingest",
			alert("goal report: crius-ingest exit 1; the report reads the last good run; see ~/.crius/logs/digest.log"),
			"crius-rec digest --write", done}},
		{"no report written", []string{"0"}, "0-nofile", "0", 1, []string{"crius-ingest", "crius-rec digest --write",
			alert("goal report: crius-rec digest --write exit 0 but wrote no report in ~/.crius/state/digest")}},
		{"only an old report", []string{"0"}, "0-old", "0", 1, []string{"crius-ingest", "crius-rec digest --write",
			alert("goal report: crius-rec digest --write exit 0 but wrote no report in ~/.crius/state/digest")}},
		{"done cannot be posted", []string{"0"}, "0", "2", 1, []string{"crius-ingest", "crius-rec digest --write", done}},
		{"digest fails", []string{"0"}, "1", "0", 1, []string{"crius-ingest", "crius-rec digest --write",
			alert("goal report: crius-rec digest --write exit 1, no report; see ~/.crius/logs/digest.log")}},
		{"busy, then runs", []string{"75", "75", "0"}, "0", "0", 0, []string{"crius-ingest", "sleep 10", "crius-ingest",
			"sleep 10", "crius-ingest", "crius-rec digest --write", done}},
		{"busy for a minute", []string{"75", "75", "75", "75", "75", "75"}, "0", "0", 1, []string{"crius-ingest", "sleep 10",
			"crius-ingest", "sleep 10", "crius-ingest", "sleep 10", "crius-ingest", "sleep 10", "crius-ingest", "sleep 10",
			"crius-ingest", alert("goal report: crius-ingest busy for a minute (lock held); the report reads the last good run"),
			"crius-rec digest --write", done}},
		{"alert cannot be posted", []string{"0"}, "1", "2", 1, []string{"crius-ingest", "crius-rec digest --write",
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
