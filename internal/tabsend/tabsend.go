// Package tabsend types a message into a Claude Code session in a tmux pane on another machine and proves it was sent.
// A message with line breaks goes in as one bracketed paste.
//
// Claude Code takes keys that arrive in one read as a paste, where Enter is a new line: `tmux send-keys "<text>" Enter`
// in one call leaves the text sitting in the input box, with no error (11-15 04:06: quotes:qa, quotes:builder-2 and
// staffy:builder were told nothing). Send clears the box, types the text literally, presses Enter in a call of its own,
// and reads the pane until the text has left the box.
package tabsend

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/wakewatch"
)

// Runner runs one shell command line on the machine that holds the pane (through ssh) and gives its output.
type Runner func(line string) ([]byte, error)

var (
	// ErrRefused: nothing was typed.
	ErrRefused = errors.New("nothing typed")
	// ErrNotSent: keys were typed, but the text was not sent; the box may hold it.
	ErrNotSent = errors.New("not sent")
	// ErrMaybeSent: Enter was pressed and the pane could not be read after: the text may have gone, so do not send again.
	ErrMaybeSent = errors.New("maybe sent")
)

// targetRe is session:window, with an optional .pane: names only, so tmux's own match can be checked against them.
var targetRe = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_@+-]*):([A-Za-z0-9_][A-Za-z0-9_@+-]*)(?:\.(\d+))?$`)

// Send types text into the Claude Code box of the tmux pane target, then Enter, and checks that it went: the box no
// longer holds typed text and the text shows once more above it than before (for /clear: the screen shows a cleared
// session).
func Send(run Runner, target, text string) error {
	if why := refuse(target, text); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	pane, err := resolve(run, target)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	t := quote(pane)
	keys := func(args string) error {
		if out, err := run("tmux send-keys -t " + t + " " + args); err != nil {
			return fmt.Errorf("tmux send-keys: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	typed, _, _, err := read(run, t, text)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRefused, err)
	}
	// Clear what the box holds, a line per round: C-e C-u empties the line (Claude Code's C-u stops at a line start),
	// BSpace takes out the line break above it. The box is read after each round, until it holds no typed text.
	for round := 1; typed != ""; round++ {
		if round > ClearRounds {
			return fmt.Errorf("%w: the box still holds %q after clearing for %d rounds: text not typed", ErrNotSent, shown(typed), ClearRounds)
		}
		if err := keys("C-e C-u C-u"); err != nil {
			return fmt.Errorf("%w: clearing the box: %v", ErrNotSent, err)
		}
		if err := keys("BSpace"); err != nil {
			return fmt.Errorf("%w: clearing the box: %v", ErrNotSent, err)
		}
		time.Sleep(Wait)
		if typed, _, _, err = read(run, t, text); err != nil {
			return fmt.Errorf("%w: clearing the box: %v: text not typed", ErrNotSent, err)
		}
	}
	// Literally, as one key stream; tmux takes a word that ends in an unescaped ";" as the end of its command.
	lit := text
	if strings.HasSuffix(lit, ";") {
		lit = strings.TrimSuffix(lit, ";") + `\;`
	}
	if !strings.Contains(text, "\n") {
		if err := keys("-l -- " + quote(lit)); err != nil {
			return fmt.Errorf("%w: typing: %v", ErrNotSent, err)
		}
	} else if err := paste(run, pane, lit); err != nil { // keys would press Enter at each line break
		return fmt.Errorf("%w: pasting: %v", ErrNotSent, err)
	}
	isText := func(d string) bool { return squash(d) == squash(text) || strings.Contains(d, "[Pasted text") }
	typed, seen, _, err := waitBox(run, t, text, func(d string, _ int, _ string) bool { return isText(d) })
	if err != nil || !isText(typed) {
		return fmt.Errorf("%w: the box holds %q after typing, not the text (%v): Enter not pressed", ErrNotSent, shown(typed), err)
	}
	before := seen - onScreen(typed, text) // where the text shows outside the box
	// went says the text left the box for Claude: it shows once more above the box. /clear wipes the screen instead, so
	// its text shows there once however often the command menu showed it before (11-17, twice): the screen it leaves
	// proves it.
	went := func(seen int, screen string) bool { return seen > before }
	if strings.TrimSpace(text) == "/clear" {
		went = func(_ int, screen string) bool { return wakewatch.Cleared(screen) }
	}
	// Enter in a call of its own, so it reaches Claude in a read of its own and is not taken as part of a paste.
	for try := 1; ; try++ {
		if err := keys("Enter"); err != nil {
			return fmt.Errorf("%w: %v", ErrMaybeSent, err)
		}
		var screen string
		typed, seen, screen, err = waitBox(run, t, text, func(d string, n int, s string) bool { return d == "" && went(n, s) })
		switch {
		case err != nil:
			return fmt.Errorf("%w: Enter pressed, then %v", ErrMaybeSent, err)
		case typed == "" && went(seen, screen):
			return nil
		case typed == "" && strings.TrimSpace(text) == "/clear":
			return fmt.Errorf("%w: the box is empty after Enter, but the screen still shows the old session, not a cleared one", ErrMaybeSent)
		case typed == "":
			return fmt.Errorf("%w: the box is empty after Enter, but the text does not show above it", ErrMaybeSent)
		case !isText(typed): // an autocomplete or someone else changed it: Enter again would send something else
			return fmt.Errorf("%w: the box holds %q after Enter, not the text: Enter not pressed again", ErrNotSent, shown(typed))
		case try == 2:
			return fmt.Errorf("%w: the box still holds %q after Enter twice", ErrNotSent, shown(typed))
		}
	}
}

// resolve gives the id of the pane tmux picks for target, after checking that its session and window names (or
// index) are the ones asked for: tmux falls back to a name that starts with the target, or to another window, without
// an error, and every key would go there.
func resolve(run Runner, target string) (string, error) {
	m := targetRe.FindStringSubmatch(target)
	out, err := run("tmux display-message -t " + quote(target) + " -p " +
		quote("#{pane_id}\t#{session_name}\t#{window_name}\t#{window_index}\t#{pane_index}\t#{pane_in_mode}"))
	if err != nil {
		return "", fmt.Errorf("tmux display-message: %v: %s", err, strings.TrimSpace(string(out)))
	}
	f := strings.Split(strings.TrimRight(string(out), "\n"), "\t")
	if len(f) != 6 || !paneIDRe.MatchString(f[0]) {
		return "", fmt.Errorf("tmux display-message gave %q for %s", strings.TrimSpace(string(out)), target)
	}
	if f[1] != m[1] || f[2] != m[2] && f[3] != m[2] || m[3] != "" && f[4] != m[3] {
		return "", fmt.Errorf("tmux takes %s as %s:%s.%s (%s), not that pane", target, f[1], f[2], f[4], f[0])
	}
	if f[5] != "0" {
		return "", fmt.Errorf("pane %s is in copy mode: keys would go there", target)
	}
	return f[0], nil
}

var paneIDRe = regexp.MustCompile(`^%\d+$`)

// read captures the pane and gives the typed text in its box, how often the start of text shows on screen, and the
// screen as plain text; a pane without a box is an error, since Enter would run or pick something there.
func read(run Runner, target, text string) (typed string, seen int, screen string, err error) {
	out, err := run("tmux capture-pane -t " + target + " -p -e")
	if err != nil {
		said, _ := redact.Redact(strings.TrimSpace(string(out)))
		return "", 0, "", fmt.Errorf("tmux capture-pane: %v: %s", err, said)
	}
	typed, boxed := Box(string(out))
	if !boxed {
		return "", 0, "", fmt.Errorf("pane %s shows no Claude input box", target)
	}
	// Dim text counts here: a message queued in a busy tab may be drawn dim.
	screen = plain(string(out))
	return typed, onScreen(screen, text), screen, nil
}

// paste puts text with line breaks into the box as one bracketed paste (tmux paste-buffer -p), where a line break is a
// new line and never Enter; Claude Code shows a long one as "[Pasted text #1 +N lines]" (2.1.326, 11-17). lit is
// text with a trailing ";" escaped, which tmux would take as the end of set-buffer.
func paste(run Runner, pane, lit string) error {
	buf := "crius-tsend-" + strings.TrimPrefix(pane, "%") // one per pane, so a send to another pane never takes it
	for _, cmd := range []string{"tmux set-buffer -b " + buf + " -- " + quote(lit), "tmux paste-buffer -p -d -b " + buf + " -t " + quote(pane)} {
		if out, err := run(cmd); err != nil {
			return fmt.Errorf("%s: %v: %s", strings.Fields(cmd)[1], err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// waitBox reads the pane until it is as wanted, for up to 10 reads (Claude draws a key a moment after it arrives),
// and gives the last read.
func waitBox(run Runner, target, text string, wanted func(typed string, seen int, screen string) bool) (typed string, seen int, screen string, err error) {
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(Wait)
		}
		if typed, seen, screen, err = read(run, target, text); err == nil && wanted(typed, seen, screen) {
			return typed, seen, screen, nil
		}
	}
	return typed, seen, screen, err
}

// shown is what an error may say about a box: redacted, on one line, cut to 60 runes.
func shown(typed string) string {
	text, _ := redact.Redact(typed)
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 60 {
		text = string(r[:60]) + "…"
	}
	return text
}

func refuse(target, text string) string {
	lead := strings.TrimSpace(text)
	switch {
	case !targetRe.MatchString(target):
		return fmt.Sprintf("tmux target %q: give session:window or session:window.pane, by name or index", target)
	case strings.ContainsRune(text, '\r'):
		return "a carriage return would press Enter early: break lines with \\n"
	case strings.IndexFunc(text, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }) >= 0:
		return "a control character (Esc, Tab, C-c …) would act as a key in the box: send plain text"
	case lead == "":
		return "the text is empty"
	case strings.HasPrefix(lead, "!") || strings.HasPrefix(lead, "#"):
		return "a leading ! or # switches Claude Code's box to shell or memory mode: start with a word"
	}
	return ""
}

// ClearRounds is how many rounds Send spends clearing a box (one line each) before it gives up.
const ClearRounds = 20

// Wait is the pause between two reads of the pane. Tests set it to 0.
var Wait = 300 * time.Millisecond

// Box gives the typed text in the Claude Code input box of a pane as `tmux capture-pane -p -e` prints it, and whether
// the pane shows a box. Dim text (Claude Code's grey placeholder or suggestion) is not typed text, so an empty box that
// shows a suggestion gives "".
func Box(screen string) (typed string, boxed bool) {
	return wakewatch.Prompt(undimmed(screen))
}

// undimmed drops the escape sequences from a pane capture and the text drawn dim (SGR 2) with them.
func undimmed(s string) string { return strip(s, false) }

// plain drops the escape sequences from a pane capture and keeps all its text.
func plain(s string) string { return strip(s, true) }

func strip(s string, keepDim bool) string {
	var b strings.Builder
	dim := false
	r := []rune(s)
	for i := 0; i < len(r); i++ {
		if r[i] != 0x1b {
			if keepDim || !dim || r[i] == '\n' {
				b.WriteRune(r[i])
			}
			continue
		}
		if i+1 >= len(r) {
			break
		}
		switch r[i+1] {
		case '[': // CSI: parameters, then a final byte in @..~
			j := i + 2
			for j < len(r) && (r[j] < 0x40 || r[j] > 0x7e) {
				j++
			}
			if j < len(r) && r[j] == 'm' {
				dim = sgrDim(string(r[i+2:j]), dim)
			}
			i = j
		case ']': // OSC (a hyperlink): to BEL or ESC \
			j := i + 2
			for j < len(r) && r[j] != 0x07 && !(r[j] == 0x1b && j+1 < len(r) && r[j+1] == '\\') {
				j++
			}
			if j < len(r) && r[j] == 0x1b {
				j++
			}
			i = j
		case '(', ')', '*', '+': // a character set: one more byte
			i += 2
		default:
			i++
		}
	}
	return b.String()
}

// sgrDim applies one SGR parameter list to the dim flag: 2 sets it, 0 (or none) and 22 clear it. The colour forms 38,
// 48 and 58 carry their own numbers (5;n or 2;r;g;b), which are skipped so a 2 in them is not dim.
func sgrDim(params string, dim bool) bool {
	ps := strings.FieldsFunc(params, func(r rune) bool { return r == ';' || r == ':' })
	if len(ps) == 0 {
		return false
	}
	for i := 0; i < len(ps); i++ {
		switch ps[i] {
		case "0", "00", "22":
			dim = false
		case "2", "02":
			dim = true
		case "38", "48", "58":
			if i+1 < len(ps) && ps[i+1] == "5" {
				i += 2
			} else if i+1 < len(ps) && ps[i+1] == "2" {
				i += 4
			}
		}
	}
	return dim
}

// end is what shows text on screen: its last 40 runes without white space, enough to know it. The end, since a long
// text's first lines scroll off once it is sent, and it sits just above the reply (11-17 review: a short last line
// alone, such as "Go.", can show on screen more often before Enter than after).
func end(text string) string {
	r := []rune(squash(text))
	if len(r) > 40 {
		r = r[len(r)-40:]
	}
	return string(r)
}

// onScreen is how often text shows on a plain screen: white space and the │ some screens draw at each end of each sent
// line are dropped, so a line the screen wrapped or framed reads as typed.
func onScreen(screen, text string) int {
	return strings.Count(strings.ReplaceAll(squash(screen), "│", ""), end(text))
}

// squash drops all white space, so a line the box wrapped equals the line as typed.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// quote makes s one word for a POSIX shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Visible gives a `tmux capture-pane -p -e` screen as plain text without the text drawn dim, so an empty box that shows
// Claude Code's grey suggestion reads as empty.
func Visible(screen string) string { return undimmed(screen) }
