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
)

// Topics are the files of the playbook. A model answer with another topic is
// filed under "other" and shows in the index, so it is seen and can be moved.
var Topics = []string{"testing", "review-and-qa", "team-and-bus", "harness-and-accounts", "ui-and-design",
	"deploy-and-k8s", "databases", "stack-quirks", "owner-preferences", "other"}

// Kinds of entry.
var Kinds = map[string]bool{"tactic": true, "trap": true, "tool-fact": true, "team-fact": true, "owner-preference": true}

// Evidence is one sighting of a finding.
type Evidence struct {
	Team string `json:"team"`
	Date string `json:"date"`
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
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
	Created  string     `json:"created,omitempty"`
	Archived bool       `json:"archived,omitempty"`
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

// Apply adds one model answer. It returns true when a new entry was added and
// false when evidence went to an existing one. A new entry whose id is taken is
// the same finding again, so it adds evidence too.
func (b *Book) Apply(op Op, c Candidate) (bool, error) {
	ev := Evidence{Team: c.Team, Date: c.Time.Local().Format("2006-01-02"), Ref: c.Ref, Note: op.Note}
	target := op.SameAs
	if target == "" && b.Find(op.Entry.ID) != nil {
		target = op.Entry.ID
	}
	if target != "" {
		e := b.Find(target)
		if e == nil {
			return false, fmt.Errorf("same_as %q is not an entry", target)
		}
		for _, old := range e.Evidence {
			if old.Ref == ev.Ref {
				return false, nil // the same line read twice is not a second sighting
			}
		}
		if op.Replace {
			if err := check(op.Entry); err != nil {
				return false, err
			}
			e.When, e.Do, e.Because, e.Kind = op.Entry.When, op.Entry.Do, op.Entry.Because, op.Entry.Kind
			if len(op.Entry.Keys) > 0 {
				e.Keys = op.Entry.Keys
			}
		}
		e.Archived = false
		e.Evidence = append(e.Evidence, ev)
		return false, nil
	}
	e := op.Entry
	if err := check(e); err != nil {
		return false, err
	}
	if !contains(Topics, e.Topic) {
		e.Topic = "other"
	}
	e.Evidence = []Evidence{ev}
	e.Created = ev.Date
	b.Entries = append(b.Entries, e)
	return true, nil
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
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
