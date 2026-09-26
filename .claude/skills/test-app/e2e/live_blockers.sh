#!/bin/sh
# usage: live_blockers.sh A|B|C
#   A  ADR-81 happy path: codex plan reviewer runs review-plan as a prompt, codex implement reviewer runs /review in its pane
#   B  ADR-82 reviewer blocker: codex reviewStart never appears; the unattended watchdog resolves the blocker, the phase blocks (exit 1)
#   C  ADR-82 land blocker: the Done-when gate is red and land.fixRounds is 0; the watchdog resolves it (block/stop)
set -u
MODE="${1:-A}"
case "$MODE" in A|B|C) ;; *) echo "usage: $0 A|B|C"; exit 2 ;; esac
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3000}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/agents" "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-blockers-$MODE-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

python3 - "$MODE" "$ROOT/internal/providers/shipped/codex.yaml" <<'EOF'
import sys
mode, shipped = sys.argv[1:]
p = ".r-loop/config.yaml"
s = open(p).read()
w = "  blockerTimeout: 3m\n"
if mode == "C":
    s = s.replace("land:\n  gateTimeout: 5m\n", "land:\n  fixRounds: 0\n  gateTimeout: 5m\n")
    s = s.replace("  implement:\n    provider: claude\n    model: sonnet\n    effort: low\n    timeout: 30m\n    fallback:\n      provider: codex\n      model: gpt-5.6-sol\n      effort: medium\n    reviewers:\n      - provider: codex\n        model: gpt-5.6-sol\n        effort: medium\n",
                  "  implement:\n    provider: claude\n    model: sonnet\n    effort: low\n    timeout: 30m\n    fallback:\n      provider: codex\n      model: gpt-5.6-sol\n      effort: medium\n    reviewers:\n      - provider: claude\n        model: sonnet\n        effort: low\n")
s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n" + w)
if mode == "B":
    block = "providers:\n  codex:\n"
    for line in open(shipped):
        if line.startswith("reviewStart:"):
            line = 'reviewStart: ">> Never started"\n'
        block += "    " + line
    s += block
open(p, "w").write(s)
if mode == "C":
    t = "docs/plan/todo-tiny.md"
    d = open(t).read()
    d = d.replace("**Done when:** `go test ./...` is green.", "**Done when:** `go test ./...` is green and `pwd | grep -q /.r-loop/wt/` passes.")
    open(t, "w").write(d)
EOF
cp .r-loop/config.yaml "$OUT/config.yaml"
cp docs/plan/todo-tiny.md "$OUT/todo-tiny.md"
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_blockers $MODE setup"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run after config edit"; cat "$OUT/dryrun.out"; exit 1; }
grep -qx 'prompt review-plan: embedded' "$OUT/dryrun.out" && ok "dry-run lists prompt review-plan" || fail "dry-run has no 'prompt review-plan: embedded'"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-blockers}-$MODE"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4200}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frames/start-120x40.txt"

start=$(date +%s)
n=0
final=""
review_framed=""
blocker_framed=""
while :; do
  n=$((n + 1))
  herdr agent list > "$OUT/agents/list.json" 2>/dev/null
  python3 - "$S" "$OUT/agents/list.json" > "$OUT/agents/ours.txt" <<'EOF'
import json, sys
root = sys.argv[1].rstrip("/")
for a in json.load(open(sys.argv[2]))["result"]["agents"]:
    cwd = a.get("cwd", "") + " " + a.get("foreground_cwd", "")
    if root in cwd:
        print(a["pane_id"], a.get("agent", "?"), a.get("agent_status", "?"))
EOF
  while read -r pane agent st; do
    echo "$(date +%T) $n $pane $agent $st" >> "$OUT/agents/timeline.txt"
    f="$OUT/agents/$(echo "$pane" | tr ':' '_')-$n.txt"
    herdr agent read "$pane" --source visible > "$f" 2>/dev/null
    hit=""
    grep -q -e '/review' -e 'Code review' -e 'review-plan' -e 'codex exec review' "$f" && hit=1
    if [ -n "$hit" ]; then
      echo "$(date +%T) $n $pane $agent $st $(grep -o -e '>> Code review started' -e '<< Code review finished' -e '/review Review the current' -e 'codex exec review' "$f" | sort -u | tr '\n' ';')" >> "$OUT/review-hits.txt"
    else
      rm -f "$f"
    fi
  done < "$OUT/agents/ours.txt"
  EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
  if [ -n "$EV" ]; then
    if [ -z "$review_framed" ] && grep -q '"Kind":"review-running"' "$EV"; then
      review_framed=1
      cp "$("$TUI" capture "$H")" "$OUT/frames/review-running-120x40.txt"
    fi
    if [ -z "$blocker_framed" ] && grep -q '"Kind":"blocked-on"' "$EV"; then
      blocker_framed=1
      sleep 1
      cp "$("$TUI" capture "$H")" "$OUT/frames/blocker-120x40.txt"
      cp "$("$TUI" capture "$H" --ansi)" "$OUT/frames/blocker-120x40.ansi"
      "$BIN" status --plain > "$OUT/status-open.txt" 2>&1
    fi
  fi
  [ $((n % 4)) = 1 ] || { sleep 3; continue; }
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  sleep 3
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 5
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
ls -R "$RUN" > "$OUT/rundir.txt"
python3 - "$EV" > "$OUT/key-events.txt" <<'EOF'
import json, sys
keep = {"review-round", "review-find", "review-running", "review-ran", "blocked-on", "blocker-resolved", "watchdog-call",
        "stalled", "halt", "blocked", "landed", "warning", "restart", "restart-refused", "reviewer-skipped", "gate", "run"}
for line in open(sys.argv[1]):
    r = json.loads(line)
    if r.get("Kind") == "step":
        st = r.get("Step") or {}
        print("state", st.get("Phase"), st.get("Kind"), st.get("Attempt"), r.get("State"), (r.get("Reason") or "")[:200])
    e = r.get("Event") or {}
    if e.get("Kind") in keep or "block" in (e.get("Kind") or ""):
        print(e.get("Kind"), e.get("Phase"), e.get("Step"), json.dumps(e.get("Fields") or {}, ensure_ascii=False)[:400])
EOF

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

ev() { grep "\"Kind\":\"$1\"" "$EV"; }
blocked=$(ev blocked-on | wc -l | tr -d ' ')
stalled_workers=$(grep '"State":"stalled"' "$EV" | wc -l | tr -d ' ')

case "$MODE" in
  A)
    case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
    ev review-find | grep '"Step":"plan"' | grep -q '"command":"prompt review-plan"' \
      && ok "plan review-find names prompt review-plan: $(ev review-find | grep '"Step":"plan"' | head -1 | grep -o '"Fields":{[^}]*}')" \
      || fail "no plan review-find with command 'prompt review-plan': $(ev review-find | head -2)"
    ev review-running | grep -q '"Step":"plan"' && fail "review-running on the plan step" || ok "no review-running on the plan step"
    grep -q 'codex exec review' "$OUT/review-hits.txt" 2>/dev/null && fail "'codex exec review' seen in a pane" || ok "no 'codex exec review' in any pane"
    ev review-running | grep -q '"Step":"implement"' && ok "review-running on implement: $(ev review-running | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no review-running on implement"
    ev review-ran | grep -q '"Step":"implement"' && ok "review-ran on implement" || fail "no review-ran on implement"
    grep -q '>> Code review started' "$OUT/review-hits.txt" 2>/dev/null && ok "a pane showed '>> Code review started'" || fail "no pane showed '>> Code review started' (polling may have missed it)"
    grep -q '<< Code review finished' "$OUT/review-hits.txt" 2>/dev/null && ok "a pane showed '<< Code review finished'" || fail "no pane showed '<< Code review finished' (polling may have missed it)"
    NR=$(ls "$RUN"/phase-1/implement-rv-*/native-review.txt 2>/dev/null | head -1)
    [ -n "$NR" ] && [ -s "$NR" ] && ok "native-review.txt: $NR ($(wc -c < "$NR") bytes)" && cp "$NR" "$OUT/native-review.txt" || fail "no non-empty implement native-review.txt under $RUN/phase-1"
    ev review-find | grep '"Step":"implement"' | grep -q '"state":"ok"' && ok "implement review-find ok: $(ev review-find | grep '"Step":"implement"' | head -1 | grep -o '"Fields":{[^}]*}')" || fail "implement review-find not ok"
    [ "$blocked" = 0 ] && ok "zero blocked-on events" || fail "$blocked blocked-on events: $(ev blocked-on | head -2)"
    git log --oneline > "$OUT/gitlog.txt"
    grep -q 'phase 1' "$OUT/gitlog.txt" && ok "phase commit landed: $(grep 'phase 1' "$OUT/gitlog.txt" | head -1)" || fail "no phase commit"
    grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "todo-tiny.md not ticked"
    ;;
  B|C)
    case "$final" in *timeout*|*"app gone"*) fail "run did not end cleanly: $final" ;; *) ok "run ended: $final" ;; esac
    if [ "$MODE" = B ]; then want_src=reviewer; else want_src=land; fi
    [ "$blocked" -ge 1 ] && ok "$blocked blocked-on event(s): $(ev blocked-on | head -1 | grep -o '"Fields":{[^}]*}')" || fail "no blocked-on event"
    if [ "$MODE" = B ]; then
      ev blocked-on | grep '"source":"reviewer"' | grep -q '"step":"implement-rv-codex' && ok "blocked-on source reviewer, step implement-rv-codex" || fail "no blocked-on with source reviewer on implement-rv-codex"
      ev blocked-on | grep -q 'review never started\|did not start' && ok "reason names the start wait: $(ev blocked-on | head -1 | grep -o '"reason":"[^"]\{0,120\}')" || fail "blocked-on reason does not name the start wait"
    else
      ev blocked-on | grep -q -e '"source":"land"' -e '"source":"gatefix"' -e '"source":"gate-probe"' && ok "blocked-on from land/gatefix/gate-probe: $(ev blocked-on | grep -o '"source":"[^"]*"' | sort -u | tr '\n' ' ')" || fail "no land-side blocked-on: $(ev blocked-on | grep -o '"source":"[^"]*"' | tr '\n' ' ')"
    fi
    ev watchdog-call | grep -q resolve_blocker && ok "watchdog-call resolve_blocker: $(ev watchdog-call | grep resolve_blocker | grep -o '"Fields":{[^}]*}' | cut -c1-200 | tr '\n' ' ')" || fail "no watchdog-call resolve_blocker"
    res=$(ev blocker-resolved | wc -l | tr -d ' ')
    [ "$res" -ge 1 ] && ok "$res blocker-resolved: $(ev blocker-resolved | grep -o '"Fields":{[^}]*}' | tr '\n' ' ')" || fail "no blocker-resolved"
    [ "$res" = "$blocked" ] && ok "every blocker resolved ($res/$blocked)" || fail "blocked-on $blocked vs resolved $res"
    grep -q '^b[0-9]* phase-1/.*→' "$OUT/status.txt" && ok "status lists: $(grep '^b[0-9]* phase-1/' "$OUT/status.txt" | sed 's/: .*→/ … →/' | tr '\n' ';')" || fail "status has no bN line"
    grep -q 'b[0-9]* phase-1/.*→' "$OUT/report.md" 2>/dev/null && ok "report lists: $(grep 'b[0-9]* phase-1/.*→' "$OUT/report.md" | tr '\n' ';')" || fail "report.md has no bN line"
    grep -q 'watchdog · b1' "$OUT/frames/blocker-120x40.txt" 2>/dev/null && ok "TUI row shows 'watchdog · b1' while open" || fail "no 'watchdog · b1' in the blocker frame"
    [ "$stalled_workers" = 0 ] && ok "no stalled step state" || fail "$stalled_workers stalled step states"
    last=$(ev blocker-resolved | tail -1 | grep -o '"action":"[^"]*"' | cut -d'"' -f4)
    case "$last" in
      block) want=1 ;; stop) want=5 ;; *) want="?" ;;
    esac
    [ "$code" = "$want" ] && ok "exit $code matches the last action $last" || fail "exit $code, last action $last (want $want)"
    ;;
esac

grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off ($(cat "$OUT/tui-status.txt"))" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited > /dev/null 2>&1; src=$?
[ "$MODE" = A ] && { [ "$src" = 0 ] && ok "stop --expect-exited 0" || fail "stop --expect-exited rc=$src"; }
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
