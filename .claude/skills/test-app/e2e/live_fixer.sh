#!/bin/sh
# usage: live_fixer.sh
#   ADR-91 fixer end to end, attended: implement's provider claudex passes a flag claude rejects; the
#   step blocks, this script (as the maintainer) hands the blocker to the fixer, applies its config
#   proposal through the watchdog, and the step reruns with the fixed args in the same run.
#   The run gets a herdr workspace of its own: HERDR_PANE_ID points at that workspace's root pane, so
#   the test watchdog splits beside it and the fixer opens a tab there, never in the maintainer's
#   workspace. This script answers every question the test watchdog asks (Enter on its recommended
#   option). Nothing here ever waits on a person.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
E2E="$ROOT/.claude/skills/test-app/e2e"
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-4800}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
BAD=--no-such-flag-r-loop

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-fixer-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

python3 - "$ROOT/internal/providers/shipped/claude.yaml" "$BAD" <<'EOF'
import sys
shipped, bad = sys.argv[1:]
p = ".r-loop/config.yaml"
s = open(p).read()
old = "  implement:\n    provider: claude\n"
assert old in s
s = s.replace(old, "  implement:\n    provider: claudex\n")
s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n  blockerTimeout: 20m\n  fixer:\n    provider: claude\n    model: sonnet\n    effort: medium\n    timeout: 15m\n")
block = "providers:\n  claudex:\n"
for line in open(shipped):
    block += "    " + line
block += '    flags: "%s"\n' % bad
s += block
open(p, "w").write(s)
EOF
cp .r-loop/config.yaml "$OUT/config.orig.yaml"
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_fixer setup"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1; rc=$?
[ "$rc" = 0 ] && ok "dry-run exit 0" || { fail "dry-run exit $rc"; cat "$OUT/dryrun.out"; exit 1; }
grep -q 'fixer: claude sonnet medium' "$OUT/dryrun.out" && ok "dry-run banner: fixer: claude sonnet medium" || fail "dry-run banner has no fixer line"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-fixer-live}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-5400}"
RUNWS_JSON=$(herdr workspace create --cwd "$S" --label "r-loop test fixer" --no-focus)
RUNWS=$(printf '%s' "$RUNWS_JSON" | jq -r '.result.workspace.workspace_id')
RUNPANE=$(printf '%s' "$RUNWS_JSON" | jq -r '.result.root_pane.pane_id')
[ -n "$RUNPANE" ] && [ "$RUNPANE" != null ] || { echo "FAIL could not create the test workspace"; exit 1; }
echo "workspace $RUNWS ($RUNPANE)"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$RUNPANE" "$BIN" docs/plan/todo-tiny.md) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; herdr workspace close "$RUNWS" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frames/start-120x40.txt"

has() { [ -n "$EV" ] && grep -q "\"Kind\":\"$1\"" "$EV"; }
wd() { "$E2E/wd_call.sh" "$WDURL" "$@"; }

(
  stage=0
  answered=0
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    RUN=$(dirname "${EV:-x}")
    asked=0
    [ -n "$EV" ] && asked=$(grep -c '"Kind":"watchdog-waiting"' "$EV")
    if [ "$asked" -gt "$answered" ]; then
      answered=$asked
      WDPANE=$(grep '"Kind":"watchdog-placed"' "$EV" | head -1 | sed 's/.*"pane":"\([^"]*\)".*/\1/')
      sleep 3
      herdr pane read "$WDPANE" 2>/dev/null | tail -20 > "$OUT/question-$asked.txt"
      herdr pane send-keys "$WDPANE" enter >> "$OUT/answers.txt" 2>&1
      echo "question $asked answered with Enter in $WDPANE at $(date +%T)" >> "$OUT/answers.txt"
    fi
    if [ $stage = 0 ] && has blocked-on; then
      stage=1
      WDURL=$("$E2E/wd_url.sh" "$S" 2> "$OUT/wdurl.err")
      echo "$WDURL" > "$OUT/wdurl.txt"
      BID=$(grep '"Kind":"blocked-on"' "$EV" | head -1 | sed 's/.*"id":"\(b[0-9]*\)".*/\1/')
      wd resolve_blocker '{"id":"'"$BID"'","action":"fix","addendum":"implement'"'"'s claudex provider passes a flag claude does not accept; check its flags"}' > "$OUT/call-fix.json" 2>&1
      date +%T >> "$OUT/call-fix.json"
      sleep 1
      cp "$("$TUI" capture "$H")" "$OUT/frames/blocker-120x40.txt"
      "$BIN" status --plain > "$OUT/status-blocker.txt" 2>&1
    fi
    if [ $stage = 1 ] && has fix-started; then
      stage=2
      sleep 2
      cp "$("$TUI" capture "$H")" "$OUT/frames/fixer-120x40.txt"
      cp "$("$TUI" capture "$H" --ansi)" "$OUT/frames/fixer-120x40.ansi"
      "$TUI" resize "$H" 80x24 > /dev/null; sleep 1
      cp "$("$TUI" capture "$H")" "$OUT/frames/fixer-80x24.txt"
      "$TUI" resize "$H" 120x40 > /dev/null; sleep 1
      cp "$(ls -t "$RUN"/fix-b*/incident.md | head -1)" "$OUT/incident.md" 2>/dev/null
      herdr pane list > "$OUT/panes-fixing.json" 2>/dev/null
      "$BIN" status --plain > "$OUT/status-fixing.txt" 2>&1
    fi
    if [ $stage -ge 1 ] && [ $stage -lt 3 ] && has fix-failed; then
      stage=9
      cp "$(ls -td "$RUN"/fix-b* | head -1)"/fixer.sentinel "$OUT/" 2>/dev/null
    fi
    if [ $stage = 2 ] && has fix-proposed; then
      stage=3
      FD=$(grep '"Kind":"fix-proposed"' "$EV" | tail -1 | sed 's/.*"id":"\(b[0-9]*\)".*/fix-\1/')
      cp "$RUN/$FD/proposal.json" "$RUN/$FD/fixer.sentinel" "$OUT/" 2>/dev/null
      git status --porcelain > "$OUT/porcelain-proposed.txt"
      cp .r-loop/config.yaml "$OUT/config.at-proposed.yaml"
      cp "$("$TUI" capture "$H")" "$OUT/frames/proposed-120x40.txt"
      WDURL=$(cat "$OUT/wdurl.txt")
      wd apply_fix '{"id":"'"${FD#fix-}"'","decision":"apply"}' > "$OUT/call-apply-nosaid.json" 2>&1
      wd apply_fix '{"id":"b99","decision":"apply","maintainer_said":"yes"}' > "$OUT/call-apply-b99.json" 2>&1
      proposed_at=$(date +%s)
    fi
    if [ $stage = 3 ] && ! has fix-applied && [ $(( $(date +%s) - proposed_at )) -gt 180 ]; then
      wd apply_fix '{"id":"'"${FD#fix-}"'","decision":"apply","maintainer_said":"yes, apply it"}' > "$OUT/call-apply.json" 2>&1
      echo "fallback: the watchdog did not apply within 180s" >> "$OUT/call-apply.json"
    fi
    if [ $stage = 3 ] && has fix-applied; then
      stage=4
      sleep 3
      cp "$("$TUI" capture "$H")" "$OUT/frames/applied-120x40.txt"
      git status --porcelain > "$OUT/porcelain-applied.txt"
    fi
    sleep 0.3
  done
) &
watcher=$!

start=$(date +%s)
n=0
final=""
while :; do
  n=$((n + 1))
  ps -axww -o pid=,command= | grep -F "$S" | grep -v grep | grep -E 'claude|codex' >> "$OUT/ps.txt"
  if [ $((n % 3)) = 1 ]; then
    "$BIN" status --plain > "$OUT/status.txt" 2>&1
    if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
      final=$(head -1 "$OUT/status.txt"); break
    fi
    st=$("$TUI" status "$H")
    case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
    [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  fi
  sleep 2
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 5
kill "$watcher" 2>/dev/null
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1
sort -u "$OUT/ps.txt" > "$OUT/ps-uniq.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
ls -R "$RUN" > "$OUT/rundir.txt"
git log --format='%h %p | %s' > "$OUT/gitlog.txt"
git status --porcelain > "$OUT/porcelain-end.txt"
python3 - "$EV" > "$OUT/key-events.txt" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    if r.get("Kind") == "step":
        st = r.get("Step") or {}
        print("state", st.get("Phase"), st.get("Kind"), st.get("Attempt"), r.get("State"), (r.get("Reason") or "")[:300])
    e = r.get("Event") or {}
    k = e.get("Kind") or ""
    if k.startswith("fix") or k in {"blocked-on", "blocker-resolved", "watchdog-call", "restart", "restart-refused", "landed", "halt", "blocked", "warning", "run", "provider-versions", "gate"}:
        print(k, e.get("Phase"), e.get("Step"), json.dumps(e.get("Fields") or {}, ensure_ascii=False)[:600])
EOF

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

ev() { grep "\"Kind\":\"$1\"" "$EV"; }
FID=$(ev fix-applied | tail -1 | sed 's/.*"id":"\(b[0-9]*\)".*/\1/')
[ -n "$FID" ] || FID=$(ev fix-proposed | tail -1 | sed 's/.*"id":"\(b[0-9]*\)".*/\1/')
echo "info: the applied fix is fix-$FID; fixes: $(ev fix-started | grep -o '"id":"b[0-9]*"' | tr '\n' ' ')"
ev fix-failed | grep -q '"reason":"withdrawn"' && fail "a fix was withdrawn mid-investigation: $(ev fix-failed | grep -o '"Fields":{[^}]*}')" || ok "no fix withdrawn mid-investigation"
case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
ev provider-versions > /dev/null && ok "provider-versions: $(ev provider-versions | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no provider-versions event"
B=$(ev blocked-on | head -1)
echo "$B" | grep -q '"source":"step"' && ok "blocked-on source step: $(echo "$B" | grep -o '"Fields":{[^}]*}' | cut -c1-300)" || fail "first blocked-on is not source step: $B"
grep -q 'versions:' "$EV" "$RUN"/*.jsonl "$OUT/status-blocker.txt" 2>/dev/null && ok "blocker text carries a versions: line: $(grep -ho 'versions: [^"\\]*' "$RUN"/*.jsonl "$OUT/status-blocker.txt" 2>/dev/null | head -1)" || fail "no 'versions:' line in the blocker text"
fixcall=$(cat "$OUT/call-fix.json" 2>/dev/null | head -1)
case "$fixcall" in
  *authorised*) ok "resolve_blocker fix by the maintainer script: $fixcall" ;;
  *"already fixed"*) ok "watchdog took fix itself first; script call refused: $fixcall" ;;
  *) fail "resolve_blocker fix: $fixcall" ;;
esac
WDP=$(ev watchdog-placed | head -1 | sed 's/.*"pane":"\([^"]*\)".*/\1/')
python3 - "$OUT/panes-fixing.json" "$WDP" "$RUNWS" <<'PY' && ok "the fixer opened in a new tab of the run's workspace; the watchdog's tab kept its two panes" || fail "fixer placement wrong (see panes-fixing.json)"
import json, sys
panes = json.load(open(sys.argv[1]))["result"]["panes"]
wd, ws = sys.argv[2], sys.argv[3]
tab = next(p["tab_id"] for p in panes if p["pane_id"] == wd)
assert tab.startswith(ws + ":"), ("watchdog not in the run's workspace", tab)
assert sum(p["tab_id"] == tab for p in panes) == 2, [p["pane_id"] for p in panes if p["tab_id"] == tab]
tabs = {p["tab_id"] for p in panes if p["workspace_id"] == ws}
assert len(tabs) >= 2, tabs
PY
ev fix-started > /dev/null && ok "fix-started: $(ev fix-started | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no fix-started"
grep -q 'fixer · b' "$OUT/frames/fixer-120x40.txt" 2>/dev/null && ok "TUI 120x40 row shows 'fixer · b': $(grep 'fixer · b' "$OUT/frames/fixer-120x40.txt" | head -1)" || fail "no 'fixer · b' in fixer-120x40"
grep -q 'fixer · b' "$OUT/frames/fixer-80x24.txt" 2>/dev/null && ok "TUI 80x24 row shows 'fixer · b'" || fail "no 'fixer · b' in fixer-80x24"
for sec in Blocker Diagnosis Excerpt Versions 'Recent records' Config Paths; do
  grep -q "^## $sec" "$OUT/incident.md" 2>/dev/null || { fail "incident.md has no '## $sec'"; continue; }
done
grep -c '^## ' "$OUT/incident.md" 2>/dev/null | grep -q 7 && ok "incident.md has the 7 sections"
if ev fix-proposed > /dev/null; then
  ok "fix-proposed: $(ev fix-proposed | grep -o '"Fields":{[^}]*}' | head -1)"
else
  fail "no fix-proposed; fix-failed: $(ev fix-failed | grep -o '"Fields":{[^}]*}' | head -1)"
fi
python3 - "$OUT/proposal.json" "$BAD" <<'EOF' && ok "proposal kind config, file .r-loop/config.yaml, bad flag gone" || fail "proposal not a config fix removing the flag: $(cat "$OUT/proposal.json" 2>/dev/null | head -c 600)"
import json, sys
p = json.load(open(sys.argv[1]))
assert p["kind"] == "config", p["kind"]
assert p["config"]["file"] == ".r-loop/config.yaml", p["config"]["file"]
assert sys.argv[2] not in p["config"]["content"]
EOF
[ -f "$OUT/porcelain-proposed.txt" ] && [ ! -s "$OUT/porcelain-proposed.txt" ] && ok "tree clean when proposed" || fail "tree not clean when proposed: $(cat "$OUT/porcelain-proposed.txt" 2>/dev/null)"
cmp -s "$OUT/config.at-proposed.yaml" "$OUT/config.orig.yaml" && ok "config untouched by the fixer" || fail "config changed before apply_fix"
grep -q refused "$OUT/call-apply-nosaid.json" 2>/dev/null && ok "apply without maintainer_said refused: $(cat "$OUT/call-apply-nosaid.json")" || fail "apply without maintainer_said: $(cat "$OUT/call-apply-nosaid.json" 2>/dev/null)"
grep -q refused "$OUT/call-apply-b99.json" 2>/dev/null && ok "apply b99 refused: $(cat "$OUT/call-apply-b99.json")" || fail "apply b99: $(cat "$OUT/call-apply-b99.json" 2>/dev/null)"
A=$(ev watchdog-call | grep '"tool":"apply_fix"' | grep '"id":"'"$FID"'"' | grep '"decision":"apply"' | grep -v '"maintainer_said":""' | head -1)
[ -n "$A" ] && ok "apply_fix with the maintainer's reply: $(echo "$A" | grep -o '"Fields":{[^}]*}')" || fail "no apply_fix with maintainer_said for $FID"
[ -f "$OUT/call-apply.json" ] && echo "info: $(tail -1 "$OUT/call-apply.json")"
[ -f "$OUT/porcelain-applied.txt" ] && [ ! -s "$OUT/porcelain-applied.txt" ] && ok "tree clean after apply" || fail "tree dirty after apply: $(cat "$OUT/porcelain-applied.txt" 2>/dev/null)"
grep -q "chore(r-loop): apply fix-$FID to .r-loop/config.yaml" "$OUT/gitlog.txt" && ok "config fix committed by the driver" || fail "no 'chore(r-loop): apply fix-$FID' commit"
ev fix-applied > /dev/null && ok "fix-applied: $(ev fix-applied | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no fix-applied"
cmp -s "$RUN/fix-$FID/config.bak" "$OUT/config.orig.yaml" && ok "config.bak equals the original config" || fail "config.bak differs from the original or is missing"
python3 - "$OUT/proposal.json" .r-loop/config.yaml <<'EOF' && ok "sandbox config now has the proposal's content" || fail "sandbox config differs from the proposal"
import json, sys
assert json.load(open(sys.argv[1]))["config"]["content"] == open(sys.argv[2]).read()
EOF
ev blocker-resolved | grep '"id":"'"$FID"'"' | grep '"action":"retry"' | grep -q "fix-$FID" && ok "$FID resolved retry citing fix-$FID: $(ev blocker-resolved | grep '"id":"'"$FID"'"' | grep -o '"Fields":{[^}]*}')" || fail "$FID not resolved retry/fix-$FID: $(ev blocker-resolved | grep -o '"Fields":{[^}]*}')"
ev restart | grep -q "fix-$FID" && ok "restart cites fix-$FID: $(ev restart | grep fix-$FID | grep -o '"Fields":{[^}]*}')" || fail "no restart citing fix-$FID: $(ev restart | grep -o '"Fields":{[^}]*}')"
grep '"Kind":"implement"' "$EV" | grep -q '"Attempt":2' && ok "implement attempt 2 recorded" || fail "no implement attempt 2"
grep -q -- "$BAD" "$OUT/ps-uniq.txt" && echo "info: bad flag seen in ps: $(grep -c -- "$BAD" "$OUT/ps-uniq.txt") lines"
grep -v -- "$BAD" "$OUT/ps-uniq.txt" | grep -q 'implement' && ok "an implement agent ran without $BAD" || echo "info: no implement process line without the flag in ps (see ps-uniq.txt)"
ev landed > /dev/null && ok "landed: $(ev landed | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no landed event"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md ticked" || fail "todo-tiny.md not ticked"
grep -q "fix-$FID config: .*→ applied" "$OUT/status.txt" && ok "status lists $(grep "fix-$FID" "$OUT/status.txt")" || fail "status has no 'fix-$FID config: … → applied': $(grep fix "$OUT/status.txt")"
grep -q "fix-$FID config: .*→ applied" "$OUT/report.md" 2>/dev/null && ok "report lists $(grep "fix-$FID" "$OUT/report.md")" || fail "report has no 'fix-$FID config: … → applied'"
[ "$code" = 0 ] && ok "exit 0" || fail "exit $code"
grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited --status 0 > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "stop --expect-exited" || fail "stop --expect-exited rc=$src"
trap - EXIT
herdr workspace close "$RUNWS" > /dev/null 2>&1

echo "questions answered by the script: $(grep -c answered "$OUT/answers.txt" 2>/dev/null || true)"
echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
