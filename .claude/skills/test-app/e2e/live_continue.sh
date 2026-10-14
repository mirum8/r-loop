#!/bin/sh
# usage: live_continue.sh U|A
#   resolve_blocker continue on a land blocker: the Done-when gate is ./gate.sh, green in a step worktree and red on the
#   primary checkout until a marker file outside the repo exists; land.fixRounds is 0, so the red gate raises a land
#   blocker. The script creates the marker and calls resolve_blocker continue itself; the driver re-reads the phase,
#   asks the watchdog for the gate again (choose_gate) and re-lands in the same process.
#   U  unattended: skip is not a land action, fix is refused "unattended: continue, block or stop", continue is authorised
#   A  attended: continue without maintainer_said is ask, with maintainer_said is authorised; the script answers every
#      other question the test watchdog asks (Enter on its recommended option), never a person
set -u
MODE="${1:-U}"
case "$MODE" in U|A) ;; *) echo "usage: $0 U|A"; exit 2 ;; esac
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
E2E="$ROOT/.claude/skills/test-app/e2e"
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3600}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-continue-$MODE-$RAND"
MARKER="$S.green"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

cat > gate.sh <<EOF
#!/bin/sh
go test ./... || exit 1
case "\$PWD" in */.r-loop/wt/*) exit 0 ;; esac
[ -f "$MARKER" ] || { echo "gate: red on the primary checkout"; exit 1; }
EOF
chmod +x gate.sh
python3 - <<'EOF'
p = ".r-loop/config.yaml"
s = open(p).read()
old = "land:\n  gateTimeout: 5m\n"
assert old in s
s = s.replace(old, "land:\n  fixRounds: 0\n  gateTimeout: 5m\n")
s = s.replace("  implement:\n    provider: claude\n    model: sonnet\n    effort: low\n    timeout: 30m\n    fallback:\n      provider: codex\n      model: gpt-5.6-sol\n      effort: medium\n    reviewers:\n      - provider: codex\n        model: gpt-5.6-sol\n        effort: medium\n",
              "  implement:\n    provider: claude\n    model: sonnet\n    effort: low\n    timeout: 30m\n    fallback:\n      provider: codex\n      model: gpt-5.6-sol\n      effort: medium\n    reviewers:\n      - provider: claude\n        model: sonnet\n        effort: low\n")
s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n  blockerTimeout: 20m\n")
open(p, "w").write(s)
t = "docs/plan/todo-tiny.md"
d = open(t).read()
old = "**Done when:** `go test ./...` is green."
assert old in d
open(t, "w").write(d.replace(old, "**Done when:** `./gate.sh` passes."))
EOF
cp .r-loop/config.yaml "$OUT/config.yaml"
cp docs/plan/todo-tiny.md "$OUT/todo-tiny.md"
git add gate.sh
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_continue $MODE setup"
./gate.sh > /dev/null 2>&1 && { echo "FAIL gate.sh is green on the primary checkout before the marker"; exit 1; }
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run after setup"; cat "$OUT/dryrun.out"; exit 1; }
ok "setup: gate.sh red on the primary checkout, dry-run valid"

FLAGS=""
[ "$MODE" = U ] && FLAGS="--unattended"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-continue}-$MODE"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4800}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md $FLAGS) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; rm -f "$MARKER"' EXIT
echo "handle  $H"

has() { [ -n "$EV" ] && grep -q "\"Kind\":\"$1\"" "$EV"; }
wd() { "$E2E/wd_call.sh" "$WDURL" "$@"; }

(
  stage=0
  answered=0
  WDURL=""
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    if [ -z "$WDURL" ] && has watchdog-placed; then
      WDURL=$("$E2E/wd_url.sh" "$S" 2>> "$OUT/wdurl.err") && echo "$WDURL" > "$OUT/wdurl.txt"
      cp .r-loop/runs/current "$OUT/current-start.txt" 2>/dev/null
    fi
    if [ $stage = 0 ] && [ -n "$WDURL" ] && grep '"Kind":"blocked-on"' "$EV" 2>/dev/null | grep -q '"source":"land"'; then
      stage=1
      BID=$(grep '"Kind":"blocked-on"' "$EV" | grep '"source":"land"' | head -1 | sed 's/.*"id":"\(b[0-9]*\)".*/\1/')
      echo "$BID" > "$OUT/bid.txt"
      cp .r-loop/runs/current "$OUT/current-blocker.txt" 2>/dev/null
      wd resolve_blocker '{"id":"'"$BID"'","action":"skip"}' > "$OUT/call-skip.json" 2>&1
      if [ "$MODE" = U ]; then
        wd resolve_blocker '{"id":"'"$BID"'","action":"fix","addendum":"the gate is red"}' > "$OUT/call-fix.json" 2>&1
        touch "$MARKER"
        wd resolve_blocker '{"id":"'"$BID"'","action":"continue"}' > "$OUT/call-continue.json" 2>&1
      else
        touch "$MARKER"
        wd resolve_blocker '{"id":"'"$BID"'","action":"continue"}' > "$OUT/call-continue-nosaid.json" 2>&1
        wd resolve_blocker '{"id":"'"$BID"'","action":"continue","maintainer_said":"yes"}' > "$OUT/call-continue.json" 2>&1
      fi
      date +%T >> "$OUT/calls-at.txt"
      sleep 0.5
      cp "$("$TUI" capture "$H")" "$OUT/frames/continued-120x40.txt"
      "$BIN" status --plain > "$OUT/status-continued.txt" 2>&1
    fi
    if [ "$MODE" = A ] && [ -n "$EV" ]; then
      asked=$(grep -c '"Kind":"watchdog-waiting"' "$EV")
      if [ "$asked" -gt "$answered" ]; then
        answered=$asked
        WDPANE=$(grep '"Kind":"watchdog-placed"' "$EV" | head -1 | sed 's/.*"pane":"\([^"]*\)".*/\1/')
        sleep 3
        herdr pane read "$WDPANE" 2>/dev/null | tail -20 > "$OUT/question-$asked.txt"
        herdr pane send-keys "$WDPANE" enter >> "$OUT/answers.txt" 2>&1
        echo "question $asked answered with Enter in $WDPANE at $(date +%T)" >> "$OUT/answers.txt"
      fi
    fi
    if [ $stage = 1 ] && has landed; then
      stage=2
      cp .r-loop/runs/current "$OUT/current-landed.txt" 2>/dev/null
      read -r _ pid < .r-loop/runs/current 2>/dev/null && ps -p "$pid" -o pid=,command= > "$OUT/ps-landed.txt" 2>&1
    fi
    sleep 0.2
  done
) &
watcher=$!

start=$(date +%s)
n=0
final=""
while :; do
  n=$((n + 1))
  if [ $((n % 3)) = 1 ]; then
    "$BIN" status --plain > "$OUT/status.txt" 2>&1
    if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
      final=$(head -1 "$OUT/status.txt"); break
    fi
    st=$("$TUI" status "$H")
    case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
    [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  fi
  sleep 2
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 5
kill "$watcher" 2>/dev/null
wait "$watcher" 2>/dev/null
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/questions.jsonl" "$OUT/questions.jsonl" 2>/dev/null
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
git status --porcelain > "$OUT/porcelain-end.txt"
python3 - "$EV" > "$OUT/key-events.txt" <<'EOF'
import json, sys
keep = {"blocked-on", "blocker-resolved", "watchdog-call", "gate-chosen", "gate-skipped", "gate", "landed", "halt", "blocked",
        "warning", "restart", "restart-refused", "human", "watchdog-placed", "watchdog-waiting", "run"}
for line in open(sys.argv[1]):
    r = json.loads(line)
    e = r.get("Event") or {}
    if e.get("Kind") in keep:
        print(e.get("Kind"), e.get("Phase"), e.get("Step"), json.dumps(e.get("Fields") or {}, ensure_ascii=False)[:400])
EOF

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

ev() { grep "\"Kind\":\"$1\"" "$EV"; }
BID=$(cat "$OUT/bid.txt" 2>/dev/null)
case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
[ -n "$BID" ] && ok "land blocker $BID: $(ev blocked-on | grep '"source":"land"' | head -1 | grep -o '"Fields":{[^}]*}' | cut -c1-300)" || fail "no land blocked-on: $(ev blocked-on | grep -o '"Fields":{[^}]*}')"
python3 - "$OUT/questions.jsonl" "$BID" <<'EOF' && ok "blocker $BID options end with continue: $(cat "$OUT/options.txt")" || fail "blocker $BID options: $(cat "$OUT/options.txt" 2>/dev/null)"
import json, sys, os
opts = None
for line in open(sys.argv[1]):
    q = json.loads(line).get("Question") or {}
    if q.get("ID") == sys.argv[2]:
        opts = q.get("Options")
open(os.path.join(os.path.dirname(sys.argv[1]), "options.txt"), "w").write(json.dumps(opts))
assert opts and opts[-1] == "continue", opts
EOF
grep -q 'refused' "$OUT/call-skip.json" 2>/dev/null && grep -q 'retry, fix, block, stop or continue' "$OUT/call-skip.json" && ok "skip refused, listing the land actions: $(cat "$OUT/call-skip.json")" || fail "skip call: $(cat "$OUT/call-skip.json" 2>/dev/null)"
if [ "$MODE" = U ]; then
  grep -q '"decision":"refused"' "$OUT/call-fix.json" 2>/dev/null && grep -q 'unattended: continue, block or stop' "$OUT/call-fix.json" && ok "unattended fix refused: $(cat "$OUT/call-fix.json")" || fail "unattended fix: $(cat "$OUT/call-fix.json" 2>/dev/null)"
else
  grep -q '"decision":"ask"' "$OUT/call-continue-nosaid.json" 2>/dev/null && ok "attended continue without maintainer_said: $(cat "$OUT/call-continue-nosaid.json")" || fail "attended continue without maintainer_said: $(cat "$OUT/call-continue-nosaid.json" 2>/dev/null)"
fi
cont=$(cat "$OUT/call-continue.json" 2>/dev/null)
case "$cont" in
  *'"decision":"authorised"'*) ok "resolve_blocker continue by the script: $cont" ;;
  *) fail "resolve_blocker continue: $cont; resolved by: $(ev blocker-resolved | grep -o '"Fields":{[^}]*}')" ;;
esac
if [ "$MODE" = U ]; then want_cite=unattended; else want_cite=maintainer; fi
R=$(ev blocker-resolved | grep "\"id\":\"$BID\"" | head -1)
CITE=$(python3 -c 'import json,sys
c=""
for l in open(sys.argv[1]):
    q=json.loads(l).get("Question") or {}
    if q.get("ID")==sys.argv[2] and q.get("Citation"): c=q["Citation"]
print(c)' "$OUT/questions.jsonl" "$BID" 2>/dev/null)
echo "$R" | grep -q '"action":"continue"' && [ "$CITE" = "$want_cite" ] && ok "blocker-resolved continue, question citation $CITE: $(echo "$R" | grep -o '"Fields":{[^}]*}')" || fail "blocker-resolved for $BID: $R (citation '$CITE', want $want_cite)"
python3 - "$EV" "$BID" > "$OUT/order.txt" <<'EOF' && ok "order: $(cat "$OUT/order.txt")" || fail "event order: $(cat "$OUT/order.txt")"
import json, sys
seq = []
for line in open(sys.argv[1]):
    e = json.loads(line).get("Event") or {}
    k, f = e.get("Kind"), e.get("Fields") or {}
    if k == "blocked-on" and f.get("id") == sys.argv[2]: seq.append("blocked-on")
    elif k == "blocker-resolved" and f.get("id") == sys.argv[2]: seq.append("resolved:" + f.get("action", ""))
    elif k in ("gate-chosen", "gate-skipped", "landed"): seq.append(k)
    elif k == "human" and f.get("what") == "resume": seq.append("human/resume")
print(" ".join(seq))
i = seq.index("blocked-on")
assert seq.index("gate-chosen") < i, "no gate pick before the blocker"
tail = seq[i + 1:]
assert tail[0] == "resolved:continue", tail
assert tail[1] == "gate-chosen" and tail[2] == "landed", tail
assert "human/resume" not in seq
EOF
gc=$(ev gate-chosen | wc -l | tr -d ' ')
[ "$gc" -ge 2 ] && ok "$gc gate-chosen: $(ev gate-chosen | grep -o '"command":"[^"]*"' | tr '\n' ' ')" || fail "$gc gate-chosen events"
p0=$(cut -d' ' -f2 "$OUT/current-start.txt" 2>/dev/null); p1=$(cut -d' ' -f2 "$OUT/current-blocker.txt" 2>/dev/null); p2=$(cut -d' ' -f2 "$OUT/current-landed.txt" 2>/dev/null)
[ -n "$p0" ] && [ "$p0" = "$p1" ] && [ "$p1" = "$p2" ] && [ -s "$OUT/ps-landed.txt" ] && ok "same driver pid $p0 at start, blocker and landed (alive: $(cat "$OUT/ps-landed.txt" | cut -c1-80))" || fail "driver pid start=$p0 blocker=$p1 landed=$p2 ps=$(cat "$OUT/ps-landed.txt" 2>/dev/null)"
wp=$(ev watchdog-placed | wc -l | tr -d ' ')
[ "$wp" = 1 ] && ok "one watchdog-placed: $(ev watchdog-placed | grep -o '"pane":"[^"]*"')" || fail "$wp watchdog-placed events"
ev human | grep -q '"what":"resume"' && fail "a human/resume event" || ok "no human/resume event"
ev landed > /dev/null && ok "landed: $(ev landed | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no landed"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md ticked" || fail "todo-tiny.md not ticked"
[ ! -s "$OUT/porcelain-end.txt" ] && ok "primary tree clean" || fail "primary tree dirty: $(cat "$OUT/porcelain-end.txt")"
grep -q "watchdog · $BID" "$OUT/frames/continued-120x40.txt" 2>/dev/null && echo "info: continued frame still shows 'watchdog · $BID'"
[ "$code" = 0 ] && ok "exit 0" || fail "exit $code"
grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited --status 0 > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "stop --expect-exited --status 0" || fail "stop --expect-exited rc=$src"
trap - EXIT
rm -f "$MARKER"

[ "$MODE" = A ] && echo "questions answered by the script: $(grep -c answered "$OUT/answers.txt" 2>/dev/null || true)"
echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
