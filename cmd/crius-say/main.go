// Command crius-say types a message into a Claude tab by its title:
//
//	crius-say <tab title> <message…>
//
// The message is stored first in $CRIUS_HOME/outbox and stays there until the tab's box has taken it. Text someone
// typed in the box holds it, and crius-wakewatch types it on a later run, once the box is empty. It exits 0 when the
// message was sent, 3 when it must not be sent again (held for wakewatch, or typed onto other text and set aside), and 1
// when it was not stored: only then may the caller send it again.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: crius-say <tab title> <message…>")
		os.Exit(2)
	}
	err := say(store.Home(), os.Args[1], strings.Join(os.Args[2:], " "))
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-say:", err)
	}
	os.Exit(exitCode(err))
}

// say types text as one line, cleaned as wakewatch cleans its alerts: a line break would press Enter early. Text the
// cleaning would change in meaning (cut at MaxLine runes, a backslash turned into "/") is refused, not changed. The
// length counts the text with its secrets masked, as Clean cuts it: a mask can be longer than the secret.
func say(home, to, text string) error {
	if masked, _ := redact.Redact(text); len([]rune(masked)) > wakewatch.MaxLine {
		return fmt.Errorf("not sent: %d runes with secrets masked, at most %d go in one line", len([]rune(masked)), wakewatch.MaxLine)
	}
	if strings.Contains(text, `\`) {
		return fmt.Errorf(`not sent: cmux reads a backslash as a key (\n is Enter), so the text cannot hold one`)
	}
	text = wakewatch.Clean(text)
	if text == "" {
		return fmt.Errorf("nothing to say to %q", to)
	}
	return outbox.Send(home, to, text)
}

// exitCode tells the caller whether to send again: 3 means the message is stored, in the box, or may have gone, 1 that
// it is none of these.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, outbox.ErrHeld), errors.Is(err, outbox.ErrOnto), errors.Is(err, outbox.ErrMaybeSent):
		return 3
	}
	return 1
}
