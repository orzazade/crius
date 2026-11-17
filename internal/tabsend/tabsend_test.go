package tabsend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/shell"

	"github.com/orzazade/crius/internal/tabsend/fakeprompt"
	"github.com/orzazade/crius/internal/wakewatch"
)

func init() { Wait = 0 }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const wake = "Wake up: QA posted a green build for the renewal page."

func TestBoxReadsTypedTextNotTheDimPlaceholder(t *testing.T) {
	for _, c := range []struct {
		file, typed string
		boxed       bool
	}{
		{"placeholder.txt", "", true}, // grey suggestion in an empty box: no typed text
		{"empty.txt", "", true},       // a survey above the box does not count
		{"typed.txt", wake, true},
		{"leftover-multiline-busy.txt", wake + "\nCheck it against the brief and post VERIFIED or a\nlinked fix item.", true},
		{"busy-queued.txt", "", true}, // the queued message sits above the box, the hint in it is dim
		{"dialog.txt", "", false},     // a permission dialog: Enter would pick an option
	} {
		typed, boxed := Box(fixture(t, c.file))
		if typed != c.typed || boxed != c.boxed {
			t.Errorf("%s: Box = %q, %v; want %q, %v", c.file, typed, boxed, c.typed, c.boxed)
		}
	}
}

func TestBoxIgnoresTrueColourParamsThatLookLikeDim(t *testing.T) {
	screen := "\x1b[38;5;244m" + strings.Repeat("─", 20) + "\n❯ \x1b[38;2;2;2;2mhello\x1b[0m\n" + strings.Repeat("─", 20) + "\n"
	if typed, _ := Box(screen); typed != "hello" {
		t.Fatalf("Box = %q: a 38;2;r;g;b colour is not dim", typed)
	}
}

// fakeTmux runs remote command lines against a fakeprompt box, as tmux 3.4 does: each send-keys call reaches the pane
// in one write, so text and Enter in one call land in one read, where the box takes Enter as a new line.
type fakeTmux struct {
	target string // session:window the pane really has; its id is %1
	mode   string // pane_in_mode
	m      *fakeprompt.Model
	calls  []string
	enter  func() bool // nil: Enter works; false: this Enter is lost
	stuck  bool        // the box ignores C-u and BSpace, so it never clears
	bufs   map[string]string
}

func (f *fakeTmux) run(line string) ([]byte, error) {
	f.calls = append(f.calls, line)
	argv, err := shell.Fields(line, nil)
	if err != nil {
		return nil, err
	}
	if len(argv) > 1 && argv[0] == "tmux" && (argv[1] == "set-buffer" || argv[1] == "paste-buffer") {
		return nil, f.buffer(argv[1:])
	}
	if len(argv) < 4 || argv[0] != "tmux" || argv[2] != "-t" {
		return nil, fmt.Errorf("fake tmux: unexpected %q", line)
	}
	if argv[1] == "display-message" {
		// as tmux: a session or window name that only starts the real one still finds it
		sess, win, _ := strings.Cut(f.target, ":")
		ws, ww, _ := strings.Cut(argv[3], ":")
		ww, _, _ = strings.Cut(ww, ".")
		if !strings.HasPrefix(sess, ws) || !strings.HasPrefix(win, ww) && ww != "3" {
			return []byte("can't find session: " + ws), errors.New("exit status 1")
		}
		mode := f.mode
		if mode == "" {
			mode = "0"
		}
		return []byte(strings.Join([]string{"%1", sess, win, "3", "0", mode}, "\t") + "\n"), nil
	}
	if argv[3] != "%1" {
		return []byte("fake tmux: keys and reads go to the pane id, not " + argv[3]), errors.New("exit status 1")
	}
	switch argv[1] {
	case "capture-pane":
		if strings.Join(argv[4:], " ") != "-p -e" {
			return nil, fmt.Errorf("fake tmux: capture-pane wants -p -e: %q", line)
		}
		return []byte(f.m.Screen()), nil
	case "send-keys":
		args, literal := argv[4:], false
		if len(args) > 0 && args[0] == "-l" {
			literal, args = true, args[1:]
		}
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		var chunk strings.Builder
		for i, a := range args {
			// tmux reads an argument that ends in an unescaped ";" as the end of the command
			if strings.HasSuffix(a, `\;`) {
				a = strings.TrimSuffix(a, `\;`) + ";"
			} else if strings.HasSuffix(a, ";") {
				a = strings.TrimSuffix(a, ";")
				if i != len(args)-1 {
					return nil, fmt.Errorf("fake tmux: unknown command %q", args[i+1])
				}
			}
			if literal {
				chunk.WriteString(a)
				continue
			}
			switch a {
			case "Enter":
				if f.enter != nil && !f.enter() {
					continue
				}
				chunk.WriteString("\r")
			case "C-u", "BSpace":
				if f.stuck {
					continue
				}
				chunk.WriteString(map[string]string{"C-u": "\x15", "BSpace": "\x7f"}[a])
			case "C-e":
				chunk.WriteByte(0x05)
			default:
				chunk.WriteString(a)
			}
		}
		if chunk.Len() > 0 {
			f.m.Feed([]byte(chunk.String()))
		}
		return nil, nil
	}
	return nil, fmt.Errorf("fake tmux: unexpected %q", line)
}

// buffer runs set-buffer -b <name> -- <data> and paste-buffer -p -d -b <name> -t %1 as tmux does: a bare ";" ending the
// data ends the command, and a paste with -p reaches the pane in one read, bracketed, its line breaks as CR.
func (f *fakeTmux) buffer(argv []string) error {
	if f.bufs == nil {
		f.bufs = map[string]string{}
	}
	if len(argv) == 5 && argv[0] == "set-buffer" && argv[1] == "-b" && argv[3] == "--" {
		data := argv[4]
		if strings.HasSuffix(data, `\;`) {
			data = strings.TrimSuffix(data, `\;`) + ";"
		} else {
			data = strings.TrimSuffix(data, ";")
		}
		f.bufs[argv[2]] = data
		return nil
	}
	if strings.Join(argv, " ") == "paste-buffer -p -d -b crius-tsend-1 -t %1" {
		data, ok := f.bufs["crius-tsend-1"]
		if !ok {
			return errors.New("fake tmux: no buffer crius-tsend-1")
		}
		delete(f.bufs, "crius-tsend-1")
		f.m.Feed([]byte("\x1b[200~" + strings.ReplaceAll(data, "\n", "\r") + "\x1b[201~"))
		return nil
	}
	return fmt.Errorf("fake tmux: unexpected %q", argv)
}

func newFake() *fakeTmux { return &fakeTmux{target: "team:qa", m: fakeprompt.New()} }

func TestSendSubmitsIntoAPasteSensitiveBox(t *testing.T) {
	f := newFake()
	if err := Send(f.run, "team:qa", wake); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.m.Sent) != 1 || f.m.Sent[0] != wake {
		t.Fatalf("the box sent %q, want just %q; box now %q; calls %q", f.m.Sent, wake, f.m.Lines, f.calls)
	}
}

func TestSendClearsAMultiLineLeftoverFirst(t *testing.T) {
	f := newFake()
	f.m.Feed([]byte(wake + "\rsecond line\r")) // the 04:06 state: an old paste whose Enter became new lines
	if err := Send(f.run, "team:qa", "new text"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.m.Sent) != 1 || f.m.Sent[0] != "new text" {
		t.Fatalf("the box sent %q, want only %q", f.m.Sent, "new text")
	}
}

// The 04:27 state in quotes:dev: a seven-line paste left in the box. Claude Code's C-u stops at a line start, so
// the clear must take the line breaks out one by one and read the box after each round.
func TestSendClearsASevenLineLeftoverInRounds(t *testing.T) {
	f := newFake()
	f.m.Busy = true
	f.m.Feed([]byte("Answer the ASK just posted on the bus.\rRow AC16 is yours.\rRed first.\rThen the page.\r" +
		"Tag the board.\rPost DONE with the sha.\rThen park."))
	if len(f.m.Lines) != 7 {
		t.Fatalf("set-up: the box has %d lines, want 7", len(f.m.Lines))
	}
	if err := Send(f.run, "team:qa", wake); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.m.Sent) != 1 || f.m.Sent[0] != wake {
		t.Fatalf("the box sent %q, want only %q", f.m.Sent, wake)
	}
}

func TestSendGivesUpClearingAfter20Rounds(t *testing.T) {
	f := newFake()
	f.stuck = true
	f.m.Feed([]byte("old draft\rsecond line"))
	err := Send(f.run, "team:qa", wake)
	if !errors.Is(err, ErrNotSent) || !strings.Contains(err.Error(), "after clearing") {
		t.Fatalf("Send = %v, want ErrNotSent: the box still holds it after clearing", err)
	}
	rounds := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, " BSpace") {
			rounds++
		}
		if strings.Contains(c, " -l ") {
			t.Fatalf("typed the text into a box that never cleared: %q", c)
		}
	}
	if rounds != 20 {
		t.Fatalf("%d clearing rounds, want 20", rounds)
	}
}

func TestSendQueuesIntoABusyTab(t *testing.T) {
	f := newFake()
	f.m.Busy = true
	if err := Send(f.run, "team:qa", wake); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.m.Sent) != 1 {
		t.Fatalf("sent %q", f.m.Sent)
	}
}

func TestSendKeepsATrailingSemicolon(t *testing.T) {
	f := newFake()
	if err := Send(f.run, "team:qa", "go on; then stop;"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(f.m.Sent) != 1 || f.m.Sent[0] != "go on; then stop;" {
		t.Fatalf("sent %q", f.m.Sent)
	}
}

func TestSendRetriesEnterOnceThenSucceeds(t *testing.T) {
	f := newFake()
	n := 0
	f.enter = func() bool { n++; return n > 1 } // the first Enter is lost
	if err := Send(f.run, "team:qa", wake); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if n != 2 || len(f.m.Sent) != 1 {
		t.Fatalf("Enters %d, sent %q", n, f.m.Sent)
	}
}

func TestSendFailsLoudWhenTheTextStaysInTheBox(t *testing.T) {
	f := newFake()
	n := 0
	f.enter = func() bool { n++; return false } // every Enter is lost
	err := Send(f.run, "team:qa", wake)
	if !errors.Is(err, ErrNotSent) || !strings.Contains(err.Error(), "still holds") {
		t.Fatalf("Send = %v, want ErrNotSent saying the box still holds the text", err)
	}
	if n != 2 {
		t.Fatalf("Enter pressed %d times, want 2 (one retry)", n)
	}
}

func TestSendRefusesWithoutABox(t *testing.T) {
	f := newFake()
	run := func(line string) ([]byte, error) {
		if strings.Contains(line, "capture-pane") {
			return []byte(fixture(t, "dialog.txt")), nil
		}
		return f.run(line)
	}
	err := Send(run, "team:qa", wake)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Send = %v, want ErrRefused", err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "send-keys") {
			t.Fatalf("typed into a tab with no box: %q", c)
		}
	}
}

func TestSendSaysMaybeSentWhenThePaneCannotBeReadAfterEnter(t *testing.T) {
	f := newFake()
	entered := false
	run := func(line string) ([]byte, error) {
		if entered && strings.Contains(line, "capture-pane") {
			return nil, errors.New("ssh: connection reset")
		}
		if strings.HasSuffix(line, " Enter") {
			entered = true
		}
		return f.run(line)
	}
	if err := Send(run, "team:qa", wake); !errors.Is(err, ErrMaybeSent) {
		t.Fatalf("Send = %v, want ErrMaybeSent", err)
	}
}

func TestSendRefusesBadInput(t *testing.T) {
	for _, c := range [][2]string{
		{"team:qa", "two\rlines"},
		{"team:qa", "  "},
		{"-t", "x"},
		{"team:qa; rm", "x"},
		{"team", "x"},
		{"%1", "x"},
		{"team:qa", "stop\x1bnow"},
		{"team:qa", "a\tb"},
		{"team:qa", "!rm -rf /"},
		{"team:qa", " # remember this"},
	} {
		f := newFake()
		if err := Send(f.run, c[0], c[1]); !errors.Is(err, ErrRefused) || len(f.calls) != 0 {
			t.Errorf("Send(%q, %q) = %v after %d calls, want ErrRefused before any", c[0], c[1], err, len(f.calls))
		}
	}
}

func TestQuoteSurvivesTheRemoteShell(t *testing.T) {
	for _, s := range []string{`it's "fine"`, `$HOME $(id) ; | & \ *`, "-l"} {
		got, err := shell.Fields("x "+quote(s), nil)
		if err != nil || len(got) != 2 || got[1] != s {
			t.Errorf("quote(%q) parses back as %q (%v)", s, got, err)
		}
	}
}

func TestSendRefusesAPaneTmuxOnlyFindsByPrefix(t *testing.T) {
	f := newFake()
	f.target = "team-old:qa-2" // team:qa is gone; tmux would take team-old:qa-2 for it
	err := Send(f.run, "team:qa", wake)
	if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "not that pane") {
		t.Fatalf("Send = %v, want ErrRefused naming the other pane", err)
	}
	if len(f.calls) != 1 || len(f.m.Sent) != 0 {
		t.Fatalf("calls %q, sent %q: want only the display-message", f.calls, f.m.Sent)
	}
}

func TestSendTakesAWindowByIndex(t *testing.T) {
	f := newFake()
	if err := Send(f.run, "team:3", wake); err != nil { // the fake's window qa has index 3
		t.Fatalf("Send: %v", err)
	}
}

func TestSendRefusesAPaneInCopyMode(t *testing.T) {
	f := newFake()
	f.mode = "1"
	if err := Send(f.run, "team:qa", wake); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "copy mode") {
		t.Fatalf("Send = %v, want ErrRefused for copy mode", err)
	}
}

func TestSendDoesNotPressEnterAgainWhenTheBoxChanged(t *testing.T) {
	f := newFake()
	n := 0
	f.enter = func() bool { // the first Enter takes an autocomplete instead of sending
		n++
		f.m.Lines = []string{"read @docs/STATUS.md "}
		return false
	}
	err := Send(f.run, "team:qa", "read @STATUS.md")
	if !errors.Is(err, ErrNotSent) || !strings.Contains(err.Error(), "not the text") || n != 1 {
		t.Fatalf("Send = %v after %d Enters, want ErrNotSent after 1", err, n)
	}
}

func TestSendSaysMaybeSentWhenTheBoxEmptiesButTheTextShowsNowhere(t *testing.T) {
	f := newFake()
	f.enter = func() bool { f.m.Lines = []string{""}; return false } // the box emptied, nothing was sent
	if err := Send(f.run, "team:qa", wake); !errors.Is(err, ErrMaybeSent) {
		t.Fatalf("Send = %v, want ErrMaybeSent", err)
	}
}

func TestSendProvesASecondSendOfTheSameText(t *testing.T) {
	f := newFake()
	for i := 0; i < 2; i++ {
		if err := Send(f.run, "team:qa", wake); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if len(f.m.Sent) != 2 {
		t.Fatalf("sent %q", f.m.Sent)
	}
}

func TestBoxSkipsACharacterSetSwitch(t *testing.T) {
	screen := "\x1b(B" + strings.Repeat("─", 20) + "\x1b(B\n❯\u00a0hi\n" + strings.Repeat("─", 20) + "\n"
	if typed, boxed := Box(screen); !boxed || typed != "hi" {
		t.Fatalf("Box = %q, %v", typed, boxed)
	}
}

// clearTab answers a /clear with real captures of Claude Code 2.1.326 (11-17): the old session with an empty box, the
// box holding /clear under its command menu, then after Enter the screen /clear leaves (after) or, for a /clear that
// did not land, the old session again. Once the /clear is through, keys and reads go to the fake box f.
type clearTab struct {
	*fakeTmux
	t     *testing.T
	after string // the fixture shown after the /clear's Enter
	phase int    // 0 the old session, 1 /clear typed, 2 after its Enter, 3 the fake box
}

func newClearTab(t *testing.T, after string) *clearTab {
	return &clearTab{fakeTmux: newFake(), t: t, after: after}
}

func (c *clearTab) run(line string) ([]byte, error) {
	switch {
	case c.phase == 3 || strings.Contains(line, "display-message"):
	case strings.Contains(line, "capture-pane"):
		c.calls = append(c.calls, line)
		return []byte(fixture(c.t, []string{"clear-before.txt", "clear-typed.txt", c.after}[c.phase])), nil
	case c.phase == 0 && strings.HasSuffix(line, "-l -- '/clear'"):
		c.calls = append(c.calls, line)
		c.phase = 1
		return nil, nil
	case c.phase == 1 && strings.HasSuffix(line, " Enter"):
		c.calls = append(c.calls, line)
		c.phase = 2
		return nil, nil
	case c.phase == 2:
		c.phase = 3
	default:
		return nil, fmt.Errorf("clearTab: unexpected %q in phase %d", line, c.phase)
	}
	return c.fakeTmux.run(line)
}

// (a) 11-17 20:4x quotes:qa and 21:43 staffy:qa: the /clear went, but crius-tsend said MAYBE SENT, since /clear wipes
// the screen and leaves its text there once, as often as the command menu showed it before Enter. The screen /clear
// leaves is the proof instead.
func TestSendProvesAClearByTheScreenItLeaves(t *testing.T) {
	c := newClearTab(t, "clear-after.txt")
	if err := Send(c.run, "team:qa", "/clear"); err != nil {
		t.Fatalf("Send(/clear) = %v, want SENT: the screen shows a cleared session", err)
	}
	if n := strings.Count(strings.Join(c.calls, "\n"), " Enter"); n != 1 {
		t.Fatalf("Enter pressed %d times, want 1", n)
	}
}

// (b) A /clear that did not land: the box emptied, but the old session is still on the screen. It stops loudly.
func TestSendSaysMaybeSentWhenAClearLeavesTheOldSession(t *testing.T) {
	c := newClearTab(t, "clear-before.txt")
	err := Send(c.run, "team:qa", "/clear")
	if !errors.Is(err, ErrMaybeSent) || !strings.Contains(err.Error(), "still shows the old session") {
		t.Fatalf("Send(/clear) = %v, want ErrMaybeSent saying the old session still shows", err)
	}
}

// Cleared reads real screens: what /clear leaves (here and on the ThinkPad) is cleared; a session with a transcript is
// not, nor one whose /clear shows under or above other turns.
func TestClearedReadsRealScreens(t *testing.T) {
	rule := strings.Repeat("─", 50)
	box := rule + "\n❯ \n" + rule + "\n  ⏵⏵ bypass permissions on\n"
	for _, c := range []struct {
		name, screen string
		want         bool
	}{
		{"after /clear, Claude Code 2.1.326 on the Mac", fixture(t, "clear-after.txt"), true},
		{"after /clear, staffy:qa on the ThinkPad 21:43", fixture(t, "clear-after-thinkpad.txt"), true},
		{"the old session, box empty", fixture(t, "clear-before.txt"), false},
		{"the old session, /clear in the box", fixture(t, "clear-typed.txt"), false},
		{"/clear under the old session", "● Waiting for the unit run.\n\n❯ /clear\n" + box, false},
		{"the last restart's /clear, then its brief", "❯ /clear\n\n❯ Read BRIEF.md and go\n\n● On it.\n" + box, false},
		{"no box", "❯ /clear\n", false},
	} {
		if got := wakewatch.Cleared(plain(c.screen)); got != c.want {
			t.Errorf("%s: Cleared = %v, want %v", c.name, got, c.want)
		}
	}
}

const brief = "Fresh start for staffy:qa.\nRead ~/.crius/state/staffy/BRIEF.md first.\nThen post READY on the bus;"

// (c) A brief with line breaks goes in as one message: one bracketed paste, then Enter in a call of its own. Typed
// key by key, each line break would press Enter early.
func TestSendTypesAMultiLineTextAsOnePaste(t *testing.T) {
	f := newFake()
	if err := Send(f.run, "team:qa", brief); err != nil {
		t.Fatalf("Send = %v, want SENT", err)
	}
	if len(f.m.Sent) != 1 || f.m.Sent[0] != brief {
		t.Fatalf("the box sent %q, want the brief once, whole: %q", f.m.Sent, brief)
	}
}

// The whole restart of a real tab: /clear, proved by the screen it leaves, then a multi-line brief as one message;
// and a /clear that did not land stops it before the brief.
func TestRestartOfARealTabClearsThenSendsAMultiLineBrief(t *testing.T) {
	for _, c := range []struct {
		after string
		code  int
		said  string
		sent  []string
	}{
		{"clear-after.txt", wakewatch.RestartReady, "SENT team:qa", []string{brief}},
		{"clear-before.txt", wakewatch.RestartMaybe, "the brief was not sent", nil},
	} {
		tab := newClearTab(t, c.after)
		var out strings.Builder
		d := wakewatch.RestartDeps{Out: &out,
			Tabs: func() ([]wakewatch.RemoteTab, error) {
				return []wakewatch.RemoteTab{{Box: "thinkpad", Target: "team:qa"}}, nil
			},
			Capture: func(wakewatch.RemoteTab) (string, error) {
				b, err := tab.run("tmux capture-pane -t '%1' -p -e")
				return Visible(string(b)), err
			},
			Procs: func(wakewatch.RemoteTab) (string, error) {
				return "100\n 100 99 -bash\n 200 100 claude --resume 58128bf4\n--- cmdline\n", nil
			},
			SendTab: func(t wakewatch.RemoteTab, text string) error { return Send(tab.run, t.Target, text) },
		}
		if code := wakewatch.Restart(d, "team:qa", brief); code != c.code || !strings.Contains(out.String(), c.said) {
			t.Errorf("%s: exit %d, said %q; want exit %d saying %q", c.after, code, out.String(), c.code, c.said)
		}
		if strings.Join(tab.m.Sent, "|") != strings.Join(c.sent, "|") {
			t.Errorf("%s: the box sent %q, want %q", c.after, tab.m.Sent, c.sent)
		}
	}
}

// Review P2s: a multi-line text is seen above the box by its last line. A screen that draws each sent line between │
// marks splits the text's start at its first line break; a long text's first lines scroll off once it is sent.
func TestSendSeesAMultiLineTextByItsLastLine(t *testing.T) {
	long := "Long brief, first line."
	for i := 2; i <= 40; i++ {
		long += fmt.Sprintf("\nfiller line %d of the brief.", i)
	}
	for name, c := range map[string]struct {
		bubble bool
		rows   int
		text   string
	}{"drawn in a bubble": {true, 0, brief}, "first lines scrolled off": {false, 12, long}} {
		f := newFake()
		f.m.Bubble, f.m.Rows = c.bubble, c.rows
		if err := Send(f.run, "team:qa", c.text); err != nil {
			t.Errorf("%s: Send = %v, want SENT", name, err)
		}
		if len(f.m.Sent) != 1 || f.m.Sent[0] != c.text {
			t.Errorf("%s: the box sent %q, want the text once", name, f.m.Sent)
		}
	}
}

// Re-review P2: a brief that ends in a short, common line ("Go.") is still seen when its long echo scrolls the old
// copies of that line off the screen.
func TestSendSeesALongTextThatEndsInACommonLine(t *testing.T) {
	text := "Long brief, first line."
	for i := 2; i <= 20; i++ {
		text += fmt.Sprintf("\nfiller line %d of the brief.", i)
	}
	text += "\nGo."
	f := newFake()
	f.m.Sent, f.m.Rows = []string{"Go.", "Go."}, 12
	if err := Send(f.run, "team:qa", text); err != nil {
		t.Fatalf("Send = %v, want SENT", err)
	}
}
