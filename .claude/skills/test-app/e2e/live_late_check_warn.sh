#!/usr/bin/env bash
# usage: live_late_check_warn.sh
#   a watchdog signal on phase-1/check after the check closed (plan step live) is rejected with
#   "phase-1/check is not open", recorded, shown as signal-rejected, and halts nothing: the phase still lands
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
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-latecheck}"
WANT="phase-1/check is not open"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; exit 1; }
trap '"$TUI" stop "$H" >/dev/null 2>&1; herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
echo "handle  $H"

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
expect_late_refusal() {
  local label=$1 args=$2 got
  got=$("$HERE/wd_call.sh" "$URL" signal "$args")
  echo "     $label -> $got"
  printf '%s\n' "$got" > "$OUT/wd-$label.json"
  [ "$(printf '%s' "$got" | jq -r '.accepted')" = false ] && [ "$(printf '%s' "$got" | jq -r '.reason')" = "$WANT" ]
  check $? "$label refused with \"$WANT\""
}
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events.jsonl" && echo "events  $OUT/events.jsonl"
  "$BIN" status --plain > "$OUT/status.txt" 2>&1; echo "status  $OUT/status.txt"; sed 's/^/     /' "$OUT/status.txt" | head -20
  git -C "$S" log --oneline > "$OUT/gitlog.txt"; sed 's/^/     git: /' "$OUT/gitlog.txt"
  echo "signal records:"; qs '.Kind=="signal"' | cut -c1-400 | sed 's/^/     /'
  echo "events of interest:"
  q '.Event.Kind|IN("signal-rejected","halt","warning","error","landed","finished","phase-check","phase-check-start","phase-check-timeout","phase-check-skipped")' \
    | jq -c '{k:.Event.Kind,p:.Event.Phase,s:.Event.Step,f:.Event.Fields}' | cut -c1-300 | sed 's/^/     /'
  echo "step records:"; q '.Kind=="step"' | jq -c '{k:.Step.Kind,a:.Step.Attempt,s:.State,r:.Reason}' | cut -c1-200 | sed 's/^/     /'
}
finish() { dump; echo "RESULT: $passes passed, $fails failed"; exit "$fails"; }

log "waiting for phase 1's check to end"
wait_q "$(ev_kind phase-check) and .Event.Phase==\"1\"" 900; check $? "phase 1 check ended (phase-check event)"
log "waiting for phase 1 plan to be live"
wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and (.State=="spawned" or .State=="running")' 600; check $? "phase 1 plan spawned/running"
if has '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and (.State=="ok" or .State=="failed")'; then
  fail "plan still live at injection time"
fi
URL=$("$HERE/wd_url.sh" "$S") || { fail "watchdog url found"; finish; }
echo "url     $URL"

expect_late_refusal warn '{"kind":"warn","step":"phase-1/check","reason":"late test warning","evidence":"calc.go:1"}'
expect_late_refusal halt '{"kind":"halt","step":"phase-1/check","reason":"late test halt","evidence":"calc.go:1"}'
sleep 3

[ "$(qs ".Kind==\"signal\" and .Signal.Rejected==true and .Signal.RejectReason==\"$WANT\"" | wc -l | tr -d ' ')" = 2 ]; check $? "two rejected signal records with the reason (signals.jsonl)"
[ -z "$(qs '.Kind=="signal" and .Signal.Rejected!=true')" ]; check $? "no accepted or forwarded signal"
[ "$(grep -c "(rejected: $WANT)" "$(REPORT)")" = 2 ]; check $? "report.md lists both signals as rejected"
frame inject-120x40.txt
frame inject-120x40.ansi --ansi
grep -c "$WANT" "$OUT/frames/inject-120x40.txt" | sed 's/^/     rows with reason: /'
[ "$(grep -c "phase 1 check: $WANT" "$OUT/frames/inject-120x40.txt")" = 2 ]; check $? "two signal-rejected lines in the 120x40 frame"
width_ok "$OUT/frames/inject-120x40.txt" 120; check $? "no line wider than 120 at 120x40"
L=$(grep -F "$WANT" "$OUT/frames/inject-120x40.ansi" | tail -1)
printf '%s' "$L" | cat -v | LC_ALL=C cut -c1-260 | sed 's/^/     ansi: /'
! printf '%s' "$L" | grep -qE '38;2;224;16[34];88'; check $? "rejected line not amber"
"$TUI" resize "$H" 80x24 >/dev/null; check $? "resize 80x24"
sleep 2; frame inject-80x24.txt
width_ok "$OUT/frames/inject-80x24.txt" 80; check $? "no line wider than 80 at 80x24"
grep -n "not open\|signal" "$OUT/frames/inject-80x24.txt" | sed 's/^/     80x24: /'
"$TUI" resize "$H" 120x40 >/dev/null

log "waiting for the run to finish"
wait_q "$(ev_kind finished) or $(ev_kind halt)" "$LIMIT"
has "$(ev_kind finished)"; check $? "finished event"
q "$(ev_kind halt)" | cut -c1-300 | sed 's/^/     halt: /'
! has "$(ev_kind halt)"; check $? "no halt event"
q '.Kind=="step" and .State=="failed"' | jq -c '{k:.Step.Kind,r:.Reason}' | sed 's/^/     failed: /'
! has '.Kind=="step" and .State=="failed" and (.Reason|test("signal"))'; check $? "no step failed on a signal"
! has '(.Reason//""|tostring|test("watchdog signal rejected")) or (.Event.Fields.reason//""|test("watchdog signal rejected"))'; check $? "nothing mentions watchdog signal rejected"
for k in plan implement; do
  has ".Kind==\"step\" and .Step.Phase==\"1\" and .Step.Kind==\"$k\" and .State==\"ok\""; check $? "phase 1 $k ok"
done
has "$(ev_kind landed) and .Event.Phase==\"1\""; check $? "phase 1 landed"
grep -q '^- \[x\]' docs/plan/todo-tiny.md; check $? "todo-tiny.md box ticked"
[ "$(git rev-list --count HEAD)" -gt 1 ]; check $? "phase commit in git log"
"$BIN" status --plain 2>&1 | head -1 | grep -q finished; check $? "status --plain head shows finished"
! has "$(ev_kind error)"; check $? "no error events"
"$TUI" wait-for "$H" "finished" --timeout 60 >/dev/null; check $? "TUI shows finished"
frame finished-120x40.txt
grep -n "×" "$OUT/frames/finished-120x40.txt" | sed 's/^/     cross: /'
! grep -q "×" "$OUT/frames/finished-120x40.txt"; check $? "no failed (×) marker in the finished frame"
"$TUI" send "$H" q >/dev/null; sleep 3
"$TUI" stop "$H" --expect-exited; check $? "q quits, terminal restored (stop --expect-exited)"
finish
