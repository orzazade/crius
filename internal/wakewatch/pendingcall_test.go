package wakewatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// transcriptLine is one line of a Claude Code transcript (.jsonl) in the shape seen on 11-19: a record with a type, an
// RFC 3339 UTC timestamp and a message whose content is a list of blocks.
func transcriptLine(t *testing.T, typ string, at time.Time, blocks ...map[string]any) string {
	t.Helper()
	rec := map[string]any{"type": typ, "timestamp": at.UTC().Format("2006-01-02T15:04:05.000Z"), "isSidechain": false,
		"sessionId": cortexSID, "cwd": "/Users/me/Projects/cortex", "uuid": "u-" + at.Format("150405.000")}
	if blocks != nil {
		role := map[string]string{"assistant": "assistant"}[typ]
		if role == "" {
			role = "user"
		}
		rec["message"] = map[string]any{"role": role, "content": blocks}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func bashUse(id, command string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "caller": map[string]any{"type": "direct"},
		"input": map[string]any{"command": command, "description": "a step"}}
}

func toolResult(id string, isErr bool) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "content": "done", "is_error": isErr}
}

// cortexTranscript writes the Cortex lead's transcript as it stood at 11-19 22:13: two Bash calls answered, a background
// one answered at once, then the call that hit the permission prompt with nothing after it. It returns the config dir.
func cortexTranscript(t *testing.T, asked time.Time, command string) string {
	t.Helper()
	config := t.TempDir()
	start := asked.Add(-25 * time.Second)
	text := transcriptLine(t, "queue-operation", start, nil) +
		transcriptLine(t, "user", start.Add(time.Second), map[string]any{"type": "text", "text": "go on"}) +
		transcriptLine(t, "assistant", start.Add(4*time.Second), bashUse("toolu_01RjK6tX", "git status --short")) +
		transcriptLine(t, "user", start.Add(8*time.Second), toolResult("toolu_01RjK6tX", false)) +
		transcriptLine(t, "attachment", start.Add(8*time.Second), nil) +
		transcriptLine(t, "assistant", start.Add(9*time.Second), map[string]any{"type": "text", "text": "Cleaning up."}) +
		transcriptLine(t, "assistant", start.Add(12*time.Second), bashUse("toolu_01DMsJk4", "go test ./... > /tmp/t.log 2>&1")) +
		transcriptLine(t, "user", start.Add(12*time.Second), toolResult("toolu_01DMsJk4", false)) +
		// a copy of an API request (no timestamp): it holds an earlier call whose result is in another record, so it
		// is no open call of its own (the real 11-19 transcript had 10 of these in its last 4 MB)
		`{"type":"api-request-blob","sessionId":"` + cortexSID + `","hash":"9f2c","message":{"role":"assistant","content":[` +
		`{"type":"tool_use","id":"toolu_01OLDcopy","name":"Edit","input":{"file_path":"/x","old_string":"a","new_string":"b"}}]}}` + "\n" +
		transcriptLine(t, "assistant", start.Add(17*time.Second), bashUse("toolu_011ZcffThXYBtu7m2VoLU6sC", command))
	testutil.Write(t, filepath.Join(config, "projects", "-Users-me-Projects-cortex", cortexSID+".jsonl"), text)
	return config
}

// stuckCortex is the 11-19 setting: the Cortex lead, a T3 thread, waits on a permission prompt logged at `at`; its
// crius-hook state names config as its Claude config folder ("" for a file that names none).
func stuckCortex(t *testing.T, at time.Time, config string) (string, *fake) {
	t.Helper()
	home := setup(t, map[string]string{"waits.log": permWait(at, cortexSID)})
	writeState(t, home, cortexSID, `"state":"waiting","event":"Notification","detail":"permission_prompt","time":"`+
		at.Format(time.RFC3339Nano)+`","cwd":"/Users/me/Projects/cortex","project":"cortex","role":"unknown","t3_thread":"th-cortex","config":"`+config+`"`)
	return home, &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
}

// 11-19 22:03: the Cortex lead's Bash call waited on a permission prompt; the alert said only "Claude needs your
// permission to use Bash", and Crius could not tell which command (it was `git branch -D`, a delete asked on purpose).
// The alert names the waiting call from the session's own transcript: the tool and its command, on one line.
func TestAStuckPromptNamesTheWaitingCommand(t *testing.T) {
	at := t0.Add(9 * time.Second)
	cmd := "git worktree remove /Users/me/Projects/cortex-hf &&\n  git branch -D hotfix/cortex-hf"
	home, f := stuckCortex(t, at, cortexTranscript(t, at, cmd))
	runOnce(t, home, f, t0.Add(11*time.Minute))
	w := promptWakes(f)
	if len(w) != 1 {
		t.Fatalf("want one wake, got %q (all sent: %q)", w, f.sent)
	}
	t.Logf("the alert Crius gets:\n%s", w[0]) // the morning demo: go test ./internal/wakewatch -run NamesTheWaitingCommand -v
	want := "Bash: git worktree remove /Users/me/Projects/cortex-hf && git branch -D hotfix/cortex-hf"
	if !strings.Contains(w[0], want) {
		t.Fatalf("the wake does not name the waiting call %q: %q", want, w[0])
	}
	for _, answered := range []string{"git status", "go test", "also open", "Edit"} {
		if strings.Contains(w[0], answered) {
			t.Errorf("the wake names the answered call %q: %q", answered, w[0])
		}
	}
	if !strings.Contains(w[0], "T3 holds 1 open request") {
		t.Errorf("the wake lost the T3 count: %q", w[0])
	}
}

// A plain terminal (no T3 thread, no cmux tab) wakes from the hook state alone; it names the call too.
func TestAStuckPromptOutsideT3NamesTheWaitingCommand(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at, "rm -rf build/")
	home := setup(t, map[string]string{"waits.log": permWait(at, cortexSID)})
	writeState(t, home, cortexSID, `"state":"waiting","time":"`+at.Format(time.RFC3339Nano)+`","cwd":"/Users/me/Projects/cortex","config":"`+config+`"`)
	f := &fake{}
	runOnce(t, home, f, t0.Add(14*time.Minute))
	if w := promptWakes(f); len(w) != 1 || !strings.Contains(w[0], "Bash: rm -rf build/") {
		t.Fatalf("want one wake naming the call: %q", w)
	}
}

// A transcript that cannot be read is said, with why: never an alert that just leaves the command out.
func TestAStuckPromptWithAnUnreadableTranscriptSaysWhy(t *testing.T) {
	at := t0.Add(9 * time.Second)
	for _, c := range []struct {
		name, config string
		want         []string
	}{
		{"no transcript", t.TempDir(), []string{"command unknown:", "no transcript", cortexSID + ".jsonl"}},
		{"no config folder", "", []string{"command unknown:", "names no Claude config folder"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, f := stuckCortex(t, at, c.config)
			runOnce(t, home, f, t0.Add(11*time.Minute))
			w := promptWakes(f)
			if len(w) != 1 {
				t.Fatalf("want one wake, got %q", w)
			}
			for _, s := range c.want {
				if !strings.Contains(w[0], s) {
					t.Errorf("wake %q lacks %q", w[0], s)
				}
			}
		})
	}
	// A transcript whose every call has its result shows no open call: said too, never a guess at an answered one.
	config := cortexTranscript(t, at, "git branch -D x")
	path := filepath.Join(config, "projects", "-Users-me-Projects-cortex", cortexSID+".jsonl")
	testutil.Write(t, path, testutil.Read(t, path)+transcriptLine(t, "user", at.Add(time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false)))
	if got := waitingCall(config, cortexSID, "", at, 2*time.Second); !strings.HasPrefix(got, "command unknown: ") || !strings.Contains(got, "no tool call still open") {
		t.Errorf("all calls answered: %q", got)
	}
	// A transcript that is there but cannot be read.
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	if os.Getuid() != 0 {
		if got := waitingCall(config, cortexSID, "", at, 2*time.Second); !strings.HasPrefix(got, "command unknown: ") || !strings.Contains(got, "permission denied") {
			t.Errorf("unreadable transcript: %q", got)
		}
	}
}

// A secret in the waiting command never reaches the alert, not even a piece of it left by the cut: the text is
// redacted before it is cut.
func TestAStuckPromptMasksATokenInTheCommand(t *testing.T) {
	at := t0.Add(9 * time.Second)
	token := "ghp_" + strings.Repeat("Ab3dE", 8) // 44 characters, a GitHub token's shape
	for _, cmd := range []string{
		"git push https://github.com/orzazade/x.git --force-with-lease; echo " + token,
		// the token straddles where the command is cut
		strings.Repeat("x", callMax-10) + " " + token,
		"curl -H 'Authorization: Bearer " + "abcdefghijklmnop" + strings.Repeat("qrstuvwxyz", 3) + "' https://api.example.com/v1/items",
	} {
		got := waitingCall(cortexTranscript(t, at, cmd), cortexSID, "", at, 2*time.Second)
		for _, leak := range []string{"Ab3dE", "abcdefghijklmnop"} {
			if strings.Contains(got, leak) {
				t.Errorf("the call text leaks %q: %q", leak, got)
			}
		}
		if !strings.HasPrefix(got, "Bash: ") {
			t.Errorf("not named: %q", got)
		}
	}
	// and through the whole alert
	home, f := stuckCortex(t, at, cortexTranscript(t, at, "gh api -H 'Authorization: token "+token+"' /user"))
	runOnce(t, home, f, t0.Add(11*time.Minute))
	if w := promptWakes(f); len(w) != 1 || strings.Contains(w[0], "Ab3dE") || !strings.Contains(w[0], "[redacted]") {
		t.Fatalf("want one wake with the token masked: %q", w)
	}
}

// A long command is cut to one short line, so the rest of the alert still fits.
func TestAWaitingCallIsCutToOneShortLine(t *testing.T) {
	at := t0.Add(9 * time.Second)
	got := waitingCall(cortexTranscript(t, at, "echo start\n"+strings.Repeat("echo y; ", 200)), cortexSID, "", at, 2*time.Second)
	if strings.ContainsAny(got, "\n\r") || len([]rune(got)) > len("Bash: ")+callMax+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("not one cut line (%d runes): %q", len([]rune(got)), got)
	}
}

// A subagent's call waits on the prompt while the main transcript shows only the Agent call that started it: the
// alert names the subagent's call, the newest open one. A tool other than Bash shows its input.
func TestAWaitingCallInASubagentIsNamed(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at.Add(-time.Minute), "git status")
	main := filepath.Join(config, "projects", "-Users-me-Projects-cortex", cortexSID+".jsonl")
	testutil.Write(t, main, testutil.Read(t, main)+
		transcriptLine(t, "user", at.Add(-50*time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false))+
		transcriptLine(t, "assistant", at.Add(-40*time.Second), map[string]any{"type": "tool_use", "id": "toolu_agent1", "name": "Agent",
			"input": map[string]any{"description": "fix it", "prompt": "fix the thing"}}))
	sub := filepath.Join(config, "projects", "-Users-me-Projects-cortex", cortexSID, "subagents", "agent-a01ecee2e56d4053d.jsonl")
	testutil.Write(t, sub, transcriptLine(t, "user", at.Add(-39*time.Second), map[string]any{"type": "text", "text": "fix the thing"})+
		transcriptLine(t, "assistant", at.Add(-3*time.Second), map[string]any{"type": "tool_use", "id": "toolu_sub1", "name": "WebFetch",
			"input": map[string]any{"url": "https://example.com/doc", "prompt": "read it"}}))
	// a parallel subagent started an approved run 20 s after the prompt: it waits on no dialog (deep review, 11-20)
	other := filepath.Join(filepath.Dir(sub), "agent-b77.jsonl")
	testutil.Write(t, other, transcriptLine(t, "assistant", at.Add(20*time.Second), bashUse("toolu_sub2", "go test ./...")))
	// an old subagent file, untouched for an hour: not read (a session keeps hundreds), so even an unreadable one
	// does not hide the call
	stale := filepath.Join(filepath.Dir(sub), "agent-old.jsonl")
	testutil.Write(t, stale, transcriptLine(t, "assistant", at.Add(-time.Hour), bashUse("toolu_old", "ls")))
	if err := os.Chmod(stale, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(stale, 0o644) })
	if err := os.Chtimes(stale, at.Add(-time.Hour), at.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	got := waitingCall(config, cortexSID, "", at, 2*time.Second)
	if !strings.HasPrefix(got, "WebFetch: ") || !strings.Contains(got, "url=https://example.com/doc") {
		t.Errorf("want the subagent's WebFetch call: %q", got)
	}
	// the parent's Agent call is open too, but it is no other dialog; a value other than where the tool acts is not shown
	for _, bad := range []string{"also open", "read it"} {
		if strings.Contains(got, bad) {
			t.Errorf("the call text has %q: %q", bad, got)
		}
	}
}

// An Edit or Write shows the file it acts on, never what it writes: a write to ~/.secrets is the call that waits on a
// prompt, and a secret of a shape no redaction rule knows would reach Crius (deep review, 11-20).
func TestAWaitingWriteShowsItsFileNotItsContent(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at, "true")
	main := filepath.Join(config, "projects", "-Users-me-Projects-cortex", cortexSID+".jsonl")
	testutil.Write(t, main, testutil.Read(t, main)+
		transcriptLine(t, "user", at.Add(-time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false))+
		transcriptLine(t, "assistant", at, map[string]any{"type": "tool_use", "id": "toolu_w", "name": "Write",
			"input": map[string]any{"file_path": "/Users/me/.secrets", "content": "TELEGRAM_BOT=8123:AAHplainbutsecret\nother"}}))
	got := waitingCall(config, cortexSID, "", at, 2*time.Second)
	if got != "Write: content=… file_path=/Users/me/.secrets" {
		t.Errorf("got %q", got)
	}
}

// With a real T3 thread id (129 characters on 11-20) and a long cwd, the waiting call and T3's count still fit the line
// Crius gets (MaxLine): the long parts go last (deep review, 11-20: the first version lost both past 500 runes).
func TestAStuckPromptAlertKeepsTheCallWithALongThreadID(t *testing.T) {
	at := t0.Add(9 * time.Second)
	thread := "thread:delegated-task:command%3Amcp%3Ac9bc86ee-d5f8-4570-801a-ba134d6943c4%3Adelegate-task%3A256092f5-8459-40f9-b79c-8255db54f5a5"
	cmd := "git worktree remove /Users/me/Projects/appxite-github/cortex-hf-something-long && git branch -D hotfix/cortex-hf-something-long && " +
		"git push origin --delete hotfix/cortex-hf-something-long-END"
	config := cortexTranscript(t, at, cmd)
	cwd := "…/Projects/appxite-github/cortex/.claude/worktrees/agent-a01ecee2e56d4053d-long"
	home := setup(t, map[string]string{"waits.log": strings.Replace(permWait(at, cortexSID), "cwd=/Users/me/Projects/cortex", "cwd="+cwd, 1)})
	writeState(t, home, cortexSID, `"state":"waiting","time":"`+at.Format(time.RFC3339Nano)+`","cwd":"/Users/me/Projects/appxite-github/cortex/.claude/worktrees/agent-a01ecee2e56d4053d-long","project":"cortex","t3_thread":"`+thread+`","config":"`+config+`"`)
	f := &fake{threads: map[string]T3Thread{thread: {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	w := promptWakes(f)
	if len(w) != 1 {
		t.Fatalf("want one wake, got %q", w)
	}
	want := callLine(cmd) // the command as the alert shows it: cut to callMax
	for _, s := range []string{"Bash: " + want, "T3 holds 1 open request"} {
		if !strings.Contains(w[0], s) {
			t.Errorf("the wake (%d runes) lost %q: %q", len([]rune(w[0])), s, w[0])
		}
	}
}

// The waiting call sits in the last part of a transcript over callTail long. The part read can start on a line's first
// byte: that line is kept (deep review, 11-20: it was dropped as if cut). A call older than the part read is not seen,
// and that is said.
func TestALongTranscriptIsReadFromItsEnd(t *testing.T) {
	at := t0.Add(9 * time.Second)
	dir := filepath.Join(t.TempDir(), "projects", "-Users-me-Projects-cortex")
	path := filepath.Join(dir, cortexSID+".jsonl")
	filler := func(n int) string { // one record line of exactly n bytes, the newline included
		head := `{"type":"attachment","x":"`
		return head + strings.Repeat("f", n-len(head)-3) + "\"}\n"
	}
	call := transcriptLine(t, "assistant", at.Add(-time.Second), bashUse("toolu_big", "git branch -D x"))
	after := filler(callTail - len(call)) // the call line is the first byte of the last callTail bytes
	testutil.Write(t, path, filler(1000)+filler(1000)+call+after)
	config := filepath.Dir(filepath.Dir(dir))
	if got := waitingCall(config, cortexSID, "", at, 2*time.Second); got != "Bash: git branch -D x" {
		t.Errorf("a call on the first byte of the part read: %q", got)
	}
	// one more byte at the end: the call line now starts one byte before the part read, so it is cut and not seen
	testutil.Write(t, path, filler(1000)+filler(1000)+call+filler(callTail-len(call)+1))
	if got := waitingCall(config, cortexSID, "", at, 2*time.Second); !strings.HasPrefix(got, "command unknown: ") || !strings.Contains(got, "read the last 4 MB") {
		t.Errorf("a call before the part read: %q", got)
	}
}

// msgUse is a tool_use line of model message msg (Claude Code writes each block of a message as its own line).
func msgUse(t *testing.T, at time.Time, msg string, use map[string]any) string {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(transcriptLine(t, "assistant", at, use)), &rec); err != nil {
		t.Fatal(err)
	}
	rec["message"].(map[string]any)["id"] = msg
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

// stuckPlain runs one wakewatch pass over a plain-terminal session (no T3 thread, no cmux tab) whose waits.log line is
// `line` and whose crius-hook state has the given extra fields, and gives the one prompt wake.
func stuckPlain(t *testing.T, line, config, fields string, now time.Time) string {
	t.Helper()
	home := setup(t, map[string]string{"waits.log": line})
	writeState(t, home, cortexSID, `"state":"waiting","time":"`+t0.Format(time.RFC3339Nano)+`","cwd":"/Users/me/Projects/cortex","config":"`+config+`"`+fields)
	f := &fake{}
	runOnce(t, home, f, now)
	w := promptWakes(f)
	if len(w) != 1 {
		t.Fatalf("want one wake, got %q", w)
	}
	return w[0]
}

func appendTranscript(t *testing.T, config, rel, text string) {
	t.Helper()
	p := filepath.Join(config, "projects", "-Users-me-Projects-cortex", rel)
	old := ""
	if _, err := os.Stat(p); err == nil {
		old = testutil.Read(t, p)
	}
	testutil.Write(t, p, old+text)
}

// Two calls in one message, both open: they run one after another, so the dialog is on the first; the second has not
// started (deep review, 11-20: the newest was named). A parallel subagent's approved run started 1 s after the prompt
// is not named either: crius-hook's state says the main agent holds the dialog, so only its file is read.
func TestTheDialogsCallIsTheFirstOpenOneOfItsAgent(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at.Add(-time.Minute), "true")
	appendTranscript(t, config, cortexSID+".jsonl",
		transcriptLine(t, "user", at.Add(-40*time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false))+
			msgUse(t, at.Add(-6*time.Second), "msg_A", bashUse("toolu_a1", "git worktree remove /x/cortex-hf"))+
			msgUse(t, at.Add(-5*time.Second), "msg_A", bashUse("toolu_a2", "git push origin --delete hotfix")))
	appendTranscript(t, config, filepath.Join(cortexSID, "subagents", "agent-b77.jsonl"),
		msgUse(t, at.Add(time.Second), "msg_B", bashUse("toolu_b1", "go test ./...")))
	wake := stuckPlain(t, permWait(at, cortexSID), config,
		`,"waits":["main"],"opened":{"main":"`+at.Add(-5*time.Second).Format(time.RFC3339Nano)+`"}`, t0.Add(12*time.Minute))
	if !strings.Contains(wake, "waiting call Bash: git worktree remove /x/cortex-hf (then 1 more call(s) of that message);") {
		t.Errorf("want the first open call of the main agent's newest message: %q", wake)
	}
	// with no agent named, the newest message of any agent is read: here the subagent's
	if got := waitingCall(config, cortexSID, "", at, 2*time.Second); !strings.HasPrefix(got, "Bash: go test") {
		t.Errorf("no agent named: %q", got)
	}
}

// A subagent holds the dialog: its own file is read (subagents/agent-<id>.jsonl), not a newer call of another agent.
// A subagent id with no file is said, and the call is then picked by time.
func TestTheDialogsSubagentFileIsRead(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at.Add(-time.Minute), "true")
	appendTranscript(t, config, filepath.Join(cortexSID, "subagents", "agent-a01ec.jsonl"),
		msgUse(t, at.Add(-4*time.Second), "msg_S", bashUse("toolu_s1", "rm -rf /tmp/build")))
	appendTranscript(t, config, filepath.Join(cortexSID, "subagents", "agent-b77.jsonl"),
		msgUse(t, at.Add(time.Second), "msg_B", bashUse("toolu_b1", "go test ./...")))
	wake := stuckPlain(t, permWait(at, cortexSID), config,
		`,"waits":["a01ec","main"],"opened":{"a01ec":"`+at.Add(-3*time.Second).Format(time.RFC3339Nano)+`","main":"`+at.Add(time.Minute).Format(time.RFC3339Nano)+`"}`,
		t0.Add(12*time.Minute))
	if !strings.Contains(wake, "waiting call Bash: rm -rf /tmp/build;") {
		t.Errorf("want the subagent's call: %q", wake)
	}
	if got := waitingCall(config, cortexSID, "c99", at, 2*time.Second); !strings.HasSuffix(got, "(picked by time: no transcript for agent c99)") {
		t.Errorf("a subagent with no file: %q", got)
	}
}

// A hook line whose at= names another minute than its stamp gives only the minute: the call may have started up to a
// minute after it, and is still found. A later dialog, timed from its own open time, takes 2 s again: a parallel
// agent's run started 30 s after it is not the one asked about.
func TestAPromptKnownToTheMinuteLooksAMinuteOn(t *testing.T) {
	line := strings.Replace(permWait(t0, cortexSID), "at=22:00:00", "at=22:01:00", 1) // stamp 22:00, at= 22:01
	config := cortexTranscript(t, t0.Add(38*time.Second), "git branch -D late")       // the call at 22:00:30
	if wake := stuckPlain(t, line, config, `,"waits":["main"]`, t0.Add(12*time.Minute)); !strings.Contains(wake, "waiting call Bash: git branch -D late;") {
		t.Errorf("a call 30 s into the prompt's minute: %q", wake)
	}
	opened := t0.Add(5 * time.Minute)
	config = cortexTranscript(t, opened.Add(-time.Minute), "true")
	appendTranscript(t, config, cortexSID+".jsonl",
		transcriptLine(t, "user", opened.Add(-40*time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false))+
			msgUse(t, opened.Add(-2*time.Second), "msg_L", bashUse("toolu_l1", "git reset --hard")))
	appendTranscript(t, config, filepath.Join(cortexSID, "subagents", "agent-b77.jsonl"),
		msgUse(t, opened.Add(30*time.Second), "msg_B", bashUse("toolu_b1", "go test ./...")))
	wake := stuckPlain(t, line, config, `,"waits":["notify"],"opened":{"notify":"`+opened.Format(time.RFC3339Nano)+`"}`, t0.Add(20*time.Minute))
	if !strings.Contains(wake, "waiting call Bash: git reset --hard;") {
		t.Errorf("a later dialog: %q", wake)
	}
}

// Only the model's own records hold calls: a copy in another record, even one with a timestamp, is none.
func TestACallCopiedIntoAnotherRecordIsNoCall(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at, "git branch -D real")
	appendTranscript(t, config, cortexSID+".jsonl", transcriptLine(t, "progress", at.Add(-time.Second), bashUse("toolu_copy", "echo copy")))
	if got := waitingCall(config, cortexSID, "main", at, 2*time.Second); got != "Bash: git branch -D real" {
		t.Errorf("got %q", got)
	}
}

// Only Bash shows its command: another tool's "command" field is its input like any other (an MCP tool's
// `deploy --token=…` would pass on to Crius whole wherever redaction misses the shape).
func TestOnlyBashShowsItsCommand(t *testing.T) {
	at := t0.Add(9 * time.Second)
	config := cortexTranscript(t, at.Add(-time.Minute), "true")
	appendTranscript(t, config, cortexSID+".jsonl",
		transcriptLine(t, "user", at.Add(-40*time.Second), toolResult("toolu_011ZcffThXYBtu7m2VoLU6sC", false))+
			msgUse(t, at.Add(-3*time.Second), "msg_M", map[string]any{"type": "tool_use", "id": "toolu_m", "name": "mcp__ops__run",
				"input": map[string]any{"command": "deploy prodsecretvalue", "env": "prod"}}))
	if got := waitingCall(config, cortexSID, "main", at, 2*time.Second); got != "mcp__ops__run: command=… env=…" {
		t.Errorf("got %q", got)
	}
}
