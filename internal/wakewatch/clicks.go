package wakewatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/watchdog"
)

// clickRepeat is how often a prompt still waiting on the owner's click is posted again.
const clickRepeat = 30 * time.Minute

var (
	// The post-guard's ask (cmd/crius-post-guard) as Claude Code shows a hook's ask, and its reason in brackets: "(az repos
	// pr create --description)". Only these fixed lines count: a command or a diff that quotes the words is no ask.
	clickHookRe = regexp.MustCompile(`Hook PreToolUse:\S+ requires confirmation for this command: Posts text to Azure DevOps under the owner's name: needs the owner's click\.(?: Show the owner the exact text first\.)?(?: \((.+?)\)(?: \[\w+\])?(?: settings\.json| Do you want|$))?`)
	// An Azure DevOps MCP tool's ask: its header "Tool use azure-devops — Repo Create Pull Request Thread Tool: (MCP)", or
	// its rule's line.
	clickToolRe = regexp.MustCompile(`Tool use azure-devops — (.+?)(?: Tool:)? \(MCP\)|Permission rule mcp__azure-devops__(\w+) requires confirmation`)
	// A dialog's option under the cursor: the owner may have moved it off option 1.
	optionRe  = regexp.MustCompile(`^\s*❯\s*\d+\.\s`)
	menuRe    = regexp.MustCompile(`^\s*\d+\.\s`)
	prTitleRe = regexp.MustCompile(`--title[ =]"([^"]+)"|--title[ =]'([^']+)'|(?:^| )title: "([^"]+)"`)
	prIDRe    = regexp.MustCompile(`--(?:id|pull-request-id)[ =](\d+)|pullRequestId: (\d+)`)
	prRepoRe  = regexp.MustCompile(`--repository[ =]"?([\w.-]+)|repositoryId: "([^"]+)"`)
)

// ClickDeps is what one owner-click check needs: Screens reads every tab, deep enough to hold a tall dialog.
type ClickDeps struct {
	Home    string
	Now     time.Time
	Screens func() ([]Screen, error)
}

// flagged is a tab's prompt waiting on the owner's click: its hash, when first seen, when last posted, and when a post
// last reached the bus (Confirmed equals Posted only then: review X4 r2, a failed append must not quiet anything).
type flagged struct {
	Hash                     string
	Since, Posted, Confirmed time.Time
}

// OwnerClick reads a screen's live permission dialog and reports whether only the owner may answer it: the post-guard's
// ask or an Azure DevOps MCP tool's. prompt is the dialog's text (the same prompt gives the same text), what says what it
// asks for, with the PR's title (else its id and repo) when the dialog shows one.
func OwnerClick(text string) (prompt, what string, ok bool) {
	lines := strings.Split(text, "\n")
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if optionRe.MatchString(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", false
	}
	for end > 0 && menuRe.MatchString(lines[end-1]) { // the options above the cursor: the prompt is the same wherever it sits
		end--
	}
	for i := end + 1; i < len(lines)-1; i++ { // an input box below it: the dialog was answered, this is history
		if borderRe.MatchString(strings.TrimSpace(lines[i])) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			return "", "", false
		}
	}
	start := 0 // the dialog's top border, or the screen's top when the dialog is taller than what was read
	for i := end - 1; i >= 0; i-- {
		if m := borderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil && m[1] == "" {
			start = i + 1
			break
		}
	}
	var words []string
	for _, l := range lines[start:end] {
		words = append(words, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "│")))
	}
	prompt = strings.Join(strings.Fields(strings.Join(words, " ")), " ")
	if m := clickHookRe.FindStringSubmatch(prompt); m != nil {
		what = "post-guard ask"
		if m[1] != "" {
			what += " (" + m[1] + ")"
		}
	} else if m := clickToolRe.FindStringSubmatch(prompt); m != nil {
		what = "Azure DevOps tool " + m[1] + m[2]
	} else {
		return "", "", false
	}
	if m := prTitleRe.FindStringSubmatch(prompt); m != nil {
		what += fmt.Sprintf("; PR %q", m[1]+m[2]+m[3])
	} else if m := prIDRe.FindStringSubmatch(prompt); m != nil {
		what += "; PR " + m[1] + m[2]
		if r := prRepoRe.FindStringSubmatch(prompt); r != nil {
			what += " in " + r[1] + r[2]
		}
	}
	return prompt, what, true
}

// tabName is the session name on a dialog's bottom border ("quotes · frontend"), else the tab's title.
func tabName(s Screen) string {
	lines := strings.Split(s.Text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := borderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil && m[1] != "" {
			return m[1]
		}
	}
	return s.Title
}

// Clicks checks every tab once for a prompt only the owner may answer (owner 11-01 19:1x: Quotes tabs waited on his
// approval unseen). A new one goes on Crius's bus as one OWNER-CLICK line at once, and again every 30 min while it
// waits; the next prompt in that tab is new. A tab of a project whose projects.toml says clicks = "crius" gets CRIUS-CLICK
// instead, which Crius answers with no owner push (owner 11-01 22:4x: the quotes tabs). A tab that cannot be read keeps
// what it had. launchd runs it every minute.
// A run that fails says so on the bus as an ALERT, once per error (kept in state/owner-clicks.error): its log is read by
// nobody.
func Clicks(d ClickDeps) error {
	last := filepath.Join(d.Home, "state", "owner-clicks.error")
	err := clicks(d)
	if err == nil {
		if rerr := os.Remove(last); rerr != nil && !os.IsNotExist(rerr) {
			return rerr
		}
		return nil
	}
	msg, _ := redact.Redact(err.Error())
	if b, rerr := os.ReadFile(last); rerr != nil || strings.TrimSuffix(string(b), "\n") != msg {
		if perr := watchdog.Post(d.Home, d.Now, "owner-click watch failed: "+msg); perr != nil {
			return fmt.Errorf("%w; the ALERT was not posted: %v", err, perr)
		}
		if werr := os.WriteFile(last, []byte(msg+"\n"), 0o600); werr != nil {
			return fmt.Errorf("%w; %v", err, werr)
		}
	}
	return err
}

func clicks(d ClickDeps) error {
	path := filepath.Join(d.Home, "state", "owner-clicks.json")
	seen := map[string]flagged{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &seen); err != nil {
			// Afresh posts each waiting prompt once more; stopping at the bad file would never post one again. The fresh
			// file is written at once, so a run that fails later does not say this again.
			seen = map[string]flagged{}
			if err := watchdog.Post(d.Home, d.Now, fmt.Sprintf("owner-click state %s was unreadable (%v); started afresh", path, err)); err != nil {
				return err
			}
			if err := writeJSON(path, seen); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	crius, cerr := criusClicks(d.Home)
	screens, err := d.Screens()
	if err != nil {
		return err
	}
	next := map[string]flagged{}
	var posts, posted []string
	for _, s := range screens {
		if s.Err != "" {
			if f, ok := seen[s.Surface]; ok {
				next[s.Surface] = f
			}
			continue
		}
		prompt, what, ok := OwnerClick(s.Text)
		if !ok {
			continue
		}
		f := seen[s.Surface]
		if h := hash(promptKey(prompt)); f.Hash != h {
			f = flagged{Hash: h, Since: d.Now}
		}
		if d.Now.Sub(f.Posted) >= clickRepeat {
			typ, whose := "OWNER-CLICK", "your"
			if p, _, _ := strings.Cut(tabName(s), " · "); crius[strings.ToLower(strings.TrimSpace(p))] {
				typ, whose = "CRIUS-CLICK", "Crius's"
			}
			text := fmt.Sprintf("%s (%s) waits on %s click since %s: %s", tabName(s), s.Surface, whose, f.Since.Format("15:04"), what)
			if f.Posted.IsZero() {
				text = fmt.Sprintf("%s (%s) waits on %s click: %s", tabName(s), s.Surface, whose, what)
			}
			if r := []rune(Clean(text)); len(r) > store.MaxLen {
				text = string(r[:store.MaxLen-1]) + "…"
			} else {
				text = string(r)
			}
			line, err := store.FormatLine(typ, "crius", "wakewatch", text, "crius:lead", d.Now)
			if err != nil {
				return err
			}
			posts = append(posts, line)
			posted = append(posted, s.Surface)
			f.Posted = d.Now
		}
		next[s.Surface] = f
	}
	// The state goes first: a post that then fails is said once as an ALERT; a state that failed after the posts would
	// post them again every minute.
	if err := writeJSON(path, next); err != nil {
		return err
	}
	if len(posts) == 0 {
		return cerr
	}
	bus := filepath.Join(d.Home, "bus", "crius.log")
	f, err := os.OpenFile(bus, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	text := strings.Join(posts, "\n") + "\n"
	// A bus that ends mid-line (a writer died) gets a newline first, so each line starts with its stamp.
	if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
		b := make([]byte, 1)
		if _, err := f.ReadAt(b, fi.Size()-1); err == nil && b[0] != '\n' {
			text = "\n" + text
		}
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	for _, s := range posted {
		e := next[s]
		e.Confirmed = e.Posted
		next[s] = e
	}
	if err := writeJSON(path, next); err != nil {
		return err
	}
	return cerr
}

// criusClicks gives the projects whose Azure DevOps prompts Crius answers, not the owner: clicks = "crius" in
// projects.toml (owner 11-01 22:4x: the quotes tabs, AGENTS.md 1205fdc). A file that cannot be read gives none, so every
// click goes to the owner, and its error, which the run returns after posting.
func criusClicks(home string) (map[string]bool, error) {
	out := map[string]bool{}
	var c struct {
		Projects map[string]struct{ Clicks string }
	}
	if _, err := toml.DecodeFile(filepath.Join(home, "projects.toml"), &c); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return out, nil
		}
		return out, fmt.Errorf("projects.toml cannot be read, so every click went to the owner: %w", err)
	}
	for p, pc := range c.Projects {
		if pc.Clicks == "crius" {
			out[strings.ToLower(p)] = true
		}
	}
	return out, nil
}

// promptKey is a prompt without spaces and box lines: a pane of another width wraps a long word elsewhere and draws its
// ─ and ╌ lines as long as it is wide.
func promptKey(prompt string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r >= 0x2500 && r <= 0x257F {
			return -1
		}
		return r
	}, prompt)
}

// writeJSON writes v to path through a temporary file, so a reader never sees half of it.
func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// clickFresh is how old the owner-click state may be: the clicks job rewrites it every minute, so an older one means the
// job stopped and nothing repeats the prompt (review X4).
const clickFresh = 3 * time.Minute

// ClickFlagged says whether a tab's live prompt is one OWNER-CLICK posted (Crius 11-01 20:02: 9 repeat wakes 19:27-20:00
// for 4 known Azure DevOps clicks): OWNER-CLICK's own 30-min repeat covers it then. read gives the tab as deep as the
// clicks run reads it, and is called only when the state holds a prompt whose post reached the bus. Another prompt in that tab,
// a read that fails, or a state file that cannot be read or is older than clickFresh is not flagged and alerts as before.
func ClickFlagged(home string, now time.Time, surface string, read func() (string, error)) bool {
	path := filepath.Join(home, "state", "owner-clicks.json")
	fi, err := os.Stat(path)
	if err != nil || now.Sub(fi.ModTime()) > clickFresh {
		return false
	}
	var seen map[string]flagged
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &seen) != nil {
		return false
	}
	f, ok := seen[surface]
	if !ok || f.Posted.IsZero() || !f.Confirmed.Equal(f.Posted) {
		return false
	}
	text, err := read()
	if err != nil {
		return false
	}
	prompt, _, ok := OwnerClick(text)
	return ok && f.Hash == hash(promptKey(prompt))
}
