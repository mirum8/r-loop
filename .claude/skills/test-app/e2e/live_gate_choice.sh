#!/bin/sh
# Live: at land the watchdog chooses which of the Done-when line's backticked spans are the gate (choose_gate).
# todo-gate.md's line backticks `go test ./...`, `calc` and `GET /sum`; only `go test ./...` may run.
# While the pick is open the script sends a non-candidate (`app-api`) itself and expects it refused, never a candidate:
# a candidate would be accepted and race the watchdog's own choice.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

E2E=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$E2E/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3000}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
wd()   { "$E2E/wd_call.sh" "$WDURL" "$@"; }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-gate-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-gate}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4200}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-gate.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frames/start-120x40.txt"

has() { [ -n "${EV:-}" ] && grep -q "\"Kind\":\"$1\"" "$EV"; }

start=$(date +%s)
early=0 window=0 n=0 final=""
while :; do
  n=$((n + 1))
  EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
  if [ $early = 0 ] && [ -f .r-loop/runs/current ] && has watchdog-placed && ! has gate-chosen; then
    WDURL=$("$E2E/wd_url.sh" "$S" 2> "$OUT/wdurl.err") && {
      echo "$WDURL" > "$OUT/wdurl.txt"
      wd choose_gate '{"phase":"1","commands":["app-api"]}' > "$OUT/call-closed.json" 2>&1
      grep -q 'not available' "$OUT/call-closed.json" || early=1
    }
  fi
  if [ $window = 0 ] && [ $early = 1 ] && ! has gate-chosen; then
    WDPANE=$(grep '"Kind":"watchdog-placed"' "$EV" | head -1 | sed 's/.*"pane":"\([^"]*\)".*/\1/')
    if herdr pane read "$WDPANE" 2>/dev/null | grep -q 'choose gate phase 1'; then
      window=1
      wd choose_gate '{"phase":"1","commands":["app-api"]}' > "$OUT/call-noncand.json" 2>&1
      herdr pane read "$WDPANE" 2>/dev/null | tail -30 > "$OUT/watchdog-pick.txt"
      for g in 160x50 120x40 80x24; do
        "$TUI" resize "$H" "$g" > /dev/null || echo "resize $g rc=$?" >> "$OUT/frames/errors.txt"
        sleep 0.5
        cp "$("$TUI" capture "$H")" "$OUT/frames/pick-$g.txt"
      done
      "$TUI" resize "$H" 120x40 > /dev/null
    fi
  fi
  [ $((n % 3)) = 1 ] || { sleep 1; continue; }
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  sleep 1
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 4
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
cp "$EV" "$OUT/events.jsonl"
cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null
python3 - "$EV" > "$OUT/key-events.txt" <<'EOF'
import json, sys
keep = {"watchdog-call", "gate-chosen", "merge-intent", "land-stage", "gate-skipped", "landed", "blocked-on", "blocker-resolved", "halt", "warning", "run"}
for i, line in enumerate(open(sys.argv[1])):
    r = json.loads(line)
    e = r.get("Event") or {}
    if e.get("Kind") in keep or r.get("Kind") in ("merge-intent",):
        print(i, r.get("Kind"), e.get("Kind"), e.get("Phase"), json.dumps(e.get("Fields") or {k: v for k, v in r.items() if k not in ("Event",)}, ensure_ascii=False)[:400])
EOF

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

python3 - "$EV" > "$OUT/checks.txt" <<'EOF'
import json, sys
recs = [json.loads(l) for l in open(sys.argv[1])]
def ev(i, r):
    e = r.get("Event") or {}
    return e.get("Kind"), e.get("Fields") or {}
calls = [(i, f) for i, r in enumerate(recs) for k, f in [ev(i, r)] if k == "watchdog-call" and f.get("tool") == "choose_gate"]
chosen = [(i, f) for i, r in enumerate(recs) for k, f in [ev(i, r)] if k == "gate-chosen"]
merge = [i for i, r in enumerate(recs) if r.get("Kind") == "merge-intent" or ev(i, r)[0] == "merge-intent"]
gates = [(i, f) for i, r in enumerate(recs) for k, f in [ev(i, r)] if k == "land-stage" and f.get("stage") == "gate"]
skipped = [i for i, r in enumerate(recs) if ev(i, r)[0] == "gate-skipped"]
print("calls", json.dumps([f for _, f in calls]))
print("chosen", json.dumps([f for _, f in chosen]))
print("chosen_idx", chosen[0][0] if chosen else -1)
print("merge_idx", merge[0] if merge else -1)
print("gates", json.dumps([f.get("command") for _, f in gates]))
print("skipped", len(skipped))
print("commands_ok", bool(chosen) and len(chosen) == 1 and json.loads(chosen[0][1].get("commands", "null")) == ["go test ./..."])
EOF
cat "$OUT/checks.txt"
val() { grep "^$1 " "$OUT/checks.txt" | cut -d' ' -f2-; }

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
[ "$code" = 0 ] && ok "exit 0" || fail "exit $code"
val calls | grep -q '"tool": "choose_gate"' && ok "watchdog-call choose_gate recorded" || fail "no watchdog-call choose_gate"
[ "$(val commands_ok)" = True ] && ok "gate-chosen commands exactly [\"go test ./...\"]: $(val chosen)" || fail "gate-chosen wrong: $(val chosen)"
ci=$(val chosen_idx); mi=$(val merge_idx)
[ "$ci" -ge 0 ] && [ "$mi" -ge 0 ] && [ "$ci" -lt "$mi" ] && ok "gate-chosen (line $ci) before merge-intent (line $mi)" || fail "gate-chosen $ci vs merge-intent $mi"
[ "$(val gates)" = '["go test ./..."]' ] && ok "land-stage gate command: $(val gates)" || fail "land-stage gate commands: $(val gates)"
[ "$(val skipped)" = 0 ] && ok "no gate-skipped" || fail "gate-skipped present"
grep -q '"Kind":"landed"' "$EV" && ok "landed event" || fail "no landed event"
grep -Eq 'exited 127|command not found' "$EV" && fail "a 127 / command not found in events" || ok "no exit 127 in events"
grep -q '^- \[x\] `Subtract' docs/plan/todo-gate.md && ok "todo-gate.md box ticked" || fail "todo-gate.md not ticked"
git log --oneline > "$OUT/gitlog.txt"
LM=$(grep '"Kind":"landed"' "$EV" | grep -o '"merge":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
[ -n "$LM" ] && git cat-file -e "$LM" 2>/dev/null && ok "phase commit in git log: $(git log -1 --format='%h %s' "$LM")" || fail "no landed merge commit ($LM): $(head -3 "$OUT/gitlog.txt" | tr '\n' ';')"
grep -q '"accepted":false,"reason":"no gate pick is open for phase 1"' "$OUT/call-closed.json" 2>/dev/null \
  && ok "choose_gate with no pick open refused: $(cat "$OUT/call-closed.json")" || fail "closed-pick call: $(cat "$OUT/call-closed.json" 2>/dev/null)"
if [ -f "$OUT/call-noncand.json" ]; then
  if grep -q 'no gate pick is open' "$OUT/call-noncand.json"; then
    echo "SKIP app-api call landed after the pick closed: $(head -1 "$OUT/call-noncand.json")"
  else
    grep -q 'is not one of the line' "$OUT/call-noncand.json" && grep -q 'go test ./...' "$OUT/call-noncand.json" && grep -q 'GET /sum' "$OUT/call-noncand.json" \
      && ok "non-candidate app-api refused with the candidate list: $(head -1 "$OUT/call-noncand.json")" || fail "app-api call: $(cat "$OUT/call-noncand.json")"
  fi
else
  echo "SKIP pick window not caught; no app-api call"
fi
for g in 160x50 120x40 80x24; do
  f="$OUT/frames/pick-$g.txt"
  [ -s "$f" ] || { echo "SKIP no pick frame at $g"; continue; }
  w=$(python3 -c 'import sys; print(max(len(l) for l in open(sys.argv[1], encoding="utf-8").read().split("\n")))' "$f")
  gw=${g%x*}
  [ "$w" -le "$gw" ] && grep -q 'PHASE 1' "$f" && ok "pick frame $g fits (max width $w) and names PHASE 1" || fail "pick frame $g: width $w, PHASE 1 $(grep -c 'PHASE 1' "$f")"
done
[ ! -s "$OUT/frames/errors.txt" ] || fail "resize errors: $(cat "$OUT/frames/errors.txt")"

grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited --status 0 > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "stop --expect-exited" || fail "stop --expect-exited rc=$src"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
