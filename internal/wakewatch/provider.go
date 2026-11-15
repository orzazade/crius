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
	// providerRe: a provider or API failure, not the session's own work (403 Cloudflare 11-13, a 5xx, overloaded).
	providerRe = regexp.MustCompile(`(?i)API Error|Cloudflare|overloaded|\b(?:500|502|503|504|529)\b`)
	// quotaRe: a usage limit is not cured by "carry on": it waits for its reset, which the quota checks handle.
	quotaRe = regexp.MustCompile(`(?i)rate limit|usage limit|\b429\b`)
)

// providerError says whether an error text is a provider error worth one carry-on.
func providerError(text string) bool {
	return providerRe.MatchString(text) && !quotaRe.MatchString(text) && !strings.Contains(text, "Retrying in")
}

// ProviderStop gives the error line a tab's last turn died on, when the tab sits at an idle, empty input box after a
// provider error: no working line, no dialog, nothing typed in the box, and no ❓ ✅ ⏳ after the error (a later reply
// that only mentions it). key names the stop: the error line and the "✻ … done <time>" line under it, so the same
// stop read again keeps its key while the shell count beside it changes.
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
		k := t
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
	st, err := loadProvider(d.Home)
	if err != nil {
		return rep, err
	}
	var errs []error
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
		if err := send(); err != nil {
			st.Stops[key] = providerStop{d.Now, "send failed"}
			errs = append(errs, fmt.Errorf("restarting %s: %v", who, err))
			say("ALERT", fmt.Sprintf("%s stopped on a provider error (%s) and the watchdog could not restart it: %v. Look at it", who, errText, err))
			return
		}
		st.Stops[key] = providerStop{d.Now, "restarted"}
		st.Restarted[session] = d.Now
		say("INFO", fmt.Sprintf("watchdog restarted %s after a provider error (%s)", who, errText))
	}

	threads, err := d.Threads()
	if err != nil {
		cannot("the T3 Code threads", err)
	}
	rep.Threads = len(threads)
	for _, th := range threads {
		text := th.Message
		if th.Class == "usage_limit" || !providerError(text) {
			continue
		}
		at := th.Ended
		if at.IsZero() {
			at = d.Now
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
		stop("tab "+t.String(), "tab "+t.String()+" "+key, "tab "+t.String(), line, d.Now, func() error {
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

func loadProvider(home string) (providerState, error) {
	st := providerState{}
	b, err := os.ReadFile(ProviderStateFile(home))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return st, fmt.Errorf("%s: %v", ProviderStateFile(home), err)
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
	return st, nil
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
