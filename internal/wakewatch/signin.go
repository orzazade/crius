package wakewatch

// The sign-in watch. Every Max account's sign-in has a hard end (`claude auth status` → loginExpiresAt); when it passes,
// every session on that account stops, at night too, and nothing says so. Each run reads `claude auth status` of each Max
// config dir on the Mac and on the Legion (no model call) and wakes Crius once per account, state and day when the end
// is under 4 days away, has passed or the account is signed out, naming the exact command that renews it. A status that
// could not be read, or that names no end, is its own loud state, never OK. The enterprise dir (~/.claude-work) is never
// read. No secret file or keychain value is read here: only the CLI's own status output.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

const (
	SigninOK      = "OK"
	SigninWarn    = "WARN"    // the end is under signinAhead away
	SigninExpired = "EXPIRED" // signed out, or the end has passed
	SigninUnknown = "UNKNOWN" // signed in, but the status names no end
	SigninUnread  = "UNREAD"  // the status could not be read or was not what it should be

	signinAhead = 4 * 24 * time.Hour

	// MacClaude is the real CLI on the Mac: plain `claude` is the aitch wrapper.
	MacClaude = "~/.local/bin/claude"
	// LegionSSH runs a command on the Legion (its own key and known-hosts file).
	LegionSSH  = "~/.crius/state/quotes/build/legion/E"
	LegionHost = "crius@100.109.19.101"
	// LegionClaude finds the CLI on the Legion: it lives under nvm, which a non-interactive ssh shell has not on its PATH
	// (11-15: a bare `claude` there was "command not found", so the status printed no end date).
	LegionClaude = `"$(ls -dv ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude 2>/dev/null | tail -1)"`
)

// Account is one Max config dir: Host "" is the Mac, "legion" the Legion; Dir is written as that machine's shell reads it.
type Account struct{ Name, Host, Dir string }

// Accounts are the Max config dirs. Never ~/.claude-work: the enterprise account is never run (owner 11-07).
var Accounts = []Account{
	{"personal", "", "~/.claude"},
	{"max2", "", "~/.claude-max2-personal"},
	{"work-max", "", "~/.claude-work-max"},
	{"work-max3", "", "~/.claude-work-max3"},
	{"legion-max1", "legion", "~/.claude-work-max1"},
	{"legion-max2", "legion", "~/.claude-work-max2"},
	{"legion-max3", "legion", "~/.claude-work-max3"},
}

// Shell gives the command line that runs `claude auth <sub>` for the account on its machine, as a shell on that machine
// (the Mac) or the ssh wrapper (the Legion) reads it. tty asks ssh for a terminal, which `auth login` needs.
func (a Account) Shell(sub string, tty bool) string {
	if a.Host == "" {
		return "CLAUDE_CONFIG_DIR=" + a.Dir + " " + MacClaude + " auth " + sub
	}
	t := ""
	if tty {
		t = "-t "
	}
	return LegionSSH + " " + t + LegionHost + " '" + a.Remote(sub) + "'"
}

// Remote is the command the Legion's shell runs for `claude auth <sub>`.
func (a Account) Remote(sub string) string {
	return "CLAUDE_CONFIG_DIR=" + a.Dir + " " + LegionClaude + " auth " + sub
}

// Renew is the exact command that signs the account in again.
func (a Account) Renew() string { return a.Shell("login", true) }

func (a Account) where() string {
	if a.Host == "" {
		return "Mac " + a.Dir
	}
	return a.Host + " " + a.Dir
}

// SigninRow is one account's state; End is zero when not known.
type SigninRow struct {
	Account
	State string
	End   time.Time
	Why   string
}

// Signins reads every account at once (one slow or dead machine must not hold up the rest) and gives one row each, in
// the order of Accounts.
func Signins(read func(Account) ([]byte, error), now time.Time) []SigninRow {
	rows := make([]SigninRow, len(Accounts))
	var wg sync.WaitGroup
	for i, a := range Accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := read(a)
			rows[i] = signin(a, b, err, now)
		}()
	}
	wg.Wait()
	return rows
}

func signin(a Account, b []byte, err error, now time.Time) SigninRow {
	r := SigninRow{Account: a, State: SigninUnread}
	if err != nil {
		r.Why = "could not read claude auth status: " + err.Error()
		return r
	}
	var s struct {
		LoggedIn       *bool   `json:"loggedIn"`
		LoginExpiresAt *string `json:"loginExpiresAt"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		// the output itself is not quoted: plain text from a future CLI could carry the account's email
		r.Why = fmt.Sprintf("could not read claude auth status: not JSON (%d bytes: %v)", len(b), err)
		return r
	}
	if s.LoggedIn == nil {
		r.Why = "could not read claude auth status: no loggedIn field"
		return r
	}
	if !*s.LoggedIn {
		r.State, r.Why = SigninExpired, "signed out (loggedIn false)"
		return r
	}
	if s.LoginExpiresAt == nil || *s.LoginExpiresAt == "" {
		r.State, r.Why = SigninUnknown, "signed in, but claude auth status gives no loginExpiresAt: the end is unknown"
		return r
	}
	end, err := time.Parse(time.RFC3339, *s.LoginExpiresAt)
	if err != nil {
		r.Why = fmt.Sprintf("could not read claude auth status: loginExpiresAt %q is no time", *s.LoginExpiresAt)
		return r
	}
	r.End = end
	switch left := end.Sub(now); {
	case left <= 0:
		r.State, r.Why = SigninExpired, "the sign-in ended"
	case left < signinAhead:
		r.State, r.Why = SigninWarn, span(left)+" left"
	default:
		r.State, r.Why = SigninOK, span(left)+" left"
	}
	return r
}

func span(d time.Duration) string {
	h := int(d / time.Hour)
	return fmt.Sprintf("%dd %dh", h/24, h%24)
}

// signins gives one alert per account that is not OK, raised only when due: once per account, state and day (the day as
// d.Now's clock reads it), so the 5 min runs and the 20 min resend say it once. A new state the same day says it again.
func signins(d Deps, st *state) [][2]string {
	day := d.Now.Format("2006-01-02")
	if st.Signin == nil {
		st.Signin = map[string]string{}
	}
	for k, on := range st.Signin {
		if on != day {
			delete(st.Signin, k)
		}
	}
	var out [][2]string
	for _, r := range Signins(d.Signin, d.Now) {
		if r.State == SigninOK {
			continue
		}
		key := "signin:" + r.Name + ":" + r.State
		if st.Signin[key] == day {
			continue
		}
		st.Signin[key] = day
		out = append(out, [2]string{key, r.alert(d.Now.Location())})
	}
	return out
}

func (r SigninRow) alert(loc *time.Location) string {
	head := fmt.Sprintf("sign-in %s %s (%s)", r.State, r.Name, r.where())
	switch r.State {
	case SigninWarn:
		return fmt.Sprintf("%s ends %s (%s); every session on it stops then. Renew: %s", head, r.End.In(loc).Format("2006-01-02 15:04"), r.Why, r.Renew())
	case SigninExpired:
		return fmt.Sprintf("%s: %s; sessions on it stop. Renew: %s", head, r.Why, r.Renew())
	default: // UNKNOWN, UNREAD
		return fmt.Sprintf("%s: %s. Check by hand: %s", head, r.Why, r.Shell("status", false))
	}
}

// SigninTable is the morning view: each account, its end and state, and for one not OK what to do.
func SigninTable(rows []SigninRow, loc *time.Location) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ACCOUNT\tWHERE\tENDS\tSTATE\tNOTE")
	for _, r := range rows {
		end := "-"
		if !r.End.IsZero() {
			end = r.End.In(loc).Format("2006-01-02 15:04")
		}
		note := r.Why
		if r.State == SigninOK {
			note += "; WARN from " + r.End.Add(-signinAhead).In(loc).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Name, r.where(), end, r.State, note)
	}
	w.Flush()
	for _, r := range rows {
		switch r.State {
		case SigninWarn, SigninExpired:
			fmt.Fprintf(&b, "renew %s: %s\n", r.Name, r.Renew())
		case SigninUnknown, SigninUnread:
			fmt.Fprintf(&b, "check %s: %s\n", r.Name, r.Shell("status", false))
		}
	}
	return b.String()
}
