#!/bin/bash
# Demo: crius-spawn -fresh against a fake t3, in a throwaway Crius home (no real thread, account or T3 server is used).
#   hang  (default) the fake t3 never answers: -fresh fails loud after the 30 s per-call limit
#   ghost the fake t3 lists a background job no claude runs (the 11-21 axi:ms-flow case): -fresh fails loud at once
# Run from the repo root: cmd/crius-spawn/testdata/fresh-hang-demo.sh [hang|ghost]
set -u
mode=${1:-hang}
home=$(mktemp -d "${TMPDIR:-/tmp}/fresh-demo.XXXXXX")
mkdir -p "$home/bin" "$home/roles" "$home/sessions" "$home/cfg/sessions"
case $mode in
hang) printf '#!/bin/sh\nexec sleep 3600\n' >"$home/bin/t3" ;;
ghost)
	cat >"$home/bin/t3" <<'EOF'
#!/bin/sh
echo '{"thread":{"threadId":"th-demo","status":"completed","activeRunId":null,"pendingRequestCount":0,"pendingBackgroundTasks":[{"taskId":"bq41","kind":"command","description":"Run the ms-flow suite"}]},"messages":[{"role":"assistant","text":"suite started ⏳"}]}'
EOF
	;;
*) echo "usage: $0 [hang|ghost]" >&2; exit 2 ;;
esac
chmod +x "$home/bin/t3"
row='{"session_id":"s-demo","project":"axi","role":"ms-flow","t3_thread":"th-demo","config":"'"$home/cfg"'","time":"2026-11-21T15:20:00Z"}'
echo "$row" >"$home/roles/axi.ms-flow.json"
echo "$row" >"$home/sessions/s-demo.json"
go build -o "$home/crius-spawn" ./cmd/crius-spawn || exit 1
start=$(date +%s)
CRIUS_HOME=$home "$home/crius-spawn" -fresh -backend t3 axi ms-flow "demo prompt"
rc=$?
echo "exit code $rc after $(($(date +%s) - start)) s (demo home $home)"
