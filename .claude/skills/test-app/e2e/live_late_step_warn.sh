#!/usr/bin/env bash
# usage: live_late_step_warn.sh [warn|halt|both]   (default both)
#   warn: a watchdog warn on phase-1/plan after plan ended ok (next step live) is rejected with
#         "phase-1/plan is ok", recorded, shown as signal-rejected, and halts nothing: the phase still lands
#   halt: a watchdog halt on the same ended step is still rejected and still halts the run (exit 5)
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
(cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2700}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-latestep}"
WANT="phase-1/plan is ok"
MODE="${1:-both}"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
SG() { ls "$S"/.r-loop/runs/*/signals.jsonl 2>/dev/null | head -1; }
qs() { local f; f=$(SG); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
REPORT() { ls "$S"/.r-loop/runs/*/report.md 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
has() { [ -n "$(q "$1" | head -1)" ]; }
tui_state() { "$TUI" status "$H" 2>/dev/null | awk '{print $1}'; }
wait_q() {
  local filter=$1 limit=$2 t=0
  while [ "$t" -lt "$limit" ]; do
    has "$filter" && return 0
    [ "$(tui_state)" = running ] || { has "$filter" && return 0; return 1; }
    sleep 3; t=$((t + 3))
  done
  return 1
}
frame() { local p; p=$("$TUI" capture "$H" ${2:-}) && cp "$p" "$OUT/frames/$1" && echo "frame   $OUT/frames/$1"; }
ev_kind() { printf '.Event.Kind=="%s"' "$1"; }
width_ok() { python3 -c "import sys; sys.exit(any(len(l.rstrip('\\n')) > int(sys.argv[2]) for l in open(sys.argv[1], encoding='utf-8')))" "$1" "$2"; }
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events-$1.jsonl" && echo "events  $OUT/events-$1.jsonl"
  "$BIN" status --plain > "$OUT/status-$1.txt" 2>&1; echo "status  $OUT/status-$1.txt"; sed 's/^/     /' "$OUT/status-$1.txt" | head -20
  git -C "$S" log --oneline > "$OUT/gitlog-$1.txt"; sed 's/^/     git: /' "$OUT/gitlog-$1.txt"
  echo "signal records:"; qs '.Kind=="signal"' | cut -c1-400 | sed 's/^/     /'
  echo "events of interest:"
  q '.Event.Kind|IN("signal-rejected","halt","warning","error","landed","finished","phase-check","phase-check-start","phase-check-timeout","phase-check-skipped")' \
    | jq -c '{k:.Event.Kind,p:.Event.Phase,s:.Event.Step,f:.Event.Fields}' | cut -c1-300 | sed 's/^/     /'
  echo "step records:"; q '.Kind=="step"' | jq -c '{k:.Step.Kind,a:.Step.Attempt,s:.State,r:.Reason}' | cut -c1-200 | sed 's/^/     /'
}
finish() { echo "RESULT: $passes passed, $fails failed"; exit "$fails"; }
H=""
trap '[ -n "$H" ] && "$TUI" stop "$H" >/dev/null 2>&1; herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
start_run() {
  S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
  cd "$S" || exit 1
  echo "sandbox $S"
  echo "out     $OUT"
  H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; exit 1; }
  echo "handle  $H"
}
wait_next_step() {
  log "waiting for phase 1 plan ok"
  wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and .State=="ok"' 1500; check $? "phase 1 plan ok"
  log "waiting for the next phase 1 step to be live"
  wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind!="plan" and .Step.Kind!="check" and (.State=="spawned" or .State=="running")' 600; check $? "next phase 1 step spawned/running"
  NEXT=$(q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind!="plan" and .Step.Kind!="check" and (.State=="spawned" or .State=="running")' | head -1 | jq -r '.Step.Kind')
  echo "     next step: $NEXT"
  URL=$("$HERE/wd_url.sh" "$S") || { fail "watchdog url found"; finish; }
  echo "url     $URL"
}
call_signal() {
  local label=$1 args=$2 got
  got=$("$HERE/wd_call.sh" "$URL" signal "$args")
  echo "     $label -> $got"
  printf '%s\n' "$got" > "$OUT/wd-$label.json"
  [ "$(printf '%s' "$got" | jq -r '.accepted')" = false ] && [ "$(printf '%s' "$got" | jq -r '.reason')" = "$WANT" ]
  check $? "$label refused with \"$WANT\""
}


run_warn() {
  log "=== warn run"
  start_run
  wait_next_step
  call_signal warn '{"kind":"warn","step":"phase-1/plan","reason":"late test warning on an ended step","evidence":"calc.go:1"}'
  sleep 5
  [ "$(qs ".Kind==\"signal\" and .Signal.Kind==\"warn\" and .Signal.Rejected==true and .Signal.RejectReason==\"$WANT\"" | wc -l | tr -d ' ')" = 1 ]; check $? "one rejected warn record with the reason (signals.jsonl)"
  [ -z "$(qs '.Kind=="signal" and .Signal.Rejected!=true')" ]; check $? "no accepted or forwarded signal"
  ! has '.Kind=="step" and .State=="failed"'; check $? "no step failed right after the rejected warn"
  frame warn-120x40.txt
  frame warn-120x40.ansi --ansi
  grep -n "$WANT" "$OUT/frames/warn-120x40.txt" | sed 's/^/     120x40: /'
  [ "$(grep -c "phase 1 plan: $WANT" "$OUT/frames/warn-120x40.txt")" = 1 ]; check $? "one signal-rejected line in the 120x40 frame (face-only event, not in events.jsonl)"
  width_ok "$OUT/frames/warn-120x40.txt" 120; check $? "no line wider than 120 at 120x40"
  L=$(grep -F "$WANT" "$OUT/frames/warn-120x40.ansi" | tail -1)
  printf '%s' "$L" | cat -v | LC_ALL=C cut -c1-260 | sed 's/^/     ansi: /'
  ! printf '%s' "$L" | grep -qE '38;2;224;16[34];88'; check $? "rejected line not amber"
  "$TUI" resize "$H" 80x24 >/dev/null; check $? "resize 80x24"
  sleep 2; frame warn-80x24.txt
  width_ok "$OUT/frames/warn-80x24.txt" 80; check $? "no line wider than 80 at 80x24"
  grep -n "is ok\|signal" "$OUT/frames/warn-80x24.txt" | sed 's/^/     80x24: /'
  "$TUI" resize "$H" 120x40 >/dev/null

  log "waiting for the run to finish"
  wait_q "$(ev_kind finished) or $(ev_kind halt)" "$LIMIT"
  has "$(ev_kind finished)"; check $? "finished event"
  q "$(ev_kind halt)" | cut -c1-300 | sed 's/^/     halt: /'
  ! has "$(ev_kind halt)"; check $? "no halt event"
  q '.Kind=="step" and .State=="failed"' | jq -c '{k:.Step.Kind,r:.Reason}' | sed 's/^/     failed: /'
  ! has '.Kind=="step" and .State=="failed" and (.Reason|tostring|test("signal"))'; check $? "no step failed on a signal"
  ! has '(.Reason//""|tostring|test("watchdog signal rejected")) or (.Event.Fields.reason//""|test("watchdog signal rejected"))'; check $? "nothing mentions watchdog signal rejected"
  has '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="implement" and .State=="ok"'; check $? "phase 1 implement ok"
  has "$(ev_kind landed) and .Event.Phase==\"1\""; check $? "phase 1 landed"
  grep -q '^- \[x\]' docs/plan/todo-tiny.md; check $? "todo-tiny.md box ticked"
  [ "$(git rev-list --count HEAD)" -gt 1 ]; check $? "phase commit in git log"
  "$BIN" status --plain 2>&1 | head -1 | grep -q finished; check $? "status --plain head shows finished"
  ! has "$(ev_kind error)"; check $? "no error events"
  "$TUI" wait-for "$H" "finished" --timeout 60 >/dev/null; check $? "TUI shows finished"
  frame warn-finished-120x40.txt
  ! grep -q "×" "$OUT/frames/warn-finished-120x40.txt"; check $? "no failed (×) marker in the finished frame"
  "$TUI" send "$H" q >/dev/null; sleep 3
  "$TUI" stop "$H" --expect-exited; check $? "q quits, terminal restored (stop --expect-exited)"
  H=""
  dump warn
}

run_halt() {
  log "=== halt run"
  start_run
  wait_next_step
  call_signal halt '{"kind":"halt","step":"phase-1/plan","reason":"late test halt on an ended step","evidence":"calc.go:1"}'
  wait_q "$(ev_kind halt)" 300; check $? "halt event"
  q "$(ev_kind halt)" | cut -c1-300 | sed 's/^/     halt: /'
  has ".Kind==\"step\" and .State==\"failed\" and (.Reason|tostring|test(\"watchdog signal rejected: $WANT\"))"; check $? "live step failed with watchdog signal rejected: $WANT"
  ! has "$(ev_kind landed)"; check $? "phase 1 did not land"
  "$TUI" wait-for "$H" "halted" --timeout 600 >/dev/null; check $? "TUI shows halted"
  frame halt-120x40.txt
  grep -q "phase 1 plan: $WANT" "$OUT/frames/halt-120x40.txt"; check $? "signal-rejected line in the halted frame"
  grep -q "watchdog signal rejected: $WANT" "$OUT/frames/halt-120x40.txt"; check $? "halt reason in the halted frame"
  "$TUI" send "$H" q >/dev/null; sleep 3
  "$TUI" status "$H" | sed 's/^/     tui: /'
  "$TUI" stop "$H" --expect-exited --status 5; check $? "q quits with exit 5, terminal restored"
  H=""
  dump halt
}

case "$MODE" in
  warn) run_warn ;;
  halt) run_halt ;;
  *) run_warn; run_halt ;;
esac
finish
