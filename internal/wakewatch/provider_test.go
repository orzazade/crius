package wakewatch_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/tabsend"
	"github.com/orzazade/crius/internal/testutil"
	"github.com/orzazade/crius/internal/wakewatch"
)

// Recorded panes (testdata/provider): Legion quotes:dev captured with `tmux capture-pane -p -e` on 11-15 23:57, its reply
// text replaced (public repo) and the error line Legion's transcript holds from 11-13 09:49 ("Please run /login · API
// Error: 403 Attention Required! | Cloudflare") put above its "✻ … done" line; tab-sleep.txt is a cortex · lead pane
// Crius read on 10-30 07:34, reply text replaced.
func pane(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", "provider", name))
	if err != nil {
		t.Fatal(err)
	}
	return tabsend.Visible(string(b))
}

func TestProviderStopReadsRecordedPanes(t *testing.T) {
	for _, c := range []struct {
		file, line string
		ok         bool
	}{
		{"legion-403.txt", "Please run /login · API Error: 403 Attention Required! | Cloudflare", true},
		{"tab-sleep.txt", "API Error: Your computer went to sleep mid-response. The response above may be incomplete.", true},
		{"legion-idle.txt", "", false},      // an ordinary ⏳ reply, a grey suggestion in the box
		{"legion-403-busy.txt", "", false},  // working again: the error is not where it stopped
		{"legion-403-typed.txt", "", false}, // someone typed into the box: not idle, not ours to clear
	} {
		line, _, ok := wakewatch.ProviderStop(pane(t, c.file))
		if ok != c.ok || line != c.line {
			t.Errorf("%s: ProviderStop = %q, %v; want %q, %v", c.file, line, ok, c.line, c.ok)
		}
	}
}

// The same stop read again keeps its key while the shell count beside it changes; a later stop gets a new one.
func TestProviderStopKeyIsTheStop(t *testing.T) {
	s := pane(t, "legion-403.txt")
	_, k1, _ := wakewatch.ProviderStop(s)
	_, k2, _ := wakewatch.ProviderStop(strings.Replace(s, "2 shells still running", "1 shell still running", 1))
	_, k3, ok := wakewatch.ProviderStop(strings.Replace(s, "done 11:57 PM", "done 12:09 AM", 1))
	if k1 == "" || k1 != k2 || !ok || k3 == k1 {
		t.Errorf("keys %q %q %q", k1, k2, k3)
	}
}

type fakeSweep struct {
	threads    []wakewatch.FailedThread
	threadsErr error
	screens    map[string]string
	sendErr    error
	sentThread []string
	sentTab    []string
	posts      []string
	crius      []string
}

func (f *fakeSweep) deps(home string, now time.Time) wakewatch.ProviderDeps {
	return wakewatch.ProviderDeps{Home: home, Now: now,
		Threads: func() ([]wakewatch.FailedThread, error) { return f.threads, f.threadsErr },
		Tabs:    func() ([]wakewatch.RemoteTab, error) { return wakewatch.ReadProviderTabs(home) },
		Capture: func(t wakewatch.RemoteTab) (string, error) {
			s, ok := f.screens[t.String()]
			if !ok {
				return "", errors.New("ssh: connect to host 100.109.19.101 port 22: Operation timed out")
			}
			return s, nil
		},
		SendThread: func(id, text string) error {
			f.sentThread = append(f.sentThread, id+" | "+text)
			return f.sendErr
		},
		SendTab: func(t wakewatch.RemoteTab, text string) error {
			f.sentTab = append(f.sentTab, t.String()+" | "+text)
			return f.sendErr
		},
		Post:      func(typ, text string) error { f.posts = append(f.posts, typ+" "+text); return nil },
		SendCrius: func(text string) error { f.crius = append(f.crius, text); return nil },
	}
}

// The 403 Cloudflare stop of AXI · ms-flow on 11-13 09:46 (its error item, as T3's store holds it).
func cloudflare(run string, ended time.Time) wakewatch.FailedThread {
	return wakewatch.FailedThread{ID: "mcp:launch:5a5f", Title: "AXI · ms-flow", Project: "AXI", Run: run, Status: "failed",
		Ended: ended, Class: "provider_error", Message: "Failed to authenticate. API Error: 403 Attention Required! | Cloudflare"}
}

func TestProviderErrorsRestartsAThreadOncePerStop(t *testing.T) {
	home := t.TempDir()
	t0 := time.Date(2026, 11, 13, 9, 50, 0, 0, time.UTC)
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", t0.Add(-3*time.Minute))}}
	rep, err := wakewatch.ProviderErrors(f.deps(home, t0))
	if err != nil {
		t.Fatal(err)
	}
	want := "mcp:launch:5a5f | Crius: your last turn failed on a provider error (Failed to authenticate. API Error: 403 Attention Required! | Cloudflare), not your work. Carry on where you were; re-arm your bus waiter first."
	if len(f.sentThread) != 1 || f.sentThread[0] != want {
		t.Fatalf("sent %q", f.sentThread)
	}
	if len(f.posts) != 1 || !strings.HasPrefix(f.posts[0], `INFO watchdog restarted T3 thread "AXI · ms-flow"`) || len(f.crius) != 1 || rep.Stopped != 1 {
		t.Fatalf("posts %q crius %q report %+v", f.posts, f.crius, rep)
	}
	// the same stop five minutes later: nothing again (no loop)
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(5*time.Minute))); err != nil || len(f.sentThread) != 1 || len(f.posts) != 1 {
		t.Fatalf("second look: err %v sent %q posts %q", err, f.sentThread, f.posts)
	}
	// it stops again 8 min after the restart: Crius hears, nothing is sent
	f.threads = []wakewatch.FailedThread{cloudflare("run:10", t0.Add(8*time.Minute))}
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sentThread) != 1 || len(f.posts) != 2 || !strings.HasPrefix(f.posts[1], `ALERT T3 thread "AXI · ms-flow"`) ||
		!strings.Contains(f.posts[1], "stopped again on a provider error") || !strings.Contains(f.posts[1], "8 min after the watchdog restarted it") || len(f.crius) != 2 {
		t.Fatalf("second stop: sent %q posts %q crius %d", f.sentThread, f.posts, len(f.crius))
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(15*time.Minute))); err != nil || len(f.posts) != 2 || len(f.crius) != 2 {
		t.Fatalf("the alerted stop said again: posts %q", f.posts)
	}
	// a stop over 15 min after the last restart is a new stop: one restart
	f.threads = []wakewatch.FailedThread{cloudflare("run:11", t0.Add(40*time.Minute))}
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(45*time.Minute))); err != nil || len(f.sentThread) != 2 {
		t.Fatalf("a later stop: err %v sent %q", err, f.sentThread)
	}
}

// Real T3 failures that "carry on" does not cure: a usage limit (11-15 19:41), a sign-in the owner must renew (11-14
// 00:27), and an interrupted run with no error item.
func TestProviderErrorsLeavesOtherFailures(t *testing.T) {
	f := &fakeSweep{threads: []wakewatch.FailedThread{
		{ID: "a", Title: "Cortex", Run: "r1", Status: "failed", Class: "usage_limit", Message: "Claude API rate limit reached. Try again later."},
		{ID: "b", Title: "AXI · ms-flow", Run: "r2", Status: "failed", Class: "provider_error",
			Message: "Claude could not authenticate. For subscription login, run `claude auth login` on this environment's machine"},
		{ID: "c", Title: "AXI · api-split", Run: "r3", Status: "interrupted"},
	}}
	rep, err := wakewatch.ProviderErrors(f.deps(t.TempDir(), time.Now()))
	if err != nil || len(f.sentThread)+len(f.posts)+len(f.crius) != 0 || rep.Threads != 3 || rep.Stopped != 0 {
		t.Fatalf("err %v sent %q posts %q report %+v", err, f.sentThread, f.posts, rep)
	}
}

func TestProviderErrorsRestartsARemoteTabOnce(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "# the Legion Quotes team\nlegion quotes:dev\nlegion quotes:qa\n")
	stopped := pane(t, "legion-403.txt")
	f := &fakeSweep{screens: map[string]string{"legion quotes:dev": stopped, "legion quotes:qa": pane(t, "legion-idle.txt")}}
	now := time.Date(2026, 11, 16, 0, 5, 0, 0, time.UTC)
	rep, err := wakewatch.ProviderErrors(f.deps(home, now))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.sentTab) != 1 || f.sentTab[0] != "legion quotes:dev | "+wakewatch.TabText || rep.Tabs != 2 || len(f.posts) != 1 ||
		f.posts[0] != "INFO watchdog restarted tab legion quotes:dev after a provider error (Please run /login · API Error: 403 Attention Required! | Cloudflare)" {
		t.Fatalf("sent %q posts %q report %+v", f.sentTab, f.posts, rep)
	}
	// read again with a shell finished beside the same stop: nothing again
	f.screens["legion quotes:dev"] = strings.Replace(stopped, "2 shells still running", "1 shell still running", 1)
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(5*time.Minute))); err != nil || len(f.sentTab) != 1 {
		t.Fatalf("again: %v %q", err, f.sentTab)
	}
	// a new stop 10 min after the restart: Crius hears, nothing typed
	f.screens["legion quotes:dev"] = strings.Replace(stopped, "done 11:57 PM", "done 12:14 AM", 1)
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(10*time.Minute))); err != nil || len(f.sentTab) != 1 ||
		len(f.posts) != 2 || !strings.HasPrefix(f.posts[1], "ALERT tab legion quotes:dev stopped again") {
		t.Fatalf("second stop: %v %q %q", err, f.sentTab, f.posts)
	}
}

// "Could not look" is loud: an error every run, and Crius hears it once it failed two sweeps in a row and once an hour
// while it lasts.
func TestProviderErrorsCouldNotLookIsLoud(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\n")
	f := &fakeSweep{threadsErr: errors.New("sqlite3: unable to open database file"), screens: map[string]string{}}
	now := time.Date(2026, 11, 16, 1, 0, 0, 0, time.UTC)
	_, err := wakewatch.ProviderErrors(f.deps(home, now))
	if err == nil || !strings.Contains(err.Error(), "could not look at the T3 Code threads") || !strings.Contains(err.Error(), "could not look at tab legion quotes:dev") {
		t.Fatalf("err %v", err)
	}
	if len(f.posts) != 0 {
		t.Fatalf("first failed sweep: posts %q", f.posts)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(5*time.Minute))); err == nil || len(f.posts) != 2 || len(f.crius) != 2 ||
		!strings.Contains(f.posts[0], "could not look at the T3 Code threads, so a session stopped there or waiting on a prompt is not seen") {
		t.Fatalf("5 min on: err %v posts %q", err, f.posts)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(10*time.Minute))); err == nil || len(f.posts) != 2 {
		t.Fatalf("10 min on: err %v posts %d", err, len(f.posts))
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(66*time.Minute))); err == nil || len(f.posts) != 4 {
		t.Fatalf("an hour on: err %v posts %d", err, len(f.posts))
	}
	// it can look again, then fails twice again: said on the second
	f.threadsErr = nil
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(70*time.Minute))); err == nil || len(f.posts) != 4 {
		t.Fatalf("threads back: err %v posts %d", err, len(f.posts))
	}
	f.threadsErr = errors.New("t3 down")
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(75*time.Minute))); err == nil || len(f.posts) != 4 {
		t.Fatalf("threads gone again once: err %v posts %q", err, f.posts)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(80*time.Minute))); err == nil || len(f.posts) != 5 {
		t.Fatalf("threads gone again twice: err %v posts %q", err, f.posts)
	}
}

// One failed look is not an outage (11-17 12:37: a 2-minute router restart failed one sweep's ssh to four tabs and woke
// Crius four times; the next sweep looked fine). A "could not look" is said only when the sweep before failed on the
// same thing too; a sweep that looks fine starts the count again. The error (exit 1) is there every failed sweep.
func TestProviderErrorsOneFailedLookIsNotSaid(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\nthinkpad staffy:builder\n")
	f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": ""}}
	now := time.Date(2026, 11, 17, 12, 37, 0, 0, time.UTC)
	sweep := func(min int) {
		t.Helper()
		if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(time.Duration(min)*time.Minute))); err == nil ||
			!strings.Contains(err.Error(), "could not look at tab legion quotes:dev") {
			t.Fatalf("sweep at +%d min: err %v", min, err)
		}
	}
	sweep(0)
	if len(f.posts) != 0 || len(f.crius) != 0 {
		t.Fatalf("one failed sweep: posts %q crius %q", f.posts, f.crius)
	}
	sweep(5)
	if len(f.posts) != 1 || len(f.crius) != 1 ||
		f.posts[0] != "ALERT the provider-error sweep could not look at tab legion quotes:dev, so a session stopped there or waiting on a prompt is not seen: ssh: connect to host 100.109.19.101 port 22: Operation timed out" {
		t.Fatalf("two failed sweeps in a row: posts %q crius %q", f.posts, f.crius)
	}

	// fail, ok, fail: nothing said
	home = t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\n")
	f = &fakeSweep{screens: map[string]string{}}
	sweep(0)
	f.screens["legion quotes:dev"] = ""
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(5*time.Minute))); err != nil {
		t.Fatalf("looked fine: %v", err)
	}
	delete(f.screens, "legion quotes:dev")
	sweep(10)
	if len(f.posts) != 0 || len(f.crius) != 0 {
		t.Fatalf("fail, ok, fail: posts %q crius %q", f.posts, f.crius)
	}

	// the count is per thing: one tab failing, then another, is two first failures
	home = t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\nthinkpad staffy:builder\n")
	f = &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": ""}}
	sweep(0)
	f.screens = map[string]string{"legion quotes:dev": ""}
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(5*time.Minute))); err == nil ||
		!strings.Contains(err.Error(), "could not look at tab thinkpad staffy:builder") {
		t.Fatalf("other tab failing: err %v", err)
	}
	if len(f.posts) != 0 || len(f.crius) != 0 {
		t.Fatalf("one tab, then another: posts %q crius %q", f.posts, f.crius)
	}
}

// A restart that could not be sent is said to Crius and not tried again: crius-tsend's NOT SENT may leave text in the box.
func TestProviderErrorsSendFailureAlertsOnce(t *testing.T) {
	home := t.TempDir()
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", time.Now())}, sendErr: fmt.Errorf("t3 thread send: exit status 1")}
	_, err := wakewatch.ProviderErrors(f.deps(home, time.Now()))
	if err == nil || len(f.posts) != 1 || !strings.Contains(f.posts[0], "could not restart it: t3 thread send: exit status 1") {
		t.Fatalf("err %v posts %q", err, f.posts)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, time.Now())); err != nil || len(f.sentThread) != 1 || len(f.posts) != 1 {
		t.Fatalf("again: err %v sent %q posts %q", err, f.sentThread, f.posts)
	}
}

func TestProviderErrorsDryDoesNothing(t *testing.T) {
	home := t.TempDir()
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", time.Now())}}
	d := f.deps(home, time.Now())
	d.Dry = true
	rep, err := wakewatch.ProviderErrors(d)
	if err != nil || len(f.sentThread)+len(f.posts)+len(f.crius) != 0 || len(rep.Actions) != 1 ||
		!strings.HasPrefix(rep.Actions[0], `would restart T3 thread "AXI · ms-flow"`) {
		t.Fatalf("err %v sent %q report %+v", err, f.sentThread, rep)
	}
	if _, err := os.Stat(wakewatch.ProviderStateFile(home)); !os.IsNotExist(err) {
		t.Fatalf("dry run saved its state: %v", err)
	}
}

// Review of 315b98e, P1-1: a 401 or an expired sign-in is not cured by "carry on" (the owner must sign in), though its
// line also says "API Error" and "Please run /login"; nor is a reply that only names a status code.
func TestProviderErrorsLeavesSignInAndPlainReplies(t *testing.T) {
	signin := "Please run /login · API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"OAuth token has expired.\"}}"
	s := pane(t, "legion-403.txt")
	for name, line := range map[string]string{
		"401":   signin,
		"reply": "The staging server returned 503 on the health check, so I stopped there.",
	} {
		if l, _, ok := wakewatch.ProviderStop(strings.Replace(s, "Please run /login · API Error: 403 Attention Required! | Cloudflare", line, 1)); ok {
			t.Errorf("%s: read as a provider stop: %q", name, l)
		}
	}
	f := &fakeSweep{threads: []wakewatch.FailedThread{{ID: "b", Title: "AXI · qa", Run: "r", Status: "failed", Class: "provider_error",
		Ended: time.Now(), Message: "Failed to authenticate. " + strings.TrimPrefix(signin, "Please run /login · ")}}}
	if _, err := wakewatch.ProviderErrors(f.deps(t.TempDir(), time.Now())); err != nil || len(f.sentThread) != 0 {
		t.Fatalf("err %v sent %q", err, f.sentThread)
	}
}

// Review P1-2: a state file it cannot read is said to Crius and set aside; the sweep still runs.
func TestProviderErrorsBadStateIsSaidAndSetAside(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderStateFile(home), "{\"Stops\": {")
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", time.Now())}}
	_, err := wakewatch.ProviderErrors(f.deps(home, time.Now()))
	if err == nil || len(f.sentThread) != 1 || len(f.posts) != 2 || !strings.Contains(f.posts[0], "could not read its state") {
		t.Fatalf("err %v sent %q posts %q", err, f.sentThread, f.posts)
	}
	if _, err := os.Stat(wakewatch.ProviderStateFile(home) + ".bad"); err != nil {
		t.Fatal(err)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, time.Now())); err != nil || len(f.sentThread) != 1 {
		t.Fatalf("next run: err %v sent %q", err, f.sentThread)
	}
}

// Review P1-3: a tab that dies again with the same error and no "✻" line under it is a new stop (the restart's text is
// above it): Crius hears, nothing is typed.
func TestProviderErrorsSameErrorAgainWithoutDoneLine(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\n")
	s := pane(t, "legion-403.txt")
	done := "✻ Cogitated for 4m 4s · done 11:57 PM · 2 shells still running"
	if !strings.Contains(s, done) {
		t.Fatal("fixture changed")
	}
	first := strings.Replace(s, done, "", 1)
	errLine := "⏺ Please run /login · API Error: 403 Attention Required! | Cloudflare"
	again := strings.Replace(first, errLine, errLine+"\n\n> "+wakewatch.TabText+"\n\n"+errLine, 1)
	f := &fakeSweep{screens: map[string]string{"legion quotes:dev": first}}
	now := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	if _, err := wakewatch.ProviderErrors(f.deps(home, now)); err != nil || len(f.sentTab) != 1 {
		t.Fatalf("first: %v %q", err, f.sentTab)
	}
	f.screens["legion quotes:dev"] = again
	if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(5*time.Minute))); err != nil || len(f.sentTab) != 1 ||
		len(f.posts) != 2 || !strings.Contains(f.posts[1], "stopped again") {
		t.Fatalf("again: %v %q %q", err, f.sentTab, f.posts)
	}
}

// Review P2-4: a restart whose state cannot be saved is not sent (else every run sends it again).
func TestProviderErrorsUnsavedRestartIsNotSent(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Dir(wakewatch.ProviderStateFile(home))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", time.Now())}}
	_, err := wakewatch.ProviderErrors(f.deps(home, time.Now()))
	if err == nil || !strings.Contains(err.Error(), "its state could not be saved") || len(f.sentThread) != 0 ||
		len(f.posts) != 1 || !strings.Contains(f.posts[0], "not restarted, as the watchdog cannot save its state") {
		t.Fatalf("err %v sent %q posts %q", err, f.sentThread, f.posts)
	}
}

// Review P2-5: a tab that dies 12 min after its restart, seen by the run 20 min after it, still counts as within 15
// min: it died after the run at 15 min saw it working.
func TestProviderErrorsTabStopTimedFromTheLastLook(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:dev\n")
	s := pane(t, "legion-403.txt")
	f := &fakeSweep{screens: map[string]string{"legion quotes:dev": s}}
	now := time.Date(2026, 11, 16, 3, 0, 0, 0, time.UTC)
	for i, screen := range []string{s, pane(t, "legion-idle.txt"), pane(t, "legion-idle.txt"), pane(t, "legion-idle.txt"),
		strings.Replace(s, "done 11:57 PM", "done 12:12 AM", 1)} {
		f.screens["legion quotes:dev"] = screen
		if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(time.Duration(i)*5*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sentTab) != 1 || len(f.posts) != 2 || !strings.Contains(f.posts[1], "stopped again") {
		t.Fatalf("sent %q posts %q", f.sentTab, f.posts)
	}
}

// Review P2-6, round 2 P2-1: a thread stopped long ago may have been left so: it is not restarted, and Crius hears of it
// once.
func TestProviderErrorsOldStopIsSaidNotRestarted(t *testing.T) {
	home, now := t.TempDir(), time.Now()
	f := &fakeSweep{threads: []wakewatch.FailedThread{cloudflare("run:9", now.Add(-13*time.Hour))}}
	for i := 0; i < 2; i++ {
		if _, err := wakewatch.ProviderErrors(f.deps(home, now.Add(time.Duration(i)*5*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sentThread) != 0 || len(f.posts) != 1 || !strings.Contains(f.posts[0], "13 h ago; not restarted") {
		t.Fatalf("sent %q posts %q", f.sentThread, f.posts)
	}
}

// Round 2 P2-2: with no "✻" line, a resize that rewraps the text above the error keeps the stop's key.
func TestProviderStopKeySurvivesARewrap(t *testing.T) {
	s := strings.Replace(pane(t, "legion-403.txt"), "✻ Cogitated for 4m 4s · done 11:57 PM · 2 shells still running", "", 1)
	wide := "⏺ I finished the row and posted DONE on the bus."
	if !strings.Contains(s, wide) {
		t.Fatal("fixture changed")
	}
	_, k1, ok1 := wakewatch.ProviderStop(s)
	_, k2, ok2 := wakewatch.ProviderStop(strings.Replace(s, wide, "⏺ I finished the row and posted\n  DONE on the bus.", 1))
	if !ok1 || !ok2 || k1 != k2 {
		t.Fatalf("keys %q %q", k1, k2)
	}
}

// testdata/provider/legion-git-clean-prompt.txt: Legion quotes:qa captured with `tmux capture-pane -p -e` on 11-17 14:40
// (scratch paths replaced, public repo), on the `git clean -f` permission prompt it sat on unanswered from 05:30 to
// 12:41 and again from 14:3x. Nobody was woken: the stuck-prompt check read only the Mac's own hook log and cmux tabs.
func TestProviderErrorsWakesCriusForARemoteTabOnAPrompt(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:qa\nlegion quotes:dev\n")
	stuck := pane(t, "legion-git-clean-prompt.txt")
	f := &fakeSweep{screens: map[string]string{"legion quotes:qa": stuck, "legion quotes:dev": pane(t, "legion-idle.txt")}}
	t0 := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC)
	sweep := func(min int) wakewatch.ProviderReport {
		t.Helper()
		rep, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute)))
		if err != nil {
			t.Fatalf("sweep at +%d min: %v", min, err)
		}
		return rep
	}
	if rep := sweep(0); len(f.crius) != 0 || rep.Tabs != 2 {
		t.Fatalf("first seen: crius %q report %+v", f.crius, rep)
	}
	sweep(5)
	if len(f.crius) != 0 {
		t.Fatalf("open 5 min: crius %q", f.crius)
	}
	sweep(10)
	want := `permission prompt unanswered for 10 min in tab legion quotes:qa: first seen at 05:30, "Reset b8352 to b1, apply API red subset SP=/tmp/claude-1000/-home-crius-work-app/0f3c1d2e-5a6b-4c7d-8e9f-a0b1c2d3e4f5/scratchpad; D=~/.crius/state/demo/notes/red-tests-on-old-code.diff; cd $SP/b8352 …"; the tab's screen still shows the dialog`
	if len(f.crius) != 1 || f.crius[0] != want || len(f.posts) != 1 || f.posts[0] != "ALERT "+want {
		t.Fatalf("no wake for the remote tab stuck on a prompt 10 min:\n crius %q\n posts %q\n want %q", f.crius, f.posts, want)
	}
	if len(f.sentTab) != 0 {
		t.Fatalf("a prompt is never typed into: sent %q", f.sentTab)
	}
	sweep(15) // the same prompt: said once
	if len(f.crius) != 1 {
		t.Fatalf("said again: %q", f.crius)
	}
	// answered, then a prompt again: timed afresh, and said once it is 10 min old
	f.screens["legion quotes:qa"] = pane(t, "legion-idle.txt")
	sweep(20)
	f.screens["legion quotes:qa"] = stuck
	sweep(25)
	sweep(30)
	if len(f.crius) != 1 {
		t.Fatalf("a new prompt open 5 min: %q", f.crius)
	}
	sweep(35)
	if len(f.crius) != 2 || !strings.HasPrefix(f.crius[1], "permission prompt unanswered for 10 min in tab legion quotes:qa: first seen at 05:55,") {
		t.Fatalf("the prompt asked again: %q", f.crius)
	}
}

// A tab that cannot be read is not a tab with no prompt: the prompt's timer goes on through failed looks, two failed
// looks in a row are said to Crius (the provider sweep's rule), and the wake comes once the tab can be read again.
func TestProviderErrorsFailedLookIsNotNoPrompt(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:qa\n")
	f := &fakeSweep{screens: map[string]string{"legion quotes:qa": pane(t, "legion-git-clean-prompt.txt")}}
	t0 := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC)
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	delete(f.screens, "legion quotes:qa") // ssh times out
	for _, min := range []int{5, 10} {
		if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err == nil ||
			!strings.Contains(err.Error(), "could not look at tab legion quotes:qa") {
			t.Fatalf("+%d min: a failed look must fail the run, got %v", min, err)
		}
	}
	want := "ALERT the provider-error sweep could not look at tab legion quotes:qa, so a session stopped there or waiting on a prompt is not seen: ssh: connect to host 100.109.19.101 port 22: Operation timed out"
	if len(f.posts) != 1 || f.posts[0] != want {
		t.Fatalf("two failed looks must say a prompt there is not seen:\n posts %q\n want %q", f.posts, want)
	}
	f.screens["legion quotes:qa"] = pane(t, "legion-git-clean-prompt.txt")
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(15*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.crius) != 2 || !strings.HasPrefix(f.crius[1], "permission prompt unanswered for 15 min in tab legion quotes:qa: first seen at 05:30,") {
		t.Fatalf("the failed looks reset the prompt's timer or lost it: crius %q", f.crius)
	}
}

// A tab list that cannot be read looks at no tab: every prompt's timer goes on. A tab listed twice wakes Crius once.
func TestProviderErrorsUnreadTabListKeepsPromptTimers(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "legion quotes:qa\nlegion quotes:qa\n")
	f := &fakeSweep{screens: map[string]string{"legion quotes:qa": pane(t, "legion-git-clean-prompt.txt")}}
	t0 := time.Date(2026, 11, 17, 5, 30, 0, 0, time.UTC)
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	d := f.deps(home, t0.Add(5*time.Minute))
	d.Tabs = func() ([]wakewatch.RemoteTab, error) { return nil, errors.New("permission denied") }
	if _, err := wakewatch.ProviderErrors(d); err == nil || !strings.Contains(err.Error(), "could not look at the remote tab list") {
		t.Fatalf("an unread tab list must fail the run, got %v", err)
	}
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.crius) != 1 || !strings.HasPrefix(f.crius[0], "permission prompt unanswered for 10 min in tab legion quotes:qa: first seen at 05:30,") {
		t.Fatalf("the unread tab list reset the prompt's timer, or a tab listed twice woke Crius twice: %q", f.crius)
	}
}
