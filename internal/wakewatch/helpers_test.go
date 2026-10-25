package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// boot is a helper list and the processes, launchd jobs and bus around it, at t0 (10-25 11:44 the machine restarted;
// 02:26 the account move stopped the Quotes lead's hourly timer, and nobody listed what had to be started again).
func boot(t *testing.T, helpers string) (BootDeps, *[]string) {
	_, home := testutil.Temp(t)
	testutil.Write(t, filepath.Join(home, "helpers.toml"), helpers)
	written := t0.Add(-time.Hour)
	for name, pid := range map[string]string{"live": "100", "dead": "200", "reused": "300"} {
		p := filepath.Join(home, name+".pid")
		testutil.Write(t, p, pid+"\n")
		if err := os.Chtimes(p, written, written); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-50*time.Minute))+" INFO quotes:lead -> crius:lead — Lead 10:54 queue: 7 open READYs\n"+
			stamp(t0.Add(-10*time.Minute))+" INFO quotes:p2 -> crius:lead — P2 queue: mine\n"+
			stamp(t0.Add(-9*time.Hour))+" STATUS quotes:rig -> crius:lead — Rig hourly: x\n")
	var sent []string
	return BootDeps{Home: home, Now: t0,
		Process: func(pid int) (time.Time, bool, error) {
			switch pid {
			case 100:
				return written.Add(-time.Second), true, nil // it wrote the file
			case 300:
				return t0.Add(-10 * time.Minute), true, nil // a later process given the old pid
			}
			return time.Time{}, false, nil
		},
		Launchd: func(label string) (bool, error) { return label == "space.scifilab.crius.wakewatch", nil },
		Send:    func(text string) error { sent = append(sent, text); return nil },
	}, &sent
}

const helpersToml = `
[[pid]]
name = "rig billing watch"
file = "~/live.pid"
[[pid]]
name = "rig machine watch"
file = "~/dead.pid"
[[pid]]
name = "standin"
file = "~/reused.pid"
[[pid]]
name = "no file"
file = "~/none.pid"
[[launchd]]
name = "wakewatch"
label = "space.scifilab.crius.wakewatch"
[[launchd]]
name = "watchdog"
label = "space.scifilab.crius.watchdog"
[[bus]]
name = "quotes hourly report"
log = "quotes"
from = "quotes:lead"
has = ["hourly", " queue:"]
within_min = 75
[[bus]]
name = "rig hourly"
log = "quotes"
from = "quotes:rig"
has = ["hourly"]
within_min = 75
`

func TestAfterBootNamesEachDeadHelperToCrius(t *testing.T) {
	d, sent := boot(t, helpersToml)
	t.Setenv("HOME", d.Home) // "~/" in the list is the user's home; the test's files sit in its Crius home
	if err := AfterBoot(d); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("want one message, got %q", *sent)
	}
	m := (*sent)[0]
	if !strings.HasPrefix(m, Prefix+"after login, 5 of 8 helpers are not running") {
		t.Fatalf("%q", m)
	}
	for _, want := range []string{"rig machine watch (pid 200 from ~/dead.pid is gone)",
		"standin (pid 300 from ~/reused.pid is another process now)", "no file (no ~/none.pid)",
		"watchdog (launchd space.scifilab.crius.watchdog not loaded)",
		"rig hourly (last quotes:rig line with 'hourly' 9h0m ago)"} {
		if !strings.Contains(m, want) {
			t.Errorf("lacks %q: %s", want, m)
		}
	}
	for _, alive := range []string{"billing", "wakewatch (", "quotes hourly"} {
		if strings.Contains(m, alive) {
			t.Errorf("names running %q: %s", alive, m)
		}
	}
}

func TestAfterBootWithAllRunningSendsNothing(t *testing.T) {
	d, sent := boot(t, "[[pid]]\nname = \"x\"\nfile = \"~/live.pid\"\n")
	t.Setenv("HOME", d.Home)
	if err := AfterBoot(d); err != nil || len(*sent) != 0 {
		t.Fatal(err, *sent)
	}
}

// Failed to look is not found nothing: a list that does not read, or a check that fails, goes to Crius too.
func TestAfterBootThatCannotLookSaysSo(t *testing.T) {
	for _, c := range []struct{ toml, want string }{
		{"[[pid]]\nname = \"x\"\nfil = \"a\"\n", "could not read the helper list"},
		{"[[launchd]]\nname = \"x\"\nlabel = \"y\"\n", "x (could not look: launchctl hung)"},
	} {
		d, sent := boot(t, c.toml)
		d.Launchd = func(string) (bool, error) { return false, errors.New("launchctl hung") }
		if err := AfterBoot(d); err != nil || len(*sent) != 1 || !strings.Contains((*sent)[0], c.want) {
			t.Errorf("%q: %v %q", c.toml, err, *sent)
		}
	}
}

// Review of d2bbf74 (P2s): a long list goes in several lines, none cut, and a heartbeat stamped in the future is no
// heartbeat.
func TestAfterBootSendsEveryNameAndDistrustsFutureLines(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&b, "[[pid]]\nname = \"helper-%02d\"\nfile = \"~/watchers/helper-%02d.pid\"\n", i, i)
	}
	b.WriteString("[[bus]]\nname = \"future\"\nlog = \"quotes\"\nfrom = \"quotes:qa9\"\nhas = [\"beat\"]\nwithin_min = 75\n")
	d, sent := boot(t, b.String())
	t.Setenv("HOME", d.Home)
	testutil.Write(t, filepath.Join(d.Home, "bus", "quotes.log"), stamp(t0.Add(24*time.Hour))+" INFO quotes:qa9 -> x — beat\n")
	if err := AfterBoot(d); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(*sent, "\n")
	for i := 1; i <= 20; i++ {
		if want := fmt.Sprintf("helper-%02d (no ~/watchers/helper-%02d.pid)", i, i); !strings.Contains(all, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if !strings.Contains(all, "future (no quotes:qa9 line") {
		t.Errorf("a future line counted as a heartbeat:\n%s", all)
	}
	for _, m := range *sent {
		if len([]rune(m)) > len(Prefix)+MaxLine || strings.HasSuffix(m, "…") {
			t.Errorf("cut: %q", m)
		}
	}
}
