#!/bin/sh
# live_agents_tree.sh — the live-step panel's AGENTS tree on a real todo-tiny run:
#   the tree at 120x40 (watchdog node, step rows with provider · model · workspace, aligned branches),
#   the review half (reviewer rows, then findings / the folded round, a finished step as a ✓ row),
#   a geometry sweep 80x24 / 70x30 / 120x40 (header on row 1, one line per tree row, provider column
#   gone below 72 panel columns, EVENTS keeps a line), amber only when the watchdog asks, no bold,
#   q quits and the terminal is restored, no error-level events.
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }

ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-1200}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-agents-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-agents}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
start=$(date +%s)

cap() {
  f=$("$TUI" capture "$H") || { fail "capture $1 rc=$?"; return 1; }
  cp "$f" "$OUT/frames/$1.txt"
  if [ -n "${2:-}" ]; then
    f=$("$TUI" capture "$H" --ansi) || { fail "capture --ansi $1 rc=$?"; return 1; }
    cp "$f" "$OUT/frames/$1.ansi"
  fi
}

check() {
  python3 - "$OUT/frames/$1.txt" "$2" "$3" "${OUT}/frames/$1.ansi" <<'EOF'
import os, re, sys
path, cols, rows = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
ansi = sys.argv[4]
lines = open(path, encoding="utf-8").read().split("\n")
while lines and lines[-1] == "":
    lines.pop()
bad = []
def say(ok, msg):
    print(("OK   " if ok else "FAIL ") + msg)
    if not ok:
        bad.append(msg)
name = os.path.basename(path)
say(len(lines) <= rows, f"{name}: {len(lines)} lines <= {rows} rows")
say(bool(lines) and re.match(r"^\s*r-loop\s+\S+", lines[0]) is not None, f"{name}: header on row 1: {lines[0].strip()[:60] if lines else ''!r}")
say(all(len(l) <= cols for l in lines), f"{name}: no line wider than {cols}")
stacked = cols < 80
def panel(l):
    if stacked:
        return l
    i = l.find("│")
    return l[i + 1:] if i >= 0 else ""
p = [panel(l) for l in lines]
try:
    a = next(i for i, l in enumerate(p) if l.strip() == "AGENTS")
except StopIteration:
    say(False, f"{name}: AGENTS label")
    sys.exit(1)
tree = []
for l in p[a + 1:]:
    if l.strip() == "" or l.strip() == "EVENTS":
        break
    tree.append(l)
say(len(tree) >= 1, f"{name}: {len(tree)} tree rows")
say(bool(tree) and "◆ watchdog" in tree[0], f"{name}: first node is ◆ watchdog: {tree[0].strip() if tree else ''!r}")
cols_top = {len(l) - len(l.lstrip(" ")) for l in tree if re.match(r"^\s*[├└]─ [◆●✓×]", l)}
nested = [l for l in tree if re.search(r"^\s*[├└]─ [◆●✓×] r\d", l)]
nest_cols = {len(l) - len(l.lstrip(" ")) for l in nested}
wrapped = [l for l in tree if not re.match(r"^\s*[├└]─ ", l)]
say(not wrapped, f"{name}: every tree row starts with a branch (no wrapped continuation): {wrapped[:2]!r}")
steps = [l for l in tree if re.search(r"[├└]─ [●✓×] (plan|implement|milestone|gate|gatefix)\b", l)]
say(len(steps) >= 1 or "checking phase" in tree[0], f"{name}: step rows (none needed while the watchdog checks the phase): {[s.strip()[:70] for s in steps]!r}")
step_cols = {len(l) - len(l.lstrip(" ")) for l in steps + [tree[0]]}
say(len(step_cols) == 1, f"{name}: watchdog and step branches in one column: {sorted(step_cols)}")
if nested:
    say(len(nest_cols) == 1 and min(nest_cols) == min(step_cols) + 3, f"{name}: reviewer branches in one column, indented 3: {sorted(nest_cols)}")
lastb = [l.strip()[:2] for l in tree if len(l) - len(l.lstrip(" ")) in step_cols]
say(lastb and lastb[-1] == "└─" and all(b == "├─" for b in lastb[:-1]), f"{name}: top-level branches ├─…└─: {lastb}")
w = (cols - 4) if stacked else (cols - 4 - 24 - 5)
meta = [l for l in steps if re.search(r"(claude|codex) · ", l)]
if w >= 72:
    say(len(meta) == len(steps), f"{name}: panel {w} cols, provider column on every step row ({len(meta)}/{len(steps)})")
    ws = [l for l in steps if re.search(r"(claude|codex) · \S+ · \S+", l)]
    say(len(ws) == len(steps), f"{name}: step rows carry model and workspace ({len(ws)}/{len(steps)})")
else:
    say(not meta and not any(" · " in l for l in tree), f"{name}: panel {w} cols < 72, provider column dropped")
try:
    e = next(i for i, l in enumerate(p) if l.strip() == "EVENTS")
    ev = [l for l in p[e + 1:] if l.strip() and not l.strip().startswith(("q quit", "ctrl"))]
    say(e + 1 < len(p) and p[e + 1].strip() != "", f"{name}: EVENTS shows at least one line: {p[e + 1].strip()[:70] if e + 1 < len(p) else ''!r}")
except StopIteration:
    say(False, f"{name}: EVENTS label present")
if os.path.exists(ansi):
    raw = open(ansi, encoding="utf-8", errors="replace").read().split("\n")
    ai = next((i for i, l in enumerate(raw) if re.sub(r"\x1b\[[0-9;]*m", "", l).strip().endswith("AGENTS")), None)
    if ai is None:
        say(False, f"{name}.ansi: AGENTS in ansi capture")
    else:
        block = []
        for l in raw[ai + 1:]:
            plain = re.sub(r"\x1b\[[0-9;]*m", "", l)
            if not re.search(r"[├└]─ ", plain):
                break
            block.append(l)
        amber = [re.sub(r"\x1b\[[0-9;]*m", "", l).strip() for l in block if re.search(r"38;2;224;16[34];88", l)]
        asking = any("asking you" in re.sub(r"\x1b\[[0-9;]*m", "", l) for l in block)
        say((not amber) or (asking and all("watchdog" in a for a in amber)), f"{name}.ansi: amber in tree only when watchdog asks (amber rows {amber!r}, asking={asking})")
        bold = []
        for l in block:
            seg = l.split("│", 1)[1] if (not stacked and "│" in l) else l
            if re.search(r"\x1b\[(?:[0-9;]*;)?1(?:;[0-9;]*)?m", seg):
                bold.append(re.sub(r"\x1b\[[0-9;]*m", "", seg).strip())
        say(not bold, f"{name}.ansi: no bold in tree rows {bold!r}")
sys.exit(1 if bad else 0)
EOF
  r=$?
  [ "$r" = 0 ] || fails=$((fails + 1))
}

"$TUI" wait-for "$H" 'AGENTS' --timeout 180; rc=$?
if [ "$rc" = 0 ]; then
  sleep 1
  cap agents-120x40 ansi && check agents-120x40 120 40
else
  fail "wait-for AGENTS rc=$rc"
fi

"$TUI" wait-for "$H" '(●|✓|×) r1 ' --timeout 600; rc=$?
if [ "$rc" = 0 ]; then
  sleep 1
  cap review-120x40 ansi && check review-120x40 120 40
  grep -qE '(├|└)─ (●|✓|×) r1 \S+' "$OUT/frames/review-120x40.txt" && ok "reviewer row: $(grep -oE '(├|└)─ (●|✓|×) r1 .*' "$OUT/frames/review-120x40.txt" | head -2 | tr -s ' ' | tr '\n' ';')" || fail "no reviewer row in review frame"
  for g in 80x24 70x30 120x40; do
    "$TUI" resize "$H" "$g"; rrc=$?
    [ "$rrc" = 0 ] || { fail "resize $g rc=$rrc"; continue; }
    sleep 1
    cap "sweep-$g" && check "sweep-$g" "${g%x*}" "${g#*x}"
  done
else
  fail "wait-for r1 rc=$rc"
fi

"$TUI" wait-for "$H" '(●|✓|×) implement' --timeout 900 > /dev/null 2>&1
"$TUI" wait-for "$H" '([0-9]+ findings?|(✓|×) r1  )' --timeout 900; rc=$?
[ "$rc" = 0 ] || echo "NOTE wait-for findings/folded r1 rc=$rc"
sleep 1
cap moved-on-120x40 ansi && check moved-on-120x40 120 40
grep -qE '✓ plan .* ok [0-9]' "$OUT/frames/moved-on-120x40.txt" && ok "finished step row: $(grep -oE '(├|└)─ ✓ plan.*' "$OUT/frames/moved-on-120x40.txt" | tr -s ' ')" || fail "no '✓ plan … ok <elapsed>' row"
grep -qE '([0-9]+ findings?|(✓|×) r1  )' "$OUT/frames/moved-on-120x40.txt" && ok "reviewer result shown: $(grep -oE '(├|└)─ .*([0-9]+ findings?|(✓|×) r1  ).*' "$OUT/frames/moved-on-120x40.txt" | tr -s ' ' | head -2 | tr '\n' ';')" || fail "no 'N findings' / folded r1 row in the moved-on frame"

final=""
while :; do
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed|blocked)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  if [ $(( $(date +%s) - start )) -ge "$LIMIT" ]; then
    "$TUI" send "$H" C-c > /dev/null; sleep 1; "$TUI" send "$H" y > /dev/null
    final="aborted after ${LIMIT}s"
    sleep 30
    "$BIN" status --plain > "$OUT/status.txt" 2>&1
    break
  fi
  sleep 10
done
echo "end     $final ($(( $(date +%s) - start ))s)"
case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
sleep 3
"$TUI" send "$H" q > /dev/null; src=$?
[ "$src" = 0 ] || fail "send q rc=$src"
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
echo "tui     $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "q then stop --expect-exited 0 ($(cat "$OUT/tui-status.txt"))" || fail "stop --expect-exited rc=$src ($(cat "$OUT/tui-status.txt"))"
trap - EXIT

EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
if [ -n "$EV" ]; then
  cp "$EV" "$OUT/events.jsonl"
  cp "$(dirname "$EV")/report.md" "$OUT/report.md" 2>/dev/null
  errs=$(grep -cE '"Kind":"(error|restart-refused)"|"Level":"error"' "$EV")
  [ "$errs" = 0 ] && ok "no error-level events in $EV" || fail "$errs error-level events: $(grep -E '"Kind":"(error|restart-refused)"|"Level":"error"' "$EV" | head -3)"
else
  fail "no events.jsonl"
fi

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
