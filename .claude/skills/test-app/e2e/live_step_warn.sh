#!/usr/bin/env bash
# usage: live_step_warn.sh
#   an accepted watchdog warn on the live phase-1/implement step is recorded, shown as a warning, typed into the
#   implement pane as "r-loop: watchdog warning: <reason>" (only while the agent works and before its sentinel),
#   rendered into the implement round-1 reviewer prompt, and halts nothing: the phase still lands
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
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-stepwarn}"
TOKEN="livewarn-$$-$(date +%s)"
REASON="$TOKEN keep the change minimal and add no new dependencies"
TYPED="r-loop: watchdog warning: $REASON"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
SG() { ls "$S"/.r-loop/runs/*/signals.jsonl 2>/dev/null | head -1; }
RUNDIR() { dirname "$(EV)"; }
qs() { local f; f=$(SG); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
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
agent_status() { herdr agent get "$1" 2>/dev/null | jq -r '.result.agent.status // "gone"'; }
agent_read() { herdr agent read "$1" --source recent-unwrapped --lines "${2:-600}" 2>/dev/null; }
squashed_has() { tr -d ' \n\r' < "$1" | grep -qF "$(printf '%s' "$2" | tr -d ' ')"; }
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events.jsonl" && echo "events  $OUT/events.jsonl"
  "$BIN" status --plain > "$OUT/status.txt" 2>&1; echo "status  $OUT/status.txt"; sed 's/^/     /' "$OUT/status.txt" | head -20
  git -C "$S" log --oneline > "$OUT/gitlog.txt"; sed 's/^/     git: /' "$OUT/gitlog.txt"
  echo "signal records:"; qs '.Kind=="signal"' | cut -c1-400 | sed 's/^/     /'
  echo "events of interest:"
  q '.Event.Kind|IN("signal-rejected","halt","warning","error","landed","finished","agent-named","agent-transcript")' \
    | jq -c '{k:.Event.Kind,p:.Event.Phase,s:.Event.Step,f:.Event.Fields}' | cut -c1-300 | sed 's/^/     /'
  echo "step records:"; q '.Kind=="step"' | jq -c '{k:.Step.Kind,a:.Step.Attempt,s:.State,r:.Reason}' | cut -c1-200 | sed 's/^/     /'
}
finish() { echo "RESULT: $passes passed, $fails failed"; exit "$fails"; }
H=""
trap '[ -n "$H" ] && "$TUI" stop "$H" >/dev/null 2>&1; herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
echo "reason  $REASON"
H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; exit 1; }
echo "handle  $H"

log "waiting for phase 1 implement running"
wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="implement" and .State=="running"' 1800; check $? "phase 1 implement running"
has '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="implement" and .State=="running"' || { dump; finish; }
IMPL=$(q "$(ev_kind agent-named) and .Event.Phase==\"1\" and .Event.Step==\"implement\" and (.Event.Fields.reviewer//\"\")==\"\"" | tail -1 | jq -r '.Event.Fields.agent')
echo "     implement agent: $IMPL"
[ -n "$IMPL" ] && [ "$IMPL" != null ]; check $? "implement agent named"
URL=$("$HERE/wd_url.sh" "$S") || { fail "watchdog url found"; dump; finish; }
echo "url     $URL"

PRE=$(agent_status "$IMPL")
PRE_SENT=$(ls "$(RUNDIR)"/phase-1/implement-a*.sentinel 2>/dev/null | head -1)
echo "     before signal: agent status=$PRE sentinel=${PRE_SENT:-none}"
args=$(jq -cn --arg r "$REASON" '{kind:"warn",step:"phase-1/implement",reason:$r,evidence:"calc.go:1"}')
got=$("$HERE/wd_call.sh" "$URL" signal "$args")
echo "     warn -> $got"
printf '%s\n' "$got" > "$OUT/wd-warn.json"
[ "$(printf '%s' "$got" | jq -r '.accepted')" = true ]; check $? "warn on phase-1/implement accepted by the MCP tool"
sleep 8

[ "$(qs ".Kind==\"signal\" and .Signal.Kind==\"warn\" and .Signal.Rejected!=true and .Signal.Reason==\"$REASON\"" | wc -l | tr -d ' ')" = 1 ]; check $? "one accepted warn record with the reason (signals.jsonl)"
has "$(ev_kind warning) and .Event.Phase==\"1\" and .Event.Step==\"implement\" and .Event.Fields.reason==\"$REASON\""; check $? "warning event with the reason (events.jsonl)"
! has "$(ev_kind warning) and (.Event.Fields.reason|test(\"watchdog warning not typed\"))"; check $? "no 'watchdog warning not typed' event"

agent_read "$IMPL" 800 > "$OUT/implement-pane.txt"
echo "pane    $OUT/implement-pane.txt"
if [ "$PRE" = working ] && [ -z "$PRE_SENT" ]; then
  squashed_has "$OUT/implement-pane.txt" "$TYPED"; check $? "implement pane shows \"$TYPED\""
  grep -n "$TOKEN" "$OUT/implement-pane.txt" | head -3 | sed 's/^/     pane: /'
else
  if squashed_has "$OUT/implement-pane.txt" "$TYPED"; then
    fail "warning typed although the agent was $PRE / sentinel ${PRE_SENT:-none} before the signal"
  else
    ok "agent was $PRE / sentinel ${PRE_SENT:-none} before the signal: nothing typed, as designed"
  fi
fi

frame warn-120x40.txt
grep -n "$TOKEN" "$OUT/frames/warn-120x40.txt" | sed 's/^/     120x40: /'
grep -q "$TOKEN" "$OUT/frames/warn-120x40.txt"; check $? "warning reason in the 120x40 frame"
width_ok "$OUT/frames/warn-120x40.txt" 120; check $? "no line wider than 120 at 120x40"

log "waiting for the implement round-1 reviewer"
RV=""
t=0
while [ "$t" -lt 1800 ] && [ -z "$RV" ]; do
  RV=$(q "$(ev_kind agent-named) and .Event.Phase==\"1\" and .Event.Step==\"implement\" and .Event.Fields.reviewer!=null and .Event.Fields.reviewer!=\"\" and .Event.Fields.round==\"1\"" | head -1 | jq -r '.Event.Fields.agent // empty')
  [ -n "$RV" ] && break
  [ "$(tui_state)" = running ] || break
  sleep 3; t=$((t + 3))
done
echo "     reviewer agent: ${RV:-none}"
[ -n "$RV" ]; check $? "implement round-1 reviewer named"
RV_SEEN=1
if [ -n "$RV" ]; then
  t=0
  while [ "$t" -lt 600 ]; do
    agent_read "$RV" 3000 > "$OUT/reviewer-pane.txt"
    squashed_has "$OUT/reviewer-pane.txt" "$REASON" && { RV_SEEN=0; echo "     reviewer prompt seen on the reviewer pane"; break; }
    [ "$(agent_status "$RV")" = gone ] && break
    sleep 5; t=$((t + 5))
  done
  echo "pane    $OUT/reviewer-pane.txt"
fi

log "waiting for the run to finish"
wait_q "$(ev_kind finished) or $(ev_kind halt)" "$LIMIT"
if [ "$RV_SEEN" != 0 ]; then
  TR=$(q "$(ev_kind agent-transcript) and .Event.Phase==\"1\" and .Event.Fields.agent==\"$RV\"" | head -1 | jq -r '.Event.Fields.transcript_path // empty')
  echo "     reviewer transcript: ${TR:-none}"
  [ -n "$TR" ] && [ -f "$TR" ] && grep -qF "$TOKEN" "$TR" && grep -qF "The watchdog warned during this step" "$TR" && RV_SEEN=0 && echo "     reviewer prompt seen in the reviewer transcript"
fi
[ "$RV_SEEN" = 0 ]; check $? "implement round-1 reviewer prompt carries the warning (pane or transcript; rendered prompts are not persisted in the run dir)"

has "$(ev_kind finished)"; check $? "finished event"
q "$(ev_kind halt)" | cut -c1-300 | sed 's/^/     halt: /'
! has "$(ev_kind halt)"; check $? "no halt event"
! has '.Kind=="step" and .State=="failed" and (.Reason|tostring|test("signal"))'; check $? "no step failed on a signal"
has '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="implement" and .State=="ok"'; check $? "phase 1 implement ok"
has "$(ev_kind landed) and .Event.Phase==\"1\""; check $? "phase 1 landed"
grep -q '^- \[x\]' docs/plan/todo-tiny.md; check $? "todo-tiny.md box ticked"
[ "$(git rev-list --count HEAD)" -gt 1 ]; check $? "phase commit in git log"
! has "$(ev_kind error)"; check $? "no error events"
"$TUI" wait-for "$H" "finished" --timeout 60 >/dev/null; check $? "TUI shows finished"
frame finished-120x40.txt
"$TUI" send "$H" q >/dev/null; sleep 3
"$TUI" stop "$H" --expect-exited; check $? "q quits, terminal restored (stop --expect-exited)"
H=""
dump
finish
