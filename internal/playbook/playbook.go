// Package playbook watches what the teams did (bus logs, team docs, session
// transcripts), picks the findings with cheap rules, has a model turn them into
// "when / do / because" entries, and keeps them in one private skill folder.
// No session has to write anything for a finding to be kept.
package playbook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Candidate is one piece of the record a rule picked as a possible finding.
type Candidate struct {
	ID     string    `json:"id"`     // stable across runs: "bus:<log>:<offset>", "tx:<uuid>"
	Source string    `json:"source"` // bus, transcript, owner
	Team   string    `json:"team"`
	Time   time.Time `json:"time"`
	Ref    string    `json:"ref"` // where to read it: "bus shop.log 2026-11-20T10:12", "<transcript>#<uuid>"
	Text   string    `json:"text"`
	Why    string    `json:"why"`             // the rule that picked it
	Topic  string    `json:"topic,omitempty"` // set by Pick
	Tries  int       `json:"tries,omitempty"` // runs that got no answer for it
}

// Verdict is the cheap model's answer for one candidate.
type Verdict struct {
	ID    string `json:"id"`
	Keep  bool   `json:"keep"`
	Topic string `json:"topic"`
}

// Op is the writing model's answer for one kept candidate: a new entry, or more
// evidence for an existing one (SameAs). Replace rewrites the existing entry with
// Entry (a conflicting finding) and keeps both evidences.
type Op struct {
	Candidate string `json:"candidate"`
	SameAs    string `json:"same_as,omitempty"`
	Replace   bool   `json:"replace,omitempty"`
	Retire    bool   `json:"retire,omitempty"` // a later record shows SameAs no longer holds
	Note      string `json:"note,omitempty"`
	Outcome   string `json:"outcome,omitempty"` // hit, applied or same (a retire's evidence is stored as "retired")
	Entry     Entry  `json:"entry"`
}

// Model is the two model steps: Pick is cheap (haiku), Write is the write-up (sonnet).
type Model interface {
	Pick(context.Context, []Candidate) ([]Verdict, error)
	Write(context.Context, []Candidate, []Entry) ([]Op, error)
}

// Config says what to read and where to keep the result.
type Config struct {
	BusDirs  []string // folders of *.log bus files
	TxRoots  []string // transcript roots (~/.claude*/projects)
	DocFiles []string // old lesson files to seed from, one candidate per paragraph
	BookDir  string   // the skill folder
	StateDir string   // cursor
	Since    time.Time
	DryRun   bool // pick and print; no model call, no write
	// ProjectOf names the projects.toml project of a session folder ("" for
	// none), so transcript findings file under the same name as the bus.
	ProjectOf func(cwd string) string
	Hosts     []Host   // other machines: their transcripts are pulled first, the book pushed after
	CacheDir  string   // where pulled transcripts are kept, one folder per host
	RuleFiles []string // the current rules; entries they show are gone are retired
}

// Calls counts model calls in one run.
type Calls struct{ Pick, Write, Check int }

// Result is what one run did.
type Result struct {
	Candidates []Candidate
	Kept       int
	New        int
	Evidence   int
	Duplicates int      // answers that named a source line the entry already had
	Retired    []string // entries a later record showed no longer hold
	Unwritten  int      // kept candidates the writing model left out (no lasting lesson)
	Dropped    []string // model answers that could not be applied, with the reason
	Failed     []string // model batches that failed; their candidates wait in the cursor for the next run
	Lost       int      // candidates given up after maxTries; kept in dropped.jsonl
	Hosts      []HostResult
	Calls      Calls
	Read       ReadStats
	Review     Review
}

// Healthy says the run read and answered everything: no read error, no failed
// batch, no answer that could not be applied. A night that is not healthy is
// reported as an ALERT, never as a quiet night.
func (r *Result) Healthy() bool {
	return len(r.Read.Errors) == 0 && len(r.Failed) == 0 && len(r.Dropped) == 0 && r.Lost == 0 && r.Read.ShownBad == 0 && hostsOK(r.Hosts)
}

const (
	pickBatch  = 40
	writeBatch = 15
)

// Run reads everything new since the cursor (and not before cfg.Since), picks
// candidates, asks the model, and saves the book, its topic files and the cursor.
// The cursor moves only after the book is saved, so a failed run is read again.
// One run at a time: a second one waits for the lock in cfg.StateDir.
func Run(ctx context.Context, cfg Config, m Model) (*Result, error) {
	unlock, err := lock(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	now := time.Now()
	today := now.Format("2006-01-02")
	cur, err := loadCursor(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	// Read before any model call: a problem here must not cost the night's calls.
	shown, shownBad, shownEnd, err := LoadShown(cfg.StateDir, cur.ShownChecked)
	if err != nil {
		return nil, err
	}
	// Other hosts first: their new transcript lines join the local ones. A host
	// that cannot be read is said in the report and makes the run unhealthy;
	// the rest of the night goes on.
	var hosts []HostResult
	for _, h := range cfg.Hosts {
		dir, n, err := pull(ctx, h, cfg.CacheDir)
		r := HostResult{Name: h.Name, Files: n}
		if err != nil {
			r.Err = err.Error()
		}
		if _, serr := os.Stat(dir); serr == nil {
			cfg.TxRoots = append(cfg.TxRoots, dir) // read what came, even from a pull cut short
		}
		hosts = append(hosts, r)
	}
	cands, stats, err := Gather(cfg, cur, now)
	if err != nil {
		return nil, err
	}
	stats.ShownBad = shownBad
	// Candidates a past run could not get an answer for come first.
	cands = append(cur.Pending, cands...)
	cur.Pending = nil
	res := &Result{Candidates: cands, Read: stats, Hosts: hosts}
	if cfg.DryRun {
		return res, nil
	}
	book, err := LoadBook(cfg.BookDir)
	if err != nil {
		return nil, err
	}
	byID := map[string]Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	var lost []Candidate
	lostID := map[string]bool{}
	// retry parks a batch for the next run; after maxTries it goes to dropped.jsonl.
	retry := func(batch []Candidate, why error) {
		res.Failed = append(res.Failed, why.Error())
		for _, c := range batch {
			if c.Tries++; c.Tries >= maxTries {
				lost = append(lost, c)
				continue
			}
			cur.Pending = append(cur.Pending, c)
		}
	}
	var kept []Candidate
	for i := 0; i < len(cands); i += pickBatch {
		batch := cands[i:min(i+pickBatch, len(cands))]
		vs, err := m.Pick(ctx, batch)
		res.Calls.Pick++
		if stop(ctx, err) {
			return res, fmt.Errorf("pick: %w", err)
		}
		if err != nil {
			retry(batch, fmt.Errorf("pick: %w", err))
			continue
		}
		answered := map[string]bool{}
		for _, v := range vs {
			answered[v.ID] = true
			if c, ok := byID[v.ID]; ok && v.Keep {
				c.Topic = v.Topic
				kept = append(kept, c)
			}
		}
		var missed []Candidate
		for _, c := range batch {
			if !answered[c.ID] {
				missed = append(missed, c)
			}
		}
		if len(missed) > 0 {
			retry(missed, fmt.Errorf("pick skipped %d of %d", len(missed), len(batch)))
		}
	}
	res.Kept = len(kept)
	for i := 0; i < len(kept); i += writeBatch {
		batch := kept[i:min(i+writeBatch, len(kept))]
		ops, err := m.Write(ctx, batch, book.Live())
		res.Calls.Write++
		if stop(ctx, err) {
			return res, fmt.Errorf("write: %w", err)
		}
		if err != nil {
			retry(batch, fmt.Errorf("write: %w", err))
			continue
		}
		inBatch := map[string]Candidate{} // the picked copies, with their topic
		for _, c := range batch {
			inBatch[c.ID] = c
		}
		// New entries first, so an answer that points at another candidate of
		// the batch ("same_as": "<candidate id>") finds the entry made from it.
		sort.SliceStable(ops, func(i, j int) bool { return ops[i].SameAs == "" && ops[j].SameAs != "" })
		made := map[string]string{} // candidate id -> entry id made from it
		answered := map[string]bool{}
		for _, op := range ops {
			c, ok := inBatch[op.Candidate]
			if !ok {
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: not a candidate of this batch", op.Candidate))
				continue
			}
			answered[op.Candidate] = true
			if id, ok := made[op.SameAs]; ok {
				op.SameAs = id
			}
			if op.Retire && len(res.Retired) >= maxRetires {
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: retire cap reached (%d per run), %s stays live", op.Candidate, maxRetires, op.SameAs))
				if !lostID[c.ID] {
					lostID[c.ID] = true
					lost = append(lost, c)
				}
				continue
			}
			switch got, err := book.Apply(op, c, today); {
			case err != nil:
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: %v", op.Candidate, err))
				if !lostID[c.ID] { // kept in dropped.jsonl, never only in the log; once per candidate
					lostID[c.ID] = true
					lost = append(lost, c)
				}
			case got == AddedEntry:
				res.New++
				made[op.Candidate] = op.Entry.ID
			case got == AddedEvidence:
				res.Evidence++
			case got == Retired:
				res.Retired = append(res.Retired, op.SameAs)
			default:
				res.Duplicates++
			}
		}
		res.Unwritten += len(batch) - len(answered)
	}
	var overCap []string
	if ck, ok := m.(Checker); ok && len(cfg.RuleFiles) > 0 {
		var err error
		if overCap, err = checkStale(ctx, ck, cfg, book, now, res); err != nil {
			return res, err
		}
	}
	res.Review = ReviewBook(book, shown, now)
	res.Review.NeedsCheck = append(res.Review.NeedsCheck, overCap...)
	if err := book.Save(cfg.BookDir); err != nil {
		return res, err
	}
	if err := saveLost(cfg.StateDir, lost); err != nil {
		return res, err
	}
	res.Lost = len(lost)
	cur.LastRun = now
	cur.ShownChecked = shownEnd
	if err := cur.save(cfg.StateDir, now); err != nil {
		return res, err
	}
	// The book goes to each host after it is saved here. A host that could not
	// be read is not written either: it is down, and a push would wait out
	// another timeout.
	for i, h := range cfg.Hosts {
		if h.Book == "" || res.Hosts[i].Err != "" {
			continue
		}
		if err := push(ctx, h, cfg.BookDir); err != nil {
			res.Hosts[i].Err = strings.TrimPrefix(strings.TrimSpace(res.Hosts[i].Err+"; push: "+err.Error()), "; ")
			continue
		}
		res.Hosts[i].Pushed = true
	}
	return res, nil
}

func hostsOK(hs []HostResult) bool {
	for _, h := range hs {
		if h.Err != "" {
			return false
		}
	}
	return true
}

// saveLost appends candidates that got no answer after maxTries to
// dropped.jsonl, so they can be read or fed back by hand.
func saveLost(dir string, cs []Candidate) error {
	if len(cs) == 0 {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, "dropped.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, c := range cs {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return nil
}

// lock holds dir/run.lock (an OS lock, so a crashed run leaves none behind).
func lock(dir string) (func(), error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "run.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); f.Close() }, nil
}

// maxTries is how many runs a candidate may go unanswered before it is dropped.
const maxTries = 3

// stop says a model error ends the run with nothing saved: an API-key login
// (the call may be billed), no picked Max account, or a cancelled run. Other
// errors park the batch.
func stop(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, ErrAPIKey) || errors.Is(err, ErrNoAccount) || ctx.Err() != nil)
}

// maxRetires caps the entries one run may retire.
const maxRetires = 5

// Stale is the checking model's answer for one entry.
type Stale struct {
	ID    string `json:"id"`
	Stale bool   `json:"stale"`
	Why   string `json:"why"`
}

// Checker checks live entries against the current rules (optional for a Model).
type Checker interface {
	Check(ctx context.Context, rules string, es []Entry) ([]Stale, error)
}

const (
	checkBatch   = 60
	checkBatches = 3        // up to 180 entries a night
	checkEvery   = 7        // days before an entry is checked again
	ruleCap      = 40 << 10 // bytes kept of each rule file
	decisionDays = 7        // days of DECISION and CORRECTED lines shown
	decisionCap  = 150      // newest lines kept
)

// checkStale shows the entries not checked in a week to the checking model with
// the current rules, and retires those the rules show are gone. A night retires
// at most a twentieth of the live book (5 at least); the rest are returned for
// "needs a check". Seen 11-14: the write step never linked "post guard removed"
// to the entry about that guard.
func checkStale(ctx context.Context, ck Checker, cfg Config, book *Book, now time.Time, res *Result) ([]string, error) {
	today := now.Format("2006-01-02")
	due := now.AddDate(0, 0, -checkEvery).Format("2006-01-02")
	var todo []*Entry
	for i := range book.Entries {
		if e := &book.Entries[i]; !e.Archived && e.Checked <= due {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return nil, nil
	}
	sort.SliceStable(todo, func(i, j int) bool { return todo[i].Checked < todo[j].Checked })
	todo = todo[:min(len(todo), checkBatch*checkBatches)]
	rules, err := rulesText(cfg, now)
	if err != nil {
		res.Failed = append(res.Failed, "check: "+err.Error())
		return nil, nil
	}
	limit := max(5, len(book.Live())/20)
	retired := 0
	var overCap []string
	for i := 0; i < len(todo); i += checkBatch {
		batch := todo[i:min(i+checkBatch, len(todo))]
		es := make([]Entry, len(batch))
		byID := map[string]*Entry{}
		for j, e := range batch {
			es[j], byID[e.ID] = *e, e
		}
		ss, err := ck.Check(ctx, rules, es)
		res.Calls.Check++
		if stop(ctx, err) {
			return nil, fmt.Errorf("check: %w", err)
		}
		if err != nil {
			res.Failed = append(res.Failed, "check: "+err.Error())
			continue
		}
		for _, e := range batch {
			e.Checked = today // an entry the model left out counts as not stale
		}
		for _, s := range ss {
			e := byID[s.ID]
			if e == nil || !s.Stale {
				continue
			}
			// An owner preference is the owner's word: only a rule file (also the
			// owner's word) ends it, never a decision line of Crius or a lead.
			if e.Kind == "owner-preference" && !namesRuleFile(s.Why, cfg.RuleFiles) {
				overCap = append(overCap, e.ID)
				continue
			}
			if retired >= limit {
				e.Checked = "" // tried again the next night, when the cap is new
				overCap = append(overCap, e.ID)
				continue
			}
			retired++
			e.Archived, e.Retired = true, today
			e.Evidence = append(e.Evidence, Evidence{Team: "crius", Date: today, Ref: "stale check " + today,
				Note: clean(s.Why), Outcome: "retired", Added: today})
			res.Retired = append(res.Retired, e.ID)
		}
	}
	return overCap, nil
}

// namesRuleFile reports whether why names one of the rule files.
func namesRuleFile(why string, files []string) bool {
	for _, f := range files {
		if strings.Contains(why, filepath.Base(f)) {
			return true
		}
	}
	return false
}

// rulesText is the rule files (each cut at ruleCap) and the newest DECISION and
// CORRECTED bus lines of the last decisionDays, oldest first.
func rulesText(cfg Config, now time.Time) (string, error) {
	var s strings.Builder
	for _, f := range cfg.RuleFiles {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		if len(data) > ruleCap {
			data = data[:ruleCap]
		}
		fmt.Fprintf(&s, "== %s\n%s\n", filepath.Base(f), data)
	}
	from := now.AddDate(0, 0, -decisionDays).Format("2006-01-02T15:04")
	var lines []string
	for _, dir := range cfg.BusDirs {
		logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
		for _, l := range logs {
			data, err := os.ReadFile(l)
			if err != nil {
				return "", err
			}
			for _, ln := range strings.Split(string(data), "\n") {
				if len(ln) < 17 || ln[:16] < from {
					continue
				}
				if head := ln[16:min(len(ln), 28)]; strings.HasPrefix(head, " DECISION ") || strings.HasPrefix(head, " CORRECTED ") {
					lines = append(lines, clean(ln))
				}
			}
		}
	}
	sort.Strings(lines)
	if len(lines) > decisionCap {
		// The owner's words (a queue answer or a line naming the owner) are
		// kept first; the newest other lines fill the rest.
		var owner, other []string
		for _, l := range lines {
			if strings.Contains(l, "[queue ") || strings.Contains(strings.ToLower(l), "owner") {
				owner = append(owner, l)
			} else {
				other = append(other, l)
			}
		}
		owner = owner[max(0, len(owner)-decisionCap):]
		other = other[max(0, len(other)-(decisionCap-len(owner))):]
		lines = append(owner, other...)
		sort.Strings(lines)
	}
	fmt.Fprintf(&s, "== latest decisions and corrections (oldest first)\n%s\n", strings.Join(lines, "\n"))
	return s.String(), nil
}
