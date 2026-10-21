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
		}
	}
}

func TestOtherBusUseIsAllowed(t *testing.T) {
	for _, cmd := range []string{
		`~/.crius/bin/bus-post ~/.crius/bus/crius.log VERIFIED crius:lead quotes:lead "F-118 · ~/.crius/runs/x.log"`,
		`grep VERIFIED ~/.crius/bus/crius.log > /tmp/v.txt`,
		`tail -5 ~/.crius/bus/crius.log`,
		`echo "$(date +%FT%H:%M) INFO crius:build -> crius:lead — x" >> ~/.crius/bus/crius.log`,
	} {
		if why := check(cmd); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}
