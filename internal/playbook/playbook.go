// Package playbook watches what the teams did (bus logs, team docs, session
// transcripts), picks the findings with cheap rules, has a model turn them into
// "when / do / because" entries, and keeps them in one private skill folder.
// No session has to write anything for a finding to be kept.
package playbook

import (
	"context"
	"errors"
	"fmt"
	"time"
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
}

// Calls counts model calls in one run.
type Calls struct{ Pick, Write int }

// Result is what one run did.
type Result struct {
	Candidates []Candidate
	Kept       int
	New        int
	Evidence   int
	Dropped    []string // model answers that could not be applied, with the reason
	Failed     []string // model batches that failed; their candidates wait in the cursor for the next run
	Calls      Calls
	Read       ReadStats
	Review     Review
}

const (
	pickBatch  = 40
	writeBatch = 15
)

// Run reads everything new since the cursor (and not before cfg.Since), picks
// candidates, asks the model, and saves the book, its topic files and the cursor.
// The cursor moves only after the book is saved, so a failed run is read again.
func Run(ctx context.Context, cfg Config, m Model) (*Result, error) {
	cur, err := loadCursor(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	cands, stats, err := Gather(cfg, cur)
	if err != nil {
		return nil, err
	}
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
	// retry parks a batch for the next run; after maxTries it is dropped, loudly.
	retry := func(batch []Candidate, why error) {
		res.Failed = append(res.Failed, why.Error())
		for _, c := range batch {
			if c.Tries++; c.Tries >= maxTries {
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: no answer after %d tries: %v", c.ID, c.Tries, why))
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
		for _, op := range ops {
			c, ok := byID[op.Candidate]
			if !ok {
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: not a candidate of this batch", op.Candidate))
				continue
			}
			switch added, err := book.Apply(op, c); {
			case err != nil:
				res.Dropped = append(res.Dropped, fmt.Sprintf("%s: %v", op.Candidate, err))
			case added:
				res.New++
			default:
				res.Evidence++
			}
		}
	}
	shown, err := LoadShown(cfg.StateDir)
	if err != nil {
		return res, err
	}
	res.Review = ReviewBook(book, shown, time.Now())
	if err := book.Save(cfg.BookDir); err != nil {
		return res, err
	}
	return res, cur.save(cfg.StateDir)
}

// maxTries is how many runs a candidate may go unanswered before it is dropped.
const maxTries = 3

// stop says a model error ends the run with nothing saved: an API-key login
// (the call may be billed) or a cancelled run. Other errors park the batch.
func stop(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, ErrAPIKey) || ctx.Err() != nil)
}
