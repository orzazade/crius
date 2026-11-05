package main

import (
	"strings"
	"testing"
)

// Review of bf78438 (Opus, 11-05 23:46), P1-a: in find, a read command at -exec took the rest of the line as its own
// words, so a later -exec part went unseen. P1-b: a bare $X in an az string that a shell or Perl runs is filled in
// before az sees it, so it may be the command words.
func TestEveryFindExecPartIsJudged(t *testing.T) {
	dir := scriptDir(t, map[string]string{"post.sh": "#!/bin/bash\naz repos pr create --title t -d hi\n"})
	for _, cmd := range []string{
		`find . -name x -exec /usr/bin/grep -l foo {} \; -exec az repos pr create --title t \;`,
		`find . -name x -exec grep -l foo {} \; -exec az repos pr create --title t \;`,
		`find . -exec /usr/bin/printf %s {} \; -exec az pipelines run --name p \;`,
		`find . -exec /usr/bin/test -f {} \; -exec az boards work-item create --title t --type Bug \;`,
		`find . -name x -exec /usr/bin/grep -l foo {} + -exec /bin/sh -c "az repos pr create --title t" \;`,
		`find . -exec /bin/cat {} \; -exec /bin/bash ./post.sh {} +`,
		`find . -exec cat {} \; -exec bash ./post.sh {} +`,
		`find . -exec az repos pr show --id 1 \; -exec az repos pr create --title t \;`,
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("%s\n  passed, want an ask", cmd)
		}
	}
	for _, cmd := range []string{
		`find . -name '*.md' -exec /usr/bin/grep -l foo {} \; -exec wc -l {} +`,
		`find . -exec /usr/bin/grep -l foo {} + -print`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%s\n  asked: %s", cmd, why)
		}
	}
}

func TestADollarWordInAShellRunAzStringAsks(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{
		`python3 -c 'import os; os.system("az $SUB --title " + t)'`,
		`python3 -c 'import subprocess; subprocess.run("az $SUB " + x, shell=True)'`,
		`node -e 'require("child_process").execSync("az $A --title " + t)'`,
		`perl -e 'system("az $g " . $rest)'`,
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("%s\n  passed, want an ask", cmd)
		}
	}
}
