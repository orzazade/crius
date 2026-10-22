// Package wakewatch tells the owner when Crius missed a wake: a lead's DONE, NEED, CLEAR-READY or ASK to Crius with no
// crius:lead line after it on the same bus for 20 min, a lead tab waiting on a question or dialog for 10 min, or any
// tab of a team idle for 10 min (noting when its last reply says CLEAR-READY), the
// Mac short of disk or deep in swap, and a tool the team needs (cmux, gh, az, bun, docker) that fails or does not answer, and a lead still in the same session
// 20 min after its DONE. When ~/.crius/wakewatch-projects exists, only the projects it names are watched.
// It runs as its own launchd job, outside
// any chat, and wakes Crius by typing one line into Crius's own tab (when it cannot, the line goes on crius.log as an
// ALERT, which Crius's waiter wakes on): Crius then acts, and sends the owner a push
// notification when the owner is needed (owner 10-21 23:5x: no Telegram). Like the watchdog it imports nothing from
// the ingest or the store.
package wakewatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/watchdog"
)

const (
	wakeAfter   = 20 * time.Minute
	screenAfter = 10 * time.Minute
	idleAfter   = 10 * time.Minute // any tab of a team, lead or not (owner, ORDER 10-22 13:13)
	window      = 12 * time.Hour   // older lines are history: a first run must not wake Crius for them
	resend      = 20 * time.Minute // an item still open wakes Crius again, never more often
	freshAfter  = 20 * time.Minute // a lead's DONE with no new session of that lead this long wakes Crius
	minFreeGB   = 20               // less free disk than this wakes Crius
	maxSwapGB   = 20               // more swap in use than this wakes Crius
	MaxLine     = 500

	Prefix        = "WAKEWATCH: "
	CriusTitle    = "crius" // the cmux title of Crius's own tab: waiting() skips it, and alerts are typed into it
	screensFailed = "screens-failed"
)

// wakeTypes ask Crius to act; replyTypes from crius:lead show it woke.
var (
	wakeTypes  = map[string]bool{"DONE": true, "NEED": true, "CLEAR-READY": true, "ASK": true}
	replyTypes = map[string]bool{"ORDER": true, "ANSWER": true, "VERIFIED": true, "ACK": true, "INFO": true, "GO": true, "OWNER": true}
	lineRe     = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\dZ?) ([A-Z][A-Z-]*) (\S+)(?: -> (.*?))? — `)
	// A Claude Code permission or choice dialog: a numbered option under the cursor.
	dialogRe = regexp.MustCompile(`(?m)^\s*❯\s*1\.\s`)
	// The input box's top border carries the session name: "──── quotes: lead ─".
	borderRe  = regexp.MustCompile(`^─+(?: (.+?) ─+)?$`)
	sessionRe = regexp.MustCompile(`^([A-Za-z0-9][\w.-]*)\s*[:·]\s*([\w.-]+)$`)
	// Under the input box: background shells, monitors, tasks or agents still running.
	jobsRe = regexp.MustCompile(`\b\d+ (?:shells?|monitors?|background tasks?|agents?)\b|(?m)^\s*◯ `)
	leadRe = regexp.MustCompile(`^lead(?:er)?$`)
	// The status line's context use: "██░░░░░░░░ ctx 21%".
	ctxRe = regexp.MustCompile(`\bctx (\d{1,3})%`)
)

// Screen is one tab. Err is set when its read failed; Text is then empty. ID is cmux's surface id (a UUID), which a
// session in the tab has as CMUX_SURFACE_ID: crius-hook's state file names it.
type Screen struct{ Surface, ID, Title, Text, Err string }

type Deps struct {
	Home       string
	Now        time.Time
	Screens    func() ([]Screen, error)
	Send       func(text string) error
	Resources  func() (freeDiskGB, swapUsedGB float64, err error)
	Tools      func() map[string]error // each tool the team needs, and why it failed or did not answer (nil: fine)
	Sessions   func() ([]Session, error)
	Workspaces func() ([]Workspace, error)
	States     func() ([]sessionstate.State, error) // crius-hook's file of each session (package sessionstate)
}

// Workspace is one cmux workspace: Named is false while it shows a default title, and Color is "" when none is set.
type Workspace struct {
	Ref, Title string
	Named      bool
	Color      string
}

// Session is a live Claude Code session: its name ("quotes: lead"), id, and when it started (zero: nothing written yet).
type Session struct {
	Name, ID string
	Started  time.Time
}

type seenScreen struct {
	Hash  string
	Since time.Time
	Idle  bool // its idle alert is raised; a bus line from it lowers it, so the next idle spell alerts again
}

type state struct {
	Alerted     map[string]time.Time // key -> when last typed, or posted on crius.log when typing failed
	Screens     map[string]seenScreen
	SendFailing bool
	Views       map[string][2]string // check -> what the screen and crius-hook's state said on the last run
	HookLog     int64                // how much of crius-hook.log was already reported
}

// Run checks every bus log and every tab once and sends each alert that is new, or still open 20 min after it was
// last sent, as one line. A line that cannot be typed goes on crius.log as an ALERT instead, and the first failure in a
// row also says why there.
func Run(d Deps) error {
	path := filepath.Join(d.Home, "state", "wakewatch.json")
	st := state{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			// Starting afresh can repeat an alert; stopping at the bad file would silence every later one.
			st = state{}
			if err := watchdog.Post(d.Home, d.Now, fmt.Sprintf("wakewatch state %s was unreadable (%v); started afresh", path, err)); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if st.Alerted == nil {
		st.Alerted = map[string]time.Time{}
	}
	if st.Screens == nil {
		st.Screens = map[string]seenScreen{}
	}

	watch, alerts := watched(d.Home)
	park, pa := readParked(d.Home)
	alerts = append(alerts, pa...)
	m, err := missed(d.Home, d.Now, watch)
	if err != nil {
		return err
	}
	alerts = append(alerts, m...)
	ss, sesErr := d.Sessions()
	hv, ha := readHooks(d, st.Views, ss, sesErr)
	alerts = append(alerts, ha...)
	screens, err := d.Screens()
	if err != nil {
		alerts = append(alerts, [2]string{screensFailed, "the tab check failed to look: " + err.Error()})
		hv.blind = true
	} else {
		alerts = append(alerts, waiting(&st, d, watch, park, screens, hv)...)
	}
	alerts = append(alerts, resources(d)...)
	alerts = append(alerts, tools(d)...)
	alerts = append(alerts, fresh(d, watch, park, screens, ss, sesErr, hv)...)
	alerts = append(alerts, unnamed(d, screens)...)
	alerts = append(alerts, hookLog(d.Home, &st)...)
	hv.finish()
	st.Views = hv.views
	if err := appendLines(filepath.Join(d.Home, "logs", "wakewatch-state.log"), hv.log); err != nil {
		st.Views = hv.prev // the log holds none of this run's lines: the next run logs them anew, a "gone" line too (OF6)
		alerts = append(alerts, [2]string{"statelog-failed", "wakewatch could not write its screen-versus-state log: " + err.Error()})
	}

	active := map[string]bool{}
	var sendErr error
	tried := false
	for _, a := range alerts {
		active[a[0]] = true
		if last, ok := st.Alerted[a[0]]; ok && d.Now.Sub(last) < resend {
			continue
		}
		// Send stores the line before it types it, so it gets every alert: one it holds is typed on a later run.
		tried = true
		text := Clean(a[1])
		err := d.Send(Prefix + text)
		if err != nil && sendErr == nil && !st.SendFailing {
			if err := watchdog.Post(d.Home, d.Now, "wakewatch cannot reach Crius's tab: "+err.Error()); err != nil {
				return err
			}
		}
		if err != nil {
			sendErr = err
		}
		// Crius's waiter wakes on a bus line: a wake that cannot be typed goes there, so none is lost (S2, 10-22).
		if err != nil {
			if err := watchdog.Post(d.Home, d.Now, text); err != nil {
				return err
			}
		}
		st.Alerted[a[0]] = d.Now
	}
	if tried { // a run with nothing to send says nothing about the tab: the streak goes on
		st.SendFailing = sendErr != nil
	}
	// A key goes once its alert is no longer raised (line answered or past the window, tab stopped asking, check
	// works again), so the same thing alerts at once when it comes back; a lasting one keeps its 20 min rhythm.
	for surface, s := range st.Screens {
		active[screenKey(surface, s.Hash)] = true
		if s.Idle {
			active[idleKey(surface, s.Hash)] = true
		}
	}
	if active[screensFailed] { // no tab was read: a tab already reported unreadable has not recovered
		for k := range st.Alerted {
			if strings.HasPrefix(k, "screen-failed:") {
				active[k] = true
			}
		}
	}
	for k := range st.Alerted {
		if !active[k] {
			delete(st.Alerted, k)
		}
	}
	b, _ := json.Marshal(st)
	tmp, err := os.CreateTemp(filepath.Dir(path), "wakewatch.json.")
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
		return werr
	}
	return sendErr
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// projects is the set of projects watched; nil watches every project.
type projects map[string]bool

func (p projects) has(project string) bool { return p == nil || p[strings.ToLower(project)] }

// watched reads home/wakewatch-projects: the project names (bus file names: quotes, crius), split by spaces, commas or
// lines, with # starting a comment, in any case (owner 10-22: only Quotes and Crius are watched). With no file every
// project is watched. A file that cannot be read or names no project would silence every team: it is an alert, and
// every project stays watched. A name with no bus file would silence its team: it is an alert too.
func watched(home string) (projects, [][2]string) {
	path := filepath.Join(home, "wakewatch-projects")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, [][2]string{{"projects-failed", "the watched project list could not be read, so every project is watched: " + err.Error()}}
	}
	p := projects(names(b))
	if len(p) == 0 {
		return nil, [][2]string{{"projects-failed", path + " names no project, so every project is watched"}}
	}
	var out [][2]string
	for _, name := range slices.Sorted(maps.Keys(p)) {
		if _, err := os.Stat(filepath.Join(home, "bus", name+".log")); err != nil {
			out = append(out, [2]string{"projects-unknown:" + name, path + " names " + name + ", which has no bus file, so that team is not watched: " + err.Error()})
		}
	}
	return p, out
}

// names reads a list of names split by spaces, commas or lines, with # starting a comment, in lower case.
func names(b []byte) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line, _, _ = strings.Cut(line, "#")
		for _, name := range strings.FieldsFunc(line, func(c rune) bool { return c == ',' || unicode.IsSpace(c) }) {
			out[strings.ToLower(name)] = true
		}
	}
	return out
}

// parked is the set of roles ("quotes:p4") and whole projects ("quotes") Crius stopped on purpose.
type parked map[string]bool

// has says whether a tab's bus name or its project is parked.
func (p parked) has(who, team string) bool {
	return p[strings.ToLower(who)] || p[strings.ToLower(team)] || p[strings.ToLower(project(who))]
}

// readParked reads home/parked, a list as in wakewatch-projects (Crius 10-22 20:40: P4 parked, and Quotes pauses whole
// at 70% of the weekly quota): Crius adds a role or project when it parks it and removes it on resume. A parked tab
// wakes no one when idle or asking, and a parked lead needs no fresh start. Not a field in roles/*.json: crius-hook
// rewrites that file on every event. A file that cannot be read parks nothing and is an alert. A tab with no bus name
// whose title is one word ("✳ quotes") counts as the lead, so a parked quotes:lead quiets any such tab; a lead under
// another name (quotes:leader) is parked by that exact name.
func readParked(home string) (parked, [][2]string) {
	path := filepath.Join(home, "parked")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, [][2]string{{"parked-failed", "the parked list could not be read, so no role is parked: " + err.Error()}}
	}
	return names(b), nil
}

// project gives the project of a bus name ("quotes:lead" -> "quotes").
func project(who string) string {
	p, _, _ := strings.Cut(who, ":")
	return p
}

// missed gives [key, text] for each wake line to Crius, 20 min to 12 h old, with no reply line after it in its file.
func missed(home string, now time.Time, watch projects) ([][2]string, error) {
	logs, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(logs)
	var out [][2]string
	for _, log := range logs {
		if !watch.has(strings.TrimSuffix(filepath.Base(log), ".log")) {
			continue
		}
		b, err := os.ReadFile(log)
		if err != nil {
			return nil, err
		}
		// Only whole lines: a last line with no newline may still be being written.
		text := string(b[:bytes.LastIndexByte(b, '\n')+1])
		var open []string
		for _, line := range strings.Split(text, "\n") {
			m := lineRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			typ, from, to := m[2], m[3], m[4]
			if from == "crius:lead" && replyTypes[typ] {
				open = nil
				continue
			}
			if !wakeTypes[typ] || !toCrius(to) {
				continue
			}
			// A team's line on another team's bus ("aitch:build" on crius.log) goes by the sender's team.
			if team, _, ok := strings.Cut(from, ":"); ok && !watch.has(team) {
				continue
			}
			at, err := lineTime(m[1])
			if err != nil {
				continue
			}
			if age := now.Sub(at); age > wakeAfter && age <= window {
				open = append(open, line)
			}
		}
		for _, line := range open {
			out = append(out, [2]string{"bus:" + hash(log+"\n"+line), filepath.Base(log) + ": " + line})
		}
	}
	return out, nil
}

func toCrius(to string) bool {
	return sentTo(to, "crius:lead") || sentTo(to, "crius")
}

// sentTo reports whether a line's recipients ("a, b", "a b" or "a (cc b)", as the bus grammar allows) name who.
func sentTo(to, who string) bool {
	for _, r := range recipients(to) {
		if r == who {
			return true
		}
	}
	return false
}

func recipients(to string) []string {
	return strings.FieldsFunc(to, func(c rune) bool { return c == ',' || c == ' ' || c == '(' || c == ')' })
}

func lineTime(s string) (time.Time, error) {
	if strings.HasSuffix(s, "Z") {
		return time.Parse("2006-01-02T15:04Z", s)
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

// waiting gives [key, text] for each tab whose screen has waited on a question or dialog, unchanged, for 10 min, and
// for each idle tab: unchanged and with no bus line of its own for 10 min, any role, unless the bus parks it
// (parkedOnBus). A screen counts as unchanged by
// what is above its input box: the status line below it holds a clock. A tab whose read failed is an alert of its own
// and keeps its place meanwhile (Run reports a failed read of all tabs). When a live session's crius-hook state names
// the tab, the screen and the state each say whether it asks, works or is idle; the mode file says which one decides.
func waiting(st *state, d Deps, watch projects, park parked, screens []Screen, hv *hooks) [][2]string {
	var out [][2]string
	live := map[string]bool{}
	for _, s := range screens {
		// A tab of a team not watched stays quiet. A dialog hides the named input box, and a tab that cannot be read
		// shows nothing: then the title's first word that starts with a letter or digit names the project (a lead tab
		// is titled "quotes"; a mark like "✳" may come first). A tab that cannot be read and has no such word still
		// alerts: nothing says whose it is.
		if s.Err != "" {
			hv.keep("tab:" + s.Surface) // unread, whatever its title now says: its pair stays as last seen (OF6)
		}
		name, _ := session(s.Text, s.Title)
		team := project(name)
		parkAs := name
		if name == "" {
			var words []string
			for _, w := range strings.Fields(strings.NewReplacer(":", " ", "·", " ").Replace(s.Title)) {
				if r, _ := utf8.DecodeRuneInString(w); unicode.IsLetter(r) || unicode.IsDigit(r) {
					words = append(words, w)
				}
			}
			if len(words) > 0 {
				team = words[0]
			}
			if len(words) == 1 { // "✳ quotes" is the lead's tab: a parked lead stays quiet (review round 13)
				parkAs = team + ":lead"
			}
		}
		if watch != nil && !watch.has(team) && (s.Err == "" || team != "") {
			continue
		}
		if s.Err != "" {
			live[s.Surface] = true
			out = append(out, [2]string{"screen-failed:" + s.Surface, fmt.Sprintf("tab %q (%s) could not be read: %s", s.Title, s.Surface, s.Err)})
			continue
		}
		if s.Title == CriusTitle || park.has(parkAs, team) {
			continue
		}
		body, _, _ := split(s.Text)
		view := screenView(s.Text)
		if hs, ok := hv.surface[s.ID]; ok {
			view = hv.pick("tab:"+s.Surface, s.Title, view, stateView(hs, s.Text))
		}
		if view == "busy" {
			continue
		}
		ask := view == "asks"
		var who string
		var clear bool
		var lastLine, unparked time.Time
		if !ask {
			if who, _ = session(s.Text, s.Title); who == "" {
				continue
			}
			clear = clearReady(body)
			var found bool
			var err error
			if lastLine, found, err = lastFrom(d.Home, who, ""); err != nil {
				live[s.Surface] = true
				out = append(out, [2]string{"screen-failed:" + s.Surface, fmt.Sprintf("tab %q (%s): idle check could not read its bus: %v", s.Title, s.Surface, err)})
				continue
			} else if !found {
				continue // no bus for this project: not a team Crius runs
			}
			var p bool
			if p, unparked, err = parkedOnBus(d.Home, who); err != nil {
				live[s.Surface] = true
				out = append(out, [2]string{"screen-failed:" + s.Surface, fmt.Sprintf("tab %q (%s): idle check could not read its bus: %v", s.Title, s.Surface, err)})
				continue
			} else if p {
				continue // its idle spell starts afresh once a newer line unparks it
			}
		}
		live[s.Surface] = true
		h := hash(name + "\n" + body) // another session in the same tab starts afresh
		seen, ok := st.Screens[s.Surface]
		if !ok || seen.Hash != h {
			st.Screens[s.Surface] = seenScreen{Hash: h, Since: d.Now}
			continue
		}
		if ask {
			if wait := d.Now.Sub(seen.Since); wait >= screenAfter {
				out = append(out, [2]string{screenKey(s.Surface, h),
					fmt.Sprintf("tab %q (%s) waits on a question or dialog for %d min", s.Title, s.Surface, int(wait.Minutes()))})
			}
			continue
		}
		from := seen.Since
		if lastLine.After(from) {
			from = lastLine
		}
		if unparked.After(from) {
			from = unparked
		}
		seen.Idle = d.Now.Sub(from) >= idleAfter
		st.Screens[s.Surface] = seen
		if wait := d.Now.Sub(from); seen.Idle {
			text := fmt.Sprintf("%s (tab %q, %s) is idle: not working, nothing running, no bus line from it for %d min", who, s.Title, s.Surface, int(wait.Minutes()))
			if lastLine.IsZero() { // Quotes builders post as "P3": only the screen's age is known
				text = fmt.Sprintf("%s (tab %q, %s) is idle: not working, nothing running, screen unchanged for %d min, no bus line from %s", who, s.Title, s.Surface, int(wait.Minutes()), who)
			}
			if clear {
				text += "; its screen says CLEAR-READY"
			}
			out = append(out, [2]string{idleKey(s.Surface, h), text})
		}
	}
	for k := range st.Screens {
		if !live[k] {
			delete(st.Screens, k)
		}
	}
	return out
}

// resources gives an alert when free disk is below 20 GB or swap in use above 20 GB, or when they could not be read.
func resources(d Deps) [][2]string {
	free, swap, err := d.Resources()
	if err != nil {
		return [][2]string{{"resources-failed", "the disk and swap check failed to look: " + err.Error()}}
	}
	var out [][2]string
	if free < minFreeGB {
		out = append(out, [2]string{"disk", fmt.Sprintf("disk: %.1f GB free, below %d GB", free, minFreeGB)})
	}
	if swap > maxSwapGB {
		out = append(out, [2]string{"swap", fmt.Sprintf("swap: %.1f GB in use, above %d GB", swap, maxSwapGB)})
	}
	return out
}

// fresh gives an alert for each lead whose newest DONE (in the last 12 h) is 20 min old while no session of that lead
// started after it and Crius sent it no ORDER after it: no /clear and no relaunch (cmux-team SKILL §5, fresh context
// per item). A lead whose tabs all show ctx under 50% in the status line below the input box has room left and needs
// no fresh start yet (Crius 10-22 13:23). Nor does a lead whose crius-hook state says it works, when the state decides.
func fresh(d Deps, watch projects, park parked, screens []Screen, ss []Session, err error, hv *hooks) [][2]string {
	used := map[string]int{} // the most any tab of that session shows
	text := map[string]string{}
	unread := map[string]bool{} // tabs cmux could not read: they show no text to judge from
	for _, s := range screens {
		if who, _ := session(s.Text, s.Title); who != "" {
			used[who] = max(used[who], ctxUsed(s.Text))
		}
		if s.ID != "" {
			text[s.ID] = s.Text
			unread[s.ID] = s.Err != ""
		}
	}
	if err != nil {
		return [][2]string{{"sessions-failed", "the lead fresh-start check failed to look: " + err.Error()}}
	}
	newest := map[string]time.Time{}
	ids := map[string][]string{} // the lead's own sessions, by id: a child claude can carry the lead's project and role
	var whos []string
	for _, s := range ss {
		m := sessionRe.FindStringSubmatch(strings.TrimSpace(s.Name))
		if m == nil || !leadRe.MatchString(m[2]) || !watch.has(m[1]) || park.has(m[1]+":"+m[2], m[1]) {
			continue
		}
		who, started := m[1]+":"+m[2], s.Started
		if started.IsZero() {
			started = d.Now // nothing written yet: it has just started
		}
		if _, ok := newest[who]; !ok {
			whos = append(whos, who)
		}
		if started.After(newest[who]) {
			newest[who] = started
		}
		ids[who] = append(ids[who], s.ID)
	}
	sort.Strings(whos)
	var out [][2]string
	for _, who := range whos {
		at, found, err := lastFrom(d.Home, who, "DONE")
		if err != nil {
			out = append(out, [2]string{"fresh-failed:" + who, fmt.Sprintf("the lead fresh-start check failed to look at %s's bus: %v", who, err)})
			hv.keep("fresh:" + who)
			continue
		}
		u, shown := used[who]
		if !found || at.IsZero() || d.Now.Sub(at) < freshAfter || d.Now.Sub(at) > window || newest[who].After(at) ||
			shown && u < 50 {
			continue
		}
		// Crius sent the lead back to work after the DONE (Crius 10-22 01:07): a decision, so no alert. An ACK is not.
		ordered, err := orderAfterDone(d.Home, who)
		if err != nil {
			out = append(out, [2]string{"fresh-failed:" + who, fmt.Sprintf("the lead fresh-start check failed to look at %s's bus: %v", who, err)})
			hv.keep("fresh:" + who)
			continue
		}
		if ordered {
			continue
		}
		var hs []sessionstate.State
		for _, id := range ids[who] {
			if s, ok := hv.session[id]; ok {
				hs = append(hs, s)
				if unread[s.Surface] {
					hv.keep("fresh:" + who) // its pair stays as last seen (OF6)
				}
			}
		}
		if len(hs) > 0 {
			state := "alert"
			for _, s := range hs {
				if stateView(s, text[s.Surface]) != "idle" {
					state = "none"
				}
			}
			if hv.pick("fresh:"+who, who+" fresh start", "alert", state) == "none" {
				continue
			}
		}
		when := at.Format("2006-01-02T15:04")
		out = append(out, [2]string{"fresh:" + who + ":" + when, fmt.Sprintf("%s posted DONE at %s and has had no fresh "+
			"start since (no /clear or relaunch in %d min; cmux-team SKILL §5: fresh context per item)", who, when,
			int(d.Now.Sub(at).Minutes()))})
	}
	return out
}

// tools gives one alert per tool that failed or did not answer.
func tools(d Deps) [][2]string {
	res := d.Tools()
	names := make([]string, 0, len(res))
	for name, err := range res {
		if err != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out [][2]string
	for _, name := range names {
		out = append(out, [2]string{"tool:" + name, fmt.Sprintf("tool check: %s does not answer: %v", name, res[name])})
	}
	return out
}

func screenKey(surface, hash string) string { return "screen:" + surface + ":" + hash }
func idleKey(surface, hash string) string   { return "idle:" + surface + ":" + hash }

// Clean makes text safe to type into a Claude prompt as one line: secrets redacted, one line, at most MaxLine runes.
func Clean(s string) string {
	// Redact first: a cut could split a secret so the redactor no longer sees it, and redaction can grow the text.
	// The line is typed: cmux send reads a backslash-n or -r as Enter, so no backslash and no line break reach it.
	text, _ := redact.Redact(s)
	// cmux reads \n as Enter, and control bytes (Ctrl-U, Esc) would edit Crius's prompt: neither may pass.
	text = strings.Join(strings.Fields(strings.Map(printable, text)), " ")
	if r := []rune(text); len(r) > MaxLine {
		text = string(r[:MaxLine]) + "…"
	}
	return text
}

func printable(r rune) rune {
	if r == '\\' {
		return '/'
	}
	if !unicode.IsPrint(r) {
		return ' '
	}
	return r
}

// queuedHint is what Claude shows in an empty input box while messages sent mid-turn wait in its queue.
const queuedHint = "Press up to edit queued messages"

// Prompt gives the text in a Claude Code screen's input box, and whether the screen has one. A box showing only the
// queued-messages hint is empty.
func Prompt(screen string) (string, bool) {
	_, foot, boxed := split(screen)
	if !boxed {
		return "", false
	}
	var lines []string
	for i, line := range strings.Split(foot, "\n") {
		if line = strings.TrimSpace(line); i > 0 && borderRe.MatchString(line) {
			break
		}
		lines = append(lines, line)
	}
	draft := strings.TrimSpace(strings.TrimPrefix(strings.Join(lines, "\n"), "❯"))
	if draft == queuedHint {
		return "", true
	}
	return draft, true
}

// split cuts a Claude Code screen at its input box: what is above it, what is below it, and whether it has one.
func split(text string) (body, foot string, boxed bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 2; i >= 0; i-- {
		if borderRe.MatchString(strings.TrimSpace(lines[i])) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return text, "", false
}

// ctxUsed gives the context % a screen's status line shows below the input box's bottom border (the last border line:
// a draft may hold a dash line), or 100 when it shows none. Text typed in the box does not count.
func ctxUsed(text string) int {
	_, foot, boxed := split(text)
	lines := strings.Split(foot, "\n")
	for i := len(lines) - 1; boxed && i >= 1; i-- {
		if borderRe.MatchString(strings.TrimSpace(lines[i])) {
			if m := ctxRe.FindStringSubmatch(strings.Join(lines[i+1:], "\n")); m != nil {
				n, _ := strconv.Atoi(m[1])
				return n
			}
			break
		}
	}
	return 100
}

// session gives the bus name ("quotes:lead") and role of a tab, from the input box border or else the tab title.
func session(text, title string) (who, role string) {
	name := title
	lines := strings.Split(text, "\n")
	for i := len(lines) - 2; i >= 0; i-- {
		if m := borderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil && m[1] != "" &&
			strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			name = m[1]
			break
		}
	}
	m := sessionRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return "", ""
	}
	return m[1] + ":" + m[2], m[2]
}

// clearReady reports whether the last reply above the input box says CLEAR-READY. A screen with no reply marker has
// no last reply to read.
func clearReady(body string) bool {
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "⏺") {
			return strings.Contains(strings.Join(lines[i:], "\n"), "CLEAR-READY")
		}
	}
	return false
}

// lastFrom gives the time of the newest whole line sent by who ("quotes:lead") on its project's bus, of type typ or
// any type for "". found is false when the project has no bus.
func lastFrom(home, who, typ string) (last time.Time, found bool, err error) {
	lines, found, err := busLines(home, who)
	for _, m := range lines {
		if m[3] != who || typ != "" && m[2] != typ {
			continue
		}
		if at, err := lineTime(m[1]); err == nil && at.After(last) {
			last = at
		}
	}
	return last, found, err
}

// orderAfterDone reports whether crius:lead sent who an ORDER below who's last DONE in its project's bus log. The
// file's order counts, not the minute: both lines often carry the same one.
func orderAfterDone(home, who string) (bool, error) {
	lines, _, err := busLines(home, who)
	ordered := false
	for _, m := range lines {
		switch {
		case m[3] == who && m[2] == "DONE":
			ordered = false
		case m[3] == "crius:lead" && m[2] == "ORDER" && sentTo(m[4], who):
			ordered = true
		}
	}
	return ordered, err
}

// parkedOnBus reports whether who's newest line on its project's bus, sent by it or to it by name, parks it (Crius
// 10-23 00:26; ORDER 10-22 01:49: cortex:lead, parked by an ORDER, drew idle wakes): a PARKED or PAUSE line, or one
// with a sentence that is a park ("Park now, waiter armed.", "Stay parked: no demo item", "parked (quota): …"). A park
// goes down, from crius:lead to a project's lead to its other roles, or up from the role it parks ("Parked until QA"
// to its lead): the lead that parks a worker is not parked by it. A sentence that only mentions parking ("then park"
// after work, "P2 is parked", "park it in refs/wip") does not count: a role told to work first still wakes when idle.
// Lines to <project>:all neither park nor unpark: a whole project is ~/.crius/parked. unparked is the minute of the
// line that ended the newest park, where an idle spell starts afresh (review: a park and an unpark between two runs).
func parkedOnBus(home, who string) (parked bool, unparked time.Time, err error) {
	lines, _, err := busLines(home, who)
	project := strings.SplitN(who, ":", 2)[0]
	for _, m := range lines {
		from, to := busName(project, m[3]), recipients(m[4])
		for i, r := range to {
			to[i] = busName(project, r)
		}
		aimed := false // a park on this line would park who
		switch {
		case from == who: // sent up: every recipient is above who ("thanks. Park now." to P5 cc Crius parks P5 alone)
			aimed = len(to) > 0 && !slices.ContainsFunc(to, func(r string) bool { return rank(r) <= rank(who) })
		case slices.Contains(to, who):
			aimed = rank(from) > rank(who)
		default:
			continue
		}
		parks := m[2] == "PARKED" || m[2] == "PAUSE" || m[2] == "PAUSED"
		for _, s := range sentenceRe.Split(m[5], -1) {
			s = strings.TrimSpace(s)
			if l := labelRe.FindStringSubmatch(s); l != nil && slices.Contains(to, busName(project, l[1])) {
				if busName(project, l[1]) != who {
					continue // "P4: parked; P5: fix item 4." parks P4 alone
				}
				s = s[len(l[0]):]
			}
			parks = parks || parkRe.MatchString(s)
		}
		if parked && !(aimed && parks) {
			if at, err := lineTime(m[1]); err == nil {
				unparked = at
			}
		}
		parked = aimed && parks
	}
	return parked, unparked, err
}

var (
	sentenceRe = regexp.MustCompile(`[.!?;](?:\s+|$)`)
	labelRe    = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9-]*):\s+`)
	parkRe     = regexp.MustCompile(`(?i)^(?:stay\s+)?park(?:ed)?(?:\s+(?:for\s+)?now)?(?:$|\s*[,(:]|\s+(?:until|pending)\b)`)
)

// busName gives the full bus name of a role on project's bus: Quotes builders post as "P5" (quotes:p5), and "crius"
// is crius:lead.
func busName(project, name string) string {
	switch {
	case strings.Contains(name, ":"):
		return name
	case strings.EqualFold(name, "crius"):
		return "crius:lead"
	}
	return project + ":" + strings.ToLower(name)
}

// rank orders who may park whom: crius:lead over a project's lead over its other roles.
func rank(who string) int {
	switch {
	case who == "crius:lead":
		return 2
	case strings.HasSuffix(who, ":lead"):
		return 1
	}
	return 0
}

// busLines gives the parsed whole lines of the bus log of who's project, each with its text after the header as its
// last field. found is false when that log does not exist yet.
func busLines(home, who string) (lines [][]string, found bool, err error) {
	b, err := os.ReadFile(filepath.Join(home, "bus", strings.SplitN(who, ":", 2)[0]+".log"))
	if os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	for _, line := range strings.Split(string(b[:bytes.LastIndexByte(b, '\n')+1]), "\n") {
		if m := lineRe.FindStringSubmatch(line); m != nil {
			lines = append(lines, append(m, line[len(m[0]):]))
		}
	}
	return lines, true, nil
}

// asks reports whether a screen waits on someone: its newest status emoji is ❓, or a dialog offers numbered options.
func asks(text string) bool {
	return dialogRe.MatchString(text) || asksOwner(text)
}

// asksOwner reports whether a screen's newest status emoji is ❓: the last reply asks the owner something.
func asksOwner(text string) bool {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if j := strings.LastIndexAny(lines[i], "❓✅⏳"); j >= 0 {
			return strings.HasPrefix(lines[i][j:], "❓")
		}
	}
	return false
}

// unnamed alerts on each workspace with a default name or no colour, and on each team tab (its input box names a
// session) whose name does not start with its project: the owner found such tabs three times before (ORDER 10-22
// 13:36). It looks at every workspace, watched project or not.
func unnamed(d Deps, screens []Screen) [][2]string {
	var alerts [][2]string
	wss, err := d.Workspaces()
	if err != nil {
		alerts = append(alerts, [2]string{"looks-failed", "the workspace name and colour check failed to look: " + err.Error()})
	}
	for _, w := range wss {
		var miss []string
		if !w.Named {
			miss = append(miss, "no name")
		}
		if w.Color == "" {
			miss = append(miss, "no colour")
		}
		if miss != nil {
			alerts = append(alerts, [2]string{"look:" + w.Ref, fmt.Sprintf("workspace %s %q has %s: crius-spawn names and colours a "+
				"project's workspace, or set it with cmux workspace-action", w.Ref, w.Title, strings.Join(miss, " and "))})
		}
	}
	for _, s := range screens {
		who, role := session(s.Text, s.Title)
		if project, _, _ := strings.Cut(who, ":"); who != "" && !strings.HasPrefix(strings.ToLower(s.Title), strings.ToLower(project)) {
			alerts = append(alerts, [2]string{"tabname:" + s.Surface, fmt.Sprintf("tab %s %q runs %s but is not named for it: "+
				"cmux rename-tab --surface %s -- '%s · %s'", s.Surface, s.Title, who, s.Surface, project, role)})
		}
	}
	return alerts
}

// hooks is what crius-hook's state files say on this run, and the log lines where they and the screen disagree.
type hooks struct {
	decide  bool                          // ~/.crius/wakewatch-mode says "decide": the state decides, not the screen
	surface map[string]sessionstate.State // cmux surface id -> the state that speaks for that tab
	session map[string]sessionstate.State // session id -> its live state
	prev    map[string][2]string
	views   map[string][2]string
	log     []string
	now     time.Time
	blind   bool            // this run could not read the sessions, the states or the screens: what it did not see stays as last seen
	kept    map[string]bool // pairs this run could not look at (keep): a later pick leaves them as last seen
}

// readHooks reads the mode file and the state of each live session: one Claude Code lists as running (ss) and the
// hook has not marked ended. With no session list nothing says which states are live, so the screen decides alone.
// A missing state folder is loud only in decide mode: before the hook is installed it is expected.
func readHooks(d Deps, prev map[string][2]string, ss []Session, sesErr error) (*hooks, [][2]string) {
	h := &hooks{surface: map[string]sessionstate.State{}, session: map[string]sessionstate.State{}, prev: prev,
		views: map[string][2]string{}, kept: map[string]bool{}, now: d.Now}
	var out [][2]string
	p := filepath.Join(d.Home, "wakewatch-mode")
	if b, err := os.ReadFile(p); err != nil && !os.IsNotExist(err) {
		out = append(out, [2]string{"mode-failed", fmt.Sprintf("%s could not be read, so the screen decides: %v", p, err)})
	} else if m := strings.TrimSpace(string(b)); m == "decide" {
		h.decide = true
	} else if m != "observe" && m != "" {
		out = append(out, [2]string{"mode-failed", fmt.Sprintf("%s says %q, not observe or decide, so the screen decides", p, m)})
	}
	states, err := d.States()
	if err != nil && (h.decide || !errors.Is(err, fs.ErrNotExist)) {
		out = append(out, [2]string{"states-failed", "crius-hook's session states could not be read: " + err.Error()})
	}
	h.blind = err != nil || sesErr != nil
	if sesErr != nil {
		return h, out
	}
	live := map[string]bool{}
	for _, s := range ss {
		live[s.ID] = true
	}
	for _, s := range states {
		if !live[s.SessionID] || s.State != sessionstate.Running && s.State != sessionstate.Waiting && s.State != sessionstate.Idle {
			continue
		}
		h.session[s.SessionID] = s
		// A child claude run inside a tab (codex-ask's) shares its surface id. A waiting session speaks for the tab, so
		// a child's newer "running" never hides the parent's dialog; otherwise the newest state does.
		o, ok := h.surface[s.Surface]
		waits, owaits := s.State == sessionstate.Waiting, o.State == sessionstate.Waiting
		if s.Surface != "" && (!ok || waits && !owaits || waits == owaits && s.Time.After(o.Time)) {
			h.surface[s.Surface] = s
		}
	}
	return h, out
}

// pick gives the view that decides, the screen's or the state's, and logs the pair when it changed since the last run
// and either pair disagrees: one line when they start to disagree, one when they agree again. A run blind to the
// screens, sessions or states logs nothing and keeps the last pair: what it judged from may be missing (OF6). So does a
// pair this run kept, its tab unread.
func (h *hooks) pick(key, what, screen, state string) string {
	v, p := [2]string{screen, state}, h.prev[key]
	if h.blind || h.kept[key] {
		h.keep(key)
	} else {
		h.views[key] = v
	}
	if !h.blind && !h.kept[key] && v != p && (screen != state || p[0] != p[1]) {
		decides := "screen"
		if h.decide {
			decides = "state"
		}
		h.log = append(h.log, fmt.Sprintf("%s %s %q screen=%s state=%s decides=%s", h.now.Format(time.RFC3339), key, what,
			screen, state, decides))
	}
	if h.decide {
		return state
	}
	return screen
}

// keep carries a pair this run could not look at (its tab or bus unread) as last seen, so the next run that sees it
// does not log it again as new (OF6).
func (h *hooks) keep(key string) {
	h.kept[key] = true
	if v, ok := h.prev[key]; ok {
		h.views[key] = v
	}
}

// finish closes the pairs of the last run that this run did not see: kept as they were when it was blind, and a
// disagreement that went (its tab gone or no longer watched) gets one "gone" line, so statelog does not judge it open.
func (h *hooks) finish() {
	for _, key := range slices.Sorted(maps.Keys(h.prev)) {
		p := h.prev[key]
		if _, ok := h.views[key]; ok {
			continue
		}
		if h.blind {
			h.views[key] = p
		} else if p[0] != p[1] {
			h.pick(key, "", "gone", "gone")
			delete(h.views, key)
		}
	}
}

// spinRe is Claude Code's working line above the input box: a mark, a verb ending in "…", then the time it has worked
// or "esc to interrupt" in brackets ("✽ Garnishing… (3m 14s · ↓ 3.4k tokens)" in 2.1.308). Its words quoted in a
// tool's output are not one (review 10-22). A verb can hold any mark but "(": "Razzle-dazzling", "Beboppin'".
var spinRe = regexp.MustCompile(`^\s*[^\s\w]\s+\w[^(]*…\s*\((?:\d+[hms]\b[^)]*|[^)]*esc to interrupt[^)]*)\)\s*$`)

// spinning says whether the working line is in the last 15 lines above the input box (a todo list can sit between).
func spinning(text string) bool {
	body, _, boxed := split(text)
	if !boxed {
		return false
	}
	lines := strings.Split(body, "\n")
	for _, l := range lines[max(0, len(lines)-15):] {
		if spinRe.MatchString(l) {
			return true
		}
	}
	return false
}

// screenView is what a tab's screen says: "asks" (a dialog or a question), "busy" (working, jobs running, or no input
// box to read) or "idle".
func screenView(text string) string {
	_, foot, boxed := split(text)
	switch {
	case asks(text):
		return "asks"
	case !boxed || strings.Contains(text, "esc to interrupt") || jobsRe.MatchString(foot):
		return "busy"
	}
	return "idle"
}

// stateView is what a session's crius-hook state says, in screenView's words. The screen still adds what no hook
// tells: a tool approved in its dialog runs (the working line) while the state still says waiting, a turn that ended
// on a question (❓), and background jobs that run after the turn. "running" with no working line on screen is a
// state the hook missed the end of (an API error, a dialog no hook sees): the screen decides.
func stateView(s sessionstate.State, text string) string {
	_, foot, _ := split(text)
	switch {
	case s.State == sessionstate.Running && spinning(text):
		return "busy"
	case s.State == sessionstate.Running:
		return screenView(text)
	case s.State == sessionstate.Waiting && spinning(text):
		return "busy"
	case s.State == sessionstate.Waiting:
		return "asks"
	case jobsRe.MatchString(foot):
		return "busy"
	case asksOwner(text):
		return "asks"
	}
	return "idle"
}

// hookLog gives one alert each time crius-hook.log grows, with its last line: the hook exits 0 on its own errors so
// it never blocks a session, and this log is the only place they show.
func hookLog(home string, st *state) [][2]string {
	p := filepath.Join(home, "logs", "crius-hook.log")
	fi, err := os.Stat(p)
	if os.IsNotExist(err) {
		st.HookLog = 0
		return nil
	} else if err != nil {
		return [][2]string{{"hooklog-failed", "crius-hook's error log could not be read: " + err.Error()}}
	}
	if fi.Size() < st.HookLog { // cut or replaced: all of it is new
		st.HookLog = 0
	}
	if fi.Size() == st.HookLog {
		return nil
	}
	n, last, err := countLines(p, st.HookLog, fi.Size())
	if err != nil {
		return [][2]string{{"hooklog-failed", "crius-hook's error log could not be read: " + err.Error()}}
	}
	st.HookLog = fi.Size()
	return [][2]string{{fmt.Sprintf("hooklog:%d", fi.Size()), fmt.Sprintf("crius-hook logged %d new error line(s) in %s; "+
		"the last: %s", n, p, last)}}
}

// countLines counts the lines of the file at p from byte from to byte to and gives the last one, reading 64 KB at a
// time: the first run after a start sees all of a log that may be big (OF6).
func countLines(p string, from, to int64) (n int, last string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	var end byte
	for at := from; at < to; {
		k, err := f.ReadAt(buf[:min(int64(len(buf)), to-at)], at)
		if err != nil {
			return 0, "", err
		}
		n += bytes.Count(buf[:k], []byte("\n"))
		end = buf[k-1]
		at += int64(k)
	}
	if end != '\n' {
		n++
	}
	// The last line starts after the last "\n" before its own end: look back for it, then give up to 64 KB from there.
	stop, start := to, from
	if end == '\n' {
		stop--
	}
	for at := stop; at > from; {
		k := min(int64(len(buf)), at-from)
		if _, err := f.ReadAt(buf[:k], at-k); err != nil {
			return 0, "", err
		}
		if i := bytes.LastIndexByte(buf[:k], '\n'); i >= 0 {
			start = at - k + int64(i) + 1
			break
		}
		at -= k
	}
	line := buf[:min(int64(len(buf)), stop-start)]
	if _, err := f.ReadAt(line, start); err != nil {
		return 0, "", err
	}
	return n, string(line), nil
}

// stateLogMax is the size at which a state log is moved to <name>.1 and started afresh.
const stateLogMax = 1 << 20

// appendLines adds lines to the file at p, making its folder. A file past stateLogMax first moves to p.1, replacing
// the one there.
func appendLines(p string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if fi, err := os.Stat(p); err == nil && fi.Size() >= stateLogMax {
		if err := os.Rename(p, p+".1"); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
