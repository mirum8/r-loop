#!/usr/bin/env bash
# usage: live_full_plan.sh
#   The full three-phase docs/plan/todo.md, --unattended, in its own herdr workspace, TUI at 120x40.
#   Unattended skips the ## Resolve first walk (ADR-72): no unblock/entry-resolved events; the open entry is
#   recorded as entry-deferred for phase 3, triage and the run list cover phases 1 and 2 only, both land, phase 3
#   never starts, the entry and phase 3 stay unticked, the run finishes, q quits and the terminal is restored.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3600}"
OUT="${OUT:-$HERE/frames/full-plan-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-fullplan}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-7200}"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

WSJ=$(herdr workspace create --cwd "$S" --label full-plan --no-focus) || { echo "FAIL workspace create"; exit 1; }
WS=$(printf '%s' "$WSJ" | jq -r '.result.workspace.workspace_id')
PANE=$(printf '%s' "$WSJ" | jq -r '.result.root_pane.pane_id')
echo "herdr   $WS $PANE"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$PANE" "$BIN" docs/plan/todo.md --unattended) || { echo "FAIL start"; herdr workspace close "$WS" > /dev/null 2>&1; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; herdr workspace close "$WS" > /dev/null 2>&1' EXIT
echo "handle  $H"

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select(.Kind==\"event\" and ($1))" "$f" 2>/dev/null; }
has() { [ -n "$(q "$1" | head -1)" ]; }
kind() { printf '.Event.Kind=="%s"' "$1"; }

start=$(date +%s)
final=""
while :; do
  sleep 5
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
RD=$(dirname "$F")
cp "$F" "$OUT/events.jsonl"
cp "$RD/report.md" "$OUT/report.md" 2>/dev/null
"$BIN" status --plain > "$OUT/status.txt" 2>&1
git log --oneline > "$OUT/git-log.txt"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
has "$(kind unblock)" && fail "unattended run started the Resolve first walk: $(q "$(kind unblock)")" || ok "no unblock walk unattended"
has "$(kind entry-resolved)" && fail "entry-resolved unattended: $(q "$(kind entry-resolved)")" || ok "no entry-resolved unattended"
d=$(q "$(kind entry-deferred)" | jq -c '.Event.Fields' | head -1)
[ "$(printf '%s' "$d" | jq -r '.entry')" = "Custom delimiter syntax" ] && [ "$(printf '%s' "$d" | jq -r '.phases')" = 3 ] && ok "entry-deferred: $d" || fail "entry-deferred: ${d:-none}"
t=$(q "$(kind triage-start)" | jq -r '.Event.Fields.phases' | head -1)
[ "$t" = "1, 2" ] && ok "triage-start covers phases 1, 2" || fail "triage-start phases: ${t:-none}"
has "$(kind triage)" && ok "triage event recorded" || fail "no triage event"
has "$(kind run-list)" && ok "run-list: $(q "$(kind run-list)" | jq -c '.Event.Fields' | head -1 | cut -c1-200)" || fail "no run-list event"
for p in 1 2; do
  has "$(kind landed) and .Event.Phase==\"$p\"" && ok "phase $p landed" || fail "phase $p did not land"
done
has ".Event.Phase==\"3\" and (.Event.Kind==\"phase-start\" or .Event.Kind==\"landed\")" && fail "phase 3 ran: $(q '.Event.Phase=="3"' | head -2)" || ok "phase 3 never started"
has "(.Event.Kind==\"error\" or .Event.Kind==\"halt\")" && fail "error/halt events: $(q '.Event.Kind=="error" or .Event.Kind=="halt"' | head -3)" || ok "no error/halt events"

awk '/^### Phase 1/,/^\*\*Done/' docs/plan/todo.md | grep -q '^- \[ \]' && fail "phase 1 boxes not all ticked" || ok "phase 1 boxes ticked"
awk '/^### Phase 2/,/^\*\*Done/' docs/plan/todo.md | grep -q '^- \[ \]' && fail "phase 2 boxes not all ticked" || ok "phase 2 boxes ticked"
awk '/^### Phase 3/,/^\*\*Done/' docs/plan/todo.md | grep -q '^- \[x\]' && fail "phase 3 box ticked" || ok "phase 3 boxes still open"
grep -q '^- \[ \] \*\*Custom delimiter syntax\*\*' docs/plan/todo.md && ok "Resolve first entry still open" || fail "Resolve first entry changed: $(grep -A1 'Custom delimiter' docs/plan/todo.md | head -2)"
[ -f multiply.go ] && ok "multiply.go landed" || fail "multiply.go missing"
go test ./... > "$OUT/go-test.txt" 2>&1 && ok "go test ./... green on the landed tree" || fail "go test: $(tail -3 "$OUT/go-test.txt")"
[ -z "$(git status --porcelain)" ] && ok "tree clean" || fail "tree dirty: $(git status --porcelain | head -3)"
git log --all --name-only --format= | grep -qE '\.r-loop/(runs|wt)' && fail "run artifacts committed" || ok "no run artifacts committed"
grep -qi 'custom delimiter\|deferred' "$OUT/report.md" && ok "report names the deferred entry: $(grep -i 'custom delimiter\|deferred' "$OUT/report.md" | head -1)" || fail "report.md does not mention the deferred entry"
grep -qE '×  3 ' "$OUT/frame-end.txt" && ok "end frame marks the deferred phase 3 with ×" || fail "phase 3 not marked × in the end frame: $(grep -E '  3 ' "$OUT/frame-end.txt" | head -1)"
grep -q 'Resolve first open: Custom delimiter syntax' "$OUT/frame-end.txt" "$OUT"/frame-*.txt 2>/dev/null && ok "feed names the open Resolve first entry" || echo "NOTE feed line for the deferred entry not in a captured frame"
python3 -c 'import sys; bad=[l for l in open(sys.argv[1]).read().split("\n") if len(l)>120]; [print("     wide:",l) for l in bad]; sys.exit(bool(bad))' "$OUT/frame-end.txt" && ok "end frame fits 120 columns" || fail "end frame has over-wide lines"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc"
herdr workspace close "$WS" > /dev/null 2>&1
trap - EXIT

echo "passes: $passes failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
