#!/bin/bash
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
REVIEWERS="${REVIEWERS:-3}"
case "$REVIEWERS" in 3|4) ;; *) echo "REVIEWERS must be 3 or 4"; exit 2 ;; esac
OUT="${OUT:-$ROOT/.claude/skills/test-app/e2e/frames/short-reviewer-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT/screens"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
[ -z "${PRETRUST:-}" ] || bash "$ROOT/.claude/skills/test-app/e2e/pretrust.sh" "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

python3 - .r-loop/config.yaml "$REVIEWERS" <<'PY'
import re, sys
p = sys.argv[1]
n = int(sys.argv[2])
s = open(p).read()
old = """  implement:
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
    fallback:
      provider: codex
      model: gpt-5.6-sol
      effort: medium
    reviewers:
      - provider: codex
        model: gpt-5.6-sol
        effort: medium
"""
new = """  implement:
    provider: claude
    model: sonnet
    effort: low
    timeout: 30m
    fallback:
      provider: codex
      model: gpt-5.6-sol
      effort: medium
    reviewers:
      - provider: claude
        name: claude-a
        model: sonnet
        effort: low
      - provider: claude
        name: claude-b
        model: sonnet
        effort: low
""" + ("""      - provider: claude
        name: claude-c
        model: sonnet
        effort: low
""" if n == 4 else "") + """      - provider: codex
        model: gpt-5.6-sol
        effort: medium
"""
assert old in s, "implement block not found"
open(p, "w").write(s.replace(old, new))
PY
[ $? = 0 ] || { echo "FAIL config edit"; exit 1; }
git add .r-loop/config.yaml && git -c user.name=sandbox -c user.email=sandbox@localhost commit -q -m "$REVIEWERS implement reviewers, codex last" || { echo "FAIL commit"; exit 1; }

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-shortpane1}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$HERDR_PANE_ID" "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
POLLER=""
trap '[ -n "$POLLER" ] && kill "$POLLER" 2>/dev/null; "$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"

poll_codex() {
  local rows pane src f h
  : > "$OUT/seen.txt"
  : > "$OUT/codex-panes.txt"
  while :; do
    herdr pane list 2>/dev/null | python3 -c '
import json, sys, subprocess
root = sys.argv[1].rstrip("/")
alt = root[len("/private"):] if root.startswith("/private/") else root
for p in json.load(sys.stdin)["result"]["panes"]:
    cwd = p.get("cwd", "") + " " + p.get("foreground_cwd", "")
    if (root in cwd or alt in cwd) and "/.r-loop/wt/" in cwd:
        try:
            lay = json.loads(subprocess.run(["herdr", "pane", "layout", "--pane", p["pane_id"]], capture_output=True, text=True).stdout)
        except ValueError:
            continue
        r = next(x["rect"] for x in lay["result"]["layout"]["panes"] if x["pane_id"] == p["pane_id"])
        print(p["pane_id"], p.get("agent") or "-", p["scroll"]["viewport_rows"], r["width"], r["height"], p.get("tab_id", "-"), p.get("workspace_id", "-"))
' "$S" > "$OUT/panes-now.txt"
    sort -u "$OUT/panes-now.txt" "$OUT/codex-panes.txt" -o "$OUT/codex-panes.txt"
    sed "s/^/$(date +%H%M%S) /" "$OUT/panes-now.txt" >> "$OUT/panes-log.txt"
    while read -r pane agent rows w h tab ws; do
      [ "$agent" = codex ] || continue
      for src in visible recent-unwrapped; do
        f=$(mktemp "$OUT/screens/tmp.XXXXXX")
        if [ "$src" = visible ]; then herdr agent read "$pane" --source visible > "$f" 2>/dev/null
        else herdr agent read "$pane" --source recent-unwrapped --lines 200 > "$f" 2>/dev/null; fi
        h=$(md5 -q "$f")
        if [ ! -s "$f" ] || grep -qx "$h" "$OUT/seen.txt"; then rm -f "$f"; continue; fi
        echo "$h" >> "$OUT/seen.txt"
        mv "$f" "$OUT/screens/$(date +%H%M%S)-$(echo "$pane" | tr ':' '_')-rows$rows-$src.txt"
      done
    done < "$OUT/panes-now.txt"
    sleep 0.5
  done
}
poll_codex &
POLLER=$!

start=$(date +%s)
final=""
while :; do
  sleep 5
  "$BIN" status --plain > "$OUT/status.txt" 2>&1
  if grep -qE '^run [^ ]+ (finished|halted|aborted|failed)' "$OUT/status.txt"; then
    final=$(head -1 "$OUT/status.txt"); break
  fi
  st=$("$TUI" status "$H")
  case "$st" in running*) ;; *) final="app gone: $st"; break ;; esac
  [ $(( $(date +%s) - start )) -lt "$LIMIT" ] || { final="timeout after ${LIMIT}s"; break; }
done
echo "end     $final ($(( $(date +%s) - start ))s)"
sleep 3
kill "$POLLER" 2>/dev/null; POLLER=""
cp "$("$TUI" capture "$H")" "$OUT/frame-end.txt"

EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RD=$(dirname "$EV")
cp "$EV" "$OUT/events.jsonl"
cp "$RD/report.md" "$OUT/report.md" 2>/dev/null
cp "$RD"/blockers* "$OUT/" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac

if grep -rqs "never showed codex's prompt" "$RD" "$OUT/frame-end.txt"; then
  fail "\"never showed codex's prompt\" found: $(grep -rh "never showed codex's prompt" "$RD" | head -2)"
else
  ok "no \"never showed codex's prompt\" in the run dir or the final frame"
fi

ids="claude-a claude-b codex"; [ "$REVIEWERS" = 4 ] && ids="claude-a claude-b claude-c codex"
for id in $ids; do
  ev=$(grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep "\"reviewer\":\"$id\"" | tail -1)
  [ -n "$ev" ] && echo "$ev" | grep -q '"state":"ok"' && ok "implement reviewer $id ran round 1, state ok" || fail "implement reviewer $id: ${ev:-no review-find event}"
done

echo "step/reviewer panes seen (pane agent viewport_rows width height tab workspace):"; sed 's/^/        /' "$OUT/codex-panes.txt"
python3 - "$OUT/panes-log.txt" "$REVIEWERS" > "$OUT/placement.txt" <<'PY'
import sys, collections
snaps = collections.defaultdict(list)
for line in open(sys.argv[1]):
    t, pane, agent, rows, w, h, tab, ws = line.split()
    snaps[t].append((pane, agent, int(w), int(h), tab, ws))
need = 1 + int(sys.argv[2])
per = collections.defaultdict(list)
for t in sorted(snaps):
    byws = collections.defaultdict(list)
    for x in snaps[t]:
        byws[x[5]].append(x)
    for ws, v in byws.items():
        per[ws].append(v)
full = [v for ws in per for v in per[ws] if len({x[0] for x in v}) >= need]
if not full:
    print("NOSNAP max panes seen at once in one workspace:", max((len({x[0] for x in v}) for ws in per for v in per[ws]), default=0))
    sys.exit()
full = [v for v in full if v[0][5] == full[0][0][5]]
full = [v for v in full if len({x[0] for x in v}) == len({x[0] for x in full[0]})]
for pane, agent, w, h, tab, ws in {x[0]: x for x in full[0]}.values():
    print("FIRST", pane, agent, f"{w}x{h}", tab, ws)
last = {x[0]: x for x in full[-1]}.values()
for pane, agent, w, h, tab, ws in last:
    print("LAST", pane, agent, f"{w}x{h}", tab, ws)
small = sorted({(p, w, h) for v in full for (p, a, w, h, t, ws) in v if w < 60 or h < 15})
print("SMALL", " ".join(f"{p}={w}x{h}" for p, w, h in small) if small else "-")
tabs = collections.Counter(t for (p, a, w, h, t, ws) in last)
print("NTABS", len(tabs), " ".join(f"{t}:{n}" for t, n in tabs.items()))
print("NWS", len({ws for (p, a, w, h, t, ws) in last}))
PY
sed 's/^/        /' "$OUT/placement.txt"
if grep -q '^NOSNAP' "$OUT/placement.txt"; then
  fail "never saw the step pane and all $REVIEWERS reviewers at once: $(cat "$OUT/placement.txt")"
else
  small=$(awk '$1=="SMALL"{print $2}' "$OUT/placement.txt")
  [ "$small" = "-" ] && ok "every step/reviewer pane >= 60x15 while all $((REVIEWERS + 1)) were open" || fail "panes below 60x15: $(grep '^SMALL' "$OUT/placement.txt")"
  nws=$(awk '$1=="NWS"{print $2}' "$OUT/placement.txt")
  [ "$nws" = 1 ] && ok "all step/reviewer panes in one step workspace" || fail "step/reviewer panes span $nws workspaces"
  ntabs=$(awk '$1=="NTABS"{print $2}' "$OUT/placement.txt")
  if [ "$REVIEWERS" = 4 ]; then
    [ "$ntabs" -ge 2 ] && ok "overflow reviewer opened in another tab of the step workspace ($ntabs tabs)" || fail "4 reviewers but only $ntabs tab(s)"
  else
    echo "NOTE $ntabs tab(s) used with $REVIEWERS reviewers"
  fi
fi

landed=$(grep '"Kind":"landed"' "$EV" | head -1)
[ -n "$landed" ] && ok "phase landed: ${landed:0:200}" || fail "no landed event"

case "$final" in
  *aborted*) "$TUI" stop "$H" --expect-exited --status 1; rc=$? ;;
  *) "$TUI" send "$H" q > /dev/null; sleep 2; "$TUI" stop "$H" --expect-exited; rc=$? ;;
esac
[ "$rc" = 0 ] && ok "app exited, terminal restored" || fail "stop --expect-exited rc=$rc"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
