#!/bin/sh
# Live: ADR-92 design step. With pipeline [design, plan, implement] the triage flags phase 1 of todo-design.md (a CLI
# whose printed output is the deliverable) for design and leaves phase 2 (pure library logic) unflagged. Phase 1 runs
# design → plan → implement with the design file committed on its own and named in the plan prompt; phase 2 gets a
# step-skipped for design and no design session. The TUI leaves design out of phase 2's step row and logs the design line.
# The run gets a herdr workspace of its own, so its watchdog never splits the maintainer's pane; --unattended asks nothing.
# Usage (from the repo root, inside a herdr pane): sh .claude/skills/test-app/e2e/live_design.sh
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3600}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
note() { echo "NOTE $1"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh" "$HOME/r-loop-test-design-$$-$(date +%s)") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

awk '/^pipeline:$/ { print; print "  - design\n  - plan\n  - implement"; skip = 1; next }
  skip && /^  - / { next } { skip = 0; print }' .r-loop/config.yaml > .r-loop/config.new && mv .r-loop/config.new .r-loop/config.yaml
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_design setup"
base=$(git rev-parse HEAD)
"$BIN" docs/plan/todo-design.md --dry-run --plain > "$OUT/dryrun.out" 2>&1; rc=$?
[ "$rc" = 0 ] && grep -q '^phase 1 .* design → plan' "$OUT/dryrun.out" && ok "dry-run: design before plan" \
  || { fail "dry-run rc=$rc"; cat "$OUT/dryrun.out"; exit 1; }

touch "$OUT/start.marker"
export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-design}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-5400}"
RUNWS_JSON=$(herdr workspace create --cwd "$S" --label "r-loop test design" --no-focus)
RUNWS=$(printf '%s' "$RUNWS_JSON" | jq -r '.result.workspace.workspace_id')
RUNPANE=$(printf '%s' "$RUNWS_JSON" | jq -r '.result.root_pane.pane_id')
[ -n "$RUNPANE" ] && [ "$RUNPANE" != null ] || { echo "FAIL could not create the test workspace"; exit 1; }
echo "workspace $RUNWS ($RUNPANE)"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$RUNPANE" "$BIN" docs/plan/todo-design.md --unattended) \
  || { echo "FAIL start rc=$?"; herdr workspace close "$RUNWS" > /dev/null 2>&1; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; herdr workspace close "$RUNWS" > /dev/null 2>&1' EXIT
echo "handle  $H"

steps_of() { grep -A1 "PHASE $1 · " "$2" | grep -o 'steps .*' | head -1; }
sweep() {
  for g in 160x50 120x40 80x24; do
    "$TUI" resize "$H" "$g" || { fail "resize $g rc=$?"; continue; }
    sleep 2
    f=$("$TUI" capture "$H") && cp "$f" "$OUT/frames/sweep-$g.txt" || fail "capture $g rc=$?"
    f=$("$TUI" capture "$H" --ansi) && cp "$f" "$OUT/frames/sweep-$g.ansi" || fail "capture --ansi $g rc=$?"
  done
  "$TUI" resize "$H" 120x40 || fail "resize back to 120x40 rc=$?"
}

start=$(date +%s)
final=""
swept=0
p2bad=0
while :; do
  if f=$("$TUI" capture "$H" 2>/dev/null); then
    cp "$f" "$OUT/frames/cur.txt"
    if grep -q 'PHASE 1 · ' "$OUT/frames/cur.txt"; then
      st=$(steps_of 1 "$OUT/frames/cur.txt")
      case "$st" in *design*) [ -e "$OUT/frames/p1-design.txt" ] || cp "$OUT/frames/cur.txt" "$OUT/frames/p1-design.txt" ;; esac
      if [ "$swept" = 0 ] && grep -q 'PHASE 1 · design' "$OUT/frames/cur.txt"; then sweep; swept=1; fi
    fi
    if grep -q 'PHASE 2 · ' "$OUT/frames/cur.txt"; then
      st=$(steps_of 2 "$OUT/frames/cur.txt")
      case "$st" in
        *design*) p2bad=$((p2bad + 1)); cp "$OUT/frames/cur.txt" "$OUT/frames/p2-bad-$p2bad.txt" ;;
        ?*) [ -e "$OUT/frames/p2.txt" ] || cp "$OUT/frames/cur.txt" "$OUT/frames/p2.txt" ;;
      esac
    fi
    if grep -qE 'phase 1 ?: design( skipped)?: ' "$OUT/frames/cur.txt"; then
      [ -e "$OUT/frames/feed-design.txt" ] || cp "$OUT/frames/cur.txt" "$OUT/frames/feed-design.txt"
    fi
  fi
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  now=$(date +%s)
  [ $((now - start)) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  sleep 5
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frames/end.txt"

RUN=$(dirname "$(ls .r-loop/runs/*/events.jsonl | head -1)")
EV="$RUN/events.jsonl"
for f in events.jsonl triage.json triage.md report.md; do cp "$RUN/$f" "$OUT/$f" 2>/dev/null || note "no $f in $RUN"; done
"$BIN" status --plain > "$OUT/status-end.txt" 2>&1
jq -c 'select(.Event) | .Event' "$EV" > "$OUT/events.only.jsonl"
evk() { jq -c --arg k "$1" 'select(.Kind == $k)' "$OUT/events.only.jsonl"; }

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
grep -q '^- \[ \]' docs/plan/todo-design.md && fail "unticked items left: $(grep '^- \[ \]' docs/plan/todo-design.md | tr '\n' ';')" \
  || ok "every item in todo-design.md ticked"
[ -z "$(git status --porcelain)" ] && ok "tree clean" || fail "tree not clean: $(git status --porcelain | tr '\n' ';')"
go test ./... > "$OUT/gotest.txt" 2>&1 && ok "go test ./... green" || fail "go test ./... red: $(tail -3 "$OUT/gotest.txt")"

TJ="$OUT/triage.json"
d1=$(jq -c '[.. | objects | select((.phase|tostring) == "1" and has("status"))][0] | {design, design_note}' "$TJ" 2>/dev/null)
d2=$(jq -c '[.. | objects | select((.phase|tostring) == "2" and has("status"))][0] | {design, design_note}' "$TJ" 2>/dev/null)
echo "$d1" | jq -e '.design == true and ((.design_note // "") | length > 0)' > /dev/null 2>&1 \
  && ok "triage.json phase 1 design true: $d1" || fail "triage.json phase 1: $d1"
echo "$d2" | jq -e '.design == false' > /dev/null 2>&1 && ok "triage.json phase 2 design false: $d2" || fail "triage.json phase 2: $d2"
[ "$(grep -c '^  Design: ' "$OUT/triage.md")" = 2 ] && ok "triage.md: a Design: line per phase: $(grep '^  Design: ' "$OUT/triage.md" | tr '\n' '|')" \
  || fail "triage.md Design: lines: $(grep -c '^  Design: ' "$OUT/triage.md")"
summary=$(evk triage | jq -r '.Fields.summary // empty' | tail -1)
{ grep -q '1 to design' "$OUT/triage.md" || echo "$summary" | grep -q '1 to design'; } \
  && ok "triage summary says 1 to design: $summary" || fail "no '1 to design' in triage.md or summary: $summary"
rl=$(evk run-list | tail -1 | jq -r '.Fields.design // empty')
echo "$rl" | jq -e 'keys == ["1"] and (.["1"] | length > 0)' > /dev/null 2>&1 \
  && ok "run-list design names phase 1 only: $rl" || fail "run-list design field: $rl"

sk=$(evk step-skipped | jq -c 'select(.Phase == "2" and .Step == "design")')
[ -n "$sk" ] && ok "phase 2 step-skipped design: $(echo "$sk" | jq -c .Fields)" || fail "no step-skipped for phase 2 design"
n2=$(jq -c 'select((.Step.Phase == "2" and .Step.Kind == "design") or (.Event.Phase == "2" and .Event.Step == "design" and .Event.Kind != "step-skipped"))' "$EV" | wc -l | tr -d ' ')
[ "$n2" = 0 ] && ok "phase 2 has no design step or session record" || fail "phase 2 has $n2 design records"
jq -e -s 'any(.[]; .Step.Phase == "1" and .Step.Kind == "design" and .State == "ok")' "$EV" > /dev/null \
  && ok "phase 1 design step ended ok" || fail "phase 1 design step never ok: $(jq -c 'select(.Step.Phase == "1" and .Step.Kind == "design") | .State' "$EV" | tr '\n' ' ')"
de=$(evk design | jq -c 'select(.Phase == "1")')
ds=$(evk design-skipped | jq -c 'select(.Phase == "1")')
if [ -n "$de" ] && [ -n "$(echo "$de" | jq -r '.Fields.intent')" ]; then ok "design event, intent: $(echo "$de" | jq -r .Fields.intent)"
elif [ -n "$ds" ]; then note "design-skipped on the CLI phase, reason: $(echo "$ds" | jq -r .Fields.reason)"
else fail "phase 1 has neither a design nor a design-skipped event"; fi

DP=$(git log --all --format= --name-only | grep -E '^\.task-plans/phase-1-.*\.design\.md$' | sort -u | head -1)
if [ -n "$DP" ]; then
  first=$(git log --all --reverse --format=%H -- "$DP" | head -1)
  git show "$first:$DP" > "$OUT/design.md"
  last=$(grep -v '^$' "$OUT/design.md" | tail -1)
  if grep -q '^status: no-visual-change' "$OUT/design.md"; then
    grep -q '^## Evidence' "$OUT/design.md" && note "design file is the no-visual-change form with ## Evidence" || fail "no-visual-change design has no ## Evidence"
  else
    grep -q '^## States' "$OUT/design.md" && grep -q '^## Reuse map' "$OUT/design.md" && ok "$DP has ## States and ## Reuse map" \
      || fail "$DP lacks ## States or ## Reuse map"
    case "$last" in DESIGN-INTENT:*) ok "last line: $last" ;; *) fail "last line is not DESIGN-INTENT: $last" ;; esac
  fi
  touched=$(git show --format= --name-only "$first")
  [ "$touched" = "$DP" ] && ok "design commit $(git log -1 --format='%h %s' "$first") touched only $DP" \
    || fail "design commit $first touched: $(echo "$touched" | tr '\n' ' ')"
else
  fail "no .task-plans/phase-1-*.design.md in git history"
fi
pt=$(evk warning | jq -r '.Fields.reason' | grep -i 'design' | head -3)
[ -z "$pt" ] && ok "no warning naming the design file" || fail "warnings about design: $pt"

PROJ=$(echo "$S" | sed 's/[^A-Za-z0-9]/-/g')
hits=$( { grep -rlF "The design step decided what the user sees in" "$HOME/.claude/projects/$PROJ"* 2>/dev/null
  find "$HOME/.codex/sessions" -type f -newer "$OUT/start.marker" -exec grep -lF "The design step decided what the user sees in" {} + 2>/dev/null; } | head -3)
if [ -n "$hits" ]; then
  grep -hoF "$DP" $hits > /dev/null 2>&1 && ok "plan prompt named the design path $DP: $hits" || note "design block seen but path not matched: $hits"
else note "plan prompt's design block not observable in agent transcripts"; fi

[ -e "$OUT/frames/p1-design.txt" ] && ok "phase 1 step row shows design: $(steps_of 1 "$OUT/frames/p1-design.txt")" || fail "no frame with design in phase 1's step row"
if [ -e "$OUT/frames/p2.txt" ]; then ok "phase 2 step row without design: $(steps_of 2 "$OUT/frames/p2.txt")"; else note "phase 2 never shown live in the panel"; fi
[ "$p2bad" = 0 ] && ok "phase 2's row never showed design" || fail "phase 2 row showed design in $p2bad frames"
{ [ -e "$OUT/frames/feed-design.txt" ] || grep -qE 'design( skipped)?: ' "$OUT/frames/end.txt"; } \
  && ok "feed shows a design line: $(grep -hoE 'phase 1 ?: design( skipped)?: .*' "$OUT/frames/feed-design.txt" "$OUT/frames/end.txt" 2>/dev/null | head -1)" \
  || fail "no design: line in any feed frame"
if [ "$swept" = 1 ]; then
  for g in 160x50 120x40 80x24; do
    w=${g%x*} h=${g#*x}
    f="$OUT/frames/sweep-$g.txt"
    over=$(perl -CSD -ne 'chomp; $n++ if length($_) > '"$w"'; END { print $n + 0 }' "$f")
    rows=$(wc -l < "$f" | tr -d ' ')
    [ "$over" = 0 ] && [ "$rows" -le "$h" ] && ok "sweep $g: $rows rows, none wider than $w" || fail "sweep $g: $over over-wide rows, $rows rows"
    amber=$(grep -E '38;2;224;16[34];88' "$OUT/frames/sweep-$g.ansi" | perl -pe 's/\e\[[0-9;]*m//g' | head -3)
    [ -z "$amber" ] && ok "sweep $g: no amber (nothing waiting)" || note "sweep $g amber rows: $amber"
  done
else
  fail "geometry sweep never ran (no frame with PHASE 1 · design)"
fi

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))"
trap - EXIT
herdr workspace close "$RUNWS" > /dev/null 2>&1

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
