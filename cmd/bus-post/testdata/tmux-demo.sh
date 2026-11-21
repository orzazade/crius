#!/bin/sh
# tmux-demo.sh: bus-post types a line for a role in a local tmux window into that window, as crius-tsend does.
# Run from anywhere: sh cmd/bus-post/testdata/tmux-demo.sh
# It uses a tmux server of its own (its own socket folder) with a fake Claude Code box in window demo:builder, and a
# throwaway Crius home; it never reaches a real tmux window or bus. Exits 0 only when bus-post says "delivered to
# demo:builder (tmux <this machine>)" and the window shows the line, else 1.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d /tmp/bp-demo.XXXXXX) # short: a tmux socket path is cut at about 100 bytes
export TMUX_TMPDIR="$tmp"
unset TMUX T3CODE_THREAD_ID
trap 'tmux kill-server 2>/dev/null || true; rm -rf "$tmp"' EXIT INT TERM

(cd "$root" && GOTOOLCHAIN=local go build -o "$tmp/bus-post" ./cmd/bus-post &&
	GOTOOLCHAIN=local go build -o "$tmp/fakeprompt" ./internal/tabsend/fakeprompt/run)

home=$tmp/home
mkdir -p "$home/bus" "$home/roles" "$home/runs" "$home/state"
: >"$home/bus/demo.log"
# As crius-hook files a role outside cmux and T3 Code (the ThinkPad's staffy.builder.json): no tab, no thread.
echo '{"session_id":"s-demo","project":"demo","role":"builder"}' >"$home/roles/demo.builder.json"

tmux new-session -d -x 200 -y 20 -s demo -n builder "$tmp/fakeprompt"
i=0
until tmux capture-pane -p -t demo:builder | /usr/bin/grep -q 'fix lint errors'; do
	i=$((i + 1))
	[ $i -lt 100 ] || { echo "demo: the fake box never showed" >&2; exit 1; }
	sleep 0.05
done

echo "\$ bus-post $home/bus/demo.log DECISION crius:lead demo:builder \"go on with S-12\""
rc=0
"$tmp/bus-post" "$home/bus/demo.log" DECISION crius:lead demo:builder "go on with S-12" 2>"$tmp/said" || rc=$?
cat "$tmp/said"
echo "exit $rc"
echo "--- window demo:builder now shows:"
tmux capture-pane -p -t demo:builder | sed '/^[[:space:]]*$/d'

if [ "$rc" -eq 0 ] && /usr/bin/grep -q 'delivered to demo:builder (tmux ' "$tmp/said" &&
	tmux capture-pane -p -t demo:builder | /usr/bin/grep -q 'go on with S-12'; then
	echo "demo: PASS"
	exit 0
fi
echo "demo: FAIL" >&2
exit 1
