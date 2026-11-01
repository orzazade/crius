package wakewatch

import (
	"errors"
	"regexp"
	"strings"
)

var (
	// wait-sweep.sh's rule (~/.claude/hooks): a numbered option, the "❯" cursor on one, a rule or box border, a dropped
	// prompt's notice. A space may be drawn as a no-break space, which perl's \s takes and Go's does not (review of X5);
	// these scan only below the idle input box, whose "❯" a no-break space follows.
	sweepOptRe     = regexp.MustCompile(`^[\s\x{a0}│]*(?:❯[\s\x{a0}]*)?(\d+)\.[\s\x{a0}]`)
	sweepCursorRe  = regexp.MustCompile(`^[\s\x{a0}│]*❯[\s\x{a0}]*\d+\.[\s\x{a0}]`)
	sweepFirstRe   = regexp.MustCompile(`^[\s\x{a0}│]*(?:❯[\s\x{a0}]*)?1\.[\s\x{a0}]`)
	sweepBorderRe  = regexp.MustCompile(`^[\s\x{a0}│]*[─╌╭╰┌└]`)
	sweepDroppedRe = regexp.MustCompile(`^[\s\x{a0}│⎿⏺✗✘⚠•·*-]*Prompt dropped by a hook`)
	// The turn's last reply line, "⏺ text" or a result under it, "⎿  text"; Claude Code's error line in it
	replyRe    = regexp.MustCompile(`^\s*[⏺⎿]\s*(.*)$`)
	apiErrorRe = regexp.MustCompile(`^API Error(?::|\s*\()`)
)

// LivePrompt says whether a tab's screen still shows a prompt waiting on an answer (Crius 11-01 22:09: Crius relayed
// waits whose prompt was gone; a waiter armed late hands over lines minutes old). It keeps a prompt when any rule sees
// one: wait-sweep.sh's (a menu from "1." with the "❯" cursor on any option, under a question ending in "?", or a
// dropped prompt's notice, below the idle input box, in the last 15 non-blank lines), the owner-click dialog, and
// the dialog rule of the tab check.
func LivePrompt(text string) bool {
	if _, _, ok := OwnerClick(text); ok || dialogRe.MatchString(text) {
		return true
	}
	var l []string
	for _, x := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if strings.TrimSpace(x) != "" {
			l = append(l, x)
		}
	}
	idle := -1 // the idle input box: "❯" right under a "─" rule
	for i := 1; i < len(l); i++ {
		if strings.HasPrefix(l[i], "❯") && strings.HasPrefix(l[i-1], "─") {
			idle = i
		}
	}
	for i := max(idle+1, len(l)-15); i < len(l); i++ {
		if sweepDroppedRe.MatchString(l[i]) {
			return true
		}
		m := sweepOptRe.FindStringSubmatch(l[i])
		if i == 0 || m == nil || m[1] != "1" || sweepOptRe.MatchString(l[i-1]) || !strings.HasSuffix(sweepClean(l[i-1]), "?") {
			continue
		}
		for j := i; j < len(l) && j <= i+10; j++ {
			if j > i && (sweepBorderRe.MatchString(l[j]) || sweepFirstRe.MatchString(l[j]) || strings.HasSuffix(sweepClean(l[j]), "?")) {
				break
			}
			if sweepCursorRe.MatchString(l[j]) {
				return true
			}
		}
	}
	return false
}

func sweepClean(s string) string {
	return strings.Trim(s, " \t │")
}

// apiStop gives the line a turn ended on when it is an API error (Crius 11-01 22:09: quotes · backend-orders sat dead
// on one, unseen): its last reply line above the input box, with no working line after it. A retry ("API Error (…) ·
// Retrying in 5 seconds") is still working. A ❓ above it was asked before the turn that died (review of X5), one after
// it ends an ordinary turn that only mentions an error (review r2), and a dialog hides the input box.
func apiStop(text string) (string, bool) {
	body, _, boxed := split(text)
	if !boxed || spinning(text) || strings.Contains(text, "esc to interrupt") {
		return "", false
	}
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := replyRe.FindStringSubmatch(lines[i]); m != nil {
			t := strings.TrimSpace(m[1])
			return t, apiErrorRe.MatchString(t) && !strings.Contains(t, "Retrying in") && !strings.ContainsAny(strings.Join(lines[i:], "\n"), "❓✅⏳")
		}
	}
	return "", false
}

// Gone says whether a tab no longer shows a prompt (crius-wakewatch gone, which team-wait.sh asks right before it hands
// Crius a wait line); read gives the tab as deep as the clicks run reads it. A tab that cannot be read is not gone: the
// line goes to Crius. Nor is one read with neither the input box nor a prompt, empty or half drawn (review r3 of X5).
func Gone(read func() (string, error)) (bool, error) {
	text, err := read()
	if err != nil {
		return false, err
	}
	if LivePrompt(text) {
		return false, nil
	}
	if _, _, boxed := split(text); !boxed {
		return false, errors.New("the tab shows neither the input box nor a prompt")
	}
	return true, nil
}
