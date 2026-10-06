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
EXPECT_PLACED="${NARROW_EXPECT_PLACED:-tab}"
SPLITS="${NARROW_SPLITS:-3}"
OUT="${OUT:-$ROOT/.claude/skills/test-app/e2e/frames/narrow-pane-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT/screens"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
width() { herdr pane layout --pane "$1" | python3 -c 'import json,sys
r=json.load(sys.stdin)["result"]["layout"]
print(next(p["rect"]["width"] for p in r["panes"] if p["pane_id"]==sys.argv[1]))' "$1"; }

WS=$(herdr workspace create --label "narrow-test-$$" --no-focus --cwd /tmp | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["workspace"]["workspace_id"])') || { echo "FAIL workspace create"; exit 1; }
P="$WS:p1"
i=0; while [ "$i" -lt "$SPLITS" ]; do i=$((i + 1))
  P=$(herdr pane split "$P" --direction right --no-focus --cwd /tmp | python3 -c 'import json,sys; print(json.load(sys.stdin)["result"]["pane"]["pane_id"])') || { echo "FAIL split"; herdr workspace close "$WS"; exit 1; }
done
PW=$(width "$P")
echo "workspace $WS parent $P width $PW"
herdr pane layout --pane "$P" > "$OUT/layout-before.json"

S="$HOME/r-loop-test-narrow-$(openssl rand -hex 4)"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || { herdr workspace close "$WS"; exit 1; }
cd "$S" || exit 1
[ -z "${PRETRUST:-}" ] || bash "$ROOT/.claude/skills/test-app/e2e/pretrust.sh" "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-narrow1}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$P" "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; herdr workspace close "$WS"; exit 1; }
POLLER=""
trap '[ -n "$POLLER" ] && kill "$POLLER" 2>/dev/null; "$TUI" stop "$H" > /dev/null 2>&1; herdr workspace close "$WS" > /dev/null 2>&1' EXIT
echo "handle  $H"

poll() {
  local pane agent cols f h
  : > "$OUT/seen.txt"
  : > "$OUT/panes.txt"
  while :; do
    herdr pane list 2>/dev/null | python3 -c '
import json, sys, subprocess
root = sys.argv[1].rstrip("/")
alt = root[len("/private"):] if root.startswith("/private/") else root
for p in json.load(sys.stdin)["result"]["panes"]:
    cwd = p.get("cwd", "") + " " + p.get("foreground_cwd", "")
    if root in cwd or alt in cwd:
        try:
            lay = json.loads(subprocess.run(["herdr", "pane", "layout", "--pane", p["pane_id"]], capture_output=True, text=True).stdout)
        except ValueError:
            continue
        r = next(x["rect"] for x in lay["result"]["layout"]["panes"] if x["pane_id"] == p["pane_id"])
        print(p["pane_id"], p.get("agent") or "-", r["width"], r["height"], p.get("tab_id", "-"))
' "$S" > "$OUT/panes-now.txt" 2>/dev/null
    sort -u "$OUT/panes-now.txt" "$OUT/panes.txt" -o "$OUT/panes.txt"
    while read -r pane agent cols rows tab; do
      f=$(mktemp "$OUT/screens/tmp.XXXXXX")
      herdr agent read "$pane" --source visible > "$f" 2>/dev/null
      h=$(md5 -q "$f")
      if [ ! -s "$f" ] || grep -qx "$h" "$OUT/seen.txt"; then rm -f "$f"; continue; fi
      echo "$h" >> "$OUT/seen.txt"
      mv "$f" "$OUT/screens/$(date +%H%M%S)-$(echo "$pane" | tr ':' '_')-$agent-cols$cols.txt"
      case "$pane" in "$WS":*) herdr agent read "$pane" --source recent-unwrapped --lines 200 > "$OUT/screens/$(date +%H%M%S)-$(echo "$pane" | tr ':' '_')-$agent-cols$cols-history.txt" 2>/dev/null ;; esac
    done < "$OUT/panes-now.txt"
    sleep 0.5
  done
}
poll &
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

EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
RD=$(dirname "${EV:-.}")
[ -n "$EV" ] && cp "$EV" "$OUT/events.jsonl"
cp "$RD/report.md" "$OUT/report.md" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

bad=$(grep -rhoE 'agent_not_ready|agent_not_running|never showed[^"]*|did not start[^"]*' "$RD" "$OUT/frame-end.txt" 2>/dev/null | sort -u)
[ -z "$bad" ] && ok "no agent_not_ready / agent_not_running / never showed / did not start" || fail "start errors: $bad"

echo "panes r-loop opened (pane agent cols):"; sed 's/^/        /' "$OUT/panes.txt"
placed=$(grep '"Kind":"watchdog-placed"' "$EV" 2>/dev/null | head -1)
echo "        $placed"
pl=$(echo "$placed" | python3 -c 'import json,sys; r=json.loads(sys.stdin.read() or "{}"); f=(r.get("Event") or r).get("Fields") or {}; print(f.get("placed",""), f.get("pane",""))' 2>/dev/null)
wpl=${pl%% *}; wpane=${pl#* }
[ "$wpl" = "$EXPECT_PLACED" ] && ok "watchdog-placed placed=$wpl pane=$wpane" || fail "watchdog-placed: want placed=$EXPECT_PLACED, got '${placed:-no event}'"
case "$wpane" in "$WS":*) ok "watchdog pane $wpane is in the driver pane's workspace $WS" ;; *) fail "watchdog pane '$wpane' not in $WS" ;; esac
wd=$(awk -v p="$wpane" '$1 == p {print $3" "$4" "$5}' "$OUT/panes.txt" | sort -n | head -1)
if [ -z "$wd" ]; then
  fail "watchdog pane $wpane never seen in herdr pane list"
else
  set -- $wd
  echo "        watchdog pane $wpane ${1}x${2} tab $3 (driver parent $P tab $(herdr pane list | python3 -c 'import json,sys; print(next(p["tab_id"] for p in json.load(sys.stdin)["result"]["panes"] if p["pane_id"]==sys.argv[1]))' "$P" 2>/dev/null))"
  [ "$1" -ge 60 ] && [ "$2" -ge 15 ] && ok "watchdog pane is ${1}x${2} (>= 60x15)" || fail "watchdog pane is ${1}x${2}, below 60x15"
  wp=$(echo "$wpane" | tr ':' '_')
  tr_screen=$(for f in "$OUT"/screens/*-"$wp"-*; do tr -d ' \n' < "$f" | grep -q 'Yes,Itrustthisfolder\|trustthisfolder' && echo "$f"; done | head -1)
  [ -n "$tr_screen" ] && ok "watchdog showed its trust dialog: $tr_screen" || echo "NOTE no trust dialog captured in the watchdog pane"
fi

landed=$(grep '"Kind":"landed"' "$EV" 2>/dev/null | head -1)
[ -n "$landed" ] && ok "phase landed: ${landed:0:200}" || fail "no landed event"
grep -q '^- \[x\]' docs/plan/todo-tiny.md && ok "todo box ticked" || fail "todo box not ticked"

case "$final" in
  *aborted*) "$TUI" stop "$H" --expect-exited --status 1; rc=$? ;;
  *) "$TUI" send "$H" q > /dev/null; sleep 2; "$TUI" stop "$H" --expect-exited; rc=$? ;;
esac
[ "$rc" = 0 ] && ok "app exited, terminal restored" || fail "stop --expect-exited rc=$rc"
trap - EXIT
herdr workspace close "$WS" > /dev/null 2>&1

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
