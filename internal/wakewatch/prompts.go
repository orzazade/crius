package wakewatch

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/watchdog"
)

// The prompt watch (owner 11-08: "make sure you have a watchdog for your own sessions, and they have for the rest"). On
// 11-07 from 20:00 crius:ops sat on its own permission prompt, so it answered no one's, and 10 tabs waited until the owner
// noticed: their CRIUS-CLICK lines went to Crius's digest, and the tab check's dialog alert is muted for those. Every
// minute (the clicks job) each tab with an open dialog is timed:
//   - Crius's own tabs (crius:ops, crius:build): open ownAfter wakes Crius, naming the tab and the command;
//   - every other tab: open opsAfter is typed into crius:ops's tab (not a bus line), and still open criusAfter wakes
//     Crius too, so one stuck ops never blocks everyone again;
//   - crius:ops with no live Claude wakes Crius at once; while it is on a prompt, or its tab cannot be typed into, the
//     prompts it would get go to Crius at once instead.
//
// Crius's own tab is the owner's: on a prompt, with no input box while nothing runs, or with no live Claude for
// criusAfter, it pushes the owner a desktop alert, never a line typed into that tab (11-10: Crius froze 4h10 while its
// wakes were typed into it). Each wake and push is said again every clickRepeat while it lasts.
const (
	ownAfter   = 3 * time.Minute
	opsAfter   = 2 * time.Minute
	criusAfter = 10 * time.Minute
	OpsName    = "crius:ops"
	opsTitle   = "crius · ops"
)

// PromptDeps is what one prompt-watch run needs. SendCrius types a line into Crius's tab, SendTab into the tab with this
// title; HandedOver says a send error still hands the text over (stored to be typed later). Live says why the tab has
// no live Claude in front, nil when it has.
type PromptDeps struct {
	Home       string
	Now        time.Time
	Screens    func() ([]Screen, error)
	SendCrius  func(text string) error
	SendTab    func(title, text string) error
	HandedOver func(err error) bool
	Live       func(surface string) error
	// Push sends the owner a desktop alert pointing at a tab (Crius's own tab stuck: a line typed into it reaches no one)
	Push func(surface, title, body string) error
	// CriusHeld gives, while Crius runs in a T3 Code thread, when the oldest message the outbox tried and failed to hand
	// it was stored and why it failed; a zero time when none is held.
	CriusHeld func() (since time.Time, why string, err error)
}

// openPrompt is one tab's open dialog: its hash, when first seen, and when crius:ops and Crius were last told of it.
type openPrompt struct {
	Hash              string
	Since, Ops, Crius time.Time
}

type promptState struct {
	Tabs      map[string]openPrompt
	DownSince time.Time // when crius:ops was first seen with no live Claude in this spell; zero while it has one
	OpsDown   time.Time // when Crius was last told so
	Crius     stuck     // Crius's own tab, while it is stuck
	T3        string    // the T3 Code thread Crius runs in, once its INFO is posted ("" while Crius is in a cmux tab)
}

// stuck is Crius's own tab while it cannot go on by itself: since when, and when the owner was last pushed. The timer
// runs through changes of form (one prompt to the next, a failed liveness read: review P3-2) and ends only when the
// tab is seen free.
type stuck struct {
	Since, Pushed time.Time
	Row           bool // a failed push opened the owner's row in this spell: one row per spell, not one per 30 min
}

// Prompts runs the prompt watch once. A run that fails says so on the bus as an ALERT, once per error.
func Prompts(d PromptDeps) error {
	return alertOnce(d.Home, d.Now, "prompts.error", "prompt watch failed: ", prompts(d))
}

// queued is one prompt for crius:ops: its tab and how it is named in the line.
type queued struct{ surface, text string }

func prompts(d PromptDeps) error {
	path := filepath.Join(d.Home, "state", "prompts.json")
	st := promptState{}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &st) != nil {
			st = promptState{} // afresh: each open prompt is timed again, never silenced
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if st.Tabs == nil {
		st.Tabs = map[string]openPrompt{}
	}
	screens, err := d.Screens()
	if err != nil {
		return err
	}
	// projects.toml unread: no session is known to be Crius's, so every Azure DevOps ask is the owner's (Clicks says why)
	crius, _ := criusRuns(d.Home)
	// crius:ops's tab: by its title, else by the name it draws (review of 42281dd: a pasted border is no tab)
	var ops *Screen
	for i := range screens {
		if screens[i].Title == opsTitle {
			ops = &screens[i]
			break
		}
	}
	for i := range screens {
		if ops == nil && BusName(tabName(screens[i])) == OpsName {
			ops = &screens[i]
		}
	}
	down := ""
	switch {
	case ops == nil:
		down = "no tab is named " + opsTitle
	case ops.Err != "":
		down = "its tab cannot be read: " + ops.Err
	default:
		if err := d.Live(ops.Surface); err != nil {
			down = err.Error()
		}
	}
	var toCrius, late, held []string
	var toOps []queued
	// a dead ops wakes Crius once it is seen so on two runs in a row: one failed read or cmux call is no restart (review)
	if down == "" {
		st.DownSince, st.OpsDown = time.Time{}, time.Time{}
	} else if st.DownSince.IsZero() {
		st.DownSince = d.Now
	} else if d.Now.Sub(st.OpsDown) >= clickRepeat {
		toCrius = append(toCrius, fmt.Sprintf("%s has no live Claude (%s), so nobody answers the other tabs' prompts: start it again", OpsName, down))
		st.OpsDown = d.Now
	}
	blocked := down
	if blocked != "" {
		blocked = "it has no live Claude: " + down
	} else if _, open := openDialog(ops.Text); open {
		blocked = "it is on a prompt of its own"
	}
	// Crius in a T3 Code thread has no cmux tab: the tab check is left out, said once per thread id as an INFO, and the
	// thread is checked by what the outbox could not hand it instead.
	var pushTab, what string
	var infoErr error
	switch thread, terr := CriusThread(d.Home); {
	case terr != nil:
		st.T3 = ""
		what = "Crius's T3 Code thread cannot be named, so its messages are held: " + terr.Error()
	case thread != "":
		if st.T3 != thread {
			infoErr = watchdog.PostOn(d.Home, "crius", "INFO", "crius:wakewatch", "crius:lead", d.Now, "Crius runs in T3 Code thread "+
				thread+" ("+CriusThreadFile(d.Home)+"): the checks of a cmux tab titled "+CriusTitle+" are off while that file exists")
			if infoErr == nil {
				st.T3 = thread
			}
		}
		what = criusThreadStuck(d, thread)
	default:
		st.T3 = ""
		pushTab, what = criusStuck(d, screens)
	}
	push := stuckPush(d, &st, what)
	next := map[string]openPrompt{}
	for _, s := range screens {
		if s.Err != "" {
			if p, ok := st.Tabs[s.Surface]; ok {
				next[s.Surface] = p
			}
			continue
		}
		prompt, open := openDialog(s.Text)
		who := BusName(tabName(s))
		// Crius's own tab and an Azure DevOps ask only the owner may answer (OWNER-CLICK) are the owner's; a dialog on a tab
		// whose Claude has gone is its last frame, which nobody can answer (review of 42281dd)
		if !open || who == "crius:lead" || ownerOnly(s, crius) || d.Live(s.Surface) != nil {
			continue
		}
		p := st.Tabs[s.Surface]
		if h := hash(promptKey(prompt)); p.Hash != h {
			p = openPrompt{Hash: h, Since: d.Now}
		}
		age := d.Now.Sub(p.Since)
		name := fmt.Sprintf("%s (%s, %d min", tabName(s), s.Surface, int(age.Minutes()))
		switch {
		case who == OpsName || who == "crius:build":
			if age >= ownAfter && d.Now.Sub(p.Crius) >= clickRepeat {
				toCrius = append(toCrius, fmt.Sprintf("%s (%s) has had a permission prompt open for %d min, and only you answer its prompts: %s",
					tabName(s), s.Surface, int(age.Minutes()), command(s.Text, prompt)))
				p.Crius = d.Now
			}
		// told ops, and Crius has not heard of it since (a wake while ops was blocked came before)
		case age >= criusAfter && !p.Ops.IsZero() && (p.Crius.Before(p.Ops) || d.Now.Sub(p.Crius) >= clickRepeat):
			late = append(late, name+"; "+OpsName+" told "+p.Ops.Format("15:04")+")")
			p.Crius = d.Now
		case age >= opsAfter && p.Ops.IsZero() && blocked != "":
			if d.Now.Sub(p.Crius) >= clickRepeat {
				held = append(held, name+")")
				p.Crius = d.Now
			}
		case age >= opsAfter && p.Ops.IsZero():
			toOps = append(toOps, queued{s.Surface, name + ")"})
			p.Ops = d.Now // a line that fails takes it back below
		}
		next[s.Surface] = p
	}
	st.Tabs = next
	if len(late) > 0 {
		toCrius = append(toCrius, pack("prompts still open 10+ min, and "+OpsName+" has not answered them: ", late)...)
	}
	if len(held) > 0 {
		toCrius = append(toCrius, pack(OpsName+" cannot answer prompts now ("+blocked+"): ", held)...)
	}
	// The state goes first: a wake that then fails is posted as an ALERT, and a state that cannot be written stops the
	// run before anything is typed, so nothing is typed again every minute.
	if err := writeJSON(path, st); err != nil {
		return err
	}
	var pushErr error
	if push != "" {
		// the owner is the only one who can free Crius's tab. A push that fails becomes a NEED-OWNER line, a row in the
		// owner's "Waiting on you" pane (an ALERT would go to the stuck Crius's own waiter: review P2-2); the wakes below
		// still go out whatever happens here (P2-3: the state already says they were sent)
		err := fmt.Errorf("no push is set up")
		if d.Push != nil {
			err = d.Push(pushTab, "Crius is stuck", Clean(push))
		}
		if err != nil && !st.Crius.Row {
			// the reason first (a line is cut at 300 runes), and no "[queue qN]" from the stuck command: the owner queue
			// reads a tagged NEED-OWNER line as Crius closing a row and opens none (review 2, P3-2)
			text := "desktop push failed (" + err.Error() + "): " + queueTag.ReplaceAllString(push, "(queue $1)")
			if perr := watchdog.PostOn(d.Home, "crius", "NEED-OWNER", "crius:watchdog", "owner", d.Now, text); perr != nil {
				pushErr = fmt.Errorf("push to the owner failed: %v; and the NEED-OWNER line was not posted: %v", err, perr)
			} else {
				st.Crius.Row = true
				if werr := writeJSON(path, st); werr != nil {
					pushErr = werr
				}
			}
		}
	}
	if len(toOps) > 0 {
		var failed []string
		var why error
		for _, l := range packQueued("prompts open 2+ min for you to answer: ", toOps) {
			err := d.SendTab(ops.Title, Prefix+Clean(l.text))
			if err == nil || d.HandedOver != nil && d.HandedOver(err) { // held: typed by a later run
				continue
			}
			why = cmp.Or(why, err)
			for _, q := range l.items { // not stored: tried again next run, and Crius hears of it once per clickRepeat
				p := st.Tabs[q.surface]
				p.Ops = time.Time{}
				if d.Now.Sub(p.Crius) >= clickRepeat {
					p.Crius = d.Now
					failed = append(failed, q.text)
				}
				st.Tabs[q.surface] = p
			}
		}
		if why != nil {
			if err := writeJSON(path, st); err != nil {
				return errors.Join(pushErr, err)
			}
		}
		if len(failed) > 0 {
			toCrius = append(toCrius, pack(OpsName+" cannot answer prompts now (typing into its tab failed: "+why.Error()+"): ", failed)...)
		}
	}
	for _, line := range toCrius {
		text := Clean(line)
		// Crius's waiter wakes on an ALERT line: a wake that cannot be typed, nor held to be typed later, goes there
		if err := d.SendCrius(Prefix + text); err != nil && (d.HandedOver == nil || !d.HandedOver(err)) {
			if perr := watchdog.Post(d.Home, d.Now, text); perr != nil {
				return errors.Join(pushErr, fmt.Errorf("%v; and the ALERT was not posted: %v", err, perr))
			}
		}
	}
	return errors.Join(pushErr, infoErr)
}

// criusStuck says what keeps Crius's own tab stuck, with the tab's surface ("" when no tab is found); "" when it is
// free. Stuck is: no tab titled crius, a tab that cannot be read, no live Claude in it, a dialog open, or no input box
// while no working line shows (the transcript view or a tall draft hides the box while Claude works: review P2-1).
func criusStuck(d PromptDeps, screens []Screen) (surface, what string) {
	var tab *Screen
	for i := range screens {
		if screens[i].Title == CriusTitle { // by title, as outbox types into it: a dialog of another tab quoted on Crius's
			tab = &screens[i] // screen names that tab on its border (review 2, P2-1)
			break
		}
	}
	form := ""
	if tab == nil {
		form = "is not found (no cmux tab titled " + CriusTitle + ")"
	} else {
		surface = tab.Surface
		prompt, open := openDialog(tab.Text)
		_, _, boxed := split(tab.Text)
		var dead error
		if tab.Err == "" {
			dead = d.Live(surface) // the screen cannot say: a Claude that exited leaves its last frame, box and all
		}
		switch {
		case tab.Err != "":
			form = "(" + surface + ") cannot be read (" + tab.Err + ")"
		case dead != nil:
			form = "(" + surface + ") has no live Claude (" + dead.Error() + ")"
		case open:
			form = "(" + surface + ") has a prompt open: " + command(tab.Text, prompt)
		case !boxed && !working(tab.Text):
			form = "(" + surface + ") shows no input box and no working line"
		}
	}
	if form == "" {
		return "", ""
	}
	return surface, "Crius's own tab " + form
}

// criusThreadStuck says what keeps Crius's T3 Code thread from its messages: one the outbox tried to hand it and still
// holds (t3 failed: the app is closed, the link to t3 is broken, the thread is gone), or an outbox it cannot read; ""
// when nothing is held.
func criusThreadStuck(d PromptDeps, thread string) string {
	if d.CriusHeld == nil {
		return ""
	}
	since, why, err := d.CriusHeld()
	switch {
	case err != nil:
		return "Crius's outbox cannot be read, so its T3 Code thread " + thread + " is not checked: " + err.Error()
	case since.IsZero():
		return ""
	}
	return fmt.Sprintf("Crius's T3 Code thread %s has not taken a message held since %s (%s)", thread, since.Format("15:04"), why)
}

// stuckPush times what keeps Crius stuck ("" when it is free) and gives the push for the owner when it has lasted
// criusAfter, again every clickRepeat; "" otherwise. The timer runs through changes of form and ends only when Crius is
// seen free.
func stuckPush(d PromptDeps, st *promptState, what string) string {
	if what == "" {
		st.Crius = stuck{}
		return ""
	}
	if st.Crius.Since.IsZero() {
		st.Crius.Since = d.Now
	}
	age := d.Now.Sub(st.Crius.Since)
	if age < criusAfter || d.Now.Sub(st.Crius.Pushed) < clickRepeat {
		return ""
	}
	st.Crius.Pushed = d.Now
	return fmt.Sprintf("%s: stuck %d min; only you can free it", what, int(age.Minutes()))
}

// queueTag is the owner queue's row tag, "[queue q3]".
var queueTag = regexp.MustCompile(`\[queue (q\d+)\]`)

// working says whether Claude Code's working line ("✻ Burrowing… (9m 43s · ↓ 17.1k tokens)") is among a screen's last
// 20 lines, with or without an input box under it.
func working(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, l := range lines[max(0, len(lines)-20):] {
		if spinRe.MatchString(l) {
			return true
		}
	}
	return false
}

// ownerOnly says whether a tab's prompt is an Azure DevOps ask only the owner may answer: the ones Clicks posts as
// OWNER-CLICK, not CRIUS-CLICK.
func ownerOnly(s Screen, crius map[string]bool) bool {
	_, _, ok := OwnerClick(s.Text)
	return ok && !criusClick(s, crius)
}

// criusClick says whether a tab's Azure DevOps ask is Crius's to answer: the name its dialog's border draws (never the
// tab title, review r2) is a project Crius runs, and not Crius's own tab, which its dialog blocks (review of 2ae5117).
func criusClick(s Screen, crius map[string]bool) bool {
	who := BusName(dialogBorder(s))
	return who != "crius:lead" && crius[project(who)]
}

// command gives what a prompt asks to run: the dialog's text up to the hook's reason or the question, cut short, and the
// post-guard's reason when it is the post-guard's ask.
func command(text, prompt string) string {
	cmd := strings.TrimPrefix(prompt, "Bash command ")
	for _, end := range []string{" Hook PreToolUse", " Do you want"} {
		if i := strings.Index(cmd, end); i >= 0 {
			cmd = cmd[:i]
		}
	}
	if r := []rune(cmd); len(r) > 200 {
		cmd = string(r[:199]) + "…"
	}
	if _, what, ok := OwnerClick(text); ok {
		cmd += " — " + what
	}
	return cmd
}

// pack puts items after head, as few lines as fit MaxLine each.
func pack(head string, items []string) []string {
	q := make([]queued, len(items))
	for i, it := range items {
		q[i].text = it
	}
	var out []string
	for _, l := range packQueued(head, q) {
		out = append(out, l.text)
	}
	return out
}

// packedLine is one line of packed prompts and the prompts in it.
type packedLine struct {
	text  string
	items []queued
}

// packQueued is pack that keeps which prompts went into each line.
func packQueued(head string, items []queued) []packedLine {
	var out []packedLine
	var cur packedLine
	for _, it := range items {
		if len(cur.items) > 0 && len([]rune(head+cur.text+"; "+it.text)) > MaxLine {
			cur.text = head + cur.text
			out = append(out, cur)
			cur = packedLine{}
		}
		if len(cur.items) > 0 {
			cur.text += "; "
		}
		cur.text += it.text
		cur.items = append(cur.items, it)
	}
	if len(cur.items) > 0 {
		cur.text = head + cur.text
		out = append(out, cur)
	}
	return out
}

// alertOnce posts err as an ALERT on Crius's bus, once per error (the last one kept in state/<file>), and gives it back;
// nil clears the file. The log of a launchd job is read by nobody.
func alertOnce(home string, now time.Time, file, what string, err error) error {
	last := filepath.Join(home, "state", file)
	if err == nil {
		if rerr := os.Remove(last); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		return nil
	}
	msg, _ := redact.Redact(err.Error())
	if b, rerr := os.ReadFile(last); rerr != nil || strings.TrimSuffix(string(b), "\n") != msg {
		if perr := watchdog.Post(home, now, what+msg); perr != nil {
			return fmt.Errorf("%w; the ALERT was not posted: %v", err, perr)
		}
		if werr := os.WriteFile(last, []byte(msg+"\n"), 0o600); werr != nil {
			return fmt.Errorf("%w; %v", err, werr)
		}
	}
	return err
}
