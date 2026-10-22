// Package launch opens a team session the one way cmux-team SKILL.md §5 asks: a tab inside the project's workspace
// (a workspace is made only when the project has none), named "<project> · <role>", the workspace coloured by its
// group, and Claude started through aitch with the role's model and the same name (-n, which is what /rename sets).
// It then answers the folder-trust dialog, checks the status bar model, types the provenance tag line and the first
// prompt, and proves the tab started work.
package launch

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
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
	okRe        = regexp.MustCompile(`^OK ((?:surface|workspace):\d+)`)
	optionRe    = regexp.MustCompile(`^❯?\s*(?:\d+\.\s+)?`)
	modelRe     = regexp.MustCompile(`\b(?:Opus|Sonnet|Haiku|Fable) \d+(?:\.\d+)?\b`)
)

// reads is how many times a step reads the new tab before it gives up (with Wait between: two minutes at 1s).
const reads = 120

type Deps struct {
	Lock  string // file locked while finding or making the workspace, so two launches make one
	Cmux  func(args ...string) (string, error)
	Place func(project string) (cwd, account, persona string, err error) // from projects.toml
	Wait  time.Duration                                                  // between two reads of the new tab
}

// Spawn is one session to open: Parent (the leader's session id) and Item ("quotes#41") go in the provenance tag when
// known, and Prompt is typed on the line after the tag.
type Spawn struct{ Project, Role, Account, Parent, Item, Prompt string }

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
	}
	if account != "max" && account != "enterprise" {
		return "", "", "", fmt.Errorf("bad account %q: want max or enterprise", account)
	}
	look, table := looks[s.Project]
	if !table {
		palette := groups[persona]
		if palette == nil {
			return "", "", "", fmt.Errorf("project %s has persona %q in projects.toml: want work or personal to pick its colour", s.Project, persona)
		}
		h := fnv.New32a()
		h.Write([]byte(s.Project))
		look.name, look.color = strings.ToUpper(s.Project[:1])+s.Project[1:], palette[h.Sum32()%uint32(len(palette))]
	}
	name := s.Project + " · " + s.Role
	model := "claude-opus-5-5"
	if sonnetRoles.MatchString(s.Role) {
		model = "claude-sonnet-5-5"
	}
	// Through aitch, never bare claude: cmux's shell wraps claude and skips aitch's quota route (SKILL.md §5).
	cmd := "aitch launch claude -- --model " + model +
		` --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n '` + name + `'`

	ws, surface, err = open(d, look.name, look.color, table, cwd, cmd)
	if err != nil {
		return "", "", "", err
	}
	if out, err := d.Cmux("rename-tab", "--workspace", ws, "--surface", surface, "--", name); err != nil {
		return "", "", "", fmt.Errorf("cmux rename-tab: %v: %s", err, out)
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
	return ws, surface, tag, nil
}

// open finds or makes the workspace under the lock, adds the tab and colours the workspace: always for a project in
// the table, and for one outside it only when the workspace is new or has no colour (the owner may have picked one).
func open(d Deps, title, color string, table bool, cwd, cmd string) (ws, surface string, err error) {
	if d.Lock == "" {
		return "", "", fmt.Errorf("no launch lock file named: two launches at once could make two workspaces")
	}
	lf, err := os.OpenFile(d.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("launch lock: %v", err)
	}
	defer lf.Close() // closing releases the lock
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return "", "", fmt.Errorf("launch lock %s: %v", d.Lock, err)
	}
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
		if draft != "" {
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
	err := until(d, ws, surface, `work ("esc to interrupt") or the sent tag above an empty box`, func(screen string) bool {
		if strings.Contains(screen, "esc to interrupt") {
			return true
		}
		draft, boxed := wakewatch.Prompt(screen)
		if !boxed || draft != "" || !strings.Contains(screen, "[crius account=") {
			return false
		}
		for _, line := range strings.Split(screen, "\n") {
			if l := strings.TrimSpace(line); strings.HasPrefix(l, "⎿") && strings.Contains(l, "API Error") && !strings.Contains(l, "Retrying") {
				failed = l
			}
		}
		return true
	})
	if err == nil && failed != "" {
		return fmt.Errorf("the first turn failed: %s", strings.TrimSpace(failed))
	}
	return err
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
