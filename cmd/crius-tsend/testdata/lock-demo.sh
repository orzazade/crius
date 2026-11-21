#!/bin/sh
# lock-demo.sh: crius-tsend from "the Mac" and bus-post's wake line on "the box" type into one pane at the same time,
# and every line arrives whole, because both hold the box's typing lock.
# Run from anywhere: sh cmd/crius-tsend/testdata/lock-demo.sh [old]
# "old" builds both tools from 7f391e0 (before the fix: crius-tsend locked the Mac's file) to show the clash.
# It uses a tmux server of its own (its own socket folder) with a fake Claude Code box in window demo:builder, a fake ssh
# that runs each line on this machine with the "box" home, and throwaway Crius homes; it never reaches a real box, tmux
# window or bus. Exits 0 only when the pane got every line whole and once, else 1.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
tmp=$(mktemp -d /tmp/tl-demo.XXXXXX) # short: a tmux socket path is cut at about 100 bytes
export TMUX_TMPDIR="$tmp"
unset TMUX T3CODE_THREAD_ID CRIUS_HOME
trap 'tmux kill-server 2>/dev/null || true; rm -rf "$tmp"' EXIT INT TERM

src=$root
if [ "${1:-}" = old ]; then
	src=$tmp/old
	mkdir -p "$src"
	git -C "$root" archive 7f391e0 | tar -x -C "$src"
fi
box=$tmp/box # the box's ~
mac=$tmp/mac # the Mac's Crius home
mkdir -p "$box/.crius/bin" "$box/.crius/bus" "$box/.crius/roles" "$box/.crius/runs" "$box/.crius/state" "$mac/state/playbook"
(cd "$src" && GOTOOLCHAIN=local go build -o "$tmp/crius-tsend" ./cmd/crius-tsend &&
	GOTOOLCHAIN=local go build -o "$box/.crius/bin/bus-post" ./cmd/bus-post)
(cd "$root" && GOTOOLCHAIN=local go build -o "$tmp/fakeprompt" ./internal/tabsend/fakeprompt/run)

# The fake ssh: the line is its last word; it runs here as on the box (HOME is the box's ~, no CRIUS_HOME).
cat >"$tmp/ssh" <<EOF
#!/bin/sh
for a; do line=\$a; done
unset CRIUS_HOME
HOME="$box" exec /bin/sh -c "\$line"
EOF
chmod +x "$tmp/ssh"
echo "[{\"name\":\"demobox\",\"target\":\"u@h\",\"ssh\":[\"$tmp/ssh\"]}]" >"$mac/state/playbook/hosts.json"
: >"$box/.crius/bus/demo.log"
echo '{"session_id":"s-demo","project":"demo","role":"builder"}' >"$box/.crius/roles/demo.builder.json"

tmux new-session -d -x 220 -y 80 -s demo -n builder "$tmp/fakeprompt"
i=0
until tmux capture-pane -p -t demo:builder | /usr/bin/grep -q 'fix lint errors'; do
	i=$((i + 1))
	[ $i -lt 100 ] || { echo "demo: the fake box never showed" >&2; exit 1; }
	sleep 0.05
done

rounds=4
n=1
while [ $n -le $rounds ]; do
	echo "--- round $n: crius-tsend and bus-post start at once"
	CRIUS_HOME=$mac "$tmp/crius-tsend" demobox demo:builder "crius-tsend line $n from the mac" >"$tmp/t$n" 2>&1 &
	t=$!
	CRIUS_HOME=$box/.crius "$box/.crius/bin/bus-post" "$box/.crius/bus/demo.log" DECISION crius:lead demo:builder \
		"bus-post wake line $n" >"$tmp/b$n" 2>&1 &
	b=$!
	rt=0
	wait $t || rt=$?
	rb=0
	wait $b || rb=$?
	echo "crius-tsend exit $rt: $(cat "$tmp/t$n")"
	echo "bus-post exit $rb: $(/usr/bin/grep -h 'demo:builder' "$tmp/b$n" | head -1)"
	n=$((n + 1))
done

echo "--- window demo:builder now shows:"
tmux capture-pane -p -t demo:builder | sed '/^[[:space:]]*$/d' | tee "$tmp/pane"

bad=0
n=1
while [ $n -le $rounds ]; do
	for want in "crius-tsend line $n from the mac" "bus-post wake line $n (on the bus "; do
		c=$(/usr/bin/grep -cF -- "$want" "$tmp/pane" || true)
		[ "$c" -eq 1 ] || { echo "demo: \"$want\" shows $c times, want once" >&2; bad=1; }
	done
	n=$((n + 1))
done
# a whole line is one sent draft that holds exactly one of the texts
mixed=$(/usr/bin/grep -E 'line [0-9]' "$tmp/pane" | /usr/bin/grep -E 'crius-tsend line.*wake line|wake line.*crius-tsend line|line [0-9]+.*line [0-9]+' || true)
[ -z "$mixed" ] || { echo "demo: mixed lines: $mixed" >&2; bad=1; }
if [ $bad -eq 0 ]; then
	echo "demo: PASS (every line whole and once)"
	exit 0
fi
echo "demo: FAIL" >&2
exit 1
