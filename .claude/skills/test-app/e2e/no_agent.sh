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
