#!/bin/sh
set -u
ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
fails=0
T=$(mktemp -d)
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

FAKE="$T/bin"; CALLS="$T/calls.log"; MARK="$T/installed"; HERDR_LOG="$T/herdr.log"
mkdir -p "$FAKE" "$T/home"
cat > "$FAKE/claude" <<'EOF'
#!/bin/sh
exit 0
EOF
cat > "$FAKE/herdr" <<EOF
#!/bin/sh
echo "\$*" >> "$HERDR_LOG"
exit 1
EOF
cat > "$FAKE/codex" <<EOF
#!/bin/sh
case "\$*" in
  "debug models") echo '{"models":[{"slug":"gpt-6-sol","visibility":"list"}]}' ;;
  "plugin list --json")
    if [ -e "$MARK" ]; then echo '{"installed":[{"pluginId":"codex-security@openai-curated","enabled":true}]}'
    else echo '{"installed":[]}'; fi ;;
  "plugin add "*)
    echo "\$*" >> "$CALLS"
    if [ -n "\${FAKE_ADD_FAIL:-}" ]; then echo "marketplace unreachable" >&2; exit 1; fi
    touch "$MARK" ;;
esac
exit 0
EOF
chmod +x "$FAKE/claude" "$FAKE/codex" "$FAKE/herdr"

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") && cd "$S" || { echo "FAIL sandbox"; exit 1; }
git rm -q .r-loop/config.yaml && git -c user.name=sandbox -c user.email=sandbox@localhost commit -qm "default config"
run() { env PATH="$FAKE:$PATH" HOME="$T/home" "$@" "$BIN" docs/plan/todo-tiny.md --plain > "$T/out" 2> "$T/err"; rc=$?; }

run
adds=$(grep -c '^plugin add codex-security@openai-curated$' "$CALLS" 2>/dev/null)
lines=$(grep -cx 'plugin: codex codex-security@openai-curated installed' "$T/out")
if [ "$rc" = 4 ] && [ "$adds" = 1 ] && [ "$lines" = 1 ] && grep -q 'herdr server unreachable' "$T/err"; then
  ok "missing plugin installed once, then herdr unreachable (exit 4)"
else fail "first install rc=$rc adds=$adds lines=$lines"; cat "$T/out" "$T/err"; fi

: > "$CALLS"
run
if [ "$rc" = 4 ] && [ ! -s "$CALLS" ] && ! grep -q 'installed' "$T/out"; then
  ok "plugin already enabled: no add, no installed line"
else fail "second run rc=$rc calls=$(cat "$CALLS")"; cat "$T/out" "$T/err"; fi

rm -f "$MARK" "$HERDR_LOG"; : > "$CALLS"
run FAKE_ADD_FAIL=1
want='steps.implement.reviewers: codex plugin add codex-security@openai-curated: exit status 1: marketplace unreachable'
if [ "$rc" = 2 ] && grep -qF "$want" "$T/err" && [ ! -e "$HERDR_LOG" ]; then
  ok "failed install exits 2 naming the command and stderr; herdr never called"
else fail "failed install rc=$rc herdr=$( [ -e "$HERDR_LOG" ] && cat "$HERDR_LOG")"; cat "$T/out" "$T/err"; fi

cd /; rm -rf "$S" "$T"
[ "$fails" = 0 ] && echo "all passed" || { echo "$fails failed"; exit 1; }
