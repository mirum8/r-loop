#!/bin/sh
# usage: live_memory.sh
#   ADR-98 project memory on todo-tiny.md --unattended. Seeds three hand-written facts (step, review, and a watchdog
#   fact whose description carries ESC[2J / OSC-title bytes), then: preflight puts .r-loop/memory/ in .git/info/exclude
#   and git status never lists it; the watchdog MCP has remember/forget; remember all is accepted as
#   `remembered as <id>` with the file and a watchdog-call + memory-remember event; bad scope, 151-char, multi-line
#   description, >2 KiB body, unknown id and traversal ids are refused and touch nothing; forget removes the file
#   with a memory-forget event; remember/forget POSTed to a step URL are 404; the TUI feed shows `remembered: …` on
#   one line at 120x40 and 80x24 and an ESC-laden description never clears the screen or retitles the pane; the
#   plan/implement prompts carry the step fact and not the review fact, the reviewer prompts the reverse, the
#   watchdog prompt both with [step]/[review] tags; the phase lands and memory-compacted{facts>=2} is recorded with
#   the compact prompt reaching the watchdog; q exits 0 with the terminal restored.
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
LIMIT="${LIVE_LIMIT:-2400}"
OUT="${OUT:-$(mktemp -d)}"
mkdir -p "$OUT/frames" "$OUT/calls"
fails=0
ok()   { echo "OK   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
note() { echo "info: $1"; }

RAND=$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')
S="$HOME/r-loop-test-memory-$RAND"
"$ROOT/testdata/sandbox/make-sandbox.sh" "$S" > /dev/null || exit 1
cd "$S" || exit 1
echo "sandbox $S"
echo "out     $OUT"
echo "$RAND" > "$OUT/rand.txt"

STEPTOK="MEMTOKEN-STEP-$RAND"
REVTOK="MEMTOKEN-REVIEW-$RAND"
NEWDESC="memcheck-$RAND fresh fact"
M=.r-loop/memory
mkdir -p "$M"
printf -- '---\nscope: step\ndescription: %s keep the subtract table test to three rows\nrun: seed\nat: 2026-10-01T10:00:00Z\n---\nseeded by live_memory.sh\n' "$STEPTOK" > "$M/20261001-seed-step.md"
printf -- '---\nscope: review\ndescription: %s never flag the missing doc comment on Subtract\nrun: seed\nat: 2026-10-01T10:00:00Z\n---\nseeded by live_memory.sh\n' "$REVTOK" > "$M/20261001-seed-review.md"
printf -- '---\nscope: watchdog\ndescription: escseed \033[2J\033[H\033]0;MEMPWN-SEED\007 tail\nrun: seed\nat: 2026-10-01T10:00:00Z\n---\n' > "$M/20261001-seed-esc.md"
git status --porcelain > "$OUT/porcelain-seeded.txt"
note "porcelain after seeding, before any run: $(tr '\n' ' ' < "$OUT/porcelain-seeded.txt")"
"$BIN" docs/plan/todo-tiny.md --dry-run --plain > "$OUT/dryrun.out" 2>&1 && ok "dry-run with seeded memory exits 0" || { fail "dry-run with seeded memory: $(tail -3 "$OUT/dryrun.out")"; }
touch "$OUT/start.marker"

export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-memory}-mem"
export TUI_START_TIMEOUT="${TUI_START_TIMEOUT:-300}" TUI_TTL="${TUI_TTL:-4800}"
H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended) || { echo "FAIL start rc=$?"; exit 1; }
trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT
echo "handle  $H"
echo "$H" > "$OUT/handle.txt"

has() { [ -n "$EV" ] && grep -q "\"Kind\":\"$1\"" "$EV"; }
wd() { "$E2E/wd_call.sh" "$WDURL" "$@"; }
cap() { f=$("$TUI" capture "$H" ${2:-}) && cp "$f" "$OUT/frames/$1"; }

(
  stage=0
  WDURL=""
  : > "$OUT/porcelain-live.txt"
  while "$TUI" status "$H" 2>/dev/null | grep -q '^running'; do
    EV=$(ls .r-loop/runs/*/events.jsonl 2>/dev/null | head -1)
    git status --porcelain >> "$OUT/porcelain-live.txt" 2>/dev/null
    if [ -z "$WDURL" ] && has watchdog-placed; then
      cp .git/info/exclude "$OUT/exclude-live.txt"
      WDURL=$("$E2E/wd_url.sh" "$S" 2>> "$OUT/wdurl.err") && echo "$WDURL" > "$OUT/wdurl.txt"
    fi
    if [ $stage = 0 ] && [ -n "$WDURL" ] && has phase-start; then
      stage=1
      wd tools/list > "$OUT/tools.txt" 2>&1
      wd remember '{"scope":"all","description":"'"$NEWDESC"'","body":"evidence line","why":"live_memory check"}' > "$OUT/calls/remember-ok.json" 2>&1
      ls "$S/$M" > "$OUT/ls-after-remember.txt"
      id=$(sed -n 's/.*remembered as \([^"]*\)".*/\1/p' "$OUT/calls/remember-ok.json")
      echo "$id" > "$OUT/new-id.txt"
      [ -n "$id" ] && cp "$S/$M/$id.md" "$OUT/new-fact.md" 2>/dev/null
      cp "$EV" "$OUT/events-after-remember.jsonl"
      sleep 1.5
      cap remember-120x40.txt
      "$TUI" resize "$H" 80x24 > /dev/null 2>&1; echo $? > "$OUT/resize80.rc"
      sleep 1.5
      cap remember-80x24.txt
      "$TUI" resize "$H" 120x40 > /dev/null 2>&1
      shasum "$S/$M"/* > "$OUT/sum-pre-refused.txt"
      long=$(printf 'x%.0s' $(seq 151))
      big=$(printf 'y%.0s' $(seq 2049))
      wd remember '{"scope":"phase","description":"bad scope","why":"live_memory check"}' > "$OUT/calls/remember-scope.json" 2>&1
      wd remember '{"scope":"all","description":"'"$long"'","why":"live_memory check"}' > "$OUT/calls/remember-long.json" 2>&1
      wd remember '{"scope":"all","description":"two\nlines","why":"live_memory check"}' > "$OUT/calls/remember-multiline.json" 2>&1
      wd remember '{"scope":"all","description":"   ","why":"live_memory check"}' > "$OUT/calls/remember-empty.json" 2>&1
      wd remember '{"scope":"all","description":"big body","body":"'"$big"'","why":"live_memory check"}' > "$OUT/calls/remember-bigbody.json" 2>&1
      wd forget '{"id":"20991231-nope","why":"live_memory check"}' > "$OUT/calls/forget-unknown.json" 2>&1
      wd forget '{"id":"../../etc/passwd","why":"live_memory check"}' > "$OUT/calls/forget-passwd.json" 2>&1
      wd forget '{"id":"../config","why":"live_memory check"}' > "$OUT/calls/forget-config.json" 2>&1
      shasum "$S/$M"/* > "$OUT/sum-post-refused.txt"
      ls -la /etc/passwd "$S/.r-loop/config.yaml" > "$OUT/ls-traversal.txt" 2>&1
      wd remember '{"scope":"watchdog","description":"esctool \u001b[2J\u001b[Hcleared \u001b]0;MEMPWN-TOOL\u0007 tail","why":"live_memory check"}' > "$OUT/calls/remember-esc.json" 2>&1
      sleep 1.5
      cap esc-120x40.txt
      cap esc-120x40.ansi --ansi
      tmux -L "$H" display -p '#{pane_title}' > "$OUT/pane-title.txt" 2>&1
      escid=$(sed -n 's/.*remembered as \([^"]*\)".*/\1/p' "$OUT/calls/remember-esc.json")
      [ -n "$escid" ] && wd forget '{"id":"'"$escid"'","why":"live_memory cleanup"}' > "$OUT/calls/forget-esc.json" 2>&1
      [ -n "$id" ] && wd forget '{"id":"'"$id"'","why":"live_memory check"}' > "$OUT/calls/forget-ok.json" 2>&1
      ls "$S/$M" > "$OUT/ls-after-forget.txt"
      sleep 1.5
      cap forget-120x40.txt
    fi
    if [ $stage = 1 ] && grep '"Kind":"agent-named"' "$EV" | grep -q '"Step":"plan"'; then
      stage=2
      port=$(echo "$WDURL" | sed 's|http://127.0.0.1:\([0-9]*\)/.*|\1|')
      RUN=$(dirname "$EV")
      STEPURL="http://127.0.0.1:$port/mcp/$(cat "$RUN/token")/1/plan/1"
      echo "$STEPURL" > "$OUT/stepurl.txt"
      "$E2E/wd_call.sh" "$STEPURL" tools/list > "$OUT/step-tools.txt" 2>&1
      for t in remember forget; do
        curl -s -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
          -X POST "$STEPURL" -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"'"$t"'","arguments":{"scope":"all","description":"from a step","id":"x","why":"x"}}}' \
          > "$OUT/step-$t.code" 2>&1
      done
      ls "$S/$M" > "$OUT/ls-after-step.txt"
    fi
    sleep 0.3
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
EV=$(ls .r-loop/runs/*/events.jsonl | head -1)
RUN=$(dirname "$EV")
w=0
until grep -q '"Kind":"memory-compacted"' "$EV" || [ $w -ge 420 ]; do sleep 3; w=$((w + 3)); done
echo "compact waited ${w}s"
sleep 5
kill "$watcher" 2>/dev/null
wait "$watcher" 2>/dev/null
cap end-120x40.txt
"$BIN" status --plain > "$OUT/status.txt" 2>&1
cp "$EV" "$OUT/events.jsonl"
cp "$RUN/report.md" "$OUT/report.md" 2>/dev/null
git status --porcelain > "$OUT/porcelain-end.txt"
git log --format='%h %s' > "$OUT/gitlog.txt"
ls -la "$M" > "$OUT/ls-end.txt" 2>&1
for f in "$M"/*.md; do [ -f "$f" ] && { echo "== $f"; cat "$f"; }; done > "$OUT/memory-end.txt" 2>&1

"$TUI" send "$H" q > /dev/null
sleep 3
"$TUI" status "$H" > "$OUT/tui-status.txt"
code=$(grep -o 'exit=[0-9]*' "$OUT/tui-status.txt" | cut -d= -f2)
echo "tui     $(cat "$OUT/tui-status.txt")"

ev() { grep "\"Kind\":\"$1\"" "$EV"; }
call() { cat "$OUT/calls/$1.json" 2>/dev/null; }
refused() { r=$(call "$1"); case "$r" in *'"accepted":true'*|'') fail "$1 not refused: $r" ;; *"$2"*) ok "$1 refused: $r" ;; *) fail "$1 refused with the wrong reason (want $2): $r" ;; esac; }

grep -qx '\.r-loop/memory/' "$OUT/exclude-live.txt" 2>/dev/null && ok ".git/info/exclude has .r-loop/memory/ after preflight" || fail ".git/info/exclude: $(tr '\n' ' ' < "$OUT/exclude-live.txt" 2>/dev/null)"
grep -q '\.r-loop/memory' "$OUT/porcelain-live.txt" "$OUT/porcelain-end.txt" && fail "git status listed .r-loop/memory: $(grep -h '\.r-loop/memory' "$OUT/porcelain-live.txt" | sort -u)" || ok "git status never listed .r-loop/memory during the run ($(wc -l < "$OUT/porcelain-live.txt" | tr -d ' ') sample lines)"

grep -qx remember "$OUT/tools.txt" && grep -qx forget "$OUT/tools.txt" && ok "watchdog tools/list has remember and forget ($(wc -l < "$OUT/tools.txt" | tr -d ' ') tools)" || fail "tools/list: $(tr '\n' ' ' < "$OUT/tools.txt")"
ID=$(cat "$OUT/new-id.txt" 2>/dev/null)
r=$(call remember-ok)
case "$r" in *'"accepted":true'*"remembered as $ID"*) [ -n "$ID" ] && ok "remember all accepted: $r" || fail "remember all: no id in $r" ;; *) fail "remember all: $r" ;; esac
if [ -f "$OUT/new-fact.md" ]; then
  fm=$(sed -n '1,6p' "$OUT/new-fact.md")
  echo "$fm" | grep -qx 'scope: all' && echo "$fm" | grep -qx "description: $NEWDESC" && echo "$fm" | grep -qE '^run: [0-9]{8}-[0-9]{6}$' \
    && echo "$fm" | grep -qE '^at: 20[0-9-]{8}T[0-9:]{8}Z$' && grep -qx 'evidence line' "$OUT/new-fact.md" \
    && ok "$M/$ID.md front matter: $(echo "$fm" | tr '\n' '|')" || fail "$M/$ID.md content: $(tr '\n' '|' < "$OUT/new-fact.md")"
  case "$ID" in [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]-memcheck-*-fresh-fact) ok "id shape: $ID" ;; *) fail "id shape: $ID" ;; esac
else
  fail "$M/$ID.md missing after remember: ls $(tr '\n' ' ' < "$OUT/ls-after-remember.txt" 2>/dev/null)"
fi
python3 - "$OUT/events.jsonl" "$NEWDESC" "$ID" <<'EOF' > "$OUT/order.txt"
import json, sys
p, desc, fid = sys.argv[1:4]
evs = [json.loads(l).get("Event") or {} for l in open(p) if l.strip()]
def idx(pred):
    return next((i for i, e in enumerate(evs) if pred(e)), -1)
f = lambda e: e.get("Fields") or {}
c = idx(lambda e: e.get("Kind") == "watchdog-call" and f(e).get("tool") == "remember" and f(e).get("description") == desc)
m = idx(lambda e: e.get("Kind") == "memory-remember" and f(e).get("description") == desc)
fc = idx(lambda e: e.get("Kind") == "watchdog-call" and f(e).get("tool") == "forget" and f(e).get("id") == fid)
fm = idx(lambda e: e.get("Kind") == "memory-forget" and f(e).get("id") == fid)
refused = [f(e) for e in evs if e.get("Kind") in ("memory-remember", "memory-forget") and (f(e).get("scope") == "phase" or f(e).get("description") in ("bad scope", "big body") or f(e).get("id") in ("20991231-nope", "../../etc/passwd", "../config"))]
print(c, m, fc, fm, len(refused))
print(json.dumps(evs[m]) if m >= 0 else "-")
print(json.dumps(evs[fm]) if fm >= 0 else "-")
EOF
read -r c m fc fm nref < "$OUT/order.txt"
[ "$c" -ge 0 ] && [ "$m" -gt "$c" ] && ok "watchdog-call(remember) #$c then memory-remember #$m: $(sed -n 2p "$OUT/order.txt" | grep -o '"Fields":{[^}]*}')" || fail "remember events: call=$c memory-remember=$m"
grep -q '"Kind":"memory-remember"' "$OUT/events-after-remember.jsonl" 2>/dev/null && ok "memory-remember already in events.jsonl right after the call" || fail "memory-remember missing right after the call"
[ "$fc" -ge 0 ] && [ "$fm" -gt "$fc" ] && ok "watchdog-call(forget) #$fc then memory-forget #$fm: $(sed -n 3p "$OUT/order.txt" | grep -o '"Fields":{[^}]*}')" || fail "forget events: call=$fc memory-forget=$fm"
[ "$nref" = 0 ] && ok "no memory-remember/forget event for any refused call" || fail "$nref memory events for refused calls"

refused remember-scope 'scope \"phase\": want one of all, step, review, watchdog'
refused remember-long 'description is over 150 characters'
refused remember-multiline 'description must be one line'
refused remember-empty 'description is empty'
refused remember-bigbody 'body is over 2048 bytes'
refused forget-unknown 'fact 20991231-nope not found'
refused forget-passwd 'fact ../../etc/passwd not found'
refused forget-config 'fact ../config not found'
cmp -s "$OUT/sum-pre-refused.txt" "$OUT/sum-post-refused.txt" && ok "refused calls left every memory file unchanged" || fail "memory changed across refused calls: $(diff "$OUT/sum-pre-refused.txt" "$OUT/sum-post-refused.txt" | tr '\n' ' ')"
[ -f .r-loop/config.yaml ] && [ -f /etc/passwd ] && [ ! -e /etc/passwd.md ] && ok "traversal ids touched nothing: $(tr '\n' ' ' < "$OUT/ls-traversal.txt")" || fail "traversal: $(cat "$OUT/ls-traversal.txt")"

r=$(call forget-ok)
case "$r" in *'"accepted":true'*) ok "forget $ID accepted: $r" ;; *) fail "forget $ID: $r" ;; esac
grep -qx "$ID.md" "$OUT/ls-after-forget.txt" && fail "$ID.md still there after forget" || ok "$ID.md gone after forget"

st=$(cat "$OUT/step-tools.txt" 2>/dev/null | tr '\n' ' ')
[ "$st" = "ask_watchdog " ] && ok "step URL tools/list is ask_watchdog only" || fail "step URL tools/list: $st"
for t in remember forget; do
  c=$(cat "$OUT/step-$t.code" 2>/dev/null)
  [ "$c" = 404 ] && ok "$t POSTed to the step URL is 404" || fail "$t on the step URL: HTTP $c ($(cat "$OUT/stepurl.txt" 2>/dev/null))"
done
grep -q 'from-a-step' "$OUT/ls-after-step.txt" "$OUT/ls-end.txt" && fail "a step-path remember wrote a fact" || ok "step-path calls wrote no fact"

for g in 120x40 80x24; do
  f="$OUT/frames/remember-$g.txt"
  wcol=${g%x*}
  if [ -f "$f" ]; then
    line=$(grep -F "remembered: $NEWDESC" "$f")
    over=$(perl -CSD -ne 'chomp; $n++ if length($_) > '"$wcol"'; END { print $n + 0 }' "$f")
    cut=$(grep -F "remembered: memcheck-$RAND" "$f" | grep -F '…')
    if [ -n "$line" ] && [ "$over" = 0 ]; then ok "feed $g one line: $(echo "$line" | sed 's/  */ /g')"
    elif [ -n "$cut" ] && [ "$over" = 0 ] && [ "$(grep -c 'remembered: ' "$f")" = 1 ]; then ok "feed $g one line, cut with … to fit: $(echo "$cut" | sed 's/  */ /g')"
    elif grep -q 'remembered: ' "$f"; then fail "feed $g: remembered line wrapped or over-wide: $(grep 'remembered' "$f")"
    else fail "feed $g: no remembered line (frames/remember-$g.txt)"; fi
  else fail "no $g frame after remember"; fi
done
[ "$(cat "$OUT/resize80.rc" 2>/dev/null)" = 0 ] || fail "resize to 80x24 rc=$(cat "$OUT/resize80.rc" 2>/dev/null)"
grep -q "forgot: $ID" "$OUT/frames/forget-120x40.txt" "$OUT/frames/end-120x40.txt" && ok "feed shows forgot: $ID" || fail "no forgot: $ID line in the feed"

case "$(call remember-esc)" in *'"accepted":true'*) note "remember with ESC/OSC bytes in the description is accepted: $(call remember-esc)" ;; *) note "remember with ESC/OSC bytes refused: $(call remember-esc)" ;; esac
if [ -f "$OUT/frames/esc-120x40.ansi" ]; then
  perl -ne 'exit 1 if /\e\[2J|\e\]0;|\e\[H/' "$OUT/frames/esc-120x40.ansi" && ok "ansi frame holds no ESC[2J, ESC[H or OSC 0" || fail "ansi frame carries a raw clear/title sequence"
  grep -q 'esctool' "$OUT/frames/esc-120x40.txt" && note "esc feed line: $(grep 'esctool' "$OUT/frames/esc-120x40.txt" | sed 's/  */ /g')"
  grep -q 'PHASE\|phase 1' "$OUT/frames/esc-120x40.txt" && [ "$(wc -l < "$OUT/frames/esc-120x40.txt" | tr -d ' ')" -ge 10 ] && ok "frame intact after the ESC description ($(wc -l < "$OUT/frames/esc-120x40.txt" | tr -d ' ') rows)" || fail "frame after the ESC description looks cleared: $(head -5 "$OUT/frames/esc-120x40.txt" | tr '\n' '|')"
fi
grep -q 'MEMPWN' "$OUT/pane-title.txt" && fail "pane retitled by an OSC in memory: $(cat "$OUT/pane-title.txt")" || ok "pane title not set by memory OSC: $(cat "$OUT/pane-title.txt")"
grep -q 'MEMPWN' "$OUT/frames/end-120x40.txt" && note "MEMPWN text visible in the end frame: $(grep MEMPWN "$OUT/frames/end-120x40.txt")"

python3 - "$OUT/events.jsonl" "$S" "$STEPTOK" "$REVTOK" "$HOME" "$OUT/start.marker" <<'EOF' > "$OUT/prompts.txt"
import json, os, sys, glob, re
evp, sb, steptok, revtok, home, marker = sys.argv[1:7]
def texts(path):
    out = []
    try:
        lines = open(path).read().splitlines()
    except OSError:
        return out
    for l in lines:
        try:
            d = json.loads(l)
        except ValueError:
            continue
        msg = d.get("message") or {}
        pl = d.get("payload") or {}
        if d.get("type") == "user" and msg.get("role") == "user":
            c = msg.get("content")
            if isinstance(c, str):
                out.append(c)
            elif isinstance(c, list):
                out.append("".join(x.get("text", "") for x in c if isinstance(x, dict) and x.get("type") == "text"))
        elif d.get("type") == "response_item" and pl.get("type") == "message" and pl.get("role") == "user":
            out.append("".join(x.get("text", "") for x in pl.get("content", []) if isinstance(x, dict)))
    return out
evs = [json.loads(l).get("Event") or {} for l in open(evp) if l.strip()]
rows = []
for e in evs:
    if e.get("Kind") != "agent-transcript":
        continue
    f = e.get("Fields") or {}
    role = "reviewer" if f.get("reviewer") else "step"
    mem = [t for t in texts(f.get("transcript_path", "")) if "## Project memory" in t]
    label = f"{e.get('Step')}/{role}{'/' + f['reviewer'] if f.get('reviewer') else ''}"
    if not mem:
        print(f"NOMEM {label} {f.get('transcript_path')}")
        continue
    for i, t in enumerate(mem):
        sec = t[t.index("## Project memory"):]
        sec = sec[:sec.find("\n## ", 5)] if "\n## " in sec[5:] else sec
        print(f"{'STEP' if role == 'step' else 'REV'} {label} msg{i} step={steptok in sec} review={revtok in sec} {f.get('transcript_path')}")
proj = os.path.join(home, ".claude", "projects", re.sub(r"[^A-Za-z0-9]", "-", sb))
mt = os.path.getmtime(marker)
for p in sorted(glob.glob(proj + "/*.jsonl")):
    if os.path.getmtime(p) < mt:
        continue
    ts = texts(p)
    first = next((t for t in ts if "The facts kept so far" in t), None)
    if first:
        sec = first[first.index("The facts kept so far"):]
        sec = sec[:sec.find("\n## ")] if "\n## " in sec else sec
        print(f"WD prompt step={('[step] ' in sec and steptok in sec)} review={('[review] ' in sec and revtok in sec)} tagged={bool(re.search(r'- \[step\] \S+ — ' + re.escape(steptok), sec)) and bool(re.search(r'- \[review\] \S+ — ' + re.escape(revtok), sec))} {p}")
        open(os.path.join(os.path.dirname(marker), "wd-prompt-facts.txt"), "w").write(sec)
    comp = [t for t in ts if "compact memory\nThe run is over." in t]
    if comp:
        c = comp[-1]
        mm = re.search(r"holds (\d+) facts", c)
        print(f"COMPACT n={mm.group(1) if mm else '?'} step={steptok in c} review={revtok in c} {p}")
        open(os.path.join(os.path.dirname(marker), "compact-prompt.txt"), "w").write(c)
EOF
cat "$OUT/prompts.txt" | sed 's/^/     /'
grep -q '^STEP plan/step msg0 step=True review=False' "$OUT/prompts.txt" && ok "plan prompt carries $STEPTOK and not $REVTOK" || fail "plan prompt memory: $(grep 'plan/step' "$OUT/prompts.txt" | head -2)"
grep -q '^STEP implement/step msg0 step=True review=False' "$OUT/prompts.txt" && ok "implement prompt carries $STEPTOK and not $REVTOK" || fail "implement prompt memory: $(grep 'implement/step' "$OUT/prompts.txt" | head -2)"
grep '^STEP' "$OUT/prompts.txt" | grep -q 'review=True\|step=False' && fail "a step/fix prompt has the wrong facts: $(grep '^STEP' "$OUT/prompts.txt" | grep 'review=True\|step=False' | head -2)" || ok "every step/fix prompt with a memory section: step fact only ($(grep -c '^STEP' "$OUT/prompts.txt"))"
if grep -q '^REV' "$OUT/prompts.txt"; then
  grep '^REV' "$OUT/prompts.txt" | grep -q 'step=True\|review=False' && fail "a reviewer prompt has the wrong facts: $(grep '^REV' "$OUT/prompts.txt" | grep 'step=True\|review=False' | head -2)" || ok "reviewer prompts carry $REVTOK and not $STEPTOK ($(grep '^REV' "$OUT/prompts.txt" | cut -d' ' -f2 | tr '\n' ' '))"
else
  fail "no reviewer prompt with a memory section observable: $(grep 'NOMEM' "$OUT/prompts.txt" | tr '\n' ' ')"
fi
grep -q '^WD prompt step=True review=True tagged=True' "$OUT/prompts.txt" && ok "watchdog prompt lists both facts tagged [step]/[review]" || fail "watchdog prompt: $(grep '^WD' "$OUT/prompts.txt" || echo 'not found in transcripts')"
grep -q 'escseed' "$OUT/wd-prompt-facts.txt" 2>/dev/null && note "watchdog prompt also listed the ESC seed (as typed: $(grep escseed "$OUT/wd-prompt-facts.txt" | cat -v))"

case "$final" in *finished*) ok "run finished: $final" ;; *) fail "run did not finish: $final" ;; esac
ev landed > /dev/null && ok "landed: $(ev landed | grep -o '"Fields":{[^}]*}' | head -1)" || fail "no landed"
grep -q '^- \[x\] `Subtract' docs/plan/todo-tiny.md && ok "phase 1 ticked" || fail "todo-tiny.md not ticked"
git log --name-only --format= | grep -qx subtract.go && ok "commit with subtract.go: $(head -3 "$OUT/gitlog.txt" | tr '\n' ' ')" || fail "no commit with subtract.go: $(head -5 "$OUT/gitlog.txt" | tr '\n' ' ')"
mc=$(ev memory-compacted | tail -1 | grep -o '"Fields":{[^}]*}')
n=$(echo "$mc" | sed -n 's/.*"facts":"\([0-9]*\)".*/\1/p')
[ -n "$n" ] && [ "$n" -ge 2 ] && ok "memory-compacted: $mc" || fail "memory-compacted: '${mc:-none}'"
echo "$mc" | grep -q '"reason"' && fail "compaction reported a reason: $mc"
grep -q '^COMPACT' "$OUT/prompts.txt" && ok "compact prompt reached the watchdog: $(grep '^COMPACT' "$OUT/prompts.txt" | cut -d' ' -f1-4)" || fail "compact prompt not seen in the watchdog transcript"
note "memory after compaction: $(ls "$M" 2>/dev/null | tr '\n' ' ')"
note "memory events after the run: $(python3 -c '
import json,sys
for l in open(sys.argv[1]):
    e=json.loads(l).get("Event") or {}
    if e.get("Kind") in ("memory-remember","memory-forget","memory-compacted"): print(e["Kind"], json.dumps(e.get("Fields")))' "$OUT/events.jsonl" | tail -8 | tr '\n' ';')"
grep -q "memory compacted, $n facts before" "$OUT/frames/end-120x40.txt" && ok "feed: memory compacted, $n facts before" || note "end frame has no 'memory compacted' line (may be scrolled off): $(grep -i memory "$OUT/frames/end-120x40.txt" | tr '\n' '|')"
w=$(ev warning | grep -i memory | grep -o '"reason":"[^"]*"' | sort -u)
[ -z "$w" ] && ok "no memory warning" || fail "memory warnings: $w"
[ ! -s "$OUT/porcelain-end.txt" ] && ok "primary tree clean" || fail "primary tree dirty: $(cat "$OUT/porcelain-end.txt")"

[ "$code" = 0 ] && ok "exit 0" || fail "exit $code"
grep -q 'alt=0' "$OUT/tui-status.txt" && ok "q quits, alt screen off" || fail "after q: $(cat "$OUT/tui-status.txt")"
"$TUI" stop "$H" --expect-exited --status 0 > /dev/null 2>&1; src=$?
[ "$src" = 0 ] && ok "stop --expect-exited --status 0" || fail "stop --expect-exited rc=$src"
trap - EXIT

echo "failures: $fails"
echo "sandbox $S"
echo "out     $OUT"
exit $((fails > 0))
