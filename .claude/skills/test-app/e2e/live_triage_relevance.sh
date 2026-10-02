#!/bin/sh
# Live: backlog triage skips a false claim as not-a-bug and an out-of-scope ask as not-relevant, each citing a real
# path:line; the real item carries an approach into the plan prompt, and the planner overrides a means that breaks ADR-1.
# Usage (from the repo root, inside a herdr pane): sh .claude/skills/test-app/e2e/live_triage_relevance.sh
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
mkdir -p "$OUT"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
note() { echo "NOTE $1"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh" "$HOME/r-loop-test-relevance-$$-$(date +%s)") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"
base=$(git rev-parse HEAD)

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-relevance}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-5400}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" issues-relevance.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

start=$(date +%s)
last=$start
swept=0
final=""
while :; do
  if [ "$swept" = 0 ] && ls .r-loop/runs/*/events.jsonl > /dev/null 2>&1 && "$TUI" wait-for "$H" "ITEMS" --timeout 60 > /dev/null; then
    f=$("$TUI" capture "$H") && cp "$f" "$OUT/frame-live-120x40.txt" || fail "capture 120x40 rc=$?"
    if "$TUI" resize "$H" 80x24; then
      sleep 2
      f=$("$TUI" capture "$H") && cp "$f" "$OUT/frame-live-80x24.txt" || fail "capture 80x24 rc=$?"
    else
      fail "resize 80x24 rc=$?"
    fi
    "$TUI" resize "$H" 120x40 || fail "resize back to 120x40 rc=$?"
    swept=1
  fi
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
  sleep 5
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

RUN=$(dirname "$(ls .r-loop/runs/*/events.jsonl | head -1)")
EV="$RUN/events.jsonl"
for f in events.jsonl triage.json triage.md gate.json report.md; do cp "$RUN/$f" "$OUT/$f" 2>/dev/null || note "no $f in $RUN"; done
"$BIN" status --plain > "$OUT/status-end.txt" 2>&1

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

skip_line() { grep '"triage-skipped"' "$EV" | grep -E "\"(item|phase|items|id)\":\"(#)?$1\"" | head -1; }
check_skip() {
  item=$1 want=$2
  line=$(skip_line "$item")
  [ -n "$line" ] || { fail "no triage-skipped event for item $item"; return; }
  echo "$line" > "$OUT/skip-$item.json"
  echo "$line" | grep -q "\"$want\"" && ok "item $item skipped as $want" || fail "item $item skip status not $want: $line"
  cite=$(echo "$line" | grep -oE '[A-Za-z0-9_./-]+\.[A-Za-z]+:[0-9]+' | head -1)
  [ -n "$cite" ] || { fail "item $item reason cites no path:line: $line"; return; }
  p=${cite%:*} n=${cite##*:}
  lines=$(git show "$base:$p" 2>/dev/null | wc -l | tr -d ' ')
  if [ -n "$lines" ] && [ "$lines" -ge "$n" ] && [ "$n" -ge 1 ]; then
    ok "item $item cites $cite: $(git show "$base:$p" | sed -n "${n}p")"
  else
    fail "item $item cites $cite, which does not exist at base ($p has ${lines:-0} lines)"
  fi
}
check_skip 1 not-a-bug
check_skip 2 not-relevant

grep -q '#3' "$OUT/triage.md" 2>/dev/null && grep -q 'Approach:' "$OUT/triage.md" \
  && ok "triage.md has item 3 and an Approach: line" || fail "triage.md lacks item 3 or Approach: ($OUT/triage.md)"
if command -v jq > /dev/null; then
  jq -e '[.. | objects | select(has("findings")) | .findings[] | select((.item|tostring|ltrimstr("#")) == "3") | select((.root_cause_or_scope // .RootCause // "") != "" and (.approach // "") != "")] | length > 0' \
    "$OUT/triage.json" > /dev/null 2>&1 && ok "triage.json group for item 3 has findings with root cause and approach" \
    || fail "triage.json group for item 3 lacks findings root_cause_or_scope/approach"
else
  grep -q '"findings"' "$OUT/triage.json" && ok "triage.json has findings (jq missing, shallow check)" || fail "triage.json has no findings"
fi

PLAN=$(grep -liE '#3|overflow' .task-plans/phase-*.md 2>/dev/null | head -1)
if [ -z "$PLAN" ]; then
  for gp in $(git log --all --format= --name-only | grep -E '^\.task-plans/phase-.*\.md$' | sort -u); do
    git show "$(git log --all --format=%H -1 -- "$gp"):$gp" > "$OUT/item-3-plan.md" 2>/dev/null && grep -qiE '#3|overflow' "$OUT/item-3-plan.md" && { PLAN="$OUT/item-3-plan.md"; break; }
  done
fi
[ -z "$PLAN" ] && PLAN=$(grep -liE '#3|overflow' .r-loop/wt/*/.task-plans/phase-*.md 2>/dev/null | head -1)
if [ -n "$PLAN" ]; then
  [ "$PLAN" = "$OUT/item-3-plan.md" ] || cp "$PLAN" "$OUT/item-3-plan.md"
  sed -n '/^## Why this approach/,/^## /p' "$PLAN" > "$OUT/why.txt"
  [ -s "$OUT/why.txt" ] && ok "item 3 plan $(basename "$PLAN") has ## Why this approach" || fail "item 3 plan has no ## Why this approach ($PLAN)"
  grep -qi 'float64' "$OUT/why.txt" && ok "Why this approach names the float64 proposal" || fail "Why this approach never names float64"
  grep -qiE 'overflow|error' "$OUT/why.txt" && ok "Why this approach chooses an overflow error" || fail "Why this approach does not choose an overflow error"
else
  fail "no .task-plans/phase-*.md covering #3 in tree, history or worktrees"
fi

PROJ=$(echo "$S" | sed 's/[^A-Za-z0-9]/-/g')
hits=$(grep -rlF "The watchdog's triage found" "$HOME/.claude/projects/$PROJ"* "$HOME/.codex/sessions" 2>/dev/null | head -3)
[ -n "$hits" ] && ok "plan prompt carried the triage block: $hits" || note "triage block not observable in agent transcripts"

git log --oneline "$base..HEAD" > "$OUT/newcommits.txt"
[ -s "$OUT/newcommits.txt" ] && ok "landing commit: $(head -1 "$OUT/newcommits.txt")" || fail "no commit after baseline"
grep -q '^- \[x\] \[#3\]' issues-relevance.md && ok "#3 ticked" || fail "#3 not ticked: $(grep '#3' issues-relevance.md)"
grep -q '^- \[x\] \[#1\]' issues-relevance.md && fail "#1 ticked" || ok "#1 not ticked"
grep -q '^- \[x\] \[#2\]' issues-relevance.md && fail "#2 ticked" || ok "#2 not ticked"
go test ./... > "$OUT/gotest.txt" 2>&1 && ok "go test ./... green" || fail "go test ./... red: $(tail -3 "$OUT/gotest.txt")"
grep -qE '^func Add\(input string\) \(int, error\)' calc.go && ok "Add keeps (int, error)" || fail "Add signature changed: $(grep '^func Add' calc.go)"
[ -z "$(git status --porcelain)" ] && ok "tree clean" || fail "tree not clean: $(git status --porcelain | tr '\n' ';')"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
