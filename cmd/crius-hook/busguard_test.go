package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// hookOut feeds one hook input to the binary and returns its stdout. Like run, it fails on any exit but 0.
func hookOut(t *testing.T, env []string, stdin string) string {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("exit: %v; stderr %q", err, errb.String())
	}
	return out.String()
}

func preToolUse(t *testing.T, cwd, tool string, input map[string]string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"session_id": "s-1", "hook_event_name": "PreToolUse", "cwd": cwd,
		"tool_name": tool, "tool_input": input})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A bus line written straight into a bus log skips bus-post: its local-time stamp, length check, wake and mirror
// refusal. On 11-18 04:12 axi:api-split stamped a line with `date -u` (UTC, 4 h behind the bus) this way; cortex:lead
// did the same on 11-16. Every write into the bus folder itself is refused with a reason naming bus-post; reads,
// bus-post, the notes subfolder and other folders are not.
func TestADirectWriteIntoTheBusFolderIsDenied(t *testing.T) {
	w := newWorld(t)
	bus := filepath.Join(w.home, ".crius", "bus")
	deny := map[string]struct{ tool, cmd, cwd string }{
		"api-split 11-18": {"Bash", `echo "$(date -u +%Y-%m-%dT%H:%M) NEED axi:api-split -> crius:lead — ..." >> ~/.crius/bus/axi.log`, "/w"},
		"tee -a":          {"Bash", `tee -a ~/.crius/bus/x.log`, "/w"},
		"piped tee":       {"Bash", `echo hi | tee ~/.crius/bus/x.log >/dev/null`, "/w"},
		"$HOME quoted":    {"Bash", `echo x > "$HOME/.crius/bus/axi.log"`, "/w"},
		"${HOME}":         {"Bash", `printf 'x\n' >>${HOME}/.crius/bus/crius.log`, "/w"},
		"absolute":        {"Bash", `echo x >> ` + bus + `/axi.log`, "/w"},
		"through a var":   {"Bash", `L=~/.crius/bus/axi.log; echo x >> "$L"`, "/w"},
		"relative in bus": {"Bash", `echo x >> axi.log`, bus},
		"remote marker":   {"Bash", `cat line >> ~/.crius/bus/quotes.log.remote`, "/w"},
		">& file":         {"Bash", `echo x >& ~/.crius/bus/axi.log`, "/w"},
		"export var":      {"Bash", `export L=~/.crius/bus/axi.log; echo x >> $L`, "/w"},
		"sudo tee":        {"Bash", `echo x | sudo tee -a ~/.crius/bus/axi.log`, "/w"},
		"command tee":     {"Bash", `echo x | command tee ~/.crius/bus/axi.log`, "/w"},
		"&>>":             {"Bash", `make &>> ~/.crius/bus/axi.log`, "/w"},
		"in a subshell":   {"Bash", `(cd /w && echo x) >> ~/.crius/bus/axi.log`, "/w"},
		"Write tool":      {"Write", bus + "/axi.log", "/w"},
		"Edit tool":       {"Edit", bus + "/axi.log", "/w"},
	}
	for name, c := range deny {
		input := map[string]string{"command": c.cmd}
		if c.tool != "Bash" {
			input = map[string]string{"file_path": c.cmd}
		}
		out := hookOut(t, w.env(), preToolUse(t, c.cwd, c.tool, input))
		var got struct {
			H struct {
				Event    string `json:"hookEventName"`
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Errorf("%s: not denied, stdout %q", name, out)
			continue
		}
		if got.H.Event != "PreToolUse" || got.H.Decision != "deny" || !strings.Contains(got.H.Reason, "~/.crius/bin/bus-post") {
			t.Errorf("%s: %+v", name, got.H)
		}
	}
	allow := map[string]struct{ tool, cmd string }{
		"tail":             {"Bash", `tail -5 ~/.crius/bus/axi.log`},
		"bus-post":         {"Bash", `~/.crius/bin/bus-post ~/.crius/bus/axi.log INFO a b "x > y"`},
		"note by redirect": {"Bash", "cat > ~/.crius/bus/notes/axi-1.txt <<'EOF'\nhi\nEOF"},
		"note through var": {"Bash", "N=~/.crius/bus/notes/axi-$(date +%Y%m%d-%H%M)-x.txt; cat > $N <<'EOF'\nx\nEOF"},
		"other folder":     {"Bash", `echo x >> ~/other.log`},
		"grep to tmp":      {"Bash", `/usr/bin/grep NEED ~/.crius/bus/axi.log > /tmp/out; wc -l ~/.crius/bus/*.log`},
		"fd dup":           {"Bash", `tail ~/.crius/bus/axi.log 2>&1 >&2 3>&-`},
		"empty target":     {"Bash", `echo x > ""`},
		"bus in text":      {"Bash", `echo "never >> ~/.crius/bus/axi.log" > /tmp/x`},
		"Read tool":        {"Read", bus + "/axi.log"},
		"Write a note":     {"Write", bus + "/notes/axi-2.txt"},
	}
	for name, c := range allow {
		input := map[string]string{"command": c.cmd}
		if c.tool != "Bash" {
			input = map[string]string{"file_path": c.cmd}
		}
		if out := hookOut(t, w.env(), preToolUse(t, "/w", c.tool, input)); out != "" {
			t.Errorf("%s: denied: %q", name, out)
		}
	}
}

// $CRIUS_HOME/bus is the bus too when it is not under $HOME, as bus-post and ingest read it.
func TestAWriteIntoTheCriusHomeBusIsDenied(t *testing.T) {
	w := newWorld(t)
	w.crius = filepath.Join(t.TempDir(), "elsewhere")
	testutil.Write(t, filepath.Join(w.crius, "projects.toml"), "[projects.quotes]\ncwd = [\"/w/quotes\"]\n")
	for _, cmd := range []string{`echo x >> ` + w.crius + `/bus/axi.log`, `echo x >> "$CRIUS_HOME/bus/axi.log"`} {
		if out := hookOut(t, w.env(), preToolUse(t, "/w", "Bash", map[string]string{"command": cmd})); !strings.Contains(out, `"deny"`) {
			t.Errorf("%s: not denied: %q", cmd, out)
		}
	}
}

// A command the bash parser cannot read is let through with exit 0 and an empty stdout; it is logged only when it
// names the bus and could write there, so a zsh-only read does not wake Crius through the hook log.
func TestAnUnparsedCommandIsLetThroughAndLoggedOnlyWhenItCouldWriteTheBus(t *testing.T) {
	w := newWorld(t)
	read := `tail ~/.crius/bus/*.log(om[1,3])`
	if out := hookOut(t, w.env(), preToolUse(t, "/w", "Bash", map[string]string{"command": read})); out != "" {
		t.Fatalf("zsh read denied: %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(w.crius, "logs", "crius-hook.log")); err == nil && strings.Contains(string(b), "not parsed") {
		t.Fatalf("zsh read logged: %q", b)
	}
	write := `for p in ${(f)x}; do echo $p >> ~/.crius/bus/axi.log; done`
	if out := hookOut(t, w.env(), preToolUse(t, "/w", "Bash", map[string]string{"command": write})); out != "" {
		t.Fatalf("unparsed write printed %q", out)
	}
	if l := logText(t, w); !strings.Contains(l, "PreToolUse Bash command naming the bus not parsed, let through") {
		t.Errorf("log %q", l)
	}
}

// Tools other than Bash, Write, Edit and MultiEdit are not read: an input of another shape is not an error.
func TestAnotherToolsInputIsNotRead(t *testing.T) {
	w := newWorld(t)
	in := `{"session_id":"s-1","hook_event_name":"PreToolUse","cwd":"/w","tool_name":"mcp__x__y","tool_input":{"command":["a"],"file_path":7}}`
	if out := hookOut(t, w.env(), in); out != "" {
		t.Fatalf("stdout %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(w.crius, "logs", "crius-hook.log")); err == nil && strings.Contains(string(b), "tool_input") {
		t.Errorf("logged: %q", b)
	}
}
