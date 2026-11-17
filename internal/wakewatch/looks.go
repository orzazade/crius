package wakewatch

import "strings"

// lookMisses: a look (a read of a file, a tab, a thread, a list, a call to t3, cmux, ssh or a tool) must fail on this
// many runs in a row before Crius hears of it. One failed look is most often a blip that reads fine a minute later
// (11-17: a sign-in read during a CLI update, 2b6733b; an ssh look across a router restart, 2e2e25c; a t3 thread show
// that gave no answer in 10 s; 11-05 to 11-14: 19 "owner-click watch failed: cmux tree … timed out"). A look that keeps
// failing is still said, on the second run: failed to look is never read as found nothing. One good look starts the
// count again.
//
// This is the one rule for every failed-look alert: Run holds each alert of a kind in lookKinds through misses,
// alertOnce holds a clicks or prompts run whose every error is a failed look (failedLook), and the sign-in watch and the
// provider-error sweep count their own reads with misses. The only failed looks said at once are in lookNow: the run
// acts on a fallback for what it could not read, and its other alerts need the reason beside them. The tests in
// looks_test.go walk the source, so a new alert site cannot skip the rule.
const lookMisses = 2

// misses carries, per look, how many runs in a row it failed, from one run to the next.
type misses struct {
	prev, next map[string]int
}

// newMisses starts a run's count from the last run's (nil: none failed).
func newMisses(prev map[string]int) *misses { return &misses{prev: prev, next: map[string]int{}} }

// fail records that key's look failed on this run. It gives how many runs in a row it has failed and whether Crius
// hears of it now. A key failed twice in one run counts once.
func (m *misses) fail(key string) (int, bool) {
	n, ok := m.next[key]
	if !ok {
		n = m.prev[key] + 1
		m.next[key] = n
	}
	return n, n >= lookMisses
}

// counts is what to keep for the next run: only the looks that failed on this one, so a good look starts again.
func (m *misses) counts() map[string]int { return m.next }

// lookKinds are the alert kinds (an alert key up to its first ":") that say a look failed. Run holds such an alert until
// it was raised on lookMisses runs in a row; the rest of Run treats it as raised meanwhile (it keeps what a failed look
// keeps, such as a dead role's typed line).
var lookKinds = map[string]bool{
	"hooklog-failed":     true,
	"load-failed":        true,
	"looks-failed":       true,
	"paid-failed":        true,
	"prompt-unread":      true,
	"prompts-failed":     true,
	"rearm-failed":       true,
	"resources-failed":   true,
	"restore-unread":     true,
	"rig-failed":         true,
	"role-unread":        true,
	"roles-failed":       true,
	"screen-asks-failed": true,
	"screen-failed":      true,
	screensFailed:        true,
	"t3-failed":          true,
	"tool":               true, // a tool that did not answer: asked once a run, said on the second miss in a row
	"waiters-failed":     true,
}

// lookNow are failed looks said on the run they happen, with why. Each run that cannot read the thing goes on with a
// fallback whose own alerts are sent at once (a wrong-folder wake, a parked role's line, an unwatched team's DONE), so
// the reason must go with them, not a run later (review of the look rule, P2-1); or another look cannot change it.
var lookNow = map[string]string{
	"accounts-unread":    "folders on two Max rows of one persona read as two accounts: wrong-folder wakes follow",
	"mode-failed":        "the screen decides instead of crius-hook's state",
	"paid-unknown":       "a claude with neither CLAUDE_CONFIG_DIR nor HOME: a second look reads the same environment",
	"parked-failed":      "no role is parked: parked roles' lines wake Crius",
	"projects-failed":    "every project is watched: unwatched teams' lines wake Crius",
	"prompt-untold":      "crius-hook did not record the prompt's session or dialog: a second look reads the same record",
	"sessions-failed":    "the screen decides alone",
	"states-failed":      "the screen decides alone, and the T3 check takes each role file's thread",
	"wait-cursor-failed": "lines the waiter delivered may wake Crius again",
}

// ownMisses are alert kinds whose check counts its failed reads itself with misses, before the alert is raised: the
// sign-in watch raises once per key and day, so Run must not hold what it raises.
var ownMisses = map[string]bool{
	"signin":      true, // an UNREAD status waits on misses ("status:<account>")
	"week":        true, // a week UNKNOWN waits while a read failed for the first time
	"week-unread": true, // a spool not read waits on misses ("spool:<host>")
}

// lookErr marks an error as a failed look (a read that may work on the next run).
type lookErr struct{ error }

func (e lookErr) Unwrap() error { return e.error }

// failedLook marks err as a failed look; nil stays nil.
func failedLook(err error) error {
	if err == nil {
		return nil
	}
	return lookErr{err}
}

// onlyLooks says whether err holds at least one error and every one is a failed look (failedLook, or errors.Join of
// such). Anything else, a failed write or post among them, is not held: it is not tried again on the next run.
func onlyLooks(err error) bool {
	if _, ok := err.(lookErr); ok {
		return true
	}
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return false
	}
	n := 0
	for _, e := range j.Unwrap() {
		if e == nil {
			continue
		}
		if !onlyLooks(e) {
			return false
		}
		n++
	}
	return n > 0
}

// alertKind is an alert key up to its first ":".
func alertKind(key string) string {
	k, _, _ := strings.Cut(key, ":")
	return k
}

// holdLooks counts every failed-look alert of this run in st.LookMiss and gives the keys to hold back: those raised on
// fewer than lookMisses runs in a row.
func holdLooks(st *state, alerts [][2]string) map[string]bool {
	m := newMisses(st.LookMiss)
	held := map[string]bool{}
	for _, a := range alerts {
		if !lookKinds[alertKind(a[0])] {
			continue
		}
		if _, due := m.fail(a[0]); !due {
			held[a[0]] = true
		}
	}
	st.LookMiss = m.counts()
	return held
}
