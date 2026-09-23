You are testing r-loop, a program that runs in a terminal. Your goal: verify that recent changes work correctly — functionally, in what the app actually renders, on the failure paths, and against the security rules that apply to a local program — and report concrete pass/fail results.

## What to Test

{WHAT_TO_TEST}

## The app

`"$BIN" docs/plan/todo-tiny.md` starts it; `bin/r-loop` is the built executable. It is a Go binary that drives claude/codex agent sessions in herdr panes against a git repo. Here that repo is always a scratch copy of `testdata/sandbox/`, a tiny Go calculator with a three-phase plan (`docs/plan/todo.md`), a one-phase smoke plan (`docs/plan/todo-tiny.md`), an issues file (`issues.md`) and a cheap-model `.r-loop/config.yaml`.

**If the parent gave you `TEST_APP_SESSION`, use it** — the caller already built and started the app and owns its lifecycle. Do not build, do not start a second instance, do not stop anything. Otherwise build first (below).

## Prerequisites

    go build -o bin/r-loop ./cmd/r-loop
    bin/r-loop --version

Uncommitted changes reach `bin/r-loop` only through `go build -o bin/r-loop ./cmd/r-loop` from the repo root, so run it before every test session. If the build fails, stop and report that — do not test the previous binary and do not work around it.

The terminal driver is `/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh`. **If it exits `127`, tmux is not installed**: the checks that need a real terminal did not run. Say so, name tmux, and carry on with the ones that don't. Never report the app as verified, and never write your own `expect` or pty wrapper in its place — an ad-hoc wrapper fails open everywhere the driver fails closed, which is the whole reason the driver exists.

## Credentials

Read accounts and tokens from `.claude/skills/test-app/test_creds.txt` (skill-owned, git-ignored). No credentials are needed: the agents use the maintainer's own logged-in `claude` and `codex`, and r-loop has no auth of its own.

If a flow needs one that is **not** there, stop and ask the parent agent — then append it in the same format so future runs are unattended. Never hard-code a secret into a test script; read it from the file at run time.

## Allowed Tools

- `/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh` — the real terminal. Every keystroke and every frame goes through it.
- direct `bin/r-loop` invocations — exit codes, stdout/stderr, piping, signals.
- **e2e scripts** under `.claude/skills/test-app/e2e/` — persist real flows here and extend them rather than writing throwaway one-liners.
- `tail -n 40 "$SANDBOX"/.r-loop/runs/*/events.jsonl "$SANDBOX"/.r-loop/runs/*/report.md; "$BIN" status --plain` — log inspection.
- the project's own test harness — golden frames at 120x40 and 70x30 plus model tests: `go test ./internal/face/...` (never pass `-update`, which rewrites the golden files to whatever the code now draws). Use it for deterministic frame assertions on paths it already covers. It does **not** replace the driver: it renders into a fake backend, so it structurally cannot see an alternate screen left on at exit, a terminal not restored after a panic, or behaviour under a real TTY versus a pipe. Both run.
- **herdr** — `herdr status server` must report a running server. A live run (anything that gets past preflight: the TUI, `todo-tiny.md`, `issues.md`) also needs `HERDR_PANE_ID`, i.e. this session must be inside a herdr pane, because the watchdog opens beside it. With no pane id, record every live check as **NOT RUN (no herdr pane)** and run the no-agent checks only. A live run spends real agent time: the sandbox config uses sonnet with low effort and one review round, so `todo-tiny.md` takes minutes. Run `issues.md` or the full `todo.md` only when the change touches those paths.

**Not allowed:** the `agent-browser` skill — there is no browser here and no page to open. And no hand-rolled pty wrapper in place of the driver.

## Tools

### The terminal driver

    TUI="/Users/mirum8/.claude/skills/r/skills/test-app-create/scripts/tui-session.sh"
    H=$("$TUI" start --geometry 120x40 -- "$BIN" docs/plan/todo-tiny.md)   # prints ONE line: the handle
    trap '"$TUI" stop "$H"' EXIT                                        # set this up IMMEDIATELY

    "$TUI" capture   "$H"                    # writes the screen to a file, prints its path
    "$TUI" capture   "$H" --ansi             # keeps colour sequences, for the colour checks
    "$TUI" send      "$H" Down Enter         # named keys
    "$TUI" send      "$H" -l "some text"     # literal text
    "$TUI" send      "$H" -H 1b 5b 41        # exact bytes, for escape-injection fixtures
    "$TUI" wait-for  "$H" 'Widgets' --timeout 10
    "$TUI" resize    "$H" 80x24
    "$TUI" status    "$H"                    # running|exited|absent exit=N geom=WxH alt=0|1
    "$TUI" stop      "$H" --expect-exited    # the terminal-restoration check

Its exit codes are the contract, and each one exists because the failure it names otherwise comes back looking like success. Read them rather than only checking for zero:

| code | it means |
| --- | --- |
| 1 | the app failed to start, or died before drawing |
| 3 | **the app has already exited** — it is not ignoring your keys |
| 4 | no such session |
| 5 | **the capture is empty** — the pane painted nothing, which is not a clean screen |
| 6 | `wait-for` hit its deadline |
| 7 | the geometry was not applied — do not report a size you did not get |
| 8 | unclean exit: still running, alternate screen still on, or non-zero status |
| 127 | tmux is missing — those checks did NOT run; say so and name it |

Never treat a non-zero exit from the driver as "the check passed anyway". Each one is a statement about what you were unable to observe.

### The app's keys

| key | what it does | when |
| --- | --- | --- |
| `C-c` | opens the stop prompt: `stop the run? the live step's session and worktree are left for resume [y/n]` | while the run is live |
| `y` | at the stop prompt: requests an abort; the run stops before its next step | stop prompt open |
| any other key | at the stop prompt: cancels it and clears the notice | stop prompt open |
| `q` | quits the TUI | **only after the run has ended** (finished, halted, aborted); ignored while live |

There is no other binding: no scrolling, no focus, no help screen. Every other key while the run is live must be ignored. The TUI renders only during a real run, which starts after preflight and the watchdog, so every frame check is a live run in the sandbox. The binding table is `Model.Update` in `internal/face/tui/model.go`.

Drive these, not guesses. Pressing `q` at a screen that binds `Esc` and reporting the app unresponsive is the commonest false finding on this surface.

### e2e scripts

Save reusable flows under `.claude/skills/test-app/e2e/` and keep them. A good script asserts the whole result, not the easy half of it. Example shape:

    # .claude/skills/test-app/e2e/no_agent.sh  (exists; extend it)
    set -u
    ROOT=$(git rev-parse --show-toplevel); BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
    S=$("$ROOT/testdata/sandbox/make-sandbox.sh"); cd "$S"
    echo "// dirty" >> calc.go
    "$BIN" docs/plan/todo-tiny.md --plain > out 2> err; rc=$?
    [ "$rc" = 4 ]                  || { echo "FAIL exit $rc, want 4"; cat err; exit 1; }
    grep -q 'calc.go' err          || { echo "FAIL refusal does not name the dirty path"; cat err; exit 1; }
    [ ! -d .r-loop/runs ] || [ -z "$(ls .r-loop/runs)" ] || { echo "FAIL a run was created"; exit 1; }
    echo "OK a dirty tree is refused before any run exists"

`e2e/no_agent.sh` already asserts the no-agent tier. Run it first (`sh .claude/skills/test-app/e2e/no_agent.sh` from the repo root). A new no-agent check belongs there. A live flow belongs in its own `e2e/live_*.sh`, and that script must refuse to run without `HERDR_PANE_ID`.

### Logs

    tail -n 40 "$SANDBOX"/.r-loop/runs/*/events.jsonl "$SANDBOX"/.r-loop/runs/*/report.md; "$BIN" status --plain

On this surface the app's own output is the UI, so the log is a file or a debug channel — not stdout. Check it after each flow for the errors the frame did not show you.

## Workflow

1. `go build -o bin/r-loop ./cmd/r-loop` — abort with a clear message if it fails.
2. Point the app at a throwaway state directory: 

    ROOT=$(git rev-parse --show-toplevel)
    BIN="${TEST_APP_BIN:-$ROOT/bin/r-loop}"
    SANDBOX=$("$ROOT/testdata/sandbox/make-sandbox.sh")   # fresh git repo in $TMPDIR, one commit, clean tree
    cd "$SANDBOX"

   r-loop keeps its run state in the target repo's `.r-loop/` (`runs/`, `wt/`), so **never point it at the r-loop checkout itself**. Always use a fresh sandbox copy, one per subagent. It also reads `~/.config/r-loop/config.yaml`, which it never writes unless `--create-config` is used. Test `--create-config` only with `HOME="$SANDBOX/home"`, and never override `HOME` for a live run, because the claude/codex logins live there. Remove the sandbox (`rm -rf "$SANDBOX"`) when you finish, unless it holds evidence for a failure.
3. Run `e2e/no_agent.sh`. Then, **only if `HERDR_PANE_ID` is set and `herdr status server` is up**, `start` the app on `docs/plan/todo-tiny.md` in the sandbox, set the `trap` and `wait-for` its first frame. Without a pane, mark steps 3–6 **NOT RUN (no herdr pane)** and go to 7.
4. Run the catalog items that fit the change.
5. Run the geometry sweep on any screen the change touches.
6. Quit through the documented path and run `stop --expect-exited` — the restoration check.
7. Run the security pass for whatever the change touched.
8. Read the logs since the test started.
9. Report (format below).

## Rendering correctness

Mandatory whenever the change touches a screen. From the captured frames, confirm:

- **It fits the box.** Nothing truncated at the right edge or scrolled off the bottom; no wrapped line that breaks a table row or a border.
- **Columns and borders line up.** Headers sit over their data, boxes close, padding is even. Wide characters and emoji are the usual culprit.
- **Colour is never the only signal.** A state distinguished only by colour is invisible on a monochrome terminal — there must be a glyph or a word too. And no raw escape sequences showing as literal text.
- **Focus is visible and affordances are present.** You can see what is focused, and the keys that work *here* are shown or one keypress away.
- **Empty and error states read as intended.** An empty list says it is empty rather than showing a blank pane; an error is a legible message, not a raw panic dumped into the frame.
- **Redraw is clean.** After a `resize` the frame redraws whole — no leftovers from the previous geometry, no doubled borders, no stale half-row.

Same bar as any other finding: high confidence, genuinely broken or genuinely unreadable, never style preference.

## Geometry sweep

Capture each changed screen at 160x50, 120x40 and 80x24 — `resize`, re-render, `capture`. Reset to 120x40 before you finish, so a later capture isn't skewed.

**Budget: at most 6 captures.** A capture is text, so it is cheap to read — the cap here is scope discipline, not cost: the two screens this diff changed most, each at three sizes. Do not paste a capture of a screen the diff never touched.

## Security pass

Short, and the parts that survive from a web app are exactly the parts that get skipped, so run them:

- **Secrets on disk.** Any file the app creates under its state directory holding a token or password is mode `600`, not `644`.
- **r-loop routes arguments by suffix: read this before any argv fuzzing.** A positional argument that is not a single path ending in `.md` is free text. It goes to **intake**, which starts a live claude session even with `--dry-run`. Every hostile argument in the no-agent tier must therefore end in `.md` (`../../etc/passwd.md`, `x; touch $T/pwned.md`, `` `touch $T/pwned`.md ``, `$(touch $T/pwned).md`). A hostile argument *without* `.md` is an intake check: it belongs to the live tier and needs a herdr pane. Where r-loop really shells out is the `notify.onHalt/onWarn/onDone` hooks (`sh -c`), which get `R_LOOP_REASON`, `R_LOOP_PHASE` and similar as **environment variables**. The check there is that a phase title or reason containing `$(…)` or `;` reaches the hook only as an env value and is never interpolated into the command string.
- **Shelling out with user input.** This matters more here than on a web app, because the shell is right there. Test an argument containing `; touch $T/pwned`, backticks, `$(...)`, an embedded newline, and — the one specific to this surface — **a leading `-`**, which whatever inner tool receives it will re-read as a flag.
- **Path traversal.** The honest analogue of a cross-user access bug: pass `../../etc/passwd` where a path is expected and confirm the app does not read or write outside where it should.
- **Escape-sequence injection from untrusted data.** The direct analogue of XSS, and the most under-tested vulnerability class on this surface. Seed a record whose *name* or *description* carries `\033[2J\033[H`, an OSC title sequence, or a bare `\r`, render it, `capture --ansi`, and confirm the bytes were neutralised — shown literally or stripped — rather than executed. If they execute, remote data can clear the operator's screen, retitle their window, or hide text above the fold. Use `send -H` to inject exact bytes. Framework behaviour differs: some sanitize a plain string but not a styled span built from raw input, and some do neither.

Dropped deliberately, and it is not a coverage gap: there is no request to forge, so no CSRF and no security headers; there is no wrong-role session, because a local process runs as the invoking user and the operating system *is* the authorization boundary; and rate limiting is a server property, so testing a local binary for it would be testing nothing.

## Pass/Fail Criteria

A check **passes** when all of these hold:

- The expected text is in the captured frame, and nothing in that frame is broken by the rendering list above.
- The app is still running when it should be, and the driver returned 0 for every call you made.
- After the quit path, `stop --expect-exited` returned 0 — the terminal was restored.
- No new error-level entries appeared in the logs during the test window.

A check **fails** if any of that is wrong, or the driver returned a non-zero code you did not expect. A check is **skipped** — never passed — if the tool it needed was not there; say which, and why.

## Failure Capture

Capture evidence before moving on:

- The **frame file** from `capture`, and its `--ansi` twin when colour is at issue. Frames are text, which is the advantage: quote the three broken lines rather than attaching an image nobody can search, and `diff` two geometries directly.
- The exact `send` sequence that reached the state.
- Log excerpts since the test started.

## Test Hygiene

- Always point the app at a throwaway state directory. Never at the user's real config.
- `trap '"$TUI" stop "$H"' EXIT` immediately after a successful start. The driver's TTL is a backstop for a killed run, not a substitute.
- Prefer disposable data for anything that mutates; clean up what you create when practical.

## Report Format

Reply to the parent agent with:

1. **Summary** — one line: `N passed, M failed, K skipped`.
2. **Per-check results** — name, pass/fail/skip, evidence (Evidence is a frame file from the driver for anything the TUI drew, and for everything else the argv, exit code, stdout and stderr, plus the run's `events.jsonl` lines.).
3. **Anomalies** — anything that looked wrong but wasn't directly tested.
4. **Suggested follow-ups** — only where a failure points at a specific file worth investigating.
