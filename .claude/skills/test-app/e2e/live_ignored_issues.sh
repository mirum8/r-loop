#!/bin/sh
# Live: an untracked, gitignored issues file is driver-local — ticked on disk, never committed.
# Usage (from the repo root, inside a herdr pane): sh .claude/skills/test-app/e2e/live_ignored_issues.sh
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2700}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
gc()   { git -c user.name=sandbox -c user.email=sandbox@localhost "$@"; }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh" "$HOME/r-loop-test-ignissues-$$-$(date +%s)") || exit 1
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
  gc commit -qam "migrate config"
fi

mkdir issues && git mv issues.md issues/issues.md
echo "/issues/" >> .gitignore
git rm -q --cached issues/issues.md
git add .gitignore
gc commit -qm "ignore issues"
[ -z "$(git ls-files issues/)" ] || { echo "FAIL setup: issues/ still tracked"; exit 1; }
git check-ignore -q issues/issues.md || { echo "FAIL setup: issues/issues.md not ignored"; exit 1; }
[ -z "$(git status --porcelain)" ] || { echo "FAIL setup: tree not clean: $(git status --porcelain)"; exit 1; }
[ -f issues/issues.md ] || { echo "FAIL setup: issues/issues.md missing on disk"; exit 1; }
base=$(git rev-parse HEAD)

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-ignissues}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" issues/issues.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

start=$(date +%s)
last=$start
final=""
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  now=$(date +%s)
  [ $((now - start)) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
  if [ $((now - last)) -ge 300 ]; then
    cp "$("$TUI" capture "$H")" "$OUT/frame-$((now - start)).txt" 2>/dev/null
    last=$now
  fi
  sleep 15
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
cp "$EV" "$OUT/events.jsonl"
cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

bad=$(grep -hE 'pathspec|paths are ignored|landing refused' "$EV" "$OUT/status.txt" "$OUT/report.md" 2>/dev/null | head -3)
[ -z "$bad" ] && ok "no pathspec / ignored-path / landing-refused error" || fail "landing error: $bad"
grep -qiE 'land' "$EV" && ok "events.jsonl records a landing" || fail "no landing in events.jsonl"

git log --oneline "$base..HEAD" > "$OUT/newcommits.txt"
[ -s "$OUT/newcommits.txt" ] && ok "new landing commit: $(head -1 "$OUT/newcommits.txt")" || fail "no commit after the setup commit"
git show --name-only --format= --diff-merges=first-parent HEAD > "$OUT/landed-files.txt"
grep -q '\.go$' "$OUT/landed-files.txt" && ok "landing commit carries code: $(tr '\n' ' ' < "$OUT/landed-files.txt")" \
  || fail "landing commit has no code: $(tr '\n' ' ' < "$OUT/landed-files.txt")"
grep -q '^issues/' "$OUT/landed-files.txt" && fail "landing commit carries the ignored issues file" || ok "landing commit does not carry issues/issues.md"
[ -z "$(git ls-files issues/)" ] && ok "issues/ still untracked" || fail "issues/ became tracked: $(git ls-files issues/)"
git log --all --format= --name-only | grep -q '^issues/' && fail "issues/ appears in some commit" || ok "issues/ in no commit on any ref"
grep -q '^- \[x\] \[#1\]' issues/issues.md && ok "issues/issues.md ticked #1 on disk" || fail "#1 not ticked on disk: $(grep '#1' issues/issues.md)"
git status --porcelain > "$OUT/porcelain.txt"
[ -s "$OUT/porcelain.txt" ] && fail "tree not clean: $(tr '\n' ';' < "$OUT/porcelain.txt")" || ok "tree clean"

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" status "$H" > "$OUT/tui-status.txt"
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc ($(cat "$OUT/tui-status.txt"))"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
