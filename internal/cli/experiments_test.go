package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// Brief fixes-1025 item 7 (owner): the 09:00 goal report counts yesterday's experiments into
// state/experiments/<day>.md and includes them; the 18:00 one does not. A count that fails to look makes the report
// partial and says why; the goal report itself still goes out.
func TestTheMorningReportIncludesYesterdaysExperiments(t *testing.T) {
	home := findStore(t)
	user := t.TempDir()
	t.Setenv("HOME", user)
	repo := filepath.Join(user, "Projects", "Appxite", "svc_quote", "channels")
	os.MkdirAll(repo, 0o700)
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	os.MkdirAll(filepath.Join(user, "AppxiteRig", "run"), 0o700)
	report := func(now time.Time) (int, string, string) {
		var out, errb bytes.Buffer
		rc := digestCmd(home, now, true, false, &out, &errb)
		return rc, out.String(), errb.String()
	}
	morning := time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)
	rc, out, errs := report(morning)
	saved := filepath.Join(home, "state", "experiments", "2026-10-25.md")
	if rc != 0 && rc != exitPartial || !strings.Contains(out, "## Experiments (2026-10-25") || !strings.Contains(out, "| E8 |") {
		t.Fatal(rc, errs, out)
	}
	if b := testutil.Read(t, saved); !strings.Contains(out, b) {
		t.Fatalf("saved %q is not in the report", b)
	}
	if _, out, _ = report(morning.Add(9 * time.Hour)); strings.Contains(out, "## Experiments") {
		t.Fatal("the 18:00 report has the experiments:", out)
	}
	os.RemoveAll(filepath.Join(user, "AppxiteRig"))
	if rc, out, _ = report(morning.Add(24 * time.Hour)); rc != exitPartial || !strings.Contains(out, "Experiments: could not count 2026-10-26:") {
		t.Fatal(rc, out)
	}
}
