// Package fakeprompt is a stand-in for Claude Code's input box, for tests: input that arrives in one read with other
// keys is a paste, where Enter is a new line; an Enter that arrives alone sends the box. Empty, the box shows a dim
// placeholder, as Claude Code does. The tabsend tests drive it in process; cmd fakeprompt runs it in a real tmux pane.
package fakeprompt

import (
	"strings"
)

// Placeholder is the dim suggestion an empty box shows.
const Placeholder = `Try "fix lint errors"`

// Model is the box and what it has sent.
type Model struct {
	Lines []string // the draft, one entry per line in the box; never empty
	Sent  []string // drafts sent by an Enter
	Busy  bool     // a turn is running: a sent draft is queued, not answered
}

// New gives an empty box.
func New() *Model { return &Model{Lines: []string{""}} }

// Feed takes the bytes of one read from the terminal.
func (m *Model) Feed(chunk []byte) {
	if string(chunk) == "\r" || string(chunk) == "\n" {
		m.submit()
		return
	}
	paste := len(chunk) > 1
	for _, r := range string(chunk) {
		last := len(m.Lines) - 1
		switch {
		case r == '\r' || r == '\n':
			if paste {
				m.Lines = append(m.Lines, "")
			} else {
				m.submit()
			}
		case r == 0x15: // C-u: delete to line start; at a line start it does nothing (Claude Code, seen 11-15 04:27)
			m.Lines[last] = ""
		case r == 0x05: // C-e: the cursor is always at the end
		case r == 0x7f || r == 0x08:
			if l := []rune(m.Lines[last]); len(l) > 0 {
				m.Lines[last] = string(l[:len(l)-1])
			} else if last > 0 {
				m.Lines = m.Lines[:last]
			}
		case r >= 0x20:
			m.Lines[last] += string(r)
		}
	}
}

func (m *Model) submit() {
	draft := strings.Join(m.Lines, "\n")
	if strings.TrimSpace(draft) == "" {
		return
	}
	m.Sent = append(m.Sent, draft)
	m.Lines = []string{""}
}

// Screen draws the pane as tmux capture-pane -p -e gives it: what was sent above, the box between two rules, and a
// status line under it.
func (m *Model) Screen() string {
	rule := "\x1b[38;5;244m" + strings.Repeat("─", 60) + "\x1b[39m"
	var b strings.Builder
	for _, s := range m.Sent {
		b.WriteString("❯ " + strings.ReplaceAll(s, "\n", "\n  ") + "\n")
		if m.Busy {
			b.WriteString("  (queued)\n")
		} else {
			b.WriteString("⏺ got it\n")
		}
		b.WriteString("\n")
	}
	if m.Busy {
		b.WriteString("\x1b[38;5;174m✻\x1b[39m Thinking… (esc to interrupt)\n")
	}
	b.WriteString(rule + "\n")
	if len(m.Lines) == 1 && m.Lines[0] == "" {
		b.WriteString("❯ \x1b[2m" + Placeholder + "\x1b[0m\n")
	} else {
		for i, l := range m.Lines {
			if i == 0 {
				b.WriteString("❯ " + l + "\n")
			} else {
				b.WriteString("  " + l + "\n")
			}
		}
	}
	b.WriteString(rule + "\n")
	b.WriteString("  \x1b[38;5;246mfake-prompt │ main\x1b[39m\n")
	return b.String()
}
