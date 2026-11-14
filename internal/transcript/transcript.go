// Package transcript reads Claude Code session transcripts (*.jsonl) and keeps
// only what landed and playbook need: the owner's own turns, the assistant's text and its file actions.
// Copied from github.com/orzazade/landed (internal/transcript) on 2026-11-14; Says was added for playbook.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Turn is one message the owner typed (or answered through the owner queue).
type Turn struct {
	UUID      string
	SessionID string
	Time      time.Time
	Text      string
	Queue     bool   // came from the owner queue: "[queue qN] <answer>"
	QueueID   string // "q7"
	// PrevAssistant is the last assistant text shown before this turn, so a
	// short answer can be read against the question it answers.
	PrevAssistant string
}

// MaxCommand is where a stored shell command is cut.
const MaxCommand = 4000

// Action is a file edit or shell command the assistant ran.
type Action struct {
	Time    time.Time
	Tool    string // Edit, Write, MultiEdit, NotebookEdit, Bash
	Path    string // file edits
	Text    string // what an edit wrote (new text), cut to 3000 characters
	Command string // Bash
}

// Session is one transcript file.
type Session struct {
	ID         string
	Path       string
	Cwd        string
	Project    string
	Entrypoint string
	Headless   bool   // started by a tool (`claude -p`), not typed by the owner
	BadLines   int    // lines (or their message) that were not valid JSON
	BadTimes   int    // owner turns whose timestamp did not parse
	Injected   []Turn // user turns typed by a tool (watchers, Crius relays), not the owner
	Turns      []Turn
	Actions    []Action
	Says       []Said // the assistant's text blocks, in order (playbook reads findings from them)
}

type record struct {
	Type             string          `json:"type"`
	UUID             string          `json:"uuid"`
	SessionID        string          `json:"sessionId"`
	Cwd              string          `json:"cwd"`
	Entrypoint       string          `json:"entrypoint"`
	Timestamp        string          `json:"timestamp"`
	IsMeta           bool            `json:"isMeta"`
	IsSidechain      bool            `json:"isSidechain"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	IsAPIError       bool            `json:"isApiErrorMessage"`
	Origin           *origin         `json:"origin"`
	Message          json.RawMessage `json:"message"`
}

type origin struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type message struct {
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolInput struct {
	FilePath     string `json:"file_path"`
	NotebookPath string `json:"notebook_path"`
	Command      string `json:"command"`
	NewString    string `json:"new_string"`
	Content      string `json:"content"`
	NewSource    string `json:"new_source"`
	Edits        []struct {
		NewString string `json:"new_string"`
	} `json:"edits"`
}

var queueRe = regexp.MustCompile(`^\[queue (q\d+)\]\s*`)

// Text that the harness puts in a user turn; never the owner's words.
var notOwnerPrefixes = []string{
	"[Request interrupted", "(Re-run of /",
	"This session is being continued from a previous conversation",
	"Caveat: ",
}

// Harness tags seen at the start of real user records (checked 11-14 over 14
// days of transcripts). Listed by name so owner text like "<div> is off" stays.
var harnessTagRe = regexp.MustCompile(`^<(?:task-notification|command-[\w-]+|local-command-[\w-]+|system-reminder|artifact-content[\w-]*|teammate-message|bash-[\w-]+|pasted_content|user-memory-input|user-prompt-submit-hook)\b`)

// Image placeholders the harness writes into the owner's text: "[Image #2]",
// "[Image: original 1206x2622, …]". They are stripped; the words around them stay.
var imageRe = regexp.MustCompile(`\[Image(?: #\d+|: [^\]\n]*)\]`)

// Turns that tools type into an owner tab (bus watchers, Crius relays, spawn
// headers, cross-session messages). They hold someone else's words, so they are
// kept in Session.Injected and never read as the owner's decisions. Each form
// is the exact shape a tool writes, so owner text that merely starts with the
// same word ("DECISION: use postgres", "Lead the way", "Owner field …") stays.
var injectedRe = regexp.MustCompile(`^(?:` +
	`WAKEWATCH:` +
	`|LEADER(?: \(|:| \d)` + // "LEADER (Crius DECISION 4):", "LEADER: …", "LEADER 17:17: …"
	`|STOP \(owner order` +
	`|DECISION (?:from )?[a-z][\w-]*:[a-z][\w-]*` + // "DECISION crius:lead -> …", "DECISION from crius:lead: …"
	`|(?:DONE|NEED|INFO|ALERT|VERIFIED|CORRECTED) [a-z][\w-]*:[a-z][\w-]*` + // pasted bus lines
	`|[a-z][\w-]*:[a-z][\w-]* -> ` + // "crius:lead -> quotes:qa …"
	`|\[crius account=` + // crius-spawn header
	`|(?:Crius|Lead|Leader|Watchdog)(?: \([^)\n]*\)| [\dx:]+)?: ` + // "Crius (for x): …", "Lead 14:4x: …", "Leader: …"
	`|Crius (?:DECISION|re|on)\b|Lead ruling\b` +
	`|(?:The )?[Oo]wner (?:wants|says|said|asked|approved|decided|go|hard rule|order|change)\b` + // the owner in the third person
	`|Read the (?:new |newest |last |BUG )?[^\n]{0,20}?\b(?:crius:lead|LEADER|ORDER from)\b` +
	`|Note: the T3 server restarted` +
	`|From [a-z][\w-]*:[a-z][\w-]*[:,]` + // "From crius:lead: …"
	`|Another Claude session` +
	`|You were resumed` +
	`|Your last turn` +
	`|Delegated task ` +
	`)`)

// A session's first turn of at least this many characters that starts with
// "You are " is a spawn brief written by Crius or a lead.
const spawnBriefMin = 250

const maxPrev = 800

// ParseFile reads one transcript. Bad JSON lines are counted, not hidden.
func ParseFile(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := &Session{Path: path}
	r := bufio.NewReaderSize(f, 1<<20)
	var lastAssistant string
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			s.handle(line, &lastAssistant)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return s, fmt.Errorf("%s: %w", path, err)
		}
	}
	if s.ID == "" {
		s.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if s.Cwd != "" {
		s.Project = filepath.Base(s.Cwd)
	}
	return s, nil
}

func (s *Session) handle(line []byte, lastAssistant *string) {
	if skipFast(line) {
		return
	}
	var rec record
	if err := json.Unmarshal(line, &rec); err != nil {
		s.BadLines++
		return
	}
	if rec.Type != "user" && rec.Type != "assistant" {
		return
	}
	if s.ID == "" && rec.SessionID != "" {
		s.ID = rec.SessionID
	}
	if s.Cwd == "" && rec.Cwd != "" {
		s.Cwd = rec.Cwd
	}
	if s.Entrypoint == "" && rec.Entrypoint != "" {
		s.Entrypoint = rec.Entrypoint
		s.Headless = rec.Entrypoint == "sdk-cli"
	}
	if rec.IsSidechain {
		return
	}
	if rec.IsCompactSummary {
		*lastAssistant = "" // a summary replaces what came before
		return
	}
	ts, terr := time.Parse(time.RFC3339Nano, rec.Timestamp)
	var msg message
	if len(rec.Message) == 0 || json.Unmarshal(rec.Message, &msg) != nil {
		s.BadLines++
		return
	}
	switch rec.Type {
	case "assistant":
		var blocks []block
		if json.Unmarshal(msg.Content, &blocks) != nil {
			s.BadLines++
			return
		}
		if rec.IsAPIError {
			return // "API Error: …" is not a question the owner answers
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				if t := strings.TrimSpace(b.Text); t != "" {
					*lastAssistant = tail(t, maxPrev)
					s.Says = append(s.Says, Said{UUID: rec.UUID, Time: ts, Text: t})
				}
			case "tool_use":
				if a, ok := action(b, ts); ok {
					s.Actions = append(s.Actions, a)
				}
			}
		}
	case "user":
		text, ok := ownerText(rec, msg)
		if !ok {
			return
		}
		if terr != nil {
			s.BadTimes++ // it would fall outside every time window: count it
			return
		}
		if rec.UUID == "" {
			s.BadLines++ // no id: it can be neither cached nor labelled
			return
		}
		t := Turn{UUID: rec.UUID, SessionID: rec.SessionID, Time: ts, Text: text, PrevAssistant: *lastAssistant}
		if t.SessionID == "" {
			t.SessionID = s.ID
		}
		if m := queueRe.FindStringSubmatch(text); m != nil {
			t.Queue, t.QueueID = true, m[1]
			t.Text = strings.TrimSpace(text[len(m[0]):])
		}
		spawnBrief := len(s.Turns) == 0 && len(s.Injected) == 0 &&
			len(text) >= spawnBriefMin && strings.HasPrefix(text, "You are ")
		if spawnBrief || injectedRe.MatchString(text) {
			s.Injected = append(s.Injected, t)
			return
		}
		s.Turns = append(s.Turns, t)
	}
}

// ownerText returns the text of a user record when the owner wrote it.
func ownerText(rec record, msg message) (string, bool) {
	if rec.IsMeta {
		return "", false
	}
	if o := rec.Origin; o != nil {
		switch {
		case o.Kind == "human":
		case o.Kind == "plugin" && o.Name == "owner-queue":
		default:
			return "", false
		}
	}
	var text string
	if json.Unmarshal(msg.Content, &text) != nil {
		// Array content: tool results (never the owner), or the owner's
		// text sent together with pasted images.
		var blocks []block
		if json.Unmarshal(msg.Content, &blocks) != nil {
			return "", false
		}
		var parts []string
		for _, b := range blocks {
			switch b.Type {
			case "tool_result":
				return "", false
			case "text":
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	text = strings.TrimSpace(imageRe.ReplaceAllString(text, ""))
	if text == "" || harnessTagRe.MatchString(text) {
		return "", false
	}
	for _, p := range notOwnerPrefixes {
		if strings.HasPrefix(text, p) {
			return "", false
		}
	}
	return text, true
}

func action(b block, ts time.Time) (Action, bool) {
	var in toolInput
	if json.Unmarshal(b.Input, &in) != nil {
		return Action{}, false
	}
	switch b.Name {
	case "Edit", "Write", "MultiEdit":
		text := in.NewString + in.Content
		for _, e := range in.Edits {
			text += "\n" + e.NewString
		}
		return Action{Time: ts, Tool: b.Name, Path: in.FilePath, Text: head(text, 3000)}, in.FilePath != ""
	case "NotebookEdit":
		return Action{Time: ts, Tool: b.Name, Path: in.NotebookPath, Text: head(in.NewSource, 3000)}, in.NotebookPath != ""
	case "Bash":
		return Action{Time: ts, Tool: b.Name, Command: head(in.Command, MaxCommand)}, in.Command != ""
	}
	return Action{}, false
}

// Walk lists top-level session transcripts under each root, skipping subagent
// transcripts. Every folder that cannot be read is reported in the error, and
// the rest of the tree is still walked. No roots, or no files at all, is an error.
func Walk(roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, errors.New("no transcript roots")
	}
	var files []string
	var errs []error
	seen := map[string]bool{}
	for _, root := range roots {
		// WalkDir does not follow a symlinked root; resolve it first.
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if p == root {
					return err
				}
				errs = append(errs, err)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if d.Name() == "subagents" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			real, err := filepath.EvalSymlinks(p)
			if err != nil {
				real = p
			}
			if !seen[real] {
				seen[real] = true
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("root %s: %w", root, err))
		}
	}
	if len(files) == 0 && len(errs) == 0 {
		errs = append(errs, fmt.Errorf("no transcripts under %s", strings.Join(roots, ", ")))
	}
	return files, errors.Join(errs...)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

var typePrefix = []byte(`{"type":"`)

// skipFast drops records whose type is written first and is neither user nor
// assistant (API request dumps, titles, snapshots) without decoding them.
// Those lines are most of the bytes in a transcript.
func skipFast(line []byte) bool {
	if !bytes.HasPrefix(line, typePrefix) {
		return false
	}
	rest := line[len(typePrefix):]
	i := bytes.IndexByte(rest, '"')
	if i < 0 {
		return false
	}
	t := string(rest[:i])
	return t != "user" && t != "assistant"
}

// Said is one text block the assistant wrote.
type Said struct {
	UUID string
	Time time.Time
	Text string
}
