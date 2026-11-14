package playbook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Topics are the files of the playbook. A model answer with another topic is
// filed under "other" and shows in the index, so it is seen and can be moved.
var Topics = []string{"testing", "review-and-qa", "team-and-bus", "harness-and-accounts", "ui-and-design",
	"deploy-and-k8s", "databases", "stack-quirks", "owner-preferences", "other"}

// Kinds of entry.
var Kinds = map[string]bool{"tactic": true, "trap": true, "tool-fact": true, "team-fact": true, "owner-preference": true}

// Evidence is one sighting of a finding.
type Evidence struct {
	Team    string `json:"team"`
	Date    string `json:"date"`
	Ref     string `json:"ref"`
	Note    string `json:"note,omitempty"`
	Outcome string `json:"outcome,omitempty"` // hit, applied or same (see Outcomes); "retired" on the record that retired it
	Added   string `json:"added,omitempty"`   // the day the book got it
}

// Entry is one finding: when <situation>, do <tactic>, because <evidence>.
type Entry struct {
	ID       string     `json:"id"`
	Topic    string     `json:"topic"`
	Kind     string     `json:"kind"`
	When     string     `json:"when"`
	Do       string     `json:"do"`
	Because  string     `json:"because"`
	Keys     []string   `json:"keys,omitempty"` // project, stack and tool words the start-up hook matches on
	Evidence []Evidence `json:"evidence"`
	Created  string     `json:"created,omitempty"` // date of its first evidence
	Added    string     `json:"added,omitempty"`   // the day the book got it
	Archived bool       `json:"archived,omitempty"`
	Retired  string     `json:"retired,omitempty"` // date of the record that showed it no longer holds
	Checked  string     `json:"checked,omitempty"` // last day the stale check looked at it
}

// Book is every entry, kept in book.json; the markdown files are made from it.
type Book struct {
	Entries []Entry `json:"entries"`
}

const bookFile = "book.json"

// LoadBook reads dir/book.json; a folder with no book yet is an empty book.
func LoadBook(dir string) (*Book, error) {
	b := &Book{}
	data, err := os.ReadFile(filepath.Join(dir, bookFile))
	if errors.Is(err, fs.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, b); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, bookFile), err)
	}
	return b, nil
}

// Find returns the entry with this id, or nil.
func (b *Book) Find(id string) *Entry {
	for i := range b.Entries {
		if b.Entries[i].ID == id {
			return &b.Entries[i]
		}
	}
	return nil
}

// Live is every entry that is not archived.
func (b *Book) Live() []Entry {
	var out []Entry
	for _, e := range b.Entries {
		if !e.Archived {
			out = append(out, e)
		}
	}
	return out
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// Applied says what one model answer did to the book.
type Applied int

const (
	AddedEntry    Applied = iota // a new entry
	AddedEvidence                // a new sighting of an existing entry
	Duplicate                    // the same source line again: nothing changed
	Retired                      // a later record showed the entry no longer holds
)

// Outcomes of a sighting: the problem happened again, the advice was used, or
// the finding was only stated again.
var Outcomes = map[string]bool{"hit": true, "applied": true, "same": true}

// Apply adds one model answer on day today (the book's own date, which the
// archive counts from; the evidence date is the record's). A new entry whose id
// is taken is the same finding again, so it adds evidence too.
func (b *Book) Apply(op Op, c Candidate, today string) (Applied, error) {
	outcome := op.Outcome
	if !Outcomes[outcome] {
		outcome = "same"
	}
	ev := Evidence{Team: c.Team, Date: c.Time.Local().Format("2006-01-02"), Ref: c.Ref, Note: clean(op.Note), Outcome: outcome, Added: today}
	if op.Retire && op.SameAs == "" {
		return 0, errors.New("retire needs same_as: the entry it retires")
	}
	target := op.SameAs
	if target == "" && b.Find(op.Entry.ID) != nil {
		target = op.Entry.ID
	}
	if target != "" {
		e := b.Find(target)
		if e == nil {
			return 0, fmt.Errorf("same_as %q is not an entry", target)
		}
		for _, old := range e.Evidence {
			if old.Ref == ev.Ref {
				return Duplicate, nil // the same line read twice is not a second sighting
			}
		}
		if op.Retire {
			// Limits, so one model answer cannot wipe a good entry: never an
			// owner preference (only the owner changes those), never on a record
			// older than the entry's last sighting.
			if e.Kind == "owner-preference" {
				return 0, fmt.Errorf("%s: an owner preference is not retired by a record; needs a check", e.ID)
			}
			if last := lastSeen(e); ev.Date < last {
				return 0, fmt.Errorf("%s: retire is older than the entry's last sighting (%s < %s)", e.ID, ev.Date, last)
			}
			ev.Outcome = "retired"
			e.Archived, e.Retired = true, ev.Date
			e.Evidence = append(e.Evidence, ev)
			return Retired, nil
		}
		if op.Replace {
			fix := tidy(op.Entry)
			fix.ID = e.ID // the corrected entry keeps its id, whatever the model wrote there
			if err := check(fix); err != nil {
				return 0, err
			}
			e.When, e.Do, e.Because, e.Kind = fix.When, fix.Do, fix.Because, fix.Kind
			if len(fix.Keys) > 0 {
				e.Keys = fix.Keys
			}
		}
		if e.Retired == "" || ev.Date >= e.Retired {
			if e.Archived {
				e.Checked = "" // back in the book: the stale check looks again the next night
			}
			e.Archived, e.Retired = false, "" // seen to hold again (an older sighting does not count)
		}
		e.Evidence = append(e.Evidence, ev)
		return AddedEvidence, nil
	}
	e := tidy(op.Entry)
	if err := check(e); err != nil {
		return 0, err
	}
	if !contains(Topics, e.Topic) {
		e.Topic = c.Topic // the pick model's topic
	}
	if !contains(Topics, e.Topic) {
		e.Topic = "other"
	}
	ev.Outcome = "same"
	e.Evidence = []Evidence{ev}
	e.Created, e.Added = ev.Date, today
	b.Entries = append(b.Entries, e)
	return AddedEntry, nil
}

// lastSeen is the date of the entry's newest evidence.
func lastSeen(e *Entry) string {
	last := ""
	for _, ev := range e.Evidence {
		if ev.Date > last {
			last = ev.Date
		}
	}
	return last
}

// maxField caps each text field of an entry. Entry text is printed into every
// matching session start and comes from records that may quote outside text,
// so it is kept to one short line with no tags.
const maxField = 300

var keyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]+$`)

// clean makes s one line of at most maxField bytes with no < or >.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || (unicode.IsControl(r) && !unicode.IsSpace(r)) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxField {
		return s
	}
	cut := maxField - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// tidy cleans an entry's text and keeps only keys that can be words of a folder.
func tidy(e Entry) Entry {
	e.When, e.Do, e.Because = clean(e.When), clean(e.Do), clean(e.Because)
	var keys []string
	for _, k := range e.Keys {
		if k = strings.ToLower(strings.TrimSpace(k)); keyRe.MatchString(k) {
			keys = append(keys, k)
		}
	}
	e.Keys = keys
	return e
}

func check(e Entry) error {
	switch {
	case !idRe.MatchString(e.ID):
		return fmt.Errorf("bad id %q", e.ID)
	case !Kinds[e.Kind]:
		return fmt.Errorf("%s: bad kind %q", e.ID, e.Kind)
	case strings.TrimSpace(e.When) == "" || strings.TrimSpace(e.Do) == "" || strings.TrimSpace(e.Because) == "":
		return fmt.Errorf("%s: when, do and because are all needed", e.ID)
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Save writes book.json, one markdown file per topic and the SKILL.md index.
// Files are written to a temp name and renamed, so a reader never sees half.
func (b *Book) Save(dir string) error {
	for i := range b.Entries { // entries saved before text was cleaned are cleaned here
		b.Entries[i] = tidy(b.Entries[i])
		for j := range b.Entries[i].Evidence {
			ev := &b.Entries[i].Evidence[j]
			ev.Note = clean(ev.Note) // the model wrote it; Ref and Team come from our own code
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, bookFile), append(data, '\n')); err != nil {
		return err
	}
	byTopic := map[string][]Entry{}
	for _, e := range b.Live() {
		byTopic[e.Topic] = append(byTopic[e.Topic], e)
	}
	for _, t := range Topics {
		es := byTopic[t]
		path := filepath.Join(dir, t+".md")
		if len(es) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeAtomic(path, []byte(renderTopic(t, es))); err != nil {
			return err
		}
	}
	return writeAtomic(filepath.Join(dir, "SKILL.md"), []byte(renderIndex(byTopic)))
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}

func renderIndex(byTopic map[string][]Entry) string {
	var s strings.Builder
	s.WriteString(`---
name: playbook
description: What the teams learned, kept by the nightly playbook watcher from bus logs, team docs and session transcripts. Read the topic file before a task in that area (tests, review and QA, team and bus, harness and accounts, UI, deploy and k8s, databases, stack quirks, owner preferences).
---

# Playbook

Made by ` + "`playbook run`" + ` (crius-live cmd/playbook) from book.json. Do not edit the markdown by hand: it is rewritten
each night. To add a finding now, post ` + "`LEARNED <topic>: <finding>`" + ` on your bus.

`)
	for _, t := range Topics {
		es := byTopic[t]
		if len(es) == 0 {
			continue
		}
		fmt.Fprintf(&s, "## [%s](%s.md) (%d)\n\n", t, t, len(es))
		for _, e := range es {
			fmt.Fprintf(&s, "- `%s` when %s\n", e.ID, e.When)
		}
		s.WriteString("\n")
	}
	return s.String()
}

func renderTopic(topic string, es []Entry) string {
	sort.SliceStable(es, func(i, j int) bool { return len(es[i].Evidence) > len(es[j].Evidence) })
	var s strings.Builder
	fmt.Fprintf(&s, "# %s\n\n", topic)
	for _, e := range es {
		teams := map[string]bool{}
		for _, ev := range e.Evidence {
			teams[ev.Team] = true
		}
		fmt.Fprintf(&s, "## %s (%s, seen %d times by %d teams)\n\n", e.ID, e.Kind, len(e.Evidence), len(teams))
		fmt.Fprintf(&s, "**When** %s, **do** %s, **because** %s\n\n", e.When, e.Do, e.Because)
		for _, ev := range e.Evidence {
			line := fmt.Sprintf("- %s %s: %s", ev.Date, ev.Team, ev.Ref)
			if ev.Note != "" {
				line += " (" + ev.Note + ")"
			}
			s.WriteString(line + "\n")
		}
		s.WriteString("\n")
	}
	return s.String()
}
