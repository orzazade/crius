#!/bin/sh
# Demo of crius-spawn -move-check with the fake t3 next to this script: a move of a thread whose last reply ends on ⏳
# is denied, a move of an idle thread that ends on ✅ is let through. Run from anywhere:
#   sh cmd/crius-spawn/testdata/demo-move-check.sh
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
(cd "$here/.." && GOTOOLCHAIN=local go build -o "$tmp/crius-spawn" .)
mkdir "$tmp/home"
for id in th-wait th-done; do
  in='{"tool_name":"mcp__t3-code__t3_thread_configure","tool_input":{"threadId":"'$id'","modelSelection":{"instanceId":"claude-max2","model":"claude-opus-5-5","provider":"claude","options":{}}}}'
  out=$(printf '%s' "$in" | CRIUS_HOME="$tmp/home" PATH="$here/bin:$PATH" "$tmp/crius-spawn" -move-check)
  echo "move $id: ${out:-(nothing printed: allowed)}"
done
