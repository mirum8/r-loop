#!/bin/bash
# usage: pretrust.sh DIR — accept claude's folder-trust dialog for DIR once, so later sessions under it start without one
set -u
. "$(dirname "$0")/host.sh"
D=$(cd "$1" && pwd -P) || exit 1
WSJ=$(ws_open "pretrust-$$" "$D") || exit 1
WS=${WSJ% *}
trap 'ws_close "$WS" > /dev/null 2>&1' EXIT
N="pretrust-$$"
agent_start "$N" "${WSJ#* }" claude claude --model haiku > /dev/null 2>&1
for _ in $(seq 120); do
  scr=$(screen "$N" 2>/dev/null | tr -d ' \n')
  if echo "$scr" | grep -q 'Yes,Itrustthisfolder'; then keys "$N" down enter > /dev/null; sleep 2; echo "trusted $D"; exit 0; fi
  st=$(agent_state "$N")
  [ "$st" = idle ] && { echo "already trusted $D"; exit 0; }
  sleep 0.5
done
echo "pretrust: no trust dialog or idle state for $D"; exit 1
