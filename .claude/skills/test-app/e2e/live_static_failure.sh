#!/bin/sh
# usage: live_static_failure.sh
#   The static analyzer reviewer fails: analyze.timeout 1s makes golangci-lint (go run) time out on the
#   implement review round. The driver records review-find static failed and raises a reviewer blocker to the
#   unattended watchdog; whatever it picks (retry/skip/block/stop) must have its contracted consequence.
set -u
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
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-staticfail-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

python3 - <<'EOF'
p = ".r-loop/config.yaml"
s = open(p).read()
s = s.replace("label: test\n", "label: tsfail\n")
s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n  blockerTimeout: 3m\n")
s += "analyze:\n  timeout: 1s\n"
open(p, "w").write(s)
EOF
cp .r-loop/config.yaml "$OUT/config.yaml"
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_static_failure setup"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run after config edit"; cat "$OUT/dryrun.out"; exit 1; }
grep -q '^static: .*go' "$OUT/dryrun.out" && ok "dry-run: $(grep '^static:' "$OUT/dryrun.out")" || fail "dry-run has no 'static: ... go' line"
grep -q '^label: tsfail' "$OUT/dryrun.out" && ok "dry-run label tsfail" || fail "dry-run label is not tsfail"
grep -q 'analyze' "$OUT/dryrun.out" && echo "INFO dry-run mentions analyze: $(grep analyze "$OUT/dryrun.out")" || echo "INFO dry-run shows no analyze.timeout provenance"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-staticfail}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4200}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

(
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    if [ -n "$EV" ] && grep '"Kind":"blocked-on"' "$EV" | grep -q '"step":"implement-rv-static'; then
      sleep 0.3
      cp "$("$TUI" capture "$H")" "$OUT/frames/blocker-120x40.txt"
      cp "$("$TUI" capture "$H" --ansi)" "$OUT/frames/blocker-120x40.ansi"
      "$BIN" status --plain > "$OUT/status-open.txt" 2>&1
      pgrep -fl golangci-lint > "$OUT/pgrep-open.txt" 2>&1
      "$TUI" resize "$H" 80x24 > /dev/null && sleep 0.5 && cp "$("$TUI" capture "$H")" "$OUT/frames/blocker-80x24.txt"
      "$TUI" resize "$H" 120x40 > /dev/null
      break
    fi
    sleep 0.2
  done
) &
blocker_watch=$!

start=$(date +%s)
final=""
while :; do
  sleep 10
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
done
kill "$blocker_watch" 2>/dev/null
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 5
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1
pgrep -fl golangci-lint > "$OUT/pgrep-end.txt" 2>&1
ps -axo pid,command | grep -e "$S" -e golangci | grep -v grep > "$OUT/ps-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
ls -R "$RUN" > "$OUT/rundir.txt"
python3 - "$EV" > "$OUT/key-events.txt" <<'EOF'
import json, sys
keep = {"review-round", "review-find", "blocked-on", "blocker-resolved", "watchdog-call", "halt", "blocked",
        "landed", "warning", "reviewer-skipped", "review-clean", "run"}
for line in open(sys.argv[1]):
    r = json.loads(line)
    if r.get("Kind") == "step":
        st = r.get("Step") or {}
        print("state", st.get("Phase"), st.get("Kind"), st.get("Attempt"), r.get("State"), (r.get("Reason") or "")[:200])
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
case "$final" in *timeout*|*"app gone"*) fail "run did not end cleanly: $final" ;; *) ok "run ended: $final" ;; esac

ev review-find | grep '"Step":"plan"' | grep -q '"reviewer":"static"' && fail "plan (non-diff) round ran static" || ok "no static review-find on the plan step"
sf=$(ev review-find | grep '"Step":"implement"' | grep '"reviewer":"static"' | grep '"state":"failed"' | head -1)
[ -n "$sf" ] && ok "implement review-find static failed: $(echo "$sf" | grep -o '"Fields":{[^}]*}')" || fail "no implement review-find static failed: $(ev review-find | grep static | head -2)"

sb=$(ev blocked-on | grep '"step":"implement-rv-static"')
nb=$(echo "$sb" | grep -c . | tr -d ' ')
[ -n "$sb" ] && ok "$nb blocked-on for implement-rv-static, source reviewer: $(echo "$sb" | head -1 | grep -o '"source":"[^"]*"')" || fail "no blocked-on with step implement-rv-static: $(ev blocked-on | head -2)"
echo "$sb" | head -1 | grep -q '"source":"reviewer"' || fail "static blocker source is not reviewer"
echo "$sb" | head -1 | grep -q 'timed out after 1s' && ok "reason: $(echo "$sb" | head -1 | grep -o '"reason":"[^"]\{0,140\}')" || fail "static blocker reason lacks 'timed out after 1s'"
echo "$sb" | head -1 | grep -q -e 'golangci-lint' -e 'govulncheck' -e 'semgrep' && ok "reason names a tool" || fail "static blocker reason names no tool"
ev watchdog-call | grep -q resolve_blocker && ok "watchdog-call resolve_blocker" || fail "no watchdog-call resolve_blocker"

ids=$(echo "$sb" | grep -o '"id":"b[0-9]*"' | cut -d'"' -f4)
actions=""
for id in $ids; do
  a=$(ev blocker-resolved | grep "\"id\":\"$id\"" | grep -o '"action":"[^"]*"' | head -1 | cut -d'"' -f4)
  actions="$actions $id=$a"
done
echo "INFO static blocker actions:$actions"
last=$(echo "$actions" | awk '{print $NF}' | cut -d= -f2)
attempts=$(ev review-find | grep '"Step":"implement"' | grep '"reviewer":"static"' | grep -c '"state":"failed"' | tr -d ' ')
echo "$actions" | grep -q '=retry' && { [ "$attempts" -ge 2 ] && ok "retry re-ran analyze: $attempts static failures" || fail "retry chosen but only $attempts static failure(s)"; }
FS=$(ls "$RUN"/phase-1/*findings-static-r1.json 2>/dev/null)
case "$last" in
  skip)
    ev reviewer-skipped | grep -q '"reviewer":"static"' && ok "reviewer-skipped static: $(ev reviewer-skipped | grep static | grep -o '"Fields":{[^}]*}' | cut -c1-200)" || fail "skip chosen, no reviewer-skipped static"
    [ -z "$FS" ] && ok "no *-findings-static-r1.json" || fail "static findings file exists after skip: $FS"
    ev review-find | grep '"Step":"implement"' | grep -q '"reviewer":"codex"' && ok "codex reviewer still reported: $(ev review-find | grep '"Step":"implement"' | grep '"reviewer":"codex"' | head -1 | grep -o '"Fields":{[^}]*}')" || fail "no codex review-find on implement after skip"
    case "$final" in *finished*) ok "run finished after skip" ;; *) fail "skip chosen but run $final" ;; esac
    LM=$(ev landed | grep -o '"merge":"[0-9a-f]*"' | head -1 | cut -d'"' -f4)
    [ -n "$LM" ] && ok "phase landed: $(git log -1 --format='%h %s' "$LM")" || fail "no landed event"
    git log --name-only --format= | grep -i static && fail "static-related file committed" || ok "nothing static-related committed ($(git log --name-only --format= | sort -u | tr '\n' ' '))"
    want=0 ;;
  stop) case "$final" in *halted*) ok "stop halted the run" ;; *) fail "stop chosen but run $final" ;; esac; want=5 ;;
  block) grep -q "blocked" "$OUT/status.txt" && ok "block: status shows blocked" || fail "block chosen, status not blocked"; want=1 ;;
  *) fail "no resolved action for the static blocker"; want="?" ;;
esac
[ "$code" = "$want" ] && ok "exit $code matches action $last" || fail "exit $code, action $last (want $want)"
grep -qi static "$OUT/status.txt" && ok "status mentions static: $(grep -i static "$OUT/status.txt" | head -2 | tr '\n' ';')" || fail "status --plain says nothing about static"
grep -qi static "$OUT/report.md" 2>/dev/null && ok "report mentions static: $(grep -i static "$OUT/report.md" | head -3 | tr '\n' ';')" || fail "report.md says nothing about static"
[ -s "$OUT/pgrep-end.txt" ] && fail "golangci-lint still running: $(cat "$OUT/pgrep-end.txt")" || ok "no golangci-lint process left"

grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off ($(cat "$OUT/tui-status.txt"))" || fail "after q: $(cat "$OUT/tui-status.txt")"
if [ "$want" = 0 ]; then "$TUI" stop "$H" --expect-exited > /dev/null 2>&1; else "$TUI" stop "$H" --expect-exited --status "$want" > /dev/null 2>&1; fi
src=$?
[ "$src" = 0 ] && ok "stop --expect-exited (status $want) 0" || fail "stop --expect-exited rc=$src"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
