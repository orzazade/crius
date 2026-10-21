// Package launch opens a team session the one way cmux-team SKILL.md §5 asks: a tab inside the project's workspace
// (a workspace is made only when the project has none), named "<project> · <role>", the workspace colored from one
// table, and Claude started through aitch with the role's model and the same name (-n, which is what /rename sets).
package launch

import (
	"fmt"
	"regexp"
	"strings"
)

// looks is the one table of workspace names and colors: blue shades for Appxite, green and warm for personal work,
// Purple for Crius only (Crius 10-21 23:07).
var looks = map[string]struct{ name, color string }{
	"quotes": {"Quotes", "Blue"}, "axi": {"AXI", "Aqua"}, "cortex": {"Cortex", "Navy"},
	"trading": {"Trading", "Green"}, "falcon": {"Falcon", "Orange"}, "staffy": {"Staffy", "Rose"},
	"crius": {"Crius", "Purple"},
}

// sonnetRoles run on Sonnet (the trial in SKILL.md §5); every other role runs on Opus.
var sonnetRoles = regexp.MustCompile(`^(?:qa-.*|devops|researcher|scout|tech-writer|code-reviewer)$`)

var (
	roleRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	workspaceRe = regexp.MustCompile(`workspace (workspace:\d+) "(.*?)"`)
	paneRe      = regexp.MustCompile(`pane (pane:\d+)`)
	surfaceRe   = regexp.MustCompile(`surface (surface:\d+)`)
	okRe        = regexp.MustCompile(`^OK ((?:surface|workspace):\d+)`)
)

type Deps struct {
	Cmux  func(args ...string) (string, error)
	Place func(project string) (cwd, account string, err error) // from projects.toml
}

// Run opens the tab and gives its workspace and surface, and the provenance tag line for the first prompt.
func Run(project, role, account string, d Deps) (ws, surface, tag string, err error) {
	look, ok := looks[project]
	if !ok {
		return "", "", "", fmt.Errorf("project %s has no workspace name and color: add it to the table in internal/launch", project)
	}
	if !roleRe.MatchString(role) {
		return "", "", "", fmt.Errorf("bad role %q: want lowercase letters, digits and dashes", role)
	}
	cwd, def, err := d.Place(project)
	if err != nil {
		return "", "", "", err
	}
	if account == "" {
		account = def
	}
	if account != "max" && account != "enterprise" {
		return "", "", "", fmt.Errorf("bad account %q: want max or enterprise", account)
	}
	name := project + " · " + role
	model := "claude-opus-5-5"
	if sonnetRoles.MatchString(role) {
		model = "claude-sonnet-5-5"
	}
	// Through aitch, never bare claude: cmux's shell wraps claude and skips aitch's quota route (SKILL.md §5).
	cmd := "aitch launch claude -- --model " + model +
		` --permission-mode bypassPermissions --settings '{"autoCompactWindow":150000}' -n '` + name + `'`

	tree, err := d.Cmux("tree", "--all")
	if err != nil {
		return "", "", "", fmt.Errorf("cmux tree: %v", err)
	}
	ws, pane := find(tree, look.name)
	if ws != "" {
		out, err := d.Cmux("new-surface", "--workspace", ws, "--pane", pane, "--working-directory", cwd, "--command", cmd)
		if surface = ok1(out, "surface:"); err != nil || surface == "" {
			return "", "", "", fmt.Errorf("cmux new-surface: %v: %s", err, out)
		}
	} else {
		out, err := d.Cmux("new-workspace", "--name", look.name, "--cwd", cwd, "--command", cmd)
		if ws = ok1(out, "workspace:"); err != nil || ws == "" {
			return "", "", "", fmt.Errorf("cmux new-workspace: %v: %s", err, out)
		}
		if tree, err = d.Cmux("tree", "--all"); err != nil {
			return "", "", "", fmt.Errorf("cmux tree: %v", err)
		}
		if surface = firstSurface(tree, ws); surface == "" {
			return "", "", "", fmt.Errorf("cmux tree shows no tab in the new workspace %s", ws)
		}
	}
	if out, err := d.Cmux("workspace-action", "--workspace", ws, "--action", "set-color", "--color", look.color); err != nil {
		return "", "", "", fmt.Errorf("cmux set-color: %v: %s", err, out)
	}
	if out, err := d.Cmux("rename-tab", "--workspace", ws, "--surface", surface, "--", name); err != nil {
		return "", "", "", fmt.Errorf("cmux rename-tab: %v: %s", err, out)
	}
	return ws, surface, fmt.Sprintf("[crius account=%s project=%s role=%s]", account, project, role), nil
}

// find gives the workspace whose name is exactly name, and its first pane.
func find(tree, name string) (ws, pane string) {
	for _, line := range strings.Split(tree, "\n") {
		if m := workspaceRe.FindStringSubmatch(line); m != nil {
			if ws != "" {
				break
			}
			if m[2] == name {
				ws = m[1]
			}
		} else if m := paneRe.FindStringSubmatch(line); m != nil && ws != "" && pane == "" {
			pane = m[1]
		}
	}
	return ws, pane
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
