#!/bin/bash
# usage: pretrust.sh DIR — accept claude's folder-trust dialog for DIR once, so later sessions under it start without one
set -u
D=$(cd "$1" && pwd -P) || exit 1
j() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
WS=$(herdr workspace create --label "pretrust-$$" --no-focus --cwd "$D" | j 'd["result"]["workspace"]["workspace_id"]') || exit 1
trap 'herdr workspace close "$WS" > /dev/null 2>&1' EXIT
N="pretrust-$$"
herdr agent start "$N" --kind claude --pane "$WS:p1" -- claude --model haiku > /dev/null 2>&1
for _ in $(seq 120); do
  scr=$(herdr agent read "$N" --source visible 2>/dev/null | tr -d ' \n')
  if echo "$scr" | grep -q 'Yes,Itrustthisfolder'; then herdr agent send-keys "$N" down enter > /dev/null; sleep 2; echo "trusted $D"; exit 0; fi
  st=$(herdr agent get "$N" 2>/dev/null | j 'd["result"]["agent"]["agent_status"]' 2>/dev/null)
  [ "$st" = idle ] && { echo "already trusted $D"; exit 0; }
  sleep 0.5
done
echo "pretrust: no trust dialog or idle state for $D"; exit 1
