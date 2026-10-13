#!/bin/sh
# live_hooks.sh — claude sessions report through Claude Code hooks on a real todo-tiny run at 120x40:
#   the run's claude.settings.json carries a hooks block running `r-loop hook-tap <runDir>/hooks.jsonl`,
#   the AGENTS tree shows a live step's activity (editing/reading/running …) in place of `running`
#   (a reviewer's activity in place of `reviewing` is best-effort), hooks.jsonl holds valid lines from step
#   (and claude reviewer) agents only, never the watchdog, activity stays out of events.jsonl, steps end ok,
#   the phase lands, q quits and the terminal is restored.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"
export HERDR_SOCKET_PATH="${HERDR_SOCKET_PATH:-${XDG_CONFIG_HOME:-$HOME/.config}/herdr/herdr.sock}"

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-1500}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-hooks-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
python3 - .r-loop/config.yaml <<'EOF' || { echo "NOT RUN: neither the plan nor the implement step runs on claude"; exit 3; }
import re, sys
t = open(sys.argv[1]).read()
for k in ("plan", "implement"):
    m = re.search(r"^  " + k + r":\n    provider: (\S+)", t, re.M)
    print(f"     {k} step provider: {m.group(1) if m else '?'}")
    if m and m.group(1) == "claude":
        sys.exit(0)
sys.exit(1)
EOF

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-hooks}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
start=$(date +%s)

ACT='· [^ ]+ +(editing |reading |running [^ ]|searching |asking watchdog|subagent|fixing r[0-9]+ · )'
RVACT='─ ● r1 [^ ]+ +[^ ]+ +(editing |reading |running [^ ]|searching |asking watchdog|subagent)'
step_act=""
rv_act=""
settings_checked=""
n=0
final=""
while :; do
  if [ -z "$settings_checked" ]; then
    SET=$(ls .r-loop/runs/*/claude.settings.json 2>/dev/null | head -1)
    if [ -n "$SET" ]; then
      settings_checked=1
      cp "$SET" "$OUT/claude.settings.json"
      if python3 - "$SET" <<'EOF'
import json, os, sys
p = sys.argv[1]
want = os.path.join(os.path.dirname(os.path.abspath(p)), "hooks.jsonl")
h = json.load(open(p)).get("hooks")
assert isinstance(h, dict) and h, "no hooks object"
need = {"UserPromptSubmit", "PreToolUse", "PostToolUse", "Notification", "Stop"}
assert need <= set(h), f"missing events {need - set(h)}"
cmds = [(ev, g.get("matcher"), c.get("command", "")) for ev, groups in h.items() for g in groups for c in g.get("hooks", [])]
for c in cmds:
    print("     ", c)
bad = [c for c in cmds if "hook-tap" not in c[2] or not c[2].rstrip().rstrip("'\"").endswith(want)]
assert cmds and not bad, f"bad commands {bad} (want suffix {want})"
EOF
      then ok "claude.settings.json hooks block ($SET)"
      else fail "claude.settings.json hooks block ($SET)"
      fi
    fi
  fi
  n=$((n + 1))
  if [ -z "$step_act" ] || [ -z "$rv_act" ]; then
    if f=$("$TUI" capture "$H" 2> /dev/null); then
      if [ -z "$step_act" ] && grep -E "(├|└)─ ● (plan|implement) " "$f" | grep -qE "$ACT"; then
        cp "$f" "$OUT/frames/step-activity.txt"
        step_act=$(grep -oE "(├|└)─ ● (plan|implement) .*" "$f" | grep -E "$ACT" | head -1 | tr -s ' ')
      fi
      if [ -z "$rv_act" ] && grep -qE "$RVACT" "$f"; then
        cp "$f" "$OUT/frames/reviewer-activity.txt"
        rv_act=$(grep -oE "(├|└)─ ● r1 .*" "$f" | grep -E "$RVACT" | head -1 | tr -s ' ')
      fi
      [ $((n % 60)) = 0 ] && cp "$f" "$OUT/frames/poll-$n.txt"
    fi
  fi
  [ $((n % 5)) = 0 ] || { sleep 1; continue; }
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
    "$BIN" status --plain > "$OUT/status.txt" 2>&1
    break
  fi
  sleep 1
done
echo "end     $final ($(( $(date +%s) - start ))s)"
[ -n "$settings_checked" ] || fail "no claude.settings.json in the run dir"
[ -n "$step_act" ] && ok "step activity in AGENTS tree ($OUT/frames/step-activity.txt): $step_act" || fail "no frame showed a live plan/implement node with an activity (polled $n frames)"
[ -n "$rv_act" ] && ok "reviewer activity in AGENTS tree ($OUT/frames/reviewer-activity.txt): $rv_act" || echo "NOTE best-effort: no frame caught a reviewer node with an activity (only claude reviewers report hooks)"
case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
sleep 3
f=$("$TUI" capture "$H" 2> /dev/null) && cp "$f" "$OUT/frames/end.txt"
"$TUI" send "$H" q > /dev/null; src=$?
[ "$src" = 0 ] || fail "send q rc=$src"
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
echo "tui     $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "q then stop --expect-exited 0 ($(cat "$OUT/tui-status.txt"))" || fail "stop --expect-exited rc=$src ($(cat "$OUT/tui-status.txt"))"
trap - EXIT

RUN=$(ls -d .r-loop/runs/*/ 2>/dev/null | head -1)
RUN=${RUN%/}
HJ="$RUN/hooks.jsonl"
if [ -s "$HJ" ]; then
  cp "$HJ" "$OUT/hooks.jsonl"
  python3 - "$HJ" .r-loop/config.yaml <<'EOF' || fails=$((fails + 1))
import json, re, sys
lines = open(sys.argv[1], encoding="utf-8").read().splitlines()
bad = []
def say(ok, msg):
    print(("OK   " if ok else "FAIL ") + msg)
    if not ok:
        bad.append(msg)
recs, invalid = [], []
for l in lines:
    try:
        r = json.loads(l)
        recs.append(r)
        if not r.get("agent") or not r.get("event"):
            invalid.append(l)
    except Exception:
        invalid.append(l)
say(not invalid, f"hooks.jsonl: {len(lines)} lines, all valid JSON with agent+event (bad: {invalid[:2]!r})")
agents = sorted({r.get("agent", "") for r in recs})
events = {}
for r in recs:
    events[r.get("event")] = events.get(r.get("event"), 0) + 1
print("     agents:", agents)
print("     events:", events)
say(any(re.search(r"-p\d+-plan$", a) for a in agents), "hooks.jsonl: a plan step agent reported")
say(any(re.search(r"-p\d+-implement$", a) for a in agents), "hooks.jsonl: an implement step agent reported")
cfg = open(sys.argv[2]).read()
claude_rv = re.search(r"reviewers:\n(\s+- provider: \S+\n(\s{8}\S.*\n)*)*?\s+- provider: claude", cfg) is not None
rv = [a for a in agents if "-rv-" in a]
if claude_rv:
    say(bool(rv), f"hooks.jsonl: a claude reviewer agent reported: {rv}")
else:
    print(f"NOTE no claude reviewer configured; -rv- agents seen: {rv}")
say(not [a for a in agents if a.startswith("rloop-wd-") or "watchdog" in a], "hooks.jsonl: no watchdog lines")
say(all(a.startswith("rloop-") for a in agents), "hooks.jsonl: every agent is an rloop-… session")
for ev in ("UserPromptSubmit", "PreToolUse", "Stop"):
    say(events.get(ev, 0) > 0, f"hooks.jsonl: {ev} seen ({events.get(ev, 0)})")
edits = [r for r in recs if r.get("event") == "PreToolUse" and r.get("tool") in ("Edit", "Write", "MultiEdit", "NotebookEdit")]
if edits:
    say(events.get("PostToolUse", 0) > 0, f"hooks.jsonl: PostToolUse seen after {len(edits)} edit tool calls ({events.get('PostToolUse', 0)})")
print("     sample:")
seen = set()
for r in recs:
    k = (r.get("event"), r.get("tool"))
    if k not in seen and len(seen) < 8:
        seen.add(k)
        print("      ", json.dumps(r)[:240])
sys.exit(1 if bad else 0)
EOF
else
  fail "hooks.jsonl missing or empty at $HJ"
fi

EV="$RUN/events.jsonl"
if [ -f "$EV" ]; then
  cp "$EV" "$OUT/events.jsonl"
  act=$(grep -ciE '"kind":"activity"' "$EV")
  [ "$act" = 0 ] && ok "no activity records in events.jsonl" || fail "$act activity records in events.jsonl"
  errs=$(grep -cE '"Kind":"(error|restart-refused)"|"Level":"error"' "$EV")
  [ "$errs" = 0 ] && ok "no error-level events in $EV" || fail "$errs error-level events: $(grep -E '"Kind":"(error|restart-refused)"|"Level":"error"' "$EV" | head -3)"
  over=$(grep -c '"Kind":"turn-overran-sentinel"' "$EV")
  echo "INFO turn-overran-sentinel events: $over $(grep '"Kind":"turn-overran-sentinel"' "$EV" | head -3 | cut -c1-300)"
  python3 - "$EV" <<'EOF' || fails=$((fails + 1))
import json, sys
last = {}
for l in open(sys.argv[1]):
    r = json.loads(l)
    if r.get("Kind") == "step" and r.get("Step"):
        s = r["Step"]
        last[(s["Phase"], s["Kind"], s["Attempt"])] = (r.get("State"), r.get("Reason", ""))
for k, v in sorted(last.items()):
    print("     step", k, v)
bad = {k: v for k, v in last.items() if v[0] != "ok"}
print(("OK   " if last and not bad else "FAIL ") + f"every step ended ok ({len(last)} steps, not ok: {bad})")
sys.exit(1 if bad or not last else 0)
EOF
else
  fail "no events.jsonl"
fi

echo "--- status"; "$BIN" status --plain 2>&1 | head -20
echo "--- git log"; git log --oneline | head -5
grep -qE '^- \[x\]' docs/plan/todo-tiny.md && ok "todo-tiny box ticked: $(grep -E '^- \[x\]' docs/plan/todo-tiny.md | head -1)" || fail "no ticked box in docs/plan/todo-tiny.md"
grep -q '"Kind":"landed"' "$EV" 2>/dev/null && ok "phase landed event present" || fail "no landed event"

echo "failures: $fails"
echo "sandbox $S"
echo "out     $OUT"
exit $((fails > 0))
