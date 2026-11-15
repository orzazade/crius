package wakewatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The provider-error sweep restarts a session whose last turn died on a provider error (night 11-16: Crius ran it as a
// model turn every 5 min since 11-13, 288 a day, nearly all finding nothing). It looks at every T3 Code thread whose
// latest run failed or was interrupted, and at each remote tmux tab in providerTabsFile, and types a fixed carry-on line
// once per stop. A second stop of the same session within providerAgain of a restart is said to Crius instead, so it
// never loops. Each restart is one bus line and wakes Crius; what it cannot look at is said to Crius, never "none".

const (
	providerAgain = 15 * time.Minute // a stop this soon after a restart is said to Crius, not restarted again
	providerLoud  = time.Hour        // a "could not look" is said to Crius when it starts, then once an hour while it lasts
	providerKeep  = 7 * 24 * time.Hour
	providerOld   = 12 * time.Hour // a thread stopped longer ago was left so on purpose (Crius swept every 5 min since 11-13)
)

// ThreadText is what a T3 thread stopped by a provider error is sent; %s is the error.
const ThreadText = "Crius: your last turn failed on a provider error (%s), not your work. Carry on where you were; re-arm your bus waiter first."

// TabText is what a remote tab stopped by a provider error is sent.
const TabText = "continue: your last turn hit a provider error; carry on and re-arm your waiter"

// FailedThread is a T3 Code thread whose latest run failed or was interrupted, with the newest error item of that run
// (Class and Message are "" when the run has none).
type FailedThread struct {
	ID, Title, Project, Run, Status string
	Ended                           time.Time
	Class, Message                  string
}

// RemoteTab is a tmux window running Claude Code on another machine: a box named in the playbook's hosts.json
// (legion, thinkpad) and its session:window.
type RemoteTab struct{ Box, Target string }

func (t RemoteTab) String() string { return t.Box + " " + t.Target }

// ProviderDeps is what the sweep reads and does. Capture gives a tab's screen with dim text (Claude Code's grey
// suggestion in an empty box) taken out. Post puts one line of type typ on Crius's bus. With Dry it sends, posts and
// saves nothing and only says what it would do.
type ProviderDeps struct {
	Home       string
	Now        time.Time
	Threads    func() ([]FailedThread, error)
	Tabs       func() ([]RemoteTab, error)
	Capture    func(RemoteTab) (string, error)
	SendThread func(id, text string) error
	SendTab    func(t RemoteTab, text string) error
	Post       func(typ, text string) error
	SendCrius  func(text string) error
	Dry        bool
}

// ProviderReport says what one sweep saw and did, one line per action.
type ProviderReport struct {
	Threads, Tabs, Stopped int
	Actions                []string
}

type providerStop struct {
	At  time.Time
	Did string
}

type providerState struct {
	LastRun   time.Time
	Stops     map[string]providerStop // a stop seen: "t3 <thread> <run>" or "tab <box> <target> <hash>"
	Restarted map[string]time.Time    // a session's last restart: "t3 <thread>" or "tab <box> <target>"
	Failing   map[string]time.Time    // a "could not look" said to Crius, by what could not be looked at
}

// ProviderStateFile is where the sweep keeps what it did.
func ProviderStateFile(home string) string {
	return filepath.Join(home, "state", "provider-errors.json")
}

// ProviderTabsFile lists the remote tabs the sweep reads, one "<box> <session:window>" per line ("#" starts a comment).
func ProviderTabsFile(home string) string { return filepath.Join(home, "provider-tabs.txt") }

var (
	// providerRe: Claude Code's or T3's API failure line (403 Cloudflare 11-13, a 5xx, overloaded, the network), not the
	// session's own words: a bare "503" in a reply is not one.
	providerRe = regexp.MustCompile(`(?i)API Error|Cloudflare|overloaded_error`)
	// notCuredRe: what "carry on" does not cure. A usage limit waits for its reset (the quota checks); a 401 or a
	// sign-in the owner must renew waits for him (the 403 Cloudflare line says "Please run /login" too, and is cured).
	notCuredRe = regexp.MustCompile(`(?i)rate limit|usage limit|\b429\b|\b401\b|authentication_error|could not authenticate|invalid api key|OAuth token`)
)

// providerError says whether an error text is a provider error worth one carry-on.
func providerError(text string) bool {
	return providerRe.MatchString(text) && !notCuredRe.MatchString(text) && !strings.Contains(text, "Retrying in")
}

// ProviderStop gives the error line a tab's last turn died on, when the tab sits at an idle, empty input box after a
// provider error: no working line, no dialog, nothing typed in the box, and no ❓ ✅ ⏳ after the error (a later reply
// that only mentions it). key names the stop: the error line with the lines above it and the "✻ … done <time>" line
// under it, so the same stop read again keeps its key while the shell count beside it changes, and a stop after a
// restart differs from the one before, even with the same error and no "✻" line (the restart's text is above it).
func ProviderStop(text string) (line, key string, ok bool) {
	body, _, boxed := split(text)
	if !boxed || spinning(text) || strings.Contains(text, "esc to interrupt") || LivePrompt(text) {
		return "", "", false
	}
	if typed, _ := Prompt(text); strings.TrimSpace(typed) != "" {
		return "", "", false
	}
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := replyRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		t := strings.TrimSpace(m[1])
		if !providerError(t) || strings.ContainsAny(strings.Join(lines[i:], "\n"), "❓✅⏳") {
			return "", "", false
		}
		var above []string // the error line and up to 5 lines above it, blank and "✻" lines left out
		for j := i; j >= 0 && len(above) < 6; j-- {
			if l := strings.TrimSpace(lines[j]); l != "" && !strings.HasPrefix(l, "✻") {
				above = append(above, l)
			}
		}
		k := strings.Join(above, "\n")
		for _, l := range lines[i+1:] {
			if l = strings.TrimSpace(l); strings.HasPrefix(l, "✻") {
				parts := strings.SplitN(l, " · ", 3)
				k += "\n" + strings.Join(parts[:min(2, len(parts))], " · ")
				break
			}
		}
		h := sha256.Sum256([]byte(k))
		return t, hex.EncodeToString(h[:6]), true
	}
	return "", "", false
}

// ProviderErrors runs one sweep. The error names each thing it could not look at, read, send or save; each of those
// is also said to Crius (a "could not look" once an hour while it lasts).
func ProviderErrors(d ProviderDeps) (ProviderReport, error) {
	var rep ProviderReport
	var errs []error
	st, loadErr := loadProvider(d.Home)
	say := func(typ, text string) {
		rep.Actions = append(rep.Actions, typ+": "+text)
		if d.Dry {
			return
		}
		if err := d.Post(typ, text); err != nil {
			errs = append(errs, fmt.Errorf("bus: %v", err))
		}
		if err := d.SendCrius(text); err != nil {
			errs = append(errs, fmt.Errorf("telling Crius: %v", err))
		}
	}
	if loadErr != nil { // set aside, so one bad file does not stop every later sweep; what it held may be done again
		aside := ProviderStateFile(d.Home) + ".bad"
		if !d.Dry {
			if err := os.Rename(ProviderStateFile(d.Home), aside); err != nil {
				errs = append(errs, err)
			}
		}
		errs = append(errs, loadErr)
		say("ALERT", fmt.Sprintf("the provider-error sweep could not read its state (%v); set aside as %s and started afresh", loadErr, aside))
	}
	prev := st.LastRun // a tab stop seen now happened after the last look
	looked := map[string]bool{}
	cannot := func(what string, err error) {
		errs = append(errs, fmt.Errorf("could not look at %s: %v", what, err))
		looked[what] = true
		if last, ok := st.Failing[what]; ok && d.Now.Sub(last) < providerLoud {
			return
		}
		st.Failing[what] = d.Now
		say("ALERT", fmt.Sprintf("the provider-error sweep could not look at %s, so a session stopped there is not seen: %v", what, err))
	}
	// stop handles one session stopped by a provider error: restart it once, or tell Crius when it stopped again soon
	// after a restart, or when the restart could not be sent.
	stop := func(session, key, who, errText string, at time.Time, send func() error) {
		rep.Stopped++
		if _, seen := st.Stops[key]; seen {
			return
		}
		if last, ok := st.Restarted[session]; ok && !at.Before(last) && at.Sub(last) <= providerAgain {
			st.Stops[key] = providerStop{d.Now, "alerted"}
			say("ALERT", fmt.Sprintf("%s stopped again on a provider error (%s) %d min after the watchdog restarted it; not restarted again: look at it",
				who, errText, int(at.Sub(last).Minutes())))
			return
		}
		if d.Dry {
			rep.Actions = append(rep.Actions, fmt.Sprintf("would restart %s after a provider error (%s)", who, errText))
			return
		}
		// saved before the send: a restart that cannot be recorded is not sent, or every run would send it again
		lastRestart, restartedBefore := st.Restarted[session]
		st.Stops[key] = providerStop{d.Now, "restarted"}
		st.Restarted[session] = d.Now
		if err := saveProvider(d.Home, st); err != nil {
			delete(st.Stops, key)
			if restartedBefore {
				st.Restarted[session] = lastRestart
			} else {
				delete(st.Restarted, session)
			}
			errs = append(errs, fmt.Errorf("not restarting %s: its state could not be saved: %v", who, err))
			return
		}
		if err := send(); err != nil {
			st.Stops[key] = providerStop{d.Now, "send failed"}
			errs = append(errs, fmt.Errorf("restarting %s: %v", who, err))
			say("ALERT", fmt.Sprintf("%s stopped on a provider error (%s) and the watchdog could not restart it: %v. Look at it", who, errText, err))
			return
		}
		say("INFO", fmt.Sprintf("watchdog restarted %s after a provider error (%s)", who, errText))
	}

	threads, err := d.Threads()
	if err != nil {
		cannot("the T3 Code threads", err)
	}
	rep.Threads = len(threads)
	for _, th := range threads {
		text := th.Message
		at := th.Ended
		if at.IsZero() {
			at = d.Now
		}
		if th.Class == "usage_limit" || !providerError(text) || d.Now.Sub(at) > providerOld {
			continue
		}
		short := []rune(strings.Join(strings.Fields(text), " "))
		text = string(short[:min(len(short), 120)])
		who := fmt.Sprintf("T3 thread %q (%s, %s)", th.Title, th.Project, th.ID)
		stop("t3 "+th.ID, "t3 "+th.ID+" "+th.Run, who, text, at, func() error {
			return d.SendThread(th.ID, fmt.Sprintf(ThreadText, text))
		})
	}
	tabs, err := d.Tabs()
	if err != nil {
		cannot("the remote tab list "+ProviderTabsFile(d.Home), err)
	}
	for _, t := range tabs {
		screen, err := d.Capture(t)
		if err != nil {
			cannot("tab "+t.String(), err)
			continue
		}
		rep.Tabs++
		line, key, ok := ProviderStop(screen)
		if !ok {
			continue
		}
		at := d.Now
		if !prev.IsZero() && prev.Before(at) {
			at = prev
		}
		stop("tab "+t.String(), "tab "+t.String()+" "+key, "tab "+t.String(), line, at, func() error {
			return d.SendTab(t, TabText)
		})
	}

	for what := range st.Failing { // looked at fine this time: a new failure is said at once
		if !looked[what] {
			delete(st.Failing, what)
		}
	}
	for k, s := range st.Stops {
		if d.Now.Sub(s.At) > providerKeep {
			delete(st.Stops, k)
		}
	}
	for k, at := range st.Restarted {
		if d.Now.Sub(at) > providerKeep {
			delete(st.Restarted, k)
		}
	}
	st.LastRun = d.Now
	if !d.Dry {
		if err := saveProvider(d.Home, st); err != nil {
			errs = append(errs, err)
		}
	}
	return rep, errors.Join(errs...)
}

// loadProvider reads the state; one it cannot read gives an empty state and the error.
func loadProvider(home string) (st providerState, err error) {
	b, err := os.ReadFile(ProviderStateFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	} else if err == nil {
		if jerr := json.Unmarshal(b, &st); jerr != nil {
			st, err = providerState{}, fmt.Errorf("%s: %v", ProviderStateFile(home), jerr)
		}
	}
	if st.Stops == nil {
		st.Stops = map[string]providerStop{}
	}
	if st.Restarted == nil {
		st.Restarted = map[string]time.Time{}
	}
	if st.Failing == nil {
		st.Failing = map[string]time.Time{}
	}
	return st, err
}

// saveProvider writes the state through a temp file and a rename, so a run cut short never leaves half a file.
func saveProvider(home string, st providerState) error {
	path := ProviderStateFile(home)
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "provider-errors.json.")
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

// ReadProviderTabs reads ProviderTabsFile: no file is no tabs.
func ReadProviderTabs(home string) ([]RemoteTab, error) {
	b, err := os.ReadFile(ProviderTabsFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []RemoteTab
	for n, l := range strings.Split(string(b), "\n") {
		l, _, _ = strings.Cut(l, "#")
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if len(f) != 2 {
			return nil, fmt.Errorf("%s line %d: want \"<box> <session:window>\", got %q", ProviderTabsFile(home), n+1, strings.TrimSpace(l))
		}
		out = append(out, RemoteTab{f[0], f[1]})
	}
	return out, nil
}

// ProviderLastRun gives when the sweep last ran and saved its state (zero when never).
func ProviderLastRun(home string) (time.Time, error) {
	st, err := loadProvider(home)
	return st.LastRun, err
}
