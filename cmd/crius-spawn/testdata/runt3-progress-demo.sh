#!/bin/bash
# Demo: crius-spawn -backend t3 against a fake t3, in a throwaway Crius home (no real thread, account or T3 server is
# used; never ~/.crius). The fake thread shows "starting" for its first 20 reads, then "running", and crius-hook never
# files its session, so the start-up check says what it waits on when that changes and once a minute, and fails loud at
# its 3 min limit (launch.StartLimit) with exit code 1.
# Run from the repo root: cmd/crius-spawn/testdata/runt3-progress-demo.sh
set -u
home=$(mktemp -d "${TMPDIR:-/tmp}/runt3-demo.XXXXXX")
mkdir -p "$home/bin" "$home/state" "$home/sessions" "$home/lab" "$home/max2/projects" "$home/t3/userdata"
cat >"$home/projects.toml" <<EOF
[transcripts]
personal = "$home/max2/projects"

[projects.lab]
persona = "personal"
accounts = ["max"]
cwd = ["$home/lab"]
EOF
cat >"$home/accounts.toml" <<EOF
[[account]]
name = "max2"
plan = "max"
dirs = ["$home/max2"]
EOF
echo '{"oauthAccount":{"accountUuid":"demo-only"}}' >"$home/max2/.claude.json"
echo '{"providerInstances":{"claude-max2":{"driver":"claudeAgent","config":{"homePath":"'"$home/max2"'"}}}}' >"$home/t3/userdata/settings.json"
cat >"$home/bin/t3" <<'EOF'
#!/bin/sh
# fake t3: `thread start` makes mcp:th-demo; `thread show` says starting for 20 reads, then running
reads="$(dirname "$0")/reads"
case "$2" in
start) cat >/dev/null; echo '{"threadId":"mcp:th-demo"}' ;;
show)
	n=$(($(cat "$reads" 2>/dev/null || echo 0) + 1)); echo "$n" >"$reads"
	status=starting; [ "$n" -gt 20 ] && status=running
	echo '{"thread":{"status":"'$status'","providerInstanceId":"claude-max2","runCount":0,"pendingRequestCount":0},"messages":[]}' ;;
*) echo "fake t3: unexpected $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$home/bin/t3"
# without the fake, crius-spawn would run the real t3 on PATH and start a real thread
[ -x "$home/bin/t3" ] && [ -f "$home/t3/userdata/settings.json" ] || { echo "demo set-up failed in $home" >&2; exit 1; }
go build -o "$home/crius-spawn" ./cmd/crius-spawn || exit 1
start=$(date +%s)
CRIUS_HOME=$home T3CODE_HOME=$home/t3 "$home/crius-spawn" -backend t3 lab probe "demo prompt"
rc=$?
echo "exit code $rc after $(($(date +%s) - start)) s (demo home $home)"
