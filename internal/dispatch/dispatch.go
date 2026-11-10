// Package dispatch is the dispatcher's one run: it reads a project's board and bus, starts a fresh session for each
// ready criterion up to a cap, and moves rows on what their sessions and PRs do. No model runs in it.
//
// One run: read the PR states (no lock held); then, under the board's lock, apply them, apply the bus lines after the
// cursor, look at the building sessions, claim the rows to start (each moved to building with its session before any
// tab opens), and write the board and the cursor; then, with no lock held, open the tabs and post. A spawn that fails
// or half-works has claimed its row already, so no later run opens a second tab for it: the dead-session rule gives it
// one restart and then blocks it. A run cut off before its spawns leaves claimed rows whose sessions read dead, which
// the same rule handles.
//
// <home>/state/<project>/DISPATCH.toml:
//
//	cap = 3                      # rows building at once (rows in pr or merged keep their sessions and are not counted)
//	prefix = "pilot-"            # a row's role is prefix + its id in lower case (AC12 -> pilot-ac12)
//	prompt = "Your criterion: {id}. Read ~/.crius/state/<project>/PILOT-BRIEF.md first and follow it."
//	pipeline = 42                # optional: the pipeline whose run proves a PR into a branch with no Build policy
//	merge = "~/bin/land.sh {pr} {build} {target}"  # optional: the queue completes the named PR with this command
//	merge_into = ["refs/heads/integration/x"]      # the only branches it may complete into; never main, master or develop
//
// The merge queue (plan step 3, until a program may complete PRs): the rows in pr, oldest PR number first. The first
// whose ci-proof passes is named to <project>:lead in one MERGE line, once per PR, head and build; the next is named
// only after it leaves pr (merged, blocked, or back to building) or stops passing (FAIL, STALE, or a new head not yet
// green; a NONE on the named head holds it only while in progress); a new passing head or build on it is said again.
// When the named one ends and none passes, the lead hears "none ready" once, with each PR's state (a STALE one needs a new build). A run that
// could not read every row's proof, or in which a row in pr left it or changed its PR, leaves the queue as it was.
// What was said is in mergeq.told beside the board.
//
// With merge set (owner 11-10: the queue may complete PRs into the integration branch), the named PR is not told to the
// lead but completed: its target must be in merge_into and its proof must name one build, and the proof is read again
// just before (one that changed waits for the next run). Under mergeq.lock (held while the queue decides and merges),
// "running <key>" goes into mergeq.told and the merge command runs (bash, after the board is saved, up to 20 min, given
// {pr}, {build} and {target}, the branch name without refs/heads/; its own gates decide:
// review, QA, vote, build, and it must refuse a PR whose target is no longer {target}). The lead hears INFO "completed"
// or MERGE "not completed" with the command's last line, recorded before it is posted. So the command runs once per
// PR, head and build: no second run beside a live one; one stopped at its limit, or whose dispatcher ended during it,
// is said once as "outcome unknown". A new head or build, or removing mergeq.told, tries again. While it runs, the dispatcher does nothing else.
package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/orzazade/crius/internal/board"
)

// Deps is what one run needs. Spawn starts a session for a role with a first prompt, Close marks a role's session
// closed on purpose (crius-spawn -close; it does not end the Claude), Alive says whether a role's session runs, PRState
// gives a PR's status ("active", "completed", "abandoned"), Proof gives ci-proof's line for an active PR (exit 1 is a
// line, never an error; pipeline is DISPATCH.toml's, 0 for none), Post writes a bus line from crius:dispatch on the
// project's bus.
type Deps struct {
	Home, Project string
	Now           time.Time
	Spawn         func(role, prompt string) error
	Close         func(role string) error
	Alive         func(role string) (bool, error)
	PRState       func(pr int) (string, error)
	Proof         func(pr, pipeline int) (string, error)
	Target        func(pr int) (string, error)         // a PR's target branch (refs/heads/...)
	Merge         func(command string) (string, error) // runs the project's merge command; its output and how it ended
	Post          func(typ, to, text string) error
}

type config struct {
	Cap    int    `toml:"cap"`
	Prefix string `toml:"prefix"`
	Prompt string `toml:"prompt"`
	// Pipeline proves a PR into a branch with no Build policy: ci-proof -pipeline (0: the Build policy alone)
	Pipeline int `toml:"pipeline"`
	// Merge completes the named PR ({pr}, {build}) when its target is one of MergeInto; none: the lead is told only
	Merge     string   `toml:"merge"`
	MergeInto []string `toml:"merge_into"`
}

const (
	lead      = "crius:lead"
	self      = "crius:dispatch"
	restarted = " Your last session ended: read your log and go on."
	// grace is how long a building session may read dead before it is started again: crius-spawn -move ends a
	// session and resumes it on another account in that time.
	grace = 2 * time.Minute
)

var (
	lineRe = regexp.MustCompile(`^\S+ ([A-Z][A-Z-]*) (\S+) (?:->|→) (.*?) — (.*)$`)
	prRe   = regexp.MustCompile(`(?i)\bPR #?(\d+)\b`)
	partRe = regexp.MustCompile(`\bPARTIAL\b`) // capitals only: "partial refunds" is product text
)

type post struct{ typ, to, text string }

type spawn struct{ id, role, prompt string }

// Run is one dispatcher run.
func Run(d Deps) error {
	dir := filepath.Join(d.Home, "state", d.Project)
	var cfg config
	if _, err := toml.DecodeFile(filepath.Join(dir, "DISPATCH.toml"), &cfg); err != nil {
		return fmt.Errorf("reading the dispatch config: %w", err)
	}
	if cfg.Cap < 1 || cfg.Prefix == "" || !strings.Contains(cfg.Prompt, "{id}") {
		return fmt.Errorf("DISPATCH.toml needs cap >= 1, a prefix and a prompt with {id}")
	}
	if cfg.Merge != "" && len(cfg.MergeInto) == 0 {
		return fmt.Errorf("DISPATCH.toml: merge needs merge_into, the branches it may complete PRs into")
	}
	if cfg.Merge != "" && !(strings.Contains(cfg.Merge, "{pr}") && strings.Contains(cfg.Merge, "{build}") && strings.Contains(cfg.Merge, "{target}")) {
		// the command checks the PR still targets the branch the queue checked (review P2-3)
		return fmt.Errorf("DISPATCH.toml: merge needs {pr}, {build} and {target}")
	}
	for _, b := range cfg.MergeInto { // owner 11-10: never develop or main
		for _, d := range []string{"refs/heads/main", "refs/heads/master", "refs/heads/develop"} {
			if strings.EqualFold(b, d) {
				return fmt.Errorf("DISPATCH.toml: merge_into may not name %s", b)
			}
		}
	}
	boardPath := filepath.Join(dir, "BOARD.toml")
	session := func(id string) string { return d.Project + ":" + role(cfg, id) }
	answerers := map[string]bool{lead: true, d.Project + ":lead": true}

	// 0. the PR states, read with no lock held (az may take a minute); applied below only to a row still on that PR
	b0, err := board.Load(boardPath)
	if err != nil {
		return err
	}
	var errs []error
	prs := map[int]string{}
	proofs := map[int]string{}
	for _, r := range b0.Rows {
		if r.State != board.PR {
			continue
		}
		st, err := d.PRState(r.PR)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s PR %d: %w", r.ID, r.PR, err))
			continue
		}
		prs[r.PR] = st
		if st != "active" {
			if st != "completed" && st != "abandoned" {
				errs = append(errs, fmt.Errorf("%s PR %d: state %q is none the dispatcher knows", r.ID, r.PR, st))
			}
			continue
		}
		line, err := d.Proof(r.PR, cfg.Pipeline)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s PR %d: %w", r.ID, r.PR, err))
			continue
		}
		proofs[r.PR] = line
	}

	var posts []post
	var spawns []spawn
	var closes []string   // sessions to close, after the board is saved
	var queue []board.Row // rows in pr once the board is saved
	upto := int64(-1)
	err = board.Update(boardPath, d.Now, func(b *board.Board) error {
		lines, n, err := newLines(filepath.Join(d.Home, "bus", d.Project+".log"), filepath.Join(dir, "dispatch.cursor"))
		if err != nil {
			return err
		}
		upto = n
		bySession := map[string]string{}
		for _, r := range b.Rows {
			bySession[session(r.ID)] = r.ID
		}
		state := func(id string) board.Row {
			for _, r := range b.Rows {
				if r.ID == id {
					return r
				}
			}
			return board.Row{}
		}
		move := func(id, to, why string, set func(*board.Row)) bool {
			if err := b.Move(id, to, self, why, d.Now, set); err != nil {
				errs = append(errs, err)
				return false
			}
			return true
		}
		closeRole := func(r board.Row) {
			if r.Session == "" {
				return // proven by its check alone: no session was started
			}
			closes = append(closes, r.ID)
		}
		// 1. the PRs (before the bus, so a VERIFIED in the minute of its merge finds the row merged)
		for _, r := range b.Rows {
			st, ok := prs[r.PR]
			if r.State != board.PR || !ok {
				continue
			}
			switch st {
			case "completed":
				move(r.ID, board.Merged, fmt.Sprintf("PR %d merged", r.PR), nil)
			case "abandoned":
				pr := r.PR
				if move(r.ID, board.Building, fmt.Sprintf("PR %d abandoned", pr), func(r *board.Row) { r.PR = 0 }) {
					posts = append(posts, post{"INFO", session(r.ID), fmt.Sprintf("%s: PR %d was abandoned; the row is back to building", r.ID, pr)})
				}
			}
		}
		// 2. what the sessions, Crius and the project's lead said
		for _, l := range lines {
			m := lineRe.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			typ, sender, to, text := m[1], m[2], recipients(m[3]), m[4]
			if id, ok := bySession[sender]; ok {
				r := state(id)
				working := r.State == board.Building || r.State == board.Blocked && r.From == board.Building
				switch {
				case (typ == "NEED" || typ == "ASK" || typ == "BLOCKED" || strings.HasPrefix(typ, "NEED-")) &&
					(r.State == board.Building || r.State == board.PR):
					move(id, board.Blocked, typ+" from its session", func(r *board.Row) { r.Question = text })
				case typ == "DONE" && r.State == board.Building && partRe.MatchString(text):
					move(id, board.Blocked, "its session ended partial", func(r *board.Row) { r.Question = text })
				case typ == "DONE" && working && len(prNumbers(text)) > 1:
					ns := prNumbers(text)
					posts = append(posts, post{"NEED", lead, fmt.Sprintf("%s: its DONE names PR %s; set the right one with crius-board %s set %s pr --pr <n>", id, strings.Join(ns, " and PR "), d.Project, id)})
				case typ == "DONE" && working && prRe.MatchString(text):
					if r.State == board.Blocked && !move(id, board.Building, "its session went on and opened its PR", nil) {
						continue
					}
					n, _ := strconv.Atoi(prRe.FindStringSubmatch(text)[1])
					move(id, board.PR, "its session opened the PR", func(r *board.Row) { r.PR = n })
				}
				continue
			}
			if sender == self || len(to) == 0 {
				continue
			}
			// an answer counts only when it is sent straight to the session (not a cc) by Crius or the project's lead
			if id, ok := bySession[to[0]]; ok && answerers[sender] && (typ == "DECISION" || typ == "ANSWER" || typ == "ORDER") {
				if r := state(id); r.State == board.Blocked {
					// the answer is new: a fresh restart, and no grace (a dead session is started again in step 3 of this run)
					long := d.Now.Add(-grace)
					move(id, r.From, sender+" answered", func(r *board.Row) {
						r.Restarts, r.Dead = 0, nil
						if r.From == board.Building {
							r.Dead = &long
						}
					})
				}
			}
			if typ != "VERIFIED" || sender != lead {
				continue
			}
			for _, s := range to {
				id, ok := bySession[s]
				if !ok || !regexp.MustCompile(`\b`+regexp.QuoteMeta(id)+`\b`).MatchString(text) {
					continue // a VERIFIED proves the row it names
				}
				r := state(id)
				switch r.State {
				case board.PR: // Crius saw it live on the merged code before az said merged
					if move(id, board.Merged, "Crius verified it on the merged code", nil) && move(id, board.Proven, "Crius verified it live", nil) {
						closeRole(r)
					}
				case board.Building, board.Ready, board.Merged:
					if move(id, board.Proven, "Crius verified it live", nil) {
						closeRole(r)
					}
				}
			}
		}
		// 3. sessions that ended with no result: after the grace, started again once, then the row waits on Crius
		for _, r := range b.Rows {
			if r.State != board.Building || r.Session == "" {
				continue
			}
			alive, err := d.Alive(role(cfg, r.ID))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: is %s alive: %w", r.ID, r.Session, err))
				continue
			}
			note := func(why string, set func(*board.Row)) {
				if err := b.Note(r.ID, self, why, d.Now, set); err != nil {
					errs = append(errs, err)
				}
			}
			switch {
			case alive && r.Dead != nil:
				note("its session runs again", func(r *board.Row) { r.Dead = nil })
			case alive:
			case r.Dead == nil:
				now := d.Now
				note("its session reads ended; checked again in 2 min", func(r *board.Row) { r.Dead = &now })
			case d.Now.Sub(*r.Dead) < grace:
			case r.Restarts == 0: // counts tries, not successes: a restart that fails is not tried again
				note("its session ended; started again", func(r *board.Row) { r.Restarts++; r.Dead = nil })
				spawns = append(spawns, spawn{r.ID, role(cfg, r.ID), prompt(cfg, r.ID) + restarted})
			default:
				move(r.ID, board.Blocked, "its session ended twice", func(r *board.Row) { r.Question = "its session ended twice with no result" })
				posts = append(posts, post{"NEED", lead, fmt.Sprintf("%s: %s ended twice with no result; the row is blocked", r.ID, r.Session)})
			}
		}
		// 4. claim ready rows free of others' files, up to the cap; a role that runs already is taken over, not spawned
		building := 0
		for _, r := range b.Rows {
			if r.State == board.Building || r.State == board.Blocked && r.From == board.Building {
				building++
			}
		}
		for building < cfg.Cap {
			r, ok := b.Next()
			if !ok {
				break
			}
			s := session(r.ID)
			alive, err := d.Alive(role(cfg, r.ID))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: is %s alive: %w", r.ID, s, err))
				break
			}
			why := "claimed for " + s
			if alive {
				why = s + " runs already; taken over"
			}
			if !move(r.ID, board.Building, why, func(r *board.Row) { r.Session, r.Dead = s, nil }) {
				break
			}
			if !alive {
				spawns = append(spawns, spawn{r.ID, role(cfg, r.ID), prompt(cfg, r.ID)})
			}
			building++
		}
		for _, r := range b.Rows {
			if r.State == board.PR {
				queue = append(queue, r)
			}
		}
		return nil
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	// the cursor moves only once the board is saved: a run cut off before this reads its lines again, which moves
	// nothing twice (each rule needs the state the line moves the row from)
	if err := writeCursor(filepath.Join(dir, "dispatch.cursor"), upto); err != nil {
		errs = append(errs, err)
	}
	for _, id := range closes {
		if err := d.Close(role(cfg, id)); err != nil {
			errs = append(errs, fmt.Errorf("closing %s: %w", session(id), err))
		}
	}
	// 5. open the tabs, with no lock held
	var started []string
	for _, s := range spawns {
		if err := d.Spawn(s.role, s.prompt); err != nil {
			errs = append(errs, fmt.Errorf("%s: starting %s: %w", s.id, session(s.id), err))
			continue
		}
		started = append(started, session(s.id)+" on "+s.id)
	}
	if len(started) > 0 {
		posts = append(posts, post{"INFO", lead, fmt.Sprintf("started %s (cap %d)", strings.Join(started, ", "), cfg.Cap)})
	}
	for _, p := range posts {
		if err := d.Post(p.typ, p.to, p.text); err != nil {
			errs = append(errs, fmt.Errorf("posting %q: %w", p.text, err))
		}
	}
	// settled: every row in pr when the proofs were read is still in pr on the same PR (none merged, was abandoned, was
	// verified, blocked or sent back), so the target did not move under those proofs through the board
	settled := true
	for _, r := range b0.Rows {
		if r.State == board.PR {
			settled = settled && slices.ContainsFunc(queue, func(q board.Row) bool { return q.ID == r.ID && q.PR == r.PR })
		}
	}
	if err := mergeQueue(d, cfg, dir, queue, proofs, settled); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// mergeQueue tells the project's lead which PR to merge next (see the package comment). A run in which a queued row
// left pr or changed its PR (settled false) decides nothing: the others' proofs may be read against the old target.
func mergeQueue(d Deps, cfg config, dir string, queue []board.Row, proofs map[int]string, settled bool) error {
	if !settled {
		return nil
	}
	slices.SortFunc(queue, func(a, b board.Row) int { return a.PR - b.PR })
	for _, r := range queue {
		if _, ok := proofs[r.PR]; !ok {
			return nil // not read in this run (it reached pr in it, or the read failed and is said): decide nothing
		}
	}
	toldPath := filepath.Join(dir, "mergeq.told")
	if cfg.Merge != "" {
		// held while the queue decides and merges; the system frees it when its dispatcher ends (review r2 P3-A, P3-B)
		lock, err := os.OpenFile(filepath.Join(dir, "mergeq.lock"), os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
			return nil // another dispatcher is deciding or merging
		} else if err != nil {
			return fmt.Errorf("locking the merge queue: %w", err)
		}
	}
	b, err := os.ReadFile(toldPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading what the merge queue said: %w", err)
	}
	told := strings.TrimSpace(string(b))
	if key, ok := strings.CutPrefix(told, "running "); ok {
		// a merge command was started by a run that ended during it (a live one holds the lock): say so once
		f := strings.Fields(key) // next <pr> <head> <build>
		if len(f) != 4 {
			return fmt.Errorf("mergeq.told %q: not running next <pr> <head> <build>", told)
		}
		return sayDone(d, toldPath, key, unknown(f[1], f[2], f[3], "with no answer (its dispatcher ended)"))
	}
	keyOf := func(i int) string {
		f := proofFields(proofs[queue[i].PR])
		return fmt.Sprintf("next %d %s %s", queue[i].PR, f["head"], f["build"])
	}
	next, key := -1, "none"
	if len(queue) == 0 {
		key = ""
	}
	for i, r := range queue {
		if strings.HasPrefix(told+" ", fmt.Sprintf("next %d ", r.PR)) {
			// the named PR stays named while it passes, or reads NONE on the named head while its merge or build is in
			// progress; a new passing head or build on it is said again; anything else (FAIL, STALE, a NONE that does
			// not clear by itself such as merge-conflicts, a new head not yet green) ends it (review r3 P2-1, P2-2)
			f := proofFields(proofs[r.PR])
			pending := f["result"] == "inProgress" || f["result"] == "notStarted" || f["result"] == "merge-queued"
			switch {
			case f["state"] == "PASS" && keyOf(i) == told,
				f["state"] == "NONE" && pending && strings.HasPrefix(told, fmt.Sprintf("next %d %s ", r.PR, f["head"])):
				return nil
			case f["state"] == "PASS":
				next = i
			}
			break
		}
	}
	for i, r := range queue {
		if next < 0 && proofFields(proofs[r.PR])["state"] == "PASS" {
			next = i
		}
	}
	if next >= 0 {
		key = keyOf(next)
	}
	if key == told {
		return nil
	}
	var others []string
	for i, r := range queue {
		if i == next {
			continue
		}
		f := proofFields(proofs[r.PR])
		o := fmt.Sprintf("%s PR %d %s", r.ID, r.PR, f["state"])
		if f["state"] != "PASS" {
			o += " (" + f["result"] + ")"
		}
		others = append(others, o)
	}
	if len(others) > 4 {
		others = append(others[:4], fmt.Sprintf("+%d more", len(others)-4))
	}
	text := ""
	switch {
	case next >= 0:
		r, f := queue[next], proofFields(proofs[queue[next].PR])
		why := ""
		if cfg.Merge != "" {
			done, w, err := complete(d, cfg, r, f, key, toldPath)
			if err != nil {
				return err // the target or the proof was not read: decide nothing, try again next run
			}
			if done == "skip" {
				return nil // the proof changed since step 0: the next run decides on the new one
			}
			if done != "" {
				return sayDone(d, toldPath, key, done)
			}
			why = w
		}
		text = fmt.Sprintf("merge queue: next %s PR %d @ %s (build %s PASS): yours to merge%s.", r.ID, r.PR, f["head"], f["build"], why)
		if len(others) > 0 {
			text += " After it: " + strings.Join(others, ", ") + "."
		}
	case key == "none" && strings.HasPrefix(told, "next "):
		text = "merge queue: none ready: " + strings.Join(others, ", ") + "."
	}
	if text != "" {
		if err := d.Post("MERGE", d.Project+":lead", text); err != nil {
			return fmt.Errorf("posting %q: %w", text, err)
		}
	}
	tmp := toldPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(key+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, toldPath)
}

var oneBuild = regexp.MustCompile(`^[0-9]+$`)

// complete runs the merge command on the named PR when it may: done is the line to post when it ran ("skip" when the
// proof changed since step 0); why says, when it did not run, what the lead is told instead. An error is a target or
// proof that could not be read.
func complete(d Deps, cfg config, r board.Row, f map[string]string, key, toldPath string) (done, why string, err error) {
	target, err := d.Target(r.PR)
	if err != nil {
		return "", "", fmt.Errorf("%s PR %d: reading its target: %w", r.ID, r.PR, err)
	}
	switch {
	case !slices.Contains(cfg.MergeInto, target):
		return "", " (it targets " + target + ")", nil
	case !oneBuild.MatchString(f["build"]):
		return "", " (builds " + f["build"] + ": not one build)", nil
	}
	// the proof is read again just before: one that changed since step 0 (minutes ago) is not merged on (review P2-2)
	line, err := d.Proof(r.PR, cfg.Pipeline)
	if err != nil {
		return "", "", fmt.Errorf("%s PR %d: reading its proof again: %w", r.ID, r.PR, err)
	}
	if g := proofFields(line); g["state"] != "PASS" || fmt.Sprintf("next %d %s %s", r.PR, g["head"], g["build"]) != key {
		return "skip", "", nil
	}
	// recorded before it starts, under the lock: no second run beside it, and none after a dispatcher that ended
	// during it (review P2-1)
	if err := writeTold(toldPath, "running "+key); err != nil {
		return "", "", err
	}
	branch := strings.TrimPrefix(target, "refs/heads/")
	cmd := strings.NewReplacer("{pr}", strconv.Itoa(r.PR), "{build}", f["build"], "{target}", branch).Replace(cfg.Merge)
	out, err := d.Merge(cmd)
	last := ""
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			last = l
		}
	}
	if r := []rune(last); len(r) > 160 {
		last = string(r[:160]) + "…"
	}
	what := fmt.Sprintf("%s PR %d @ %s (build %s)", r.ID, r.PR, f["head"], f["build"])
	if errors.Is(err, ErrOutcomeUnknown) { // stopped at its limit or with its dispatcher: it may have completed (review r2 P3-C)
		return unknown(strconv.Itoa(r.PR), f["head"], f["build"], "("+err.Error()+")"), "", nil
	}
	if err != nil {
		return fmt.Sprintf("MERGE merge queue: %s not completed (%v): %s. Tried again on a new head or build, or once mergeq.told is removed.", what, err, last), "", nil
	}
	return fmt.Sprintf("INFO merge queue completed %s into %s: %s", what, target, last), "", nil
}

// sayDone records a merge command that ran before it is posted: a lost post must never run the command twice.
func sayDone(d Deps, toldPath, key, done string) error {
	if err := writeTold(toldPath, key); err != nil {
		return err
	}
	typ, text, _ := strings.Cut(done, " ")
	if err := d.Post(typ, d.Project+":lead", text); err != nil {
		return fmt.Errorf("posting %q: %w", text, err)
	}
	return nil
}

func writeTold(path, s string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(s+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrOutcomeUnknown is a merge command stopped before it said how it ended (its limit, or its dispatcher stopping).
var ErrOutcomeUnknown = errors.New("outcome unknown")

func unknown(pr, head, build, how string) string {
	return fmt.Sprintf("MERGE merge queue: the merge command for PR %s @ %s (build %s) stopped %s; outcome unknown: check the PR. Tried again on a new head or build, or once mergeq.told is removed.", pr, head, build, how)
}

// proofFields reads ci-proof's line: its state word and its key=value fields up to suites (which may hold spaces).
func proofFields(line string) map[string]string {
	f := map[string]string{"result": "unknown"}
	words := strings.Fields(line)
	if len(words) > 0 {
		f["state"] = words[0]
	}
	for _, w := range words[1:] {
		k, v, ok := strings.Cut(w, "=")
		if !ok || k == "suites" {
			break
		}
		f[k] = v
	}
	return f
}

func role(cfg config, id string) string { return cfg.Prefix + strings.ToLower(id) }

func prompt(cfg config, id string) string { return strings.ReplaceAll(cfg.Prompt, "{id}", id) }

// recipients are the bus names after the arrow, the direct one first, cc ones after.
func recipients(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '(' || r == ')' }) {
		if f != "cc" && f != "cc:" {
			out = append(out, strings.TrimPrefix(f, "cc:"))
		}
	}
	return out
}

// newLines gives the whole lines of a log after the cursor's byte offset, and the offset after the last of them. With
// no cursor (the first run on a project) it starts at the log's end: lines from before the board was seeded never
// move its rows. A cursor that does not read is an error. A log shorter than the cursor (rotated) is read from its
// start.
func newLines(path, cursorPath string) ([]string, int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	from := st.Size()
	if b, err := os.ReadFile(cursorPath); err == nil {
		if from, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err != nil || from < 0 {
			return nil, 0, fmt.Errorf("%s does not read as a byte offset (%q): fix or remove it (removed = start at the bus end)", cursorPath, b)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	if st.Size() < from {
		from = 0
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}
	end := strings.LastIndexByte(string(data), '\n')
	if end < 0 {
		return nil, from, nil
	}
	return strings.Split(string(data[:end]), "\n"), from + int64(end) + 1, nil
}

// writeCursor writes the cursor whole (a new file renamed over the old).
func writeCursor(path string, upto int64) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(upto, 10)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// prNumbers are the distinct PR numbers a line names, in order.
func prNumbers(text string) []string {
	var out []string
	for _, m := range prRe.FindAllStringSubmatch(text, -1) {
		if n := strings.TrimLeft(m[1], "0"); !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
