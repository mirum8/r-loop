#!/bin/sh
# Live: issues.md --unattended fixes #1 only. #1 ticked, #2 stays ticked, #3 struck and untouched;
# the land gate's item test is red at base and green after; go test ./... green; tree clean; q quits.
# The run gets a herdr workspace of its own, so its watchdog never splits the maintainer's pane.
# Usage (from the repo root, inside a herdr pane): sh .claude/skills/test-app/e2e/live_issues.sh
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2700}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
cp issues.md "$OUT/issues.before.md"
base=$(git rev-parse HEAD)

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-issues}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
WSJ=$(herdr workspace create --cwd "$S" --label "r-loop test issues" --no-focus)
WS=$(printf '%s' "$WSJ" | jq -r '.result.workspace.workspace_id')
P=$(printf '%s' "$WSJ" | jq -r '.result.root_pane.pane_id')
[ -n "$P" ] && [ "$P" != null ] || { echo "FAIL could not create the test workspace"; exit 1; }
echo "workspace $WS ($P)"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$P" "$BIN" issues.md --unattended) \
  || { echo "FAIL start rc=$?"; herdr workspace close "$WS" > /dev/null 2>&1; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; herdr workspace close "$WS" > /dev/null 2>&1' EXIT
echo "handle  $H"

start=$(date +%s)
last=$start
final=""
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  now=$(date +%s)
  [ $((now - start)) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  if [ $((now - last)) -ge 300 ]; then
    cp "$("$TUI" capture "$H")" "$OUT/frame-$((now - start)).txt" 2>/dev/null
    last=$now
  fi
  sleep 15
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
cp "$EV" "$OUT/events.jsonl"
cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

phases=$(jq -r 'select(.Event.Kind=="phase-start") | .Event.Phase' "$EV" | sort -u | tr '\n' ' ')
[ "$(printf '%s' "$phases" | wc -w | tr -d ' ')" = 1 ] && ok "one phase ran: $phases" || fail "phases started: '$phases'"

stages=$(jq -r 'select(.Event.Kind=="land-stage") | .Event.Fields.stage' "$EV" | tr '\n' ',')
case "$stages" in *"red check,"*gate,*) ok "land gate ran the red check, then the gate: $stages" ;;
  *) fail "land stages: '$stages'" ;; esac
item=$(jq -r 'select(.Event.Kind=="land-stage" and .Event.Fields.stage=="red check") | .Event.Fields.command' "$EV" | head -1)
gate=$(jq -r 'select(.Event.Kind=="land-stage" and .Event.Fields.stage=="gate") | .Event.Fields.command' "$EV" | tail -1)
echo "     item test: $item"
echo "     gate:      $gate"
[ -n "$item" ] && case "$gate" in "$item && "*) true ;; *) false ;; esac \
  && ok "gate runs the item test first, then the suite" || fail "gate '$gate' does not start with the item test '$item'"
jq -e 'select(.Event.Kind=="landed")' "$EV" > /dev/null && ok "landed event" || fail "no landed event"
bad=$(jq -c 'select(.Event.Kind=="error" or .Event.Kind=="halt" or .Event.Kind=="stuck")' "$EV" | head -3)
[ -z "$bad" ] && ok "no error/halt/stuck events" || fail "bad events: $bad"

git log --oneline "$base..HEAD" > "$OUT/newcommits.txt"
[ -s "$OUT/newcommits.txt" ] && ok "new commits: $(tr '\n' ';' < "$OUT/newcommits.txt")" || fail "no commit after base"

if [ -n "$item" ]; then
  RW=$(mktemp -d)
  git worktree add -q --detach "$RW" "$base" 2> "$OUT/redwt.err"
  for t in $(git diff --name-only "$base" HEAD -- '*_test.go'); do mkdir -p "$RW/$(dirname "$t")"; git show "HEAD:$t" > "$RW/$t"; done
  (cd "$RW" && sh -c "$item") > "$OUT/item-at-base.txt" 2>&1; rcb=$?
  git worktree remove --force "$RW" > /dev/null 2>&1; rm -rf "$RW"
  [ "$rcb" != 0 ] && ok "item test red at base with the new tests (exit $rcb)" || fail "item test passes at base: $(tail -3 "$OUT/item-at-base.txt")"
  sh -c "$item" > "$OUT/item-after.txt" 2>&1; rca=$?
  [ "$rca" = 0 ] && ok "item test green after" || fail "item test red after: $(tail -3 "$OUT/item-after.txt")"
fi
go test ./... > "$OUT/gotest.txt" 2>&1 && ok "go test ./... green" || fail "go test ./...: $(tail -5 "$OUT/gotest.txt")"

grep -qE '^- \[x\] \[#1\]' issues.md && ok "#1 ticked" || fail "#1 not ticked: $(grep '#1' issues.md)"
grep -qxF -- "$(grep '\[#2\]' "$OUT/issues.before.md")" issues.md && ok "#2 line unchanged (still ticked)" || fail "#2 changed: $(grep '#2' issues.md)"
grep -qxF -- "$(grep '\[#3\]' "$OUT/issues.before.md")" issues.md && ok "#3 line unchanged (struck, unticked)" || fail "#3 changed: $(grep '#3' issues.md)"
diff "$OUT/issues.before.md" issues.md > "$OUT/issues.diff"
dl=$(grep -c '^[<>]' "$OUT/issues.diff")
[ "$dl" -le 2 ] && ok "issues.md diff touches only #1's line ($dl lines)" || fail "issues.md diff: $(tr '\n' ';' < "$OUT/issues.diff")"

git status --porcelain > "$OUT/porcelain.txt"
[ -s "$OUT/porcelain.txt" ] && fail "tree not clean: $(tr '\n' ';' < "$OUT/porcelain.txt")" || ok "tree clean"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))"
herdr workspace close "$WS" > /dev/null 2>&1
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
[ "$fails" = 0 ] && rm -rf "$S"
exit $((fails > 0))
