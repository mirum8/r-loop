#!/bin/bash
set -u
[ -n "${HERDR_PANE_ID:-}" ] || { echo "NOT RUN: no herdr pane (HERDR_PANE_ID unset)"; exit 3; }
[ -z "${R_LOOP_RUN:-}" ] || { echo "NOT RUN: inside an r-loop run (R_LOOP_RUN set)"; exit 3; }
case "$PWD" in */.r-loop/wt/*) echo "NOT RUN: inside an r-loop worktree"; exit 3 ;; esac
herdr status server > /dev/null 2>&1 || { echo "NOT RUN: herdr server down"; exit 3; }
command -v semgrep > /dev/null || { echo "NOT RUN: semgrep not on PATH"; exit 3; }

ROOT=$(git rev-parse --show-toplevel)
BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
[ -n "${TEST_APP_BIN:-}" ] || (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop) || { echo "FAIL build"; exit 1; }
TUI="${TUI:-/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh}"
LIMIT="${LIVE_LIMIT:-2400}"
OUT="${OUT:-$ROOT/.claude/skills/test-app/e2e/frames/static-reviewer-$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

S=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
cd "$S" || exit 1
[ -z "${PRETRUST:-}" ] || bash "$ROOT/.claude/skills/test-app/e2e/pretrust.sh" "$S" || exit 1
S=$(pwd -P)
echo "sandbox $S"
echo "out     $OUT"

cat > .semgrep.yml <<'EOF'
rules:
  - id: rloop-test-subtract
    languages: [go]
    severity: WARNING
    message: "test rule: Subtract seen"
    pattern: |
      func Subtract(...) $T { ... }
EOF
sed -i '' "s/^label: test\$/label: ${LABEL:-tstatic}/" .r-loop/config.yaml
git add -A && git -c user.name=sandbox -c user.email=sandbox@localhost commit -qm "test: semgrep rule" || exit 1
SEMGREP_SHA=$(git hash-object .semgrep.yml)

"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dry-run.txt" 2>&1
grep -qx 'static: go, semgrep' "$OUT/dry-run.txt" && ok "dry-run banner: static: go, semgrep" || fail "dry-run banner: $(grep '^static' "$OUT/dry-run.txt")"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-staticlive}"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-3600}"
H=$("$TUI" start --geometry 120x40 -- env HERDR_PANE_ID="$HERDR_PANE_ID" "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
POLLER=""
trap '[ -n "$POLLER" ] && kill "$POLLER" 2>/dev/null; "$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
"$TUI" wait-for "$H" 'Subtract' --timeout 300 > /dev/null || fail "no first frame"

poll_analyzers() {
  while :; do
    for pid in $(ps -axww -o pid=,command= | grep -E 'semgrep scan|golangci-lint' | grep -v grep | awk '{print $1}'); do
      cwd=$(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p')
      case "$cwd" in "$S"/*|"$S") echo "$(date +%Y-%m-%dT%H:%M:%S) analyzer running pid=$pid cwd=$cwd $(ps -ww -o command= -p "$pid" | cut -c1-200)" ;; esac
    done
    sleep 0.5
  done > "$OUT/analyzer-ps.txt"
}
poll_analyzers &
POLLER=$!

start=$(date +%s)
final=""
captured=""
while :; do
  sleep 2
  EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
  if [ -z "$captured" ] && [ -n "$EV" ] && grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep -q '"reviewer":"static"'; then
    captured=1
    for g in 160x50 120x40 80x24; do
      "$TUI" resize "$H" "$g" > /dev/null || fail "resize $g rc=$?"
      sleep 1
      cp "$("$TUI" capture "$H")" "$OUT/frame-review-$g.txt"
    done
    cp "$("$TUI" capture "$H" --ansi)" "$OUT/frame-review-80x24.ansi"
    "$TUI" resize "$H" 120x40 > /dev/null
  fi
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
"$BIN" status --plain > "$OUT/status.txt" 2>&1
find "$RD" -name '*-findings-*' -o -name '*-verdict-*' > "$OUT/findings.txt"
cp "$RD"/phase-1/*-findings-static-* "$RD"/phase-1/*-verdict-* "$OUT/" 2>/dev/null

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
[ -n "$captured" ] && ok "captured review frames at 3 geometries" || fail "never saw implement static review-find while live"

sf=$(grep '"Kind":"review-find"' "$EV" | grep '"Step":"implement"' | grep '"reviewer":"static"' | head -1)
echo "$sf" | grep -q '"state":"ok"' && ok "implement static review-find state ok" || fail "implement static review-find: $sf"
n=$(echo "$sf" | grep -o '"findings":"[0-9]*"' | grep -o '[0-9][0-9]*')
[ "${n:-0}" -ge 1 ] && ok "static findings=$n" || fail "static findings=${n:-none}"
echo "$sf" | grep -q '"command":"analyze [^"]*golangci-lint' && echo "$sf" | grep -q '"command":"analyze [^"]*semgrep' && ok "command: $(echo "$sf" | grep -o '"command":"[^"]*"')" || fail "static command: $sf"
grep '"Kind":"review-find"' "$EV" | grep '"Step":"plan"' | grep -q '"reviewer":"static"' && fail "plan step (check plan-file) got a static review" || ok "plan step got no static review"
grep -q 'golangci-lint@v2.12.2' "$OUT/analyzer-ps.txt" && ok "golangci-lint pinned v2.12.2: $(grep -m1 -o 'golangci-lint@v[0-9.]*' "$OUT/analyzer-ps.txt")" || fail "golangci-lint v2.12.2 not seen in ps: $(grep -o 'golangci-lint@v[0-9.]*' "$OUT/analyzer-ps.txt" | sort -u | tr '\n' ' ')"
grep '"Kind":"reviewer-skipped"' "$EV" | grep -q static && fail "static reviewer-skipped present" || ok "no static reviewer-skipped"

python3 - "$EV" "$OUT/analyzer-ps.txt" <<'PY' && ok "static ran concurrently with the codex reviewer" || fail "concurrency not shown"
import json, sys
evs = [json.loads(l)["Event"] for l in open(sys.argv[1]) if '"Kind":"event"' in l]
evs = [e for e in evs if e and e.get("Step") == "implement"]
f = lambda k, rv=None: next((e["At"][:19] for e in evs if e["Kind"] == k and (rv is None or (e.get("Fields") or {}).get("reviewer") == rv)), None)
rnd, named, cdx, st = f("review-round"), f("agent-named", "codex"), f("review-find", "codex"), f("review-find", "static")
ps = [l.split()[0] for l in open(sys.argv[2]) if "analyzer running" in l]
print(f"     review-round {rnd}  codex agent-named {named}  codex review-find {cdx}  static review-find {st}")
print(f"     analyzer seen in sandbox: first {ps[0] if ps else None} last {ps[-1] if ps else None}")
sys.exit(0 if ps and rnd and cdx and rnd <= ps[0] < cdx and (named is None or ps[0] <= cdx) else 1)
PY

SF=$(ls "$RD"/phase-1/implement-findings-static-r1.json 2>/dev/null)
python3 - "$SF" <<'PY' && ok "implement-findings-static-r1.json shape" || fail "static findings file: $SF"
import json, sys
d = json.load(open(sys.argv[1]))
fs = d["findings"] if "findings" in d else d["Findings"]
rv = d.get("reviewer", d.get("Reviewer"))
print("     ", json.dumps(d)[:400])
assert rv == "static"
assert fs and all(f.get("id", f.get("ID", "")).startswith("static-r1-s") for f in fs)
hit = [f for f in fs if "semgrep/rloop-test-subtract" in f.get("title", f.get("Title", ""))]
assert hit, "no semgrep/rloop-test-subtract finding"
assert any("subtract.go" in json.dumps(f) for f in hit)
PY
VF="$RD/phase-1/implement-verdict-r1.json"
python3 - "$VF" <<'PY' && ok "verdict covers the static finding" || fail "verdict: $VF"
import json, sys
d = json.load(open(sys.argv[1]))
fs = d["findings"] if "findings" in d else d["Findings"]
st = [f for f in fs if f.get("reviewer", f.get("Reviewer")) == "static"]
for f in st: print("     ", json.dumps(f)[:400])
assert st
PY
grep '"Kind":"finding"' "$EV" | grep -q '"reviewer":"static"' && ok "finding event for static: $(grep '"Kind":"finding"' "$EV" | grep '"reviewer":"static"' | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no finding event for static"

git log --oneline > "$OUT/git-log.txt"
grep -qi subtract "$OUT/git-log.txt" && ok "phase commit: $(head -1 "$OUT/git-log.txt")" || fail "no phase commit: $(head -3 "$OUT/git-log.txt")"
grep -q '^- \[x\]' docs/plan/todo-tiny.md && ok "todo-tiny.md box ticked" || fail "box not ticked"
[ "$(git hash-object .semgrep.yml)" = "$SEMGREP_SHA" ] && ok ".semgrep.yml unchanged" || fail ".semgrep.yml changed"
git log --all --name-only --format= | grep -E 'findings|verdict|\.r-loop/runs' && fail "run artifacts committed" || ok "no findings/verdict files in any commit"
git show --stat HEAD > "$OUT/head-stat.txt"
[ -z "$(git status --porcelain)" ] && ok "tree clean after land" || fail "tree dirty: $(git status --porcelain | head -3)"
grep -q 'static' "$OUT/status.txt" && ok "status mentions static: $(grep static "$OUT/status.txt" | head -2)" || echo "NOTE status --plain does not mention static"
grep -q 'static' "$OUT/report.md" && ok "report mentions static: $(grep static "$OUT/report.md" | head -3)" || fail "report.md does not mention static"
for g in 160x50 120x40 80x24; do
  grep -qE 'static [0-9]|r1 static' "$OUT/frame-review-$g.txt" 2>/dev/null && ok "TUI $g shows static: $(grep -E 'static [0-9]|r1 static' "$OUT/frame-review-$g.txt" | head -1 | sed 's/^ *//')" || fail "TUI $g frame does not show static"
  python3 -c 'import sys; w=int(sys.argv[2]); bad=[l for l in open(sys.argv[1]).read().split("\n") if len(l)>w]; [print("     wide:",l) for l in bad]; sys.exit(bool(bad))' "$OUT/frame-review-$g.txt" "${g%x*}" && ok "$g frame fits ${g%x*} columns" || fail "$g frame has over-wide lines"
done

"$TUI" send "$H" q > /dev/null
sleep 2
"$TUI" stop "$H" --expect-exited; rc=$?
[ "$rc" = 0 ] && ok "q quits, terminal restored" || fail "stop --expect-exited rc=$rc"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
exit $((fails > 0))
