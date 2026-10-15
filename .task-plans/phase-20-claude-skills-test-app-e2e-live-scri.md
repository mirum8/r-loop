status: planned

## Summary

Two test-suite fixes, all in shell under `.claude/skills/test-app/`, with no Go change.

**#20, `no_agent.sh`.** `BIN` becomes absolute against the caller's cwd right after it is set. The dirty-tree check runs r-loop with a stub `herdr` first on `PATH`. The stub answers `workspace list` with `{}`, so preflight gets past `Reachable` and reaches the dirty-tree check. The check's pass condition moves into a function, `dirty_refused`. It needs exit 4, `primary tree is not clean`, `calc.go`, and no run directory. A second stub, which always fails, pins that `herdr server unreachable` on a clean tree never satisfies that condition.

**#24, three new live scripts.** Each is modelled on `live_hooks.sh`, and each commits a `.r-loop/prompts/*.md` override into its sandbox to steer the claude agent onto a rare path:

- `live_background_sleep.sh` has the agent start a background `sleep 60` and end its turn. It expects no stall and no nudge, and the step to end ok.
- `live_nudge_reply.sh` has the agent end its first turn with no sentinel. It expects stalled → nudge → running → ok.
- `live_evidence_repair.sh` plants a finding through a `review.md` override, and a `fix.md` override asks for `.r-loop/wt/phase-1/…` evidence. It expects a `… read as …` warning and the round to be accepted.

Each script hands its run directory to a standalone checker, `check_background_sleep.py`, `check_nudge_reply.py` or `check_evidence_repair.py`. The checker reads `events.jsonl`, `hooks.jsonl`, the findings file and the verdict file to tell an agent that ignored the steering (`NOT RUN: the agent ignored the steering: …`, exit 3) from a driver defect (`FAIL`). `no_agent.sh` gains the no-agent checks of the three scripts' `NOT RUN` guards, and it runs each checker over fixed fixture run directories, one per classification, so the gate exercises every scenario's verdict logic without a live run. `SKILL.md` names the scripts.

Choices:

- **herdr unreachable: stub or NOT RUN.** This plan uses a stub on `PATH`. It makes the check deterministic whether herdr is up or down. NOT RUN would skip the check exactly where it was falsely passing.
- **Three scripts, or one script with a mode argument.** This plan writes three standalone scripts. Every `live_*.sh` is one standalone scenario (`live_hooks.sh`, `live_subject_repair.sh`), and the items ask for "a live script" each, every one with its own NOT RUN guards.
- **Overrides: hand-written copies, or generated from the embedded templates.**
  - `implement.md` and `fix.md` are generated from `internal/prompts/templates/*` with a steering insert or one sentence replaced. They then keep tracking the real prompt. A hand copy would drift.
  - `review.md` is hand-written and minimal, because the finding must be guaranteed and the stock review prompt leaves findings to the model.
- **Provider check: regex on the sandbox YAML (`live_hooks.sh:33-42`), or the resolved `implement` row of a `--dry-run --plain` banner.** This plan reads the banner. It reflects every config layer and the flags forwarded to the script, and it lets the guard be tested with `--provider implement=codex`.
- **Where the guard tests live: the gate command line, or `no_agent.sh`.** They go in `no_agent.sh`, because `references/subagent-prompt.md:102` says "A new no-agent check belongs there".
- **Scenario verdict logic: an inline python heredoc in each live script, or a standalone `.py` checker taking a run directory.** This plan uses standalone checkers, after `native_review_check.py`, which `live_dialogs.sh:206` calls the same way. A heredoc can only run inside a live run. A standalone checker runs in the gate over fixture run directories, so a broken classification fails acceptance instead of surfacing on the next manual run.

## Why this approach

**#20, the proposal.** The item and the triage propose the same three moves:
- make `BIN` absolute;
- use a stub `herdr` first on `PATH`, or report NOT RUN;
- require both `calc.go` and `primary tree is not clean`.

This plan follows them and drops the NOT RUN branch. Here is the evidence:

- **Why the check falsely passes today.** `Preflight` returns `herdr server unreachable` (exit 4) at `internal/app/preflight.go:46-47`. That is before `w.clean()` (`preflight.go:69`, message at `preflight.go:270-274`), and `no_agent.sh:192` accepts it as a pass.
- **Why the stub takes effect.** `Reachable` only runs `herdr workspace list` and unmarshals the output into `struct{}` (`internal/herdr/client.go:110-113`). The binary is resolved through `Env.Path` = `$PATH`:
  - `cmd/r-loop/main.go:48`
  - `internal/app/wire.go:61-83`
  - `internal/app/preflight.go:170`
- **Why the stub needs no NOT RUN fallback.** The stub can always be created, so the branch would never run. A stub that fails to take effect shows up as the unreachable message, and the check prints FAIL for it, never OK.
- **Why it is safe to reach the dirty-tree check with a stub.** Between `Reachable` and `clean()`, preflight only takes the run lock (`internal/store/store.go:350-369`), which writes the `gate` and `lock` files that `no_agent.sh:193` already tolerates. Nothing else calls herdr before the exit.
- **Why `BIN` breaks today.** `BIN` (`no_agent.sh:4`) is used after `fresh()` cds into the sandbox (`no_agent.sh:10-13`). A relative `TEST_APP_BIN=bin/r-loop` makes all 64 checks fail today; this was reproduced on the base tree.

**#24, the proposal.** The triage proposes new live scripts modelled on `live_hooks.sh`, steered by committed sandbox `.r-loop/prompts` overrides. They report "agent ignored steering" from `hooks.jsonl`, and they are listed in the test-app `SKILL.md`. This plan follows that, with these specifics:

- **How the overrides take effect.** `Renderer.load` reads `<root>/.r-loop/prompts/<name>.md` in place of the embedded template (`internal/prompts/render.go:115`). The shared partials (`sentinel`, `tests`, `commit`) stay available to an override (`render.go:101-103`). The override must be committed, because only `.r-loop/runs/` and `.r-loop/wt/` are excluded, and preflight refuses a dirty tree. Each script also asserts that the dry-run banner names the override (`prompt implement: …/.r-loop/prompts/implement.md`, printed by `internal/app/preflight.go:424`). That way a driver that stopped loading overrides is reported as FAIL, never as "agent ignored the steering".
- **Watchdog warning 1, phase 6's foreground rule.** Phase 6 tells step agents to run builds and tests in the foreground (`render.go:28,43`). The `sleep 60` insert overrides that sentence by name. An agent that still refuses is caught by the `hooks.jsonl` check and reported as ignored steering.
- **Watchdog warning 1, the stall bound.** The bound is `backgroundGraces × StallGrace`:
  - `internal/core/session.go:25,627`
  - `internal/core/session.go:21` gives the 2m default; the sandbox config does not set `stallGrace`
  - that makes 6m, and 60s is inside it, so "no nudge and no stall" is the right expectation
  - the turn-end nudge is skipped while `background` is set (`session.go:591-606`)
  - the flag sticks for the session (`internal/core/hooks.go:92`)
- **Watchdog warning 1, other nudges.** The `maxNudges=3` cap (`session.go:24,642`) and the watchdog's `nudge` blocker action (`internal/core/blockers.go:22`) do not change script 2's expectation: one stall, then a nudge, then running and ok, with no `no response to nudge` (`session.go:602,633`).
- **Watchdog warning 1, binary freshness.** The scripts build `bin/r-loop` from the current tree unless `TEST_APP_BIN` is given, as `live_hooks.sh:17-18` does.
- **Script 2's sequence.** A turn end with no sentinel and no background work stalls and nudges: `session.go:600-606`, then `stallAndNudge` at `session.go:640-670`, which records `stalled` before the `nudge` event.
- **Script 3's warning.** The worktree-prefix repair is `worktreeRel` (`internal/core/evidence.go:558-568`). It adds the warning `verdict <id>: evidence <ev> read as <rel>` (`evidence.go:542-545`) to the step's outcome (`session.go:794,929-930`). The loop emits that as a `warning` event with `Step: implement` (`internal/core/loop.go:409-411`).
- **Where the work half ends.** The review half starts with a `review-round` event for the step (`internal/core/review.go:85`). Fix-half stalls are recorded as the same `implement` step states (`session.go:737-741`), so script 2 binds its stalled → nudge → running sequence to the attempt-1 records before the first `review-round` event. The nudge in a real run (phase 15, attempt 2) was recorded as `stalled`, then `stalled`/`nudge` events, then `running`, all before `review-round`.
- **What `hooks.jsonl` shows of a turn's input.** Every typed prompt is a `UserPromptSubmit` line with the text in `prompt`, capped at 512 bytes (`internal/hooktap/hooktap.go:17,67`). The current run's `hooks.jsonl` shows three kinds:
  - a background task's completion arrives as `<task-notification>…`;
  - the driver's nudge arrives as `r-loop: your turn ended with no sentinel…`;
  - a fix round arrives as `…# Verify and fix review findings — phase N, round …`.

  These let a checker prove that the agent resumed after its sleep, and that the fix prompt reached the agent, before it calls a missing verdict "ignored". The attempt-1 agent is `rloop-<label>-p1-implement`, and a restart's agent is `…-p1-implement-a2`.
- **Script 1's stall bound in the checker.** A stall in the background scenario is a driver fault only when it came inside the bound `backgroundGraces × StallGrace`, counted from the agent's last activity hook line (`session.go:620-637`). `StallGrace` is read from the run's own `config.resolved.yaml`, which the store writes at run creation (`internal/store/store.go:85`) and which holds `watchdog.stallgrace` as resolved over every config layer, `~/.config` included. A `Notification` hook does not count as activity, because an `idle_prompt` notification leaves the driver's quiet clock running (`internal/core/hooks.go:114-119`). A later stall is the bounded-background stall that ADR-59 and `docs/task-loop-driver/tech-design.md:345-349` require. It means the agent never resumed, or went quiet without a sentinel, so the checker reports it as ignored steering, not FAIL.
- **Script 3's planted finding.** Implement reviewers render `review` (`internal/core/kinds.go:34-42`), and plan reviewers render `review-plan`. So a `review.md` override plants the finding only in the implement round. The findings file must name its reviewer and prefix its ids (`evidence.go:421-450`). `native-review.txt` must be non-empty (`evidence.go:405-417`), so the override writes `(none)` when the driver did not.

**Files outside an explicit list.** The phase block carries no `Files:` line.
- `SKILL.md` is changed because its live-tier paragraph (`SKILL.md:64`) is how `/test-app` decides which live script a change needs. Unlisted scripts would never be run, and the paths would stay untested. Its no-agent paragraph (`SKILL.md:62`) also claims that the dirty-tree check needs herdr up, which this phase makes untrue.

## Changes

### 1. `.claude/skills/test-app/e2e/no_agent.sh` (modify) — #20 all bullets, #24 bullet 4

- **Absolute `BIN`.** Add a new line right after line 4, before any `cd`: `case "$BIN" in /*) ;; *) BIN="$PWD/$BIN" ;; esac`. This serves #20 b3.
- **Stub herdr binaries.** After `fail()` (line 9), add two stubs in the run's temp dir. This serves #20 b1 and b2.
  ```sh
  mkdir -p "$T/herdr-up" "$T/herdr-down"
  printf '#!/bin/sh\necho "{}"\n' > "$T/herdr-up/herdr"
  printf '#!/bin/sh\necho "herdr: server not running" >&2\nexit 1\n' > "$T/herdr-down/herdr"
  chmod +x "$T/herdr-up/herdr" "$T/herdr-down/herdr"
  ```
- **The dirty-tree check.** Replace lines 190-196 with the block below. This serves #20 b1, b2 and b4.
  - It uses a direct `"$BIN"` call with an env prefix, as `HOME=$H "$BIN" …` does at line 199, not the `run` helper. That way the `PATH` assignment applies only to that one command.
  ```sh
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
  ```
- **Guard checks for the three live scripts.** Insert this just before `cd "$ROOT"` / `go test ./internal/face/...` (line 459). This serves #24 b4.
  - It follows the table-loop style of line 280.
  - Every invocation runs from `$T` (outside any `.r-loop/wt/`), with the failing stub first on `PATH`. A broken guard therefore ends at `NOT RUN: herdr server down` and never starts a live run.
  ```sh
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
  ```
- **Checker fixture tests.** Insert these right after the guard loop. They serve #24 b1-b4: they gate every scenario's verdict and its split between ignored steering and a driver fault.
  - Helpers come first, defined once. Each fixture builds a run directory from fixed lines. Times are seconds after `2026-10-15T12:00:00+00:00`.
    ```sh
    at()    { printf '2026-10-15T12:%02d:%02d+00:00' $(($1 / 60)) $(($1 % 60)); }
    step()  { printf '{"Kind":"step","At":"%s","Step":{"Run":"r","Phase":"1","Kind":"implement","Attempt":1},"State":"%s","Reason":"%s"}\n' "$(at "$1")" "$2" "${3:-}"; }
    event() { printf '{"Kind":"event","At":"%s","Event":{"At":"%s","Kind":"%s","Phase":"1","Step":"implement","Fields":{"reason":"%s"}}}\n' "$(at "$1")" "$(at "$1")" "$2" "${3:-}"; }
    hook()  { printf '{"at":"%s","agent":"rloop-t-p1-implement","event":"%s","tool":"%s","target":"%s","background":%s,"prompt":"%s"}\n' "$(at "$1")" "$2" "${3:-}" "${4:-}" "${5:-false}" "${6:-}"; }
    rd()    { D="$T/rd/$1"; mkdir -p "$D/phase-1"; : > "$D/events.jsonl"; : > "$D/hooks.jsonl"; }
    grace() { printf 'watchdog:\n    stallgrace: %s\n' "$1" > "$D/config.resolved.yaml"; }
    sent()  { : > "$D/phase-1/implement-a1.sentinel"; TZ=UTC touch -t "$(printf '20261015%02d%02d.%02d' $((12 + $1 / 3600)) $(($1 / 60 % 60)) $(($1 % 60)))" "$D/phase-1/implement-a1.sentinel"; }
    check() { actual_out=$(python3 "$ROOT/.claude/skills/test-app/e2e/check_$1.py" "$D" 2>&1); actual_rc=$?; }
    expect() { [ "$actual_rc" = "$1" ] && printf '%s\n' "$actual_out" | grep -qF "$2" && ok "$3" || fail "$3: rc=$actual_rc $(printf '%s' "$actual_out" | tail -3 | tr '\n' ' ')"; }
    PLANT='{"reviewer":"codex","findings":[{"id":"codex-r1-1","title":"Subtract returns b - a instead of a - b","detail":"d","files":["subtract.go"]}]}'
    ```
  - Each row is one test, laid out like this:
    ```sh
    # given
    rd bg-obeyed
    { step 0 running; event 100 review-round; step 300 ok; } > "$D/events.jsonl"
    { hook 10 PreToolUse Bash "sleep 60" true; hook 12 Stop; hook 71 UserPromptSubmit "" "" false "<task-notification>"; hook 80 PreToolUse Write subtract.go; hook 95 Stop; } > "$D/hooks.jsonl"

    # when
    check background_sleep

    # then
    expect 0 "OK   no stall and no nudge for implement" "background checker passes an agent that slept, ended its turn and resumed"
    ```
  - The rows. An events cell lists `step`/`event` calls in order. A hooks cell lists `hook` calls, where `bg N` stands for `hook N PreToolUse Bash "sleep 60" true`, `note N` for `hook N UserPromptSubmit "" "" false "<task-notification>"` and `nudge-text N` for `hook N UserPromptSubmit "" "" false "r-loop: your turn ended with no sentinel"`. `phase-1/` files are written with `printf '%s'`. Every `bg-*` fixture also calls `grace 2m0s` unless its row names another grace. `sent N` gives `phase-1/implement-a1.sentinel` the mtime of second N.

    | fixture | events.jsonl | hooks.jsonl | other files | expected rc | expected text |
    | --- | --- | --- | --- | --- | --- |
    | `bg-obeyed` | `step 0 running; event 100 review-round; step 300 ok` | `bg 10; hook 12 Stop; note 71; hook 80 PreToolUse Write subtract.go; hook 95 Stop` | — | 0 | `OK   no stall and no nudge for implement` |
    | `bg-no-sleep` | `step 0 running; event 30 review-round; step 200 ok` | `hook 10 PreToolUse Write subtract.go; hook 20 Stop` | — | 3 | `started no background sleep 60` |
    | `bg-kept-working` | `step 0 running; event 90 review-round; step 200 ok` | `bg 10; hook 30 PreToolUse Write subtract.go; hook 85 Stop` | — | 3 | `did not end its turn while the sleep ran` |
    | `bg-early-sentinel` | `step 0 running; event 21 review-round; step 200 ok` | `bg 10; hook 15 PreToolUse Write subtract.go; hook 20 Stop; note 71` | — | 3 | `wrote its sentinel before the sleep finished` |
    | `bg-early-stall` | `step 0 running; step 42 stalled; event 42 nudge; step 50 running; event 100 review-round; step 300 ok` | `bg 10; hook 12 Stop` | — | 1 | `inside the 360s background bound` |
    | `bg-idle-notice` | `step 0 running; step 372 stalled; event 372 nudge; step 374 running; event 500 review-round; step 700 ok` | `bg 10; hook 12 Stop; hook 72 Notification; nudge-text 373` | — | 3 | `did not resume when the sleep finished` |
    | `bg-short-grace` | `step 0 running; step 42 stalled; event 42 nudge; step 50 running; event 100 review-round; step 300 ok` | `bg 10; hook 12 Stop` | `grace 10s` | 3 | `background bound 30s is shorter than the 60s sleep` |
    | `bg-no-grace` | `step 0 running; event 100 review-round; step 300 ok` | `bg 10; hook 12 Stop; note 71` | no `config.resolved.yaml` | 1 | `no watchdog stallgrace in config.resolved.yaml` |
    | `bg-never-woke` | `step 0 running; step 412 stalled; event 412 nudge; step 414 running; event 500 review-round; step 700 ok` | `bg 10; hook 12 Stop; nudge-text 413` | — | 3 | `did not resume when the sleep finished` |
    | `nudge-obeyed` | `step 0 running; step 18 stalled; event 18 nudge; step 21 running; event 61 review-round; step 200 ok` | `hook 5 PreToolUse Read plan.md; hook 8 Stop; nudge-text 20; hook 25 PreToolUse Write subtract.go; hook 60 Stop` | `sent 58` | 0 | `OK   ok after running` |
    | `nudge-bash-sentinel` | `step 0 running; event 9 review-round; step 200 ok` | `hook 5 PreToolUse Bash "cat > subtract.go <<'EOF'"; hook 8 Stop` | `sent 7` | 3 | `wrote its sentinel in its first turn` |
    | `nudge-first-turn-work` | `step 0 running; event 9 review-round; step 200 ok` | `hook 5 PreToolUse Write subtract.go; hook 8 Stop` | — | 3 | `worked in its first turn` |
    | `nudge-fix-half-stall` | `step 0 running; event 9 review-round; step 100 stalled; event 100 nudge; step 110 running; step 200 ok` | `hook 5 PreToolUse Read plan.md; hook 8 Stop` | — | 1 | `FAIL stalled in the work half` |
    | `nudge-no-response` | `step 0 running; step 18 stalled; event 18 nudge; step 300 failed "stalled: no response to nudge"` | `hook 5 PreToolUse Read plan.md; hook 8 Stop` | — | 1 | `FAIL no 'no response to nudge'` |
    | `nudge-no-stop` | `step 0 running; event 9 review-round; step 200 ok` | `hook 5 PreToolUse Read plan.md` | — | 1 | `no Stop hook from the implement agent` |
    | `ev-obeyed` | `event 50 warning "verdict codex-r1-1: evidence .r-loop/wt/phase-1/subtract.go:3 read as subtract.go:3"; step 60 ok` | `hook 40 UserPromptSubmit "" "" false "# Verify and fix review findings — phase 1"; hook 45 Stop` | `implement-findings-codex-r1.json` = `$PLANT`; `implement-verdict-r1.json` = verdict `not-real`, evidence `.r-loop/wt/phase-1/subtract.go:3` | 0 | `OK   warning: verdict codex-r1-1: evidence .r-loop/wt/phase-1/subtract.go:3 read as subtract.go:3` |
    | `ev-no-warning` | `step 60 ok` | as `ev-obeyed` | as `ev-obeyed` | 1 | `FAIL warning:` |
    | `ev-unprefixed` | `step 60 ok` | as `ev-obeyed` | findings `$PLANT`; verdict `not-real`, evidence `subtract.go:3` | 3 | `the implement agent answered codex-r1-1` |
    | `ev-no-fix-prompt` | `step 60 failed "no verdict at implement-verdict-r1.json"` | empty | findings `$PLANT`; no verdict file | 1 | `the fix prompt never reached the implement agent` |
    | `ev-no-plant` | `step 60 ok` | empty | findings `{"reviewer":"codex","findings":[]}` | 3 | `did not report the planted finding` |
    | `ev-no-findings` | `step 60 failed "no findings"` | empty | none | 1 | `no reviewer findings file` |

    The verdict file JSON is `{"findings":[{"id":"codex-r1-1","reviewer":"codex","title":"Subtract returns b - a instead of a - b","verdict":"not-real","severity":"P4","fixed":false,"files":[],"evidence":"<evidence>"}]}`.

### 2. Shared skeleton of the three new scripts

These are all three new `e2e/live_*.sh` files. Every line below is copied into each script; there is no shared helper file. Every script starts `#!/bin/sh`, with no header comment, then `set -u`. The skeleton serves #24 b4, plus "a binary built from the current tree" from the watchdog warning.

The guards and the build come first, in this order:

1. Lines 9-11 of `live_hooks.sh`, verbatim: the `HERDR_PANE_ID`, `R_LOOP_RUN` and `*/.r-loop/wt/*` guards.
2. `ROOT=$(cd "$(dirname "$0")/../../../.." && pwd)` and `BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"`.
3. `case "$BIN" in /*) ;; *) BIN="$PWD/$BIN" ;; esac`.
4. The build line from `live_hooks.sh:18`.

Then the provider probe. It needs no herdr, so it runs before the herdr checks:
```sh
P=$("$ROOT/testdata/sandbox/make-sandbox.sh") || exit 1
prov=$(cd "$P" && "$BIN" docs/plan/todo-tiny.md --dry-run --plain "$@" 2> /dev/null | awk '$1 == "implement" { print $2; exit }')
rm -rf "$P"
[ "$prov" = claude ] || { echo "NOT RUN: the implement step runs on ${prov:-an unknown provider}, not claude"; exit 3; }
```

After the probe:

1. `live_hooks.sh:12-14` verbatim: the herdr-server guard, `. "$(dirname "$0")/own_workspace.sh"` and the `HERDR_SOCKET_PATH` export.
2. `TUI=…` as in `live_hooks.sh:19`, then `LIMIT="${LIVE_LIMIT:-1800}"`, then `OUT` and `fails`, then `ok`/`fail` as in `live_hooks.sh:21-25`.
3. A sandbox at `S="$HOME/r-loop-test-<tag>-$RAND"`, made and entered as in `live_hooks.sh:27-32`. The tags are `bgsleep`, `nudge` and `evrepair`.
4. The script-specific overrides (sections 3-5), written under `.r-loop/prompts/`, then `git add .r-loop/prompts && git commit -qm "test: steer the agent"`.
5. The override check. For each override name `<n>` the script wrote, it runs `"$BIN" docs/plan/todo-tiny.md --dry-run --plain "$@" > "$OUT/dry-run.txt" 2>&1` and greps `^prompt <n>: .*/\.r-loop/prompts/<n>\.md$`. When that is missing it prints `FAIL prompt override <n> not loaded` and does `exit 1`.

The run loop:
- `export TUI_SESSION_SUFFIX="${TUI_SESSION_SUFFIX:-<tag>}"` and the `TUI_START_TIMEOUT` and `TUI_TTL` exports, as in `live_hooks.sh:44-45`.
- `H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md --unattended "$@") || { echo "FAIL start rc=$?"; exit 1; }`, then `trap '"$TUI" stop "$H" > /dev/null 2>&1' EXIT`.
- A poll loop taken from `live_hooks.sh:58-112`, with the AGENTS-tree capture and the settings check removed. Each pass:
  - runs `"$BIN" status --plain > "$OUT/status.txt"` and breaks on `^run [^ ]+ (finished|halted|aborted|failed|blocked)`;
  - breaks when `"$TUI" status "$H"` is not `running*`;
  - at `LIMIT`, sends `C-c` then `y`, sets `final="aborted after ${LIMIT}s"`, waits 30s and breaks;
  - otherwise does `sleep 5`.
- After the loop, it prints `echo "end     $final (…s)"`, runs `"$TUI" stop "$H" > /dev/null 2>&1` and clears the trap with `trap - EXIT`.

Evidence and exit:
1. `RUN=$(ls -d .r-loop/runs/*/ 2>/dev/null | head -1); RUN=${RUN%/}`.
2. Copy `$RUN/events.jsonl` and `$RUN/hooks.jsonl` (if present) to `$OUT`.
3. Run the script's checker (sections 3-5) as `python3 "$ROOT/.claude/skills/test-app/e2e/check_<scenario>.py" "$RUN"; prc=$?`. The checker prints `OK   …` / `FAIL …` lines, and exits 0 when all is OK, 1 on any FAIL, and 3 after printing `NOT RUN: the agent ignored the steering: <what>`.
4. Finish with:
   ```sh
   echo "sandbox $S"; echo "out     $OUT"
   [ "$prc" = 3 ] && exit 3
   [ "$prc" = 0 ] || fails=$((fails + 1))
   echo "failures: $fails"; exit $((fails > 0))
   ```

The checkers are `.claude/skills/test-app/e2e/check_background_sleep.py`, `check_nudge_reply.py` and `check_evidence_repair.py` (create). Each starts `#!/usr/bin/env python3`, takes the run directory as `sys.argv[1]`, and is modelled on `native_review_check.py` with no header comment. Each defines these helpers itself; there is no shared module:
- `run = sys.argv[1].rstrip("/")`.
- `ts(s)`: `datetime.fromisoformat(re.sub(r"(\.\d{6})\d+", r"\1", s.replace("Z", "+00:00")))`, because Go writes up to 9 fractional digits.
- `recs`: the parsed `events.jsonl` lines, and `hooks`: the parsed `hooks.jsonl` lines. A missing file reads as `[]`.
- `states`: `(index, state, attempt)` of every record with `Kind == "step"`, `Step.Phase == "1"` and `Step.Kind == "implement"`, in file order. The record shape is the one `live_hooks.sh:196-209` parses.
- `ev(kind)`: the indexes of records with `Kind == "event"`, `Event.Kind == kind`, `Event.Phase == "1"` and `Event.Step == "implement"`.
- `R`: the first `ev("review-round")` index, or `len(recs)` when there is none. This is the end of the work half.
- `final`: the state of the last entry in `states`, or `None`.
- `say(ok, msg)`: as in `live_hooks.sh:138-141`.
- `ignored(what)`: prints `NOT RUN: the agent ignored the steering: ` + `what` and does `sys.exit(3)`.
- `impl`: the `hooks` lines whose `agent` matches `-p1-implement$`, the attempt-1 agent. `check_evidence_repair.py` matches `-p1-implement(-a\d+)?$` instead, because a fix round may run in a restarted attempt.

The script ends `sys.exit(1 if bad else 0)`, as `live_hooks.sh:181` does.

### 3. `.claude/skills/test-app/e2e/live_background_sleep.sh` (create) — #24 b1 and b4

- **The override.** `.r-loop/prompts/implement.md` is `head -n 1` of `$ROOT/internal/prompts/templates/implement.md`, then the block below from a quoted heredoc, then `tail -n +2` of the template:
  ```
  
  ## Driver test: background work first

  This run tests the driver. Before anything else, start `sleep 60` with the Bash tool and `run_in_background: true`, then end your turn at once: write nothing and run nothing else in this turn. When the task notification that the sleep finished arrives, do the work below and write the sentinel. For this one sleep, this overrides the rule under `## Reporting the outcome` to run commands in the foreground and never end your turn while background work you started is still running.
  ```
- **`check_background_sleep.py`**, in this order:
  0. `grace` is the `stallgrace:` value under `watchdog:` in `run/config.resolved.yaml`, a Go duration such as `2m0s` or `10s`. Parse it by summing its `h`, `m`, `s` and `ms` parts, and treat a value `<= 0` as the driver's 120s default (`session.go:530-532`). When the file or the key is missing, `say(False, "no watchdog stallgrace in config.resolved.yaml")` and exit 1. `bound = 3 * grace`, which is `backgroundGraces` (`session.go:25`). When `bound <= 60`, print `NOT RUN: the background bound {bound:.0f}s is shorter than the 60s sleep; run with a stallGrace above 20s` and exit 3: the scenario cannot hold, and the agent is not at fault.
  1. `bg` is the first `impl` line with `event == "PreToolUse"`, `tool == "Bash"`, `background` true and `target` starting with `sleep 60`. When there is none, call `ignored("the implement agent started no background sleep 60 (hooks.jsonl)")`.
  2. `end = ts(bg["at"]) + timedelta(seconds=60)`. `stop` is the first `impl` line with `event == "Stop"` and `ts(bg["at"]) < ts(at) < end`. When there is none, call `ignored("the implement agent did not end its turn while the sleep ran")`.
  3. `stall` is the smallest index among the `stalled` entries of `states`, `ev("stalled")` and `ev("nudge")`, over the whole step, or `None`.
  4. When there is a `stall`:
     - `quiet` is the seconds between `ts(recs[stall]["At"])` and the last `impl` line before it whose `event` is not `Notification`.
     - When `quiet < bound`, `say(False, f"implement stalled after {quiet:.0f}s of quiet, inside the {bound:.0f}s background bound")`.
     - Otherwise, when no `impl` `UserPromptSubmit` whose `prompt` starts with `<task-notification>` lies between `stop` and the stall, call `ignored(f"the implement agent did not resume when the sleep finished; the stall after {quiet:.0f}s of quiet is the bounded-background stall")`.
     - Otherwise call `ignored(f"the implement agent resumed after the sleep, then went quiet for {quiet:.0f}s without a sentinel")`.
  5. When there is no stall:
     - `resumed` is an `impl` line with `end <= ts(at) < ts(recs[R]["At"])`, where an `R` past the last record means no upper bound.
     - When there is none, call `ignored("the implement agent wrote its sentinel before the sleep finished")`.
     - Otherwise say each of these:
       - `say(True, "hooks.jsonl background line: " + json.dumps(bg)[:200])`
       - `say(True, f"turn ended {(ts(stop['at']) - ts(bg['at'])).seconds}s into the sleep, work resumed after it")`
       - `say(True, "no stall and no nudge for implement")`
  6. Last, `say(final == "ok", f"implement ended ok (states {[s for _, s, _ in states]})")`.

### 4. `.claude/skills/test-app/e2e/live_nudge_reply.sh` (create) — #24 b2 and b4

- **The override.** `.r-loop/prompts/implement.md` is built the same way as section 3, with this insert:
  ```
  
  ## Driver test: a first turn with no sentinel

  This run tests the driver. In your first turn, read the plan at `{{.PlanPath}}` and then end your turn: change no file, write no sentinel and do not call ask_watchdog. The driver will then send you a message; when it arrives, do the work below and write the sentinel. For your first turn only, this overrides the rule to write the sentinel as your last action.
  ```
- **`check_nudge_reply.py`**, in this order:
  1. `first` is the first `impl` line with `event == "Stop"`. When there is none, `say(False, "no Stop hook from the implement agent")` and exit 1.
  2. `sent` is the mtime of `run/phase-1/implement-a1.sentinel`, the attempt-1 work sentinel, which the driver never removes (only reviewer sentinels are removed, at `internal/core/review.go:686`), or `None`. When `sent` is set and `sent <= ts(first["at"]).timestamp()`, call `ignored("the implement agent wrote its sentinel in its first turn")`. This relies on the file, not on a hook `target`, which hook-tap caps at 512 bytes (`internal/hooktap/hooktap.go:67`), so a long Bash command that writes the sentinel is still caught.
  3. `worked` is the `impl` `PreToolUse` lines before `first` whose `tool` is `Edit`, `Write`, `MultiEdit` or `NotebookEdit`. When there are any, call `ignored(f"the implement agent worked in its first turn ({', '.join(sorted({h['tool'] for h in worked}))})")`. Both classifications always run, whatever the stalls.
  4. Every remaining check runs over the work half. `work` is the `states` entries with attempt 1 and index `< R`. `nudges` is the `ev("nudge")` indexes `< R`.
     - `i` is the index of the first `stalled` entry in `work`.
     - `j` is the first entry of `nudges` greater than `i`.
     - `k` is the first `running` entry of `work` greater than `j`.
  5. These checks follow, each resolving to FAIL, never an exception, when an earlier index is `None`:
     - `say(i is not None, "stalled in the work half")`
     - `say(j is not None, "nudge after stalled")`
     - `say(k is not None, "running after the nudge")`
     - `say(final == "ok" and states[-1][0] > k, "ok after running")`
     - `say("no response to nudge" not in open(run + "/events.jsonl").read(), "no 'no response to nudge'")`

### 5. `.claude/skills/test-app/e2e/live_evidence_repair.sh` (create) — #24 b3 and b4

- **`.r-loop/prompts/review.md`** is hand-written from a quoted heredoc:
  ```
  # Review round {{.Round}} of {{.Rounds}} — phase {{.PhaseNumber}} {{.ReviewedKind}}

  This run tests the driver. Do not review the code, and change no file in `{{.Worktree}}`.

  1. If `{{.ArtifactsDir}}/native-review.txt` is missing or empty, write the line `(none)` to it.
  2. Write `{{.FindingsPath}}` as exactly this JSON, with `<name>` replaced by your reviewer name, the `<name>` in the findings file name `<kind>-findings-<name>-r<round>.json`:

         {"reviewer":"<name>","findings":[{"id":"<name>-r{{.Round}}-1","title":"Subtract returns b - a instead of a - b","detail":"Subtract(a, b) must return a - b; check the order of the operands.","files":["subtract.go"]}]}
  {{template "sentinel" .}}
  ```
- **`.r-loop/prompts/fix.md`** is generated by a python heredoc. It reads `$ROOT/internal/prompts/templates/fix.md`, replaces this sentence exactly:

  > Your current directory is the worktree root: write the path relative to it, such as `calc_test.go:75`, never with the worktree's own path in front.

  with this one:

  > This run tests the driver: write the path with the worktree's path relative to the primary checkout in front, as `.r-loop/wt/phase-{{.PhaseNumber}}/<file>:<line>`, such as `.r-loop/wt/phase-{{.PhaseNumber}}/subtract.go:3`; the file after that prefix must be one you read in the worktree.

  and writes the result. When the old sentence is absent, it exits 1 and the script prints `FAIL fix.md no longer carries the evidence-path sentence` and does `exit 1`.
- **`check_evidence_repair.py`**, in this order:
  1. `files` is `$RUN/phase-1/implement-findings-*-r1.json`, without `implement-findings-static-r1.json`. When it is empty, `say(False, "no reviewer findings file for implement round 1")` and exit 1.
  2. `finding` is the entry titled `Subtract returns b - a instead of a - b` in any of `files`. When there is none, call `ignored("the reviewer did not report the planted finding")`.
  3. `entry` is the verdict for `finding["id"]` in `$RUN/phase-1/implement-verdict-r1.json`. A missing file reads as no entry.
  4. When `entry` is missing, its `verdict != "not-real"`, or its `evidence` does not start with `.r-loop/wt/phase-1/`:
     - `delivered` is an `impl` `UserPromptSubmit` with `# Verify and fix review findings` in `prompt`, followed by a later `impl` `Stop`.
     - When there is none, `say(False, "the fix prompt never reached the implement agent, or its turn never ended")` and exit 1.
     - Otherwise call `ignored(f"the implement agent answered {finding['id']} with {(entry or {}).get('verdict')} and evidence {(entry or {}).get('evidence')!r}")`.
- **Driver checks:**
  - `expected = f"verdict {entry['id']}: evidence {entry['evidence']} read as {entry['evidence'][len('.r-loop/wt/phase-1/'):]}"`
  - `say(any(expected in (recs[i]["Event"].get("Fields") or {}).get("reason", "") for i in ev("warning")), "warning: " + expected)`
  - `say(final == "ok", "round accepted: implement ended ok")`
  - `say("is not in the worktree" not in open(run + "/events.jsonl").read(), "no evidence rejected as outside the worktree")`

### 6. `.claude/skills/test-app/SKILL.md` (modify) — #20 b1, #24 discoverability

- **Line 62.** Replace `a dirty tree exits **4** listing the paths (the check runs before any run is created, so it spends nothing even with herdr up)` with `a dirty tree exits **4** listing the paths (run against a stub \`herdr\` first on \`PATH\`, so it needs no herdr server and creates no run)`.
- **Line 64.** After the sentence that ends `an issues-path change needs \`issues.md\`.`, insert:

  > A change to stall, nudge or verdict handling also runs the steered scripts, which commit `.r-loop/prompts` overrides into their sandbox so a claude implement agent reliably takes the rare path: `e2e/live_background_sleep.sh` (a background `sleep 60`: no stall, no nudge, ok), `e2e/live_nudge_reply.sh` (a first turn with no sentinel: stalled → nudge → running → ok) and `e2e/live_evidence_repair.sh` (worktree-prefixed verdict evidence: a `… read as …` warning, the round accepted). Each prints `NOT RUN: the agent ignored the steering: …` when the agent did not do what its override asked; that is never a driver result.

## Tests

The suite is shell, with the `ok`/`fail` style of `no_agent.sh`. There is no Go test here: the phase changes no Go code.

- `no_agent.sh` check "dirty tree refused before any run": with the `herdr-up` stub first on `PATH` and `calc.go` dirty, r-loop exits 4, and stderr names both `primary tree is not clean` and `calc.go`. `.r-loop/runs` holds nothing but `gate`/`lock`. Covers #20 b1 and b4.
- `no_agent.sh` check "an unreachable herdr on a clean tree is not taken for a dirty-tree refusal": with the `herdr-down` stub and a clean tree, stderr says `herdr server unreachable` and `dirty_refused` is false. Covers #20 b2.
- `no_agent.sh` run as `TEST_APP_BIN=bin/r-loop` from the repo root (the gate): `--version`, the first check after `fresh()` cds into the sandbox, prints OK, and the suite ends `failures: 0`. Covers #20 b3.
- `no_agent.sh` check "<script>: NOT RUN without HERDR_PANE_ID": one row per new script. The output is exactly `NOT RUN: no herdr pane (HERDR_PANE_ID unset)` and the exit is 3. Covers #24 b4.
- `no_agent.sh` check "<script>: NOT RUN inside an r-loop run": one row per new script, with `R_LOOP_RUN=x`. The output is exactly `NOT RUN: inside an r-loop run (R_LOOP_RUN set)` and the exit is 3. Covers #24 b4.
- `no_agent.sh` check "<script>: NOT RUN inside an r-loop worktree": one row per new script, run from `$T/nest/.r-loop/wt/phase-1` with `R_LOOP_RUN` empty. The output is exactly `NOT RUN: inside an r-loop worktree` and the exit is 3, with no herdr call. Covers #24 b4.
- `no_agent.sh` check "<script>: NOT RUN when implement is not on claude": one row per new script, with `--provider implement=codex`. The output is exactly `NOT RUN: the implement step runs on codex, not claude` and the exit is 3, with no herdr call. Covers #24 b4.
- `no_agent.sh` checker fixture rows, one test per row of the §1 table, with expected rc and expected text. Covers #24 b1-b4.
  - **`check_background_sleep.py`:**
    - `bg-obeyed` → 0. This is b1.
    - `bg-no-sleep` → 3, `bg-kept-working` → 3 and `bg-early-sentinel` → 3. These are b4's ignored steering, including a sentinel written while the sleep ran.
    - `bg-early-stall` → 1. This is a driver stall inside the 360s bound.
    - `bg-idle-notice` → 3. An idle notification does not shorten the measured quiet.
    - `bg-short-grace` → 3. A resolved `stallGrace` of 10s, as from `~/.config`, sets a bound shorter than the sleep, and the checker reports that instead of blaming the driver.
    - `bg-no-grace` → 1. A run directory with no resolved grace cannot be judged.
    - `bg-never-woke` → 3. This is the bounded-background stall, which is not blamed on the driver.
  - **`check_nudge_reply.py`:**
    - `nudge-obeyed` → 0. This is b2.
    - `nudge-first-turn-work` → 3. This is b4, with the first turn always checked.
    - `nudge-bash-sentinel` → 3. A sentinel written by Bash before the first `Stop` is caught by its mtime.
    - `nudge-fix-half-stall` → 1. A fix-half stall does not count for the work half.
    - `nudge-no-response` → 1 and `nudge-no-stop` → 1.
  - **`check_evidence_repair.py`:**
    - `ev-obeyed` → 0. This is b3.
    - `ev-no-warning` → 1. The driver did not repair the path.
    - `ev-unprefixed` → 3. The fix prompt was delivered, and the agent ignored it.
    - `ev-no-fix-prompt` → 1. A missing verdict with no delivered fix prompt is a driver fault, not ignored steering.
    - `ev-no-plant` → 3 and `ev-no-findings` → 1.
- `live_background_sleep.sh`, live and manual: runs b1 for real and passes the run directory to `check_background_sleep.py`. `OK` needs the background `sleep 60` line, a `Stop` while it ran, a resume after it before the work half ended, no stall or nudge, and a final `ok`. Covers #24 b1 and b4.
- `live_nudge_reply.sh`, live and manual: runs b2 for real through `check_nudge_reply.py`. `OK` needs a first turn with no edits, then attempt-1 `stalled` → `nudge` → `running` before `review-round`, then a final `ok`, and no `no response to nudge`. Covers #24 b2 and b4.
- `live_evidence_repair.sh`, live and manual: runs b3 for real through `check_evidence_repair.py`. `OK` needs the planted finding answered `not-real` with `.r-loop/wt/phase-1/…` evidence, a `warning` event for implement containing `verdict <id>: evidence <ev> read as <rel>`, a final `ok`, and no `is not in the worktree`. Covers #24 b3 and b4.

## Left out

- **A NOT RUN branch for the dirty-tree check.** The stub can always be created, so the branch would never run. A stub that fails to take effect is a FAIL.
- **Fixing the relative-`BIN` bug in the 25 existing `live_*.sh` scripts.** #20 names only `no_agent.sh`. The new scripts carry the fix because they are new.
- **A shared helper for the three scripts' skeleton.** Every e2e live script is standalone by convention, and `own_workspace.sh` is the only shared piece.
- **The TUI restoration, `q` and AGENTS-tree checks of `live_hooks.sh`.** No #24 outcome needs them, and `live_hooks.sh` already covers them.
- **Asserting that the phase lands.** The outcomes stop at the implement step's state and the round's acceptance.
- **Fixture files checked in under `testdata/`.** The fixture run directories are built inline by `no_agent.sh` from fixed lines, which keeps each test's data in the test.
- **Header comments in the new scripts.** The project rule is no comments. `SKILL.md` describes each script.
- **Go changes.** The driver already has the behaviour under test; the units are in `internal/core/hooks_test.go` and `internal/core/verdict_test.go`.

## Assumptions

- **The gate runs no live scenario.** The three live runs need claude agents, a herdr pane and a run outside r-loop, and their own guard stops them inside one (#24 b4). The gate therefore covers each scenario through its checker over fixture run directories, which hold every classification a live run can produce. The live runs themselves are run by hand with `sh .claude/skills/test-app/e2e/live_<name>.sh` from the repo root, outside any r-loop run. A NOT RUN result is never counted as a pass.
- **`no_agent.sh` runs whole.** It has no per-check selector, so the gate runs the whole suite. It finishes in about 5s and passes on the base tree with an absolute `BIN`.
- **"Ignored the steering" is exit 3 with a `NOT RUN: …` line.** That is the skill's existing exit code and wording for a check that did not run (`SKILL.md:129`), so it is never folded into a pass or a fail.
- **Claude Code wakes an interactive session when a background Bash task finishes.** It does this with a `<task-notification>` prompt, as the current run's `hooks.jsonl` shows. If it does not, the driver's bounded stall comes at least the resolved bound (6m at the default `stallGrace`) after the agent's last activity, and the checker reports that the agent did not resume (exit 3), not a driver fault.
- **The checker's background bound is `3 × stallgrace`, from the run's `config.resolved.yaml`.** The multiplier 3 is `backgroundGraces` (`session.go:25`), a constant, not config. Quiet is measured from the agent's last non-`Notification` hook line, which approximates the driver's own quiet clock.
- **Script 2 tells a first-turn sentinel by the mtime of `implement-a1.sentinel` against the first `Stop` hook, and first-turn edits by edit-tool hook lines.** The override forbids any file change in that turn.
- **The `HERDR_SOCKET_PATH` export is kept.** It is copied from `live_hooks.sh:14` because the new scripts read `hooks.jsonl` the way that script does.
- **The watchdog's warning on #24's stall rules is met.** The expectations follow the landed code: the 6m background bound, `maxNudges=3` and the `nudge` blocker action. The background insert overrides phase 6's foreground sentence by name.
- **The watchdog's warning on #20 is met.** Both strings are required, a stub herdr sits first on `PATH`, and `BIN` is made absolute right after line 4.

## Gate

`go build -o bin/r-loop ./cmd/r-loop && TEST_APP_BIN=bin/r-loop sh .claude/skills/test-app/e2e/no_agent.sh`
