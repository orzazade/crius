package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/testutil"
	"github.com/orzazade/crius/internal/wakewatch"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// fakeCmux puts a cmux on PATH that records each call, one per line.
func fakeCmux(t *testing.T) (log string) {
	log, _ = fakeTab(t)
	return log
}

// fakeTab puts a fake cmux and ps on PATH. cmux logs every call but its reads (tree, read-screen) and acts like the
// real one: the JSON tree (dir/tree.json) shows Crius's tab, titled "crius", on ttys015; read-screen prints
// dir/screen if there is one, else a Claude input box holding dir/draft (one read late if dir/lag exists, as Claude redraws after a key;
// an empty box shows Claude's queued-messages hint if dir/queued exists, or grey text dir/ghost, which read-screen
// shows plain as cmux does); rpc surface.input_state says draft or empty as dir/draft holds text or not (cmux reads
// the style, so grey text is empty); send refuses over a draft (not over grey text), else types into it (plus
// dir/extra, if any: someone typing at the same time); send-key enter sends the draft (fails if dir/enterfail exists).
// ps on ttys015 prints dir/front (Claude in front) and on another tty dir/front-<tty>.
func fakeTab(t *testing.T) (log, dir string) {
	dir = t.TempDir()
	log = filepath.Join(dir, "calls")
	f := func(name string) string { return filepath.Join(dir, name) }
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"--json) cat " + f("warn") + " >&2 2>/dev/null; cat " + f("tree.json") + "; exit 0 ;;\n" +
		"read-screen) if [ -f " + f("screen") + " ]; then cat " + f("screen") + "; exit 0; fi\n  src=" + f("draft") +
		"; [ -f " + f("lag") + " ] && [ -f " + f("shown") + " ] && src=" + f("shown") + "\n" +
		"  box=$(cat $src); [ -z \"$box\" ] && [ -f " + f("queued") + " ] && box='Press up to edit queued messages'\n" +
		"  [ -z \"$box\" ] && [ -f " + f("ghost") + " ] && box=$(cat " + f("ghost") + ")\n" +
		"  printf '⏺ done.\\n──────────────\\n❯ %s\\n──────────────\\n  quota: 7d 97%%\\n' \"$box\"\n" +
		"  [ -f " + f("lag") + " ] && cp " + f("draft") + " " + f("shown") + "; exit 0 ;;\n" +
		"rpc) state=empty; [ -s " + f("draft") + " ] && state=draft\n" +
		"  printf '{\"surface_id\":\"%s\",\"state\":\"%s\"}' \"$(printf %s \"$3\" | sed 's/.*\"surface_id\":\"\\([^\"]*\\)\".*/\\1/')\" $state; exit 0 ;;\nesac\n" +
		"for a in \"$@\"; do printf '[%s]' \"$a\" >> " + log + "; last=$a; done\necho >> " + log + "\n" +
		"case \"$1\" in\nsend) [ -s " + f("draft") + " ] && { echo 'Error: the prompt holds a draft' >&2; exit 1; }\n" +
		"  printf '%s' \"$last\" > " + f("draft") + "; cat " + f("extra") + " >> " + f("draft") + " 2>/dev/null ;;\n" +
		"send-key) [ -f " + f("enterfail") + " ] && exit 1; : > " + f("draft") + " ;;\nesac\necho OK\n"
	ps := "#!/bin/sh\nif [ \"$2\" = ttys015 ]; then cat " + filepath.Join(dir, "front") + "; else cat " +
		filepath.Join(dir, "front-") + "\"$2\" 2>/dev/null; fi\nexit 0\n"
	for name, body := range map[string]string{"cmux": script, "ps": ps} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"draft":     "",
		"front":     "52166 52763 /usr/bin/login\n52169 52763 -/bin/zsh\n52763 52763 /Users/o/.local/bin/claude\n52859 52763 <defunct>\n",
		"tree.json": treeJSON(map[string][3]string{"workspace:1000020000": {"surface:1000020029", "crius", "ttys015"}}),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CRIUS_HOME", t.TempDir())
	return log, dir
}

// treeJSON gives a cmux --json tree --all answer with one terminal surface per workspace: {ref, title, tty}.
func treeJSON(ws map[string][3]string) string {
	var out []string
	for w, s := range ws {
		out = append(out, fmt.Sprintf(`{"ref":%q,"panes":[{"ref":"pane:1","surfaces":[{"ref":%q,"id":%q,"title":%q,"tty":%q,"type":"terminal"}]}]}`,
			w, s[0], "ID-"+s[0], s[1], s[2]))
	}
	return `{"active":{},"windows":[{"ref":"window:1","workspaces":[` + strings.Join(out, ",") + `]}]}`
}

// Owner 10-21 23:5x: no Telegram; an alert is one line typed into Crius's own tab, then Enter.
func TestSendTypesTheLineIntoCriusTab(t *testing.T) {
	log := fakeCmux(t)
	if err := send("WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "[send][--workspace][workspace:1000020000][--surface][surface:1000020029][--][WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86]\n" +
		"[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n"
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
}

// One tab that cannot be read must not hide the others.
func TestOneFailedTabReadKeepsTheOthers(t *testing.T) {
	real := outbox.Cmux
	t.Cleanup(func() { outbox.Cmux = real })
	outbox.Cmux = func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "--json":
			return []byte(treeJSON(map[string][3]string{"workspace:1": {"surface:1", "quotes · lead", "ttys001"},
				"workspace:2": {"surface:2", "axi · lead", "ttys002"}})), nil
		case args[2] == "surface:2":
			return []byte("surface gone"), errors.New("exit status 1")
		}
		return []byte("question?\n❓\n"), nil
	}
	got, err := screens()
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Surface < got[j].Surface })
	if len(got) != 2 || got[0].Title != "quotes · lead" || got[0].Text != "question?\n❓\n" || got[0].Err != "" || !strings.Contains(got[1].Err, "surface gone") {
		t.Fatalf("%+v", got)
	}
}

func TestLoadIsTheOneMinuteAverage(t *testing.T) {
	if v, err := parseLoad("{ 6.93 8.27 8.48 }\n"); err != nil || v != 6.93 {
		t.Fatal(v, err)
	}
	for _, bad := range []string{"", "{ }", "vm.loadavg: unknown oid"} {
		if _, err := parseLoad(bad); err == nil {
			t.Errorf("%q read as a load", bad)
		}
	}
}

func TestSwapUsedIsReadInGB(t *testing.T) {
	for in, want := range map[string]float64{
		"total = 17408.00M  used = 15997.06M  free = 1410.94M  (encrypted)\n": 15997.06 / 1024,
		"total = 30.00G  used = 21.50G  free = 8.50G  (encrypted)\n":          21.5,
		"total = 0.00M  used = 0.00M  free = 0.00M\n":                         0,
	} {
		got, err := swapUsed(in)
		if err != nil || got < want-0.001 || got > want+0.001 {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := swapUsed("vm.swapusage: unknown oid"); err == nil {
		t.Error("an unreadable answer gave no error")
	}
}

// Review of 91854cf, P1: if Claude has exited, Crius's tab is a shell and Enter would run the alert text there
// ("$(...)" in a bus line runs). Nothing is typed unless Claude is the tab's foreground process, and that is loud.
func TestSendTypesOnlyWhenClaudeIsInFront(t *testing.T) {
	for name, ps := range map[string]string{
		"shell in front":   "6777 6832 /usr/bin/login\n6832 6832 -/bin/zsh\n",
		"claude in back":   "52763 99 /Users/o/.local/bin/claude\n99 99 vim\n",
		"no front process": "",
		"claude-like name": "7 7 /usr/local/bin/claude-wrapper\n",
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "front"), []byte(ps), 0o644)
		err := send("WAKEWATCH: x $(touch /tmp/pwned)")
		if err == nil || !strings.Contains(err.Error(), "not Claude") {
			t.Errorf("%s: %v", name, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Errorf("%s: cmux typed into the tab", name)
		}
	}
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "tree.json"), []byte(treeJSON(map[string][3]string{"workspace:1000020000": {"surface:1000020029", "crius", ""}})), 0o644)
	if err := send("WAKEWATCH: x"); err == nil || !strings.Contains(err.Error(), "no tty") {
		t.Errorf("tab with no tty: %v", err)
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Error("tab with no tty: cmux typed")
	}
}

// stored gives the texts in the outbox, oldest first.
func stored(t *testing.T) []string {
	paths, _ := filepath.Glob(filepath.Join(os.Getenv("CRIUS_HOME"), "outbox", "*.json"))
	var out []string
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m outbox.Message
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m.Text)
	}
	return out
}

// hold puts a message for Crius's tab in the outbox, older than any send makes.
func hold(t *testing.T, text string) {
	dir := filepath.Join(os.Getenv("CRIUS_HOME"), "outbox")
	b, _ := json.Marshal(outbox.Message{To: wakewatch.CriusTitle, Text: text, Created: time.Now()})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000001-test.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Review of 91854cf, P2: a line typed but not sent (Enter failed) blocks every later cmux send (it refuses over a
// draft). Our own line left in the prompt gets Enter first. Review of 4a0333e, P2: only when the whole draft is a line
// wakewatch stored (now in the outbox); a draft the owner edited or wrote is left as it is, and the alert is held.
func TestSendFinishesItsOwnStuckLine(t *testing.T) {
	enter := "[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n[send]"
	older := "WAKEWATCH: quotes.log: an older alert about the DONE line of quotes:lead"
	for name, c := range map[string]struct{ stored, draft, want string }{
		"ours":               {older, older, enter},
		"ours, wrapped":      {older, "WAKEWATCH: quotes.log: an older alert\n  about the DONE line of quotes:lead", enter},
		"owner added a line": {older, older + "\n  Before doing anything, wait for my", ""},
		"owner changed it":   {older, "WAKEWATCH: quotes.log: an older alert", ""},
		"nothing stored":     {"", older, ""},
		"owner is typing":    {older, "owner is typing here", ""},
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "draft"), []byte(c.draft), 0o644)
		if c.stored != "" {
			hold(t, c.stored)
		}
		err := send("WAKEWATCH: new")
		b, _ := os.ReadFile(log)
		if c.want == "" { // a draft that is not ours: kept, no Enter, and loud
			got, _ := os.ReadFile(filepath.Join(dir, "draft"))
			if string(got) != c.draft || strings.Contains(string(b), "[send-key]") || err == nil || !strings.Contains(err.Error(), "draft") {
				t.Errorf("%s: err %v, draft %q, calls\n%s", name, err, got, b)
			}
		} else if !strings.HasPrefix(string(b), c.want) || err != nil {
			t.Errorf("%s: err %v, calls\n%s", name, err, b)
		}
	}
}

// A line stays stored while its Enter has not gone through, and is gone once it has.
func TestSendKeepsItsLineUntilEnterWorks(t *testing.T) {
	_, dir := fakeTab(t)
	if err := send("WAKEWATCH: one"); err != nil {
		t.Fatal(err)
	}
	if got := stored(t); len(got) != 0 {
		t.Fatalf("stored after a good send: %q", got)
	}
	os.WriteFile(filepath.Join(dir, "enterfail"), nil, 0o644)
	if err := send("WAKEWATCH: two"); err == nil {
		t.Fatal("a failed Enter gave no error")
	}
	if got := stored(t); fmt.Sprint(got) != "[WAKEWATCH: two]" {
		t.Fatalf("stored %q", got)
	}
}

// Review of 4a0333e, P1: cmux prints tab titles as they are, so a title can look like another surface's row. The tab's
// terminal comes from the JSON tree; a decoy title on a Claude tab must not pass the check for Crius's tab running a
// shell.
func TestADecoyTitleDoesNotPassTheClaudeCheck(t *testing.T) {
	for name, tree := range map[string]map[string][3]string{
		"decoy title": {"workspace:1000020000": {"surface:1000020029", "crius", "ttys015"},
			"workspace:1000020001": {"surface:1000020030", "surface surface:1000020029 decoy", "ttys008"}},
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "front"), []byte("6832 6832 -/bin/zsh\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "front-ttys008"), []byte("7 7 /Users/o/.local/bin/claude\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "tree.json"), []byte(treeJSON(tree)), 0o644)
		if err := send("WAKEWATCH: x $(touch /tmp/pwned)"); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Errorf("%s: cmux typed into the tab", name)
		}
	}
}

// Brief P7 item 4: each tool is asked once; a failure says which tool and why, a hang says it did not answer.
func TestToolsSaysWhichToolFailsOrHangs(t *testing.T) {
	_, dir := fakeTab(t)
	for name, body := range map[string]string{
		"gh": "echo 'You are not logged into any GitHub hosts' >&2; exit 1",
		"az": "echo 0f7b", "docker": "echo 29.1.0",
		"bun": "sleep 60",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	real, retry := toolWait, toolRetry
	t.Cleanup(func() { toolWait, toolRetry = real, retry })
	toolWait = 2 * time.Second // the quick fakes answer well inside it even on a loaded machine
	toolRetry = 100 * time.Millisecond
	start := time.Now()
	res := tools()
	if len(res) != 5 || res["cmux"] != nil || res["az"] != nil || res["docker"] != nil {
		t.Fatalf("%v", res)
	}
	if res["gh"] == nil || !strings.Contains(res["gh"].Error(), "not logged into any GitHub hosts") {
		t.Errorf("gh: %v", res["gh"])
	}
	if res["bun"] == nil || !strings.Contains(res["bun"].Error(), "no answer") {
		t.Errorf("bun: %v", res["bun"])
	}
	if d := time.Since(start); d > 20*time.Second { // far below the hung tool's 60 s: it was stopped, not waited for
		t.Errorf("a hung tool held the check for %v", d)
	}
}

// Crius 10-23 22:36: az and gh missed the 10 s once under load 100-180 on 14 cores (22:20, 22:35) and answered in a
// second on a recheck. A tool that misses is asked again after toolRetry; only a second miss is a failure, and it names
// both misses and the 1-minute load.
func TestToolsFailOnlyOnASecondMissAndSayTheLoad(t *testing.T) {
	_, dir := fakeTab(t)
	once := filepath.Join(dir, "gh-missed")
	for name, body := range map[string]string{
		"gh":  "[ -f " + once + " ] && exit 0; : > " + once + "; echo 'connection reset' >&2; exit 1",
		"az":  "echo 'Please run az login' >&2; exit 1",
		"bun": "echo 1.3.0", "docker": "echo 29.1.0",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	wait, retry := toolWait, toolRetry
	t.Cleanup(func() { toolWait, toolRetry = wait, retry })
	toolWait, toolRetry = 5*time.Second, 300*time.Millisecond
	res := tools()
	if res["gh"] != nil {
		t.Errorf("gh missed once and answered on the recheck: want no failure, got %v", res["gh"])
	}
	if _, err := os.Stat(once); err != nil {
		t.Errorf("gh was not asked a first time: %v", err)
	}
	az := fmt.Sprint(res["az"])
	if res["az"] == nil || strings.Count(az, "Please run az login") != 2 || !strings.Contains(az, "1-min load ") {
		t.Errorf("az missed twice: want both misses and the 1-minute load, got %v", res["az"])
	}
}

// The fresh-start check reads every config folder's live sessions; a linked folder (~/.claude-personal) lists the
// same process twice, a dead pid's file is left over, and a session with no transcript yet has no start.
func TestSessionsReadsLiveSessionsWithTheirStart(t *testing.T) {
	home := t.TempDir()
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	me, parent := os.Getpid(), os.Getppid()
	write(filepath.Join(home, ".claude", "sessions", fmt.Sprint(me)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"aaa","name":"quotes: lead","status":"busy","procStart":%q}`, me, testutil.ProcStart(t, me)))
	write(filepath.Join(home, ".claude", "sessions", "99999999.json"), `{"pid":99999999,"sessionId":"dead","name":"axi: lead"}`)
	write(filepath.Join(home, ".claude", "projects", "-Users-o-x", "aaa.jsonl"), "{}\n")
	write(filepath.Join(home, ".claude-work", "sessions", fmt.Sprint(parent)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"bbb","name":"cortex · lead","procStart":%q}`, parent, testutil.ProcStart(t, parent)))
	if err := os.Symlink(filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")); err != nil {
		t.Fatal(err)
	}
	ss, err := wakewatch.LiveSessions(home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]wakewatch.Session{}
	for _, s := range ss {
		got[s.ID] = s
	}
	if len(ss) != 2 || got["aaa"].Name != "quotes: lead" || got["bbb"].Name != "cortex · lead" {
		t.Fatalf("%+v", ss)
	}
	if d := time.Since(got["aaa"].Started); d < 0 || d > time.Minute {
		t.Errorf("aaa started %v", got["aaa"].Started)
	}
	if !got["bbb"].Started.IsZero() {
		t.Errorf("bbb has no transcript: %v", got["bbb"].Started)
	}
	// Review of 078f69a, P3: pid 0 (kill(0, 0) signals our own group) and a file gone between the listing and the
	// read (a session ending) are not live sessions, and not a failure to look.
	write(filepath.Join(home, ".claude", "sessions", "0.json"), `{"pid":0,"sessionId":"zero","name":"axi: lead"}`)
	os.Symlink(filepath.Join(home, "gone.json"), filepath.Join(home, ".claude", "sessions", "2.json"))
	if ss, err := wakewatch.LiveSessions(home); err != nil || len(ss) != 2 {
		t.Errorf("pid 0 or a vanished file: %v %+v", err, ss)
	}
	write(filepath.Join(home, ".claude", "sessions", "1.json"), `{"pid":`)
	if _, err := wakewatch.LiveSessions(home); err == nil || !strings.Contains(err.Error(), "1.json") {
		t.Errorf("a broken session file: %v", err)
	}
}

// Review of ea0867e, P2: Claude being in front is not enough. Without its input box on screen (bash mode after "!",
// a dialog, a picker) Enter runs or picks something, so nothing is typed, and that is loud.
func TestSendTypesOnlyIntoClaudeInputBox(t *testing.T) {
	for name, screen := range map[string]string{
		"bash mode": "⏺ done.\n──────────────\n! \n──────────────\n  ! for bash mode\n",
		"dialog":    "Do you want to proceed?\n❯ 1. Yes\n  2. No\n",
		"no screen": "",
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "screen"), []byte(screen), 0o644)
		err := send("WAKEWATCH: x $(touch /tmp/pwned)")
		if err == nil || !strings.Contains(err.Error(), "input box") {
			t.Errorf("%s: %v", name, err)
		}
		if b, _ := os.ReadFile(log); len(b) != 0 {
			t.Errorf("%s: cmux typed:\n%s", name, b)
		}
	}
}

// Review of ea0867e, P2: if someone types while wakewatch types, Enter would send the mix. Enter goes only when the
// box holds exactly the alert; otherwise it is loud. Review 10-22: the alert is already in the box, so it is never typed
// again onto the same words. Re-review 10-22: it is set aside by the next run, which posts it to the bus.
func TestSendPressesEnterOnlyOnItsOwnText(t *testing.T) {
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "extra"), []byte(" and the owner's words"), 0o644)
	err := send("WAKEWATCH: x")
	if err == nil || !strings.Contains(err.Error(), "typed onto other text") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "[send-key]") {
		t.Fatalf("Enter pressed on a mixed draft:\n%s", b)
	}
	home := os.Getenv("CRIUS_HOME")
	os.MkdirAll(filepath.Join(home, "bus"), 0o700)
	os.Remove(filepath.Join(dir, "extra"))
	flushOutbox(home, time.Now())
	if b, _ := os.ReadFile(log); strings.Count(string(b), "WAKEWATCH: x") != 1 {
		t.Fatalf("the alert was typed again:\n%s", b)
	}
	if got := stored(t); len(got) != 0 {
		t.Fatalf("stored %q, want it set aside", got)
	}
	if kept, _ := filepath.Glob(filepath.Join(home, "outbox", "dropped", "*.json")); len(kept) != 1 {
		t.Fatalf("dropped/ holds %q", kept)
	}
	if bus, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log")); !strings.Contains(string(bus), "WAKEWATCH: x") {
		t.Errorf("bus:\n%s\nwant the alert typed onto other text named", bus)
	}
}

// Review of ea0867e, P3: a warning cmux prints on stderr must not break the JSON tree, or every wake fails.
func TestAWarningOnStderrKeepsTheTree(t *testing.T) {
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "warn"), []byte("warning: a newer cmux is available\n"), 0o644)
	if err := send("WAKEWATCH: x"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "[send-key]") {
		t.Fatalf("calls:\n%s", b)
	}
}

// Review of c9df15f, P2: Claude draws a key's effect a moment after it arrives. A read right after typing or Enter
// sees the old box, so send waits for the box to catch up instead of failing (two alerts in one run, a stuck line).
func TestSendWaitsForTheBoxToCatchUp(t *testing.T) {
	_, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "lag"), nil, 0o644)
	for _, text := range []string{"WAKEWATCH: one", "WAKEWATCH: two"} {
		if err := send(text); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
	for _, f := range []string{"draft", "shown"} { // on screen for a while: no lag left
		os.WriteFile(filepath.Join(dir, f), []byte("WAKEWATCH: stuck"), 0o644)
	}
	hold(t, "WAKEWATCH: stuck")
	if err := send("WAKEWATCH: three"); err != nil {
		t.Fatalf("after a stuck line: %v", err)
	}
}

// Live 10-22 01:29: Crius was mid-turn, so Enter queued the alert and the empty box showed Claude's hint "Press up to
// edit queued messages". Queued is delivered: the hint is an empty box, after Enter and on the first read.
func TestSendTakesTheQueuedHintAsAnEmptyBox(t *testing.T) {
	_, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "queued"), nil, 0o644)
	for _, text := range []string{"WAKEWATCH: one", "WAKEWATCH: two"} {
		if err := send(text); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
	}
}

// Crius ORDER 10-22 02:10: crius-tab went stale on Crius's handover (it named surface 029, Crius was on 061). The tab
// is the one terminal titled "crius" in the cmux tree, the title waiting() already skips as Crius's own. None, or
// several (a handover not finished), is loud and types nothing: the alert is sent again on the next run.
func TestSendFindsCriusTabByItsTitle(t *testing.T) {
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "tree.json"), []byte(treeJSON(map[string][3]string{
		"workspace:1000020000": {"surface:1000020061", "crius", "ttys015"},
		"workspace:1000020001": {"surface:1000020003", "quotes", "ttys008"}})), 0o644)
	if err := send("WAKEWATCH: x"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.HasPrefix(string(b), "[send][--workspace][workspace:1000020000][--surface][surface:1000020061]") {
		t.Fatalf("calls\n%s", b)
	}
	for name, tree := range map[string]map[string][3]string{
		"none":    {"workspace:1000020001": {"surface:1000020003", "quotes", "ttys008"}},
		"builder": {"workspace:1000020000": {"surface:1000020041", "crius · build", "ttys008"}},
		"spaced":  {"workspace:1000020000": {"surface:1000020061", " crius", "ttys008"}},
		"several": {"workspace:1000020000": {"surface:1000020029", "crius", "ttys015"}, "workspace:1000020001": {"surface:1000020061", "crius", "ttys008"}},
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "front-ttys008"), []byte("7 7 /Users/o/.local/bin/claude\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "tree.json"), []byte(treeJSON(tree)), 0o644)
		if err := send("WAKEWATCH: x"); err == nil || !strings.Contains(err.Error(), `titled "crius"`) {
			t.Errorf("%s: %v", name, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Errorf("%s: cmux typed into a tab", name)
		}
	}
}

// S2 (10-22 11:37-12:42): eight refusals said only "holds a draft", so nobody could tell what was in Crius's box
// ("Quote report"). The error names the draft's start; the rest of a long draft stays out of the bus.
func TestSendSaysWhatTheDraftIs(t *testing.T) {
	for draft, want := range map[string]string{
		"Quote report":                    `"Quote report"`,
		strings.Repeat("long draft ", 20): `"` + strings.Repeat("long draft ", 5)[:50] + `…"`,
	} {
		_, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "draft"), []byte(draft), 0o644)
		if err := send("WAKEWATCH: new"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err %v, want it to name %s", err, want)
		}
	}
	// Review of e871917, P2: the error goes to wakewatch.log and the bus, so the draft is redacted before the cut; a
	// token that crosses rune 50 would be too short for the redactor after it. Built here so no scanner flags the file.
	token := "ghp_" + strings.Repeat("a1B2c3D4e", 4)
	_, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "draft"), []byte("please push the fix to main with "+token+" and then"), 0o644)
	if err := send("WAKEWATCH: new"); err == nil || strings.Contains(err.Error(), "ghp_a1B") || !strings.Contains(err.Error(), "please push") {
		t.Errorf("err %v, want the draft without any part of the token", err)
	}
	// Review of the fix, P2 + P3: the box wraps a long line, so a token can reach Redact as two short pieces; and every
	// error that quotes the box (here: the owner typed while wakewatch typed) shows it the same safe way.
	for name, put := range map[string][2]string{
		"wrapped": {"draft", "push with " + token[:17] + "\n" + token[17:] + " now"},
		"typed":   {"extra", " " + token},
		// Re-review P1: a hit on the box text as shown must not let a wrapped secret next to it through.
		"two":      {"draft", "use " + token + "\n" + token[:17] + "\n" + token[17:] + " ok"},
		"password": {"draft", "password=hunt\ner2 is it"},
		// A wrap ate the space before it, so the glued text has no word break; low entropy, so only the ghp rule can see it.
		"spaced": {"draft", "use\nghp_" + strings.Repeat("B", 13) + "\n" + strings.Repeat("B", 23) + " ok"},
	} {
		_, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, put[0]), []byte(put[1]), 0o644)
		err := send("WAKEWATCH: new")
		if err == nil || strings.Contains(err.Error(), "ghp_a1B") || strings.Contains(err.Error(), "c3D4ea1B2c3D4e") || strings.Contains(err.Error(), "er2") || strings.Contains(err.Error(), "BBB") {
			t.Errorf("%s: err %v, want no part of the token", name, err)
		}
	}
}

// ORDER 10-22 13:36: the name and colour check reads every window's workspaces; a window it cannot read fails loudly.
func TestWorkspacesAreReadFromEveryWindow(t *testing.T) {
	real := outbox.Cmux
	t.Cleanup(func() { outbox.Cmux = real })
	fail := false
	outbox.Cmux = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "--json tree --all":
			return []byte(`{"windows":[{"ref":"window:1","workspaces":[]},{"ref":"window:2","workspaces":[]}]}`), nil
		case "workspace list --window window:1 --json":
			return []byte(`{"workspaces":[{"ref":"workspace:1","title":"Crius","has_custom_title":true,"custom_color":"#6A1B9A"},` +
				`{"ref":"workspace:5","title":"~/Projects/openrig","has_custom_title":false,"custom_color":null}]}`), nil
		case "workspace list --window window:2 --json":
			if fail {
				return []byte("no such window"), errors.New("exit status 1")
			}
			return []byte(`{"workspaces":[{"ref":"workspace:6","title":"Tamada","has_custom_title":true}]}`), nil
		}
		return nil, fmt.Errorf("unexpected cmux %q", args)
	}
	got, err := workspaces()
	want := []wakewatch.Workspace{{Ref: "workspace:1", Title: "Crius", Named: true, Color: "#6A1B9A"},
		{Ref: "workspace:5", Title: "~/Projects/openrig"}, {Ref: "workspace:6", Title: "Tamada", Named: true}}
	if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%+v %v", got, err)
	}
	fail = true
	if _, err := workspaces(); err == nil || !strings.Contains(err.Error(), "window:2") || !strings.Contains(err.Error(), "no such window") {
		t.Fatal(err)
	}
}

// OpenRig parts item 3: a tab's screen carries cmux's surface id, which a session in it has as CMUX_SURFACE_ID and
// crius-hook writes in its state file. cmux --json tree prints ids only with --id-format both.
func TestScreensCarryTheSurfaceIDTheHookSees(t *testing.T) {
	real := outbox.Cmux
	t.Cleanup(func() { outbox.Cmux = real })
	outbox.Cmux = func(args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "--json --id-format both tree --all":
			return []byte(`{"windows":[{"ref":"window:1","workspaces":[{"ref":"workspace:1","panes":[{"surfaces":[` +
				`{"id":"6F1C0A2E-surface","ref":"surface:1","title":"quotes · p3","tty":"ttys001","type":"terminal"}]}]}]}]}`), nil
		case "--json tree --all":
			return []byte(treeJSON(map[string][3]string{"workspace:1": {"surface:1", "quotes · p3", "ttys001"}})), nil
		}
		return []byte("ok\n"), nil
	}
	got, err := screens()
	if err != nil || len(got) != 1 || got[0].ID != "6F1C0A2E-surface" || got[0].Surface != "surface:1" {
		t.Fatalf("%+v %v", got, err)
	}
}

// OpenRig parts item 4, today's real cases: grey text in Crius's box is not a draft. 11:37-12:42 the box showed a grey
// prompt suggestion ("Quote report") and eight wakes were refused; 16:05 a fresh box showed the grey placeholder
// 'Try "refactor <filepath>"'. cmux send (no --force) reads the text style and types over faint text, so the wake goes in.
func TestSendTypesOverGreyText(t *testing.T) {
	for _, ghost := range []string{"Quote report", `Try "refactor <filepath>"`} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "ghost"), []byte(ghost), 0o644)
		if err := send("WAKEWATCH: x"); err != nil {
			t.Errorf("%s: %v", ghost, err)
		}
		if b, _ := os.ReadFile(log); !strings.Contains(string(b), "[send-key]") {
			t.Errorf("%s: no Enter:\n%s", ghost, b)
		}
	}
}

// Item 4: text the owner typed is held, not typed over. The message is stored (one file per message, once) and goes in
// on a later run once the box is empty, before the newer one.
func TestSendHoldsAMessageUntilTheBoxIsEmpty(t *testing.T) {
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "draft"), []byte("owner is typing"), 0o644)
	for i := 0; i < 2; i++ {
		if err := send("WAKEWATCH: first"); err == nil || !strings.Contains(err.Error(), "held") {
			t.Fatalf("over a draft: %v", err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "draft")); string(b) != "owner is typing" {
		t.Fatalf("the owner's draft became %q", b)
	}
	stored, _ := filepath.Glob(filepath.Join(os.Getenv("CRIUS_HOME"), "outbox", "*.json"))
	if len(stored) != 1 {
		t.Fatalf("stored %d messages, want 1: %v", len(stored), stored)
	}
	os.WriteFile(filepath.Join(dir, "draft"), nil, 0o644) // the owner sent their line
	os.Remove(log)
	if err := send("WAKEWATCH: second"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	first, second := strings.Index(string(b), "[WAKEWATCH: first]"), strings.Index(string(b), "[WAKEWATCH: second]")
	if first < 0 || second < first {
		t.Fatalf("want first then second typed:\n%s", b)
	}
	if left, _ := filepath.Glob(filepath.Join(os.Getenv("CRIUS_HOME"), "outbox", "*.json")); len(left) != 0 {
		t.Fatalf("still stored after delivery: %v", left)
	}
}

// Re-review 10-22: a crius-say message for a lead's tab that aged out or was damaged went only to wakewatch.log, so it
// was lost with nobody told. What the outbox sets aside goes to Crius's bus; what it still holds does not (every run
// would post it again).
func TestFlushPostsWhatTheOutboxSetsAside(t *testing.T) {
	_, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "draft"), []byte("owner is typing"), 0o644)
	home := os.Getenv("CRIUS_HOME")
	os.MkdirAll(filepath.Join(home, "outbox"), 0o700)
	os.MkdirAll(filepath.Join(home, "bus"), 0o700)
	old, _ := json.Marshal(outbox.Message{To: "quotes · lead", Text: "merge it", Created: time.Now().Add(-2 * outbox.MaxAge)})
	os.WriteFile(filepath.Join(home, "outbox", "00000000000000000001-old.json"), old, 0o600)
	hold(t, "WAKEWATCH: held")
	if err := flushOutbox(home, time.Now()); err == nil || !strings.Contains(err.Error(), "held") {
		t.Errorf("flushOutbox: %v, want the held message named", err)
	}
	bus, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if !strings.Contains(string(bus), "merge it") || strings.Contains(string(bus), "WAKEWATCH: held") {
		t.Errorf("bus:\n%s\nwant the set-aside message and not the held one", bus)
	}
}

// OF8 (round 4): when one post to the bus failed, flushOutbox returned and the other set-aside messages were named
// nowhere, though they had already left the outbox. Each one not posted is named in the error, which goes to the log.
func TestFlushNamesEverySetAsideMessageTheBusMissed(t *testing.T) {
	fakeTab(t)
	home := os.Getenv("CRIUS_HOME")
	os.MkdirAll(filepath.Join(home, "outbox"), 0o700)
	os.MkdirAll(filepath.Join(home, "bus", "crius.log"), 0o700) // a folder: every post fails
	for i, text := range []string{"merge it", "ship it"} {
		old, _ := json.Marshal(outbox.Message{To: "quotes · lead", Text: text, Created: time.Now().Add(-2 * outbox.MaxAge)})
		os.WriteFile(filepath.Join(home, "outbox", fmt.Sprintf("0000000000000000000%d-old.json", i+1)), old, 0o600)
	}
	err := flushOutbox(home, time.Now())
	if err == nil || !strings.Contains(err.Error(), "merge it") || !strings.Contains(err.Error(), "ship it") {
		t.Errorf("flushOutbox: %v, want both set-aside messages named", err)
	}
}

// OF8 review: the post to the bus is redacted, but a message it missed went to the log as the outbox named it: the
// text it was typed onto, unredacted.
func TestFlushRedactsWhatTheBusMissed(t *testing.T) {
	fakeTab(t)
	home := os.Getenv("CRIUS_HOME")
	os.MkdirAll(filepath.Join(home, "outbox"), 0o700)
	os.MkdirAll(filepath.Join(home, "bus", "crius.log"), 0o700)
	onto, _ := json.Marshal(outbox.Message{To: "crius", Text: "merge it", Created: time.Now(), Onto: "password=hunter22x"})
	os.WriteFile(filepath.Join(home, "outbox", "00000000000000000001-onto.json"), onto, 0o600)
	err := flushOutbox(home, time.Now())
	if err == nil || !strings.Contains(err.Error(), "merge it") || strings.Contains(err.Error(), "hunter22x") {
		t.Errorf("flushOutbox: %v, want the message named and the password gone", err)
	}
}

// OF7: a status file left by a process that ended, whose pid now belongs to a later process, is not a live session:
// Claude's own start time in the file differs from the live process's.
func TestSessionsSkipsAStatusFileWhosePidWasReused(t *testing.T) {
	home, me := t.TempDir(), os.Getpid()
	testutil.Write(t, filepath.Join(home, ".claude", "sessions", "1.json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"old","name":"quotes · p1","procStart":"Thu Jan  1 00:00:00 2026"}`, me))
	testutil.Write(t, filepath.Join(home, ".claude-work", "sessions", "2.json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"now","name":"quotes · p2","procStart":%q}`, me, testutil.ProcStart(t, me)))
	ss, err := wakewatch.LiveSessions(home)
	if err != nil || len(ss) != 1 || ss[0].ID != "now" {
		t.Errorf("%v %+v", err, ss)
	}
}
