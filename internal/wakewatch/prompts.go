package wakewatch

import (
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
	Tabs    map[string]openPrompt
	OpsDown time.Time // when Crius was last told crius:ops has no live Claude; zero while it has one
}

// Prompts runs the prompt watch once. A run that fails says so on the bus as an ALERT, once per error.
func Prompts(d PromptDeps) error {
	return alertOnce(d.Home, d.Now, "prompts.error", "prompt watch failed: ", prompts(d))
}

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
	// crius:ops: why it cannot answer prompts now, "" when it can
	var ops *Screen
	for i, s := range screens {
		if BusName(tabName(s)) == OpsName || s.Title == opsTitle {
			ops = &screens[i]
			break
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
	var toCrius, toOps, late []string
	var opsKeys []string
	if down == "" {
		st.OpsDown = time.Time{}
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
		if !open || who == "crius:lead" {
			continue
		}
		p := st.Tabs[s.Surface]
		if h := hash(promptKey(prompt)); p.Hash != h {
			p = openPrompt{Hash: h, Since: d.Now}
		}
		age := d.Now.Sub(p.Since)
		name := fmt.Sprintf("%s (%s, %d min", tabName(s), s.Surface, int(age.Minutes()))
		switch {
		case project(who) == "crius":
			if age >= ownAfter && d.Now.Sub(p.Crius) >= clickRepeat {
				toCrius = append(toCrius, fmt.Sprintf("%s (%s) has had a permission prompt open for %d min, and only you answer its prompts: %s",
					tabName(s), s.Surface, int(age.Minutes()), command(s.Text, prompt)))
				p.Crius = d.Now
			}
		case age >= criusAfter && !p.Ops.IsZero() && d.Now.Sub(p.Crius) >= clickRepeat:
			late = append(late, name+"; "+OpsName+" told "+p.Ops.Format("15:04")+")")
			p.Crius = d.Now
		case age >= opsAfter && p.Ops.IsZero():
			if blocked != "" {
				if d.Now.Sub(p.Crius) >= clickRepeat {
					toOps = append(toOps, name+")")
					p.Crius = d.Now
				}
				break
			}
			opsKeys = append(opsKeys, s.Surface)
			toOps = append(toOps, name+")")
		}
		next[s.Surface] = p
	}
	st.Tabs = next
	if len(late) > 0 {
		toCrius = append(toCrius, pack("prompts still open 10+ min, and "+OpsName+" has not answered them: ", late)...)
	}
	if len(toOps) > 0 && blocked == "" {
		var sendErr error
		for _, line := range pack("prompts open 2+ min for you to answer: ", toOps) {
			if err := d.SendTab(ops.Title, Prefix+Clean(line)); err != nil && sendErr == nil {
				sendErr = err
			}
		}
		if sendErr == nil || d.HandedOver != nil && d.HandedOver(sendErr) {
			for _, k := range opsKeys {
				p := st.Tabs[k]
				p.Ops = d.Now
				st.Tabs[k] = p
			}
		}
		if sendErr != nil {
			blocked = "typing into its tab failed: " + sendErr.Error()
			fresh := toOps[:0]
			for i, k := range opsKeys { // Crius hears of each prompt once per clickRepeat, however often ops fails
				if p := st.Tabs[k]; d.Now.Sub(p.Crius) >= clickRepeat {
					p.Crius = d.Now
					st.Tabs[k] = p
					fresh = append(fresh, toOps[i])
				}
			}
			toOps = fresh
		} else {
			toOps = nil
		}
	}
	if len(toOps) > 0 {
		toCrius = append(toCrius, pack(OpsName+" cannot answer prompts now ("+blocked+"): ", toOps)...)
	}
	// The state goes first: a wake that then fails is posted as an ALERT; a state that failed after the wakes would
	// say them again every minute.
	if err := writeJSON(path, st); err != nil {
		return err
	}
	for _, line := range toCrius {
		text := Clean(line)
		if err := d.SendCrius(Prefix + text); err != nil {
			// Crius's waiter wakes on an ALERT line: a wake that cannot be typed goes there
			if perr := watchdog.Post(d.Home, d.Now, text); perr != nil {
				return fmt.Errorf("%v; and the ALERT was not posted: %v", err, perr)
			}
		}
	}
	return nil
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
	var out []string
	line := ""
	for _, it := range items {
		if line != "" && len([]rune(head+line+"; "+it)) > MaxLine {
			out = append(out, head+line)
			line = ""
		}
		if line != "" {
			line += "; "
		}
		line += it
	}
	if line != "" {
		out = append(out, head+line)
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
