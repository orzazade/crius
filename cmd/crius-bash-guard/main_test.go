package main

import (
	"strings"
	"testing"
)

func TestADetachedTeamWaitIsRefused(t *testing.T) {
	for _, cmd := range []string{
		// 10-21 22:44: the waiter ran detached and never woke the session.
		"~/.claude/skills/cmux-team/scripts/team-wait.sh crius 900 &",
		"team-wait.sh crius 600 > /tmp/w.log 2>&1 &",
		"cd ~/x && bash ~/.claude/skills/cmux-team/scripts/team-wait.sh crius 600 &",
		"nohup team-wait.sh crius 600 & echo started",
	} {
		if why := check(cmd); !strings.Contains(why, "run_in_background") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestATeamWaitInTheForegroundIsAllowed(t *testing.T) {
	for _, cmd := range []string{
		"~/.claude/skills/cmux-team/scripts/team-wait.sh crius 900",
		"team-wait.sh crius 600 && echo woke",
		"sleep 1 & team-wait.sh crius 600",
		"grep -n 'team-wait.sh' notes.md &> /dev/null",
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

func TestARawVerifiedWriteToTheBusIsRefused(t *testing.T) {
	for _, cmd := range []string{
		// 10-20 and 10-21: VERIFIED lines echoed straight onto the bus, before the run and with a UTC time.
		`echo "2026-10-21T19:02 VERIFIED crius:lead -> quotes:lead — F-118 · ok" >> ~/.crius/bus/quotes.log`,
		`printf '%s VERIFIED crius:lead -> x — y\n' "$(date +%FT%H:%M)" | tee -a $HOME/.crius/bus/crius.log`,
		"cat >> /Users/o/.crius/bus/crius.log <<EOF\n2026-10-21T19:02 VERIFIED crius:lead -> x — y\nEOF",
		`cd /tmp; echo "VERIFIED crius:lead -> x — y" > ~/.crius/bus/new.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		} else if strings.Contains(why, `no "(cc`) { // bus-post takes "a (cc b)" since 10-22
			t.Errorf("%q: the refusal still says a cc is not allowed: %q", cmd, why)
		}
	}
}

// 10-22: 332 lines of quotes.log that ingest and the digest cannot read, all echoed straight onto the bus (the quotes
// brief says to): a sender with no project, a "(cc ...)" in the recipients, a type that is not a bus type. bus-post
// parses each line back before it writes, so every line goes through it.
func TestAnyRawWriteToTheBusIsRefused(t *testing.T) {
	for _, cmd := range []string{
		`echo "$(date +%Y-%m-%dT%H:%M) PASS QA2 -> quotes:lead — 17:57 P2 stream/s" >> ~/.crius/bus/quotes.log`,
		`echo "$(date +%Y-%m-%dT%H:%M) ORDER quotes:lead -> quotes:p6 (cc quotes:p3) — x" >> ~/.crius/bus/quotes.log`,
		`echo "$(date +%FT%H:%M) INFO crius:build -> crius:lead — x" >> ~/.crius/bus/crius.log`,
		`printf '%s\n' "$line" >> $HOME/.crius/bus/quotes.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		} else if strings.Contains(why, `no "(cc`) { // bus-post takes "a (cc b)" since 10-22
			t.Errorf("%q: the refusal still says a cc is not allowed: %q", cmd, why)
		}
	}
}

func TestOtherBusUseIsAllowed(t *testing.T) {
	for _, cmd := range []string{
		`~/.crius/bin/bus-post ~/.crius/bus/crius.log VERIFIED crius:lead quotes:lead "F-118 · ~/.crius/runs/x.log"`,
		`grep VERIFIED ~/.crius/bus/crius.log > /tmp/v.txt`,
		`tail -5 ~/.crius/bus/crius.log`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 1 (b93c71f, 4782e51): ways past the guard.
func TestReviewBypassesAreRefused(t *testing.T) {
	v := "2026-10-21T23:00 VERIFIED crius:lead -> quotes:lead — F-1 · unchecked"
	for _, c := range []struct{ cmd, want string }{
		{"team-wait.sh crius 900 & echo started\nif", "run_in_background"},
		{"printf '%s\\n' '" + v + "' | tee -a ~/.crius/bus/quotes.log\nif", "bus-post"},
		{"printf '%s\\n' '" + v + "' | /usr/bin/tee -a ~/.crius/bus/quotes.log", "bus-post"},
		{"printf '%s\\n' '" + v + "' | command tee -a ~/.crius/bus/quotes.log", "bus-post"},
		{"printf '%s\\n' '" + v + "' | env \"tee\" -a ~/.crius/bus/quotes.log", "bus-post"},
		{"line='" + v + "'; echo \"$line\" >> ~/.crius/bus/crius.log", "bus-post"},
		{"cd ~/.crius/bus && echo '" + v + "' >> crius.log", "bus-post"},
		{"bash -c '~/.claude/skills/cmux-team/scripts/team-wait.sh crius 600 &'", "run_in_background"},
		{"sh -c \"echo '" + v + "' >> ~/.crius/bus/crius.log\"", "bus-post"},
	} {
		if why := check(c.cmd); !strings.Contains(why, c.want) {
			t.Errorf("%q: %q", c.cmd, why)
		}
	}
}

// Review round 1: normal work the guard must not block.
func TestReviewFalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		"grep -R -n 'team-wait.sh' docs > /tmp/waiter-references.txt &",
		`~/.crius/bin/bus-post ~/.crius/bus/crius.log INFO crius:build crius:lead "waiting for VERIFIED on F-1"`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 2 (46dbb90).
func TestReviewRound2BypassesAreRefused(t *testing.T) {
	v := "2026-10-21T23:00 VERIFIED crius:lead -> quotes:lead — F-1"
	for _, c := range []struct{ cmd, want string }{
		{`printf '%s VERIFIED %s -> %s — %s\n' "$(date +%FT%H:%M)" crius:lead quotes:lead F-1 >> ~/.crius/bus/quotes.log`, "bus-post"},
		{`echo "$(date +%FT%H:%M) VERIFIED $me -> quotes:lead — F-1" >> ~/.crius/bus/quotes.log`, "bus-post"},
		{"bash -c '~/.claude/skills/cmux-team/scripts/team-wait.sh crius 600' &", "run_in_background"},
		{"nohup bash -c 'team-wait.sh crius 600' > /tmp/w.log 2>&1 &", "run_in_background"},
		{"bash -lc \"echo '" + v + "' >> ~/.crius/bus/crius.log\"", "bus-post"},
		{"bash -lc 'team-wait.sh crius 600 &'", "run_in_background"},
		{`bash -c "echo \"$(date +%FT%H:%M) VERIFIED crius:lead -> x:y — z\" >> ~/.crius/bus/crius.log"`, "bus-post"},
		{"LOG=~/.crius/bus/quotes.log; echo '" + v + "' >> \"$LOG\"", "bus-post"},
		{"timeout 900 team-wait.sh crius 600 &", "run_in_background"},
		{"cd ~/.crius/bus && echo '" + v + "' &>> crius.log", "bus-post"},
	} {
		if why := check(c.cmd); !strings.Contains(why, c.want) {
			t.Errorf("%q: %q", c.cmd, why)
		}
	}
}

func TestReviewRound2FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`grep -c 'VERIFIED crius:lead -> quotes' ~/.crius/bus/quotes.log; ~/.crius/bin/bus-post ~/.crius/bus/quotes.log INFO crius:build crius:lead ok`,
		`cd ~/.crius/bus && grep -n 'VERIFIED crius:lead ->' crius.log > /tmp/v.txt`,
		"grep -n 'VERIFIED crius:lead ->' ~/.crius/bus/crius.log; if",
		"bash", "sh -c",
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 3 (10-22, the any-raw-write guard): reads of the bus that write elsewhere.
func TestReviewRound3FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`grep -c VERIFIED ~/.crius/bus/crius.log > "$TMPDIR/v.txt"`,
		`grep VERIFIED ~/.crius/bus/crius.log > $HOME/v.txt`,
		`tail -3 ~/.crius/bus/crius.log 2> "$err"`,
		`wc -l ~/.crius/bus/*.log > /tmp/n-$(date +%s).txt`,
		`for f in ~/.crius/bus/*.log; do tail -1 "$f" >> "$out"; done`,
		`cd ~/.crius/bus && ls > /tmp/x; cd ~/Projects/x && make > build.log`,
		`cd ~/.crius/bus && cp crius.log /tmp/c.log`,
		`cp ~/.crius/bus/crius.log /tmp/c.log`,
		`sed -n 1p ~/.crius/bus/crius.log > /tmp/first.txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 3: ways past the any-raw-write guard.
func TestReviewRound3BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`eval "echo x >> ~/.crius/bus/crius.log"`,
		`ls | xargs -I{} sh -c 'echo {} >> ~/.crius/bus/crius.log'`,
		`c='echo x >> ~/.crius/bus/crius.log'; sh -c "$c"`,
		`cd ~/.crius && echo x >> bus/crius.log`,
		`echo x >> ~/.crius//bus/crius.log`,
		`echo x >> ~/.crius/./bus/crius.log`,
		`B=~/.crius; echo x >> $B/bus/crius.log`,
		`echo x 1<>~/.crius/bus/crius.log`,
		`cp /tmp/x ~/.crius/bus/crius.log`,
		`mv /tmp/x ~/.crius/bus/crius.log`,
		`sed -i '' '$d' ~/.crius/bus/crius.log`,
		`perl -pi -e 's/a/b/' ~/.crius/bus/crius.log`,
		`dd if=/tmp/x of=$HOME/.crius/bus/crius.log`,
		`for f in ~/.crius/bus/*.log; do echo x >> "$f"; done`,
		`echo $(echo x >> ~/.crius/bus/crius.log)`,
		`export LOG=~/.crius/bus/crius.log; echo x >> "$LOG"`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 4 (10-22): a path the command computes ($(...), read, $1, an array) is unknown, and the command names
// the bus; a shell reading its script from a heredoc, a here-string or a pipe; cd options; bash's long options.
func TestReviewRound4BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`L=$(ls -d ~/.crius/bus/crius.log); echo "VERIFIED a:b -> c" >> "$L"`,
		`echo x >> "$(ls ~/.crius/bus/crius.log)"`,
		"echo x >> `echo ~/.crius/bus/crius.log`",
		`ls ~/.crius/bus/*.log | while read f; do echo x >> "$f"; done`,
		`set -- ~/.crius/bus/q.log; echo x >> $1`,
		`a=(~/.crius/bus/q.log); echo x >> ${a[0]}`,
		"bash <<EOF\necho x >> ~/.crius/bus/q.log\nEOF",
		`echo 'echo x >> ~/.crius/bus/q.log' | bash`,
		`bash <<<'echo x >> ~/.crius/bus/q.log'`,
		`bash -c -- 'echo x >> ~/.crius/bus/q.log'`,
		`bash --norc -c 'echo x >> ~/.crius/bus/q.log'`,
		`cd -P ~/.crius/bus; echo x >> q.log`,
		`cd -- ~/.crius/bus; echo x >> q.log`,
		`cd "$(dirname ~/.crius/bus/q.log)"; echo x >> q.log`,
		`sed -i -e 's/a/b/' ~/.crius/bus/q.log`,
		`sed -i '' 's/a/b/' ~/.crius/bus/q.log`,
		`perl -pi -e 's/a/b/' ~/.crius/bus/q.log`,
		`L=~/.crius; L+=/bus/q.log; echo x >> $L`,
		"cat <<EOF\n$(echo x >> ~/.crius/bus/q.log)\nEOF",
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 4: the script of sed or perl -i is not a file it writes, and a folder that only starts with "bus" is not
// the bus.
func TestReviewRound4FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`cd ~/.crius/bus && perl -pi -e 's/a/b/' /tmp/x`,
		`cd ~/.crius/bus && sed -i '' s/a/b/ /tmp/x`,
		`sed -i '' 's|~/.crius/bus|BUS|' /tmp/notes.md`,
		`echo x >> ~/x/.crius/busy.txt`,
		`bash <<EOF` + "\ntail -3 ~/.crius/bus/crius.log > /tmp/t.txt\nEOF",
		`L=$(ls -d /tmp/x); echo x >> "$L"`,
		`bash ~/.crius/bin/bus-post ~/.crius/bus/crius.log INFO crius:build crius:lead "x"`,
		"F=$(ls -t ~/.crius/state/crius/HANDOFF-*.md | head -1); cat >> \"$F\" <<EOF\nx\nEOF",
		`tail ~/.crius/bus/crius.log | bash -c 'wc -l > /tmp/n.txt'`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound5BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`echo x >& ~/.crius/bus/q.log`,
		`echo x >> ~/\.crius/bus/q.log`,
		`echo x | \tee -a ~/.crius/bus/q.log`,
		`echo x >> ~/$(echo .crius/bus/q.log)`,
		`echo x >> "$HOME/$(printf .crius/bus/q.log)"`,
		`cd ~/.crius && L=$(ls -d bus/q.log) && echo x >> "$L"`,
		`cd ~/.crius/bus; cd /tmp; cd -; echo x >> q.log`,
		`pushd ~/.crius/bus; pushd /tmp; popd; echo x >> q.log`,
		"cp /tmp/x ~/.crius/bus/q.log\nif",
		`set -- ~/.crius/bus/q.log; for f; do echo x >> "$f"; done`,
		`echo x >> "${D:-$HOME/.crius}/bus/q.log"`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q passed: %q", cmd, why)
		}
	}
}

func TestReviewRound5FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`out=$(mktemp); grep x ~/.crius/bus/q.log > "$out"`,
		`tail ~/.crius/bus/q.log > "$(mktemp)"`,
		`tmp=$(mktemp -d); cp ~/.crius/bus/q.log "$tmp/"`,
		`tail ~/.crius/bus/q.log >&2`,
		`tail ~/.crius/bus/q.log >&- 2>&1`,
		`cat ~/.crius/bus/q.log > "${TMPDIR:-/tmp}/y.txt"`,
		`grep '\.' ~/.crius/bus/q.log > /tmp/dots.txt`,
		`tail ~/.crius/bus/q.log > ~/.crius/state/$(date +%s).txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 6 (10-22): a computed path after a cd was taken as a child of the cd's folder; ${X:=d} kept no X; an
// unquoted here-document lost the backslash of \", which bash keeps there.
func TestReviewRound6BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`cd ~/Projects/x && L=$(ls ~/.crius/bus/crius.log) && echo x >> "$L"`,
		`cd /tmp && ls ~/.crius/bus/*.log | while read f; do echo x >> "$f"; done`,
		`cd ~/Projects/x && echo x >> "$(ls ~/.crius/bus/crius.log)"`,
		`cd ~/Projects/x; set -- ~/.crius/bus/q.log; echo x >> $1`,
		`cd ~/Projects/x && cd "$(dirname ~/.crius/bus/q.log)" && echo x >> q.log`,
		`: "${LOG:=$HOME/.crius/bus/q.log}"; echo x >> "$LOG"`,
		`: "${LOG=$HOME/.crius/bus/q.log}"; echo x >> "$LOG"`,
		"bash <<EOF\necho \\\"a > ~/.crius/bus/q.log\\\"\nEOF",
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 7 (10-22): a variable set in a ( ... ) or $( ... ) was kept after it; a script text with a computed
// part was read as known; a ".." after a computed part was judged by the folder before it; $(mktemp) with no template
// was taken as /tmp/ when the command set TMPDIR.
func TestReviewRound7BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`F=~/.crius/bus/q.log; (F=/tmp/x); echo x >> "$F"`,
		`F=~/.crius/bus/q.log; : "$(F=/tmp/x)"; echo x >> "$F"`,
		`bash -c "cd ~/.crius/bus; $(cat /tmp/s.sh)"`,
		`echo x >> ~/Projects/$(ls ~/Projects/x)/../../.crius/bus/q.log`,
		`export TMPDIR=~/.crius/bus; F=$(mktemp); echo x >> "$F"`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound8BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`echo x >> ~/.crius/$(echo bus)/../bus/q.log`,
		`F=~/.crius/bus/q.log; F=/tmp/x true; echo x >> "$F"`,
		`F=~/.crius/bus/q.log; F=/tmp/x | true; echo x >> "$F"`,
		`cd ~/.crius/bus; cd /tmp | true; echo x >> q.log`,
		`F=~/.crius/bus/q.log; F=/tmp/x & echo x >> "$F"`,
		`cd ~/.crius/$(echo bus/x)/..; echo x >> q.log`,
		`[[ -n $(echo x >> ~/.crius/bus/q.log) ]]`,
		`(( $(echo x >> ~/.crius/bus/q.log) ))`,
		`let x=$(echo x >> ~/.crius/bus/q.log)`,
		`case $(echo x >> ~/.crius/bus/q.log) in *) ;; esac`,
		`export $(echo x >> ~/.crius/bus/q.log)`,
		`mv -t ~/.crius/bus /tmp/q.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound8FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`mv -t /tmp/old ~/.crius/bus/old.log`,
		`F=/tmp/x true; echo x >> "$F"`,
		`cd /tmp; grep x ~/.crius/bus/q.log | sort > out.txt`,
		`grep x ~/.crius/bus/q.log & echo x > /tmp/o.txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound9BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`cd ~/Projects/$(true)/.. && echo x >> .crius/bus/q.log`,
		`cd ~/Projects/$(true)/.. && cd .crius/bus && echo x >> q.log`,
		`cd /tmp/$(true)/../.. && echo x >> ` + strings.TrimPrefix(homeDir, "/") + `/.crius/bus/q.log`,
		`echo x >> /tmp/$(printf ../Users/me)/.crius/bus/q.log`,
		`for ((i=$(echo x >> ~/.crius/bus/q.log; echo 0); i<1; i++)); do :; done`,
		`mv --target-directory=$HOME/.crius/bus /tmp/q.log`,
		`mv -t$HOME/.crius/bus /tmp/q.log`,
		`export $(echo D=$HOME/.crius/bus); echo x >> $D/q.log`,
		`env D=~/.crius/bus sh -c 'echo x >> $D/q.log'`,
		`env D=$HOME/.crius/bus sh -c 'echo x >> $D/q.log'`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound9FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`mv --target-directory=/tmp/old ~/.crius/bus/old.log`,
		`export $(cat /tmp/env); grep x ~/.crius/bus/q.log > /tmp/out.txt`,
		`env LC_ALL=C grep x ~/.crius/bus/q.log > /tmp/o.txt`,
		`env D=/tmp sh -c 'grep x ~/.crius/bus/q.log > $D/o.txt'`,
		`for ((i=0; i<3; i++)); do grep x ~/.crius/bus/q.log; done > /tmp/o.txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound10BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`D=/tmp; export $(echo D=$HOME/.crius/bus); echo x >> $D/q.log`,
		`D=/tmp; declare "$(echo D=$HOME/.crius/bus)"; echo x >> $D/q.log`,
		`printf -v "$(echo D)" %s ~/.crius/bus; echo x >> "$D/q.log"`,
		`read "$(echo D)" <<< ~/.crius/bus; echo x >> $D/q.log`,
		`cd "$(echo ~)/.crius" && echo x >> bus/q.log`,
		`cd "$(echo ~)"; cd .crius; cd bus; echo x >> q.log`,
		`T=$(mktemp -d); echo x >> $T/../../Users/me/.crius/bus/q.log`,
		`env -u X sh -c "echo x >> ~/.crius/bus/q.log"`,
		`env -C ~/.crius/bus tee -a q.log`,
		`env --chdir=$HOME/.crius/bus tee -a q.log`,
		`env -S "sh -c 'echo x >> ~/.crius/bus/q.log'"`,
		`exec -a x sh -c "echo x >> ~/.crius/bus/q.log"`,
		`timeout -s KILL 5 sh -c "echo x >> ~/.crius/bus/q.log"`,
		`cp -at ~/.crius/bus /tmp/x.log`,
		`mv -ft ~/.crius/bus /tmp/x.log`,
		`gcp --target-dir ~/.crius/bus /tmp/x.log`,
		`cp --target=$HOME/.crius/bus /tmp/x.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound10FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`T=$(mktemp -d); mkdir -p $T/.crius/bus; echo x > $T/.crius/bus/q.log`,
		`T=$(mktemp -d); cd $T; mkdir -p .crius/bus; echo x > .crius/bus/q.log`,
		`cp -- -tx /tmp/a.log ~/.crius/bus/old.log /tmp/out/`,
		`install -m 644 ~/.crius/bus/q.log /tmp/q.log`,
		`timeout -k 5 60 grep x ~/.crius/bus/q.log > /tmp/o.txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 10 (2): the mktemp marker let a second computed part climb out, could be typed, and held when mktemp
// printed nothing; wrapper options with a value were still read as the command.
func TestReviewRound10bBypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`T=$(mktemp -d); echo x >> "$T/$(printf ../..)$HOME/.crius/bus/q.log"`,
		`T=$(mktemp -d); X=$(cat f); echo x >> "$T/$X/.crius/bus/q.log"`,
		`T=$(mktemp -d); cd "$T/$(printf ../..)$HOME"; echo x >> .crius/bus/q.log`,
		`mkdir -p '/tmp/§new§'; echo x >> /tmp/'§new§'$(printf /../..$HOME)/.crius/bus/q.log`,
		`cd ~ && T=$(mktemp -d /nonexistent/XXXXXX 2>/dev/null); echo x >> "$T".crius/bus/q.log`,
		`env -P /usr/bin tee -a ~/.crius/bus/q.log`,
		`sudo --prompt x tee -a ~/.crius/bus/q.log`,
		`sudo -c class tee -a ~/.crius/bus/q.log`,
		`sudo -a type tee -a ~/.crius/bus/q.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Review round 10 (2): an option of declare, local, export or readonly names no variable.
func TestReviewRound10bFalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`B=/tmp/o; declare -a A; echo x > $B/f; tail ~/.crius/bus/q.log`,
		`B=/tmp/o; local -i n=0; echo x > $B/f; tail ~/.crius/bus/q.log`,
		`B=/tmp/o; export -n Z; echo x > $B/f; tail ~/.crius/bus/q.log`,
		`B=/tmp/o; declare -A M=([a]=1); echo x > $B/f; tail ~/.crius/bus/q.log`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound6FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`out=$(mktemp /tmp/bus.XXXXXX); grep x ~/.crius/bus/q.log > "$out"`,
		`out=$(mktemp "${TMPDIR:-/tmp}/v.XXXX"); grep x ~/.crius/bus/q.log > "$out"`,
		`out=$(mktemp 2>/dev/null); grep x ~/.crius/bus/q.log > "$out"`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	// A template in the bus folder is no temp file.
	if why := check(`out=$(mktemp ~/.crius/bus/q.XXXX); echo x >> "$out"`); !strings.Contains(why, "bus-post") {
		t.Errorf("mktemp in the bus folder: %q", why)
	}
}

// Review round 11: GNU long options take any unique prefix (timeout --sig); export "D=..." sets D; a ".." out of a
// mktemp folder is a known path.
func TestReviewRound11BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`timeout --sig KILL 5 tee -a ~/.crius/bus/q.log`,
		`sudo --us me tee -a ~/.crius/bus/q.log`,
		`env --uns X tee -a ~/.crius/bus/q.log`,
		`env --chd=$HOME/.crius/bus tee -a q.log`,
		`env --split-s "sh -c 'echo x >> ~/.crius/bus/q.log'"`,
		`xargs --delimiter x tee -a ~/.crius/bus/q.log`,
		`export "D=$HOME/.crius"; echo x >> $D/bus/q.log`,
		`T=$(mktemp -d ~/.crius/state/XXXX); echo x >> $T/../../bus/q.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound11FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`T=$(mktemp -d); echo x > $T/a/../.crius/bus/q.log`,
		`export "B=/tmp/o"; echo x > $B/f; tail ~/.crius/bus/q.log`,
		`timeout --fore 5 grep x ~/.crius/bus/q.log > /tmp/o.txt`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound12BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`set -- x "B=$HOME/.crius/bus"; export "D=$@"; echo hi >> "$B/q.log"`,
		`a=(x "B=$HOME/.crius/bus"); export "D=${a[@]}"; echo hi >> "$B/q.log"`,
		`sudo --login tee -a ~/.crius/bus/q.log <<< x`,
		`/usr/bin/env tee -a ~/.crius/bus/q.log <<< x`,
		`/usr/bin/timeout --sig KILL 5 tee -a ~/.crius/bus/q.log <<< x`,
		`echo x >> ~/.CRIUS/bus/q.log`,
		`echo x >> ~/.crius/BUS/q.log`,
		`cd ~/.Crius && echo x >> bus/q.log`,
		`T=$(mktemp -d ~/.Crius/state/XXXX); echo x >> $T/../../bus/q.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound12FalseRefusalsPass(t *testing.T) {
	for _, cmd := range []string{
		`export "D=$*"; echo x > /tmp/o; tail ~/.crius/bus/q.log`,
		`/usr/bin/env FOO=1 grep x ~/.crius/bus/q.log > /tmp/o.txt`,
		`echo x > ~/.crius/Busy/f; tail ~/.CRIUS/bus/q.log`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound13BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`set -- x "B=$HOME/.crius/bus"; export "D=${X:-$@}"; echo hi >> "$B/q.log"`,
		`a=(x "B=$HOME/.crius/bus"); export "D=${X:-${a[@]}}"; echo hi >> "$B/q.log"`,
		`set -- x "B=$HOME/.crius/bus"; declare "D=${X+$@}"; echo hi >> "$B/q.log"`,
		`TEE -a ~/.crius/bus/q.log <<< x`,
		`/usr/bin/TEE -a ~/.crius/bus/q.log <<< x`,
		`ENV tee -a ~/.crius/bus/q.log <<< x`,
		`CP /tmp/x ~/.crius/bus/q.log`,
		`SED -i '' s/a/b/ ~/.crius/bus/q.log`,
		`gtimeout 5 tee -a ~/.crius/bus/q.log <<< x`,
		`genv tee -a ~/.crius/bus/q.log <<< x`,
		`/usr/bin/time tee -a ~/.crius/bus/q.log <<< x`,
		`echo x >> $(echo ~/.criuſ/buſ)/q.log`,
		`echo x >> ~/.criuſ/buſ/q.log`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	if why := check(`TEAM-WAIT.SH &`); why == "" {
		t.Errorf("a waiter in capitals in the background passes")
	}
}

func TestReviewRound14BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`cd ~/.crius/bus; CD /tmp; echo x >> q.log`,
		`cd ~/.crius/bus; /usr/bin/cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus && genv cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus && env cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus && timeout 5 cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus && nohup popd; echo x >> q.log`,
		`cd /tmp; builtin cd ~/.crius/bus; echo x >> q.log`,
		`genv -a x tee -a ~/.crius/bus/q.log <<< x`,
		`genv --argv0 x tee -a ~/.crius/bus/q.log <<< x`,
		`gtimeout inf tee -a ~/.crius/bus/q.log <<< x`,
		`timeout .5 tee -a ~/.crius/bus/q.log <<< x`,
		`timeout 1e2 tee -a ~/.crius/bus/q.log <<< x`,
		`timeout -- 5 tee -a ~/.crius/bus/q.log <<< x`,
		`export "D="{/tmp,"$HOME/.crius/bus"}; echo x >> "$D/q.log"`,
		`a=(x "$HOME/.crius/bus"); export "D=/tmp/x$a[@]"; echo x >> "$D/q.log"`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{ // the shell's own cd still moves it
		`cd ~/.crius/bus; \cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus; command cd /tmp; echo x >> q.log`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestReviewRound15BypassesAreRefused(t *testing.T) {
	for _, cmd := range []string{
		`bash -c 'export D={/tmp,$HOME/.crius/bus}; echo x >> "$D/q.log"'`,
		`declare D={/tmp,~/.crius/bus}; echo x >> "$D/q.log"`,
		`cd ~/.crius/bus; command -v cd /tmp; echo x >> q.log`,
		`cd ~/.crius/bus; command -pV cd /tmp; echo x >> q.log`,
		`env D=/Users/x/.crius/bus/env sh -c 'echo x >> "${D%/env}/q.log"'`,
		`env D=/Users/x/.crius/bus/nohup sh -c 'echo x >> "$D"'`,
	} {
		if why := check(cmd); !strings.Contains(why, "bus-post") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	// bash expands no braces in a plain assignment: D is the text as written
	if why := check(`D={/tmp,~/.crius/bus}; echo x >> "/tmp/$D.log"`); why != "" {
		t.Errorf("a plain assignment with braces: %q", why)
	}
}

// Crius 10-22 21:25: a review agent ran a bare zsh `local`, which printed every variable with its value (a PAT and
// cmux tokens among them) into the transcript and so to the API. A command that prints all variables is refused.
func TestACommandThatPrintsEveryVariableIsRefused(t *testing.T) {
	for _, cmd := range []string{
		"local", "env", "set", "printenv", "export", "export -p", "declare", "declare -p", "declare -px", "typeset",
		"typeset -x", "readonly", "local -p", "printenv -0", "env -0", "env -u PATH", "env -i X=1", "/usr/bin/env",
		"command env", "sudo -u o env", "nohup env > /tmp/e.txt", "env | grep -i token", "set | head -50",
		"builtin declare -p", "command export -p", "cd /tmp && env", "( set -o posix; set )", `bash -c 'printenv'`,
		`zsh -c "local"`, "echo $(env)", "f() { env; }; f", "if true; then export; fi", "ENV", `"env"`,
	} {
		if why := check(cmd); !strings.Contains(why, "every variable") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestCommandsThatPrintNoValuesPass(t *testing.T) {
	for _, cmd := range []string{
		"env go test ./...", "env -u PATH ls", "env X=1 go vet ./...", "printenv HOME", "export X=1", "export X",
		"declare -p X", "local x=1", "typeset -i n=2", "declare -f", "declare -F", "typeset -f", "set -o", "set +o",
		"set -e", "set -- a b", "readonly X=1", "command -v env", "which env", "echo env", "grep env file.go",
		"cut -d= -f1 <<< x", "man env", "go doc os.Environ", "/usr/bin/env bash script.sh", "timeout 5 env true",
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review of the rule (10-22): zsh syntax the parser cannot read switched the check off, even in the form the refusal
// recommends; env -S and env -C hid the command after them; local - and typeset + print no values.
func TestReviewOfTheVariableRuleCatchesZshAndEnvFlags(t *testing.T) {
	for _, cmd := range []string{
		"env\necho ${(k)parameters}", "local; echo ${(j:,:)arr}", "ls **/*.go(.); env", "cat =(env)",
		"echo ${(k)parameters} && typeset -p", "env -S printenv", "env -C /tmp printenv",
		// second review: the zsh path missed what the walker catches, and the hides branch did not unwrap again
		"echo ${(k)parameters}; env 2>&1", "echo ${(k)parameters}; if true; then env; fi", "echo ${(k)parameters}; sudo env",
		"echo ${(k)parameters}; printenv -0", "echo ${(k)parameters}; ENV", "echo ${(k)parameters}; ! env",
		"echo ${=x}; time env", "env -C /tmp env", "sudo -D /tmp env", "env -C /tmp nohup printenv",
		"env -C /tmp command export -p",
		// third review: a qualifier is no $(...), and env -C in env -C is unwrapped again
		"print ${(k)x}; echo *$(printenv)", "env -C /tmp env -C /tmp printenv",
		`print ${(k)x}; echo '${(' ; set ; echo ')'`, // set runs in zsh too: the quote ends before it
	} {
		if why := check(cmd); !strings.Contains(why, "every variable") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		"f() { local -; set -e; }", "typeset +", "print -l ${(k)parameters}", "echo ${(k)parameters} | wc -l",
		"setopt extended_glob; ls **/*.go(.)", "print -l ${(k)parameters} | grep -i env",
		// second review: quoted text in a command the parser cannot read is no command
		"print -l ${(k)parameters} | grep -iE 'token|env|key'", "print -l ${(k)parameters} | grep -E '(path|env)'",
		`zsh -c 'echo ${(k)parameters} | grep -E "(env|path)"'`, `ls **/*.go(.) && git commit -m "docs: explain (env) handling"`,
		"echo ${(k)parameters}; echo 'run: (local) is fine'", "echo ${(k)parameters}; cat <<'EOF'\nset\n| env |\nEOF",
		// third review: the rewrite keeps quotes
		"print ${(k)x}; echo 'a*(.); set'", `print ${(k)x}; echo "${(k)y} env"`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Crius 10-23: a cmux send with no target types into the caller's own tab. Twice: a bare number taken for the tab
// (cmux send 3 "..."), and zsh, which does not split $S, gave "--workspace w --surface s" as one word, so an ORDER
// landed in Crius's own tab. send and send-key need --workspace and --surface as literal words, each with a value.
func TestACmuxSendWithoutALiteralTargetIsRefused(t *testing.T) {
	for _, cmd := range []string{
		`cmux send 3 "ORDER: do it"`,
		`cmux send-key enter`,
		`S="--workspace workspace:2 --surface surface:5"; cmux send $S "ORDER: do it"`,
		`cmux send "$S" "ORDER: do it"`,
		`cmux send ${=S} "ORDER: do it"`,
		`cmux send --workspace workspace:2 "text"`,
		`cmux send --surface surface:5 "text"`,
		`cmux send-key --workspace workspace:2 --surface`,
		`cmux send --workspace=workspace:2 --surface=surface:5 "text"`,
		`cmux send -- --workspace w --surface s`,
		`/opt/homebrew/bin/cmux send 3 hi`, `env X=1 cmux send hi`, `bash -c 'cmux send-key enter'`,
		`sleep 1 && cmux send hi`, `for w in 1 2; do cmux send $w hi; done`,
		// review of a648553: a global option's value was taken for the subcommand
		`cmux --socket /tmp/cmux.sock send-key enter`, `cmux --password p --id-format both send hi`,
		`cmux --json send hi`, `cmux --new-global x send hi`,
	} {
		if why := check(cmd); !strings.Contains(why, "--workspace") || !strings.Contains(why, "own tab") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	enter := func(w, s string) string { return `; cmux send-key --workspace ` + w + ` --surface ` + s + ` Enter` }
	for _, cmd := range []string{
		`cmux send --workspace workspace:2 --surface surface:5 "ORDER: do it"` + enter("workspace:2", "surface:5"),
		`cmux send --workspace "$W" --surface "$S" "text"` + enter(`"$W"`, `"$S"`),
		`cmux send --surface $S --workspace $W --paste -- "text"` + enter("$W", "$S"),
		`cmux send-key --workspace workspace:2 --surface surface:5 enter`,
		`cmux read-screen --surface surface:5`, `cmux list-panels`, `cmux send-panel --panel 3 hi`,
		`echo cmux send 3 hi`, `grep -n "cmux send" notes.md`,
		`cmux --socket /tmp/cmux.sock send --workspace w --surface s hi` + enter("w", "s"), `cmux --id-format refs notify --title send`,
		`cmux notify --title send --body x`,
		// review of 84c4a2e: a global option that takes no value ends no scan early
		`cmux --json rename-workspace send`, `cmux --json notify --title send --body x`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// axi:leader 10-30 21:49 (Crius NEED 22:10): text typed with cmux send and no Enter after it sat unsent in a tab from
// 18:16 to 21:45. A send needs a cmux send-key ... Enter to the same surface after it, in the same command.
func TestACmuxSendWithNoEnterToTheSameSurfaceIsRefused(t *testing.T) {
	to := func(s string) string { return ` --workspace workspace:2 --surface ` + s + ` ` }
	for _, cmd := range []string{
		`cmux send` + to("surface:5") + `"ORDER: do it"`,
		`cmux send` + to("surface:5") + `hi; cmux send-key` + to("surface:6") + `Enter`,
		`cmux send-key` + to("surface:5") + `Enter; cmux send` + to("surface:5") + `hi`,
		`cmux send` + to("surface:5") + `hi && cmux send-key` + to("surface:5") + `Escape`,
		`cmux send` + to("surface:5") + `a; cmux send` + to("surface:6") + `b; cmux send-key` + to("surface:5") + `Enter`,
		`bash -c 'cmux send` + to("surface:5") + `hi'`,
		`cmux --socket /tmp/c.sock send` + to("surface:5") + `hi`,
		// review of a45a137: the Enter goes to the same surface in another workspace; an Enter in the middle of the text
		`cmux send` + to("surface:5") + `hi; cmux send-key --workspace workspace:3 --surface surface:5 Enter`,
		`cmux send` + to("surface:5") + `'hi\nthere'`,
		// review of 65c6aab: a command bash cannot parse (zsh forms), a help word after the text
		`cmux send` + to("surface:5") + `hi; echo ${(e)x}`, `setopt glob_subst; cmux send` + to("surface:5") + `hi; print ${=x}`,
		`bash -c 'cmux send` + to("surface:5") + `${=x}'`,
		`cmux send` + to("surface:5") + `hi -h`,
		`cmux send` + to("$(a)") + `hi; cmux send-key` + to("$(b)") + `Enter`, // two computed surfaces are not one
	} {
		if why := check(cmd); !strings.Contains(why, "send-key") || !strings.Contains(why, "unsent") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		`cmux send` + to("surface:5") + `"ORDER: do it" && cmux send-key` + to("surface:5") + `Enter`,
		`cmux send` + to("surface:5") + `hi; cmux send-key` + to("surface:5") + `enter`,
		`cmux send` + to("surface:5") + `hi; cmux send-key` + to("surface:5") + `Return`,
		`cmux send` + to(`"$S"`) + `hi; sleep 0.3; cmux send-key` + to(`"$S"`) + `Enter`,
		`bash -c 'cmux send` + to("surface:5") + `hi' && cmux send-key` + to("surface:5") + `Enter`,
		`cmux send` + to("surface:5") + `a; cmux send` + to("surface:6") + `b; cmux send-key` + to("surface:6") +
			`Enter; cmux send-key` + to("surface:5") + `Enter`,
		// review of a45a137: text that ends in Enter (cmux: "\n and \r send Enter"); the same surface quoted one way and
		// written plain the other
		`cmux send` + to("surface:5") + `'hi\n'`, `cmux send` + to("surface:5") + `$'hi\n'`,
		`cmux send` + to("surface:5") + `'hi\r'`, `cmux send` + to("surface:5") + `"hi` + "\n" + `"`,
		`cmux send` + to(`"surface:5"`) + `hi; cmux send-key` + to("surface:5") + `Enter`,
		`cmux send --workspace 'workspace:2' --surface surface:5 hi; cmux send-key` + to("surface:5") + `Enter`,
		// review of 65c6aab: a target from a variable the command set, quoted one way and plain the other
		`W=workspace:2; S=surface:5; cmux send --workspace $W --surface $S hi; cmux send-key` + to("surface:5") + `Enter`,
		`cmux send --workspace "$W" --surface "$S" hi; cmux send-key --workspace $W --surface $S Enter`,
		// help types nothing (it was refused as an untargeted send)
		`cmux send --help`, `cmux send-key --help`, `cmux send -h`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Crius 10-29 03:2x: a for loop with zsh's set -- ${=pair} writing run logs, then bus-post, was refused as a bus write:
// the parser cannot read ${=x}, and the text rule refuses any > in a command that names the bus. Read as bash, the
// walker judges it. A form that runs code or reads another variable ((e), (P), a glob's e: or + qualifier) keeps the
// text rule.
func TestAZshSplitIsReadAsBash(t *testing.T) {
	runLogs := "for pair in \"56ad6de item1\" \"058480d item2\"; do set -- ${=pair}; cat > ~/.crius/runs/crius-build-$1-$2.log <<EOF2\n" +
		"sha: $1\nEOF2\ndone\n~/.crius/bin/bus-post ~/.crius/bus/crius.log VERIFIED crius:lead crius:build \"56ad6de · x\""
	for _, cmd := range []string{runLogs, "echo ${=x} > /tmp/a; cat ~/.crius/bus/crius.log", "ls **/*.go(.) > /tmp/l; cat ~/.crius/bus/x.log",
		"echo ${(f)x} ${(j:,:)y} > /tmp/a; cat ~/.crius/bus/crius.log"} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	for _, cmd := range []string{
		"for p in a; do set -- ${=p}; echo s > ~/.crius/bus/crius.log; done", "x=~/.crius/bus/crius.log; echo s > ${=x}",
		"echo ${(e)x} > /tmp/a; cat ~/.crius/bus/crius.log", "echo ${(P)x} > /tmp/a; cat ~/.crius/bus/crius.log",
		"echo ${=y}; ls *(+f) > /tmp/a; cat ~/.crius/bus/crius.log", "echo ${=y}; ls *(e:f:) > /tmp/a; cat ~/.crius/bus/crius.log",
		// Flags that do math or prompt expansion, and ${~x}, which globs the value with its qualifiers, run code too
		// (review of 2f86353: each wrote the bus in zsh).
		`typeset -a a; x='a[$(echo pwn >> ~/.crius/bus/crius.log)]'; echo ${(#)x}`,
		`typeset -a a; n='a[$(echo pwn >> ~/.crius/bus/crius.log)]'; x=ab; echo ${(l:n:)x}`,
		`x='$(echo pwn >> ~/.crius/bus/crius.log)'; echo ${(%%)x}`,
		`x='*(e:"echo pwn >> ~/.crius/bus/crius.log":)'; echo ${~x}`,
		// With glob_subst on, ${=x} globs the value too (second review of 29ca7fd); emulate sh turns it on.
		`setopt GLOB_SUBST; x='*(e:"echo pwn >> ~/.crius/bus/crius.log":)'; print ${=x}`,
		`emulate sh; x='*(e:"echo pwn >> ~/.crius/bus/crius.log":)'; print ${=x}`,
		`setopt -m 'glob*'; x='*(e:"echo pwn >> ~/.crius/bus/crius.log":)'; print ${=x}`,
		`zsh -o glob_subst -c "x='*(e:\"echo pwn >> ~/.crius/bus/crius.log\":)'; print \${=x}"`,
		// A shell the command starts may run with any options (third review: zsh started as sh has glob_subst on).
		`ARGV0=sh zsh -c "setopt BARE_GLOB_QUAL; unsetopt SH_GLOB; x='*(e:\"echo>>~/.crius/bus/crius.log\":)'; print \${=x}"`,
	} {
		if why := check(cmd); why != rawWrite {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// Crius 10-30 20:22: the marketplace backend ran a docker compose build of the whole AXI stack with no heavy slot: the
// Docker VM held 11 cores, docker and az stopped answering. An image build runs through heavy.ts with HEAVY_SLOTS=1,
// which waits until no heavy job holds a slot, so two builds never run at once.
func TestADockerBuildOutsideOneHeavySlotIsRefused(t *testing.T) {
	const heavy = "bun ~/Projects/Appxite/svc_quote/channels/poc/ui-app/scripts/heavy.ts run -- "
	for _, cmd := range []string{
		`cd ~/Projects/Appxite/AXI && docker compose up --build 2>&1 | tail -20`,
		`docker compose up -d --build`, `docker compose -f a.yml -p axi --profile all up -d --build api`,
		`docker compose build`, `docker compose --project-directory x build api`, `docker-compose up --build`,
		`docker-compose -f a.yml build`, `docker compose run --build api npm test`, `docker compose create --build`,
		`docker build -t x .`, `docker --context colima build .`, `docker -H unix:///x.sock build .`,
		`docker buildx build --platform linux/amd64 -t x .`, `docker buildx --builder b bake`, `docker image build .`,
		`docker builder build .`, `sudo docker build .`, `timeout 900 docker compose build`, `/usr/local/bin/docker build .`,
		`bash -c 'docker build .'`, `for s in a b; do docker build -t $s .; done`,
		heavy + `docker build .`, `HEAVY_SLOTS=2 ` + heavy + `docker compose up --build`,
		// review of cc9ab86: a HEAVY_SLOTS=1 no export gives reaches no program; values and forms read wrongly
		`HEAVY_SLOTS=1; ` + heavy + `docker build .`, `HEAVY_SLOTS=1 && ` + heavy + `docker build .`,
		`docker compose -f "$F" up -d --build`, `docker compose run -e A=1 --build api`, `docker buildx b .`,
		`docker-compose -H tcp://h build`, `docker-compose --log-level INFO build`, `docker compose run --build api df -h`,
		// review of 833ddf2: a glued option value is an option; set -a exports only what is set after it, until set +a
		`docker compose --file="$F" up -d --build`, `docker compose --env-file="$E" build`, `docker compose -p"$P" up --build`,
		`docker --context="$CTX" build .`, `HEAVY_SLOTS=1; set -a; ` + heavy + `docker build .`,
		`set -a; set +a; HEAVY_SLOTS=1; ` + heavy + `docker build .`, `set -- -a; HEAVY_SLOTS=1; ` + heavy + `docker build .`,
		`set -a; bash -c 'HEAVY_SLOTS=1; ` + heavy + `docker build .'`,
	} {
		if why := check(cmd); !strings.Contains(why, "HEAVY_SLOTS=1") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		`HEAVY_SLOTS=1 ` + heavy + `docker compose up -d --build`, `env HEAVY_SLOTS=1 ` + heavy + `docker build .`,
		`export HEAVY_SLOTS=1; ` + heavy + `docker build -t x .`,
		`HEAVY_SLOTS=1 bun scripts/heavy.ts run -- timeout 900 docker compose build`,
		`docker compose up -d`, `docker compose up -d --no-build`, `docker compose ps`, `docker compose logs build`,
		`docker logs build`, `docker run img build`, `docker exec c npm run build`, `docker ps`, `docker images`,
		`docker buildx ls`, `docker image ls`, `docker compose down`, `echo docker build .`, `grep -n "docker build" x.md`,
		heavy + `npm test`,
		// review of cc9ab86: other programs' builds in a slot, help and dry runs, an option's value
		heavy + `dotnet build -c Release`, heavy + `go build ./...`, heavy + `cargo build`, heavy + `make build`,
		heavy + `bun build ./src`, `docker build --help`, `docker image build --help`, `docker compose build --help`,
		`docker buildx bake --print`, `docker compose up --build --dry-run`, `docker compose --workdir build ps`,
		`HEAVY_SLOTS=1; export HEAVY_SLOTS; ` + heavy + `docker build .`, `docker compose run api make --build`,
		// review of 51bbeea: the variable reaches a child shell and other export forms; a computed subcommand
		`HEAVY_SLOTS=1 bash -c '` + heavy + `docker build .'`, `env HEAVY_SLOTS=1 sh -c '` + heavy + `docker build .'`,
		"HEAVY_SLOTS=1 bash <<'EOF'\n" + heavy + "docker build .\nEOF", `declare -x HEAVY_SLOTS=1; ` + heavy + `docker build .`,
		`typeset -x HEAVY_SLOTS=1; ` + heavy + `docker build .`, `set -a; HEAVY_SLOTS=1; ` + heavy + `docker build .`,
		`export "HEAVY_SLOTS=1"; ` + heavy + `docker build .`, `C=logs; docker $C build`, `docker "$cmd" compose up --build`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}
