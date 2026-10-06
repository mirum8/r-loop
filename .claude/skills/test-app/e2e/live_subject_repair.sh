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
LONG='feat(calc): add a subtract function that takes two integers and returns their difference, with table tests for edge cases'
[ "$(printf '%s' "$LONG" | wc -m | tr -d ' ')" -gt 100 ] || { echo "FAIL fixture subject is not over 100 chars"; exit 1; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"
BASE=$(git rev-parse --abbrev-ref HEAD)

python3 - docs/plan/todo-tiny.md "$LONG" <<'EOF'
import sys
p, long = sys.argv[1], sys.argv[2]
t = open(p).read()
note = ("When you write the ok sentinel for the implement step, set `commit` to exactly this subject "
        "(do not shorten or reword it; the maintainer is testing the driver's handling of it): `" + long + "`\n")
t = t.replace("**Done when:**", note + "**Done when:**", 1)
open(p, "w").write(t)
EOF
git add docs/plan/todo-tiny.md && git commit -q -m "docs(plan): pin the implement commit subject" || { echo "FAIL fixture commit"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "FAIL sandbox tree not clean"; exit 1; }

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-subjfix}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
cp "$("$TUI" capture "$H")" "$OUT/frame-start.txt"

start=$(date +%s)
final=""
n=0
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  if ls .r-loop/runs/*/events.jsonl > /dev/null 2>&1 && grep -q '"repair":' .r-loop/runs/*/events.jsonl && [ ! -f "$OUT/frame-warning.txt" ]; then
    sleep 2; cp "$("$TUI" capture "$H")" "$OUT/frame-warning.txt"; "$TUI" capture "$H" --ansi > /dev/null
  fi
  n=$((n + 1))
  sleep 15
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

RUN=$(dirname "$(ls .r-loop/runs/*/events.jsonl | head -1)")
EV="$RUN/events.jsonl"
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
find "$RUN" -name '*.sentinel' -o -name '*.json' -path '*phase-*' | while read -r f; do echo "== $f"; cat "$f"; echo; done > "$OUT/sentinels.txt" 2>/dev/null

python3 - "$EV" > "$OUT/subjects.txt" <<'EOF'
import json, sys
for line in open(sys.argv[1]):
    r = json.loads(line)
    e = r.get("Event") or {}
    k = e.get("Kind")
    f = e.get("Fields") or {}
    if k in ("commit-subject", "commit-intent", "merge-intent", "warning", "blocker", "phase-blocked", "step-failed"):
        print(json.dumps({"kind": k, "step": e.get("Step"), "fields": f}))
EOF
cat "$OUT/subjects.txt"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
python3 - "$EV" "$LONG" "$OUT/subjects.txt" <<'EOF' || fails=$((fails + $?))
import json, sys, re
ev, long, _ = sys.argv[1], sys.argv[2], sys.argv[3]
recs = [json.loads(l) for l in open(ev)]
evs = [(r.get("Event") or {}) for r in recs]
raw = open(ev).read()
fails = 0
def ok(m): print("OK  ", m)
def fail(m):
    global fails; fails += 1; print("FAIL", m)
cs = [e for e in evs if e.get("Kind") == "commit-subject" and e.get("Step") == "implement"]
if not cs: fail("no commit-subject event for implement")
else:
    f = cs[-1].get("Fields") or {}
    sub, orig, rep = f.get("subject", ""), f.get("original"), f.get("repair", "")
    if orig is None:
        print("NOT EXERCISED: implement subject not repaired; subject =", repr(sub))
        fails += 1
    else:
        ok(f"original == long subject") if orig == long else fail(f"original {orig!r} != long")
        ok(f"subject <=100 ({len(sub)}): {sub!r}") if len(sub) <= 100 else fail(f"subject too long {len(sub)}")
        ok("subject starts feat(calc): ") if sub.startswith("feat(calc): ") else fail(f"subject prefix {sub!r}")
        ok("subject is a word-boundary prefix of the original") if long.startswith(sub) and long[len(sub)] in " ," and not sub.endswith((" ", ",")) else fail(f"not a word-boundary cut: {sub!r}")
        ok(f"repair non-empty: {rep!r}") if rep else fail("repair empty")
ws = [e for e in evs if e.get("Kind") == "warning" and "commit subject repaired" in json.dumps(e)]
ok(f"warning event: {json.dumps(ws[0].get('Fields'))}") if ws else fail("no warning event with 'commit subject repaired'")
bl = [e for e in evs if e.get("Kind") in ("blocker", "phase-blocked")]
ok("no blocker/phase-blocked events") if not bl else fail(f"blocker events: {bl}")
ok("no attempt 2 (-a2) spawned") if "-a2" not in raw else fail("an -a2 attempt appears in events")
fl = [e for e in evs if e.get("Step") == "implement" and "fail" in (e.get("Kind") or "")]
ok("implement did not fail") if not fl else fail(f"implement failure events: {fl}")
pl = [e for e in evs if e.get("Kind") == "commit-subject" and e.get("Step") == "plan"]
if pl:
    f = pl[-1].get("Fields") or {}
    ok(f"plan subject unchanged, no original/repair: {f.get('subject')!r}") if "original" not in f and "repair" not in f else fail(f"plan subject repaired: {f}")
    pw = [e for e in evs if e.get("Kind") == "warning" and e.get("Step") == "plan" and "commit subject repaired" in json.dumps(e)]
    ok("no repair warning for plan") if not pw else fail(f"plan repair warning: {pw}")
else: fail("no commit-subject event for plan")
sys.exit(fails)
EOF

impl=$(python3 -c 'import json,sys
v=""
for l in open(sys.argv[1]):
    e=json.loads(l).get("Event") or {}
    if e.get("Kind")=="commit-subject" and e.get("Step")=="implement": v=(e.get("Fields") or {}).get("subject","")
print(v)' "$EV")
git log --format='%H %P | %s' --all -n 20 > "$OUT/gitlog-all.txt"
MERGE=$(git rev-list --first-parent --min-parents=2 -n 1 "$BASE")
msub=$(git log -1 --format=%s "$MERGE" 2>/dev/null)
[ -n "$MERGE" ] && [ "$msub" = "$impl" ] && ok "merge commit subject is the repaired subject: $msub" || fail "merge commit '$msub' vs implement '$impl'"
git log --format=%s "$MERGE^1..$MERGE^2" > "$OUT/gitlog-branch.txt" 2>/dev/null
grep -qxF "$impl" "$OUT/gitlog-branch.txt" && ok "step commit carries the repaired subject" || fail "no step commit with '$impl': $(tr '\n' ';' < "$OUT/gitlog-branch.txt")"
grep -qF "$LONG" "$OUT/gitlog-all.txt" && fail "the long subject reached git history" || ok "the long subject is not in git history"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "todo-tiny.md not ticked"
for fr in "$OUT"/frame-*.txt; do grep -q 'commit subject repaired' "$fr" && echo "     frame with warning: $fr"; done
cat "$OUT"/frame-*.txt | grep -q 'commit subject repaired' && ok "a TUI frame shows the repair warning" || fail "no captured frame shows 'commit subject repaired'"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
case "$final" in
  *finished*) [ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))" ;;
  *) fail "run not finished; after q: $(cat "$OUT/tui-status.txt")" ;;
esac
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
