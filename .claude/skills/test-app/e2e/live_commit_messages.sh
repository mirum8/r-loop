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
mkdir -p "$OUT"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
SHAPE='^(feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: .+$'
subject_ok() {
  printf '%s' "$1" | grep -Eq "$SHAPE" || return 1
  [ "$(printf '%s' "$1" | wc -m | tr -d ' ')" -le 100 ] || return 1
  printf '%s' "$1" | grep -Eqi 'phase|r-loop:' && return 1
  return 0
}

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"
BASE=$(git rev-parse --abbrev-ref HEAD)

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-commitmsg}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frame-start.txt"

start=$(date +%s)
final=""
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  sleep 15
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

RUN=$(dirname "$(ls .r-loop/runs/*/events.jsonl | head -1)")
EV="$RUN/events.jsonl"
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
find "$RUN" -name '*.sentinel' -exec sh -c 'for f; do echo "== $f"; cat "$f"; echo; done' _ {} + > "$OUT/sentinels.txt" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
grep -q 'sentinel commit' "$EV" && fail "a step failed on its commit subject: $(grep 'sentinel commit' "$EV" | head -2)" || ok "no 'sentinel commit:' failure in events.jsonl"

python3 - "$EV" > "$OUT/subjects.txt" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    e = r.get("Event") or {}
    k = e.get("Kind")
    f = e.get("Fields") or {}
    if k == "commit-subject":
        print("subject", e.get("Step"), f.get("attempt"), f.get("subject"), sep="\t")
    elif k == "commit-intent":
        print("intent", e.get("Step"), f.get("attempt"), f.get("message"), sep="\t")
    elif k == "merge-intent":
        print("merge", e.get("Step"), "-", f.get("message"), sep="\t")
EOF
cat "$OUT/subjects.txt"
last() { awk -F'\t' -v t="$1" -v s="$2" '$1 == t && $2 == s { v = $4 } END { print v }' "$OUT/subjects.txt"; }
for k in plan implement; do
  sub=$(last subject "$k"); intent=$(last intent "$k")
  [ -n "$sub" ] && ok "commit-subject event for $k: $sub" || fail "no commit-subject event for $k"
  [ -n "$sub" ] && [ "$sub" = "$intent" ] && ok "$k commit-intent message equals its subject" || fail "$k subject '$sub' vs commit-intent '$intent'"
  subject_ok "$sub" && ok "$k subject is a conventional subject without phase/r-loop:" || fail "$k subject malformed: '$sub'"
done
impl=$(last subject implement); merge=$(last merge land)
[ -n "$merge" ] && [ "$merge" = "$impl" ] && ok "merge-intent message equals the implement subject" || fail "merge-intent '$merge' vs implement '$impl'"

git log --format='%H %P | %s' --all -n 20 > "$OUT/gitlog-all.txt"
git log --first-parent --format=%s "$BASE" > "$OUT/gitlog-base.txt"
MERGE=$(git rev-list --first-parent --min-parents=2 -n 1 "$BASE")
msub=$(git log -1 --format=%s "$MERGE" 2>/dev/null)
[ -n "$MERGE" ] && [ "$msub" = "$impl" ] && ok "--no-ff merge commit subject equals the implement subject: $msub" || fail "merge commit '$msub' ($MERGE) vs implement '$impl'"
git log --format=%s "$MERGE^1..$MERGE^2" > "$OUT/gitlog-branch.txt" 2>/dev/null
git log --first-parent --format=%s "$MERGE..$BASE" > "$OUT/gitlog-after.txt"
bad=""
while IFS= read -r s; do subject_ok "$s" || bad="$bad[$s]"; done < "$OUT/gitlog-branch.txt"
while IFS= read -r s; do subject_ok "$s" || bad="$bad[$s]"; done < "$OUT/gitlog-after.txt"
subject_ok "$msub" || bad="$bad[$msub]"
[ "$(wc -l < "$OUT/gitlog-branch.txt" | tr -d ' ')" -ge 2 ] && [ -z "$bad" ] \
  && ok "every driver commit is conventional: $(cat "$OUT/gitlog-branch.txt" "$OUT/gitlog-after.txt" | tr '\n' ';')" || fail "bad or missing driver commit subjects: $bad"
grep -Eq 'r-loop: phase|^phase [0-9]+:' "$OUT/gitlog-all.txt" && fail "old-style subject in history: $(grep -E 'r-loop: phase|phase [0-9]+:' "$OUT/gitlog-all.txt")" || ok "no old-style r-loop:/phase N: subject in history"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "todo-tiny.md not ticked"
grep -q 'Naming the commit' "$ROOT/internal/prompts/render.go" && grep -q '{{template "commit"' "$ROOT/internal/prompts/templates/implement.md" \
  && ok "implement prompt includes the Naming the commit partial" || fail "implement template lacks the commit partial"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
case "$final" in
  *finished*) [ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))" ;;
  *) grep -q 'exited.* alt=0' "$OUT/tui-status.txt" && ok "q quits a run that did not finish, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")" ;;
esac
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
