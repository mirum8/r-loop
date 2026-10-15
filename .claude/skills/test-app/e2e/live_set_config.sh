#!/bin/sh
# usage: live_set_config.sh
#   ADR-97 on a held land blocker (gate.sh red on the primary checkout until a marker outside the repo exists,
#   land.fixRounds 0, unattended). On the first land blocker the script calls the watchdog MCP itself:
#   tools/list has set_config and no apply_fix; set_config steps.implement.timeout 90m is authorised, written,
#   committed alone as chore(r-loop): set steps.implement.timeout, with a config-set event; watchdog.*, an unknown
#   key and a bad value are refused with the file and HEAD unchanged; resolve_blocker block (no maintainer_said) is
#   refused while retries are left; skip is not an action. Then a plan edit plus a stray file: continue is refused by
#   a new land blocker naming the stray path; with the stray gone and the marker made, continue commits the plan as
#   docs(plan): correct phase 1 and the phase lands.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
. "$(dirname "$0")/own_workspace.sh"

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
E2E="$ROOT/.claude/skills/test-app/e2e"
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-3000}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-set-config-$RAND"
MARKER="$S.green"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

cat > gate.sh <<EOF
#!/bin/sh
go test ./... || exit 1
case "\$PWD" in */.r-loop/wt/*) exit 0 ;; esac
[ -f "$MARKER" ] || { echo "gate: red on the primary checkout"; exit 1; }
EOF
chmod +x gate.sh
python3 - <<'EOF'
p = ".r-loop/config.yaml"
s = open(p).read()
old = "land:\n  gateTimeout: 5m\n"
assert old in s
s = s.replace(old, "land:\n  fixRounds: 0\n  gateTimeout: 5m\n")
impl = "  implement:\n    provider: claude\n    model: sonnet\n    effort: low\n    timeout: 30m\n    fallback:\n      provider: codex\n      model: gpt-5.6-sol\n      effort: medium\n    reviewers:\n"
assert impl + "      - provider: codex\n" in s
s = s.replace(impl + "      - provider: codex\n        model: gpt-5.6-sol\n        effort: medium\n", impl + "      - provider: claude\n        model: sonnet\n        effort: low\n")
s = s.replace("  unblockTimeout: 15m\n", "  unblockTimeout: 15m\n  blockerTimeout: 20m\n  maxRestarts: 5\n")
assert "    timeout: 30m\n" in s
open(p, "w").write(s)
t = "docs/plan/todo-tiny.md"
d = open(t).read()
old = "**Done when:** `go test ./...` is green."
assert old in d
open(t, "w").write(d.replace(old, "**Done when:** `./gate.sh` passes."))
EOF
cp .r-loop/config.yaml "$OUT/config.yaml"
git add gate.sh
git -c user.name=sandbox -c user.email=sandbox@localhost commit -qam "live_set_config setup"
./gate.sh > /dev/null 2>&1 && { echo "FAIL gate.sh is green on the primary checkout before the marker"; exit 1; }
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 || { echo "FAIL dry-run after setup"; cat "$OUT/dryrun.out"; exit 1; }
ok "setup: gate.sh red on the primary checkout, dry-run valid"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-setconfig}-sc"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4800}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1; rm -f "$MARKER"' EXIT
echo "handle  $H"

has() { [ -n "$EV" ] && grep -q "\"Kind\":\"$1\"" "$EV"; }
wd() { "$E2E/wd_call.sh" "$WDURL" "$@"; }
land_ids() { grep '"Kind":"blocked-on"' "$EV" 2>/dev/null | grep '"source":"land"' | sed 's/.*"id":"\(b[0-9]*\)".*/\1/'; }
snap() { git rev-parse HEAD > "$OUT/$1.head"; shasum .r-loop/config.yaml > "$OUT/$1.sum"; }

(
  stage=0
  WDURL=""
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    if [ -z "$WDURL" ] && has watchdog-placed; then
      WDURL=$("$E2E/wd_url.sh" "$S" 2>> "$OUT/wdurl.err") && echo "$WDURL" > "$OUT/wdurl.txt"
    fi
    if [ $stage = 0 ] && [ -n "$WDURL" ] && [ -n "$(land_ids)" ]; then
      stage=1
      B1=$(land_ids | head -1)
      echo "$B1" > "$OUT/b1.txt"
      date +%T > "$OUT/b1-at.txt"
      wd tools/list > "$OUT/tools.txt" 2>&1
      wd apply_fix '{}' > "$OUT/call-apply_fix.json" 2>&1
      snap pre-set
      wd set_config '{"key":"steps.implement.timeout","value":"90m","why":"live_set_config check"}' > "$OUT/call-set-ok.json" 2>&1
      snap post-set
      git log -1 --format='%s' > "$OUT/set-subject.txt"
      git show --name-only --format= HEAD > "$OUT/set-files.txt"
      git diff HEAD~1 HEAD -- .r-loop/config.yaml > "$OUT/set-diff.txt"
      wd set_config '{"key":"watchdog.maxRestarts","value":"9","why":"live_set_config check"}' > "$OUT/call-set-watchdog.json" 2>&1
      wd set_config '{"key":"unattended.allow.0","value":"deps","why":"live_set_config check"}' > "$OUT/call-set-unattended.json" 2>&1
      wd set_config '{"key":"steps.implement.bogus","value":"1","why":"live_set_config check"}' > "$OUT/call-set-unknown.json" 2>&1
      wd set_config '{"key":"steps.implement.timeout","value":"soon","why":"live_set_config check"}' > "$OUT/call-set-bad.json" 2>&1
      snap post-refused
      git status --porcelain > "$OUT/porcelain-refused.txt"
      wd resolve_blocker '{"id":"'"$B1"'","action":"block"}' > "$OUT/call-block.json" 2>&1
      wd resolve_blocker '{"id":"'"$B1"'","action":"skip"}' > "$OUT/call-skip.json" 2>&1
      wd resolve_blocker '{"id":"'"$B1"'","action":"fix"}' > "$OUT/call-fix.json" 2>&1
      sed -i '' 's/^\*\*Implements:\*\* Subtract numbers$/**Implements:** Subtract numbers (corrected by live_set_config)/' docs/plan/todo-tiny.md
      echo stray > stray.txt
      git status --porcelain > "$OUT/porcelain-dirty-continue.txt"
      wd resolve_blocker '{"id":"'"$B1"'","action":"continue"}' > "$OUT/call-continue-dirty.json" 2>&1
      cp "$("$TUI" capture "$H")" "$OUT/frames/b1-120x40.txt"
    fi
    if [ $stage = 1 ] && [ "$(land_ids | wc -l | tr -d ' ')" -ge 2 ]; then
      stage=2
      B2=$(land_ids | sed -n 2p)
      echo "$B2" > "$OUT/b2.txt"
      grep '"Kind":"blocked-on"' "$EV" | grep "\"id\":\"$B2\"" > "$OUT/b2-event.json"
      rm -f stray.txt
      touch "$MARKER"
      git status --porcelain > "$OUT/porcelain-clean-continue.txt"
      wd resolve_blocker '{"id":"'"$B2"'","action":"continue"}' > "$OUT/call-continue-clean.json" 2>&1
    fi
    sleep 0.2
  done
) &
watcher=$!

start=$(date +%s)
n=0
final=""
while :; do
  n=$((n + 1))
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
wait "$watcher" 2>/dev/null
cp "$("$TUI" capture "$H")" "$OUT/frames/end-120x40.txt"
"$BIN" status --plain > "$OUT/status.txt" 2>&1

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
git status --porcelain > "$OUT/porcelain-end.txt"
git log --format='%h %s' > "$OUT/gitlog.txt"

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

ev() { grep "\"Kind\":\"$1\"" "$EV"; }
B1=$(cat "$OUT/b1.txt" 2>/dev/null)
B2=$(cat "$OUT/b2.txt" 2>/dev/null)
[ -n "$B1" ] && ok "land blocker $B1 held at $(cat "$OUT/b1-at.txt")" || fail "no land blocker: $(ev blocked-on | grep -o '"Fields":{[^}]*}')"

grep -qx set_config "$OUT/tools.txt" && ok "tools/list has set_config ($(wc -l < "$OUT/tools.txt" | tr -d ' ') tools)" || fail "tools/list: $(tr '\n' ' ' < "$OUT/tools.txt")"
grep -qx apply_fix "$OUT/tools.txt" && fail "tools/list still has apply_fix" || ok "tools/list has no apply_fix"
grep -qi 'apply_fix' "$OUT/call-apply_fix.json" && grep -qi -e 'unknown tool' -e 'not found' -e '"code"' "$OUT/call-apply_fix.json" && ok "apply_fix call is an error: $(cat "$OUT/call-apply_fix.json")" || fail "apply_fix call: $(cat "$OUT/call-apply_fix.json" 2>/dev/null)"

grep -q '"decision":"authorised"' "$OUT/call-set-ok.json" 2>/dev/null && ok "set_config steps.implement.timeout 90m authorised: $(cat "$OUT/call-set-ok.json")" || fail "set_config steps.implement.timeout: $(cat "$OUT/call-set-ok.json" 2>/dev/null)"
[ "$(cat "$OUT/set-subject.txt" 2>/dev/null)" = "chore(r-loop): set steps.implement.timeout" ] && ok "commit subject: $(cat "$OUT/set-subject.txt")" || fail "commit subject: $(cat "$OUT/set-subject.txt" 2>/dev/null)"
[ "$(tr -d ' \n' < "$OUT/set-files.txt" 2>/dev/null)" = ".r-loop/config.yaml" ] && ok "commit touches only .r-loop/config.yaml" || fail "commit files: $(tr '\n' ' ' < "$OUT/set-files.txt" 2>/dev/null)"
grep -q '^+.*timeout: 90m' "$OUT/set-diff.txt" && grep -q '^-.*timeout: 30m' "$OUT/set-diff.txt" && ok "config diff: $(grep '^[-+] ' "$OUT/set-diff.txt" | tr '\n' ' ')" || fail "config diff: $(cat "$OUT/set-diff.txt" 2>/dev/null)"
! cmp -s "$OUT/pre-set.head" "$OUT/post-set.head" && ok "HEAD moved on set_config" || fail "HEAD unchanged after set_config"
grep '"Kind":"config-set"' "$EV" | grep -q '"key":"steps.implement.timeout"' && ok "config-set event: $(ev config-set | head -1 | grep -o '"Fields":{[^}]*}')" || fail "no config-set event for steps.implement.timeout"
ev config-set-failed > /dev/null && echo "info: config-set-failed events: $(ev config-set-failed | grep -o '"Fields":{[^}]*}' | tr '\n' ' ')"

for c in watchdog unattended unknown bad; do
  r=$(cat "$OUT/call-set-$c.json" 2>/dev/null)
  case "$r" in *'"decision":"refused"'*) ok "set_config $c refused: $r" ;; *) fail "set_config $c: $r" ;; esac
done
grep -q 'watchdog.\* is not yours to set' "$OUT/call-set-watchdog.json" && ok "watchdog.* refusal names the lock" || fail "watchdog.* refusal reason"
cmp -s "$OUT/post-set.head" "$OUT/post-refused.head" && cmp -s "$OUT/post-set.sum" "$OUT/post-refused.sum" && ok "refusals left HEAD and .r-loop/config.yaml unchanged" || fail "refusals changed HEAD or the config"
[ ! -s "$OUT/porcelain-refused.txt" ] && ok "tree clean after the refusals" || fail "tree dirty after the refusals: $(cat "$OUT/porcelain-refused.txt")"

grep -q '"decision":"refused"' "$OUT/call-block.json" && grep -Eq '[0-9]+ retries left for phase-1/land: fix the cause and retry' "$OUT/call-block.json" && ok "block refused: $(cat "$OUT/call-block.json")" || fail "block call: $(cat "$OUT/call-block.json" 2>/dev/null)"
grep -q '"decision":"refused"' "$OUT/call-skip.json" && grep -q "skip is not an action for blocker $B1 (land): retry, block, stop or continue" "$OUT/call-skip.json" && ok "skip refused: $(cat "$OUT/call-skip.json")" || fail "skip call: $(cat "$OUT/call-skip.json" 2>/dev/null)"
grep -q "fix is not an action" "$OUT/call-fix.json" && ok "fix refused: $(cat "$OUT/call-fix.json")" || fail "fix call: $(cat "$OUT/call-fix.json" 2>/dev/null)"

grep -q '"decision":"authorised"' "$OUT/call-continue-dirty.json" && ok "continue with a stray file accepted by the tool: $(cat "$OUT/call-continue-dirty.json")" || fail "continue (dirty): $(cat "$OUT/call-continue-dirty.json" 2>/dev/null)"
[ -n "$B2" ] && grep -q 'primary tree has changes other than the plan: stray.txt' "$OUT/b2-event.json" && ok "dirty continue raised land blocker $B2: $(grep -o '"reason":"[^"]*"' "$OUT/b2-event.json")" || fail "no land blocker naming stray.txt after the dirty continue: $(ev blocked-on | grep -o '"reason":"[^"]\{0,160\}' | tr '\n' ' ')"
grep -q '"decision":"authorised"' "$OUT/call-continue-clean.json" 2>/dev/null && ok "continue (clean) authorised: $(cat "$OUT/call-continue-clean.json")" || fail "continue (clean): $(cat "$OUT/call-continue-clean.json" 2>/dev/null)"
pc=$(grep -c ' docs(plan): correct phase 1$' "$OUT/gitlog.txt")
PC=$(git log --format='%H' --grep='^docs(plan): correct phase 1$')
[ "$pc" = 1 ] && [ "$(git show --name-only --format= "$PC" | tr -d '\n')" = docs/plan/todo-tiny.md ] && ok "one plan commit, todo-tiny.md alone: $(git log -1 --format='%h %s' "$PC")" || fail "plan commits: $pc ($(grep 'docs(plan)' "$OUT/gitlog.txt" | tr '\n' ' '))"
ev plan-corrected > /dev/null && ok "plan-corrected event: $(ev plan-corrected | head -1 | grep -o '"Fields":{[^}]*}')" || fail "no plan-corrected event"
git log --format=%s | grep -q '^chore(r-loop): set watchdog' && fail "a watchdog.* commit exists" || ok "no watchdog.* commit"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
ev landed > /dev/null && ok "landed: $(ev landed | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no landed"
grep -q 'corrected by live_set_config' docs/plan/todo-tiny.md && grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "todo-tiny.md ticked and keeps the correction" || fail "todo-tiny.md: $(grep -e Implements -e '^- \[' docs/plan/todo-tiny.md | tr '\n' ' ')"
grep -q 'timeout: 90m' .r-loop/config.yaml && ok "config keeps timeout: 90m" || fail "config lost the set value"
[ ! -s "$OUT/porcelain-end.txt" ] && ok "primary tree clean" || fail "primary tree dirty: $(cat "$OUT/porcelain-end.txt")"
ev blocker-resolved | grep -o '"Fields":{[^}]*}' > "$OUT/resolved.txt"
echo "info: blocker-resolved: $(tr '\n' ' ' < "$OUT/resolved.txt")"
[ "$code" = 0 ] && ok "exit 0" || fail "exit $code"
grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited --status 0 > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "stop --expect-exited --status 0" || fail "stop --expect-exited rc=$src"
trap - EXIT
rm -f "$MARKER"

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
