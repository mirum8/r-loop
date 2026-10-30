#!/usr/bin/env bash
# usage: live_workspace_close.sh
#   docs/plan/todo-tiny.md, --unattended, in its own workspace, TUI at 120x40.
#   A step's workspace closes as soon as the step finishes ok: the plan workspace is recorded closed before the
#   implement step starts and is gone from herdr while implement runs; the implement workspace is closed before
#   the landing; nothing is closed twice and no close warns. The phase lands and ticks, q quits, terminal restored.
set -u
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
. "$(dirname "$0")/host.sh"
host_up || { echo "NOT RUN: no session host (herdr or herdr-app)"; exit 3; }

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-1800}"
OUT="${OUT:-$HERE/frames/workspace-close-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT"
[ "$HOST" != herdr ] || export HERDR_SOCKET_PATH="${HERDR_SOCKET_PATH:-${XDG_CONFIG_HOME:-$HOME/.config}/herdr/herdr.sock}"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-wsclose}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

WSJ=$(ws_open workspace-close "$S") || { echo "FAIL workspace create"; exit 1; }
WS=${WSJ% *}
PANE=${WSJ#* }
echo "$HOST   $WS $PANE"
H=$("$TUI" start --geometry 120x40 -- env $(host_env "$PANE") "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; ws_close "$WS" > /dev/null 2>&1; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; ws_close "$WS" > /dev/null 2>&1' EXIT
echo "handle  $H"

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select(.Kind==\"event\" and ($1))" "$f" 2>/dev/null; }
stepws() { q ".Event.Kind==\"step\" and .Event.Step==\"$1\" and .Event.Fields.workspace!=null and .Event.Fields.workspace!=\"\"" | jq -r '.Event.Fields.workspace' | head -1; }
alive() { ws_alive "$1"; }

start=$(date +%s)
final="" seen_impl=0 plan_alive_during_impl=0
while :; do
  sleep 5
  wsp=$(stepws plan) wsi=$(stepws implement)
  if [ -n "$wsp" ] && [ -n "$wsi" ] && alive "$wsi"; then
    seen_impl=1
    alive "$wsp" && plan_alive_during_impl=1
    [ -f "$OUT/frame-implement.txt" ] || cp "$("$TUI" capture "$H")" "$OUT/frame-implement.txt"
  fi
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then final=$(head -1 "$OUT/status.txt"); break; fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"
F=$(EV)
cp "$F" "$OUT/events.jsonl"
git log --oneline > "$OUT/git-log.txt"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
WSP=$(stepws plan) WSI=$(stepws implement)
[ -n "$WSP" ] && [ -n "$WSI" ] && [ "$WSP" != "$WSI" ] && ok "plan $WSP, implement $WSI" || fail "step workspaces plan=${WSP:-none} implement=${WSI:-none}"

cp_plan=$(jq -c 'select(.Kind=="event") | .Event' "$F" | grep -n "\"Kind\":\"workspace-closed\"" | grep -F "\"$WSP\"" | head -1 | cut -d: -f1)
cp_impl=$(jq -c 'select(.Kind=="event") | .Event' "$F" | grep -n "\"Kind\":\"workspace-closed\"" | grep -F "\"$WSI\"" | head -1 | cut -d: -f1)
first_impl=$(jq -c 'select(.Kind=="event") | .Event' "$F" | grep -n '"Kind":"step"' | grep -F '"Step":"implement"' | head -1 | cut -d: -f1)
landed=$(jq -c 'select(.Kind=="event") | .Event' "$F" | grep -n '"Kind":"landed"' | head -1 | cut -d: -f1)
echo "        event index: plan-closed=${cp_plan:-?} implement-first=${first_impl:-?} implement-closed=${cp_impl:-?} landed=${landed:-?}"
[ -n "$cp_plan" ] && [ -n "$first_impl" ] && [ "$cp_plan" -lt "$first_impl" ] && ok "plan workspace closed before implement started" || fail "plan close not before implement"
[ -n "$cp_impl" ] && [ -n "$landed" ] && [ "$cp_impl" -lt "$landed" ] && ok "implement workspace closed before landing" || fail "implement close not before landed"
[ "$seen_impl" = 1 ] && { [ "$plan_alive_during_impl" = 0 ] && ok "plan workspace gone from herdr while implement ran" || fail "plan workspace still in herdr while implement ran"; } || echo "SKIP herdr poll never caught implement live"
dups=$(q '.Event.Kind=="workspace-closed"' | jq -r '.Event.Fields.workspace' | sort | uniq -d)
[ -z "$dups" ] && ok "no workspace closed twice" || fail "closed twice: $dups"
w=$(q '.Event.Kind=="warning" and (.Event.Fields.reason|test("close workspace"))' | head -1)
[ -z "$w" ] && ok "no close warnings" || fail "close warning: $w"
for id in "$WSP" "$WSI"; do alive "$id" && fail "$id still open after the run" || ok "$id not in herdr after the run"; done
grep -q '^- \[ \]' docs/plan/todo-tiny.md && fail "todo-tiny box not ticked" || ok "todo-tiny ticked"
[ -f subtract.go ] && ok "subtract.go landed" || fail "subtract.go missing"
[ -z "$(git status --porcelain)" ] && ok "tree clean" || fail "tree dirty: $(git status --porcelain | head -3)"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc"
ws_close "$WS" > /dev/null 2>&1
trap - EXIT

echo "passes: $passes failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
