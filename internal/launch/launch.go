// Package launch opens a team session the one way cmux-team SKILL.md §5 asks: a tab inside the project's workspace
// (a workspace is made only when the project has none), named "<project> · <role>" (and " · <account>" on a picked
// Max account), the workspace coloured by its group, and Claude started through aitch with the role's model and the
// same name (-n, which is what /rename sets).
// It then answers the folder-trust dialog, checks the status bar model, types the provenance tag line and the first
// prompt, and proves the tab started work.
package launch

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/wakewatch"
)

// looks is the one table of workspace names and colors: blue shades for Appxite, green and warm for personal work,
// Purple for Crius only (Crius 10-21 23:07).
var looks = map[string]struct{ name, color string }{
	"quotes": {"Quotes", "Blue"}, "axi": {"AXI", "Aqua"}, "cortex": {"Cortex", "Navy"},
	"trading": {"Trading", "Green"}, "falcon": {"Falcon", "Orange"}, "staffy": {"Staffy", "Rose"},
	"crius": {"Crius", "Purple"},
}

// groups colour a project outside the table by its persona in projects.toml (SKILL.md §5: blue shades for Appxite
// work, green and warm for personal), with colours the table does not use.
var groups = map[string][]string{"work": {"Indigo", "Teal"}, "personal": {"Olive", "Amber", "Brown"}}

// sonnetRoles run on Sonnet (the trial in SKILL.md §5); every other role runs on Opus.
var sonnetRoles = regexp.MustCompile(`^(?:qa-.*|devops|researcher|scout|tech-writer|code-reviewer)$`)

var (
	slugRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	parentRe    = regexp.MustCompile(`^[0-9a-f-]{8,36}$`)        // as the transcript reader takes it
	itemRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*#\d+$`) // as the transcript reader takes it
	windowRe    = regexp.MustCompile(`window (window:\d+)`)
	workspaceRe = regexp.MustCompile(`workspace (workspace:\d+) "(.*?)"`)
	paneRe      = regexp.MustCompile(`pane (pane:\d+)`)
	surfaceRe   = regexp.MustCompile(`surface (surface:\d+)`)
	tabRe       = regexp.MustCompile(`surface (surface:\d+) \[terminal\] "(.*)".*? tty=(\S+)`)
	idRe        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`) // a Claude Code session id
	okRe        = regexp.MustCompile(`^OK ((?:surface|workspace):\d+)`)
	optionRe    = regexp.MustCompile(`^❯?\s*(?:\d+\.\s+)?`)
	modelRe     = regexp.MustCompile(`\b(?:Opus|Sonnet|Haiku|Fable) \d+(?:\.\d+)?\b`)
)

// reads is how many times a step reads the new tab before it gives up (with Wait between: two minutes at 1s).
const reads = 120

type Deps struct {
	Lock    string // file locked while finding or making the workspace, so two launches make one
	Cmux    func(args ...string) (string, error)
	Place   func(project string) (cwd, account, persona string, err error) // from projects.toml
	Persona func(project string) (string, error)                           // from projects.toml; -resume needs no launch folder
	Wait    time.Duration                                                  // between two reads of the new tab
	Front   func(tty string) (string, error)                               // the command in front on a tty, as ps shows it
	Typed   func(surface string) (bool, error)                             // whether a tab's box holds typed text, not grey text (cmux input state)
	// Pick is the Max account a session starts on, "max" for the one with the most room or a row's name, and its
	// config folder (quota.Pick); no name launches as before, through aitch's own route. Nil: no accounts.
	Pick func(project, want string) (name, dir string, err error)
}

// Spawn is one session to open: Parent (the leader's session id) and Item ("quotes#41") go in the provenance tag when
// known, and Prompt is typed on the line after the tag. Move, when set, moves the role off the account it was on.
type Spawn struct {
	Project, Role, Account, Parent, Item, Prompt string
	Move                                         *Moved
}

// Moved is the role's old session: its folder (the worktree and branch the new session starts in) and the Max account
// it ran on ("" for none in accounts.toml).
type Moved struct{ Cwd, From string }

// Run opens the tab, starts its first prompt, and gives its workspace and surface and the provenance tag line typed.
func Run(s Spawn, d Deps) (ws, surface, tag string, err error) {
	switch {
	case !slugRe.MatchString(s.Project):
		return "", "", "", fmt.Errorf("bad project %q: want lowercase letters, digits and dashes", s.Project)
	case !slugRe.MatchString(s.Role):
		return "", "", "", fmt.Errorf("bad role %q: want lowercase letters, digits and dashes", s.Role)
	case s.Parent != "" && !parentRe.MatchString(s.Parent):
		return "", "", "", fmt.Errorf("bad parent %q: want the leader's session id", s.Parent)
	case s.Item != "" && !itemRe.MatchString(s.Item):
		return "", "", "", fmt.Errorf("bad item %q: want <project>#<n>", s.Item)
	case strings.TrimSpace(s.Prompt) == "":
		return "", "", "", fmt.Errorf("no first prompt: the tag line goes before one")
	case strings.Contains(s.Prompt, `\`):
		// cmux send reads a backslash-n or -r as Enter, which would send half the prompt.
		return "", "", "", fmt.Errorf("the first prompt holds a backslash, which cmux could read as Enter")
	case strings.IndexFunc(s.Prompt, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0:
		return "", "", "", fmt.Errorf("the first prompt holds a line break or control character: it must be one line")
	}
	cwd, account, persona, err := d.Place(s.Project)
	if err != nil {
		return "", "", "", err
	}
	if s.Account != "" {
		account = s.Account
	} else if s.Move != nil {
		account = "max" // a move goes to the Max account with the most room, whatever the project starts on
	}
	var acct, dir string
	switch {
	case account == "enterprise":
	case d.Pick != nil && slugRe.MatchString(account):
		if acct, dir, err = d.Pick(s.Project, account); err != nil {
			return "", "", "", err
		}
		account = "max"
	case account != "max":
		return "", "", "", fmt.Errorf("bad account %q: want max or enterprise", account)
	}
	switch {
	case acct != "" && !slugRe.MatchString(acct): // typed into a shell and a title
		return "", "", "", fmt.Errorf("bad account name %q: want lowercase letters, digits and dashes", acct)
	case acct != "" && !safeDir(dir):
		return "", "", "", fmt.Errorf("bad folder %q for account %s: want an absolute path with no quote, backslash or control character", dir, acct)
	}
	name, model, cmd := command(s.Project, s.Role, acct, dir)
	var oldWs, oldSurface, oldTty string
	if s.Move != nil {
		if d.Lock == "" {
			return "", "", "", fmt.Errorf("no launch lock file named: a resume of the role at the same time could start a second Claude")
		}
		// The role's lock, as Resume takes it, held until the new box is up.
		lf, err := hold(d.Lock + "." + s.Project + "." + s.Role)
		if err != nil {
			return "", "", "", err
		}
		defer lf.Close()
		if cwd, oldWs, oldSurface, oldTty, err = move(d, s, acct, name); err != nil {
			return "", "", "", err
		}
	}
	ws, surface, err = tab(d, s.Project, persona, name, cwd, cmd)
	if err != nil {
		return "", "", "", err
	}
	tag = "[crius account=" + account + " project=" + s.Project + " role=" + s.Role
	if s.Parent != "" {
		tag += " parent=" + s.Parent
	}
	if s.Item != "" {
		tag += " item=" + s.Item
	}
	tag += "]"
	if err := start(d, ws, surface, display(model), tag, s.Prompt); err != nil {
		return "", "", "", fmt.Errorf("%s in %s %s: %v", name, ws, surface, err)
	}
	// The old tab, a shell, closes only once the new session is up, so a failure above leaves the role its tab; it is
	// looked at again, as something started in it meanwhile keeps it.
	if oldSurface != "" {
		if front, err := d.Front(oldTty); err != nil {
			return "", "", "", fmt.Errorf("%s is up in %s %s, but its old tab %s %s: %v", name, ws, surface, oldWs, oldSurface, err)
		} else if !shells[filepath.Base(strings.TrimPrefix(front, "-"))] {
			return "", "", "", fmt.Errorf("%s is up in %s %s, but its old tab %s %s runs %s in front now: left open", name, ws, surface, oldWs, oldSurface, front)
		}
		if out, err := d.Cmux("close-surface", "--workspace", oldWs, "--surface", oldSurface); err != nil {
			return "", "", "", fmt.Errorf("%s is up in %s %s, but its old tab %s %s did not close: %v: %s", name, ws, surface, oldWs, oldSurface, err, out)
		}
	}
	return ws, surface, tag, nil
}

// move checks a move to acct and finds the role's old tab, a shell once its Claude has stopped, for Run to close once
// the new session is up, so the role has one tab; it gives the folder the new session starts in, the old one's, and the
// old tab and its terminal ("" when there is none). Anything wrong is refused before a tab is touched.
func move(d Deps, s Spawn, acct, name string) (cwd, ws, surface, tty string, err error) {
	switch {
	case !safeDir(s.Move.Cwd):
		return "", "", "", "", fmt.Errorf("bad folder %q: want an absolute path with no quote, backslash or control character", s.Move.Cwd)
	case acct == "":
		return "", "", "", "", fmt.Errorf("%s %s: no Max account in accounts.toml with room to move to", s.Project, s.Role)
	case acct == s.Move.From:
		return "", "", "", "", fmt.Errorf("%s %s is on %s already: not moved", s.Project, s.Role, acct)
	}
	tree, err := d.Cmux("tree", "--all")
	if err != nil {
		return "", "", "", "", fmt.Errorf("cmux tree: %v", err)
	}
	ws, surface, tty, err = named(tree, s.Project+" · "+s.Role)
	if err != nil || surface == "" {
		return s.Move.Cwd, "", "", "", err
	}
	front, err := d.Front(tty)
	if err != nil {
		return "", "", "", "", err
	}
	if !shells[filepath.Base(strings.TrimPrefix(front, "-"))] {
		return "", "", "", "", fmt.Errorf("%s %s (%s %s) runs %s in front, not a shell: left alone, not moved to %s", s.Project, s.Role, ws, surface, front, name)
	}
	return s.Move.Cwd, ws, surface, tty, nil
}

// command gives a role's tab name, its model, and the shell line that starts Claude for it. With an account (acct, its
// config folder dir) the name ends in it and aitch starts Claude in that folder.
func command(project, role, acct, dir string) (name, model, cmd string) {
	name = project + " · " + role
	model = "claude-opus-5-5"
	if sonnetRoles.MatchString(role) {
		model = "claude-sonnet-5-5"
	}
	// Through aitch, never bare claude: cmux's shell wraps claude and skips aitch's quota route (SKILL.md §5).
	cmd = "aitch launch claude -- --model "
	if acct != "" {
		name += " · " + acct
		cmd = "aitch launch claude --primary '" + dir + "' -- --model "
	}
	cmd += model +
		` --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n '` + name + `'`
	return name, model, cmd
}

// tab opens a new tab running cmd in the project's workspace, coloured by its look, and names it.
func tab(d Deps, project, persona, name, cwd, cmd string) (ws, surface string, err error) {
	look, table := looks[project]
	if !table {
		palette := groups[persona]
		if palette == nil {
			return "", "", fmt.Errorf("project %s has persona %q in projects.toml: want work or personal to pick its colour", project, persona)
		}
		h := fnv.New32a()
		h.Write([]byte(project))
		look.name, look.color = strings.ToUpper(project[:1])+project[1:], palette[h.Sum32()%uint32(len(palette))]
	}
	if ws, surface, err = open(d, look.name, look.color, table, cwd, cmd); err != nil {
		return "", "", err
	}
	if out, err := d.Cmux("rename-tab", "--workspace", ws, "--surface", surface, "--", name); err != nil {
		return "", "", fmt.Errorf("cmux rename-tab: %v: %s", err, out)
	}
	return ws, surface, nil
}

// Resume brings a role's session back after a crash or a reboot: claude --resume <id> in the session's own folder,
// typed into the role's tab when the tab is still there with a shell in front (a crash), else in a new tab of the
// project's workspace named as before (a reboot). A tab with anything else in front, Claude itself, is left alone. It
// waits for Claude's input box, so a resume that finds no conversation fails with what the screen said. A session that
// ran on a Max account of accounts.toml (acct, its config folder dir) comes back in that folder, titled with the
// account; acct "" goes through aitch's own route as before.
func Resume(project, role, id, cwd, acct, dir string, d Deps) (ws, surface string, err error) {
	switch {
	case acct != "" && !slugRe.MatchString(acct): // typed into a shell and a title
		return "", "", fmt.Errorf("bad account name %q: want lowercase letters, digits and dashes", acct)
	case acct != "" && !safeDir(dir):
		return "", "", fmt.Errorf("bad folder %q for account %s: want an absolute path with no quote, backslash or control character", dir, acct)
	case !slugRe.MatchString(project) || !slugRe.MatchString(role):
		return "", "", fmt.Errorf("bad project %q or role %q: want lowercase letters, digits and dashes", project, role)
	case !idRe.MatchString(id): // typed into a shell
		return "", "", fmt.Errorf("bad session id %q", id)
	case !safeDir(cwd):
		return "", "", fmt.Errorf("bad folder %q: want an absolute path with no quote, backslash or control character", cwd)
	}
	if d.Lock == "" {
		return "", "", fmt.Errorf("no launch lock file named: two resumes at once could both type into the tab")
	}
	// One resume of a role at a time, held until its box is up: a second one then finds Claude in front and leaves the
	// tab alone, where both saw a shell and both typed (OF9).
	lf, err := hold(d.Lock + "." + project + "." + role)
	if err != nil {
		return "", "", err
	}
	defer lf.Close()
	name, _, cmd := command(project, role, acct, dir)
	cmd += " --resume " + id
	tree, err := d.Cmux("tree", "--all")
	if err != nil {
		return "", "", fmt.Errorf("cmux tree: %v", err)
	}
	ws, surface, tty, err := named(tree, project+" · "+role) // titled with an account or not
	if err != nil {
		return "", "", err
	}
	typed := surface != "" // a shell line typed into a crashed tab
	if typed {
		front, err := d.Front(tty)
		if err != nil {
			return "", "", err
		}
		if !shells[filepath.Base(strings.TrimPrefix(front, "-"))] {
			return "", "", fmt.Errorf("%s (%s %s) runs %s in front, not a shell: left alone", name, ws, surface, front)
		}
		line := "clear && cd '" + cwd + "' && " + cmd
		if out, err := d.Cmux("send", "--workspace", ws, "--surface", surface, "--", line); err != nil {
			return "", "", fmt.Errorf("cmux send: %v: %s", err, out)
		}
		// The dead Claude's last screen may still show an input box (OF9): Enter goes once the line is on the shell's
		// line, and the wait below takes a box only once clear wiped that line.
		if err := until(d, ws, surface, "typed line", func(s string) bool { return onShell(s, id) }); err != nil {
			return "", "", fmt.Errorf("%s in %s %s: %v", name, ws, surface, err)
		}
		if out, err := d.Cmux("send-key", "--workspace", ws, "--surface", surface, "enter"); err != nil {
			return "", "", fmt.Errorf("cmux send-key: %v: %s", err, out)
		}
	} else {
		persona, err := d.Persona(project)
		if err != nil {
			return "", "", err
		}
		if ws, surface, err = tab(d, project, persona, name, cwd, cmd); err != nil {
			return "", "", err
		}
	}
	failed := false
	err = until(d, ws, surface, "Claude's input box", func(screen string) bool {
		if typed && onShell(screen, id) { // clear has not run: the old screen
			return false
		}
		_, boxed := wakewatch.Prompt(screen)
		failed = !boxed && strings.Contains(screen, "No conversation found") // an up screen may quote the words
		return boxed || failed
	})
	if err == nil && failed {
		screen, _ := read(d, ws, surface)
		err = fmt.Errorf("claude --resume %s: the screen ends: %s", id, tail(screen))
	}
	if err != nil {
		return "", "", fmt.Errorf("%s in %s %s: %v", name, ws, surface, err)
	}
	return ws, surface, nil
}

// safeDir is a folder that can go in single quotes on a shell line: absolute, with no quote, backslash or control
// character.
func safeDir(dir string) bool {
	return filepath.IsAbs(dir) && !strings.ContainsAny(dir, `'\`) && strings.IndexFunc(dir, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// hold opens the lock file and waits for its lock; closing the file releases it.
func hold(path string) (*os.File, error) {
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("launch lock: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		lf.Close()
		return nil, fmt.Errorf("launch lock %s: %v", path, err)
	}
	return lf, nil
}

// shells are what may be in front of a tab whose Claude has ended.
var shells = map[string]bool{"zsh": true, "bash": true, "sh": true, "fish": true}

// named gives the one terminal tab titled name, or name and " · <account>" (a tab on a picked account), its workspace
// and its tty, or no surface when there is none.
func named(tree, name string) (ws, surface, tty string, err error) {
	var w string
	for _, line := range strings.Split(tree, "\n") {
		if m := workspaceRe.FindStringSubmatch(line); m != nil {
			w = m[1]
		} else if m := tabRe.FindStringSubmatch(line); m != nil && (m[2] == name || strings.HasPrefix(m[2], name+" · ")) {
			if surface != "" {
				return "", "", "", fmt.Errorf("two tabs are titled %q (%s, %s): rename one", name, surface, m[1])
			}
			ws, surface, tty = w, m[1], m[3]
		}
	}
	return ws, surface, tty, nil
}

// open finds or makes the workspace under the lock, adds the tab and colours the workspace: always for a project in
// the table, and for one outside it only when the workspace is new or has no colour (the owner may have picked one).
func open(d Deps, title, color string, table bool, cwd, cmd string) (ws, surface string, err error) {
	if d.Lock == "" {
		return "", "", fmt.Errorf("no launch lock file named: two launches at once could make two workspaces")
	}
	lf, err := hold(d.Lock)
	if err != nil {
		return "", "", err
	}
	defer lf.Close() // closing releases the lock
	tree, err := d.Cmux("tree", "--all")
	if err != nil {
		return "", "", fmt.Errorf("cmux tree: %v", err)
	}
	ws, pane, window := find(tree, title)
	paint := table || ws == ""
	if ws != "" {
		out, err := d.Cmux("new-surface", "--workspace", ws, "--pane", pane, "--working-directory", cwd, "--command", cmd)
		if surface = ok1(out, "surface:"); err != nil || surface == "" {
			return "", "", fmt.Errorf("cmux new-surface: %v: %s", err, out)
		}
		if !paint {
			if paint, err = uncoloured(d, window, ws); err != nil {
				return "", "", err
			}
		}
	} else {
		out, err := d.Cmux("new-workspace", "--name", title, "--cwd", cwd, "--command", cmd)
		if ws = ok1(out, "workspace:"); err != nil || ws == "" {
			return "", "", fmt.Errorf("cmux new-workspace: %v: %s", err, out)
		}
		// Coloured at once, not after the wait below, so wakewatch never sees it uncoloured (review 10-22).
		if out, err := d.Cmux("workspace-action", "--workspace", ws, "--action", "set-color", "--color", color); err != nil {
			return "", "", fmt.Errorf("cmux set-color: %v: %s", err, out)
		}
		paint = false
		// The tree read just after new-workspace may not list it yet (live run 10-22).
		for i := 0; surface == "" && i < reads; i++ {
			if i > 0 {
				time.Sleep(d.Wait)
			}
			if tree, err = d.Cmux("tree", "--all"); err != nil {
				return "", "", fmt.Errorf("cmux tree: %v", err)
			}
			surface = firstSurface(tree, ws)
		}
		if surface == "" {
			return "", "", fmt.Errorf("cmux tree shows no tab in the new workspace %s after %d reads", ws, reads)
		}
	}
	if paint {
		if out, err := d.Cmux("workspace-action", "--workspace", ws, "--action", "set-color", "--color", color); err != nil {
			return "", "", fmt.Errorf("cmux set-color: %v: %s", err, out)
		}
	}
	return ws, surface, nil
}

// uncoloured tells whether workspace ws in window has no colour set.
func uncoloured(d Deps, window, ws string) (bool, error) {
	out, err := d.Cmux("workspace", "list", "--window", window, "--json")
	if err != nil {
		return false, fmt.Errorf("cmux workspace list: %v: %s", err, out)
	}
	var list struct {
		Workspaces []struct {
			Ref   string
			Color string `json:"custom_color"`
		}
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return false, fmt.Errorf("cmux workspace list: %v", err)
	}
	for _, w := range list.Workspaces {
		if w.Ref == ws {
			return w.Color == "", nil
		}
	}
	return false, fmt.Errorf("cmux workspace list for %s does not show %s", window, ws)
}

// start waits for Claude's empty input box (answering the folder-trust dialog Yes), checks the banner shows the model
// asked for, types the tag line and the prompt, sends them, and waits until the tab shows the prompt was taken.
func start(d Deps, ws, surface, model, tag, prompt string) error {
	var screen string
	answered := false
	for i := 0; ; i++ {
		if i == reads {
			return fmt.Errorf("no input box with %s on screen after %d reads; the screen ends: %s", model, reads, tail(screen))
		}
		if i > 0 {
			time.Sleep(d.Wait)
		}
		var err error
		if screen, err = read(d, ws, surface); err != nil {
			return err
		}
		if keys := answer(screen); keys != nil {
			if !answered { // once: a dialog still drawn after its Enter must not get another (review 10-22)
				answered = true
				for _, key := range keys {
					if out, err := d.Cmux("send-key", "--workspace", ws, "--surface", surface, key); err != nil {
						return fmt.Errorf("cmux send-key %s: %v: %s", key, err, out)
					}
				}
			}
			continue
		}
		draft, boxed := wakewatch.Prompt(screen)
		if !boxed {
			continue
		}
		if held, err := typed(d, surface, draft); err != nil {
			return err
		} else if held {
			return fmt.Errorf("the new tab's input box already holds text: %q", draft)
		}
		// Live 10-22: the banner names the model at start ("Sonnet 5.5 · Claude Max"); the status bar does only after the
		// first reply. Only the banner counts: a tip may name another model.
		var got []string
		for _, line := range strings.Split(screen, "\n") {
			if strings.Contains(line, " · Claude ") {
				got = append(got, modelRe.FindAllString(line, -1)...)
			}
		}
		for _, m := range got {
			if m != model {
				return fmt.Errorf("the screen shows %s, want %s", m, model)
			}
		}
		if got != nil {
			break
		}
	}
	// The tag must be alone on its line: a backslash before Enter makes a new line in Claude Code's box. cmux send
	// refuses a box that holds text (live 10-22), so the prompt is forced, only once the box holds just the tag.
	for _, step := range [][]string{{"send", "--", tag + `\`}, {"send-key", "enter"}} {
		if out, err := d.Cmux(append([]string{step[0], "--workspace", ws, "--surface", surface}, step[1:]...)...); err != nil {
			return fmt.Errorf("cmux %s: %v: %s", step[0], err, out)
		}
	}
	if err := until(d, ws, surface, "the tag line in the box", func(screen string) bool {
		draft, _ := wakewatch.Prompt(screen)
		return squash(draft) == squash(tag)
	}); err != nil {
		return err
	}
	if out, err := d.Cmux("send", "--workspace", ws, "--surface", surface, "--force", "--", prompt); err != nil {
		return fmt.Errorf("cmux send: %v: %s", err, out)
	}
	if err := until(d, ws, surface, "the typed prompt in the box", func(screen string) bool {
		draft, _ := wakewatch.Prompt(screen)
		return squash(draft) == squash(tag+prompt)
	}); err != nil {
		return err
	}
	if out, err := d.Cmux("send-key", "--workspace", ws, "--surface", surface, "enter"); err != nil {
		return fmt.Errorf("cmux send-key enter: %v: %s", err, out)
	}
	// A short prompt can be answered before a read sees work (live 10-22: 5 s), so the tag shown above an emptied box
	// also proves the prompt was taken: the tab is new, so nothing else on it can show a tag.
	// A first turn that fails at once empties the box as well, under Claude Code's "⎿  API Error" line: that session
	// never started. Only that line counts, and only with no work showing: the prompt may name an API error, and a
	// line that says it is retrying may still recover (review 10-22).
	var failed string
	var stateErr error
	err := until(d, ws, surface, `work ("esc to interrupt") or the sent tag above an empty box`, func(screen string) bool {
		stateErr = nil // only the last read counts: an old failure is no reason a later wait timed out
		if strings.Contains(screen, "esc to interrupt") {
			return true
		}
		draft, boxed := wakewatch.Prompt(screen)
		if !boxed || !strings.Contains(screen, "[crius account=") {
			return false
		}
		// A read taken just before Claude takes the prompt still draws it in the box, while the input state read after
		// it says empty: that screen proves nothing and may hide the API error drawn next (review 10-22).
		if squash(draft) == squash(tag+prompt) {
			return false
		}
		held, err := typed(d, surface, draft)
		if stateErr = err; err != nil || held { // one failed read is no answer: the next read decides (review 10-22)
			return false
		}
		for _, line := range strings.Split(screen, "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "⎿") && strings.Contains(l, "API Error") && !strings.Contains(l, "Retrying") {
				failed = l
			}
		}
		return true
	})
	if err != nil { // after Enter: a rerun would send the prompt twice (review 10-22)
		if stateErr != nil {
			err = fmt.Errorf("%v; its input state cannot be read: %v", err, stateErr)
		}
		return fmt.Errorf("the prompt was sent to %s %s, but %v; look at the tab, do not rerun", ws, surface, err)
	}
	if failed != "" {
		return fmt.Errorf("the first turn failed: %s", strings.TrimSpace(failed))
	}
	return nil
}

// typed tells typed text in the box from grey text (Claude's placeholder 'Try "…"'), which read-screen gives as plain
// text (qa3 10-22): only cmux's input state keeps the faint style.
func typed(d Deps, surface, draft string) (bool, error) {
	if draft == "" {
		return false, nil
	}
	return d.Typed(surface)
}

// until reads the tab until ok holds, or fails naming what never showed.
func until(d Deps, ws, surface, what string, ok func(string) bool) error {
	var screen string
	for i := 0; i < reads; i++ {
		if i > 0 {
			time.Sleep(d.Wait)
		}
		var err error
		if screen, err = read(d, ws, surface); err != nil {
			return err
		}
		if ok(screen) {
			return nil
		}
	}
	return fmt.Errorf("no %s after %d reads; the screen ends: %s", what, reads, tail(screen))
}

// read gives the visible screen, all of it: "--lines 40" cut the top off a 54-line tab (live 10-22).
func read(d Deps, ws, surface string) (string, error) {
	screen, err := d.Cmux("read-screen", "--workspace", ws, "--surface", surface)
	if err != nil {
		return "", fmt.Errorf("cmux read-screen: %v: %s", err, screen)
	}
	return screen, nil
}

// answer gives the keys that pick the Yes option of the folder-trust dialog on screen ("down", …, "enter"), or nil
// when the screen shows no such dialog ("Do you trust", "Enter to confirm", a cursor, a Yes option). No other dialog
// is answered: Yes to one such as "use this API key?" could bill the wrong account (review 10-22). The options are
// the lines around the cursor's up to a blank line, numbered or not (live 10-22 they were not).
func answer(screen string) []string {
	if !strings.Contains(screen, "Enter to confirm") || !strings.Contains(screen, "Do you trust") {
		return nil
	}
	lines := strings.Split(screen, "\n")
	cur := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "❯ ") {
			cur = i
			break
		}
	}
	if cur < 0 {
		return nil
	}
	lo, hi := cur, cur
	for lo > 0 && strings.TrimSpace(lines[lo-1]) != "" {
		lo--
	}
	for hi < len(lines)-1 && strings.TrimSpace(lines[hi+1]) != "" {
		hi++
	}
	yes := -1
	for i := lo; i <= hi && yes < 0; i++ {
		if strings.HasPrefix(optionRe.ReplaceAllString(strings.TrimSpace(lines[i]), ""), "Yes") {
			yes = i
		}
	}
	if yes < 0 {
		return nil
	}
	var keys []string
	for ; cur < yes; cur++ {
		keys = append(keys, "down")
	}
	for ; cur > yes; cur-- {
		keys = append(keys, "up")
	}
	return append(keys, "enter")
}

// display gives the name the status bar shows for a model id: "claude-opus-5-5" is "Opus 5.5".
func display(model string) string {
	parts := strings.Split(strings.TrimPrefix(model, "claude-"), "-")
	return strings.ToUpper(parts[0][:1]) + parts[0][1:] + " " + strings.Join(parts[1:], ".")
}

func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// onShell reports whether a screen shows the end of a typed resume line (--resume <id>; zsh keeps the end when it clips
// a line wider than the tab) with no input box under it: the shell's line, not a quote of it above Claude's box (review
// of OF9). A wrapped line is read with the line breaks squashed out.
func onShell(screen, id string) bool {
	lines := strings.Split(screen, "\n")
	var joined string
	ends := make([]int, len(lines))
	for i, l := range lines {
		joined += squash(l)
		ends[i] = len(joined)
	}
	mark := squash("--resume " + id)
	at := strings.LastIndex(joined, mark)
	if at < 0 {
		return false
	}
	i := 0
	for ends[i] < at+len(mark) {
		i++
	}
	_, boxed := wakewatch.Prompt(strings.Join(lines[i+1:], "\n"))
	return !boxed
}

// tail gives the last lines of a screen, for an error.
func tail(screen string) string {
	lines := strings.Split(strings.TrimRight(screen, "\n"), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, " ⏎ ")
}

// find gives the workspace whose name is name in any case, its first pane, and its window.
func find(tree, name string) (ws, pane, window string) {
	var w string
	for _, line := range strings.Split(tree, "\n") {
		if m := workspaceRe.FindStringSubmatch(line); m != nil {
			if ws != "" {
				break
			}
			if strings.EqualFold(m[2], name) {
				ws, window = m[1], w
			}
		} else if m := paneRe.FindStringSubmatch(line); m != nil && ws != "" && pane == "" {
			pane = m[1]
		} else if m := windowRe.FindStringSubmatch(line); m != nil && strings.HasPrefix(line, "window ") {
			w = m[1]
		}
	}
	return ws, pane, window
}

// firstSurface gives the first tab of workspace ws.
func firstSurface(tree, ws string) string {
	in := false
	for _, line := range strings.Split(tree, "\n") {
		if m := workspaceRe.FindStringSubmatch(line); m != nil {
			in = m[1] == ws
		} else if m := surfaceRe.FindStringSubmatch(line); m != nil && in {
			return m[1]
		}
	}
	return ""
}

// ok1 gives the ref in cmux's "OK <ref> …" answer when it is of the given kind.
func ok1(out, kind string) string {
	if m := okRe.FindStringSubmatch(strings.TrimSpace(out)); m != nil && strings.HasPrefix(m[1], kind) {
		return m[1]
	}
	return ""
}
