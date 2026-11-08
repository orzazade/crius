package wakewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Claude is one running Claude Code process: its pid and the arguments and environment it was started with
// (Deps.Claudes); Err when they could not be read.
type Claude struct {
	PID  int
	Args []string
	Env  map[string]string
	Err  error
}

// Variables that make Claude Code bill something other than the folder's sign-in: an API credential, a long-lived
// OAuth token (its plan is not the folder's), or a cloud provider.
var (
	credVars   = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN"}
	switchVars = []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"}
)

// managedSettings is the settings file an admin installs for every Claude Code on the Mac.
var managedSettings = "/Library/Application Support/ClaudeCode/managed-settings.json"

// paid gives an S1 for each Claude Code process that may run on paid usage (owner 11-08, the hardest rule: every model
// run uses one of the three Max subscriptions, never an API key, usage credits or extra paid usage): a credential or a
// cloud provider in its environment or in the env block of its settings (its folder's settings.json, its --settings
// file, the managed file), an apiKeyHelper there, a config folder whose sign-in is not on a Max plan or has none, or a
// sign-in with extra usage (usage credits) on. The plan is the sign-in's own, as its .claude.json has it
// ($CLAUDE_CONFIG_DIR/.claude.json, else $HOME/.claude.json), never the folder's name. A folder's plan alert names
// every process on it under one key. ss names each pid's session.
func paid(d Deps, ss []Session) [][2]string {
	cs, err := d.Claudes()
	if err != nil {
		return [][2]string{{"paid-failed", "the paid-usage check failed to look: " + err.Error()}}
	}
	names := map[int]string{}
	for _, s := range ss {
		names[s.PID] = s.Name
	}
	var out [][2]string
	failed := func(key, what string) {
		out = append(out, [2]string{"paid-failed:" + key, "the paid-usage check failed to look: " + what})
	}
	s1 := func(key, what string) {
		out = append(out, [2]string{"paid:" + key, "S1 paid usage: " + what +
			"; only the Max subscriptions may run (owner's hardest rule): stop it or move it to a Max account"})
	}
	onDir := map[string][]string{} // config folder -> the processes on it
	var dirs []string
	for _, c := range cs {
		who := "claude pid " + strconv.Itoa(c.PID)
		if n := names[c.PID]; n != "" {
			who += " (" + n + ")"
		}
		if c.Err != nil {
			failed(strconv.Itoa(c.PID), who+": "+c.Err.Error())
			continue
		}
		for _, k := range append(credVars, switchVars...) {
			if on(k, c.Env[k]) {
				s1(strconv.Itoa(c.PID)+":"+k, who+" runs with "+k+" set")
			}
		}
		dir, home := c.Env["CLAUDE_CONFIG_DIR"], c.Env["HOME"]
		if dir == "" && home == "" {
			failed(strconv.Itoa(c.PID), who+" has neither CLAUDE_CONFIG_DIR nor HOME, so its sign-in is unknown")
			continue
		}
		user := filepath.Join(dir, "settings.json")
		if dir == "" {
			dir, user = home, filepath.Join(home, ".claude", "settings.json")
		}
		files := []string{user, managedSettings}
		if i := slices.Index(c.Args, "--settings"); i >= 0 && i+1 < len(c.Args) {
			files = append(files, c.Args[i+1])
		}
		for _, f := range files {
			name := f // inline JSON is named, never quoted: it can hold the key itself
			if inline(f) {
				name = "its inline --settings"
			}
			set, err := settingsCreds(f)
			if err != nil {
				failed(strconv.Itoa(c.PID)+":"+name, who+": "+name+": "+err.Error())
			}
			for _, k := range set {
				s1(strconv.Itoa(c.PID)+":"+name+":"+k, who+" runs with "+k+" set in "+name)
			}
		}
		if onDir[dir] == nil {
			dirs = append(dirs, dir)
		}
		onDir[dir] = append(onDir[dir], who)
	}
	for _, dir := range dirs {
		file := filepath.Join(dir, ".claude.json")
		plan, extra, err := signIn(file)
		who := strings.Join(onDir[dir], ", ")
		switch {
		case err != nil:
			failed(dir, who+": "+err.Error())
		case plan == "":
			s1("plan:"+dir, who+" on "+dir+", which has no sign-in")
		case plan != "claude_max":
			s1("plan:"+dir, who+" on "+dir+", signed in on a "+plan+" plan, not Max")
		case extra:
			s1("extra:"+dir, who+" on "+dir+", whose sign-in has extra usage (usage credits) on")
		}
	}
	return out
}

// on says a variable is set: any value but none, "0" or "false" (a cloud switch reads those as off).
func on(name, v string) bool {
	return v != "" && (slices.Contains(credVars, name) || v != "0" && !strings.EqualFold(v, "false"))
}

// settingsCreds names what a settings file (or the inline JSON Claude Code takes for --settings) sets that bills
// elsewhere: a variable of credVars or switchVars in its env block, or apiKeyHelper. A missing file sets nothing.
func settingsCreds(file string) ([]string, error) {
	b := []byte(file)
	if !inline(file) {
		var err error
		if b, err = os.ReadFile(file); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	var s struct {
		Env          map[string]any `json:"env"`
		APIKeyHelper string         `json:"apiKeyHelper"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	var out []string
	for _, k := range append(credVars, switchVars...) {
		if v, ok := s.Env[k]; ok && on(k, fmt.Sprint(v)) {
			out = append(out, k)
		}
	}
	if s.APIKeyHelper != "" {
		out = append(out, "apiKeyHelper")
	}
	return out, nil
}

// inline says a --settings value is JSON itself, not a file's path.
func inline(s string) bool { return strings.HasPrefix(strings.TrimSpace(s), "{") }

// signIn reads the plan of a config folder's sign-in (oauthAccount.organizationType, "" when it has none) and whether
// it has extra usage on. A folder with no .claude.json has no sign-in.
func signIn(file string) (plan string, extra bool, err error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var p struct {
		OAuth *struct {
			Org   string `json:"organizationType"`
			Extra bool   `json:"hasExtraUsageEnabled"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return "", false, fmt.Errorf("%s: %v", file, err)
	}
	if p.OAuth == nil {
		return "", false, nil
	}
	return p.OAuth.Org, p.OAuth.Extra, nil
}
