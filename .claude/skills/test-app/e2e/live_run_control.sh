#!/usr/bin/env bash
# usage: live_run_control.sh A|B|C|D
#   A  pause_run, then continue_run, in the TUI; a pause during the last phase is dropped with a warning
#   B  stop_run after-phase: phase 1 lands, exit 5, halted; then r-loop resume runs phase 2 only
#   C  stop_run now during phase 1: exit 1, aborted, nothing lands
#   D  the real watchdog: the maintainer types "pause" / "continue" into its pane
set -u
MODE="${1:-A}"
case "$MODE" in A|B|C|D) ;; *) echo "usage: $0 A|B|C|D"; exit 2 ;; esac
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -x "$BIN" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2700}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-ctl$MODE}"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo.md --phases 1,2 --unattended) || { echo "FAIL start"; exit 1; }
trap '"$TUI" stop "$H" >/dev/null 2>&1' EXIT
echo "handle  $H"

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
has() { [ -n "$(q "$1" | head -1)" ]; }
wait_q() {
  local filter=$1 limit=$2 t=0
  while [ "$t" -lt "$limit" ]; do
    has "$filter" && return 0
    [ "$(tui_state)" = running ] || { has "$filter" && return 0; return 1; }
    sleep 5; t=$((t + 5))
  done
  return 1
}
tui_state() { "$TUI" status "$H" 2>/dev/null | awk '{print $1}'; }
tui_exit()  { "$TUI" status "$H" 2>/dev/null | sed -n 's/.*exit=\([0-9-]*\).*/\1/p'; }
frame() { local p; p=$("$TUI" capture "$H" ${2:-}) && cp "$p" "$OUT/frames/$1" && echo "frame   $OUT/frames/$1"; }
ev_kind() { printf '.Event.Kind=="%s"' "$1"; }
wd() { "$HERE/wd_call.sh" "$URL" "$@"; }
expect_refusal() {
  local label=$1 want=$2 got; shift 2
  got=$(wd "$@")
  echo "     $label -> $got"
  [ "$(printf '%s' "$got" | jq -r '.accepted')" = false ] && [ "$(printf '%s' "$got" | jq -r '.reason')" = "$want" ]
  check $? "$label refused with \"$want\""
}
expect_accept() {
  local label=$1 got; shift
  got=$(wd "$@")
  echo "     $label -> $got"
  [ "$(printf '%s' "$got" | jq -r '.accepted')" = true ]
  check $? "$label accepted"
}
live_step() { wait_q ".Step.Phase==\"$1\" and .State==\"running\"" "$2"; }
get_url() {
  local t=0
  while [ "$t" -lt 300 ]; do
    URL=$("$HERE/wd_url.sh" "$S" 2>/dev/null) && [ -n "$URL" ] && { echo "url     $URL"; return 0; }
    sleep 5; t=$((t + 5))
  done
  return 1
}
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events.jsonl" && echo "events  $OUT/events.jsonl"
  "$BIN" status --plain > "$OUT/status.txt" 2>&1; echo "status  $OUT/status.txt"; sed 's/^/     /' "$OUT/status.txt" | head -20
  git -C "$S" log --oneline > "$OUT/gitlog.txt"; echo "gitlog  $OUT/gitlog.txt"; sed 's/^/     /' "$OUT/gitlog.txt"
  echo "control events:"
  q '.Event.Kind|IN("pause-requested","stop-requested","paused","continued","warning","aborted","halt","finished","landed","phase-start","human","watchdog-call")' \
    | jq -c '{k:.Event.Kind,p:.Event.Phase,f:.Event.Fields}' | cut -c1-300 | sed 's/^/     /'
  echo "run records:"; q '.Kind=="run"' | jq -c '{Run,Reason}' | sed 's/^/     /'
}
finish() { dump; echo "RESULT $MODE: $passes passed, $fails failed"; exit "$fails"; }

log "waiting for phase 1 to start"
wait_q "$(ev_kind phase-start) and .Event.Phase==\"1\"" 900; check $? "phase 1 started"
get_url || { fail "watchdog url found"; finish; }

case "$MODE" in
A)
  live_step 1 600; check $? "a phase 1 step is running"
  expect_refusal "continue_run with nothing paused" "the run is not paused" continue_run '{"maintainer_said":"go on"}'
  expect_refusal "pause_run empty maintainer_said" "maintainer_said is empty: quote the maintainer's reply" pause_run '{"reason":"lunch","maintainer_said":""}'
  expect_refusal "pause_run empty reason" "reason is empty: say why, in the maintainer's terms" pause_run '{"reason":"","maintainer_said":"pause"}'
  expect_refusal "stop_run when=later" 'when "later" is not after-phase or now' stop_run '{"when":"later","reason":"x","maintainer_said":"stop"}'
  has "$(ev_kind phase-start) and .Event.Phase==\"1\"" && ! has "$(ev_kind landed) and .Event.Phase==\"1\""; check $? "phase 1 still live before pause_run"
  expect_accept "pause_run lunch" pause_run '{"reason":"lunch","maintainer_said":"pause after this phase"}'
  expect_refusal "second pause_run" "a pause is already pending" pause_run '{"reason":"lunch","maintainer_said":"pause after this phase"}'
  sleep 2
  has "$(ev_kind pause-requested) and .Event.Fields.reason==\"lunch\""; check $? "pause-requested event"
  has "$(ev_kind human) and .Event.Fields.what==\"consent\" and .Event.Fields.tool==\"pause_run\" and .Event.Fields.maintainer_said==\"pause after this phase\""; check $? "human consent event for pause_run"
  has "$(ev_kind watchdog-call)"; check $? "watchdog-call events recorded"

  log "waiting for phase 1 to land"
  wait_q "$(ev_kind landed) and .Event.Phase==\"1\"" "$LIMIT"; check $? "phase 1 landed"
  wait_q "$(ev_kind paused)" 300; check $? "paused event"
  q "$(ev_kind paused)" | head -1 | sed 's/^/     /'
  has '.Kind=="run" and .Run=="paused" and .Reason=="lunch"'; check $? "run record status paused reason lunch"
  "$BIN" status --plain > "$OUT/status-paused.txt" 2>&1
  head -3 "$OUT/status-paused.txt" | sed 's/^/     /'
  head -1 "$OUT/status-paused.txt" | grep -q ' paused'; check $? "status --plain head shows paused"
  FOOT="paused · lunch · tell the watchdog to continue or stop"
  "$TUI" wait-for "$H" "paused · lunch" --timeout 30 >/dev/null; check $? "TUI footer shows paused (120x40)"
  frame paused-120x40.txt; grep -qF "$FOOT" "$OUT/frames/paused-120x40.txt"; check $? "footer text exact at 120x40"
  frame paused-120x40.ansi "--ansi"
  L=$(grep -F "paused · lunch" "$OUT/frames/paused-120x40.ansi" | tail -1)
  printf '%s' "$L" | cat -v | cut -c1-200 | sed 's/^/     ansi: /'
  printf '%s' "$L" | grep -qE '38;2;224;16[34];88'; check $? "footer in secondary amber (#E0A458, lipgloss renders 224;163;88)"
  ! printf '%s' "$L" | grep -q '38;2;224;115;106'; check $? "footer not in error red (#E0736A)"
  for g in 80x24 160x50; do
    "$TUI" resize "$H" "$g" >/dev/null; check $? "resize $g"
    sleep 2; frame "paused-$g.txt"
    grep -q "paused · lunch" "$OUT/frames/paused-$g.txt"; check $? "footer visible at $g"
    python3 -c "import sys; sys.exit(any(len(l.rstrip('\\n')) > int(sys.argv[2]) for l in open(sys.argv[1], encoding='utf-8')))" "$OUT/frames/paused-$g.txt" "${g%x*}"; check $? "no line wider than ${g%x*} at $g"
  done
  "$TUI" resize "$H" 120x40 >/dev/null
  log "holding 60s to confirm phase 2 does not start"
  sleep 60
  ! has "$(ev_kind phase-start) and .Event.Phase==\"2\""; check $? "phase 2 not started while paused (60s)"
  expect_refusal "pause_run while paused" "the run is already paused" pause_run '{"reason":"x","maintainer_said":"pause"}'
  expect_accept "continue_run go on" continue_run '{"maintainer_said":"go on"}'
  wait_q "$(ev_kind continued)" 60; check $? "continued event"
  has '.Kind=="run" and .Run=="running"' ; check $? "run record back to running"
  wait_q "$(ev_kind phase-start) and .Event.Phase==\"2\"" 300; check $? "phase 2 started after continue"
  sleep 5; frame continued-120x40.txt
  ! grep -q "paused · lunch" "$OUT/frames/continued-120x40.txt"; check $? "paused footer gone after continue"
  live_step 2 600; check $? "a phase 2 step is running"
  expect_accept "pause_run during last phase" pause_run '{"reason":"tea","maintainer_said":"pause after this one"}'
  log "waiting for phase 2 to land and the run to finish"
  wait_q "$(ev_kind finished)" "$LIMIT"; check $? "finished event"
  has "$(ev_kind warning) and (.Event.Fields.reason|test(\"the last one\"))"; check $? "warning mentions the last one"
  q "$(ev_kind warning) and (.Event.Fields.reason|test(\"the last one\"))" | sed 's/^/     /'
  ! has "$(ev_kind paused) and .Event.Phase==\"2\""; check $? "no pause after the last phase"
  "$TUI" wait-for "$H" "finished" --timeout 60 >/dev/null; check $? "TUI shows finished"
  frame finished-120x40.txt
  "$TUI" send "$H" q >/dev/null; sleep 3
  "$TUI" stop "$H" --expect-exited; check $? "q quits, terminal restored (stop --expect-exited)"
  ;;
B)
  live_step 1 600; check $? "a phase 1 step is running"
  expect_accept "stop_run after-phase" stop_run '{"when":"after-phase","reason":"review phase 1","maintainer_said":"stop after this phase"}'
  sleep 2
  has "$(ev_kind stop-requested) and .Event.Fields.reason==\"review phase 1\""; check $? "stop-requested event"
  has "$(ev_kind human) and .Event.Fields.tool==\"stop_run\""; check $? "human consent event for stop_run"
  log "waiting for phase 1 to land and the run to halt"
  wait_q "$(ev_kind landed) and .Event.Phase==\"1\"" "$LIMIT"; check $? "phase 1 landed"
  t=0; while [ "$(tui_state)" = running ] && [ $t -lt 300 ]; do
    "$TUI" capture "$H" >/dev/null 2>&1; grep -q "resume" "$("$TUI" capture "$H" 2>/dev/null)" && break; sleep 5; t=$((t+5)); done
  sleep 3; frame halted-120x40.txt
  grep -q "halted" "$OUT/frames/halted-120x40.txt"; check $? "TUI shows halted"
  grep -q "resume: r-loop resume" "$OUT/frames/halted-120x40.txt"; check $? "TUI banner names resume: r-loop resume"
  ! has "$(ev_kind phase-start) and .Event.Phase==\"2\""; check $? "phase 2 never started"
  has '.Kind=="run" and .Run=="halted" and .Reason=="stopped by the watchdog: review phase 1"'; check $? "run record halted with the stop reason"
  "$TUI" send "$H" q >/dev/null; sleep 3
  code=$(tui_exit); echo "     exit=$code"
  [ "$code" = 5 ]; check $? "exit code 5"
  "$TUI" stop "$H" --expect-exited --status 5; check $? "terminal restored after halt"
  "$BIN" status --plain > "$OUT/status-halted.txt" 2>&1; head -3 "$OUT/status-halted.txt" | sed 's/^/     /'
  head -1 "$OUT/status-halted.txt" | grep -q halted && grep -q "stopped by the watchdog: review phase 1" "$OUT/status-halted.txt"; check $? "status --plain halted with reason"
  git log --oneline | head -3 | sed 's/^/     /'
  N1=$(git rev-list --count HEAD)
  log "resume"
  H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" resume --unattended) || { fail "resume start"; finish; }
  wait_q "$(ev_kind finished)" "$LIMIT"; check $? "resumed run finished"
  frame resumed-finished.txt
  q "$(ev_kind phase-start)" | jq -c '{k:.Event.Kind,p:.Event.Phase,at:.At}' | sed 's/^/     /'
  [ "$(q "$(ev_kind phase-start) and .Event.Phase==\"1\"" | wc -l | tr -d ' ')" = 1 ]; check $? "phase 1 started exactly once (not redone)"
  has "$(ev_kind landed) and .Event.Phase==\"2\""; check $? "phase 2 landed after resume"
  "$TUI" send "$H" q >/dev/null; sleep 3
  "$TUI" stop "$H" --expect-exited; check $? "resume exits 0, terminal restored"
  ;;
C)
  wait_q ".Step.Phase==\"1\" and .State==\"running\" and (.Step.Kind==\"plan\" or .Step.Kind==\"implement\")" 600; check $? "phase 1 plan/implement running"
  expect_accept "stop_run now" stop_run '{"when":"now","reason":"wrong plan","maintainer_said":"stop now"}'
  t=0; while [ "$(tui_state)" = running ] && [ $t -lt 180 ] && ! has "$(ev_kind aborted)"; do sleep 3; t=$((t+3)); done
  sleep 5; frame stopped-now.txt
  [ "$(tui_state)" = running ] && { echo "     still running after abort; sending q"; "$TUI" send "$H" q >/dev/null; sleep 5; }
  code=$(tui_exit); echo "     exit=$code"
  [ "$code" = 1 ]; check $? "exit code 1"
  has "$(ev_kind aborted)"; check $? "aborted event"
  q '.Kind=="run"' | tail -1 | jq -c '{Run,Reason}' | sed 's/^/     /'
  q '.Kind=="run"' | tail -1 | jq -e '.Run=="halted" and (.Reason|test("abort"))' >/dev/null; check $? "run halted with reason aborted"
  ! has "$(ev_kind landed)"; check $? "nothing landed"
  [ "$(git rev-list --count HEAD)" = 1 ]; check $? "no new commit"
  "$TUI" stop "$H" --expect-exited --status 1; check $? "terminal restored after abort"
  echo "leftover worktrees:"; git worktree list | sed 's/^/     /'
  ;;
D)
  live_step 1 600; check $? "a phase 1 step is running"
  WDP=$(herdr agent list | jq -r --arg b "/$(basename "$S")" '.result.agents[] | select((tostring|test("rloop-wd-")) and (.cwd|endswith($b))) | .pane_id' | head -1)
  echo "     watchdog pane: ${WDP:-none}"
  herdr agent list | jq -c '.result.agents[] | select((tostring|test("rloop-wd-")))' | cut -c1-400 | sed 's/^/     /'
  [ -n "$WDP" ] || { fail "watchdog pane found"; finish; }
  herdr agent prompt "$WDP" "Maintainer here: pause the run after this phase, I want to look at phase 1 first." >/dev/null; check $? "typed pause into the watchdog pane"
  wait_q "$(ev_kind watchdog-call) and (.Event.Fields.tool==\"pause_run\")" 300; check $? "watchdog called pause_run itself"
  q "$(ev_kind watchdog-call) and (.Event.Fields|tostring|test(\"pause_run|continue_run|stop_run\"))" | cut -c1-400 | sed 's/^/     /'
  has "$(ev_kind pause-requested)"; check $? "pause-requested from the real watchdog"
  wait_q "$(ev_kind paused)" "$LIMIT"; check $? "paused after phase 1"
  frame d-paused.txt
  herdr agent prompt "$WDP" "Maintainer here: ok, continue the run." >/dev/null; check $? "typed continue into the watchdog pane"
  wait_q "$(ev_kind continued)" 300; check $? "watchdog called continue_run (continued event)"
  q "$(ev_kind human) and .Event.Fields.what==\"consent\"" | cut -c1-400 | sed 's/^/     /'
  herdr agent read "$WDP" 2>/dev/null | tail -40 > "$OUT/watchdog-pane.txt"; echo "wdpane  $OUT/watchdog-pane.txt"
  wait_q "$(ev_kind finished)" "$LIMIT"; check $? "run finished"
  "$TUI" send "$H" q >/dev/null; sleep 3
  "$TUI" stop "$H" --expect-exited; check $? "terminal restored"
  ;;
esac
finish
