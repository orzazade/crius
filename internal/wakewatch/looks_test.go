package wakewatch

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// failedLooks gives the lines sent to Crius that say the T3 idle check failed to look.
func failedLooks(f *fake) []string {
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "T3 thread idle check failed to look at") {
			out = append(out, s)
		}
	}
	return out
}

// 11-17 16:3x, live: "the T3 thread idle check failed to look at axi · ms-flow: thread mcp:launch:9981e1aa…: t3 thread
// show: t3 gave no answer in time (10s)"; a minute later the thread read fine. The third false wake of one cause that
// day (2b6733b sign-in read, 2e2e25c provider sweep's ssh look): one failed look woke Crius. A look must fail on
// lookMisses runs in a row before Crius hears of it.
func TestOneFailedT3LookDoesNotWakeCrius(t *testing.T) {
	home := threadHome(t, "ms")
	f := &fake{threadErr: fmt.Errorf("t3 thread show: %w (10s)", ErrT3NoAnswer)}
	runOnce(t, home, f, t0)
	if hit := failedLooks(f); len(hit) != 0 {
		t.Fatalf("a single failed T3 look woke Crius: %q", hit)
	}
}

func TestASecondFailedT3LookInARowWakesCrius(t *testing.T) {
	home := threadHome(t, "ms")
	f := &fake{threadErr: fmt.Errorf("t3 thread show: %w (10s)", ErrT3NoAnswer)}
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	hit := failedLooks(f)
	if len(hit) != 1 || !strings.Contains(hit[0], "quotes · ms") || !strings.Contains(hit[0], "no answer") {
		t.Fatalf("a look that failed on two runs in a row must wake Crius once: %q (all: %q)", hit, f.sent)
	}
}

func TestAGoodT3LookBetweenTwoFailedOnesStartsTheCountAgain(t *testing.T) {
	home := threadHome(t, "ms")
	bad := fmt.Errorf("t3 thread show: %w (10s)", ErrT3NoAnswer)
	f := &fake{threadErr: bad, threads: map[string]T3Thread{"th-ms": {Status: "running"}}}
	runOnce(t, home, f, t0)
	f.threadErr = nil
	runOnce(t, home, f, t0.Add(5*time.Minute))
	f.threadErr = bad
	runOnce(t, home, f, t0.Add(10*time.Minute))
	if hit := failedLooks(f); len(hit) != 0 {
		t.Fatalf("a good look between two failed ones must start the count again: %q", hit)
	}
}

// runLooked runs Run as many runs in a row as a failed look needs before Crius hears of it (lookMisses), so a test of
// what a failed-look alert says sees it said. The runs before the last one must say no failed look.
func runLooked(t *testing.T, f *fake, d Deps) {
	t.Helper()
	for i := 1; i <= lookMisses; i++ {
		before := len(f.sent)
		if err := Run(d); err != nil {
			t.Fatal(err)
		}
		if i == lookMisses {
			return
		}
		for _, s := range f.sent[before:] {
			if lookWords.MatchString(s) {
				t.Fatalf("run %d of %d said a failed look before its time: %q", i, lookMisses, s)
			}
		}
	}
}

// lookWords is how an alert says that a look failed.
var lookWords = regexp.MustCompile(`(?i)failed to (look|read)|could not (read|look|list|tell|be read|be checked)|cannot (be read|read|look)|unreadable|\bunread\b|does not answer|not read:`)

// notLooks are the alert kinds that say what a look found, or that a write failed: said on the run they are raised.
var notLooks = map[string]string{
	"bus":               "a bus line that woke nobody",
	"dead":              "a role with no live Claude",
	"disk":              "free disk below the line",
	"folder":            "a session in the wrong folder",
	"handed":            "a lead handed back",
	"hooklog":           "crius-hook logged error lines",
	"idle":              "an idle tab",
	"look":              "a workspace with the wrong name or colour",
	"paid":              "a claude on paid usage",
	"projects-unknown":  "a watched project with no bus file",
	"prompt":            "a permission prompt waiting",
	"queue-failed":      "a write into the owner queue failed",
	"restore":           "a tab that would restore on another folder",
	"screen":            "a tab asking",
	"statelog-failed":   "a write of the screen-versus-state log failed",
	"stopped":           "a tab stopped",
	"swap":              "swap above the line",
	"t3idle":            "an idle T3 thread",
	"tabname":           "a tab with no name",
	"twins":             "two live sessions for one role",
	"wait-cursor-ahead": "the waiter cursor is past the log",
	"waited":            "a lead waited on",
	"waiter":            "a bus log with no waiter",
}

// alertSite is one place in the package source that raises an alert: its kind and the literal text it says.
type alertSite struct {
	at, fn, kind, text string
	unstable           bool // its key holds an error, a time or a formatted value: it may change from run to run
}

// unstableExpr says whether a key part may change from run to run while the same item fails: an error's text, a time,
// a formatted value.
func unstableExpr(e ast.Node) bool {
	bad := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			switch x.Sel.Name {
			case "Error", "Err", "Format", "String", "Unix", "UnixNano", "Sprint", "Sprintf", "Now":
				bad = true
			}
		case *ast.Ident:
			if x.Name == "err" || x.Name == "why" {
				bad = true
			}
		}
		return true
	})
	return bad
}

// pkgSource parses the package's own (non-test) files.
func pkgSource(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	fs := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fs, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return fs, files
}

// closureArgs gives, when x is a parameter of the closure in fn that x is used in, what each call of that closure passes
// for it.
func closureArgs(fn *ast.FuncDecl, x *ast.Ident) []ast.Expr {
	var out []ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			id, ok := l.(*ast.Ident)
			lit, isFunc := as.Rhs[i].(*ast.FuncLit)
			if !ok || !isFunc || x.Pos() < lit.Pos() || x.Pos() > lit.End() {
				continue
			}
			at, j := -1, 0
			for _, p := range lit.Type.Params.List {
				for _, pn := range p.Names {
					if pn.Name == x.Name {
						at = j
					}
					j++
				}
			}
			if at < 0 {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if f, ok := c.Fun.(*ast.Ident); ok && f.Name == id.Name && at < len(c.Args) {
						out = append(out, c.Args[at])
					}
				}
				return true
			})
		}
		return true
	})
	return out
}

// alertSites finds every [2]string alert literal in the package source and resolves its key to the kinds it can have:
// a literal, a const, a local assigned once, a closure parameter (each call of the closure), a func of the package (each
// return) or fmt.Sprintf (its format). A key it cannot resolve fails the test: a new shape of alert must be taught here.
func alertSites(t *testing.T) []alertSite {
	fs, files := pkgSource(t)
	consts := map[string]string{}
	funcs := map[string]*ast.FuncDecl{}
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil {
					funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					if vs, ok := sp.(*ast.ValueSpec); ok && d.Tok == token.CONST {
						for i, n := range vs.Names {
							if i < len(vs.Values) {
								if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
									consts[n.Name], _ = strconv.Unquote(bl.Value)
								}
							}
						}
					}
				}
			}
		}
	}
	unstable := false // set by keys when a key part may change from run to run
	var keys func(e ast.Expr, fn *ast.FuncDecl, depth int) []string
	keys = func(e ast.Expr, fn *ast.FuncDecl, depth int) []string {
		if depth > 30 {
			return nil
		}
		switch x := e.(type) {
		case *ast.BasicLit:
			v, _ := strconv.Unquote(x.Value)
			return []string{v}
		case *ast.BinaryExpr:
			unstable = unstable || unstableExpr(x.Y)
			ast.Inspect(x.Y, func(n ast.Node) bool { // a closure parameter in the key: what each call passes
				if id, ok := n.(*ast.Ident); ok {
					for _, a := range closureArgs(fn, id) {
						unstable = unstable || unstableExpr(a)
					}
				}
				return true
			})
			return keys(x.X, fn, depth+1)
		case *ast.Ident:
			if v, ok := consts[x.Name]; ok {
				return []string{v}
			}
			var out []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
					for i, l := range as.Lhs {
						if id, ok := l.(*ast.Ident); ok && id.Name == x.Name {
							if _, isFunc := as.Rhs[i].(*ast.FuncLit); !isFunc {
								out = append(out, keys(as.Rhs[i], fn, depth+1)...)
							}
						}
					}
				}
				return true
			})
			for _, a := range closureArgs(fn, x) {
				out = append(out, keys(a, fn, depth+1)...)
			}
			return out
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sprintf" && len(x.Args) > 0 {
				for _, a := range x.Args[1:] {
					unstable = unstable || unstableExpr(a)
				}
				ks := keys(x.Args[0], fn, depth+1)
				for i, k := range ks {
					ks[i], _, _ = strings.Cut(k, "%")
				}
				return ks
			}
			if f, ok := x.Fun.(*ast.Ident); ok && funcs[f.Name] != nil {
				var out []string
				ast.Inspect(funcs[f.Name].Body, func(n ast.Node) bool {
					if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
						out = append(out, keys(r.Results[0], funcs[f.Name], depth+1)...)
					}
					return true
				})
				return out
			}
		}
		return nil
	}
	texts := func(e ast.Expr) string {
		var b strings.Builder
		ast.Inspect(e, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				v, _ := strconv.Unquote(bl.Value)
				b.WriteString(v + " ")
			}
			return true
		})
		return b.String()
	}
	is2 := func(e ast.Expr) bool {
		at, ok := e.(*ast.ArrayType)
		if !ok {
			return false
		}
		bl, ok := at.Len.(*ast.BasicLit)
		return ok && bl.Value == "2"
	}
	var out []alertSite
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				var lits []*ast.CompositeLit
				if is2(cl.Type) {
					lits = append(lits, cl)
				} else if at, ok := cl.Type.(*ast.ArrayType); ok && at.Len == nil && is2(at.Elt) {
					for _, e := range cl.Elts {
						if c, ok := e.(*ast.CompositeLit); ok && c.Type == nil {
							lits = append(lits, c)
						}
					}
				}
				for _, c := range lits {
					at := fs.Position(c.Pos()).String()
					if fn.Name.Name == "pick" { // the screen and state views of a check, not an alert
						continue
					}
					if len(c.Elts) != 2 {
						t.Errorf("%s: a [2]string alert without two elements", at)
						continue
					}
					unstable = false
					ks := keys(c.Elts[0], fn, 0)
					if len(ks) == 0 {
						t.Errorf("%s (%s): cannot tell the key of this alert; teach alertSites its shape", at, fn.Name.Name)
					}
					for _, k := range ks {
						out = append(out, alertSite{at: at, fn: fn.Name.Name, kind: alertKind(k), text: texts(c.Elts[1]), unstable: unstable})
					}
				}
				return true
			})
		}
	}
	return out
}

// Every alert kind in the source is classified: a failed look (lookKinds, held by Run), one whose check counts its own
// misses (ownMisses), a failed look said at once with its reason (lookNow), or a finding or failed write said at once
// (notLooks). An alert whose text says a look failed is never a finding. No class names a kind the source no longer
// raises. A held look's key names the item only: a key holding an error, a time or a formatted value could change each
// run, never reach lookMisses, and leave a lasting failure unsaid.
func TestEveryAlertKindIsClassified(t *testing.T) {
	sites := alertSites(t)
	if len(sites) < 60 { // positive control: the walk finds the alerts it is meant to (68 on 11-17)
		t.Fatalf("found only %d alert sites: the walk is broken", len(sites))
	}
	seen := map[string]bool{}
	for _, s := range sites {
		seen[s.kind] = true
		_, now := lookNow[s.kind]
		look := lookKinds[s.kind] || ownMisses[s.kind] || now
		if _, finding := notLooks[s.kind]; !look && !finding {
			t.Errorf("%s (%s): alert kind %q is in neither lookKinds, ownMisses, lookNow nor notLooks: a failed look goes in lookKinds", s.at, s.fn, s.kind)
		}
		if _, finding := notLooks[s.kind]; finding && look {
			t.Errorf("kind %q is both a look and a finding", s.kind)
		}
		if lookKinds[s.kind] && s.unstable {
			t.Errorf("%s (%s): the key of held look kind %q holds a part that may change from run to run: name the item only", s.at, s.fn, s.kind)
		}
		if !look && lookWords.MatchString(s.text) {
			t.Errorf("%s (%s): alert kind %q says a look failed (%q) but is not held as one: put it in lookKinds", s.at, s.fn, s.kind, s.text)
		}
	}
	for _, class := range []map[string]bool{lookKinds, ownMisses} {
		for k := range class {
			if !seen[k] {
				t.Errorf("kind %q is classified but no alert raises it", k)
			}
		}
	}
	for _, class := range []map[string]string{notLooks, lookNow} {
		for k := range class {
			if !seen[k] {
				t.Errorf("kind %q is classified but no alert raises it", k)
			}
		}
	}
	// positive control for the key check
	e, err := parser.ParseExpr(`"t3-failed:" + who + err.Error()`)
	if err != nil || !unstableExpr(e.(*ast.BinaryExpr).Y) {
		t.Fatal("unstableExpr misses an error in a key:", err)
	}
	if e, _ := parser.ParseExpr(`"t3-failed:" + who`); unstableExpr(e.(*ast.BinaryExpr).Y) {
		t.Fatal("unstableExpr flags a stable key")
	}
}

// Walks every failed-look alert kind the source raises through Run's rule: held on its first run, said on its second in
// a row, and held again after a run it was not raised (a good look). A kind that counts its own misses must call
// newMisses in the function that raises it.
func TestEveryFailedLookAlertSiteGoesThroughMisses(t *testing.T) {
	sites := alertSites(t)
	kinds := map[string]bool{}
	for _, s := range sites {
		if lookKinds[s.kind] {
			kinds[s.kind+":x"] = true
		}
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) < 15 { // positive control: 19 on 11-17
		t.Fatalf("only %d failed-look kinds found: %q", len(keys), keys)
	}
	st := state{}
	raise := func() [][2]string {
		var out [][2]string
		for _, k := range keys {
			out = append(out, [2]string{k, "failed to look"})
		}
		return out
	}
	for run, want := range []bool{true, false, false} {
		held := holdLooks(&st, raise())
		for _, k := range keys {
			if held[k] != want {
				t.Errorf("run %d: %s held=%v, want %v", run+1, k, held[k], want)
			}
		}
	}
	holdLooks(&st, nil) // every look good on this run
	held := holdLooks(&st, raise())
	for _, k := range keys {
		if !held[k] {
			t.Errorf("after a good look %s was said on its first failed run again", k)
		}
	}
	_, files := pkgSource(t)
	callsNewMisses := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CallExpr); ok {
						if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "newMisses" {
							callsNewMisses[fn.Name.Name] = true
						}
					}
					return true
				})
			}
		}
	}
	for _, s := range sites {
		if ownMisses[s.kind] && !callsNewMisses[s.fn] {
			t.Errorf("%s: %s raises %q, which counts its own misses, but never calls newMisses", s.at, s.fn, s.kind)
		}
	}
}

// Every alert that does not go through Run's alert list (a direct post, send, push or say) sits in a function named
// here, and one whose text says a look failed counts its misses (newMisses, or alertOnce), or is a one-off named here.
// The text is the call's string literals and those of each local it passes, as assigned in the function.
func TestEveryDirectAlertIsKnown(t *testing.T) {
	direct := map[string]string{
		"Run":            "posts the alert lines; a state file that does not parse is said once as it is set aside",
		"alertOnce":      "posts a failed clicks or prompts run; only failed looks wait a run",
		"prompts":        "types wakes into Crius's tab, posts them when it cannot, pushes the owner",
		"stuckPush":      "pushes the owner about Crius's own stuck tab, or posts NEED-OWNER",
		"clicks":         "posts the owner's clicks; a state file that does not parse is said once as it is set aside",
		"ProviderErrors": "the provider-error sweep: a look it could not make counts with newMisses",
		"AfterBoot":      "one-off after a boot or login: there is no next run to wait for",
		"Clicks":         "a failed run goes through alertOnce",
		"Prompts":        "a failed run goes through alertOnce",
		"rearm":          "types a waiter re-arm line into a team's tab: an order, not an alert",
		"postLine":       "posts a re-arm or rig line on a team's bus: an order, not an alert",
		"tellLeads":      "tells a team's leads that a role died: a finding",
		"Restart":        "crius-wakewatch restart prints its verdict to the caller once: there is no next run to wait for",
		"restartTab":     "types /clear and a brief into a tab (an order) and prints its verdict to the caller once",
	}
	gated := map[string]bool{"alertOnce": true, "ProviderErrors": true}
	// oneOff: functions whose failed-look text is said at once, and the words that text must hold
	oneOff := map[string]string{"AfterBoot": "", "Run": "was unreadable", "clicks": "was unreadable", "Restart": "FAILED",
		"restartTab": "FAILED"}
	sends := map[string]bool{"Post": true, "PostOn": true, "Send": true, "SendCrius": true, "SendTab": true, "Push": true, "say": true, "alertOnce": true}
	fs, files := pkgSource(t)
	n := 0
	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			lits := func(e ast.Node) string {
				var b strings.Builder
				ast.Inspect(e, func(x ast.Node) bool {
					if bl, ok := x.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						v, _ := strconv.Unquote(bl.Value)
						b.WriteString(v + " ")
					}
					return true
				})
				return b.String()
			}
			ast.Inspect(fn.Body, func(nd ast.Node) bool {
				c, ok := nd.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				switch x := c.Fun.(type) {
				case *ast.SelectorExpr:
					name = x.Sel.Name
				case *ast.Ident:
					name = x.Name
				}
				if !sends[name] {
					return true
				}
				n++
				at := fs.Position(c.Pos()).String()
				if _, ok := direct[fn.Name.Name]; !ok {
					t.Errorf("%s: %s alerts directly by %s; route it through Run's alert list (a [2]string) or name it in TestEveryDirectAlertIsKnown", at, fn.Name.Name, name)
					return true
				}
				text := lits(c)
				for _, a := range c.Args { // a local passed in: what the function assigns it
					ast.Inspect(a, func(x ast.Node) bool {
						id, ok := x.(*ast.Ident)
						if !ok {
							return true
						}
						ast.Inspect(fn.Body, func(y ast.Node) bool {
							if as, ok := y.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
								for i, l := range as.Lhs {
									if lid, ok := l.(*ast.Ident); ok && lid.Name == id.Name {
										text += lits(as.Rhs[i])
									}
								}
							}
							return true
						})
						return true
					})
				}
				if !lookWords.MatchString(text) || gated[fn.Name.Name] {
					return true
				}
				if words, ok := oneOff[fn.Name.Name]; ok && strings.Contains(text, words) {
					return true
				}
				t.Errorf("%s: %s says a failed look (%q) on its first miss", at, fn.Name.Name, text)
				return true
			})
		}
	}
	if n < 12 {
		t.Fatalf("found only %d direct alerts: the walk is broken", n)
	}
}

// 11-05 to 11-14: 19 "owner-click watch failed: cmux tree: exit status 1: Error: Command timed out" ALERTs, each a run
// that failed once while the next minute's run worked. A failed clicks or prompts run is posted on its second run in a
// row, then once per error while it lasts; a run that works starts the count again. The error is given back every time,
// so the job's own log still shows each failure.
func TestAFailedClicksOrPromptsRunIsPostedOnItsSecondRunInARow(t *testing.T) {
	home := setup(t, nil)
	cmux := failedLook(errors.New("cmux tree: exit status 1: Error: Command timed out"))
	step := func(i int, err error, alerts int) {
		t.Helper()
		got := alertOnce(home, t0.Add(time.Duration(i)*time.Minute), "owner-clicks.error", "owner-click watch failed: ", err)
		if !errors.Is(got, err) {
			t.Fatalf("run %d: gave %v, want %v back", i, got, err)
		}
		if n := len(linesOf(t, home, "ALERT")); n != alerts {
			t.Fatalf("run %d: %d ALERT lines, want %d: %q", i, n, alerts, linesOf(t, home, "ALERT"))
		}
	}
	step(0, cmux, 0) // one failed run says nothing yet
	step(1, nil, 0)  // a good run starts the count again
	step(2, cmux, 0)
	step(3, cmux, 1) // the second failed run in a row is posted
	step(4, cmux, 1) // the same error once
	// review P1-2: an error that is not a failed look (a post or a write, not tried again next run) is posted at once
	step(5, nil, 1)
	step(6, errors.Join(failedLook(errors.New("cmux tree: timed out")), errors.New("bus append: disk full")), 2)
	step(7, nil, 2)
	step(8, errors.New("state/owner-clicks.json: read-only file system"), 3)
	step(9, nil, 3)
	// review P1-1: a look whose count cannot be saved is posted at once, or a count that never grows holds it for ever
	if err := os.WriteFile(filepath.Join(home, "state", "owner-clicks.error.misses"), nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(home, "state", "owner-clicks.error.misses"), 0o400); err != nil {
		t.Fatal(err)
	}
	got := alertOnce(home, t0.Add(10*time.Minute), "owner-clicks.error", "owner-click watch failed: ", cmux)
	if a := linesOf(t, home, "ALERT"); len(a) != 4 || !strings.Contains(a[3], "count of failed runs was not saved") || got == nil {
		t.Fatalf("a look whose count cannot be saved: gave %v, ALERTs %q", got, a)
	}
}

func TestOnlyLooksHoldsJoinedLooksAlone(t *testing.T) {
	look, other := failedLook(errors.New("cmux: timed out")), errors.New("write: disk full")
	for _, c := range []struct {
		err  error
		want bool
	}{
		{look, true},
		{errors.Join(look, nil, failedLook(errors.New("t3: no answer"))), true},
		{errors.Join(look, other), false},
		{other, false},
		{fmt.Errorf("wrapped: %w", look), false}, // a wrap may add a failure of its own: said at once
	} {
		if got := c.err != nil && onlyLooks(c.err); got != c.want {
			t.Errorf("onlyLooks(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// The 19 live cases end to end: the owner-click watch's cmux read timing out once posts nothing; twice in a row posts.
func TestAClicksRunWhoseTabReadTimesOutOncePostsNothing(t *testing.T) {
	home := setup(t, nil)
	run := func(min int) error {
		return Clicks(ClickDeps{Home: home, Now: t0.Add(time.Duration(min) * time.Minute), Screens: func() ([]Screen, error) {
			return nil, errors.New("cmux tree: exit status 1: Error: Command timed out")
		}})
	}
	if err := run(0); err == nil {
		t.Fatal("a failed tab read gave no error: the job's log would not show it")
	}
	if a := linesOf(t, home, "ALERT"); len(a) != 0 {
		t.Fatalf("one timed-out tab read posted: %q", a)
	}
	run(1)
	if a := linesOf(t, home, "ALERT"); len(a) != 1 || !strings.Contains(a[0], "Command timed out") {
		t.Fatalf("two in a row: want one ALERT, got %q", a)
	}
}

// Review r2 P2-A: a paid-usage look keyed by pid never reached 2 for short-lived claudes (night builds' claude -p), so
// the money check's failure stayed unsaid. Keyed by cause, one claude failing on each of two runs is said.
func TestAPaidLookFailingForAnotherShortLivedClaudeEachRunIsSaid(t *testing.T) {
	home := setup(t, nil)
	denied := errors.New("sysctl kern.procargs2: operation not permitted")
	f := &fake{claudes: []Claude{{PID: 301, Err: denied}}}
	runOnce(t, home, f, t0)
	f.claudes = []Claude{{PID: 302, Err: denied}}
	runOnce(t, home, f, t0.Add(5*time.Minute))
	if all := strings.Join(f.sent, "\n"); strings.Count(all, "the paid-usage check failed to look") != 1 || !strings.Contains(all, "claude pid 302") {
		t.Fatalf("want one failed paid look, naming pid 302: %q", f.sent)
	}
}

// Review r2 P2-B: the held count lives in Run's state; a state that cannot be saved starts every count at 1 again, so a
// held look would never be said. Run then says what it held, on the bus, with why.
func TestARunThatCannotSaveItsStateSaysTheLooksItHolds(t *testing.T) {
	home := setup(t, nil)
	state := filepath.Join(home, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(state, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(state, 0o700) })
	f := &fake{resErr: errors.New("sysctl: exit status 1")}
	if err := Run(f.deps(home, t0)); err == nil {
		t.Fatal("a state that cannot be saved gave no error")
	}
	if a := linesOf(t, home, "ALERT"); len(a) != 1 || !strings.Contains(a[0], "could not save its state (open ") || !strings.Contains(a[0], "never be said: ") {
		t.Fatalf("want one ALERT naming the held look: %q", a)
	}
}
