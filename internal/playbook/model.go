package playbook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// ClaudeCLI runs `claude -p` on the owner's Max login: haiku picks, sonnet writes.
// Every ANTHROPIC_* and CLAUDE_CODE_USE_* variable is removed from the child's
// environment, and an answer whose init message reports an apiKeySource other
// than "none" is refused (ErrAPIKey), which stops the run. Same as landed's.
type ClaudeCLI struct {
	Bin        string // default "claude"
	PickModel  string // default "haiku"
	WriteModel string // default "sonnet"
}

// ErrAPIKey: the CLI ran on an API key, or its login could not be checked.
var ErrAPIKey = errors.New("claude -p ran on an API key or a login it could not check; playbook runs only on a Max subscription login")

const pickPrompt = `You read candidate snippets from the records of AI coding teams: bus log lines, session transcript replies, and the owner's own turns.
Keep a candidate only when it states a finding another session could reuse later: a tactic that worked (with evidence), a trap to avoid, a tool or version fact, a team or harness fact, or a lasting owner preference or correction.
Drop status updates, progress counts, plans, questions, one-off task details, and anything with no lesson in it.
Input: a JSON array of {"id","source","team","text"}.
Reply with only a JSON array, one object per input id, in input order: {"id": "...", "keep": true|false, "topic": "<one of TOPICS>"}.
TOPICS: `

const writePrompt = `You keep a playbook of what AI coding teams learned. Each entry reads: WHEN <situation>, DO <tactic>, BECAUSE <evidence>.
Input: {"candidates": [{"id","team","date","topic","text"}], "existing": [{"id","topic","when","do"}]}.
For each candidate return one object:
- the same finding as an existing entry: {"candidate": "<id>", "same_as": "<existing entry id, never a candidate id>", "outcome": "hit|applied|same", "note": "<short evidence with its numbers>"}
  outcome: "hit" = the problem happened again; "applied" = someone used the advice and it worked; "same" = only stated again.
- it contradicts an existing entry: as above plus "replace": true and the corrected "entry" (same id)
- new: {"candidate": "<id>", "entry": {"id": "<kebab-case, 3-64 chars>", "topic": "<TOPICS>", "kind": "tactic|trap|tool-fact|team-fact|owner-preference", "when": "<the situation>", "do": "<what to do>", "because": "<evidence: team, date, numbers from the text>", "keys": ["<2-6 lowercase words a session start can match: project, stack, tool>"]}}
Leave out a candidate with no lasting lesson. Plain English, short sentences, no filler. Keep numbers, names and versions exact; never invent them.
Reply with only a JSON array.
TOPICS: `

// Pick asks the cheap model which candidates hold a reusable finding.
func (c ClaudeCLI) Pick(ctx context.Context, cs []Candidate) ([]Verdict, error) {
	items := make([]map[string]string, 0, len(cs))
	for _, x := range cs {
		items = append(items, map[string]string{"id": x.ID, "source": x.Source, "team": x.Team, "text": x.Text})
	}
	in, _ := json.Marshal(items)
	out, err := c.run(ctx, or(c.PickModel, "haiku"), pickPrompt+strings.Join(Topics, ", "), in)
	if err != nil {
		return nil, err
	}
	var vs []Verdict
	if err := decodeArray(out, &vs); err != nil {
		return nil, fmt.Errorf("pick reply is not a JSON array: %w", err)
	}
	return vs, nil
}

// Write asks the writing model to turn kept candidates into entries.
func (c ClaudeCLI) Write(ctx context.Context, cs []Candidate, existing []Entry) ([]Op, error) {
	type cand struct {
		ID    string `json:"id"`
		Team  string `json:"team"`
		Date  string `json:"date"`
		Topic string `json:"topic"`
		Text  string `json:"text"`
	}
	type ex struct {
		ID    string `json:"id"`
		Topic string `json:"topic"`
		When  string `json:"when"`
		Do    string `json:"do"`
	}
	var in struct {
		Candidates []cand `json:"candidates"`
		Existing   []ex   `json:"existing"`
	}
	for _, x := range cs {
		in.Candidates = append(in.Candidates, cand{x.ID, x.Team, x.Time.Local().Format("2006-01-02"), x.Topic, x.Text})
	}
	for _, e := range existing {
		in.Existing = append(in.Existing, ex{e.ID, e.Topic, e.When, e.Do})
	}
	data, _ := json.Marshal(in)
	out, err := c.run(ctx, or(c.WriteModel, "sonnet"), writePrompt+strings.Join(Topics, ", "), data)
	if err != nil {
		return nil, err
	}
	var ops []Op
	if err := decodeArray(out, &ops); err != nil {
		return nil, fmt.Errorf("write reply is not a JSON array: %w", err)
	}
	return ops, nil
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// decodeArray reads the first JSON array in a reply into v. Text around it (a
// ```json fence, a remark after the array) is ignored.
func decodeArray(s string, v any) error {
	i := strings.Index(s, "[")
	if i < 0 {
		return errors.New("no [ in the reply")
	}
	return json.NewDecoder(strings.NewReader(s[i:])).Decode(v)
}

// cliEnv is the child's environment: no API credentials, and no
// CLAUDE_CONFIG_DIR, so claude -p always uses the personal Max login in
// ~/.claude, never the work persona's enterprise login.
func cliEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ANTHROPIC_") || strings.HasPrefix(kv, "CLAUDE_CODE_USE_") ||
			strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// CallTimeout ends one claude -p call that hangs.
const CallTimeout = 10 * time.Minute

func (c ClaudeCLI) run(ctx context.Context, model, prompt string, in []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, or(c.Bin, "claude"), "-p", "--model", model,
		"--tools", "", "--strict-mcp-config", "--disable-slash-commands",
		"--no-session-persistence", "--setting-sources", "",
		"--system-prompt", prompt, "--output-format", "stream-json", "--verbose")
	cmd.Env = cliEnv()
	cmd.Dir = os.TempDir()
	// Its own process group, so a kill (bad key, timeout) also ends what it
	// started; WaitDelay stops a leftover child from holding the pipes open.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(in)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("claude -p: %w", err)
	}
	// The init line comes first: a call on an API key is killed there, before
	// it can run up a bill.
	var out bytes.Buffer
	var badKey error
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		out.Write(line)
		out.WriteByte('\n')
		if badKey == nil {
			if ks, ok := initKeySource(line); ok && ks != "none" {
				badKey = fmt.Errorf("%w (apiKeySource %q)", ErrAPIKey, ks)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				break
			}
		}
	}
	werr := cmd.Wait()
	if badKey != nil {
		return "", badKey
	}
	if werr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("claude -p: no answer in %s", CallTimeout)
		}
		why := strings.TrimSpace(tailStr(errb.String(), 300))
		if r := streamError(out.Bytes()); r != "" {
			why = r
		}
		return "", fmt.Errorf("claude -p: %w: %s", werr, why)
	}
	return readStream(out.Bytes())
}

// initKeySource reads the apiKeySource of a stream-json init line.
func initKeySource(line []byte) (string, bool) {
	var m struct {
		Type         string  `json:"type"`
		Subtype      string  `json:"subtype"`
		APIKeySource *string `json:"apiKeySource"`
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "system" || m.Subtype != "init" {
		return "", false
	}
	if m.APIKeySource == nil {
		return "", true // an init line with no source cannot be checked: refused
	}
	return *m.APIKeySource, true
}

func streamError(out []byte) string {
	why := ""
	for _, line := range bytes.Split(out, []byte("\n")) {
		var m struct {
			Type    string `json:"type"`
			Result  string `json:"result"`
			IsError bool   `json:"is_error"`
		}
		if json.Unmarshal(line, &m) == nil && m.Type == "result" && m.IsError {
			why = tailStr(m.Result, 300)
		}
	}
	return why
}

// readStream reads stream-json output and refuses the answer unless the init
// message says no API key was used.
func readStream(out []byte) (string, error) {
	var keySource, result *string
	isError := false
	for _, line := range bytes.Split(out, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m struct {
			Type         string  `json:"type"`
			Subtype      string  `json:"subtype"`
			APIKeySource *string `json:"apiKeySource"`
			Result       *string `json:"result"`
			IsError      bool    `json:"is_error"`
		}
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch {
		case m.Type == "system" && m.Subtype == "init":
			keySource = m.APIKeySource
		case m.Type == "result":
			result, isError = m.Result, m.IsError
		}
	}
	if keySource == nil {
		return "", fmt.Errorf("%w (no init message, so the login cannot be checked)", ErrAPIKey)
	}
	if *keySource != "none" {
		return "", fmt.Errorf("%w (apiKeySource %q)", ErrAPIKey, *keySource)
	}
	if result == nil {
		return "", errors.New("claude -p: no result message")
	}
	if isError {
		return "", fmt.Errorf("claude -p error: %s", tailStr(*result, 300))
	}
	return *result, nil
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
