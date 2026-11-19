#!/usr/bin/env bash
# usage: live_concurrent_runs.sh
#   Two fresh, untrusted sandboxes A and B, each in a workspace of its own. Run A starts docs/plan/todo-tiny.md
#   --unattended --plain; once A's events show run-list (its watchdog is up), run B starts the same way while A is live.
#   Regression for bcae813: B's watchdog starts (provider-versions, triage/run-list, no exit 4, no "watchdog did not
#   start"). Both runs then land their phase (landed event, phase commit, ticked box). No Claude terminal of either run
#   ends on "Claude Code didn't start because this folder isn't trusted". Every finding event's blocking matches the
#   rule: real and (P1, or P2 with likelihood likely|possible).
set -u
. "$(dirname "$0")/host.sh"
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
host_up || { echo "NOT RUN: no session host (herdr or herdr-app)"; exit 3; }

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
(cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { if [ "$1" = 0 ]; then ok "$2"; else fail "$2"; fi; }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

A=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
B=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
WSA= WSB= PA= PB=
cleanup() {
  for p in $PA $PB; do pkill -INT -P "$p" 2>/dev/null; done
  sleep 3
  for p in $PA $PB; do pkill -KILL -P "$p" 2>/dev/null; done
  [ -n "$WSA" ] && ws_close "$WSA" > /dev/null 2>&1
  [ -n "$WSB" ] && ws_close "$WSB" > /dev/null 2>&1
}
trap cleanup EXIT INT TERM
set -- $(ws_open "r-loop test concurrent A" "$A"); WSA=${1:-} TA=${2:-}
set -- $(ws_open "r-loop test concurrent B" "$B"); WSB=${1:-} TB=${2:-}
[ -n "$TA" ] && [ -n "$TB" ] || { echo "FAIL workspace open"; exit 1; }
echo "A $A ws=$WSA terminal=$TA"
echo "B $B ws=$WSB terminal=$TB"
echo "out $OUT"

start_run() {
  local dir=$1 id=$2 name=$3
  (cd "$dir" && env $(host_env "$id") "$BIN" docs/plan/todo-tiny.md --unattended --plain > "$OUT/$name.out" 2> "$OUT/$name.err"
   echo $? > "$OUT/$name.rc") &
}
EV() { ls "$1"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
has() { local f; f=$(EV "$1"); [ -n "$f" ] && [ -n "$(jq -c "select($2)" "$f" 2>/dev/null | head -1)" ]; }
kind() { printf '.Event.Kind=="%s"' "$1"; }
wait_ev() {
  local dir=$1 filter=$2 limit=$3 rc=$4 t=0
  while [ "$t" -lt "$limit" ]; do
    has "$dir" "$filter" && return 0
    [ -s "$OUT/$rc.rc" ] && { has "$dir" "$filter"; return; }
    scan_terms; sleep 3; t=$((t + 3))
  done
  return 1
}

scan_terms() {
  local term g agent cwd st
  for term in $(hc_ ls --json --all 2>/dev/null | jq -r '.terminals[].address'); do
    g=$(hc_ get --json "$term" 2>/dev/null) || continue
    agent=$(printf '%s' "$g" | jq -r '.labels["rloop.agent"] // empty')
    [ -n "$agent" ] || continue
    cwd=$(printf '%s' "$g" | jq -r '.foreground.cwd // .cwd // ""')
    case "$cwd" in *"$(basename "$A")"*|*"$(basename "$B")"*) ;; *) continue ;; esac
    st=$(printf '%s' "$g" | jq -r '.state')
    echo "terminal $term agent=$agent state=$st cwd=$cwd" > "$OUT/agent-$term"
    [ "$st" = finished ] && [ ! -s "$OUT/term-$term.txt" ] && hc_ read --json --lines 60 "$term" 2>/dev/null | jq -r .text > "$OUT/term-$term.txt"
  done
}

log "starting run A"
start_run "$A" "$TA" a; PA=$!
wait_ev "$A" "$(kind run-list)" 600 a; check $? "A: watchdog up (run-list)"

log "starting run B while A is live"
start_run "$B" "$TB" b; PB=$!
wait_ev "$B" "$(kind provider-versions)" 300 b; check $? "B: provider-versions"
wait_ev "$B" "$(kind triage) or $(kind run-list)" 600 b; check $? "B: triage/run-list (watchdog started)"
[ "$(cat "$OUT/b.rc" 2>/dev/null)" != 4 ]; check $? "B: not exited 4"
! grep -q 'watchdog did not start' "$OUT/b.err"; check $? "B: stderr has no 'watchdog did not start'"

log "waiting for both runs to end (up to 45 min)"
t=0
while [ "$t" -lt 2700 ] && { [ ! -s "$OUT/a.rc" ] || [ ! -s "$OUT/b.rc" ]; }; do scan_terms; sleep 15; t=$((t + 15)); done
scan_terms

grep -qE "folder isn.t trusted" "$OUT"/term-*.txt 2>/dev/null && grep -lE "folder isn.t trusted" "$OUT"/term-*.txt | sed 's/^/     UNTRUSTED EXIT: /'
untrusted=$(grep -lE "folder isn.t trusted" "$OUT"/term-*.txt 2>/dev/null | wc -l | tr -d ' ')
seen=$(ls "$OUT"/agent-* 2>/dev/null | wc -l | tr -d ' ')
cat "$OUT"/agent-* 2>/dev/null | sed 's/^/     /'
echo "     run terminals seen: $seen"
[ "$seen" -gt 0 ]; check $? "found the runs' Claude terminals"
[ "$untrusted" = 0 ]; check $? "no Claude terminal ended on the untrusted-folder exit"

for r in A B; do
  dir=$A; name=a; [ "$r" = B ] && { dir=$B; name=b; }
  f=$(EV "$dir"); [ -n "$f" ] && cp "$f" "$OUT/$name-events.jsonl"
  echo "     $r exit $(cat "$OUT/$name.rc" 2>/dev/null)"
  tail -5 "$OUT/$name.err" 2>/dev/null | sed "s/^/     $r err| /"
  [ "$(cat "$OUT/$name.rc" 2>/dev/null)" = 0 ]; check $? "$r: exit 0"
  has "$dir" "$(kind landed)"; check $? "$r: landed event"
  git -C "$dir" log --oneline | sed "s/^/     $r git| /"
  [ "$(git -C "$dir" log --oneline | wc -l)" -ge 2 ]; check $? "$r: phase commit in git log"
  grep -q '^- \[x\]' "$dir/docs/plan/todo-tiny.md"; check $? "$r: todo-tiny box ticked"
  [ -n "$f" ] || continue
  jq -c 'select(.Event.Kind=="finding") | .Event.Fields | {verdict,severity,likelihood,blocking,fixed,title}' "$f" \
    | tee "$OUT/$name-findings.jsonl" | cut -c1-220 | sed "s/^/     $r finding| /"
  bad=$(jq -c 'select(.Event.Kind=="finding") | .Event.Fields
    | select((.blocking=="true") != (.verdict=="real" and (.severity=="P1" or (.severity=="P2" and (.likelihood=="likely" or .likelihood=="possible")))))' "$f") || bad="jq failed"
  [ -z "$bad" ]; check $? "$r: every finding's blocking matches the rule"
  [ -n "$bad" ] && echo "$bad" | sed "s/^/     $r bad| /"
done
n=$(cat "$OUT"/a-findings.jsonl "$OUT"/b-findings.jsonl 2>/dev/null | jq -c 'select(.verdict=="real" and .severity=="P1")' | wc -l | tr -d ' ')
echo "     real P1 findings: $n$([ "$n" = 0 ] && echo ' (P1-at-any-likelihood rule NOT RUN live)')"
echo "sandboxes: $A $B"
echo "failures: $fails"
exit "$fails"
