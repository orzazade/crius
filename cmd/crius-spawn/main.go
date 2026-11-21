// Command crius-spawn opens a team session and starts it:
//
//	crius-spawn [-backend cmux|t3] [-account max|<name>] [-parent <leader session id>] [-item <project>#<n>] <project> <role> <first prompt…>
//	crius-spawn -move [-account max|<name>] <project> <role>
//	crius-spawn -resume <project> <role>
//	crius-spawn -close <project> <role>
//	crius-spawn -fresh [-backend cmux|t3] [-account max|<name>] [-parent id] [-item project#n] <project> <role> <first prompt…>
//	crius-spawn -move-check < <PreToolUse hook input>
//
// The tab lands in the project's workspace (made, named and coloured when missing), is named "<project> · <role>",
// gets the folder-trust dialog answered, its model checked, and the provenance tag line typed before the first prompt.
// It prints the workspace, the tab and the tag, and fails loudly when any step does not show on screen.
// On a Max account it starts in the folder of the accounts.toml row whose week resets soonest under 95% (or the row
// -account names), and prints that pick and why (its tab is "<project> · <role>", the account shows in its quota
// line); with no such row aitch's own route picks.
//
// With -resume it brings the role's current session back after a crash or a reboot (claude --resume), in its own tab
// when that is still open with a shell in front, else in a new one. The current session is the one crius-hook last saw
// start for the role, kept in $CRIUS_HOME/roles/<project>.<role>.json. A session that ran in a Max row's
// folder of accounts.toml comes back in that folder, on that account.
//
// With -move it moves a role off an account at its limit as the same session (owner 10-30): at a turn boundary, with no
// background job but its bus waiter running (crius:lead 11-08), it types /exit into the role's tab and resumes the
// session there on the Max account whose week resets soonest under 95% (or the row -account names), then prints the new
// Claude's pid and config folder. A session busy or running a job is left alone, and a resume that lands in another
// folder fails.
//
// With -backend t3 the role runs as a T3 Code thread instead of a tab (launch.RunT3): on the same account pick, on the
// T3 provider instance that runs in that account's folder, titled "<project> · <role>", with the tag line before the
// first prompt; it prints the thread, its Claude session and its account, and fails loudly when the thread starts no
// work or crius-hook files no session for it. -resume of a role whose session runs in a thread sends it the resume
// prompt there; -move of one is refused (launch.ErrT3Move: T3 cannot keep the session across accounts).
//
// With -fresh it replaces a role's session that runs in a T3 thread with a new one (11-20: -close leaves the old claude
// running, so the next spawn made two live sessions for axi qa-2): it waits for the thread's turn boundary (no run
// open, no open request, no background job of its own; it refuses, changing nothing, when it cannot read the thread or
// the wait runs out), marks the old session closed, stops its claude by its own pid (SIGTERM, then SIGKILL), then
// starts the new session as a plain spawn does (same -account rules; -backend defaults to t3 here) and prints both
// sessions. On a failure after the close it puts the closed mark back as it was.
//
// With -move-check it is the PreToolUse hook for T3 Code's t3_thread_configure (see runMoveCheck): it denies, with
// the reason, a move of a T3 thread whose run, open request or ⏳ reply means a restart would kill its background jobs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	account := flag.String("account", "", "max (the Max account whose week resets soonest, under 95%; it prints the pick and why), or an accounts.toml Max row name (default: the project's first account in projects.toml); never the enterprise account (owner 11-07)")
	parent := flag.String("parent", "", "the leader's session id, for the provenance tag")
	item := flag.String("item", "", "the work item, <project>#<n>, for the provenance tag")
	again := flag.Bool("resume", false, "bring the role's current session back: <project> <role>, nothing else")
	closed := flag.Bool("close", false, "mark the role's current session closed on purpose: <project> <role>, nothing else; wakewatch's dead-role check skips it until another session of the role starts or -resume brings it back")
	move := flag.Bool("move", false, "resume the role's current session on another Max account (-account, default the one whose week resets soonest under 95%): <project> <role>; only at a turn boundary with no background job but its bus waiter")
	fresh := flag.Bool("fresh", false, "replace the role's session that runs in a T3 thread: at its turn boundary mark it closed, stop its claude by pid, then start a new session as a plain spawn does (-backend defaults to t3 here): <project> <role> <first prompt…>")
	backend := flag.String("backend", "cmux", "where a new role runs: cmux (a tab) or t3 (a T3 Code thread); -resume and -move go by where the role's session runs")
	moveCheck := flag.Bool("move-check", false, "PreToolUse hook for "+configureTool+": hook input on stdin; denies, with the reason, moving a T3 thread that is busy or cannot be read")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: crius-spawn [-backend cmux|t3] [-account max|<name>] [-parent id] [-item project#n] <project> <role> <first prompt…>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -move [-account max|<name>] <project> <role>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -resume <project> <role>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -close <project> <role>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -fresh [-backend cmux|t3] [-account max|<name>] [-parent id] [-item project#n] <project> <role> <first prompt…>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -move-check < <PreToolUse hook input>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *moveCheck {
		if flag.NArg() != 0 || flag.NFlag() != 1 {
			flag.Usage()
			os.Exit(2)
		}
		runMoveCheck(store.Home(), os.Stdin, os.Stdout)
		return
	}
	d := launch.Deps{
		Lock:    filepath.Join(store.Home(), "launch.lock"),
		Cmux:    runCmux,
		Place:   func(project string) (string, string, string, error) { return ingest.Place(store.Home(), project) },
		Persona: func(project string) (string, error) { return ingest.Persona(store.Home(), project) },
		Pick: func(project, want string) (string, string, error) {
			name, dir, why, err := ingest.PickAccount(store.Home(), project, want)
			if why != "" {
				fmt.Println(why) // the pick and why (owner 10-28): its tab shows only the quota line
			}
			return name, dir, err
		},
		Wait:  time.Second,
		Front: outbox.Front,
		Typed: outbox.Typed,
		Send:  func(ws, surface, text string) error { return outbox.SendTo(store.Home(), ws, surface, text) },
	}
	t3 := launch.T3{CLI: runT3, Instances: t3Instances, Roles: filepath.Join(store.Home(), "roles"), Wait: time.Second, Tries: 120,
		Limit: launch.StartLimit, Now: time.Now, Sleep: time.Sleep, Log: os.Stderr,
		Sessions: func() ([]sessionstate.State, error) {
			ss, err := sessionstate.List(filepath.Join(store.Home(), "sessions"))
			if errors.Is(err, sessionstate.ErrDamaged) { // one damaged file names no thread; the others still do
				err = nil
			}
			return ss, err
		}}
	if *backend != "cmux" && *backend != "t3" {
		fmt.Fprintf(os.Stderr, "crius-spawn: bad -backend %q: want cmux or t3\n", *backend)
		os.Exit(2)
	}
	if *backend != "cmux" && (*closed || *move || *again) {
		fmt.Fprintln(os.Stderr, "crius-spawn: -backend is for a new role; -resume and -move go by where the role's session runs")
		os.Exit(2)
	}
	if *closed {
		if flag.NArg() != 2 || *fresh || *again || *account != "" || *move || *parent != "" || *item != "" {
			flag.Usage()
			os.Exit(2)
		}
		if err := closeRole(store.Home(), flag.Arg(0), flag.Arg(1)); err != nil {
			fmt.Fprintln(os.Stderr, "crius-spawn:", err)
			os.Exit(1)
		}
		return
	}
	if *move {
		if flag.NArg() != 2 || *fresh || *again || *parent != "" || *item != "" {
			flag.Usage()
			os.Exit(2)
		}
		m := mover{home: store.Home(), want: *account, tries: 60, wait: time.Second, procs: psProcs, cmux: runCmux,
			pick: d.Pick,
			state: func(id string) (sessionstate.State, error) {
				return sessionstate.Read(filepath.Join(store.Home(), "sessions"), id)
			},
			resume: func(project, role, id, cwd, acct, dir string) (string, string, error) {
				return launch.Resume(project, role, id, cwd, acct, dir, d)
			}}
		out, err := m.move(flag.Arg(0), flag.Arg(1))
		if err != nil {
			fmt.Fprintln(os.Stderr, "crius-spawn:", err)
			os.Exit(1)
		}
		fmt.Println(out)
		return
	}
	if *again {
		if flag.NArg() != 2 || *fresh || *account != "" || *move || *parent != "" || *item != "" {
			flag.Usage()
			os.Exit(2)
		}
		if err := resume(store.Home(), flag.Arg(0), flag.Arg(1), d, t3); err != nil {
			fmt.Fprintln(os.Stderr, "crius-spawn:", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() < 3 {
		flag.Usage()
		os.Exit(2)
	}
	s := launch.Spawn{Project: flag.Arg(0), Role: flag.Arg(1), Account: *account, Parent: *parent, Item: *item,
		Prompt: strings.Join(flag.Args()[2:], " ")}
	start := func() (string, error) { return spawnNew(s, *backend, d, t3) }
	if *fresh {
		set := false
		flag.Visit(func(fl *flag.Flag) { set = set || fl.Name == "backend" })
		if !set { // -fresh replaces a T3 thread: the new session runs in one too unless -backend says otherwise
			start = func() (string, error) { return spawnNew(s, "t3", d, t3) }
		}
		f := newFreshener(store.Home(), start)
		start = func() (string, error) { return f.fresh(s.Project, s.Role) }
	}
	out, err := start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-spawn:", err)
		os.Exit(1)
	}
	fmt.Println(out)
}

// spawnNew starts a new session for s in a tab (cmux) or a T3 Code thread (t3), and gives what it prints.
func spawnNew(s launch.Spawn, backend string, d launch.Deps, t3 launch.T3) (string, error) {
	if backend == "t3" {
		st, err := launch.RunT3(s, d, t3)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("OK t3 %s session %s on %s (T3 instance %s)\n%s", st.Thread, st.Session, st.Account, st.Instance, st.Tag), nil
	}
	ws, surface, tag, err := launch.Run(s, d)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("OK %s %s\n%s", ws, surface, tag), nil
}

// resume brings back the session crius-hook keeps as the role's current one, and prints where it runs.
func resume(home, project, role string, d launch.Deps, t launch.T3) error {
	if r, err := sessionstate.ReadRole(filepath.Join(home, "roles"), project, role); err == nil {
		th, err := launch.RoleThread(home, project, role, r)
		if err != nil {
			return err
		}
		if th != "" { // it runs in a thread, not a tab
			if err := launch.ResumeT3(th, t); err != nil {
				return fmt.Errorf("%s %s: %w", project, role, err)
			}
			if err := sessionstate.OpenRole(filepath.Join(home, "roles"), project, role, r.SessionID); err != nil { // back: no longer closed
				return err
			}
			fmt.Printf("OK t3 %s resumed\n", th)
			return nil
		}
	}
	s, err := stopped(home, project, role, "resume")
	if err != nil {
		return err
	}
	if err := sessionstate.OpenRole(filepath.Join(home, "roles"), project, role, s.SessionID); err != nil { // wanted back: no longer closed
		return err
	}
	acct, dir, err := sessionAccount(home, s.FiledConfig()) // its account, wherever a restore put it
	if err != nil {
		return err
	}
	ws, surface, err := launch.Resume(project, role, s.SessionID, s.Cwd, acct, dir, d)
	if err != nil {
		return err
	}
	fmt.Printf("OK %s %s resumed %s\n", ws, surface, s.SessionID)
	return nil
}

// closeRole marks the role's current session closed on purpose (owner 11-08), and prints which.
func closeRole(home, project, role string) error {
	dir := filepath.Join(home, "roles")
	s, err := sessionstate.ReadRole(dir, project, role)
	if err != nil {
		return fmt.Errorf("no session to close for %s %s: %w", project, role, err)
	}
	if err := sessionstate.CloseRole(dir, project, role, s.SessionID); err != nil {
		return err
	}
	fmt.Printf("OK %s %s closed: session %s\n", project, role, s.SessionID)
	return nil
}

// stopped is the session crius-hook keeps as the role's current one, refused while its Claude still runs. A role with
// no file is refused with how to go on (unfiled).
func stopped(home, project, role, verb string) (sessionstate.State, error) {
	s, err := sessionstate.ReadRole(filepath.Join(home, "roles"), project, role)
	if errors.Is(err, fs.ErrNotExist) {
		err = unfiled(home, project, role, verb, err)
	}
	if err != nil {
		return s, fmt.Errorf("no session to %s for %s %s: %w", verb, project, role, err)
	}
	// Two Claudes on one session write one transcript: whatever its tab is titled, a running session is left alone.
	if pid, _, err := sessionstate.Process(s.Config, s.SessionID); err != nil {
		return s, err
	} else if pid != 0 {
		return s, fmt.Errorf("%s %s: session %s still runs in process %d (/exit in its tab stops it): nothing %sd", project, role, s.SessionID, pid, verb)
	}
	return s, nil
}

// unfiled says how to go on for a role whose file is missing (err) (10-28: falcon's lead, started by hand without its
// name, had none): crius writes the file when a session named "<project> · <role>" starts (claude -n). It names the
// newest stopped sessions crius filed under no role in the project's launch folder, where a lead started by hand runs,
// each with the line that resumes it by hand as the role. Agents in worktrees and scratchpads are never offered.
func unfiled(home, project, role, verb string, err error) error {
	msg := fmt.Sprintf("%v: crius writes it when a session named %q starts (claude -n)", err, project+" · "+role)
	folder, _, _, err := ingest.Place(home, project)
	if err != nil {
		return fmt.Errorf("%s; no session offered: %v", msg, err)
	}
	as, err := quota.LoadAccounts(home)
	if err != nil {
		return fmt.Errorf("%s; no session offered: %v", msg, err)
	}
	all, err := sessionstate.List(filepath.Join(home, "sessions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, sessionstate.ErrDamaged) {
		return fmt.Errorf("%s; crius's session files: %v", msg, err)
	} else if err != nil {
		msg += fmt.Sprintf(" (%v)", err)
	}
	var unnamed []sessionstate.State
	for _, s := range all {
		if s.Project == project && s.Role == "unknown" && s.Cwd != "" && filepath.Clean(s.Cwd) == folder {
			unnamed = append(unnamed, s)
		}
	}
	slices.SortFunc(unnamed, func(a, b sessionstate.State) int { return b.Time.Compare(a.Time) })
	var offers, unsure []string
	for _, s := range unnamed {
		if len(offers) == 3 {
			break
		}
		if pid, _, err := sessionstate.Process(s.Config, s.SessionID); err != nil {
			unsure = append(unsure, err.Error())
			continue
		} else if pid != 0 { // a running Claude is left alone, as for a filed role
			continue
		}
		acct, dir := maxRow(as, s.FiledConfig())
		line, err := launch.ResumeLine(project, role, s.SessionID, s.Cwd, acct, dir)
		if err != nil {
			unsure = append(unsure, err.Error())
			continue
		}
		offers = append(offers, fmt.Sprintf("%s (name %q, last event %s): %s", s.SessionID, s.Name, s.Time.Local().Format("2006-01-02 15:04"), line))
	}
	if unsure != nil {
		msg += fmt.Sprintf("; %d sessions skipped, their Claude or their resume line cannot be checked (%s)", len(unsure), unsure[0])
	}
	if offers == nil {
		return fmt.Errorf("%s; crius saw no stopped session in %s filed under no role", msg, folder)
	}
	return fmt.Errorf("%s. The newest stopped sessions in %s filed under no role: if one is the %s, run its line in a new "+
		"tab of the project's workspace (the name files it, then -%s works): %s", msg, folder, role, verb, strings.Join(offers, "; "))
}

// runCmux gives cmux's stdout, or on failure its stdout and stderr: a notice on stderr must not reach a parse.
func runCmux(args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.Command("cmux", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}

// t3Timeout bounds one t3 call: it takes about 1 s, up to 5 s under load; a hung server must not hold the launch.
const t3Timeout = 30 * time.Second

// runT3 runs the t3 in the Crius bin folder when there is one (nvm keeps t3 off launchd's PATH), else t3 on the PATH,
// with stdin, and gives its stdout; on failure its stderr and stdout with the error.
func runT3(stdin string, args ...string) (string, error) {
	bin := "t3"
	if p := filepath.Join(store.Home(), "bin", "t3"); fileThere(p) {
		bin = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), t3Timeout)
	defer cancel()
	var out, errOut strings.Builder
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &errOut
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("no answer in %s", t3Timeout)
		}
		return out.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(errOut.String()+"\n"+out.String()))
	}
	return out.String(), nil
}

// t3Instances reads T3 Code's provider instances from its settings.json, in $T3CODE_HOME (default ~/.t3).
func t3Instances() (map[string]string, error) {
	home := os.Getenv("T3CODE_HOME")
	if h, err := os.UserHomeDir(); err == nil {
		switch {
		case home == "":
			home = filepath.Join(h, ".t3")
		case home == "~" || strings.HasPrefix(home, "~/"):
			home = filepath.Join(h, home[1:])
		}
	}
	return launch.T3Instances(filepath.Join(home, "userdata", "settings.json"))
}

// fileThere says p is there, a link that points nowhere too: running it then fails under its own name.
func fileThere(p string) bool { _, err := os.Lstat(p); return err == nil }

// sessionAccount is the row of accounts.toml that lists config, the folder a session ran in, and that folder; ""
// when no row lists it.
func sessionAccount(home, config string) (acct, dir string, err error) {
	as, err := quota.LoadAccounts(home)
	if err != nil {
		return "", "", err
	}
	acct, dir = maxRow(as, config)
	return acct, dir, nil
}

// maxRow is the Max row of accounts.toml that lists config folder, and that folder; "" when none does. An enterprise
// row never counts (owner 11-07: the enterprise account is never launched): aitch's route places that session.
func maxRow(as *quota.Accounts, config string) (acct, dir string) {
	if a := as.Dir(config); a != nil && a.Plan == "max" {
		return a.Name, config
	}
	return "", ""
}
