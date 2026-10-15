#!/bin/sh
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
case "$BIN" in /*) ;; *) BIN="$PWD/$BIN" ;; esac
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }

P=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
prov=$(cd "$P" && "$BIN" docs/plan/todo-tiny.md --dry-run --plain "$@" 2> /dev/null | awk '$1 == "implement" { print $2; exit }')
rm -rf "$P"
[ "$prov" = claude ] || { echo "NOT RUN: the implement step runs on ${prov:-an unknown provider}, not claude"; exit 3; }

herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"
export HERDR_SOCKET_PATH="${HERDR_SOCKET_PATH:-${XDG_CONFIG_HOME:-$HOME/.config}/herdr/herdr.sock}"

TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-1800}"
OUT="${OUT:-$(mktemp -d)}"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-bgsleep-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

mkdir -p .r-loop/prompts
{
  head -n 1 "$ROOT/internal/prompts/templates/implement.md"
  cat <<'EOF'

## Driver test: background work first

This run tests the driver. Before anything else, start `sleep 60` with the Bash tool and `run_in_background: true`, then end your turn at once: write nothing and run nothing else in this turn. When the task notification that the sleep finished arrives, do the work below and write the sentinel. For this one sleep, this overrides the rule under `## Reporting the outcome` to run commands in the foreground and never end your turn while background work you started is still running.
EOF
  tail -n +2 "$ROOT/internal/prompts/templates/implement.md"
} > .r-loop/prompts/implement.md
git add .r-loop/prompts && git commit -qm "test: steer the agent"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain "$@" > "$OUT/dry-run.txt" 2>&1
grep -q '^prompt implement: .*/\.r-loop/prompts/implement\.md$' "$OUT/dry-run.txt" || { echo "FAIL prompt override implement not loaded"; exit 1; }

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-bgsleep}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended "$@") || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
start=$(date +%s)
final=""
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  if [ $(( $(date +%s) - start )) -ge "$LIMIT" ]; then
    "$TUI" send "$H" C-c > /dev/null; sleep 1; "$TUI" send "$H" y > /dev/null
    final="aborted after ${LIMIT}s"
    sleep 30
    break
  fi
  sleep 5
done
echo "end     $final ($(( $(date +%s) - start ))s)"
"$TUI" stop "$H" > /dev/null 2>&1
trap - EXIT

RUN=$(ls -d .r-loop/runs/*/ 2>/dev/null | head -1); RUN=${RUN%/}
cp "$RUN/events.jsonl" "$OUT/" 2> /dev/null
[ -f "$RUN/hooks.jsonl" ] && cp "$RUN/hooks.jsonl" "$OUT/"
python3 "$ROOT/.claude/skills/test-app/e2e/check_background_sleep.py" "$RUN"; prc=$?

echo "sandbox $S"; echo "out     $OUT"
[ "$prc" = 3 ] && exit 3
[ "$prc" = 0 ] || fails=$((fails + 1))
echo "failures: $fails"; exit $((fails > 0))
