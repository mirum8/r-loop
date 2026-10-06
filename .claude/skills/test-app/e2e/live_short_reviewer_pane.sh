#!/bin/bash
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2400}"
MAX_ROWS="${SHORT_MAX_ROWS:-14}"
OUT="${OUT:-$ROOT/.claude/skills/test-app/e2e/frames/short-reviewer-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT/screens"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

python3 - .r-loop/config.yaml <<'PY'
import re, sys
p = sys.argv[1]
s = open(p).read()
old = """  implement:
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
    fallback:
      provider: codex
      model: gpt-5.6-sol
      effort: medium
    reviewers:
      - provider: codex
        model: gpt-5.6-sol
        effort: medium
"""
new = """  implement:
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
    fallback:
      provider: codex
      model: gpt-5.6-sol
      effort: medium
    reviewers:
      - provider: claude
        name: claude-a
        model: sonnet
        effort: low
      - provider: claude
        name: claude-b
        model: sonnet
        effort: low
      - provider: codex
        model: gpt-5.6-sol
        effort: medium
"""
assert old in s, "implement block not found"
open(p, "w").write(s.replace(old, new))
PY
[ $? = 0 ] || { echo "FAIL config edit"; exit 1; }
git add .r-loop/config.yaml && git -c user.name=sandbox -c user.email=sandbox@localhost commit -q -m "three implement reviewers, codex last" || { echo "FAIL commit"; exit 1; }

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-shortpane1}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$HERDR_PANE_ID" "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
POLLER=""
trap '[ -n "$POLLER" ] && kill "$POLLER" 2>/dev/null; "$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

poll_codex() {
  local rows pane src f h
  : > "$OUT/seen.txt"
  : > "$OUT/codex-panes.txt"
  while :; do
    herdr pane list 2>/dev/null | python3 -c '
import json, sys
root = sys.argv[1].rstrip("/")
alt = root[len("/private"):] if root.startswith("/private/") else root
for p in json.load(sys.stdin)["result"]["panes"]:
    cwd = p.get("cwd", "") + " " + p.get("foreground_cwd", "")
    if (root in cwd or alt in cwd) and "/.r-loop/wt/" in cwd:
        print(p["pane_id"], p.get("agent") or "-", p["scroll"]["viewport_rows"])
' "$S" > "$OUT/panes-now.txt"
    sort -u "$OUT/panes-now.txt" "$OUT/codex-panes.txt" -o "$OUT/codex-panes.txt"
    while read -r pane agent rows; do
      [ "$agent" = codex ] || continue
      for src in visible recent-unwrapped; do
        f=$(mktemp "$OUT/screens/tmp.XXXXXX")
        if [ "$src" = visible ]; then herdr agent read "$pane" --source visible > "$f" 2>/dev/null
        else herdr agent read "$pane" --source recent-unwrapped --lines 200 > "$f" 2>/dev/null; fi
        h=$(md5 -q "$f")
        if [ ! -s "$f" ] || grep -qx "$h" "$OUT/seen.txt"; then rm -f "$f"; continue; fi
        echo "$h" >> "$OUT/seen.txt"
        mv "$f" "$OUT/screens/$(date +%H%M%S)-$(echo "$pane" | tr ':' '_')-rows$rows-$src.txt"
      done
    done < "$OUT/panes-now.txt"
    sleep 0.5
  done
}
poll_codex &
POLLER=$!

start=$(date +%s)
final=""
while :; do
  sleep 5
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
kill "$POLLER" 2>/dev/null; POLLER=""
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RD=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RD/report.md" "$OUT/report.md" 2>/dev/null
cp "$RD"/blockers* "$OUT/" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

if grep -rqs "never showed codex's prompt" "$RD" "$OUT/frame-end.txt"; then
  fail "\"never showed codex's prompt\" found: $(grep -rh "never showed codex's prompt" "$RD" | head -2)"
else
  ok "no \"never showed codex's prompt\" in the run dir or the final frame"
fi

for id in claude-a claude-b codex; do
  ev=$(grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep "\"reviewer\":\"$id\"" | head -1)
  [ -n "$ev" ] && echo "$ev" | grep -q '"state":"ok"' && ok "implement reviewer $id ran round 1, state ok" || fail "implement reviewer $id: ${ev:-no review-find event}"
done

echo "codex panes seen (pane agent rows):"; grep ' codex ' "$OUT/codex-panes.txt" | sed 's/^/        /'
short=$(awk -v m="$MAX_ROWS" '$2 == "codex" && $3 <= m {print $1" rows="$3}' "$OUT/codex-panes.txt" | head -1)
if [ -z "$short" ]; then
  echo "NOT EXERCISED: no codex reviewer pane had <= $MAX_ROWS rows"
  fails=$((fails + 1))
else
  ok "codex reviewer ran in a short pane: $short"
  p=$(echo "$short" | cut -d' ' -f1 | tr ':' '_')
  vis=$(ls "$OUT"/screens/*-"$p"-*-visible.txt 2>/dev/null)
  rec=$(ls "$OUT"/screens/*-"$p"-*-recent-unwrapped.txt 2>/dev/null)
  hid=$(for f in $vis; do grep -q '›' "$f" && ! grep -q '>_ OpenAI Codex' "$f" && echo "$f"; done | head -1)
  [ -n "$hid" ] && ok "visible screen shows › without the banner: $hid" || echo "NOTE no visible capture of $p had › without the banner"
  ban=$(grep -l '>_ OpenAI Codex' $rec 2>/dev/null | head -1)
  [ -n "$ban" ] && ok "recent-unwrapped holds the banner: $ban" || echo "NOTE no recent-unwrapped capture of $p held the banner"
fi

landed=$(grep '"Kind":"landed"' "$EV" | head -1)
[ -n "$landed" ] && ok "phase landed: ${landed:0:200}" || fail "no landed event"

case "$final" in
  *aborted*) "$TUI" stop "$H" --expect-exited --status 1; rc=$? ;;
  *) "$TUI" send "$H" q > /dev/null; sleep 2; "$TUI" stop "$H" --expect-exited; rc=$? ;;
esac
[ "$rc" = 0 ] && ok "app exited, terminal restored" || fail "stop --expect-exited rc=$rc"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
