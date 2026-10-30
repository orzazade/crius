package main

import (
	"cmp"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestScanRealTree measures the guard on real folders: for every doc it asks whether the guard would have refused it
// had it been created today, after the older docs of its folder (the oldest doc of a topic is never refused). It runs
// only when folders are named after -args; CRIUS_DOCGUARD_OUT names the report file, CRIUS_DOCGUARD_NOALLOW=1
// ignores seriesDirs and CRIUS_DOCGUARD_FIRSTWORD=1 also counts a shared first word.
//
//	CRIUS_DOCGUARD_OUT=/tmp/scan.txt go test -count=1 -run ScanRealTree ./cmd/crius-doc-guard/ -args ~/Projects/scifi ~/.crius
func TestScanRealTree(t *testing.T) {
	roots := flag.Args()
	if len(roots) == 0 {
		t.Skip("name the folders to scan after -args")
	}
	noAllow, firstWord := os.Getenv("CRIUS_DOCGUARD_NOALLOW") == "1", os.Getenv("CRIUS_DOCGUARD_FIRSTWORD") == "1"
	type doc struct {
		path, stem string
		mtime      int64
	}
	byDir := map[string][]doc{}
	nDocs := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				t.Logf("cannot read %s: %v", p, err)
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case "node_modules", ".git", "bin", "obj":
					return filepath.SkipDir
				}
				if strings.HasSuffix(p, "/.claude/worktrees") { // copies of the repo they sit in
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !isDoc(p) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			nDocs++
			byDir[filepath.Dir(p)] = append(byDir[filepath.Dir(p)], doc{p, topicStem(d.Name()), info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	type hit struct{ path, same string }
	var hits []hit
	perDir := map[string]int{}
	for dir, docs := range byDir {
		if !noAllow && seriesDir(dir) {
			continue
		}
		slices.SortFunc(docs, func(a, b doc) int { return cmp.Or(cmp.Compare(a.mtime, b.mtime), cmp.Compare(a.path, b.path)) })
		for i, d := range docs {
			if d.stem == "" {
				continue
			}
			for _, old := range docs[:i] {
				if old.stem != "" && sameStem(old.stem, d.stem) || firstWord && old.stem != "" && first(old.stem) == first(d.stem) {
					hits = append(hits, hit{d.path, old.path})
					perDir[dir]++
					break
				}
			}
		}
	}
	dirs := make([]string, 0, len(perDir))
	for d := range perDir {
		dirs = append(dirs, d)
	}
	slices.SortFunc(dirs, func(a, b string) int { return cmp.Or(perDir[b]-perDir[a], strings.Compare(a, b)) })
	var b strings.Builder
	fmt.Fprintf(&b, "docs %d, folders %d, would deny %d in %d folders (noallow=%v firstword=%v)\n",
		nDocs, len(byDir), len(hits), len(perDir), noAllow, firstWord)
	for _, d := range dirs {
		fmt.Fprintf(&b, "DIR %4d %s\n", perDir[d], d)
	}
	slices.SortFunc(hits, func(a, b hit) int { return strings.Compare(a.path, b.path) })
	for _, h := range hits {
		fmt.Fprintf(&b, "HIT %s <= %s\n", h.path, filepath.Base(h.same))
	}
	if out := os.Getenv("CRIUS_DOCGUARD_OUT"); out != "" {
		if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Log(strings.SplitN(b.String(), "\n", 2)[0])
}

func first(stem string) string { return strings.SplitN(stem, "-", 2)[0] }
