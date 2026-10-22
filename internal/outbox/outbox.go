// Package outbox types messages into Claude tabs in cmux. Each message is stored first, one file per message in
// $CRIUS_HOME/outbox, and stays there until the tab's input box has taken it and no longer holds it after Enter. Text
// someone typed in the box holds the messages for that tab; crius-wakewatch tries them again every run, oldest first.
//
// Grey text in the box (Claude's placeholder or a prompt suggestion) is not a draft. read-screen gives plain text, so
// it cannot tell; cmux send without --force asks the app for the prompt's state, which skips faint text, and refuses
// only over text someone typed or an open dialog (cmux Sources/TerminalController+SurfaceInputState.swift).
package outbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/wakewatch"
)

// Cmux runs cmux. Tests replace it.
var Cmux = runCmux

var (
	// MaxAge is how long a message waits for its tab. An older one is set aside in outbox/dropped, never typed: its
	// tab is gone or its news is old, and a later tab with the same title must not get it.
	MaxAge = time.Hour
	// LockWait is how long a sender waits for another to finish, and CmuxTimeout how long one cmux call may run: a
	// hung cmux must not hold the outbox, and with it every wakewatch run, for ever.
	LockWait    = 2 * time.Minute
	CmuxTimeout = 30 * time.Second
)

// ErrHeld marks a message still stored, to be typed once its tab's box is empty: sending it again would type it twice.
// ErrOnto marks a message typed onto text already in the box (cmux's draft check let it through) and set aside: it
// must not be typed onto the same text again. errKilled marks a cmux call stopped at CmuxTimeout: what it did is unknown.
var (
	ErrHeld   = errors.New("held")
	ErrOnto   = errors.New("typed onto other text in the box")
	errKilled = errors.New("killed")
)

// runCmux gives cmux's stdout, or on failure its stdout and stderr: a warning on stderr must not reach a JSON parse.
func runCmux(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), CmuxTimeout)
	defer cancel()
	var out, msg bytes.Buffer
	cmd := exec.CommandContext(ctx, "cmux", args...)
	cmd.Stdout, cmd.Stderr = &out, &msg
	cmd.WaitDelay = time.Second // a child cmux left behind keeps the pipes open
	// ErrWaitDelay alone means cmux exited 0 and only a child it left kept the pipes open: its work was done.
	if err := cmd.Run(); err != nil && !(errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil) {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w after %v: %v", errKilled, CmuxTimeout, err)
		}
		return append(out.Bytes(), msg.Bytes()...), err
	}
	return out.Bytes(), nil
}

// Message is one stored message for the terminal tab titled To. Entered is set just before its Enter is pressed: a
// message with Entered whose box no longer holds it went, even when the check after Enter failed. Onto says why it was
// typed onto other text in its box: it is not typed again, and the next Flush sets it aside and names it.
type Message struct {
	To        string    `json:"to"`
	Text      string    `json:"text"`
	Created   time.Time `json:"created"`
	Tries     int       `json:"tries"`
	Entered   bool      `json:"entered,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	Onto      string    `json:"onto,omitempty"`
}

type stored struct {
	path string
	Message
}

// Send stores text for the tab titled to (once: the same text already waiting is not stored again), then types what
// is stored for that tab, oldest first. An error wrapping ErrHeld means text is still stored, one wrapping ErrOnto that
// it was set aside; any other, that this text was not stored. Damaged and old files are left to Flush: they are not
// this send's, and an error about them would tell the caller this text failed.
func Send(home, to, text string) error {
	unlock, err := lock(home)
	if err != nil {
		return err
	}
	defer unlock()
	if err := add(home, to, text); err != nil {
		return err
	}
	return flush(home, to)
}

// Flush types every stored message, tab by tab. It gives each message or file it set aside, which nobody else hears
// of, apart from the error for what it still holds or could not do, which the next run tries again. A message typed
// onto other text, here or in an earlier Send, is set aside by the sweep after the sends.
func Flush(home string) (aside []error, held error) {
	unlock, err := lock(home)
	if err != nil {
		return nil, err
	}
	defer unlock()
	aside, err = sweep(home)
	if err != nil {
		return aside, err
	}
	all, err := list(home)
	if err != nil {
		return aside, err
	}
	var errs []error
	seen := map[string]bool{}
	for _, m := range all {
		if seen[m.To] {
			continue
		}
		seen[m.To] = true
		var now asideNow
		switch err := flush(home, m.To); {
		case errors.As(err, &now):
			aside = append(aside, now.error)
		case err != nil && !errors.Is(err, ErrOnto):
			errs = append(errs, err)
		}
	}
	more, err := sweep(home)
	return append(aside, more...), errors.Join(append(errs, err)...)
}

// asideNow is a message flush set aside itself instead of leaving it to the sweep, which Flush names.
type asideNow struct{ error }

func (a asideNow) Unwrap() error { return a.error }

// lock keeps one sender at a time: two could type the same stored message.
func lock(home string) (func(), error) {
	if err := os.MkdirAll(filepath.Join(home, "outbox"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(home, "outbox", ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for end := time.Now().Add(LockWait); ; time.Sleep(20 * time.Millisecond) {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(end) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %v (locked by another sender for over %v?)", f.Name(), err, LockWait)
		}
	}
}

// sweep sets aside what must not be typed: a file that does not parse goes to outbox/damaged, and a message older
// than MaxAge to outbox/dropped. Each is named in aside, so nothing is lost without a word, and the rest still go.
func sweep(home string) (aside []error, err error) {
	paths, err := filepath.Glob(filepath.Join(home, "outbox", "*.json"))
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		var m Message
		b, err := os.ReadFile(p)
		if err == nil {
			err = json.Unmarshal(b, &m)
		}
		switch {
		case err != nil:
			aside = append(aside, setAside(p, "damaged", fmt.Errorf("%s is damaged: %v", filepath.Base(p), err)))
		case m.Onto != "":
			aside = append(aside, setAside(p, "dropped", fmt.Errorf("to %q: %q: %s", m.To, shown(m.Text), m.Onto)))
		case !old(m):
		case m.Entered:
			aside = append(aside, setAside(p, "dropped", fmt.Errorf("Enter was pressed on it, but for %v no check saw its box without it, so it may or may not have gone: not typed again: to %q: %q", MaxAge, m.To, shown(m.Text))))
		default:
			aside = append(aside, setAside(p, "dropped", fmt.Errorf("never typed in %v, so not typed now: to %q: %q", MaxAge, m.To, shown(m.Text))))
		}
	}
	return aside, nil
}

// old tells a message that waited past MaxAge: sweep sets it aside, and a send leaves it alone.
func old(m Message) bool { return time.Since(m.Created) > MaxAge }

// setAside moves a message's file into the outbox folder dir, kept for a look, and gives why with where it went.
func setAside(path, dir string, why error) error {
	to := filepath.Join(filepath.Dir(path), dir)
	if err := os.MkdirAll(to, 0o700); err != nil {
		return errors.Join(why, err)
	}
	if err := os.Rename(path, filepath.Join(to, filepath.Base(path))); err != nil {
		return errors.Join(why, err)
	}
	return fmt.Errorf("%w: set aside in %s", why, to)
}

// list gives every stored message, oldest first: a file's name starts with its creation time. A file that does not
// parse, a message past MaxAge or one typed onto other text is left out for sweep to set aside.
func list(home string) ([]stored, error) {
	paths, err := filepath.Glob(filepath.Join(home, "outbox", "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var out []stored
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		s := stored{path: p}
		if json.Unmarshal(b, &s.Message) == nil && !old(s.Message) && s.Onto == "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func add(home, to, text string) error {
	all, err := list(home)
	if err != nil {
		return err
	}
	for _, m := range all {
		if m.To == to && m.Text == text {
			return nil
		}
	}
	now := time.Now()
	sum := sha256.Sum256([]byte(to + "\x00" + text))
	return write(filepath.Join(home, "outbox", fmt.Sprintf("%020d-%x.json", now.UnixNano(), sum[:4])),
		Message{To: to, Text: text, Created: now})
}

// write replaces a message's file whole and on disk, so neither a reader nor a power cut leaves half of one.
func write(path string, m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// flush types the messages stored for the tab titled to, oldest first, and stops at the first that does not go in.
func flush(home, to string) error {
	all, err := list(home)
	if err != nil {
		return err
	}
	for i, m := range all {
		if m.To != to {
			continue
		}
		// An Enter that surely did not go is unmarked even when its file cannot be written, so the write after keeps it
		// unmarked: a marked message in an empty box counts as sent.
		mark := func(entered bool) error {
			c := m.Message
			c.Entered = entered
			err := write(m.path, c)
			if err == nil || !entered {
				m.Entered = entered
			}
			return err
		}
		if err := deliver(to, m.Text, m.Entered, mark); err != nil {
			if errors.Is(err, ErrOnto) { // typed once already: it must not be added to the same text again
				c := m.Message
				c.Onto = err.Error()
				if werr := write(m.path, c); werr != nil { // unmarked, the sweep would not know it: set aside now
					return asideNow{setAside(m.path, "dropped", fmt.Errorf("to %q: %q: %w (and it could not be marked: %v)", to, shown(m.Text), err, werr))}
				}
				return err
			}
			m.Tries++
			m.LastError = err.Error()
			if werr := write(m.path, m.Message); werr != nil { // still stored as before: held all the same
				err = fmt.Errorf("%v (and its try could not be saved: %v)", err, werr)
			}
			n := 0
			for _, o := range all[i:] {
				if o.To == to {
					n++
				}
			}
			return fmt.Errorf("%v: %w, %d message(s) for %q stored and typed once its box is empty", err, ErrHeld, n, to)
		}
		if err := os.Remove(m.path); err != nil {
			return fmt.Errorf("%q took the message, but its file could not be removed: %v: it is still %w in the outbox, and the next run drops it as its box no longer holds it", to, err, ErrHeld)
		}
	}
	return nil
}

// Terminal is one terminal tab in cmux.
type Terminal struct{ Workspace, Surface, ID, Title, TTY string }

// Terminals lists every terminal tab from cmux's JSON tree. The text tree prints titles as they are, so a title can
// look like another tab's row.
func Terminals() ([]Terminal, error) {
	b, err := Cmux("--json", "--id-format", "both", "tree", "--all")
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %v: %s", err, strings.TrimSpace(string(b)))
	}
	var tree struct {
		Windows []struct {
			Workspaces []struct {
				Ref   string
				Panes []struct {
					Surfaces []struct{ ID, Ref, Title, TTY, Type string }
				}
			}
		}
	}
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil, fmt.Errorf("cmux --json tree: %v", err)
	}
	var out []Terminal
	for _, w := range tree.Windows {
		for _, ws := range w.Workspaces {
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type == "terminal" {
						out = append(out, Terminal{ws.Ref, s.Ref, s.ID, s.Title, s.TTY})
					}
				}
			}
		}
	}
	return out, nil
}

// deliver types text into the tab titled to, then Enter. The tab is the one terminal in cmux with that title. None or
// several is loud and types nothing: a cmux send with no target goes to the caller's tab. entered tells that Enter was
// pressed on this message on an earlier try; mark records whether it was (true just before Enter, false when the key
// surely did not go).
func deliver(to, text string, entered bool, mark func(entered bool) error) error {
	tabs, err := Terminals()
	if err != nil {
		return err
	}
	var found []Terminal
	for _, tab := range tabs {
		if tab.Title == to {
			found = append(found, tab)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("cmux tree --all has %d terminals titled %q, want one: nothing typed", len(found), to)
	}
	if err := claudeInFront(found[0]); err != nil {
		return err
	}
	ws, surface := found[0].Workspace, found[0].Surface
	draft, err := inputBox(ws, surface)
	if err != nil {
		return err
	}
	if draft != "" && strings.Contains(squash(draft), squash(text)) {
		// read-screen has no colours: what looks like the message may be a grey suggestion in an empty box.
		typed, err := typedText(found[0])
		if err != nil {
			return err
		}
		if !typed {
			draft = ""
		} else if again, err := inputBox(ws, surface); err != nil {
			return err
		} else if squash(again) != squash(draft) { // someone typed between the two looks: the typed text may be theirs
			return fmt.Errorf("%q's prompt changed from %q to %q while it was read: nothing typed", to, shown(draft), shown(again))
		}
	}
	switch {
	case draft != "" && squash(draft) == squash(text):
		// The whole draft is this message, as typed: its Enter did not go on an earlier try, so only Enter is left.
	case entered && draft != "" && strings.Contains(squash(draft), squash(text)):
		// Its Enter may not have gone (cmux was killed), and someone has typed around it since.
		return fmt.Errorf("%q's prompt holds %q after an Enter that may not have gone: %w, so Enter was not pressed", to, shown(draft), ErrOnto)
	case entered:
		// Its Enter went on an earlier try and the box no longer holds it: it was sent, and only the check after failed.
		return nil
	default:
		// A draft that someone edited or wrote is not it, and cmux send refuses to type over it.
		if out, err := Cmux("send", "--workspace", ws, "--surface", surface, "--", text); err != nil {
			if draft != "" { // read-screen has no colours: the draft's start lets Crius or the owner see what the box holds
				return fmt.Errorf("%q's prompt holds a draft (%q): cmux send: %v: %s", to, shown(draft), err, strings.TrimSpace(string(out)))
			}
			return fmt.Errorf("cmux send: %v: %s", err, strings.TrimSpace(string(out)))
		}
		// Someone may have typed at the same moment: Enter would send the mix.
		if draft, err = waitBox(ws, surface, func(d string) bool { return squash(d) == squash(text) }); err != nil {
			return err
		}
		if squash(draft) != squash(text) {
			if strings.Contains(squash(draft), squash(text)) {
				return fmt.Errorf("%q's prompt holds %q after typing: %w, so Enter was not pressed", to, shown(draft), ErrOnto)
			}
			return fmt.Errorf("%q's prompt holds %q after typing, not the message alone: Enter not pressed", to, shown(draft))
		}
	}
	if err := mark(true); err != nil {
		return err
	}
	if out, err := Cmux("send-key", "--workspace", ws, "--surface", surface, "enter"); err != nil {
		err = fmt.Errorf("cmux send-key: %w: %s", err, strings.TrimSpace(string(out)))
		if errors.Is(err, errKilled) { // the key may have gone before the kill
			return err
		}
		return errors.Join(err, mark(false))
	}
	// An empty box can show grey text again (a placeholder or suggestion), which read-screen gives as plain text: the
	// message went once the box no longer holds it.
	if draft, err = waitBox(ws, surface, func(d string) bool { return squash(d) != squash(text) }); err != nil {
		return fmt.Errorf("%q's prompt could not be read after Enter: %v", to, err)
	}
	if squash(draft) == squash(text) {
		if typed, err := typedText(found[0]); err != nil || typed {
			return errors.Join(fmt.Errorf("%q's prompt still holds the message after Enter", to), err)
		}
	}
	return nil
}

// typedText asks cmux whether a tab's input box holds typed text, typing nothing. Unlike read-screen it keeps the
// faint style, so a box with only grey text in it is empty. It asks by the tab's id and checks the answer names that
// tab: cmux answers an id it does not know with another tab's state.
func typedText(tab Terminal) (bool, error) {
	params, err := json.Marshal(map[string]string{"surface_id": tab.ID})
	if err != nil {
		return false, err
	}
	out, err := Cmux("rpc", "surface.input_state", string(params))
	if err != nil {
		return false, fmt.Errorf("cmux rpc surface.input_state: %v: %s", err, strings.TrimSpace(string(out)))
	}
	var s struct {
		SurfaceID string `json:"surface_id"`
		State     string
	}
	if err := json.Unmarshal(out, &s); err != nil {
		return false, fmt.Errorf("cmux rpc surface.input_state: %v", err)
	}
	if tab.ID == "" || !strings.EqualFold(s.SurfaceID, tab.ID) {
		return false, fmt.Errorf("cmux gave the input state of surface %q, not of %s %q: nothing typed", s.SurfaceID, tab.Surface, tab.ID)
	}
	switch s.State {
	case "draft":
		return true, nil
	case "empty":
		return false, nil
	}
	return false, fmt.Errorf("cmux calls the input box of %s %q, so typed text cannot be told from grey: nothing typed", tab.Surface, s.State)
}

// shown gives what an error may say about a draft: these errors go to wakewatch.log and the bus. It is redacted before
// the cut (a cut could split a secret). A wrap in the box splits a secret into pieces the redactor may catch only in
// part, or not at all, so a draft of several lines with a secret in it leaves nothing but the marker. The wraps are
// taken out from each line on: Prompt trims a wrap's space away, and a token glued to the word before it has no word
// break to match at.
func shown(draft string) string {
	text, hits := redact.Redact(draft)
	lines := strings.Split(draft, "\n")
	for i := range lines {
		_, n := redact.Redact(strings.Join(lines[i:], ""))
		hits += n
	}
	if len(lines) > 1 && hits > 0 {
		return "[redacted]"
	}
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 50 {
		text = string(r[:50]) + "…"
	}
	return text
}

// BoxWait is the pause between two reads of an input box. Tests shorten it.
var BoxWait = 100 * time.Millisecond

// waitBox reads a tab's input box until its draft is as wanted, for up to 20 reads: Claude draws a key's effect a
// moment after it arrives. It gives the last draft it read.
func waitBox(workspace, surface string, wanted func(draft string) bool) (draft string, err error) {
	for i := 0; i < 20; i++ {
		if i > 0 {
			time.Sleep(BoxWait)
		}
		if draft, err = inputBox(workspace, surface); err == nil && wanted(draft) {
			return draft, nil
		}
	}
	return draft, err
}

// inputBox reads a tab's screen and gives the draft in Claude's input box. Without the box on screen (bash mode, a
// dialog, a picker) Enter would run or pick something, so that is an error.
func inputBox(workspace, surface string) (string, error) {
	screen, err := Cmux("read-screen", "--workspace", workspace, "--surface", surface, "--lines", "40")
	if err != nil {
		said, _ := redact.Redact(strings.TrimSpace(string(screen))) // it may hold the screen, and goes to the bus
		return "", fmt.Errorf("cmux read-screen %s: %v: %s", surface, err, said)
	}
	draft, boxed := wakewatch.Prompt(string(screen))
	if !boxed {
		return "", fmt.Errorf("tab %s shows no Claude input box: nothing typed", surface)
	}
	return draft, nil
}

// squash drops all white space, so a line the input box wrapped equals the line as typed.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// claudeInFront checks that Claude is the foreground process of the tab's terminal. If Claude has exited, the tab is a
// shell, and Enter would run the message there.
func claudeInFront(tab Terminal) error {
	surface, tty := tab.Surface, tab.TTY
	if tty == "" {
		return fmt.Errorf("tab %s %s has no tty in cmux tree --all: nothing typed", tab.Workspace, surface)
	}
	front, err := Front(tty)
	if err != nil {
		return err
	}
	if filepath.Base(front) != "claude" {
		return fmt.Errorf("tab %s (%s) runs %q in front, not Claude: nothing typed", surface, tty, front)
	}
	return nil
}

// Front returns the command in front on a tty, the one whose process group owns the terminal, as ps shows it: a login
// shell shows as "-zsh".
func Front(tty string) (string, error) {
	if tty == "" {
		return "", errors.New("no tty to read the front command of")
	}
	out, err := exec.Command("ps", "-t", tty, "-o", "pid=,tpgid=,comm=").Output()
	if err != nil {
		return "", fmt.Errorf("ps -t %s: %v", tty, err)
	}
	front := ""
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == f[1] {
			front = strings.Join(f[2:], " ")
		}
	}
	return front, nil
}
