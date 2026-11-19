package wakewatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ownerqueue"
)

// RemoteAsk gives a remote tab's last reply when the tab sits at its input box, not working, and that reply asks Crius
// or the owner something: ownerqueue.Asks (its last status emoji is ❓, an ask label such as "Needs you: …", or, with
// no status emoji, a last line that is a question), and its last status emoji is not ⏳ or ✅. A dialog is the remote
// prompt check's, and a provider error the restart's; both are read before this.
func RemoteAsk(text string) (string, bool) {
	body, foot, boxed := split(text)
	if !boxed || spinning(text) || strings.Contains(foot, "esc to interrupt") {
		return "", false
	}
	reply := lastReply(body)
	if reply == "" || !ownerqueue.Asks(reply) {
		return "", false
	}
	if s := ownerqueue.Status(reply); s == "⏳" || s == "✅" {
		return "", false
	}
	return reply, true
}

// askLine gives the line of an asking reply to quote: the last one with a ❓, else the last question, else the last
// ask label, else the last line; cut to 160 characters.
func askLine(reply string) string {
	lines := strings.Split(reply, "\n")
	pick := ""
	for _, match := range []func(string) bool{
		func(l string) bool { return strings.Contains(l, "❓") },
		func(l string) bool { return strings.HasSuffix(strings.TrimRight(l, "* "), "?") },
		func(l string) bool { return ownerqueue.AskLabelRe.MatchString(l) },
		func(l string) bool { return true },
	} {
		for i := len(lines) - 1; i >= 0 && pick == ""; i-- {
			if l := strings.TrimSpace(lines[i]); l != "" && match(l) {
				pick = l
			}
		}
		if pick != "" {
			break
		}
	}
	if r := []rune(pick); len(r) > 160 {
		pick = string(r[:160]) + "…"
	}
	return pick
}

// askKey is what an ask is known by: the reply up to its ask line, white space folded, so a rewrap or a notice Claude
// Code prints under the reply ("✔ Update installed · restart to apply") keeps it.
func askKey(reply, ask string) string {
	if i := strings.LastIndex(reply, strings.TrimSuffix(ask, "…")); i >= 0 {
		reply = reply[:i+len(strings.TrimSuffix(ask, "…"))]
	}
	return strings.Join(strings.Fields(reply), " ")
}

// askAlert gives the wake for a remote tab's ask open since since, or, when the role posted a NEED, NEED-<X> or ASK line
// on its bus in that turn (from purposeTurn before the ask was first seen), quiet: what that line was. A bus that cannot
// be checked still wakes Crius and says so.
func askAlert(home string, t RemoteTab, screen, ask string, since, now time.Time) (text, quiet string) {
	who, _ := session(screen, "")
	if who == "" {
		who = strings.ToLower(t.Target)
	}
	from := since.Add(-purposeTurn)
	text = fmt.Sprintf("question unanswered for %d min in tab %s: first seen at %s, its last reply ends %q",
		int(now.Sub(since).Minutes()), t, since.Local().Format("15:04"), ask)
	typ, at, found, err := askedOnBus(home, who, from, now)
	switch {
	case err != nil:
		return text + "; its bus was not checked: " + err.Error(), ""
	case !found:
		return text + "; its bus was not checked: no bus log for " + project(who) + " here", ""
	case typ != "":
		return "", fmt.Sprintf("%s posted %s on the bus at %s", who, typ, at.Local().Format("15:04"))
	}
	return text + fmt.Sprintf("; no NEED, NEED-OWNER or ASK line from %s on the bus since %s", who, from.Local().Format("15:04")), ""
}

// askedOnBus gives the type and time of a line from who on its project's bus at or after from (to the minute: bus
// stamps hold no seconds) that already put its ask before Crius or the owner, "" when there is none: a NEED-OWNER (the
// owner queue takes every one, until a "[queue qN]" line from Crius closes its row), or a NEED, NEED-<X> or ASK sent to
// Crius (what wakes Crius: missed) that no later line from crius:lead to who answered. A NEED to another role
// (quotes:dev -> quotes:qa) reaches neither. found is false when the project has no bus log here.
func askedOnBus(home, who string, from, now time.Time) (typ string, at time.Time, found bool, err error) {
	lines, found, err := busLines(home, who)
	if err != nil || !found {
		return "", time.Time{}, found, err
	}
	from = from.Truncate(time.Minute)
	team := project(who)
	for _, m := range lines {
		if busName(team, m[3]) == "crius:lead" && addressed(m[4], who, team) {
			// answered: it speaks for no ask after it; an owner-queue row stays open until a "[queue qN]" line closes it
			if typ != "NEED-OWNER" || strings.HasPrefix(strings.TrimSpace(m[5]), "[queue ") {
				typ, at = "", time.Time{}
			}
			continue
		}
		if busName(team, m[3]) != who || !(m[2] == "NEED-OWNER" || (m[2] == "NEED" || m[2] == "ASK" || strings.HasPrefix(m[2], "NEED-")) && toCrius(m[4])) {
			continue
		}
		if t, err := lineTime(m[1], now); err == nil && !t.Before(from) {
			typ, at = m[2], t
		}
	}
	return typ, at, true, nil
}
