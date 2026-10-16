package store

// DECISIONS.md: append-only, `<project>-D<n>` entries, supersession read from the newer entry (design §6, §7b).

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

var deciders = map[string]bool{"owner": true, "crius": true, "lead": true}

var headRe = regexp.MustCompile(`^## ([a-z0-9][a-z0-9-]*)-D(\d+) · (\d{4}-\d\d-\d\d)\s*$`)

type Decision struct {
	ID, Project, Date                                          string
	N, Line                                                    int
	Taint                                                      bool
	Title, Why, DecidedBy, Supersedes, Reason, Overrides, Refs string
}

func (d *Decision) field(key string) *string {
	switch key {
	case "Decision":
		return &d.Title
	case "Why":
		return &d.Why
	case "Decided by":
		return &d.DecidedBy
	case "Supersedes":
		return &d.Supersedes
	case "Reason":
		return &d.Reason
	case "Overrides":
		return &d.Overrides
	case "Refs":
		return &d.Refs
	}
	return nil
}

type DecisionInput struct {
	Project, Title, Why, DecidedBy, Supersedes, Reason, Refs string
	Taint                                                    bool
	Today                                                    string // YYYY-MM-DD; empty means today
}

func oneLine(name, value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.Contains(value, "\n") {
		return "", refused("%s must be one non-empty line", name)
	}
	return strings.TrimSpace(value), nil
}

func decider(by string) string {
	if f := strings.Fields(by); len(f) > 0 {
		return f[0]
	}
	return ""
}

// ParseDecisions returns the entries and the problems found. Bad refs are kept and flagged, never dropped.
func ParseDecisions(text string) ([]Decision, []string) {
	var entries []Decision
	var problems []string
	seen := map[string]bool{}
	cur := -1
	for i, line := range strings.Split(text, "\n") {
		no := i + 1
		line = strings.TrimSuffix(line, "\r")
		if m := headRe.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			d := Decision{ID: m[1] + "-D" + m[2], Project: m[1], N: n, Date: m[3], Line: no}
			if seen[d.ID] {
				problems = append(problems, fmt.Sprintf("line %d: duplicate id %s", no, d.ID))
			}
			seen[d.ID] = true
			entries = append(entries, d)
			cur = len(entries) - 1
			continue
		}
		if strings.HasPrefix(line, "## ") {
			problems = append(problems, fmt.Sprintf("line %d: malformed decision header", no))
			cur = -1
			continue
		}
		key, val, ok := strings.Cut(line, ": ")
		if cur < 0 || !ok {
			continue
		}
		d := &entries[cur]
		f := d.field(key)
		if f == nil {
			continue
		}
		if key == "Refs" {
			if i := strings.LastIndex(val, "· taint: "); i >= 0 {
				d.Taint = strings.TrimSpace(val[i+len("· taint: "):]) == "yes"
				val = val[:i]
			}
		}
		*f = strings.TrimSpace(val)
	}
	earlier := map[string]bool{}
	for _, d := range entries {
		if d.Supersedes != "" && !earlier[d.Supersedes] {
			problems = append(problems, fmt.Sprintf("line %d: %s supersedes %s, which is not an earlier entry",
				d.Line, d.ID, d.Supersedes))
		}
		earlier[d.ID] = true
	}
	return entries, problems
}

// ActiveIDs are the ids no later entry supersedes. A supersede of an unknown or later id is ignored (it is flagged).
func ActiveIDs(entries []Decision) map[string]bool {
	active, earlier := map[string]bool{}, map[string]bool{}
	for _, d := range entries {
		active[d.ID] = true
	}
	for _, d := range entries {
		if earlier[d.Supersedes] {
			delete(active, d.Supersedes)
		}
		earlier[d.ID] = true
	}
	return active
}

func RecordDecision(home string, in DecisionInput) (string, error) {
	if !Slug.MatchString(in.Project) {
		return "", refused("bad project slug")
	}
	title, err := oneLine("title", in.Title)
	if err != nil {
		return "", err
	}
	why, err := oneLine("why", in.Why)
	if err != nil {
		return "", err
	}
	by, err := oneLine("decided by", in.DecidedBy)
	if err != nil {
		return "", err
	}
	if !deciders[decider(by)] {
		return "", refused("decided by must start with owner, crius or lead")
	}
	if in.Supersedes != "" && in.Reason == "" {
		return "", refused("a supersede needs --reason")
	}
	if strings.ContainsAny(in.Refs, "\n\r") {
		return "", refused("refs must be one line")
	}
	today := in.Today
	if today == "" {
		today = time.Now().Format("2006-01-02")
	}
	dir := filepath.Join(home, "state", in.Project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "DECISIONS.md")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	old := string(raw)
	entries, _ := ParseDecisions(old)
	overrides := ""
	if in.Supersedes != "" {
		var target *Decision
		for i := range entries {
			if entries[i].ID == in.Supersedes {
				target = &entries[i]
			}
		}
		if target == nil {
			return "", refused("%s is not a decision in %s", in.Supersedes, path)
		}
		if decider(target.DecidedBy) == "owner" && decider(by) != "owner" {
			return "", refused("%s is an owner decision; only the owner supersedes it", in.Supersedes)
		}
		if decider(by) == "owner" && decider(target.DecidedBy) == "crius" {
			overrides = "crius"
		}
	}
	n := 1
	for _, d := range entries {
		if d.Project == in.Project && d.N >= n {
			n = d.N + 1
		}
	}
	id := fmt.Sprintf("%s-D%d", in.Project, n)
	lines := []string{"## " + id + " · " + today, "Decision: " + title, "Why: " + why, "Decided by: " + by}
	if in.Supersedes != "" {
		reason, err := oneLine("reason", in.Reason)
		if err != nil {
			return "", err
		}
		lines = append(lines, "Supersedes: "+in.Supersedes, "Reason: "+reason)
	}
	if overrides != "" {
		lines = append(lines, "Overrides: "+overrides)
	}
	refs, taint := strings.TrimSpace(in.Refs), "no"
	if refs == "" {
		refs = "-"
	}
	if in.Taint {
		taint = "yes"
	}
	lines = append(lines, "Refs: "+refs+" · taint: "+taint)
	text := id + " " + title
	if in.Supersedes != "" {
		text += " (supersedes " + in.Supersedes + ")"
	}
	busLine, err := FormatLine("DECISION", in.Project, decider(by), truncate(text, MaxLen), "crius", time.Now())
	if err != nil {
		return "", err
	}
	sep := ""
	if old != "" && !strings.HasSuffix(old, "\n\n") {
		sep = "\n"
	}
	if _, err := f.WriteString(sep + strings.Join(lines, "\n") + "\n"); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := AppendLine(home, in.Project, busLine); err != nil {
		return "", &Partial{fmt.Sprintf("%s was saved in %s, but its DECISION bus line failed (%v); "+
			"do not record it again, post the bus line by hand", id, path, err)}
	}
	return id, nil
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
