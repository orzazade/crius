package main

import (
	"errors"
	"fmt"
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
// real one: the JSON tree (dir/tree.json) shows Crius's tab, titled "crius", on ttys015; read-screen prints dir/screen if there is
// one, else a Claude input box holding dir/draft (one read late if dir/lag exists, as Claude redraws after a key;
// an empty box shows Claude's queued-messages hint if dir/queued exists); send refuses over a draft, else types into it (plus dir/extra, if
// any: someone typing at the same time); send-key enter sends the draft (fails if dir/enterfail exists). ps on
// ttys015 prints dir/front (Claude in front) and on another tty dir/front-<tty>.
func fakeTab(t *testing.T) (log, dir string) {
	dir = t.TempDir()
	log = filepath.Join(dir, "calls")
	f := func(name string) string { return filepath.Join(dir, name) }
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"--json) cat " + f("warn") + " >&2 2>/dev/null; cat " + f("tree.json") + "; exit 0 ;;\n" +
		"read-screen) if [ -f " + f("screen") + " ]; then cat " + f("screen") + "; exit 0; fi\n  src=" + f("draft") +
		"; [ -f " + f("lag") + " ] && [ -f " + f("shown") + " ] && src=" + f("shown") + "\n" +
		"  box=$(cat $src); [ -z \"$box\" ] && [ -f " + f("queued") + " ] && box='Press up to edit queued messages'\n" +
		"  printf '⏺ done.\\n──────────────\\n❯ %s\\n──────────────\\n  quota: 7d 97%%\\n' \"$box\"\n" +
		"  [ -f " + f("lag") + " ] && cp " + f("draft") + " " + f("shown") + "; exit 0 ;;\nesac\n" +
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
		out = append(out, fmt.Sprintf(`{"ref":%q,"panes":[{"ref":"pane:1","surfaces":[{"ref":%q,"title":%q,"tty":%q,"type":"terminal"}]}]}`,
			w, s[0], s[1], s[2]))
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
	real := cmux
	t.Cleanup(func() { cmux = real })
	cmux = func(args ...string) ([]byte, error) {
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

// Review of 91854cf, P2: a line typed but not sent (Enter failed) blocks every later cmux send (it refuses over a
// draft). Our own line left in the prompt gets Enter first. Review of 4a0333e, P2: only when the whole draft is the
// line wakewatch typed (kept in state/wakewatch-pending); a draft the owner edited or wrote is left alone.
func TestSendFinishesItsOwnStuckLine(t *testing.T) {
	enter := "[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n[send]"
	older := "WAKEWATCH: quotes.log: an older alert about the DONE line of quotes:lead"
	for name, c := range map[string]struct{ pending, draft, want string }{
		"ours":               {older, older, enter},
		"ours, wrapped":      {older, "WAKEWATCH: quotes.log: an older alert\n  about the DONE line of quotes:lead", enter},
		"owner added a line": {older, older + "\n  Before doing anything, wait for my", ""},
		"owner changed it":   {older, "WAKEWATCH: quotes.log: an older alert", ""},
		"no pending line":    {"", older, ""},
		"owner is typing":    {older, "owner is typing here", ""},
	} {
		log, dir := fakeTab(t)
		os.WriteFile(filepath.Join(dir, "draft"), []byte(c.draft), 0o644)
		if c.pending != "" {
			os.MkdirAll(filepath.Join(os.Getenv("CRIUS_HOME"), "state"), 0o700)
			os.WriteFile(filepath.Join(os.Getenv("CRIUS_HOME"), "state", "wakewatch-pending"), []byte(c.pending), 0o600)
		}
		err := send("WAKEWATCH: new")
		b, _ := os.ReadFile(log)
		if c.want == "" { // a draft that is not ours: nothing typed, and loud
			if len(b) != 0 || err == nil || !strings.Contains(err.Error(), "draft") {
				t.Errorf("%s: err %v, calls\n%s", name, err, b)
			}
		} else if !strings.HasPrefix(string(b), c.want) || err != nil {
			t.Errorf("%s: err %v, calls\n%s", name, err, b)
		}
	}
}

// The pending line stays while its Enter has not gone through, and is gone once it has.
func TestSendKeepsItsLineUntilEnterWorks(t *testing.T) {
	_, dir := fakeTab(t)
	pending := filepath.Join(os.Getenv("CRIUS_HOME"), "state", "wakewatch-pending")
	if err := send("WAKEWATCH: one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending after a good send: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "enterfail"), nil, 0o644)
	if err := send("WAKEWATCH: two"); err == nil {
		t.Fatal("a failed Enter gave no error")
	}
	if got, _ := os.ReadFile(pending); string(got) != "WAKEWATCH: two" {
		t.Fatalf("pending %q", got)
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
	real := toolWait
	t.Cleanup(func() { toolWait = real })
	toolWait = 2 * time.Second // the quick fakes answer well inside it even on a loaded machine
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
		fmt.Sprintf(`{"pid":%d,"sessionId":"aaa","name":"quotes: lead","status":"busy"}`, me))
	write(filepath.Join(home, ".claude", "sessions", "99999999.json"), `{"pid":99999999,"sessionId":"dead","name":"axi: lead"}`)
	write(filepath.Join(home, ".claude", "projects", "-Users-o-x", "aaa.jsonl"), "{}\n")
	write(filepath.Join(home, ".claude-work", "sessions", fmt.Sprint(parent)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"bbb","name":"cortex · lead"}`, parent))
	if err := os.Symlink(filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")); err != nil {
		t.Fatal(err)
	}
	ss, err := sessions(home)
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
	if ss, err := sessions(home); err != nil || len(ss) != 2 {
		t.Errorf("pid 0 or a vanished file: %v %+v", err, ss)
	}
	write(filepath.Join(home, ".claude", "sessions", "1.json"), `{"pid":`)
	if _, err := sessions(home); err == nil || !strings.Contains(err.Error(), "1.json") {
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
// box holds exactly the alert; otherwise it is loud and the line stays pending.
func TestSendPressesEnterOnlyOnItsOwnText(t *testing.T) {
	log, dir := fakeTab(t)
	os.WriteFile(filepath.Join(dir, "extra"), []byte(" and the owner's words"), 0o644)
	err := send("WAKEWATCH: x")
	if err == nil || !strings.Contains(err.Error(), "Enter not pressed") {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), "[send-key]") {
		t.Fatalf("Enter pressed on a mixed draft:\n%s", b)
	}
	if got, _ := os.ReadFile(filepath.Join(os.Getenv("CRIUS_HOME"), "state", "wakewatch-pending")); string(got) != "WAKEWATCH: x" {
		t.Fatalf("pending %q", got)
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
	os.MkdirAll(filepath.Join(os.Getenv("CRIUS_HOME"), "state"), 0o700)
	os.WriteFile(filepath.Join(os.Getenv("CRIUS_HOME"), "state", "wakewatch-pending"), []byte("WAKEWATCH: stuck"), 0o600)
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
	real := cmux
	t.Cleanup(func() { cmux = real })
	fail := false
	cmux = func(args ...string) ([]byte, error) {
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
	real := cmux
	t.Cleanup(func() { cmux = real })
	cmux = func(args ...string) ([]byte, error) {
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
