#!/bin/sh
# usage: live_dialogs.sh A|B|C
#   A  codex implement writes its sentinel with no approval dialog (ADR-79)
#   B  codex implement asks to write outside the repo; a watchdog.dialogs rule approves it (ADR-80)
#   C  same dialog, no rule: the unattended watchdog declines with esc
# CODEX_APPROVALS=user overrides the codex provider so its approvals go to the pane, not codex's auto reviewer
set -u
MODE="${1:-A}"
case "$MODE" in A|B|C) ;; *) echo "usage: $0 A|B|C"; exit 2 ;; esac
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2400}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/agents" "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-dialogs-$MODE-$RAND"
PROBE="$HOME/r-loop-dialog-probe-$RAND.txt"
RULE="approve writing the file $PROBE"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
echo "probe   $PROBE"

"$BIN" docs/plan/todo-tiny.md --dry-run --plain > /dev/null 2> "$OUT/dryrun.err"
if grep -q 'migrate-config' "$OUT/dryrun.err"; then
  MH=$(mktemp -d)
  HOME=$MH "$BIN" --migrate-config > "$OUT/migrate.out" 2>&1
  rm -rf "$MH" .r-loop/config.yaml.bak
fi

python3 - "$MODE" "$RULE" "$PROBE" <<'EOF'
import os, re, sys
mode, rule, probe = sys.argv[1:]
p = ".r-loop/config.yaml"
s = open(p).read()
claude = "      - provider: claude\n        model: sonnet\n        effort: low\n"
s = re.sub(r"(    reviewers:\n)      - provider: codex\n        model: [^\n]+\n        effort: [^\n]+\n", r"\1" + claude.replace("\\", "\\\\"), s)
s = re.sub(r"(  implement:\n)    provider: claude\n    model: sonnet\n    effort: low\n",
           r"\1    provider: codex\n    model: gpt-5.6-sol\n    effort: medium\n", s)
s = re.sub(r"(  implement:\n(?:    [^\n]*\n)*?)    fallback:\n      provider: codex\n      model: [^\n]+\n      effort: [^\n]+\n",
           r"\1    fallback:\n      provider: claude\n      model: sonnet\n      effort: low\n", s)
if mode == "B":
    s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n  dialogs:\n    - " + rule + "\n")
reviewer = os.environ.get("CODEX_APPROVALS", "")
if reviewer:
    s += ("providers:\n  codex:\n    kind: codex\n"
          "    flags: \"-c check_for_update_on_startup=false -c sandbox_workspace_write.network_access=true -c approvals_reviewer=%s\"\n"
          "    modelFlag: \"-c model={model}\"\n    effortFlag: \"-c model_reasoning_effort={effort}\"\n"
          "    askFlag: \"-c mcp_servers.r-loop.url={url}\"\n"
          "    dirFlag: '-c sandbox_workspace_write.writable_roots=[\"{dir}\"]'\n"
          "    doneSignal: sentinel\n    ask: mcp\n"
          "    review: \"codex exec review --uncommitted {args} -o {output}\"\n") % reviewer
open(p, "w").write(s)
if mode in ("B", "C"):
    t = "docs/plan/todo-tiny.md"
    d = open(t).read()
    d = d.replace("covering a negative result\n",
                  "covering a negative result\n- [ ] write the file `%s` containing the single line `ok` (it is outside the repository on purpose: create it with a shell command such as `printf 'ok\\n' > %s`, and ask for approval if your sandbox blocks it)\n" % (probe, probe))
    open(t, "w").write(d)
EOF
cp .r-loop/config.yaml "$OUT/config.yaml"
cp docs/plan/todo-tiny.md "$OUT/todo-tiny.md"
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_dialogs $MODE setup"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run after config edit"; cat "$OUT/dryrun.out"; exit 1; }
grep -q 'implement.*codex' "$OUT/dryrun.out" || { echo "FAIL implement is not codex"; cat "$OUT/dryrun.out"; exit 1; }

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-dialogs}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frames/start-120x40.txt"

start=$(date +%s)
n=0
final=""
dialog_framed=""
while :; do
  n=$((n + 1))
  ps -axww -o pid=,command= | grep -F "$S" | grep -e 'codex' -e 'claude' | grep -v grep >> "$OUT/argv.txt"
  herdr agent list > "$OUT/agents/list.json" 2>/dev/null
  python3 - "$S" "$OUT/agents/list.json" > "$OUT/agents/ours.txt" <<'EOF'
import json, sys
root = sys.argv[1].rstrip("/")
for a in json.load(open(sys.argv[2]))["result"]["agents"]:
    cwd = a.get("cwd", "") + " " + a.get("foreground_cwd", "")
    if root in cwd:
        print(a["pane_id"], a.get("agent", "?"), a.get("agent_status", "?"))
EOF
  while read -r pane agent st; do
    echo "$(date +%T) $n $pane $agent $st" >> "$OUT/agents/timeline.txt"
    f="$OUT/agents/$(echo "$pane" | tr ':' '_')-$n.txt"
    herdr agent read "$pane" --source visible > "$f" 2>/dev/null
    if [ "$st" = blocked ] || grep -q -e 'Would you like to' -e 'Yes, proceed' -e 'Do you want to proceed' -e 'Do you want to make this edit' -e 'Do you want to create' "$f"; then
      echo "$(date +%T) $n $pane $agent $st" >> "$OUT/approval-hits.txt"
    else
      [ "$st" = blocked ] || rm -f "$f"
    fi
  done < "$OUT/agents/ours.txt"
  EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
  if [ -z "$dialog_framed" ] && [ -n "$EV" ] && grep -q '"Kind":"dialog"' "$EV"; then
    dialog_framed=1
    cp "$("$TUI" capture "$H")" "$OUT/frames/dialog-120x40.txt"
    cp "$("$TUI" capture "$H" --ansi)" "$OUT/frames/dialog-120x40.ansi"
    "$TUI" resize "$H" 80x24 > /dev/null && sleep 1 && cp "$("$TUI" capture "$H")" "$OUT/frames/dialog-80x24.txt"
    "$TUI" resize "$H" 120x40 > /dev/null
  fi
  [ $((n % 4)) = 1 ] || { sleep 3; continue; }
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  sleep 3
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
ls -R "$RUN" > "$OUT/rundir.txt"

python3 - "$EV" > "$OUT/implement-events.txt" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    if r.get("Kind") == "step" and (r.get("Step") or {}).get("Kind") == "implement":
        print("state", r["Step"].get("Attempt"), r.get("State"), r.get("Reason", ""))
    e = r.get("Event") or {}
    if e.get("Step") == "implement" or e.get("Kind") in ("dialog", "dialog-answered", "watchdog-call"):
        print(e.get("Kind"), e.get("Phase"), e.get("Step"), json.dumps(e.get("Fields") or {}, ensure_ascii=False)[:400])
EOF

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
grep -q 'writable_roots=\["'"$S"'/.r-loop/runs/[^"]*/phase-1' "$OUT/argv.txt" \
  && ok "codex argv carries writable_roots: $(grep -o 'writable_roots=\[[^]]*\]' "$OUT/argv.txt" | sort -u | head -2 | tr '\n' ' ')" \
  || fail "no codex argv with writable_roots on the phase run folder"
grep -q -- '--add-dir '"$S"'/.r-loop/runs/[^ ]*/phase-1' "$OUT/argv.txt" \
  && ok "claude argv carries --add-dir on the phase run folder" || fail "no claude argv with --add-dir on the phase run folder"
grep -q '^state 1 running' "$OUT/implement-events.txt" && grep -q '^state [0-9]* ok' "$OUT/implement-events.txt" \
  && ok "implement running -> ok: $(grep '^state' "$OUT/implement-events.txt" | cut -d' ' -f2,3 | tr '\n' ' ')" \
  || fail "implement did not go running -> ok: $(grep '^state' "$OUT/implement-events.txt" | tr '\n' ';')"
grep -q '^state [0-9]* stalled' "$OUT/implement-events.txt" && fail "implement went stalled" || ok "implement never stalled"
ls "$RUN"/phase-1/*implement*.json > /dev/null 2>&1 && ok "implement sentinel in the run folder: $(ls "$RUN"/phase-1 | grep implement | tr '\n' ' ')" || fail "no implement sentinel in $RUN/phase-1"
for k in stalled nudge; do
  c=$(grep "\"Kind\":\"$k\"" "$EV" | grep -c '"Step":"implement"')
  [ "$c" = 0 ] && ok "no implement $k events" || fail "$c implement $k events: $(grep "\"Kind\":\"$k\"" "$EV" | head -2)"
done
dialogs=$(grep -c '"Kind":"dialog"' "$EV")
answered=$(grep -c '"Kind":"dialog-answered"' "$EV")
case "$MODE" in
  A)
    [ "$dialogs" = 0 ] && ok "no dialog events" || fail "$dialogs dialog events: $(grep '"Kind":"dialog"' "$EV" | head -2)"
    [ ! -s "$OUT/approval-hits.txt" ] && ok "no approval prompt seen in any pane over $n polls" || fail "approval prompt seen: $(cat "$OUT/approval-hits.txt")"
    ;;
  B|C)
    [ "$dialogs" -ge 1 ] && ok "$dialogs dialog event(s): $(grep '"Kind":"dialog"' "$EV" | head -1 | cut -c1-300)" || fail "no dialog event"
    [ "$answered" -ge 1 ] && ok "dialog-answered: $(grep '"Kind":"dialog-answered"' "$EV" | tr '\n' ' ' | cut -c1-500)" || fail "no dialog-answered event"
    grep '"Kind":"watchdog-call"' "$EV" | grep -q answer_dialog && ok "watchdog-call answer_dialog recorded" || fail "no watchdog-call answer_dialog"
    if [ "$MODE" = B ]; then
      grep '"Kind":"dialog-answered"' "$EV" | grep -F "\"rule\":\"$RULE\"" | grep -q '"by":"watchdog"' && ok "answered by watchdog under the exact rule" || fail "no dialog-answered by=watchdog with rule=$RULE"
      [ "$(cat "$PROBE" 2>/dev/null)" = ok ] && ok "probe file written" || fail "probe file missing or wrong: $(ls -l "$PROBE" 2>&1)"
    else
      grep '"Kind":"dialog-answered"' "$EV" | grep '"rule":"decline"' | grep -q '"keys":"esc"' && ok "declined with rule=decline keys=esc" || fail "no decline/esc answer"
      grep -q 'no response to nudge' "$EV" && fail "stalled: no response to nudge present" || ok "no 'no response to nudge'"
    fi
    grep -q 'watchdog · d1' "$OUT/frames/dialog-120x40.txt" 2>/dev/null && ok "TUI row shows 'watchdog · d1' at 120x40" || fail "no 'watchdog · d1' in the dialog frame (frame may have been taken after it closed)"
    grep -q -i 'dialog' "$OUT/status.txt" && ok "status --plain lists the dialog: $(grep -i dialog "$OUT/status.txt" | head -2 | tr '\n' ' ')" || fail "status --plain has no dialog line"
    grep -q -i 'dialog' "$OUT/report.md" 2>/dev/null && ok "report.md lists the dialog: $(grep -i dialog "$OUT/report.md" | head -2 | tr '\n' ' ')" || fail "report.md has no dialog line"
    ;;
esac
git log --oneline > "$OUT/gitlog.txt"
[ "$(wc -l < "$OUT/gitlog.txt")" -ge 3 ] && ok "phase commit landed: $(head -1 "$OUT/gitlog.txt")" || fail "no phase commit"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "todo-tiny.md not ticked"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
case "$final" in
  *finished*) [ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))" ;;
  *) grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits a run that did not finish, alt screen off ($(cat "$OUT/tui-status.txt"))" || fail "after q: $(cat "$OUT/tui-status.txt")" ;;
esac
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
echo "probe   $PROBE"
exit $((fails > 0))
