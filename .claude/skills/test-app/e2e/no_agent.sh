#!/bin/sh
set -u
ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
fails=0
T=$(mktemp -d)
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
fresh() { S=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$S"; }
run()  { "$BIN" "$@" > "$T/out" 2> "$T/err"; rc=$?; }

fresh
run --version
[ "$rc" = 0 ] && grep -q '^r-loop ' "$T/out" && ok "--version" || fail "--version rc=$rc"

run --bogus
[ "$rc" = 2 ] && [ ! -s "$T/out" ] && ok "unknown flag exits 2" || fail "unknown flag rc=$rc"

run docs/plan/todo.md --dry-run --plain
if [ "$rc" = 0 ] && [ "$(grep -c '^phase [0-9]' "$T/out")" = 3 ] && grep -q 'Custom delimiter syntax.*blocks phase 3' "$T/out"; then
  ok "dry-run todo.md: 3 phases, Resolve first entry named"
else fail "dry-run todo.md rc=$rc"; fi
[ ! -d .r-loop/runs ] && ok "dry-run created no run" || fail "dry-run created .r-loop/runs"

run issues.md --dry-run --plain
[ "$rc" = 0 ] && [ "$(grep -c '^phase ' "$T/out")" = 1 ] && grep -q '^phase 1 .*1, 2' "$T/out" && ok "dry-run issues.md: only #1 open" || fail "dry-run issues.md rc=$rc"

table_ok() { awk -F'|' '/^\|/ { n = NF; if (w == "") w = n; else if (n != w) bad = 1; rows++ } END { exit !(rows >= 3 && !bad) }' "$T/out"; }

run docs/plan/todo.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Plan: .* — 3 phases' "$T/out" \
  && grep -qx '| Phase | Title | Risk | Milestone | Wave | Files | Done when |' "$T/out" \
  && ! grep -q 'Verified' "$T/out" && table_ok \
  && grep -q '^| 1 | .* | 0 | calc.go' "$T/out" && grep -q '^| 2 | .* | 0 | multiply.go' "$T/out" \
  && ! grep -q '^| 3 |' "$T/out" \
  && grep -q '^Plan check: no notes' "$T/out" \
  && grep -q '^Resolve first: Custom delimiter syntax → phases 3' "$T/out" \
  && grep -q '^2 phases: 4 step sessions' "$T/out" \
  && grep -qx 'verification: not run (--dry-run starts no sessions)' "$T/out"; then
  ok "dry-run todo.md: triage table, waves, plan check, Resolve first, cost"
else fail "dry-run todo.md triage table rc=$rc"; fi

sed -i '' 's/^- \[ \] \*\*Custom delimiter syntax\*\*/- [x] **Custom delimiter syntax**/' docs/plan/todo.md && git commit -qam tick
run docs/plan/todo.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^| 3 | .* | 1 | calc.go' "$T/out" && grep -q '^Resolve first: none outstanding' "$T/out" && table_ok \
  && ok "dry-run todo.md, entry ticked: phase 3 in wave 1" || fail "phase 3 wave rc=$rc"
git reset -q --hard HEAD~1

run docs/plan/todo-tiny.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Plan: .* — 1 phase' "$T/out" && table_ok \
  && grep -q '^| 1 | Subtract | .* | 0 | subtract.go' "$T/out" && grep -q '^Plan check: no notes' "$T/out" \
  && grep -q '^Resolve first: none outstanding' "$T/out" && grep -q '^1 phase: 2 step sessions' "$T/out" \
  && grep -qx 'verification: not run (--dry-run starts no sessions)' "$T/out"; then
  ok "dry-run todo-tiny.md: triage table"
else fail "dry-run todo-tiny.md triage table rc=$rc"; fi

run issues.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Backlog: .*issues.md (1 item)' "$T/out" && grep -qx '| Item | Title |' "$T/out" && table_ok \
  && grep -q '^| 1 | ' "$T/out" && ! grep -q '^| [23] |' "$T/out" \
  && grep -qx 'verification: not run (--dry-run starts no sessions)' "$T/out"; then
  ok "dry-run issues.md: backlog table with open items only"
else fail "dry-run issues.md backlog table rc=$rc"; fi

printf '\n### Phase 2 — Empty\n**Implements:** Nothing\n**Depends on:** Phase 1\n**Files:** `empty.go` (new)\n**Done when:** `go test ./...` is green.\n' >> docs/plan/todo-tiny.md
git commit -qam empty
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'Phase 2 — Empty.*no checklist' "$T/err" && ok "plan check: a phase with no checklist stops with exit 2" || fail "plan-check stop rc=$rc"
git reset -q --hard HEAD~1

printf '\n### Phase 2 — Notes\n**Depends on:** Phase 1\n**Files:** `notes.go` (new)\n- [ ] something\n**Done when:** the tests are green.\n' >> docs/plan/todo-tiny.md
git commit -qam notes
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^Plan check: 2 notes' "$T/out" && grep -q "^- Phase 2 — Notes: 'Done when' names no runnable command" "$T/out" \
  && grep -q "^- Phase 2 — Notes: no 'Implements' line" "$T/out" \
  && ok "plan check: no runnable Done when and no Implements are notes, exit 0" || fail "plan-check notes rc=$rc"
git reset -q --hard HEAD~1

run docs/plan/todo-tiny.md --yes --dry-run --plain
[ "$rc" = 0 ] && ok "--yes parses" || fail "--yes rc=$rc"
run resume --bogus
[ "$rc" = 2 ] && grep -q 'usage: r-loop resume .*--yes' "$T/err" && ok "resume usage names --yes" || fail "resume usage rc=$rc"

run docs/plan/todo.md --phases 9 --dry-run --plain
[ "$rc" = 2 ] && grep -q 'phase 9' "$T/err" && ok "--phases 9 exits 2" || fail "--phases 9 rc=$rc"

run docs/plan/todo.md --from 2 --dry-run --plain
[ "$rc" = 0 ] && ! grep -q '^phase 1 ' "$T/out" && grep -q '^phase 2 ' "$T/out" && ok "--from 2 skips phase 1" || fail "--from 2 rc=$rc"

run status --plain
[ "$rc" = 0 ] && grep -q 'no run' "$T/out" && ok "status with no run" || fail "status rc=$rc"

NO_COLOR=1 "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2>&1
! grep -q "$(printf '\033')" "$T/out" && ok "NO_COLOR: no escapes" || fail "NO_COLOR: escapes present"

printf 'pipeline: [plan, implement]\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'flow style' "$T/err" && ok "flow-style config exits 2" || fail "flow-style rc=$rc"
git checkout -q .r-loop/config.yaml

printf 'bogusKey: 1\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'unknown key "bogusKey"' "$T/err" && ok "unknown config key exits 2" || fail "unknown key rc=$rc"
git checkout -q .r-loop/config.yaml

watchdog_keys() {
  perl -0pi -e "s/  unblockTimeout: 15m\n/  unblockTimeout: 15m\n$1/" .r-loop/config.yaml
  run docs/plan/todo-tiny.md --dry-run --plain
  git checkout -q .r-loop/config.yaml
}
watchdog_keys '  blockerTimeout: 5m\n'
[ "$rc" = 0 ] && ok "watchdog.blockerTimeout: 5m accepted" || fail "blockerTimeout 5m rc=$rc: $(head -2 "$T/err")"
watchdog_keys '  remedyWindow: 5m\n'
[ "$rc" = 0 ] && ok "watchdog.remedyWindow alone accepted as alias" || fail "remedyWindow alone rc=$rc: $(head -2 "$T/err")"
watchdog_keys '  blockerTimeout: 5m\n  remedyWindow: 5m\n'
[ "$rc" = 2 ] && ok "blockerTimeout and remedyWindow together exit 2: $(head -1 "$T/err")" || fail "both keys rc=$rc"
watchdog_keys '  blockerTimeout: 0s\n'
[ "$rc" = 2 ] && ok "blockerTimeout: 0s exits 2: $(head -1 "$T/err")" || fail "blockerTimeout 0s rc=$rc"

run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^  reviewer codex ' "$T/out" && grep -qx 'prompt review-plan: embedded' "$T/out" \
  && ok "dry-run with a codex plan reviewer lists 'prompt review-plan: embedded'" \
  || fail "dry-run banner: no codex plan reviewer or no 'prompt review-plan' line rc=$rc"

CODEX_NOREVIEW='providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    askFlag: "-c mcp_servers.r-loop.url={url}"\n'
printf "$CODEX_NOREVIEW" >> .r-loop/config.yaml
perl -0pi -e 's/(  implement:\n(?:    [^\n]*\n)*?    reviewers:\n)      - provider: codex\n/$1      - provider: claude\n/' .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && ok "a codex with no review command passes as a plan-only reviewer" || fail "plan-only codex without review rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml
printf "$CODEX_NOREVIEW" >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'steps.implement.reviewers: provider codex has no review command' "$T/err" \
  && ok "the same codex as an implement reviewer exits 2" || fail "implement codex without review rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

printf 'providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    review: "/review x"\n    reviewStart: ">> s"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'reviewDone is required with reviewStart' "$T/err" && ok "reviewStart without reviewDone exits 2" || fail "reviewStart alone rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

echo "// dirty" >> calc.go
run docs/plan/todo-tiny.md --plain
if [ "$rc" = 4 ] && { grep -q 'calc.go' "$T/err" || grep -q 'herdr server unreachable' "$T/err"; } && [ ! -d .r-loop/runs ]; then
  ok "dirty tree refused before any run ($(head -c 60 "$T/err"))"
else fail "dirty tree rc=$rc"; fi
git checkout -q calc.go

H=$(mktemp -d)
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc1=$?
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc2=$?
[ "$rc1" = 0 ] && [ -s "$H/.config/r-loop/config.yaml" ] && [ "$rc2" = 2 ] && ok "--create-config writes once, then refuses" || fail "--create-config rc=$rc1/$rc2"
rm -rf "$H"

for arg in '../../etc/passwd.md' "x; touch $T/pwned.md" "\`touch $T/pwned\`.md" "\$(touch $T/pwned).md"; do
  run "$arg" --dry-run --plain
  if [ "$rc" = 2 ] && [ ! -s "$T/out" ] && [ "$(wc -l < "$T/err")" -le 1 ] && ! ls "$T"/pwned* >/dev/null 2>&1; then
    ok "hostile .md path refused cleanly: $arg"
  else fail "hostile .md path rc=$rc: $arg"; fi
done

cd "$ROOT"
go test ./internal/face/... > /dev/null 2>&1 && ok "golden harness go test ./internal/face/..." || fail "go test ./internal/face/..."

rm -rf "$S" "$T"
echo "failures: $fails"
[ "$fails" = 0 ]
