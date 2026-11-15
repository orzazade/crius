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
}

func (f *fakeTmux) run(line string) ([]byte, error) {
	f.calls = append(f.calls, line)
	argv, err := shell.Fields(line, nil)
	if err != nil {
		return nil, err
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
			case "C-u":
				chunk.WriteByte(0x15)
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
		{"team:qa", "two\nlines"},
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
