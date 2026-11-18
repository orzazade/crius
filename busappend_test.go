package crius

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// knownAppends: every function outside store.OpenBus that opens a file for append, and why that file is no bus log. A bus
// line goes through store.OpenBus (store.AppendLine, watchdog.PostOn and bus-post use it), which refuses a log that is only
// a copy of a bus on another machine (its <log>.remote marker). A new function that appends must be named here, or use
// OpenBus.
var knownAppends = map[string]string{
	"cmd/bus-post/main.go:appendRefusal":          "logs/bus-post-refused.log",
	"cmd/crius-hook/main.go:appendLog":            "logs/crius-hook.log",
	"cmd/crius-statusline/main.go:refresh":        "logs/ent-refresh.log",
	"internal/board/board.go:Update":              "the board's <board>.events.log",
	"internal/dispatch/dispatch.go:appendLine":    "the dispatcher's landed.log",
	"internal/playbook/playbook.go:saveLost":      "the playbook's dropped.jsonl",
	"internal/playbook/show.go:LogShown":          "the playbook's shown.log",
	"internal/quota/sample.go:Sample":             "the quota spool's quota.jsonl",
	"internal/scorecard/scorecard.go:Write":       "a scorecard's revision notes",
	"internal/store/decisions.go:RecordDecision":  "state/<project>/DECISIONS.md; its bus line goes through AppendLine",
	"internal/wakewatch/wakewatch.go:load":        "logs/load.log",
	"internal/wakewatch/wakewatch.go:appendLines": "logs/wakewatch-state.log",
}

// appendSites gives "file:function" for each function in src that names O_APPEND (os or syscall), whatever builds the flag.
func appendSites(t *testing.T, fset *token.FileSet, rel string, src any) []string {
	t.Helper()
	f, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if s, ok := n.(*ast.SelectorExpr); ok && s.Sel.Name == "O_APPEND" {
				found = true
			}
			return !found
		})
		if found {
			out = append(out, rel+":"+fn.Name.Name)
		}
	}
	return out
}

func TestEveryAppendIsABusThroughOpenBusOrAKnownLog(t *testing.T) {
	fset := token.NewFileSet()
	var sites []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			sites = append(sites, appendSites(t, fset, filepath.ToSlash(path), nil)...)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sites)
	seen := map[string]bool{}
	for _, s := range sites {
		seen[s] = true
		if s == "internal/store/remote.go:OpenBus" {
			continue
		}
		if _, ok := knownAppends[s]; !ok {
			t.Errorf("%s opens a file for append: a bus line goes through store.OpenBus (it refuses a copy of a bus on "+
				"another machine); another log is named in knownAppends with what it is", s)
		}
	}
	if !seen["internal/store/remote.go:OpenBus"] {
		t.Error("store.OpenBus, the one bus opener, was not found")
	}
	for k := range knownAppends {
		if !seen[k] {
			t.Errorf("knownAppends names %s, which no longer appends: drop it", k)
		}
	}
	if len(sites) < 10 { // positive control: 13 on 11-19
		t.Fatalf("found only %d append sites: %q", len(sites), sites)
	}
}

// The walk sees a new direct append, whatever builds the flag.
func TestTheAppendWalkSeesANewDirectAppend(t *testing.T) {
	src := `package x
import "os"
func postNew(p string) error {
	flag := os.O_WRONLY | os.O_APPEND
	f, err := os.OpenFile(p, flag, 0o600)
	if err != nil { return err }
	return f.Close()
}
func other() {}
`
	got := appendSites(t, token.NewFileSet(), "internal/x/x.go", src)
	if len(got) != 1 || got[0] != "internal/x/x.go:postNew" {
		t.Fatalf("got %q, want the one new append", got)
	}
	if _, ok := knownAppends[got[0]]; ok {
		t.Fatal("the new site must not be known")
	}
}
