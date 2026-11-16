package wakewatch

// The sign-in watch. Every Max account's sign-in has a hard end (`claude auth status` → loginExpiresAt); when it passes,
// every session on that account stops, at night too, and nothing says so. Each run reads `claude auth status` of each Max
// config dir on the Mac and on the Legion (no model call) and wakes Crius once per account, state and day when the end
// is under 4 days away, has passed or the account is signed out, naming the exact command that renews it. A status that
// could not be read, or that names no end, is its own loud state, never OK. The enterprise dir (~/.claude-work) is never
// read. No secret file or keychain value is read here: only the CLI's own status output.
//
// The same run reads each login's weekly limit (11-16: max1 hit its week; Staffy's ThinkPad tabs sat idle for hours and
// nothing said so). The source is free: the statusline's quota spool of each machine (~/.crius/spool/quota.jsonl on the
// Mac, the Legion and the ThinkPad), whose lines carry each login's seven-day share and reset. No model is run. A login at
// or over 95% of its week, or at 100% (blocked until the reset), wakes Crius once per login, state and day with the reset
// time. A share under 95% last measured over weekStale ago, a window with no sample since its reset, a login no line
// names, a login or spool that could not be read: each is UNKNOWN, said loudly, never OK. A login is the 12-hex hash
// quota.LoginOf gives (of the account id in the dir's .claude.json; never a token or an email).
//
// Known gaps (review 11-17, follow-ups): (1) a spool line names the login its folder held at render time, not the one its
// session runs on; quota.reduce now takes the window from measured lines only, which covers an idle session left on an
// old login, but a session still answering on the old login would bring the wrong window back (proper fix: tie each
// session to the login it first measured under). (2) Right after a weekly reset, until a measured line of the new week
// comes, the login reads as "reset, no sample since" (UNKNOWN), not the new week's share.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

const (
	SigninOK      = "OK"
	SigninWarn    = "WARN"    // the end is under signinAhead away
	SigninExpired = "EXPIRED" // signed out, or the end has passed
	SigninUnknown = "UNKNOWN" // signed in, but the status names no end
	SigninUnread  = "UNREAD"  // the status could not be read or was not what it should be

	signinAhead = 4 * 24 * time.Hour

	WeekOK      = "OK"
	WeekNear    = "NEAR"    // at or over quota.Limit (95%) of the week
	WeekLimit   = "LIMIT"   // the week is used up: every session on the login waits for the reset
	WeekUnknown = "UNKNOWN" // no fresh sample, or a read failed: never OK

	// weekStale: a share under 95% measured longer ago than this may have risen since (other machines, other tabs).
	weekStale = 6 * time.Hour

	// MacClaude is the real CLI on the Mac: plain `claude` is the aitch wrapper.
	MacClaude = "~/.local/bin/claude"
	// LegionSSH runs a command on the Legion (its own key and known-hosts file).
	LegionSSH  = "~/.crius/state/quotes/build/legion/E"
	LegionHost = "crius@100.109.19.101"
	// ThinkPadSSH runs a command on the ThinkPad (Staffy's tmux team); the wrapper names the host itself.
	ThinkPadSSH = "~/.crius/state/staffy/thinkpad/T"
	// RemoteClaude finds the CLI on the Legion and the ThinkPad: it lives under nvm, which a non-interactive ssh shell has
	// not on its PATH (11-15: a bare `claude` there was "command not found", so the status printed no end date).
	RemoteClaude = `"$(ls -dv ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude 2>/dev/null | tail -1)"`
	// SpoolRemote prints a remote machine's quota spool: nothing when it keeps none (the Legion runs no statusline).
	SpoolRemote = `f=~/.crius/spool/quota.jsonl; [ -e "$f" ] || exit 0; tail -n 20000 "$f"`
)

// remote is how the Mac reaches a machine: its ssh wrapper, and the host to name after it ("" when the wrapper names it).
type remote struct{ SSH, Target string }

// Remotes are the machines the watch reads besides the Mac.
var Remotes = map[string]remote{
	"legion":   {LegionSSH, LegionHost},
	"thinkpad": {ThinkPadSSH, ""},
}

// Hosts are every machine whose spool is read, the Mac ("") first.
var Hosts = []string{"", "legion", "thinkpad"}

// LoginRemote prints the login (quota.LoginOf: the first 12 hex of the sha256 of the account id in the dir's
// .claude.json, ~/.claude.json for ~/.claude) of config dir on a remote machine, "" when none: never the id, a token or an email.
func LoginRemote(dir string) string {
	return `python3 -c "import json,hashlib,os,sys
d=os.path.expanduser(sys.argv[1]); f=os.path.join(d,'.claude.json')
if not os.path.exists(f) and os.path.realpath(d)==os.path.realpath(os.path.expanduser('~/.claude')): f=os.path.expanduser('~/.claude.json')
u=((json.load(open(f)).get('oauthAccount') or {}).get('accountUuid') or '') if os.path.exists(f) else ''
print(hashlib.sha256(u.encode()).hexdigest()[:12] if u else '')" ` + dir
}

// Account is one Max config dir: Host "" is the Mac, else a machine of Remotes ("legion", "thinkpad"); Dir is written as that machine's shell reads it.
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
	{"thinkpad-claude", "thinkpad", "~/.claude"},
	{"thinkpad-claude-max2", "thinkpad", "~/.claude-max2"},
}

// Shell gives the command line that runs `claude auth <sub>` for the account on its machine, as a shell on that machine
// (the Mac) or the ssh wrapper (the Legion, the ThinkPad) reads it. tty asks ssh for a terminal, which `auth login` needs.
func (a Account) Shell(sub string, tty bool) string {
	if a.Host == "" {
		return "CLAUDE_CONFIG_DIR=" + a.Dir + " " + MacClaude + " auth " + sub
	}
	t := ""
	if tty {
		t = "-t "
	}
	r := Remotes[a.Host]
	if r.Target != "" {
		t += r.Target + " "
	}
	return r.SSH + " " + t + "'" + a.Remote(sub) + "'"
}

// Remote is the command a remote machine's shell runs for `claude auth <sub>`.
func (a Account) Remote(sub string) string {
	return "CLAUDE_CONFIG_DIR=" + a.Dir + " " + RemoteClaude + " auth " + sub
}

// Renew is the exact command that signs the account in again.
func (a Account) Renew() string { return a.Shell("login", true) }

func (a Account) where() string {
	if a.Host == "" {
		return "Mac " + a.Dir
	}
	return a.Host + " " + a.Dir
}

// SigninRow is one account's state; End is zero when not known. Login is the dir's login ("" when not known) and
// LoginName its accounts.toml name; Week is its login's weekly state (WeekPct of the week used, WeekReset when it resets,
// zero when not known).
type SigninRow struct {
	Account
	State string
	End   time.Time
	Why   string

	Login, LoginName string
	Week             string
	WeekPct          float64
	WeekReset        time.Time
	WeekWhy          string
}

// SigninReads are what the watch reads on each machine; none runs a model.
type SigninReads struct {
	Status func(Account) ([]byte, error)     // `claude auth status` (JSON) of the account
	Login  func(Account) (string, error)     // the login of its config dir (quota.LoginOf, LoginRemote)
	Spool  func(host string) ([]byte, error) // the machine's quota spool lines; nil when it keeps none
}

// SigninWatch is one read of every account: a row each, in the order of Accounts, each machine whose spool could not
// be read (host -> why), and each machine's spool lines that were not JSON and were skipped (host -> how many).
type SigninWatch struct {
	Rows   []SigninRow
	Unread map[string]string
	Bad    map[string]int
}

var loginHash = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Signins reads every account and spool at once (one slow or dead machine must not hold up the rest). home is Crius's
// folder: its accounts.toml names each login.
func Signins(r SigninReads, home string, now time.Time) SigninWatch {
	w := SigninWatch{Rows: make([]SigninRow, len(Accounts)), Unread: map[string]string{}, Bad: map[string]int{}}
	lgs := make([]string, len(Accounts))
	lgErr := make([]error, len(Accounts))
	spools := make([][]byte, len(Hosts))
	spErr := make([]error, len(Hosts))
	var wg sync.WaitGroup
	for i, a := range Accounts {
		wg.Add(2)
		go func() {
			defer wg.Done()
			b, err := r.Status(a)
			w.Rows[i] = signin(a, b, err, now)
		}()
		go func() {
			defer wg.Done()
			lgs[i], lgErr[i] = r.Login(a)
		}()
	}
	for i, h := range Hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spools[i], spErr[i] = r.Spool(h)
		}()
	}
	wg.Wait()

	var lines []quota.Line
	for i, h := range Hosts {
		if spErr[i] != nil {
			w.Unread[hostName(h)] = spErr[i].Error()
			continue
		}
		ls, bad := spoolLines(spools[i])
		if bad > 0 {
			w.Bad[hostName(h)] = bad
		}
		lines = append(lines, ls...)
	}
	weeks := quota.Weeks(lines, now)
	as, asErr := quota.LoadAccounts(home)
	for i := range w.Rows {
		w.Rows[i].week(lgs[i], lgErr[i], weeks, as, asErr, w.Unread, now)
	}
	return w
}

// spoolLines parses a spool. A last line with no newline is being written and is left. A line that is not JSON (a write
// cut short when the disk filled, and joined to the next) is skipped and counted, as ingest does: one bad line must not
// blind the whole machine for good. Only the count is ever said, never the line.
func spoolLines(b []byte) (out []quota.Line, bad int) {
	if i := bytes.LastIndexByte(b, '\n'); i < len(b)-1 {
		b = b[:i+1]
	}
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var l quota.Line
		if err := json.Unmarshal(ln, &l); err != nil {
			bad++
			continue
		}
		out = append(out, l)
	}
	return out, bad
}

func hostName(h string) string {
	if h == "" {
		return "mac"
	}
	return h
}

// week sets the row's weekly state from its login's window.
func (r *SigninRow) week(login string, err error, weeks map[string]quota.Week, as *quota.Accounts, asErr error, unread map[string]string, now time.Time) {
	r.Week = WeekUnknown
	switch {
	case err != nil:
		r.WeekWhy = "could not read the dir's login: " + err.Error()
		return
	case login == "":
		r.WeekWhy = "the dir names no login (.claude.json has no account)"
		return
	case !loginHash.MatchString(login):
		r.WeekWhy = fmt.Sprintf("the login read is not a login (%d bytes)", len(login))
		return
	}
	r.Login, r.LoginName = login, "login "+login
	if asErr == nil {
		r.LoginName = as.Name(login)
	}
	var blind string
	if len(unread) > 0 {
		blind = "; spool unread on " + strings.Join(slices.Sorted(maps.Keys(unread)), ", ")
	}
	wk, ok := weeks[login]
	switch {
	case !ok:
		r.WeekWhy = "no statusline sample of this login in any spool read" + blind
	case wk.Reset.IsZero():
		r.WeekWhy = "its week reset " + wk.Ended.Format(time.RFC3339) + "; no sample since" + blind
	case wk.SevenPct >= 100:
		r.Week, r.WeekPct, r.WeekReset = WeekLimit, wk.SevenPct, wk.Reset
		r.WeekWhy = "week used up; blocked until the reset"
	case wk.SevenPct >= quota.Limit:
		r.Week, r.WeekPct, r.WeekReset = WeekNear, wk.SevenPct, wk.Reset
		r.WeekWhy = fmt.Sprintf("at or over %d%% of the week", quota.Limit)
	case wk.Measured.IsZero() || now.Sub(wk.Measured) > weekStale:
		r.WeekPct, r.WeekReset = wk.SevenPct, wk.Reset
		r.WeekWhy = fmt.Sprintf("last measured %s at %v%%: it may have risen since", ago(now, wk.Measured, wk.Seen), wk.SevenPct) + blind
	default:
		r.Week, r.WeekPct, r.WeekReset = WeekOK, wk.SevenPct, wk.Reset
		r.WeekWhy = "measured " + ago(now, wk.Measured, wk.Seen) + blind
	}
}

// ago is how long ago a measurement was, in whole hours past one hour; with none, the newest line of any kind.
func ago(now, measured, seen time.Time) string {
	if measured.IsZero() {
		return "never this window (newest line " + now.Sub(seen).Truncate(time.Minute).String() + " ago)"
	}
	d := now.Sub(measured)
	if d >= time.Hour {
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%dm ago", int(d/time.Minute))
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

// signins gives one alert per account whose sign-in is not OK, one per login whose week is not OK (its rows share it),
// one per row whose login is not known, and one per machine whose spool could not be read: each raised only when due,
// once per key (account or login, and state) and day (the day as d.Now's clock reads it), so the 5 min runs and the 20
// min resend say it once. A new state the same day says it again.
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
	raise := func(key, text string) {
		if st.Signin[key] == day {
			return
		}
		st.Signin[key] = day
		out = append(out, [2]string{key, text})
	}
	w := Signins(SigninReads{Status: d.Signin, Login: d.Login, Spool: d.Spool}, d.Home, d.Now)
	loc := d.Now.Location()
	for _, r := range w.Rows {
		if r.State != SigninOK {
			raise("signin:"+r.Name+":"+r.State, r.alert(loc))
		}
	}
	for _, h := range slices.Sorted(maps.Keys(w.Unread)) {
		raise("week-unread:"+h, fmt.Sprintf("week samples UNREAD %s: %s; the weekly limit of a login used there may not be seen. Check by hand: %s",
			h, w.Unread[h], spoolShell(h)))
	}
	for _, g := range weekGroups(w.Rows) {
		if g[0].Week != WeekOK {
			id := g[0].Login
			if id == "" {
				id = "row " + g[0].Name
			}
			raise("week:"+id+":"+g[0].Week, weekAlert(g, loc))
		}
	}
	return out
}

// weekGroups puts the rows of one login together, in the order of Accounts; a row whose login is not known is a group
// of its own.
func weekGroups(rows []SigninRow) [][]SigninRow {
	var out [][]SigninRow
	at := map[string]int{}
	for _, r := range rows {
		if i, ok := at[r.Login]; ok && r.Login != "" {
			out[i] = append(out[i], r)
			continue
		}
		at[r.Login] = len(out)
		out = append(out, []SigninRow{r})
	}
	return out
}

func weekAlert(g []SigninRow, loc *time.Location) string {
	r := g[0]
	var dirs []string
	for _, x := range g {
		dirs = append(dirs, x.where())
	}
	if r.Login == "" {
		return fmt.Sprintf("week UNKNOWN %s (%s): %s. Check by hand on that machine: the login in its .claude.json, and a tab's statusline", r.Name, r.where(), r.WeekWhy)
	}
	head := "week " + r.Week + " " + r.LoginName
	if r.LoginName != "login "+r.Login {
		head += " (login " + r.Login + ")"
	}
	on := "; its dirs: " + strings.Join(dirs, ", ")
	reset := r.WeekReset.In(loc).Format("2006-01-02 15:04")
	switch r.Week {
	case WeekLimit:
		return fmt.Sprintf("%s: %v%% of its week used, blocked until %s; every session on it waits until then%s. Move its tabs to an account under %d%%: crius-spawn -move <project> <role>",
			head, r.WeekPct, reset, on, quota.Limit)
	case WeekNear:
		return fmt.Sprintf("%s: %v%% of its week used, resets %s; at 100%% every session on it waits for the reset%s. New launches pick an account under %d%% (crius-spawn)",
			head, r.WeekPct, reset, on, quota.Limit)
	default:
		return fmt.Sprintf("%s: %s%s. Its weekly state is not known: look at a tab's statusline on it", head, r.WeekWhy, on)
	}
}

// spoolShell is the command that shows a machine's newest spool lines by hand.
func spoolShell(host string) string {
	const tail = "tail -n 3 ~/.crius/spool/quota.jsonl"
	r, ok := Remotes[host]
	if !ok {
		return tail
	}
	t := ""
	if r.Target != "" {
		t = r.Target + " "
	}
	return r.SSH + " " + t + "'" + tail + "'"
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

// SigninTable is the morning view: each account, its sign-in end and state, its login's week, and for one not OK what
// to do.
func SigninTable(w SigninWatch, loc *time.Location) string {
	var b strings.Builder
	t := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "ACCOUNT\tWHERE\tENDS\tSTATE\tLOGIN\tWEEK\tRESETS\tNOTE")
	for _, r := range w.Rows {
		end := "-"
		if !r.End.IsZero() {
			end = r.End.In(loc).Format("2006-01-02 15:04")
		}
		note := r.Why
		if r.State == SigninOK {
			note += "; WARN from " + r.End.Add(-signinAhead).In(loc).Format("2006-01-02 15:04")
		}
		login, week, reset := "-", r.Week, "-"
		if r.LoginName != "" {
			login = r.LoginName
		}
		if r.Week != WeekUnknown {
			week = fmt.Sprintf("%v%% %s", r.WeekPct, r.Week)
		}
		if !r.WeekReset.IsZero() {
			reset = r.WeekReset.In(loc).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s; week: %s\n", r.Name, r.where(), end, r.State, login, week, reset, note, r.WeekWhy)
	}
	t.Flush()
	for _, h := range slices.Sorted(maps.Keys(w.Unread)) {
		fmt.Fprintf(&b, "spool unread %s: %s; check: %s\n", h, w.Unread[h], spoolShell(h))
	}
	for _, h := range slices.Sorted(maps.Keys(w.Bad)) {
		fmt.Fprintf(&b, "spool %s: %d lines not JSON, skipped\n", h, w.Bad[h])
	}
	for _, r := range w.Rows {
		switch r.State {
		case SigninWarn, SigninExpired:
			fmt.Fprintf(&b, "renew %s: %s\n", r.Name, r.Renew())
		case SigninUnknown, SigninUnread:
			fmt.Fprintf(&b, "check %s: %s\n", r.Name, r.Shell("status", false))
		}
	}
	return b.String()
}
