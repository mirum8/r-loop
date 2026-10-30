#!/usr/bin/env bash
# usage: live_run_report.sh [tui|plain]
#   tui   (default): docs/plan/todo-tiny.md --unattended runs to the end; before the watchdog closes, the driver asks
#         it for <runDir>/run-report.md: run-report event, report.md `- run report:`, TUI dim `run report:
#         run-report.md` line, `run report: <path>` on stdout after q, exit 0. A `narrate` on the live run is recorded
#         as narration, and a question is refused.
#   plain: the same run with --plain piped through cat; `watchdog: <text>` and `run report: <path>` in stdout
set -u
MODE=${1:-tui}
. "$(dirname "$0")/host.sh"
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
host_up || { echo "NOT RUN: no session host (herdr or herdr-app)"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
(cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-runreport}-$MODE"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

H="" PID=""
if [ "$MODE" = plain ]; then
  ( "$BIN" docs/plan/todo-tiny.md --unattended --plain 2> "$OUT/plain.err" | cat > "$OUT/plain.out"; echo "${PIPESTATUS[0]}" > "$OUT/plain.rc" ) &
  PID=$!
  trap 'kill $PID 2>/dev/null; ws_close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
  alive() { kill -0 "$PID" 2>/dev/null; }
else
  H=$("$TUI" start --ttl 7200 --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start"; exit 1; }
  trap '"$TUI" stop "$H" >/dev/null 2>&1; ws_close "$R_LOOP_TEST_WS" > /dev/null 2>&1' EXIT INT TERM
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
RR='.Event.Kind=="run-report" or .Event.Kind=="run-report-missing"'
NARRATION="e2e narration: phase 1 is planning"
dump() {
  local f; f=$(EV)
  [ -n "$f" ] && cp "$f" "$OUT/events.jsonl" && echo "events  $OUT/events.jsonl"
  [ -n "$(REPORT)" ] && cp "$(REPORT)" "$OUT/report.md"
  "$BIN" status --plain > "$OUT/status.txt" 2>&1; sed 's/^/     status: /' "$OUT/status.txt" | head -8
  git -C "$S" log --oneline | sed 's/^/     git: /'
  echo "events of interest:"
  q '.Event.Kind|IN("halt","aborted","finished","run-report","run-report-missing","narration","error")' \
    | jq -c '{at:.Event.At,k:.Event.Kind,f:.Event.Fields}' | cut -c1-400 | sed 's/^/     /'
}
finish() { dump; echo "RESULT: $passes passed, $fails failed"; exit "$fails"; }

log "waiting for phase 1 plan to be live"
wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and (.State=="running")' 900; check $? "phase 1 plan running"

URL=$("$HERE/wd_url.sh" "$S") || { fail "watchdog url found"; finish; }
echo "url     $URL"
got=$("$HERE/wd_call.sh" "$URL" narrate "$(jq -cn --arg t "$NARRATION" '{text:$t}')")
echo "     narrate -> $got"
[ "$(printf '%s' "$got" | jq -r '.accepted')" = true ]; check $? "narrate accepted"
got=$("$HERE/wd_call.sh" "$URL" narrate '{"text":"shall I restart phase 1?"}')
echo "     narrate? -> $got"
[ "$(printf '%s' "$got" | jq -r '.accepted')" = false ]; check $? "a question is refused"
has ".Event.Kind==\"narration\" and .Event.Fields.text==\"$NARRATION\""; check $? "narration recorded"
has '.Event.Kind=="watchdog-call" and .Event.Fields.tool=="narrate"'; check $? "narrate recorded as watchdog-call"

log "waiting for the run to finish"
wait_q "$(ev_kind finished)" 5400; check $? "finished event"
log "waiting for the run report"
T0=$(date +%s)
wait_q "$RR" 1200; check $? "run-report or run-report-missing recorded"
echo "     report wait: $(( $(date +%s) - T0 ))s"
has "$(ev_kind run-report)"; check $? "run-report event"
P=$(q "$(ev_kind run-report)" | jq -r '.Event.Fields.path' | tail -1)
echo "     path: $P"
RD=$(cd "$(dirname "$(EV)")" && pwd -P)
[ "$P" = "$RD/run-report.md" ]; check $? "path is <runDir>/run-report.md"
[ -s "$P" ]; check $? "run-report.md exists and is non-empty"
[ -s "$P" ] && cp "$P" "$OUT/run-report.md" && head -40 "$P" | sed 's/^/     rr| /'
for h in "## Summary" "## Problems" "## Follow-ups"; do
  grep -qF "$h" "$P" 2>/dev/null; check $? "report has $h"
done

if [ "$MODE" = plain ]; then
  t=0; while alive && [ $t -lt 300 ]; do sleep 3; t=$((t + 3)); done
  ! alive; check $? "plain run exited"
  rc=$(cat "$OUT/plain.rc" 2>/dev/null); echo "     plain exit $rc"
  [ "$rc" = 0 ]; check $? "plain exit 0"
  tail -8 "$OUT/plain.out" | sed 's/^/     out| /'
  grep -qE "^[0-9:]{8}  watchdog: $NARRATION\$" "$OUT/plain.out"; check $? "stdout has the watchdog: narration line"
  grep -qx "run report: $P" "$OUT/plain.out"; check $? "stdout has run report: <path>"
  [ "$(grep -n '^report: ' "$OUT/plain.out" | cut -d: -f1)" -lt "$(grep -n '^run report: ' "$OUT/plain.out" | cut -d: -f1)" ] 2>/dev/null; check $? "run report after report:"
else
  sleep 3
  "$TUI" wait-for "$H" "run report: run-report.md" --timeout 30 >/dev/null; check $? "TUI shows run report: run-report.md"
  frame finished-120x40.txt
  width_ok "$OUT/frames/finished-120x40.txt" 120; check $? "no line wider than 120"
  "$TUI" send "$H" q >/dev/null; sleep 3
  p=$("$TUI" capture "$H" 2>/dev/null) && cp "$p" "$OUT/frames/after-q.txt" && echo "frame   $OUT/frames/after-q.txt"
  grep -qF "run report: $P" "$OUT/frames/after-q.txt"; check $? "pane output after q has run report: <path>"
  "$TUI" stop "$H" --expect-exited --status 0; check $? "finished run exits 0, terminal restored"
fi

grep -qxF -- "- run report: $P" "$(REPORT)"; check $? "report.md has - run report: <path>"
[ -z "$(git status --porcelain --untracked-files=no)" ]; check $? "no tracked file left edited in the sandbox"
! has "$(ev_kind error)"; check $? "no error events"
finish
