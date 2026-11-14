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
	Note      string `json:"note,omitempty"`
	Outcome   string `json:"outcome,omitempty"` // hit, applied or same
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
}

// Calls counts model calls in one run.
type Calls struct{ Pick, Write int }

// Result is what one run did.
type Result struct {
	Candidates []Candidate
	Kept       int
	New        int
	Evidence   int
	Duplicates int      // answers that named a source line the entry already had
	Unwritten  int      // kept candidates the writing model left out (no lasting lesson)
	Dropped    []string // model answers that could not be applied, with the reason
	Failed     []string // model batches that failed; their candidates wait in the cursor for the next run
	Lost       int      // candidates given up after maxTries; kept in dropped.jsonl
	Calls      Calls
	Read       ReadStats
	Review     Review
}

// Healthy says the run read and answered everything: no read error, no failed
// batch, no answer that could not be applied. A night that is not healthy is
// reported as an ALERT, never as a quiet night.
func (r *Result) Healthy() bool {
	return len(r.Read.Errors) == 0 && len(r.Failed) == 0 && len(r.Dropped) == 0 && r.Lost == 0 && r.Read.ShownBad == 0
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
	cands, stats, err := Gather(cfg, cur, now)
	if err != nil {
		return nil, err
	}
	stats.ShownBad = shownBad
	// Candidates a past run could not get an answer for come first.
	cands = append(cur.Pending, cands...)
	cur.Pending = nil
	res := &Result{Candidates: cands, Read: stats}
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
			default:
				res.Duplicates++
			}
		}
		res.Unwritten += len(batch) - len(answered)
	}
	res.Review = ReviewBook(book, shown, now)
	if err := book.Save(cfg.BookDir); err != nil {
		return res, err
	}
	if err := saveLost(cfg.StateDir, lost); err != nil {
		return res, err
	}
	res.Lost = len(lost)
	cur.LastRun = now
	cur.ShownChecked = shownEnd
	return res, cur.save(cfg.StateDir, now)
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
// (the call may be billed) or a cancelled run. Other errors park the batch.
func stop(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, ErrAPIKey) || ctx.Err() != nil)
}
