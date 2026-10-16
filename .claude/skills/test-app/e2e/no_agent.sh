#!/bin/sh
set -u
ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
case "$BIN" in /*) ;; *) BIN="$PWD/$BIN" ;; esac
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
fails=0
T=$(mktemp -d)
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
mkdir -p "$T/herdr-up" "$T/herdr-down"
printf '#!/bin/sh\necho "{}"\n' > "$T/herdr-up/herdr"
printf '#!/bin/sh\necho "herdr: server not running" >&2\nexit 1\n' > "$T/herdr-down/herdr"
chmod +x "$T/herdr-up/herdr" "$T/herdr-down/herdr"
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

run docs/plan/todo-gate.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^  Done when: `go test ./...` is green and `calc` serves `GET /sum`' "$T/out" && ok "dry-run todo-gate.md: Done when shown whole" || fail "dry-run todo-gate.md rc=$rc: $(head -2 "$T/err")"

table_ok() { awk -F'|' '/^\|/ { n = NF; if (w == "") w = n; else if (n != w) bad = 1; rows++ } END { exit !(rows >= 3 && !bad) }' "$T/out"; }

block_ok() { grep -A3 "^Phase $1 · " "$T/out" | grep -q "^  M1 · wave $2\$" && grep -A3 "^Phase $1 · " "$T/out" | grep -q "^  Files: *$3"; }

run docs/plan/todo.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Plan: .* — 3 phases' "$T/out" \
  && ! grep -q 'Verified' "$T/out" \
  && block_ok 1 0 calc.go && block_ok 2 0 multiply.go \
  && ! grep -q '^Phase 3 · ' "$T/out" \
  && grep -q '^Plan check: no notes' "$T/out" \
  && grep -q '^Resolve first: Custom delimiter syntax → phase 3' "$T/out" \
  && grep -q '^2 phases: 4 step sessions' "$T/out" \
  && grep -qx 'verification: not run (--dry-run starts no sessions)' "$T/out"; then
  ok "dry-run todo.md: triage blocks, waves, plan check, Resolve first, cost"
else fail "dry-run todo.md triage blocks rc=$rc"; fi

sed -i '' 's/^- \[ \] \*\*Custom delimiter syntax\*\*/- [x] **Custom delimiter syntax**/' docs/plan/todo.md && git commit -qam tick
run docs/plan/todo.md --dry-run --plain
[ "$rc" = 0 ] && block_ok 3 1 calc.go && grep -q '^Resolve first: none outstanding' "$T/out" \
  && ok "dry-run todo.md, entry ticked: phase 3 in wave 1" || fail "phase 3 wave rc=$rc"
git reset -q --hard HEAD~1

run docs/plan/todo-tiny.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Plan: .* — 1 phase' "$T/out" \
  && grep -q '^Phase 1 · Subtract$' "$T/out" && block_ok 1 0 subtract.go && grep -q '^Plan check: no notes' "$T/out" \
  && grep -q '^Resolve first: none outstanding' "$T/out" && grep -q '^1 phase: 2 step sessions' "$T/out" \
  && grep -qx 'verification: not run (--dry-run starts no sessions)' "$T/out"; then
  ok "dry-run todo-tiny.md: triage block"
else fail "dry-run todo-tiny.md triage block rc=$rc"; fi

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

claude2() {
  perl -0pi -e 's/(  implement:\n    provider: )claude/${1}claude2/' .r-loop/config.yaml
  printf "providers:\n  claude2:\n    kind: claude\n    doneSignal: sentinel\n    ask: mcp\n$1" >> .r-loop/config.yaml
  run docs/plan/todo-tiny.md --dry-run --plain
  git checkout -q .r-loop/config.yaml
}
claude2 '    version:\n      - --version\n'
[ "$rc" = 2 ] && grep -q 'provider claude2: version must be a string' "$T/err" && [ ! -d .r-loop/runs ] \
  && ok "a provider version that is a list exits 2" || fail "version list rc=$rc: $(head -1 "$T/err")"
claude2 '    version: "--version"\n'
[ "$rc" = 0 ] && grep -q '^implement  claude2 ' "$T/out" && ok "a provider version string loads" || fail "version string rc=$rc: $(head -1 "$T/err")"

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
[ "$rc" = 2 ] && grep -q 'reviewStart is not a provider key' "$T/err" && ok "the retired reviewStart key exits 2" || fail "reviewStart rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

printf 'providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    reviewExec: "/review x"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'reviewExec is a command the driver runs' "$T/err" && ok "a slash reviewExec exits 2" || fail "slash reviewExec rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

printf 'providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    reviewDone: "x"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'reviewDone is not a provider key' "$T/err" && ok "the retired reviewDone key exits 2" || fail "reviewDone rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

printf 'providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    reviewExec: "codex review -o {output}"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'reviewExec must not contain {output}' "$T/err" && ok "a reviewExec with {output} exits 2" || fail "reviewExec {output} rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

printf 'providers:\n  codex:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    review: "/review"\n    reviewExec: "codex review --uncommitted"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'reviewExec and review are exclusive' "$T/err" && ok "review with reviewExec exits 2" || fail "review+reviewExec rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

sed -i.bak 's/      - provider: codex/      - provider: bare/' .r-loop/config.yaml && rm -f .r-loop/config.yaml.bak
printf 'providers:\n  bare:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && grep -q 'provider bare has no review command' "$T/err" && ok "a reviewer with no review command exits 2" || fail "no review command rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

sed -i.bak 's/      - provider: codex/      - provider: execonly/' .r-loop/config.yaml && rm -f .r-loop/config.yaml.bak
printf 'providers:\n  execonly:\n    kind: codex\n    doneSignal: sentinel\n    ask: mcp\n    reviewExec: "codex review --uncommitted {args}"\n' >> .r-loop/config.yaml
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && grep -q 'reviewer execonly' "$T/out" && ok "a reviewer with only reviewExec passes preflight" || fail "reviewExec-only reviewer rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

dirty_refused() {
  [ "$rc" = 4 ] && grep -q 'primary tree is not clean' "$T/err" && grep -q 'calc.go' "$T/err" \
    && [ -z "$(ls .r-loop/runs 2>/dev/null | grep -vx -e gate -e lock)" ]
}
echo "// dirty" >> calc.go
PATH="$T/herdr-up:$PATH" "$BIN" docs/plan/todo-tiny.md --plain > "$T/out" 2> "$T/err"; rc=$?
dirty_refused && ok "dirty tree refused before any run ($(head -c 80 "$T/err"))" || fail "dirty tree rc=$rc: $(head -c 120 "$T/err")"
git checkout -q calc.go

PATH="$T/herdr-down:$PATH" "$BIN" docs/plan/todo-tiny.md --plain > "$T/out" 2> "$T/err"; rc=$?
grep -q 'herdr server unreachable' "$T/err" && ! dirty_refused \
  && ok "an unreachable herdr on a clean tree is not taken for a dirty-tree refusal" || fail "unreachable herdr on a clean tree rc=$rc: $(head -c 120 "$T/err")"

H=$(mktemp -d); HC="$H/.config/r-loop/config.yaml"
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -qx "wrote $HC" "$T/out" && cmp -s "$HC" "$ROOT/internal/config/defaults.yaml" \
  && ok "--create-config writes the embedded defaults" || fail "--create-config rc=$rc"
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -qx "$HC: models already at the defaults" "$T/out" && cmp -s "$HC" "$ROOT/internal/config/defaults.yaml" && [ ! -e "$HC.bak" ] \
  && ok "--create-config over the defaults changes nothing" || fail "--create-config unchanged rc=$rc: $(cat "$T/out" "$T/err")"

cat > "$HC" <<'EOF'
# mine
steps:
  plan:
    model: fable
    timeout: 2h
  implement:
    provider: codex
    model: gpt-5.6-sol
    effort: medium
    fallback:
      provider: claude
      model: sonnet
      effort: low
    reviewers:
      - provider: claude
        model: sonnet
        effort: low
  milestone:
    reviewers:
      - provider: codex
        model: gpt-5.6-sol
        effort: low
  docs:
    prompt: docs
    check: diff
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
land:
  fixRounds: 2
  fix:
    effort: high
watchdog:
  effort: high
  maxRestarts: 5
notify:
  onHalt: "echo hi"
EOF
cp "$HC" "$T/orig"
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc=$?
lines_ok=1
for k in 'steps.plan: fable → claude opus high' 'steps.implement: codex gpt-5.6-sol medium → claude opus medium' \
  'steps.implement.fallback: ' 'steps.implement.reviewers: ' 'steps.milestone.reviewers: .* → removed' 'watchdog: ' 'land.fix: .* → removed'; do
  grep -q "^$HC: $k" "$T/out" || { lines_ok=0; echo "     missing change line: $k"; }
done
if [ "$rc" = 0 ] && [ "$lines_ok" = 1 ] && [ "$(wc -l < "$T/out")" -eq 7 ] && cmp -s "$T/orig" "$HC.bak" \
  && grep -qx '# mine' "$HC" && grep -qx '    timeout: 2h' "$HC" && grep -qx '  fixRounds: 2' "$HC" && grep -qx '  maxRestarts: 5' "$HC" \
  && grep -qx '  onHalt: "echo hi"' "$HC" && ! grep -q -e '{}' -e '\[\]' -e gpt-5.6-sol -e fable -e '^  milestone:' -e '^  fix:' "$HC" \
  && awk '/^  docs:/{f=1;next} f&&/^ ? ?[^ ]/{f=0} f' "$HC" | tr -d ' ' | tr '\n' ',' | grep -qx 'prompt:docs,check:diff,provider:claude,model:sonnet,effort:low,timeout:30m,'; then
  ok "--create-config resets the models of a customised file, keeps the rest, writes .bak"
else fail "--create-config reset rc=$rc: $(cat "$T/out" "$T/err")"; fi
git rm -q .r-loop/config.yaml && git commit -qm drop-config
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -q '^plan  claude  opus  high  2h ' "$T/out" && grep -q '^implement  claude  opus  medium ' "$T/out" \
  && grep -q '^watchdog: claude opus medium ' "$T/out" && ! grep -q -e gpt-5.6-sol -e fable "$T/out" \
  && ok "the reset file loads with the default models and the kept timeout" || fail "dry-run after reset rc=$rc: $(head -2 "$T/err")"

rm -f "$HC.bak"
printf 'steps:\n  milestone:\n    reviewers:\n      - provider: codex\n        model: x\n        effort: low\nland:\n  fix:\n    effort: high\n' > "$HC"
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc1=$?
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc2=$?
[ "$rc1" = 0 ] && [ "$rc2" = 0 ] && ! grep -q -e '{}' -e '\[\]' "$HC" \
  && ok "a reset that empties every mapping still leaves a loadable file" || fail "reset emptied every mapping: create rc=$rc1, dry-run rc=$rc2, file: $(cat "$HC") $(head -1 "$T/err")"

rm -f "$HC.bak"
printf 'steps:\n  plan:\n    model: [unclosed\n' > "$HC"; cp "$HC" "$T/orig"
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 2 ] && grep -q 'config.yaml: yaml: line' "$T/err" && cmp -s "$T/orig" "$HC" && [ ! -e "$HC.bak" ] \
  && ok "--create-config over malformed YAML exits 2 and leaves it" || fail "--create-config malformed rc=$rc"
rm -rf "$H"
git reset -q --hard HEAD~1

for arg in '../../etc/passwd.md' "x; touch $T/pwned.md" "\`touch $T/pwned\`.md" "\$(touch $T/pwned).md"; do
  run "$arg" --dry-run --plain
  if [ "$rc" = 2 ] && [ ! -s "$T/out" ] && [ "$(wc -l < "$T/err")" -le 1 ] && ! ls "$T"/pwned* >/dev/null 2>&1; then
    ok "hostile .md path refused cleanly: $arg"
  else fail "hostile .md path rc=$rc: $arg"; fi
done

R=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$R"
cat > docs/plan/todo-rework.md <<'EOF'
# Calc — Rework Plan
<!-- changes: 2026-10-12 — Finance approves invoices -->

Sources: this file · Status: draft

## Milestone 1 — Calculator

### Phase 1 — Subtract
**Implements:** Subtract numbers
**Depends on:** —
**Files:** `subtract.go` (new) · `subtract_test.go` (new)
- [x] `Subtract(a, b int) int` returns `a - b`
**Done when:** `go test ./...` is green.

### Phase 2 — Subtract saturates
**Implements:** Subtract numbers without going below zero
**Done when:** `go test ./...` is green
  and the saturation table test passes.
**Reworks:** Phase 1
**Depends on:** Phase 1
**Files:** `subtract.go` (modify) · `subtract_test.go` (modify)
- [ ] `Subtract(a, b int) int` returns `0` when `b > a`
EOF
git add -A && git commit -qm rework
run docs/plan/todo-rework.md --dry-run --plain
if [ "$rc" = 0 ] && grep -q '^Plan: .* — 2 phases, 1 already done' "$T/out" && [ "$(grep -c '^phase [0-9]' "$T/out")" = 1 ] \
  && grep -q '^phase 2  Subtract saturates ' "$T/out" && grep -q '^Plan check: no notes' "$T/out" \
  && grep -qx '  Done when: `go test ./...` is green' "$T/out" && ! grep -q 'Reworks' "$T/out"; then
  ok "rework plan: changes comment ignored, only phase 2 runs, no note"
else fail "rework plan dry-run rc=$rc: $(head -1 "$T/err")"; fi
rework_variant() {
  FROM="$1" TO="$2" perl -0pi -e 's/\Q$ENV{FROM}\E/$ENV{TO}/' docs/plan/todo-rework.md && git commit -qam variant
  run docs/plan/todo-rework.md --dry-run --plain
  git reset -q --hard HEAD~1
}
rework_variant '**Reworks:** Phase 1
**Depends on:** Phase 1' '**Reworks:** Phase 1
**Depends on:** —'
[ "$rc" = 0 ] && grep -q '^- Phase 2 — Subtract saturates: reworks phase 1 but does not depend on it$' "$T/out" \
  && ok "rework without depends on is a plan-check note, exit 0" || fail "rework note rc=$rc: $(head -1 "$T/err")"
rework_variant '**Reworks:** Phase 1' '**Reworks:** Phase 5'
[ "$rc" = 2 ] && [ ! -s "$T/out" ] && grep -q 'todo-rework.md line 19: phase 2 reworks phase 5, which does not exist$' "$T/err" \
  && ok "reworks a missing phase exits 2: $(cat "$T/err")" || fail "reworks missing rc=$rc: $(cat "$T/err")"
rework_variant '**Reworks:** Phase 1' '**Reworks:** Phase 2'
[ "$rc" = 2 ] && grep -q 'line 19: phase 2 reworks itself$' "$T/err" \
  && ok "reworks itself exits 2: $(cat "$T/err")" || fail "reworks itself rc=$rc: $(cat "$T/err")"
rework_variant '**Depends on:** —' '**Depends on:** —
**Reworks:** Phase 2'
[ "$rc" = 2 ] && grep -q 'line 11: phase 1 reworks phase 2, which comes after it' "$T/err" \
  && ok "reworks a later phase exits 2: $(cat "$T/err")" || fail "reworks later rc=$rc: $(cat "$T/err")"
rework_variant '**Reworks:** Phase 1' '**Reworks:** the approval flow'
[ "$rc" = 2 ] && grep -q 'line 19: reworks names no phase: "the approval flow"$' "$T/err" \
  && ok "reworks naming no phase exits 2: $(cat "$T/err")" || fail "reworks prose rc=$rc: $(cat "$T/err")"
cd "$ROOT"; rm -rf "$R"

D=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$D"
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^phase 1  Subtract  plan (review ×1) → implement' "$T/out" && ! grep -q '^design ' "$T/out" \
  && ok "the sandbox pins [plan, implement]: no design" || fail "plain sandbox dry-run rc=$rc: $(grep '^phase 1' "$T/out")"
design_cfg() {
  awk -v p="$1" '/^pipeline:$/ { print; n = split(p, e, ","); for (i = 1; i <= n; i++) printf "  - %s\n", e[i]; skip = 1; next }
    skip && /^  - / { next } { skip = 0; print }' "$D/.r-loop/config.yaml.orig" > "$D/.r-loop/config.yaml"
}
cp .r-loop/config.yaml .r-loop/config.yaml.orig
design_cfg design,plan,implement
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 0 ] && grep -q '^phase 1  Subtract  design → plan (review ×1) → implement' "$T/out" \
  && grep -q '^design  claude  sonnet  low  20m  design-file' "$T/out" && grep -q '^prompt design: embedded' "$T/out" \
  && grep -q '3 step sessions (design, plan, implement)' "$T/out" \
  && ok "pipeline [design, plan, implement]: dry-run runs design before plan" || fail "design dry-run rc=$rc: $(head -2 "$T/err") $(grep '^phase 1' "$T/out")"
design_cfg plan,design,implement
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && [ ! -s "$T/out" ] && grep -q 'design-file step must come before the plan-file step' "$T/err" \
  && ok "design after plan exits 2: $(cat "$T/err")" || fail "design after plan rc=$rc: $(cat "$T/err")"
design_cfg design,design,plan,implement
run docs/plan/todo-tiny.md --dry-run --plain
[ "$rc" = 2 ] && [ ! -s "$T/out" ] && grep -q 'second design-file step' "$T/err" \
  && ok "design twice exits 2: $(cat "$T/err")" || fail "design twice rc=$rc: $(cat "$T/err")"
design_cfg design,plan,implement
run docs/plan/todo-design.md --dry-run --plain
[ "$rc" = 0 ] && [ "$(grep -c '^phase [0-9].* design → plan' "$T/out")" = 2 ] \
  && ok "todo-design.md dry-run: 2 phases, design → plan" || fail "todo-design.md dry-run rc=$rc: $(head -2 "$T/err")"
cd "$ROOT"; rm -rf "$D"

W=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$W"
H=$(mktemp -d); HC="$H/.config/r-loop/config.yaml"
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -q '^watchdog: .* allow \[deps, ports, containers, locks, restart, retry, provider\]' "$T/out" \
  && ! grep -q -e '^fixer' -e '^prompt fixer' "$T/out" && ! grep -q '^warning: reviewer' "$T/out" \
  && ok "dry-run banner: default allow classes, no fixer line, no review-ui warning" || fail "watchdog banner rc=$rc: $(grep '^watchdog' "$T/out")"

perl -0pi -e 's/(  implement:\n(?:    [^\n]*\n)*?    reviewers:\n)/$1      - name: ui\n        provider: codex\n        model: gpt-5.6-sol\n        effort: medium\n        prompt: review-ui\n        requires: .claude\/skills\/test-app\/SKILL.md\n/' .r-loop/config.yaml
git commit -qam ui-codex
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -qx 'warning: reviewer ui runs review-ui on codex, whose sandbox cannot start a browser; use claude' "$T/out" \
  && ok "a review-ui reviewer on codex warns and still exits 0" || fail "review-ui on codex rc=$rc: $(head -2 "$T/err")"
git reset -q --hard HEAD~1

perl -0pi -e 's/  unblockTimeout: 15m\n/  unblockTimeout: 15m\n  fixer:\n    provider: claude\n/' .r-loop/config.yaml
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 2 ] && grep -q '\.r-loop/config.yaml:[0-9]*: watchdog.fixer was removed (run r-loop --migrate-config)' "$T/err" \
  && ok "watchdog.fixer in the project config exits 2" || fail "project fixer rc=$rc: $(head -2 "$T/err")"
git checkout -q .r-loop/config.yaml

mkdir -p "$H/.config/r-loop"
printf 'watchdog:\n  effort: high\n  fixer:\n    provider: claude\n    model: opus\n    effort: high\n    timeout: 20m\n' > "$HC"
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 2 ] && grep -q 'watchdog.fixer was removed (run r-loop --migrate-config)' "$T/err" \
  && ok "watchdog.fixer in ~/.config exits 2" || fail "home fixer rc=$rc: $(head -2 "$T/err")"
HOME=$H "$BIN" --migrate-config > "$T/out" 2> "$T/err"; rc=$?
git checkout -q .r-loop/config.yaml; rm -f .r-loop/config.yaml.bak
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > /dev/null 2> "$T/err"; rc2=$?
[ "$rc" = 0 ] && grep -q "^$HC: watchdog.fixer: claude opus high → removed" "$T/out" && ! grep -q fixer "$HC" \
  && grep -qx '  effort: high' "$HC" && [ "$rc2" = 0 ] \
  && ok "--migrate-config drops watchdog.fixer and the dry-run then loads" || fail "migrate fixer rc=$rc dry-run rc=$rc2: $(cat "$T/out") $(head -1 "$T/err")"

rm -f "$HC.bak"
cat > "$HC" <<'EOF'
steps:
  docs:
    prompt: docs
    check: diff
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
    reviewers:
      - name: ui
        provider: codex
        model: sol
        effort: high
        prompt: review-ui
watchdog:
  maxRestarts: 2
  allow:
    - restart
    - retry
  dialogs:
  fixer:
    provider: claude
    model: opus
    effort: high
    timeout: 20m
EOF
HOME=$H "$BIN" --create-config > "$T/out" 2> "$T/err"; rc=$?
lines_ok=1
for k in 'steps.docs.reviewers: ui codex sol high → ui claude opus high' 'watchdog.fixer: claude opus high → removed' \
  'watchdog.allow: restart, retry → restart, retry, deps, ports, containers, locks, provider' \
  'watchdog.dialogs: none → 2 default rules' 'watchdog.maxRestarts: 2 → 3'; do
  grep -qF "$HC: $k" "$T/out" || { lines_ok=0; echo "     missing change line: $k"; }
done
HOME=$H "$BIN" docs/plan/todo-tiny.md --dry-run --plain > /dev/null 2> "$T/err"; rc2=$?
if [ "$rc" = 0 ] && [ "$lines_ok" = 1 ] && [ "$rc2" = 0 ] && ! grep -q -e fixer -e codex -e '[[{]' "$HC" \
  && grep -qx '  maxRestarts: 3' "$HC" && grep -qx '    - provider' "$HC" && grep -q '^    - decline an escalation' "$HC"; then
  ok "--create-config upgrades an old watchdog block and a codex ui reviewer, block style, loads"
else fail "--create-config upgrade rc=$rc dry-run rc=$rc2: $(cat "$T/out" "$T/err")"; fi
rm -rf "$H"

H=$(mktemp -d); HC="$H/.config/r-loop/config.yaml"
HOME=$H "$BIN" --create-config > /dev/null 2>&1
grep -qx '  maxRestarts: 3' "$HC" && ! grep -q fixer "$HC" && [ "$(grep -c -e '^    - approve a sandbox' -e '^    - decline an escalation' "$HC")" = 2 ] \
  && [ "$(sed -n '/name: ui/,/prompt:/p' "$HC" | grep -c -e 'provider: claude' -e 'model: opus')" = 2 ] \
  && ok "--create-config on an empty HOME writes the new watchdog and ui defaults" || fail "fresh defaults: $(grep -n -e maxRestarts -e fixer "$HC")"
rm -rf "$H"

strings "$BIN" > "$T/strings"
! grep -q 'Handing a blocker to the fixer' "$T/strings" && grep -q 'Fixing it yourself' "$T/strings" && grep -q set_config "$T/strings" \
  && ok "embedded watchdog prompt: no fixer hand-off, fixes itself, set_config" || fail "embedded prompt strings"
cd "$ROOT"; rm -rf "$W"

P=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$P"
mkdir -p .r-loop/prompts
printf 'Review phase {{.PhaseNumber}} ({{.PhaseTitle}}). Write findings to {{.FindingsPath}}.\n' > .r-loop/prompts/review.md
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 0 ] && grep -q "^prompt review: .*/.r-loop/prompts/review.md" "$T/out" \
  && ok "a review.md override that never names WatchdogWarnings still renders" || fail "old review override rc=$rc: $(cat "$T/err")"
printf 'Review {{.Bogus}}\n' > .r-loop/prompts/review.md
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$T/out" 2> "$T/err"; rc=$?
[ "$rc" = 2 ] && grep -q 'map has no entry for key "Bogus"' "$T/err" \
  && ok "dry-run renders the override: an unknown key in it exits 2" || fail "bogus review override rc=$rc: $(cat "$T/err")"
cd "$ROOT"; rm -rf "$P"
env -u HERDR_PANE_ID bash "$ROOT/.claude/skills/test-app/e2e/live_step_warn.sh" > "$T/out" 2>&1 < /dev/null; rc=$?
[ "$rc" = 3 ] && [ "$(cat "$T/out")" = "NOT RUN: no herdr pane (HERDR_PANE_ID unset)" ] && ok "live_step_warn: NOT RUN without HERDR_PANE_ID" || fail "live_step_warn without a pane rc=$rc: $(cat "$T/out")"

. "$ROOT/.claude/skills/test-app/e2e/steering_checkers_test.sh"
cd "$ROOT"
go test ./internal/face/... > /dev/null 2>&1 && ok "golden harness go test ./internal/face/..." || fail "go test ./internal/face/..."

rm -rf "$S" "$T"
echo "failures: $fails"
[ "$fails" = 0 ]
