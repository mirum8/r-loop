#!/usr/bin/env bash
# usage: live_failure_report.sh [halt|abort|plain]
#   halt  (default): a watchdog `signal halt` on the live phase-1 plan halts the run; before the watchdog closes, the
#         driver asks it for <runDir>/failure-report.md: failure-report event, report.md `- diagnosis:`, TUI dim
#         `diagnosis: failure-report.md` line, `diagnosis: <path>` on stdout after q, exit 5
#   abort: C-c then y on a live step; records whether a failure report is requested and how long the TUI takes
#   plain: the halt flow with --plain piped through cat; `diagnosis: <path>` in stdout
set -u
MODE=${1:-halt}
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
(cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-failreport}-$MODE"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
HEAD0=$(git rev-parse HEAD)

H="" PID=""
if [ "$MODE" = plain ]; then
  ( "$BIN" docs/plan/todo-tiny.md --unattended --plain 2> "$OUT/plain.err" | cat > "$OUT/plain.out"; echo "${PIPESTATUS[0]}" > "$OUT/plain.rc" ) &
  PID=$!
  trap 'kill $PID 2>/dev/null; herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
  alive() { kill -0 "$PID" 2>/dev/null; }
else
  H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; exit 1; }
  trap '"$TUI" stop "$H" >/dev/null 2>&1; herdr workspace close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
  echo "handle  $H"
  alive() { [ "$("$TUI" status "$H" 2>/dev/null | awk '{print $1}')" = running ]; }
fi

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
REPORT() { ls "$S"/.r-loop/runs/*/report.md 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
has() { [ -n "$(q "$1" | head -1)" ]; }
wait_q() {
  local filter=$1 limit=$2 t=0
  while [ "$t" -lt "$limit" ]; do
    has "$filter" && return 0
    alive || { has "$filter" && return 0; return 1; }
    sleep 3; t=$((t + 3))
  done
  return 1
}
frame() { local p; p=$("$TUI" capture "$H" ${2:-}) && cp "$p" "$OUT/frames/$1" && echo "frame   $OUT/frames/$1"; }
ev_kind() { printf '.Event.Kind=="%s"' "$1"; }
width_ok() { python3 -c "import sys; sys.exit(any(len(l.rstrip('\\n')) > int(sys.argv[2]) for l in open(sys.argv[1], encoding='utf-8')))" "$1" "$2"; }
FR='.Event.Kind=="failure-report" or .Event.Kind=="failure-report-missing"'
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events.jsonl" && echo "events  $OUT/events.jsonl"
  [ -n "$(REPORT)" ] && cp "$(REPORT)" "$OUT/report.md"
  "$BIN" status --plain > "$OUT/status.txt" 2>&1; sed 's/^/     status: /' "$OUT/status.txt" | head -8
  git -C "$S" log --oneline | sed 's/^/     git: /'
  git -C "$S" status --porcelain | sed 's/^/     dirty: /'
  echo "events of interest:"
  q '.Event.Kind|IN("halt","aborted","failure-report","failure-report-missing","error","finished","abort-requested")' \
    | jq -c '{at:.Event.At,k:.Event.Kind,f:.Event.Fields}' | cut -c1-400 | sed 's/^/     /'
  echo "step records:"; q '.Kind=="step"' | jq -c '{p:.Step.Phase,k:.Step.Kind,a:.Step.Attempt,s:.State,r:.Reason}' | cut -c1-240 | sed 's/^/     /'
}
finish() { dump; echo "RESULT: $passes passed, $fails failed"; exit "$fails"; }

log "waiting for phase 1 plan to be live"
wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and (.State=="running")' 900; check $? "phase 1 plan running"

if [ "$MODE" = abort ]; then
  sleep 5
  T0=$(date +%s)
  "$TUI" send "$H" C-c >/dev/null; "$TUI" wait-for "$H" "stop the run" --timeout 10 >/dev/null; check $? "stop prompt shown"
  "$TUI" send "$H" y >/dev/null
  "$TUI" wait-for "$H" "aborted" --timeout 300 >/dev/null; check $? "TUI shows aborted"
  T1=$(date +%s)
  wait_q "$FR" 30 >/dev/null
  sleep 5
  frame aborted-120x40.txt
  q "$FR" | sed 's/^/     fr: /'
  if has "$FR"; then echo "     failure report requested on abort"; else echo "     no failure report on abort"; fi
  TS=$(stat -f %m "$OUT/frames/aborted-120x40.txt")
  echo "     abort -> aborted banner: $((T1 - T0))s"
  "$TUI" send "$H" q >/dev/null; sleep 3
  T2=$(date +%s); echo "     q -> exit observed after ${T2}-${T1}: $((T2 - T1))s"
  "$TUI" stop "$H" --expect-exited --status 1; check $? "abort exits 1, terminal restored"
  finish
fi

URL=$("$HERE/wd_url.sh" "$S") || { fail "watchdog url found"; finish; }
echo "url     $URL"
got=$("$HERE/wd_call.sh" "$URL" signal '{"kind":"halt","step":"phase-1/plan","reason":"e2e test halt: plan step deliberately halted to exercise the failure report","evidence":"calc.go:1"}')
echo "     signal -> $got"
[ "$(printf '%s' "$got" | jq -r '.accepted')" = true ]; check $? "halt signal accepted"

log "waiting for halt"
wait_q "$(ev_kind halt)" 600; check $? "halt event"
has '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and .State=="failed" and (.Reason|test("^watchdog"))'; check $? "plan step failed with watchdog: ..."
log "waiting for the failure report"
T0=$(date +%s)
wait_q "$FR" 1200; check $? "failure-report or failure-report-missing recorded"
echo "     report wait: $(( $(date +%s) - T0 ))s"
has "$(ev_kind failure-report)"; check $? "failure-report event"
P=$(q "$(ev_kind failure-report)" | jq -r '.Event.Fields.path' | tail -1)
echo "     path: $P"
RD=$(cd "$(dirname "$(EV)")" && pwd -P)
[ "$P" = "$RD/failure-report.md" ]; check $? "path is <runDir>/failure-report.md"
[ -s "$P" ]; check $? "failure-report.md exists and is non-empty"
[ -s "$P" ] && cp "$P" "$OUT/failure-report.md" && head -40 "$P" | sed 's/^/     fr| /'
for w in verdict timeline "root cause" impact fix; do
  grep -qi "$w" "$P" 2>/dev/null; check $? "report mentions $w"
done

if [ "$MODE" = plain ]; then
  t=0; while alive && [ $t -lt 300 ]; do sleep 3; t=$((t + 3)); done
  ! alive; check $? "plain run exited"
  rc=$(cat "$OUT/plain.rc" 2>/dev/null); echo "     plain exit $rc"
  [ "$rc" = 5 ]; check $? "plain exit 5"
  tail -8 "$OUT/plain.out" | sed 's/^/     out| /'
  grep -qx "diagnosis: $P" "$OUT/plain.out"; check $? "stdout has diagnosis: <path>"
  [ "$(grep -n '^report: ' "$OUT/plain.out" | cut -d: -f1)" -lt "$(grep -n '^diagnosis: ' "$OUT/plain.out" | cut -d: -f1)" ] 2>/dev/null; check $? "diagnosis after report:"
else
  sleep 3
  "$TUI" wait-for "$H" "diagnosis: failure-report.md" --timeout 30 >/dev/null; check $? "TUI shows diagnosis: failure-report.md"
  frame halted-120x40.txt
  frame halted-120x40.ansi --ansi
  grep -n "diagnosis\|halted" "$OUT/frames/halted-120x40.txt" | sed 's/^/     120: /'
  width_ok "$OUT/frames/halted-120x40.txt" 120; check $? "no line wider than 120"
  grep -F "diagnosis" "$OUT/frames/halted-120x40.ansi" | cat -v | LC_ALL=C cut -c1-260 | sed 's/^/     ansi: /'
  "$TUI" resize "$H" 80x24 >/dev/null; check $? "resize 80x24"
  sleep 2; frame halted-80x24.txt
  grep -n "diagnosis\|halted" "$OUT/frames/halted-80x24.txt" | sed 's/^/     80: /'
  width_ok "$OUT/frames/halted-80x24.txt" 80; check $? "no line wider than 80"
  grep -q "diagnosis: failure-report.md" "$OUT/frames/halted-80x24.txt"; check $? "diagnosis line at 80x24"
  "$TUI" resize "$H" 120x40 >/dev/null; sleep 1
  "$TUI" send "$H" q >/dev/null; sleep 3
  p=$("$TUI" capture "$H" 2>/dev/null) && cp "$p" "$OUT/frames/after-q.txt" && echo "frame   $OUT/frames/after-q.txt"
  grep -n "report:\|diagnosis:\|halted" "$OUT/frames/after-q.txt" | sed 's/^/     after-q: /'
  grep -qF "diagnosis: $P" "$OUT/frames/after-q.txt"; check $? "pane output after q has diagnosis: <path>"
  "$TUI" status "$H" | sed 's/^/     status: /'
  "$TUI" stop "$H" --expect-exited --status 5; check $? "halt exits 5, terminal restored"
fi

grep -qxF -- "- diagnosis: $P" "$(REPORT)"; check $? "report.md has - diagnosis: <path>"
sed -n '/^## Halt/,/^## /p' "$(REPORT)" | sed 's/^/     halt| /'
[ "$(git rev-parse HEAD)" = "$HEAD0" ]; check $? "no commit made"
[ -z "$(git status --porcelain --untracked-files=no)" ]; check $? "no tracked file edited in the sandbox"
! has "$(ev_kind error)"; check $? "no error events"
finish
