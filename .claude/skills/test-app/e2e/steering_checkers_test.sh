for s in live_background_sleep live_nudge_reply live_evidence_repair; do
  f="$ROOT/.claude/skills/test-app/e2e/$s.sh"
  out=$(cd "$T" && env -u HERDR_PANE_ID PATH="$T/herdr-down:$PATH" sh "$f" 2>&1); rc=$?
  [ "$rc" = 3 ] && [ "$out" = "NOT RUN: no herdr pane (HERDR_PANE_ID unset)" ] && ok "$s: NOT RUN without HERDR_PANE_ID" || fail "$s no pane rc=$rc: $out"
  out=$(cd "$T" && HERDR_PANE_ID=x R_LOOP_RUN=x PATH="$T/herdr-down:$PATH" sh "$f" 2>&1); rc=$?
  [ "$rc" = 3 ] && [ "$out" = "NOT RUN: inside an r-loop run (R_LOOP_RUN set)" ] && ok "$s: NOT RUN inside an r-loop run" || fail "$s inside run rc=$rc: $out"
  out=$(mkdir -p "$T/nest/.r-loop/wt/phase-1" && cd "$T/nest/.r-loop/wt/phase-1" && HERDR_PANE_ID=x R_LOOP_RUN= PATH="$T/herdr-down:$PATH" sh "$f" 2>&1); rc=$?
  [ "$rc" = 3 ] && [ "$out" = "NOT RUN: inside an r-loop worktree" ] && ok "$s: NOT RUN inside an r-loop worktree" || fail "$s inside worktree rc=$rc: $out"
  out=$(cd "$T" && HERDR_PANE_ID=x R_LOOP_RUN= TEST_APP_BIN="$BIN" PATH="$T/herdr-down:$PATH" sh "$f" --provider implement=codex 2>&1); rc=$?
  [ "$rc" = 3 ] && [ "$out" = "NOT RUN: the implement step runs on codex, not claude" ] && ok "$s: NOT RUN when implement is not on claude" || fail "$s provider rc=$rc: $out"
done

at()    { printf '2026-10-15T12:%02d:%02d+00:00' $(($1 / 60)) $(($1 % 60)); }
step()  { printf '{"Kind":"step","At":"%s","Step":{"Run":"r","Phase":"1","Kind":"implement","Attempt":%s},"State":"%s","Reason":"%s"}\n' "$(at "$1")" "${4:-1}" "$2" "${3:-}"; }
event() { printf '{"Kind":"event","At":"%s","Event":{"At":"%s","Kind":"%s","Phase":"1","Step":"implement","Fields":{"reason":"%s"}}}\n' "$(at "$1")" "$(at "$1")" "$2" "${3:-}"; }
hook()  { printf '{"at":"%s","agent":"rloop-t-p1-implement","event":"%s","tool":"%s","target":"%s","background":%s,"prompt":"%s"}\n' "$(at "$1")" "$2" "${3:-}" "${4:-}" "${5:-false}" "${6:-}"; }
bg()    { hook "$1" PreToolUse Bash "sleep 60" true; }
note()  { hook "$1" UserPromptSubmit "" "" false "<task-notification>"; }
nudge_text() { hook "$1" UserPromptSubmit "" "" false "r-loop: your turn ended with no sentinel"; }
rd()    { D="$T/rd/$1"; mkdir -p "$D/phase-1"; : > "$D/events.jsonl"; : > "$D/hooks.jsonl"; }
grace() { printf 'watchdog:\n    stallgrace: %s\n' "$1" > "$D/config.resolved.yaml"; }
sent()  { : > "$D/phase-1/implement-a1.sentinel"; TZ=UTC touch -t "$(printf '20261015%02d%02d.%02d' $((12 + $1 / 3600)) $(($1 / 60 % 60)) $(($1 % 60)))" "$D/phase-1/implement-a1.sentinel"; }
verdict() { printf '{"findings":[{"id":"codex-r1-1","reviewer":"codex","title":"Subtract returns b - a instead of a - b","verdict":"not-real","severity":"P4","fixed":false,"files":[],"evidence":"%s"}]}' "$1" > "$D/phase-1/implement-verdict-r1.json"; }
check() { actual_out=$(python3 "$ROOT/.claude/skills/test-app/e2e/check_$1.py" "$D" 2>&1); actual_rc=$?; }
expect() { [ "$actual_rc" = "$1" ] && printf '%s\n' "$actual_out" | grep -qF "$2" && ok "$3" || fail "$3: rc=$actual_rc $(printf '%s' "$actual_out" | tail -3 | tr '\n' ' ')"; }
PLANT='{"reviewer":"codex","findings":[{"id":"codex-r1-1","title":"Subtract returns b - a instead of a - b","detail":"d","files":["subtract.go"]}]}'
FIXP="# Verify and fix review findings — phase 1"

# given
rd bg-obeyed; grace 2m0s
{ step 0 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; note 71; hook 80 PreToolUse Write subtract.go; hook 95 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 0 "OK   no stall and no nudge for implement" "background checker passes an agent that slept, ended its turn and resumed"

# given
rd bg-no-sleep; grace 2m0s
{ step 0 running; event 30 review-round; step 200 ok; } > "$D/events.jsonl"
{ hook 10 PreToolUse Write subtract.go; hook 20 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "started no background sleep 60" "background checker reports an agent that started no sleep as ignored steering"

# given
rd bg-kept-working; grace 2m0s
{ step 0 running; event 90 review-round; step 200 ok; } > "$D/events.jsonl"
{ bg 10; hook 30 PreToolUse Write subtract.go; hook 85 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "did not end its turn while the sleep ran" "background checker reports an agent that kept working through the sleep as ignored steering"

# given
rd bg-early-sentinel; grace 2m0s
{ step 0 running; event 21 review-round; step 200 ok; } > "$D/events.jsonl"
{ bg 10; hook 15 PreToolUse Write subtract.go; hook 20 Stop; note 71; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "wrote its sentinel before the sleep finished" "background checker reports a sentinel written during the sleep as ignored steering"

# given
rd bg-early-stall; grace 2m0s
{ step 0 running; step 42 stalled; event 42 nudge; step 50 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 1 "inside the 360s background bound" "background checker fails a stall inside the background bound"

# given
rd bg-idle-notice; grace 2m0s
{ step 0 running; step 372 stalled; event 372 nudge; step 374 running; event 500 review-round; step 700 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; hook 72 Notification; nudge_text 373; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "did not resume when the sleep finished" "background checker does not count an idle notification as activity"

# given
rd bg-short-grace; grace 10s
{ step 0 running; step 42 stalled; event 42 nudge; step 50 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "background bound 30s is shorter than the 60s sleep" "background checker reports a stall grace too short for the sleep as not run"

# given
rd bg-spawn-failed; grace 2m0s
{ step 0 spawned; step 5 failed "spawn: agent start failed"; } > "$D/events.jsonl"

# when
check background_sleep

# then
expect 1 "no Stop hook from the implement agent" "background checker fails a step whose agent never had a turn"

# given
rd bg-failed-before-wake; grace 2m0s
{ step 0 running; step 40 failed "invalid sentinel"; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 1 "FAIL implement attempt 1 ended failed before the work half finished" "background checker fails an attempt that failed while the agent waited on the sleep"

# given
rd bg-retry-ok; grace 2m0s
{ step 0 running; step 100 failed "invalid sentinel"; step 110 running "" 2; event 150 review-round; step 300 ok "" 2; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; note 71; hook 80 PreToolUse Write subtract.go; hook 95 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 1 "FAIL implement ended ok" "background checker fails a steered attempt that failed even when a retry ended ok"

# given
rd bg-passive-notice; grace 2m0s
{ step 0 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; hook 72 Notification; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "wrote its sentinel before the sleep finished" "background checker does not take an idle notification after the sleep for a resume"

# given
rd bg-failed-then-retry; grace 2m0s
{ step 0 running; step 40 failed "invalid sentinel"; step 50 running "" 2; event 100 review-round; step 300 ok "" 2; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 1 "FAIL implement attempt 1 ended failed before the work half finished" "background checker fails a steered attempt that failed during the sleep even when a retry reached review"

# given
rd bg-no-grace
{ step 0 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; note 71; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 1 "no watchdog stallgrace in config.resolved.yaml" "background checker fails a run directory with no resolved stall grace"

# given
rd bg-never-woke; grace 2m0s
{ step 0 running; step 412 stalled; event 412 nudge; step 414 running; event 500 review-round; step 700 ok; } > "$D/events.jsonl"
{ bg 10; hook 12 Stop; nudge_text 413; } > "$D/hooks.jsonl"

# when
check background_sleep

# then
expect 3 "did not resume when the sleep finished" "background checker reports the bounded-background stall as ignored steering"

# given
rd nudge-obeyed; sent 58
{ step 0 running; step 18 stalled; event 18 nudge; step 21 running; event 61 review-round; step 200 ok; } > "$D/events.jsonl"
{ hook 5 PreToolUse Read plan.md; hook 8 Stop; nudge_text 20; hook 25 PreToolUse Write subtract.go; hook 60 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 0 "OK   ok after running" "nudge checker passes stalled, nudge, running, ok"

# given
rd nudge-bash-sentinel; sent 7
{ step 0 running; event 9 review-round; step 200 ok; } > "$D/events.jsonl"
{ hook 5 PreToolUse Bash "cat > subtract.go <<'EOF'"; hook 8 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 3 "wrote its sentinel in its first turn" "nudge checker reports a first-turn sentinel written by Bash as ignored steering"

# given
rd nudge-first-turn-work
{ step 0 running; event 9 review-round; step 200 ok; } > "$D/events.jsonl"
{ hook 5 PreToolUse Write subtract.go; hook 8 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 3 "worked in its first turn" "nudge checker reports first-turn edits as ignored steering"

# given
rd nudge-fix-half-stall
{ step 0 running; event 9 review-round; step 100 stalled; event 100 nudge; step 110 running; step 200 ok; } > "$D/events.jsonl"
{ hook 5 PreToolUse Read plan.md; hook 8 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 1 "FAIL stalled in the work half" "nudge checker does not count a fix-half stall for the work half"

# given
rd nudge-no-response
{ step 0 running; step 18 stalled; event 18 nudge; step 300 failed "stalled: no response to nudge"; } > "$D/events.jsonl"
{ hook 5 PreToolUse Read plan.md; hook 8 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 1 "FAIL no 'no response to nudge'" "nudge checker fails a step that got no response to its nudge"

# given
rd nudge-retry-ok
{ step 0 running; step 18 stalled; event 18 nudge; step 21 running; step 60 failed "invalid sentinel"; step 70 running "" 2; event 100 review-round; step 200 ok "" 2; } > "$D/events.jsonl"
{ hook 5 PreToolUse Read plan.md; hook 8 Stop; nudge_text 20; hook 25 PreToolUse Write subtract.go; hook 59 Stop; } > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 1 "FAIL ok after running" "nudge checker fails a nudged attempt that failed even when a retry ended ok"

# given
rd nudge-no-stop
{ step 0 running; event 9 review-round; step 200 ok; } > "$D/events.jsonl"
hook 5 PreToolUse Read plan.md > "$D/hooks.jsonl"

# when
check nudge_reply

# then
expect 1 "no Stop hook from the implement agent" "nudge checker fails a run with no implement Stop hook"

# given
rd ev-obeyed
{ event 50 warning "verdict codex-r1-1: evidence .r-loop/wt/phase-1/subtract.go:3 read as subtract.go:3"; step 60 ok; } > "$D/events.jsonl"
{ hook 40 UserPromptSubmit "" "" false "$FIXP"; hook 45 Stop; } > "$D/hooks.jsonl"
printf '%s' "$PLANT" > "$D/phase-1/implement-findings-codex-r1.json"; verdict .r-loop/wt/phase-1/subtract.go:3

# when
check evidence_repair

# then
expect 0 "OK   warning: verdict codex-r1-1: evidence .r-loop/wt/phase-1/subtract.go:3 read as subtract.go:3" "evidence checker passes a repaired path and an accepted round"

# given
rd ev-no-warning
step 60 ok > "$D/events.jsonl"
{ hook 40 UserPromptSubmit "" "" false "$FIXP"; hook 45 Stop; } > "$D/hooks.jsonl"
printf '%s' "$PLANT" > "$D/phase-1/implement-findings-codex-r1.json"; verdict .r-loop/wt/phase-1/subtract.go:3

# when
check evidence_repair

# then
expect 1 "FAIL warning:" "evidence checker fails a prefixed path with no read-as warning"

# given
rd ev-unprefixed
step 60 ok > "$D/events.jsonl"
{ hook 40 UserPromptSubmit "" "" false "$FIXP"; hook 45 Stop; } > "$D/hooks.jsonl"
printf '%s' "$PLANT" > "$D/phase-1/implement-findings-codex-r1.json"; verdict subtract.go:3

# when
check evidence_repair

# then
expect 3 "the implement agent answered codex-r1-1" "evidence checker reports unprefixed evidence after a delivered fix prompt as ignored steering"

# given
rd ev-no-fix-prompt
step 60 failed "no verdict at implement-verdict-r1.json" > "$D/events.jsonl"
printf '%s' "$PLANT" > "$D/phase-1/implement-findings-codex-r1.json"

# when
check evidence_repair

# then
expect 1 "the fix prompt never reached the implement agent" "evidence checker fails a missing verdict when the fix prompt never arrived"

# given
rd ev-no-plant
step 60 ok > "$D/events.jsonl"
printf '%s' '{"reviewer":"codex","findings":[]}' > "$D/phase-1/implement-findings-codex-r1.json"

# when
check evidence_repair

# then
expect 3 "did not report the planted finding" "evidence checker reports a reviewer that skipped the planted finding as ignored steering"

# given
rd ev-retry-ok
{ event 50 warning "verdict codex-r1-1: evidence .r-loop/wt/phase-1/subtract.go:3 read as subtract.go:3"; step 55 failed "verdict rejected"; step 60 running "" 2; step 90 ok "" 2; } > "$D/events.jsonl"
{ hook 40 UserPromptSubmit "" "" false "$FIXP"; hook 45 Stop; } > "$D/hooks.jsonl"
printf '%s' "$PLANT" > "$D/phase-1/implement-findings-codex-r1.json"; verdict .r-loop/wt/phase-1/subtract.go:3

# when
check evidence_repair

# then
expect 1 "FAIL round accepted: implement ended ok" "evidence checker fails a round whose attempt failed even when a retry ended ok"

# given
rd ev-no-findings
step 60 failed "no findings" > "$D/events.jsonl"

# when
check evidence_repair

# then
expect 1 "no reviewer findings file" "evidence checker fails a round with no reviewer findings file"
