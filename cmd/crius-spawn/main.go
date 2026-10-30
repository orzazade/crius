// Command crius-spawn opens a team session and starts it:
//
//	crius-spawn [-account max|<name>|enterprise] [-parent <leader session id>] [-item <project>#<n>] <project> <role> <first prompt…>
//	crius-spawn -move [-account max|<name>] [-parent <leader session id>] [-item <project>#<n>] <project> <role> <first prompt…>
//	crius-spawn -resume <project> <role>
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
// With -move it moves a role off an account at its limit: once the role's Claude has stopped, a fresh session with the
// first prompt given (the role's brief) starts in the current session's folder, so on its worktree and branch, on
// another Max account (the one whose week resets soonest under 95%, or the row -account names), and the old tab is closed.
package main

import (
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
	account := flag.String("account", "", "max (the Max account whose week resets soonest, under 95%; it prints the pick and why), an accounts.toml Max row name, or enterprise (default: the project's first account in projects.toml)")
	parent := flag.String("parent", "", "the leader's session id, for the provenance tag")
	item := flag.String("item", "", "the work item, <project>#<n>, for the provenance tag")
	again := flag.Bool("resume", false, "bring the role's current session back: <project> <role>, nothing else")
	move := flag.Bool("move", false, "start the role afresh in its current session's folder on another Max account (-account, default the one whose week resets soonest under 95%); its Claude must have stopped")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: crius-spawn [-account max|<name>|enterprise] [-parent id] [-item project#n] <project> <role> <first prompt…>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -move [-account max|<name>] [-parent id] [-item project#n] <project> <role> <first prompt…>")
		fmt.Fprintln(os.Stderr, "       crius-spawn -resume <project> <role>")
		flag.PrintDefaults()
	}
	flag.Parse()
	d := launch.Deps{
		Lock:    filepath.Join(store.Home(), "launch.lock"),
		Cmux:    runCmux,
		Place:   func(project string) (string, string, string, error) { return ingest.Place(store.Home(), project) },
		Persona: func(project string) (string, error) { return ingest.Persona(store.Home(), project) },
		Pick: func(project, want string) (string, string, error) {
			name, dir, why, err := pick(store.Home(), project, want)
			if why != "" {
				fmt.Println(why) // the pick and why (owner 10-28): its tab shows only the quota line
			}
			return name, dir, err
		},
		Wait:  time.Second,
		Front: outbox.Front,
		Typed: outbox.Typed,
	}
	if *again {
		if flag.NArg() != 2 || *account != "" || *move || *parent != "" || *item != "" {
			flag.Usage()
			os.Exit(2)
		}
		if err := resume(store.Home(), flag.Arg(0), flag.Arg(1), d); err != nil {
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
	if *move {
		m, err := moved(store.Home(), s.Project, s.Role)
		if err != nil {
			fmt.Fprintln(os.Stderr, "crius-spawn:", err)
			os.Exit(1)
		}
		s.Move = m
	}
	ws, surface, tag, err := launch.Run(s, d)
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-spawn:", err)
		os.Exit(1)
	}
	fmt.Printf("OK %s %s\n%s\n", ws, surface, tag)
}

// resume brings back the session crius-hook keeps as the role's current one, and prints where it runs.
func resume(home, project, role string, d launch.Deps) error {
	s, err := stopped(home, project, role, "resume")
	if err != nil {
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

// moved is the role's current session as a move needs it: its folder and the account it ran on, the row that lists its
// config folder, else the row of the login that folder is signed into ("" when neither).
func moved(home, project, role string) (*launch.Moved, error) {
	s, err := stopped(home, project, role, "move")
	if err != nil {
		return nil, err
	}
	as, err := quota.LoadAccounts(home)
	if err != nil {
		return nil, err
	}
	a := as.Dir(s.Config)
	if a == nil {
		login, err := quota.LoginOf(s.Config)
		if err != nil {
			return nil, fmt.Errorf("%s %s: which account its session ran on: %w", project, role, err)
		}
		a = as.Of(login)
	}
	m := &launch.Moved{Cwd: s.Cwd}
	if a != nil {
		m.From = a.Name
	}
	return m, nil
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

// pick is the account a session of project starts on, its folder and why: want is "max" for the Max one whose week
// resets soonest, a Max row's name (quota.Pick), or "enterprise" (quota.Enterprise). The figures are the record
// store's, as crius-rec quota reads them.
func pick(home, project, want string) (name, dir, why string, err error) {
	root, pauseAt, err := ingest.ForSpawn(home, project)
	if err != nil {
		return "", "", "", err
	}
	projects, err := ingest.QuotaProjects(home)
	if err != nil {
		return "", "", "", err
	}
	if want == "enterprise" {
		name, dir, err = quota.Enterprise(projects.Accounts, root)
		return name, dir, "", err
	}
	db, err := store.Open(home)
	if err != nil {
		return "", "", "", err
	}
	defer db.Close()
	r, err := quota.Build(db, projects, time.Now())
	if err != nil {
		return "", "", "", err
	}
	return quota.Pick(r, projects.Accounts, root, want, pauseAt)
}

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

// maxRow is the row of accounts.toml that lists config folder, and that folder; "" when none does. An enterprise
// row counts too: resumed through aitch's route, an enterprise session ran on Max (Crius 10-30 22:44).
func maxRow(as *quota.Accounts, config string) (acct, dir string) {
	if a := as.Dir(config); a != nil {
		return a.Name, config
	}
	return "", ""
}
