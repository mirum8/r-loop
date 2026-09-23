---
name: test-app
description: Use this skill to verify r-loop after changes — smoke tests, regression checks, post-build validation, rendered-frame and keybinding verification, terminal-restoration checks, log inspection. Triggers on "test the app", "verify the changes", "check if it works", "smoke test", "did that build work", "/test-app", or any request to validate that recent code or config changes behave correctly end to end.
---

# Test App

<!-- test-app-surface: tui -->

Verifies that recent changes to r-loop actually work — by building it with `go build -o bin/r-loop ./cmd/r-loop` and then driving the real program: a real terminal through `/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh`, direct `bin/r-loop` invocations, persisted e2e scripts, and `tail -n 40 "$SANDBOX"/.r-loop/runs/*/events.jsonl "$SANDBOX"/.r-loop/runs/*/report.md; "$BIN" status --plain`. It checks the happy path, what the app actually renders, the failure paths, **and** the security rules that apply to a program running on someone's machine, then reports concrete pass/fail.

That marker line above is not decoration: `/r:task-review` reads it to decide what to start and what to hand a verifier. Keep it accurate — if this project grows a web surface, re-run `/r:test-app-create` rather than editing the line, because the rest of this file would still describe a terminal app.

This skill owns its artifacts under its own directory — credentials in `.claude/skills/test-app/test_creds.txt`, generated e2e scripts and captured frames in `.claude/skills/test-app/e2e/` — so it never collides with the project's own `scripts/`.

## Where the app runs — build, state, and isolation

There is no server here and no port. What decides whether you are testing *this* change is the **build**, and what stops two runs from corrupting each other is the **state directory**. Resolve both before testing:

    TUI="/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh"
    ROOT=$(git rev-parse --show-toplevel)
    BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"

    if [ -n "${TEST_APP_SESSION:-}" ] || [ -n "${TEST_APP_BIN:-}" ]; then
        :                                    # (1) caller already built and started it — test THAT
    else
        (cd "$ROOT" && go build -o bin/r-loop ./cmd/r-loop)   # (2) build, so the binary under test is this diff
        "$BIN" --version                     # (3) confirm it runs at all before asserting anything
    fi
    SANDBOX=$("$ROOT/testdata/sandbox/make-sandbox.sh")      # (4) the target repo: never the r-loop checkout
    cd "$SANDBOX"

Four cases, in priority order:

1. **Caller supplied `TEST_APP_SESSION` / `TEST_APP_BIN`** (the `/r:task-review` verifier already built and started the app): use them, and do **not** build, start or stop anything — the caller owns the lifecycle, and a second instance of a program that owns a config file or a lock is a collision, not a spare.
2. **Anywhere else**: run `go build -o bin/r-loop ./cmd/r-loop` first. Uncommitted changes reach `bin/r-loop` only through `go build -o bin/r-loop ./cmd/r-loop` from the repo root, so run it before every test session. This matters more here than on a web stack, where a running server at least tells you *something* is up: an unbuilt binary is silently the previous commit, and every check passes against code you did not change.
3. **A linked git worktree needs nothing extra.** Unlike a compose-backed app, two worktrees running a terminal program collide only through its **state directory**, never through a port, and the driver already derives that directory from the checkout root. There is no "refuse to run from a worktree" case, because there is nothing to refuse.
4. **tmux is missing** (the driver exits `127`): the checks that need a real terminal did **not run**. Record them as such and **name tmux** — never report the app as verified, and never substitute a hand-rolled `expect` wrapper. The surface-independent checks below still run and still count.

Every session must be stopped. Set the teardown up as a trap the moment a start succeeds, so the session goes away on **any** exit path — a failed assertion, an error, an abort — not just the happy one:

    H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md) || exit 1
    trap '"$TUI" stop "$H"' EXIT

A leaked tmux session is worse than a leaked container in one specific way: nothing lists it where you would look. The driver's own TTL eventually reaps it, but that is a backstop for a killed run, not a substitute for the trap.

## The sandbox: what r-loop runs against

`testdata/sandbox/` is a template, not a repo. `make-sandbox.sh [dir]` copies it to a fresh directory (default: `mktemp` under `$TMPDIR`), runs `git init`, commits a clean baseline and prints the path. Give every subagent its own copy. r-loop writes `.r-loop/runs/`, `.r-loop/wt/` and `.git/info/exclude` in the repo it runs in, and two runs in one repo refuse each other (`runs/current` names a live pid).

| file | what it exercises |
| --- | --- |
| `docs/plan/todo.md` | three phases: 1 and 2 independent, 3 depends on 1 and is blocked by an open `## Resolve first` entry. It covers the run list, `--from`/`--phases`, the dry-run's `open ## Resolve first` line, and, live, the watchdog's unblock walk |
| `docs/plan/todo-tiny.md` | one phase (`Subtract`), the cheapest live `plan → implement → land` |
| `issues.md` | an issues file: `#1` open (a real bug in `calc.go`: `Add("1, 2")` fails), `#2` ticked, `#3` struck. It covers done detection and the gate that needs the item test red at base |
| `.r-loop/config.yaml` | sonnet with low effort everywhere, one review round, short timeouts, and no `ui` reviewer, so a live run never recurses into this skill |

Edit the template, never a copy, when a check needs a new fixture. Keep its baseline green (`go test ./...` inside a copy) and each plan valid (`"$BIN" <plan> --dry-run --plain` exits 0).

## Two tiers of checks

**No-agent checks always run.** They spend nothing and need no herdr pane: `--version`; argv errors (unknown flag, `--phases` naming a ticked or unknown phase, flow-style YAML or an unknown key in `.r-loop/config.yaml`) exit **2**; `--dry-run --plain` on each sandbox plan prints the banner with provenance and the right run list; the dry-run on `todo.md` names the open `## Resolve first` entry blocking phase 3; a dirty tree exits **4** listing the paths (the check runs before any run is created, so it spends nothing even with herdr up); `status` with no run; `--create-config` under a throwaway `HOME`; `NO_COLOR`; and the golden harness `go test ./internal/face/...` from the repo root. **Every positional argument in this tier must end in `.md`.** Anything else is free text, and it opens a live intake session even with `--dry-run`.

**Live checks** need `herdr status server` to be up and `HERDR_PANE_ID` set, and they spend real agent time. These include the TUI itself, because it draws only after preflight has started the watchdog. Scope them to the change: a render or key change needs one `todo-tiny.md` run in the driver (geometry sweep, `C-c` → `n` cancels, `C-c` → `y` aborts, `q` after the end, restoration); a loop, land or review change needs `todo-tiny.md` to land and tick; an issues-path change needs `issues.md`. After a live run, `"$BIN" status --plain`, `events.jsonl` and `git log` in the sandbox are the evidence. Without a herdr pane, every live check is **NOT RUN** and named, never passed.

## When this skill activates

### 1. Decide what to test — two modes

- **Argument given** (e.g. `/test-app the new filter pane`, `/test-app @parser`): test exactly what the argument names. Don't widen scope to the diff unless the argument is too vague to act on (then ask).
- **No argument**: test the recent change. Look in this order — conversation history (you usually just discussed it), then `git status` + `git diff` for uncommitted work, then `git diff HEAD~1 --stat` if the tree is clean.

Then pick the dimensions that fit (see the catalog). A change to the render layer needs the geometry sweep; a change to a key handler needs the binding pass; a change under the data layer often needs neither, only the functional path and the logs.

### 2. Build the subagent prompt

Read `references/subagent-prompt.md` and substitute `{WHAT_TO_TEST}` with a concrete checklist. Good content names exact keys, screens and expected frame text. Examples:

- *Functional:* "In a fresh sandbox, `start` `"$BIN" docs/plan/todo-tiny.md` at 120x40. `wait-for` `Subtract`. Confirm the phase row turns bold as it goes live, the plan step's state moves off `queued`, and after the run the footer says it finished. Then `git log --oneline` in the sandbox shows the phase commit, and `docs/plan/todo-tiny.md` has its box ticked."
- *Rendering:* "Capture the live frame at 160x50, 120x40 and 80x24. At 80x24 confirm each phase is still one line, the step column is not truncated away, and amber appears only on a row that is waiting for the maintainer."
- *Keys:* "While the run is live, send `C-c`: the stop prompt appears. Send `n`: the notice clears and the run continues. Press `q` while live: nothing happens. After the end, `q` quits."
- *Restoration:* "Quit with the documented key and confirm the terminal is restored — alternate screen off, non-zero exit only if the app meant it. `stop --expect-exited` is the check."

### 3. Surface scan → persisted e2e scripts

Read the real contract before testing it, the way the web pair reads controllers: the app's **binding table** (below, as found — check it is still current) and the widget/view code behind the changed screen. Guessing `q` and reporting "unresponsive" when nothing happens is the commonest way this surface produces a false finding.

| key | what it does | when |
| --- | --- | --- |
| `C-c` | opens the stop prompt: `stop the run? the live step's session and worktree are left for resume [y/n]` | while the run is live |
| `y` | at the stop prompt: requests an abort; the run stops before its next step | stop prompt open |
| any other key | at the stop prompt: cancels it and clears the notice | stop prompt open |
| `q` | quits the TUI | **only after the run has ended** (finished, halted, aborted); ignored while live |

There is no other binding: no scrolling, no focus, no help screen. Every other key while the run is live must be ignored. The TUI renders only during a real run, which starts after preflight and the watchdog, so every frame check is a live run in the sandbox. The binding table is `Model.Update` in `internal/face/tui/model.go`.

Turn the meaningful flows into reusable scripts under `.claude/skills/test-app/e2e/` and keep them, so the next run extends them instead of rewriting one-off invocations. A TUI flow is a sequence of `send` / `wait-for` / `capture` with the expected frame fragments asserted. Where the project already has a harness for this (golden frames at 120x40 and 70x30 plus model tests: `go test ./internal/face/...` (never pass `-update`, which rewrites the golden files to whatever the code now draws)), add the case **there** instead: that is where it will keep running after this review is over.

### 4. Verification catalog — pick what fits the change

Always on: **functional behaviour**, **rendering correctness**, and **terminal restoration**. Add the rest when relevant.

- **Functional** — the changed behaviour works when driven by real keystrokes, asserted against what the app drew.
- **Rendering correctness** — the frame shows what it should: the new pane, column or row is present with the right values; nothing truncated with `…` where it would fit; no mojibake where a box-drawing or wide glyph belongs; no widgets overlapping.
- **Geometry sweep** — re-capture the changed screen at 160x50, 120x40 and 80x24. **80x24 is the one that finds things**: it is the size every terminal guarantees, and it is where a layout that quietly assumes width falls apart. Below the app's own minimum size it must *say so* rather than crash.
- **Keybinding and focus** — every binding the change touches does what the app claims; `Tab` and the arrows move focus **visibly**; `Esc` backs out; the documented quit key quits. And the one that regresses most: an **unbound** key is ignored — not swallowed into a text field, not a crash.
- **Terminal restoration on exit** — after the quit path, alternate screen off, cursor visible, echo restored. This is the defect users actually report, and no in-process test harness can see it, because there is no real terminal to leave broken.
- **Crash surface** — reach the nearest error path on purpose. A panic or traceback must not be painted over the frame and left there with the terminal in raw mode; the message has to be readable, not interleaved with layout escapes.
- **Non-TTY degradation** — `"$BIN" docs/plan/todo-tiny.md < /dev/null | cat`, and `TERM=dumb`. A clear message and an exit, never a hang waiting for input that will never come. A TUI that hangs when piped is the single commonest way this kind of app breaks a CI job.
- **Colour and `NO_COLOR`** — capture with escapes kept and confirm the styling is there with a terminal attached; then `NO_COLOR=1` and confirm the same content with no colour sequences at all. An app that ignores `NO_COLOR` is a finding, not a skip.
- **Large data** — if the change touches a list or table, feed it more rows than the viewport: the last item is reachable, the scroll indicator is honest, and paging does not repaint garbage.
- **State isolation** — the run wrote nothing outside its sandbox copy (in particular nothing into the real `~/.config/r-loop/`), and a fresh sandbox starts clean rather than erroring.
- **Security** — see the pass in `references/subagent-prompt.md`; it is short, and the parts that survive from a web app are the parts that get skipped.
- **Regression of adjacent flows** — re-exercise the neighbours of what changed, so the fix didn't break something next door.

### 5. Credentials on demand

The subagent reads accounts and tokens from `.claude/skills/test-app/test_creds.txt`. No credentials are needed: the agents use the maintainer's own logged-in `claude` and `codex`, and r-loop has no auth of its own. If a flow needs one that isn't there, **pause and ask the user**, then append it in the existing format so the next run is unattended. Never invent credentials and never paste a real secret into this skill or the subagent prompt — always reference the file.

### 6. Spawn subagent(s)

Spawn via the Agent tool with `subagent_type: general-purpose`, prompt = the filled template. Use one subagent per focused area; if the change touches independent areas, spawn them in parallel in one message. **Give each one a distinct `TUI_SESSION_SUFFIX`.** Two subagents driving one pane interleave their keystrokes and both report nonsense — the driver isolates by suffix, but only if they are actually different. Run foreground — you need the results to report back.

### 7. Report results

Summarize concisely: `N passed, M failed, K skipped`, with evidence. Evidence is a frame file from the driver for anything the TUI drew, and for everything else the argv, exit code, stdout and stderr, plus the run's `events.jsonl` lines. Don't relay the subagent's text verbatim — surface the action items. **A check that could not run is reported as skipped and named, never folded into the pass count.**

## Notes

- **Uncommitted changes reach `bin/r-loop` only through `go build -o bin/r-loop ./cmd/r-loop` from the repo root, so run it before every test session.** The binary is the thing under test; if it predates the diff, nothing below means anything.
- **herdr** — `herdr status server` must report a running server. A live run (anything that gets past preflight: the TUI, `todo-tiny.md`, `issues.md`) also needs `HERDR_PANE_ID`, i.e. this session must be inside a herdr pane, because the watchdog opens beside it. With no pane id, record every live check as **NOT RUN (no herdr pane)** and run the no-agent checks only. A live run spends real agent time: the sandbox config uses sonnet with low effort and one review round, so `todo-tiny.md` takes minutes. Run `issues.md` or the full `todo.md` only when the change touches those paths.
- Keep stateful tests clean: point the app at a throwaway state directory for anything that mutates, and don't wipe the user's real config unless that is literally what is being tested.
