#!/bin/sh
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
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/agents"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

"$BIN" docs/plan/todo-tiny.md --dry-run --plain > /dev/null 2> "$OUT/dryrun.err"
if grep -q 'migrate-config' "$OUT/dryrun.err"; then
  echo "NOTE sandbox config predates the provider/model/effort blocks; migrating the copy"
  MH=$(mktemp -d)
  HOME=$MH "$BIN" --migrate-config > "$OUT/migrate.out" 2>&1
  rm -rf "$MH" .r-loop/config.yaml.bak
  git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "migrate config"
fi

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-promptfix}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frame-start.txt"

start=$(date +%s)
n=0
final=""
while :; do
  n=$((n + 1))
  herdr agent list > "$OUT/agents/list-$n.json" 2>/dev/null
  python3 - "$S" "$OUT/agents/list-$n.json" > "$OUT/agents/ours-$n.txt" <<'EOF'
import json, sys
root = sys.argv[1].rstrip("/")
alt = root[len("/private"):] if root.startswith("/private/") else root
for a in json.load(open(sys.argv[2]))["result"]["agents"]:
    cwd = a.get("cwd", "") + " " + a.get("foreground_cwd", "")
    if root in cwd or alt in cwd:
        print(a["pane_id"], a.get("agent", "?"), a.get("agent_status", "?"), a.get("terminal_title_stripped", "").replace(" ", "_"))
EOF
  while read -r pane agent st title; do
    echo "$(date +%s) $n $pane $agent $st" >> "$OUT/agents/timeline.txt"
    f="$OUT/agents/$(echo "$pane" | tr ':' '_')-$n.txt"
    herdr agent read "$pane" --source visible > "$f" 2>/dev/null
    if grep -q '\[Pasted \(Content\|text\)' "$f"; then
      echo "$n $pane $agent $st" >> "$OUT/pasted-hits.txt"
    fi
    grep -q -e 'Do you trust the contents of this directory' -e 'Trust this folder?' "$f" && echo "$(date +%T) $n $pane $agent $st" >> "$OUT/trust-dialog.txt"
  done < "$OUT/agents/ours-$n.txt"
  [ $((n % 6)) = 1 ] || { sleep 3; continue; }
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
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
cp "$EV" "$OUT/events.jsonl"
cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

grep -q 'not submitted' "$EV" && fail "a prompt was not submitted: $(grep 'not submitted' "$EV" | head -2)" || ok "no 'not submitted' anywhere in events.jsonl"
grep -rq 'not submitted' "$(dirname "$EV")" && fail "'not submitted' in run dir" || ok "no 'not submitted' anywhere in the run dir"

for k in stalled nudge; do
  c=$(grep -c "\"Kind\":\"$k\"" "$EV")
  [ "$c" = 0 ] && ok "no $k events" || fail "$c $k events: $(grep "\"Kind\":\"$k\"" "$EV" | head -2)"
done

named=$(grep -c '"Kind":"agent-named"' "$EV")
[ "$named" -ge 4 ] && ok "$named agent-named events (watchdog/steps/reviewers)" || fail "only $named agent-named events"

if [ -s "$OUT/pasted-hits.txt" ]; then
  stuck=$(awk '{ k = $2; if (k in seen && seen[k] == $1 - 1) print; seen[k] = $1 }' "$OUT/pasted-hits.txt")
  [ -z "$stuck" ] && ok "paste marker seen only transiently: $(tr '\n' ';' < "$OUT/pasted-hits.txt")" \
    || fail "paste marker sat in a composer across polls: $stuck"
else
  ok "no pane showed an unsubmitted [Pasted ...] composer in $n polls"
fi
if [ -s "$OUT/trust-dialog.txt" ]; then
  stuck=$(awk '{ k = $3; if (k in seen && seen[k] == $2 - 1) print; seen[k] = $2 }' "$OUT/trust-dialog.txt")
  [ -z "$stuck" ] && ok "trust dialog seen only transiently (answered at start): $(tr '\n' ';' < "$OUT/trust-dialog.txt")" \
    || fail "an agent sat on its trust dialog across polls: $stuck"
else
  ok "no agent seen on a trust dialog"
fi

if grep -q ' codex ' "$OUT/agents/timeline.txt" 2>/dev/null; then
  summary=$(awk '$4 == "codex" { if (!($3 in first)) first[$3] = $1; if ($5 == "working" && !($3 in work)) work[$3] = $1 }
    END { for (p in first) printf "%s:%s ", p, (p in work) ? (work[p] - first[p]) "s" : "never" }' "$OUT/agents/timeline.txt")
  slow=$(echo "$summary" | tr ' ' '\n' | awk -F: '$NF == "never" || $NF + 0 > 45')
  [ -z "$slow" ] && ok "every codex pane started a turn soon after it appeared: $summary" || fail "codex pane slow to start its turn: $slow"
else
  fail "no codex pane seen in any poll"
fi

bad=$(grep -E 'not submitted|never showed codex|never settled' "$EV" | head -2)
[ -z "$bad" ] && ok "no prompt-delivery reason in events.jsonl" || fail "prompt-delivery reason: $bad"

find "$(dirname "$EV")" -name '*-findings-codex-*.json' > "$OUT/findings.txt"
for k in plan implement; do
  grep -q "/$k-findings-codex-" "$OUT/findings.txt" && ok "$k reviewer findings file written" || fail "no $k-findings-codex file: $(cat "$OUT/findings.txt")"
done

git log --oneline > "$OUT/gitlog.txt"
[ "$(wc -l < "$OUT/gitlog.txt")" -ge 3 ] && ok "phase commit landed: $(head -1 "$OUT/gitlog.txt")" || fail "no phase commit: $(cat "$OUT/gitlog.txt")"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "todo-tiny.md not ticked"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
case "$final" in
  *finished*) [ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))" ;;
  *) grep -q 'exited.* alt=0' "$OUT/tui-status.txt" && ok "q quits a run that did not finish, alt screen off ($(cat "$OUT/tui-status.txt"))" || fail "after q: $(cat "$OUT/tui-status.txt")" ;;
esac
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
