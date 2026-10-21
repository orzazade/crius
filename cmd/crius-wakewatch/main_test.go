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

// fakeCmux puts a cmux on PATH that records each call, one per line, and gives Crius's tab in a config file.
func fakeCmux(t *testing.T, tab string) (log string) {
	log, _ = fakeTab(t, tab)
	return log
}

// fakeTab puts a fake cmux and ps on PATH. cmux logs every call but its reads (tree, read-screen) and acts like the
// real one: the JSON tree (dir/tree.json) shows Crius's surface on ttys015; read-screen prints dir/screen if there is
// one, else a Claude input box holding dir/draft (one read late if dir/lag exists, as Claude redraws after a key); send refuses over a draft, else types into it (plus dir/extra, if
// any: someone typing at the same time); send-key enter sends the draft (fails if dir/enterfail exists). ps on
// ttys015 prints dir/front (Claude in front) and on another tty dir/front-<tty>.
func fakeTab(t *testing.T, tab string) (log, dir string) {
	dir = t.TempDir()
	log = filepath.Join(dir, "calls")
	f := func(name string) string { return filepath.Join(dir, name) }
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"--json) cat " + f("warn") + " >&2 2>/dev/null; cat " + f("tree.json") + "; exit 0 ;;\n" +
		"read-screen) if [ -f " + f("screen") + " ]; then cat " + f("screen") + "; exit 0; fi\n  src=" + f("draft") +
		"; [ -f " + f("lag") + " ] && [ -f " + f("shown") + " ] && src=" + f("shown") + "\n" +
		"  printf '⏺ done.\\n──────────────\\n❯ %s\\n──────────────\\n  quota: 7d 97%%\\n' \"$(cat $src)\"\n" +
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
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	if tab != "" {
		if err := os.WriteFile(filepath.Join(home, "crius-tab"), []byte(tab), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
	log := fakeCmux(t, "# Crius's tab\nworkspace:1000020000 surface:1000020029\n")
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

// With no tab named, nothing is typed anywhere (a bare cmux send would go to the caller's own tab) and it is loud.
func TestSendWithNoCriusTabFailsLoudly(t *testing.T) {
	for _, tab := range []string{"", "surface:1000020029\n", "workspace:1 surface:2 extra\n", "workspace:x surface:2\n"} {
		log := fakeCmux(t, tab)
		err := send("WAKEWATCH: x")
		if err == nil || !strings.Contains(err.Error(), "crius-tab") {
			t.Fatalf("%q: %v", tab, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Fatalf("%q: cmux was called", tab)
		}
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
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
		os.WriteFile(filepath.Join(dir, "front"), []byte(ps), 0o644)
		err := send("WAKEWATCH: x $(touch /tmp/pwned)")
		if err == nil || !strings.Contains(err.Error(), "not Claude") {
			t.Errorf("%s: %v", name, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Errorf("%s: cmux typed into the tab", name)
		}
	}
	log, _ := fakeTab(t, "workspace:1000020000 surface:1000020077\n")
	if err := send("WAKEWATCH: x"); err == nil || !strings.Contains(err.Error(), "surface:1000020077") {
		t.Errorf("surface not in the tree: %v", err)
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Error("surface not in the tree: cmux typed")
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
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
	_, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
// terminal comes from the JSON tree, by workspace and surface; a decoy title on a Claude tab must not pass the check
// for Crius's tab running a shell.
func TestSendFindsCriusTerminalByRefNotByText(t *testing.T) {
	for name, tree := range map[string]map[string][3]string{
		"decoy title": {"workspace:1000020000": {"surface:1000020029", "crius", "ttys015"},
			"workspace:1000020001": {"surface:1000020030", "surface surface:1000020029 decoy", "ttys008"}},
		"other workspace": {"workspace:1000020001": {"surface:1000020029", "crius", "ttys008"}},
	} {
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
	_, dir := fakeTab(t, "")
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
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
	log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
	log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
	_, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
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
