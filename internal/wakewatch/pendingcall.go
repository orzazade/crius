package wakewatch

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
)

// callMax is where the waiting call's text is cut, so the rest of the alert still fits MaxLine.
const callMax = 160

// callTail is how much of the end of a transcript is read: the waiting call is among its last lines (nothing is
// written after it while the dialog is open), and a lead's transcript runs to 100 MB.
const callTail = 4 << 20

// waitingCall names the tool call a permission prompt waits on (11-19 22:03: the alert said only "Claude needs your
// permission to use Bash", and Crius could not tell it was a `git branch -D`): the tool and its input, for Bash the
// command, as one redacted line cut to callMax. It reads the session's own transcript under its Claude config folder
// (config/projects/*/<sid>.jsonl, or subagents/agent-<agent>.jsonl beside it when agent names a subagent; agent "" for
// not known reads both) and takes the tool_use with no tool_result after it that started last by asked+slack: slack
// covers how far asked may be before the dialog really opened (a few seconds; a minute when the time is known only to
// the minute). Anything that stops it reads "command unknown: <why>".
func waitingCall(config, sid, agent string, asked time.Time, slack time.Duration) string {
	c, err := openCall(config, sid, agent, asked, slack)
	if err != nil {
		// a file error names the file, not its whole path: the session id and config folder already place it, and the
		// line is cut at callMax, which a temp or worktree path alone can pass
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = fmt.Errorf("%s %s: %w", pe.Op, filepath.Base(pe.Path), pe.Err)
		}
		return "command unknown: " + callLine(err.Error())
	}
	return c
}

// callLine is text redacted, then made one line and cut to callMax runes (redacted first: a cut could split a secret so
// the redactor no longer sees it, redact package doc).
func callLine(s string) string {
	s, _ = redact.Redact(s)
	s = strings.Join(strings.Fields(strings.Map(printable, s)), " ")
	if r := []rune(s); len(r) > callMax {
		s = string(r[:callMax]) + "…"
	}
	return s
}

// toolCall is one tool_use a transcript holds no result for.
type toolCall struct {
	at    time.Time
	msg   string // the id of the model message it is part of ("" when the record names none)
	name  string
	input json.RawMessage
	file  string
}

func openCall(config, sid, agent string, asked time.Time, slack time.Duration) (string, error) {
	if config == "" {
		return "", errors.New("crius-hook's state names no Claude config folder for the session")
	}
	if !sidRe.MatchString(sid) {
		return "", fmt.Errorf("the session id %q cannot name a transcript", sid)
	}
	mains, err := filepath.Glob(filepath.Join(config, "projects", "*", sid+".jsonl"))
	if err != nil {
		return "", err
	}
	if len(mains) == 0 {
		return "", fmt.Errorf("no transcript %s.jsonl in any project folder of %s", sid, config)
	}
	// The files to read: the one agent's that holds the dialog, when crius-hook named it (a subagent's file is
	// subagents/agent-<id>.jsonl beside the main one); else the main file and each subagent's file written to since
	// shortly before the prompt (a session keeps hundreds; one that cannot be read could hold the call: loud).
	var files []string
	note := ""
	for _, m := range mains {
		subdir := filepath.Join(strings.TrimSuffix(m, ".jsonl"), "subagents")
		switch {
		case agent == "main":
			files = append(files, m)
		case agent != "":
			if !sidRe.MatchString(agent) {
				return "", fmt.Errorf("the waiting agent id %q cannot name a transcript", agent)
			}
			f := filepath.Join(subdir, "agent-"+agent+".jsonl")
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return "", err
			}
		}
	}
	if len(files) == 0 {
		// picked by time: said, so a guess never reads as sure (deep review, 11-20)
		note = " (picked by time: crius-hook named no agent)"
		if agent != "" {
			note = fmt.Sprintf(" (picked by time: no transcript for agent %s)", agent)
		}
		files = slices.Clone(mains)
		for _, m := range mains {
			subs, err := filepath.Glob(filepath.Join(strings.TrimSuffix(m, ".jsonl"), "subagents", "agent-*.jsonl"))
			if err != nil {
				return "", err
			}
			for _, s := range subs {
				fi, err := os.Stat(s)
				if err != nil {
					return "", err
				}
				if !fi.ModTime().Before(asked.Add(-10 * time.Minute)) {
					files = append(files, s)
				}
			}
		}
	}
	var open []toolCall
	cut := false // a transcript was longer than callTail, so a call before its last part was not seen
	for _, f := range files {
		calls, short, err := openCalls(f)
		if err != nil {
			return "", err
		}
		cut = cut || short
		for _, c := range calls {
			// The waiting call was written before its dialog opened, so by the time the prompt was asked (to slack): a
			// call started later is another one (a parallel subagent's approved run), not the one the dialog asks about.
			if !c.at.After(asked.Add(slack)) {
				open = append(open, c)
			}
		}
	}
	if len(open) == 0 {
		why := fmt.Sprintf("%s shows no tool call still open at %s", filepath.Base(files[0]), asked.Format("15:04:05"))
		if len(files) > 1 {
			why = fmt.Sprintf("%s and %d subagent file(s) show no tool call still open at %s", filepath.Base(files[0]), len(files)-1, asked.Format("15:04:05"))
		}
		if cut {
			why += fmt.Sprintf(" (read the last %d MB of each)", callTail>>20)
		}
		return "", errors.New(why)
	}
	// The newest model message with an open call holds the dialog's call (a subagent's message is newer than the Agent
	// call that started it). Its calls that change things run one after another, so the dialog is on the first of them
	// still open; the later ones have not started (deep review, 11-20).
	slices.SortStableFunc(open, func(a, b toolCall) int { return a.at.Compare(b.at) })
	last := open[len(open)-1]
	var same []toolCall
	for _, c := range open {
		if c.file == last.file && (c.msg == last.msg && c.msg != "" || c.at.Equal(last.at)) {
			same = append(same, c)
		}
	}
	text := describeCall(same[0])
	if n := len(same) - 1; n > 0 {
		text += fmt.Sprintf(" (then %d more call(s) of that message)", n)
	}
	return text + note, nil
}

// openCalls reads the end of one transcript and gives its tool_use blocks that no tool_result answers. short says the
// file was longer than what was read.
func openCalls(path string) (calls []toolCall, short bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	off := max(0, fi.Size()-callTail)
	// From the byte before the part read, so a part that starts on a line's first byte keeps that line: the skip below
	// then drops only the newline before it. A line cut by the start of the read is left out.
	if _, err := f.Seek(max(0, off-1), io.SeekStart); err != nil {
		return nil, false, err
	}
	r := bufio.NewReaderSize(f, 1<<20)
	if off > 0 {
		if _, err := r.ReadBytes('\n'); err != nil && err != io.EOF {
			return nil, true, err
		}
	}
	type rec struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Message   struct {
			ID      string          `json:"id"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	type block struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	byID := map[string]toolCall{}
	var order []string
	for {
		line, rerr := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var rc rec
			var blocks []block
			// a line that is no record, or whose content is text, holds no call
			if json.Unmarshal(line, &rc) == nil && json.Unmarshal(rc.Message.Content, &blocks) == nil {
				// A call is a tool_use in the model's own record ("assistant"), placed in time. Other records copy calls
				// whose results sit elsewhere (api-request-blob, with no timestamp: 10 in the 11-19 transcript's last 4 MB).
				at, terr := time.Parse(time.RFC3339Nano, rc.Timestamp)
				call := rc.Type == "assistant" && terr == nil
				for _, b := range blocks {
					switch {
					case b.Type == "tool_use" && b.ID != "" && call:
						if _, seen := byID[b.ID]; !seen {
							order = append(order, b.ID)
						}
						byID[b.ID] = toolCall{at: at, msg: rc.Message.ID, name: b.Name, input: b.Input, file: path}
					case b.Type == "tool_result":
						delete(byID, b.ToolUseID)
					}
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, off > 0, rerr
		}
	}
	for _, id := range order {
		if c, ok := byID[id]; ok {
			calls = append(calls, c)
		}
	}
	return calls, off > 0, nil
}

// describeCall gives "<tool>: <input>" as one line. A Bash call shows its command. Any other tool shows the values of the
// keys that say where it acts (file, path, url, pattern, a subagent's description) and only the names of the rest: an
// Edit or Write of ~/.secrets is the call that waits on a prompt, and its content would reach Crius's prompt wherever
// no redaction rule knows the secret's shape (deep review, 11-20).
func describeCall(c toolCall) string {
	var in map[string]any
	text := ""
	if json.Unmarshal(c.input, &in) != nil {
		text = "(input not shown: not a JSON object)"
	} else if cmd, ok := in["command"].(string); ok && cmd != "" && c.name == "Bash" {
		text = cmd
	} else {
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(in)) {
			if v, ok := in[k].(string); ok && whereKeys[k] {
				parts = append(parts, k+"="+v)
			} else {
				parts = append(parts, k+"=…")
			}
		}
		text = strings.Join(parts, " ")
	}
	name := strings.Join(strings.Fields(strings.Map(printable, c.name)), "")
	if name == "" {
		name = "(a tool with no name)"
	}
	return name + ": " + callLine(text)
}

// whereKeys are input keys whose value says where a tool acts, not what it writes.
var whereKeys = map[string]bool{"file_path": true, "notebook_path": true, "path": true, "url": true, "pattern": true,
	"description": true}
