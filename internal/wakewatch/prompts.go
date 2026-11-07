package wakewatch

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
// Crius's own tab is left out: only the owner can answer it. Each wake is said again every clickRepeat while it lasts.
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
				return err
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
				return fmt.Errorf("%v; and the ALERT was not posted: %v", err, perr)
			}
		}
	}
	return nil
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
