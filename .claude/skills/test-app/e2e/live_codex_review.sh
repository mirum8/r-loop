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
OUT="${OUT:-$ROOT/.claude/skills/test-app/e2e/frames/codex-review-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT/screens"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
[ -z "${PRETRUST:-}" ] || bash "$ROOT/.claude/skills/test-app/e2e/pretrust.sh" "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-codexrev1}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$HERDR_PANE_ID" "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
POLLER=""
trap '[ -n "$POLLER" ] && kill "$POLLER" 2>/dev/null; "$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

poll_codex() {
  local panes="" last_list=0 now pane src f h
  : > "$OUT/seen.txt"
  while :; do
    now=$(date +%s)
    if [ $((now - last_list)) -ge 2 ]; then
      last_list=$now
      panes=$(herdr agent list 2>/dev/null | python3 -c '
import json, sys
root = sys.argv[1].rstrip("/")
alt = root[len("/private"):] if root.startswith("/private/") else root
for a in json.load(sys.stdin)["result"]["agents"]:
    cwd = a.get("cwd", "") + " " + a.get("foreground_cwd", "")
    if a.get("agent") == "codex" and (root in cwd or alt in cwd):
        print(a["pane_id"])
' "$S")
    fi
    for pane in $panes; do
      for src in visible recent-unwrapped; do
        f=$(mktemp "$OUT/screens/tmp.XXXXXX")
        if [ "$src" = visible ]; then herdr agent read "$pane" --source visible > "$f" 2>/dev/null
        else herdr agent read "$pane" --source recent-unwrapped --lines 200 > "$f" 2>/dev/null; fi
        h=$(md5 -q "$f")
        if [ ! -s "$f" ] || grep -qx "$h" "$OUT/seen.txt"; then rm -f "$f"; continue; fi
        echo "$h" >> "$OUT/seen.txt"
        mv "$f" "$OUT/screens/$(date +%H%M%S)-$(python3 -c 'import time;print(int(time.time()*1000)%1000)')-$(echo "$pane" | tr ':' '_')-$src.txt"
      done
    done
    sleep 0.3
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
cp "$EV" "$OUT/events.jsonl"
cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null
find "$(dirname "$EV")" -name '*-findings-codex-*' > "$OUT/findings.txt"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

SC="$OUT/screens"
composer=$(grep -l '^ *› */review Review the current code changes' "$SC"/*visible.txt 2>/dev/null | head -1)
presses=$(grep '"Kind":"review-ran".*"reviewer":"codex"' "$EV" | grep -o '"presses":"[0-9]*"' | head -1 | grep -o '[0-9][0-9]*')
if [ -n "$composer" ]; then
  ok "composer showed /review before submit: $composer"
elif [ "${presses:-0}" -ge 1 ]; then
  echo "NOTE composer not sampled (the driver presses enter within 250ms of seeing it); review-ran presses=$presses proves the driver saw /review typed"
else
  fail "/review text never seen in a composer and review-ran has no presses"
fi
banner=$(grep -h '>> Code review started' "$SC"/*.txt 2>/dev/null | sort -u | head -2)
[ -n "$banner" ] && ok "start banner: $banner" || fail "no '>> Code review started' banner"
done_=$(grep -h '<< Code review finished' "$SC"/*.txt 2>/dev/null | sort -u | head -2)
[ -n "$done_" ] && ok "finished marker: $done_" || fail "no '<< Code review finished' marker"
echoed=$(grep -l '>> Code review started' "$SC"/*recent-unwrapped.txt 2>/dev/null | xargs grep -h '^ *› */review' 2>/dev/null | head -1)
[ -z "$echoed" ] && ok "no '› /review' line in the feed after submit (expected)" || echo "NOTE '› /review' seen next to the banner: $echoed"
grep -q "$S/.r-loop/wt/" <(grep -h -e 'Code review started' -e 'Ran ' -e '/.r-loop/wt/' "$SC"/*.txt) && ok "review ran inside a .r-loop/wt worktree" || fail "no worktree path in codex screens"

pf=$(grep '"Kind":"review-find"' "$EV" | grep '"Step":"plan"' | grep codex | head -1)
echo "$pf" | grep -q '"command":"prompt review-plan"' && ok "plan reviewer used the review-plan prompt (no native /review on plan, by design)" || fail "plan review-find: $pf"
imf=$(grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep codex | head -1)
echo "$imf" | grep -q '"command":"/review Review the current code changes' && echo "$imf" | grep -q '"state":"ok"' && ok "implement review-find ran /review, state ok" || fail "implement review-find: $imf"
grep -q '"Kind":"review-ran".*"reviewer":"codex"' "$EV" && ok "review-ran recorded: $(grep -o '"presses":"[0-9]*"' "$EV" | head -1)" || fail "no review-ran event"
for k in plan implement; do
  grep -q "/$k-findings-codex-" "$OUT/findings.txt" && ok "$k findings file" || fail "no $k findings file"
done
ls "$(dirname "$EV")"/phase-1/implement-rv-codex-*/native-review.txt > /dev/null 2>&1 && ok "native-review.txt saved" || fail "no native-review.txt"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
