#!/usr/bin/env bash
# usage: live_read_agent.sh  (herdr-app when HC_TERMINAL_ID is set; HERDR_PANE_ID=any picks herdr)
#   docs/plan/todo-tiny.md --unattended, started in a real terminal of the host, the TUI in that pane. While a step is
#   live the script types into the watchdog's session and asks for read_agent on the live step, then on no-such-agent.
#   Evidence comes from the watchdog's claude transcript: tool_use and tool_result of mcp__r-loop__read_agent, no Bash
#   `herdr agent read`, the rendered prompt in its first user message. The run must still land and tick its box.
set -u
. "$(dirname "$0")/host.sh"
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
host_up || { echo "NOT RUN: no session host (herdr or herdr-app)"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
OUT="${OUT:-$(mktemp -d)}"
LIMIT="${LIVE_LIMIT:-2400}"
mkdir -p "$OUT"
fails=0 passes=0
ok()   { echo "OK   $1"; passes=$((passes + 1)); }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
log()  { echo "---- $(date +%H:%M:%S) $*"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
S=$(cd "$S" && pwd -P)
cd "$S" || exit 1
echo "host    $HOST ($HOST_SELF)"
echo "sandbox $S"
echo "out     $OUT"

EV() { ls "$S"/.r-loop/runs/*/events.jsonl 2>/dev/null | head -1; }
q()  { local f; f=$(EV); [ -n "$f" ] && jq -c "select($1)" "$f" 2>/dev/null; }
has() { [ -n "$(q "$1" | head -1)" ]; }
wait_q() {
  local t=0
  while [ "$t" -lt "$2" ]; do has "$1" && return 0; sleep 2; t=$((t + 2)); done
  return 1
}
term_text() {
  case "$HOST" in
    herdr) herdr pane read "$HOST_SELF" 2>/dev/null ;;
    app) hc_ read --json "$HOST_SELF" 2>/dev/null | jq -r .text ;;
  esac
}
term_keys() {
  case "$HOST" in
    herdr) herdr pane send-keys "$HOST_SELF" "$@" ;;
    app) hc_ keys --json "$HOST_SELF" "$@" ;;
  esac
}

sleep 3
CMD="cd $S && $BIN docs/plan/todo-tiny.md --unattended; echo \$? > $OUT/rc"
case "$HOST" in
  herdr) herdr pane run "$HOST_SELF" "$CMD" > "$OUT/start.json" 2>&1 ;;
  app) hc_ send --json --force "$HOST_SELF" "$CMD" > "$OUT/start.json" 2>&1 ;;
esac
echo "start rc=$? $(cut -c1-200 "$OUT/start.json")"
run_spaces() {
  case "$HOST" in
    herdr) herdr agent list | jq -r --arg s "$S" '.result.agents[] | select((.cwd // "") | startswith($s)) | .workspace_id' | sort -u ;;
    app) for _t in $(hc_ ls --json | jq -r '.terminals[].address'); do hc_ get --json "$_t" | jq -r --arg s "$S" 'select(((.foreground.cwd // .cwd // "") | startswith($s))) | .labels["herdr.space"] // empty'; done | sort -u ;;
  esac
}
teardown() {
  term_text > "$OUT/term-at-exit.txt"
  ws_close "$R_LOOP_TEST_WS" > /dev/null 2>&1
  sleep 2
  for _w in $(run_spaces); do ws_close "$_w" > /dev/null 2>&1; echo "closed leftover $_w"; done
}
trap teardown EXIT INT TERM

log "waiting for a live plan step"
if ! wait_q '.Kind=="step" and .Step.Phase=="1" and .Step.Kind=="plan" and .State=="running"' 600; then
  fail "plan step never ran"
  term_text | tail -30
  exit 1
fi
STEP=$(q '.Event.Kind=="agent-named" and .Event.Step=="plan" and (.Event.Fields.reviewer|not)' | head -1 | jq -r .Event.Fields.agent)
echo "step    $STEP"
RUN=$(basename "$(dirname "$(EV)")")
echo "run     $RUN"
sed -n 1,3p "$S/.r-loop/runs/$RUN/config.resolved.yaml" | sed 's/^/     cfg: /'
q '.Event.Kind=="watchdog-placed"' | jq -c .Event.Fields | sed 's/^/     placed: /'

find_dog() {
  case "$HOST" in
    herdr) herdr agent list | jq -r '.result.agents[] | (.name // .agent_name // empty)' | grep -- "-$RUN\$" | head -1 ;;
    app) for _t in $(hc_ ls --json | jq -r '.terminals[].address'); do hc_ get --json "$_t" | jq -r '.labels["rloop.agent"] // empty'; done | grep -- "-$RUN\$" | head -1 ;;
  esac
}
DOG=$(find_dog)
[ -n "$DOG" ] || { fail "no watchdog agent named …-$RUN"; exit 1; }
echo "dog     $DOG"

ENC=$(printf '%s' "$S" | sed 's#[/.]#-#g')
TDIR="$HOME/.claude/projects/$ENC"
transcript() { grep -l 'You watch an r-loop run' "$TDIR"/*.jsonl 2>/dev/null | head -1; }
tool_count() { local t; t=$(transcript); if [ -n "$t" ]; then grep -c "$1" "$t"; else echo 0; fi; }

log "host facts for $STEP"
{
  echo "herdr agent get: $(herdr agent get "$STEP" 2>&1 | cut -c1-200)"
  echo "herdr agent read: $(herdr agent read "$STEP" --source recent-unwrapped --lines 5 2>&1 | cut -c1-200)"
  echo "hc ls label: $(hc_ ls --json --label "rloop.agent=$STEP" 2>&1 | jq -c '[.terminals[]?.address]' 2>/dev/null)"
} | tee "$OUT/host-facts.txt" | sed 's/^/     /'

URL=$("$HERE/wd_url.sh" "$S" 2>/dev/null)
(
  while [ -n "$URL" ] && [ ! -f "$OUT/probes.stop" ]; do
    st=$(agent_state "$STEP")
    for n in 5 20 40 60 200; do
      r=$("$HERE/wd_call.sh" "$URL" read_agent "{\"name\":\"$STEP\",\"lines\":$n}" 2>&1 | jq -c '{found,chars:((.text//"")|length),lines:((.text//"")|split("\n")|length),reason:((.reason//"")[0:40])}' 2>/dev/null)
      echo "$(date +%T) $st lines=$n $r" >> "$OUT/probes.txt"
    done
    sleep 6
  done
) &
PROBES=$!

log "asking the watchdog to read the live step"
ST=$(q '.Kind=="step" and .Step.Kind=="plan"' | tail -1 | jq -r .State)
echo "     plan step state now: $ST"
prompt "$DOG" "Call read_agent now with the name of the live step's agent, and report the first line you got." > "$OUT/prompt1.out" 2>&1
echo "     prompt rc=$? $(cut -c1-200 "$OUT/prompt1.out")"
t=0
until [ "$(tool_count '"name":"mcp__r-loop__read_agent"')" -ge 1 ] || [ $t -ge 240 ]; do sleep 2; t=$((t + 2)); done
echo "     first read_agent seen after ${t}s"
screen "$STEP" recent > "$OUT/step-pane.txt" 2>&1
screen "$DOG" recent > "$OUT/dog-pane-1.txt" 2>&1
q '.Kind=="step" and .Step.Kind=="plan"' | tail -1 | jq -r '"     plan step state after: " + .State'

sleep 15
log "asking the watchdog to read an unknown name"
prompt "$DOG" 'Call read_agent now with name "no-such-agent" and report the result verbatim.' > "$OUT/prompt2.out" 2>&1
echo "     prompt rc=$? $(cut -c1-200 "$OUT/prompt2.out")"
t=0
until grep -q '"name":"no-such-agent"' "$(transcript)" 2>/dev/null || [ $t -ge 240 ]; do sleep 2; t=$((t + 2)); done
echo "     unknown-name call seen after ${t}s"
sleep 8
screen "$DOG" recent > "$OUT/dog-pane-2.txt" 2>&1

log "waiting for $STEP to go idle, then asking again"
t=0
while [ $t -lt 400 ]; do st=$(agent_state "$STEP"); case "$st" in idle|done|gone) break ;; esac; sleep 2; t=$((t + 2)); done
echo "     $STEP is $st after ${t}s"
n0=$(tool_count '"name":"mcp__r-loop__read_agent"')
prompt "$DOG" "Call read_agent now with name \"$STEP\" and no lines argument, and report the first line you got." > "$OUT/prompt3.out" 2>&1
t=0
until [ "$(tool_count '"name":"mcp__r-loop__read_agent"')" -gt "$n0" ] || [ $t -ge 120 ]; do sleep 2; t=$((t + 2)); done
screen "$STEP" recent > "$OUT/step-pane-idle.txt" 2>&1
echo "     agent state at the idle read: $(agent_state "$STEP")"

touch "$OUT/probes.stop"
wait "$PROBES" 2>/dev/null

log "waiting for the run to end"
start=$(date +%s)
final=""
while [ $(( $(date +%s) - start )) -lt "$LIMIT" ]; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then final=$(head -1 "$OUT/status.txt"); break; fi
  sleep 5
done
echo "end     ${final:-timeout} ($(( $(date +%s) - start ))s)"
wait_q '.Event.Kind=="run-report" or .Event.Kind=="run-report-missing" or .Event.Kind=="failure-report"' 900
sleep 5
term_text > "$OUT/term-end.txt"
term_keys q > /dev/null 2>&1
t=0; until [ -f "$OUT/rc" ] || [ $t -ge 60 ]; do sleep 2; t=$((t + 2)); done
echo "exit    $(cat "$OUT/rc" 2>/dev/null)"

T=$(transcript)
echo "transcript $T"
cp "$(EV)" "$OUT/events.jsonl"
"$BIN" status --plain > "$OUT/status.txt" 2>&1
sed 's/^/     status: /' "$OUT/status.txt" | head -12
git log --oneline | sed 's/^/     git: /'
grep -n 'Subtract' docs/plan/todo-tiny.md | head -3 | sed 's/^/     todo: /'

python3 -I - "$T" "$STEP" "$OUT/step-pane.txt" <<'PY' | tee "$OUT/transcript-check.txt"
import json, sys
path, step, pane = sys.argv[1:4]
uses, results, bash, first_user = {}, {}, [], None
for line in open(path):
    try: r = json.loads(line)
    except ValueError: continue
    m = r.get("message") or {}
    c = m.get("content")
    if r.get("type") == "user" and first_user is None and "You watch an r-loop run" in json.dumps(c):
        first_user = c if isinstance(c, str) else "\n".join(b.get("text", "") for b in c if isinstance(b, dict))
    if not isinstance(c, list): continue
    for b in c:
        if not isinstance(b, dict): continue
        if b.get("type") == "tool_use":
            if b.get("name") == "mcp__r-loop__read_agent": uses[b["id"]] = b["input"]
            if b.get("name") == "Bash": bash.append(b["input"].get("command", ""))
        if b.get("type") == "tool_result":
            t = b.get("content")
            if isinstance(t, list): t = "".join(x.get("text", "") for x in t if isinstance(x, dict))
            results[b["tool_use_id"]] = t or ""
ok = lambda v: "PASS" if v else "FAIL"
pane_lines = {l.strip() for l in open(pane, errors="replace") if len(l.strip()) > 8}
first, later = None, False
print("read_agent calls:", len(uses))
for i, inp in uses.items():
    res = results.get(i, "")
    print("  input :", json.dumps(inp))
    print("  result:", res[:150].replace("\n", "\\n"), f"[{len(res)} chars]")
    if inp.get("name") == step:
        try: o = json.loads(res)
        except ValueError: o = {}
        txt = o.get("text", "")
        common = [l.strip() for l in txt.splitlines() if l.strip() in pane_lines]
        print(f"  found={o.get('found')} text_chars={len(txt)} lines_also_in_direct_pane_read={len(common)}; sample: {common[:2]}")
        if first is None: first = o.get("found") is True and len(txt) > 0 and len(common) > 0
        else: later = later or (o.get("found") is True and len(txt) > 0)
print(ok(first), "check2 first read_agent on the working step agent, found:true, text is that pane's output")
print(ok(later), "check2b a later read_agent on the step agent, found:true")
nf = [k for k, v in results.items() if "agent_not_found" in v and uses.get(k, {}).get("name") != "no-such-agent"]
hb = [c for c in bash if "herdr agent read" in c]
print(ok(not nf), "check3 no agent_not_found outside the no-such-agent call:", nf)
print(ok(not hb), "check3 no `herdr agent read` Bash call; Bash calls total", len(bash), hb[:3])
fu = first_user or ""
print(ok("read_agent" in fu and "herdr agent read" not in fu), f"check4 rendered prompt: read_agent x{fu.count('read_agent')}, herdr agent read x{fu.count('herdr agent read')}, {len(fu)} chars")
bad = [(inp, results.get(i, "")) for i, inp in uses.items() if inp.get("name") == "no-such-agent"]
for inp, res in bad: print("  unknown:", res[:300])
good = False
for _, res in bad:
    try: o = json.loads(res)
    except ValueError: o = {}
    good = good or (o.get("found") is not True and bool(o.get("reason")))
print(ok(good), "check5 no-such-agent: found:false with a reason")
PY
if grep -q '^FAIL' "$OUT/transcript-check.txt"; then fail "transcript checks"; else ok "transcript checks"; fi
if has '.Event.Kind=="landed" or .Kind=="landing"'; then ok "landed"; else fail "no landed record"; fi
case "$final" in *finished*) ok "run finished" ;; *) fail "run end: ${final:-timeout}" ;; esac
if grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md; then ok "todo-tiny.md ticked"; else fail "box not ticked"; fi
if [ "$(cat "$OUT/rc" 2>/dev/null)" = 0 ]; then ok "r-loop exit 0"; else fail "r-loop exit $(cat "$OUT/rc" 2>/dev/null)"; fi
if [ -z "$(git status --porcelain --untracked-files=no)" ]; then ok "tree clean"; else fail "tree dirty"; fi
jq -r 'select(.Event.Kind=="warning" or .Event.Kind=="error" or .Event.Kind=="halt")|.Event|[.Kind,.Phase,.Step,(.Fields|tostring)]|join(" ")' "$(EV)" | cut -c1-300 | sed 's/^/     note: /'
echo "RESULT: $passes passed, $fails failed"
exit "$((fails > 0))"
