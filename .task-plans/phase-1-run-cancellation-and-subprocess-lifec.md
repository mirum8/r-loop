status: planned

## Summary

This phase adds one cancellation path, from a signal, a display that exits or an abort marker, down to the gate's process group. It also puts a bound on every subprocess the driver starts.

- `app.Execute` catches SIGINT, SIGTERM and SIGHUP itself and cancels the run context with a cause that names the signal. Bubble Tea's own signal handler is turned off. When the TUI program exits, a new `tui.Face.OnExit` callback cancels the same context.
- `RunLoop` reacts to a cancelled context everywhere it waits: in a step, in the remedy window, in the phase check and in land. The last two now run under a small `guarded` helper that also polls the abort marker. The helper records `halted/aborted` before it cancels anything. An interrupted run is recorded `halted` with the reason `interrupted: <cause>` and exits 4, which is the watchdog's answer to q1 (spec.html:3675, the same code as an interrupted triage). An abort still exits 1.
- `Repo.MergeNoFF` takes a `context.Context`, so a signal or an abort during a blocked merge (a hook, LFS filter or credential prompt) kills the git process group at once and puts the primary tree back at the pre-merge HEAD.
- `Repo.Run` takes a `context.Context`. It kills the gate's process group on cancel and no longer treats `exec.ErrWaitDelay` as an error. It always kills whatever is left in the group once the command has exited. `notify.Shell` gets the same `ErrWaitDelay` fix.
- Every git call made through `gitrepo` and `store.EnsureExcluded` runs with `GIT_TERMINAL_PROMPT=0`. It runs in its own process group, and that group is killed on a timeout. Every herdr CLI call runs under a per-call timeout. A timed-out call returns an error that names the command and the timeout.

Choices:
- **Where signals are caught:** `app.Execute`, not `cmd/r-loop` main. `Execute` is the one path shared by a new run and `resume`, and it holds the run context and the TUI.
- **Timeouts on git calls:** a package-level timeout inside `gitrepo`, not a `ctx` added to all 20 `Repo` methods. Only the three primary-tree calls that a signal during land must be able to cut short take a `ctx`. `Repo.Run` covers the gate, probe and red check. `Repo.MergeNoFF` covers a merge that a signal interrupts, after which the tree must be clean (#1). `Repo.Commit` covers the landing commit, whose hooks can block. Every other git call is bounded by the timeout and runs in a step's worktree, or is a quick read or cleanup.
- **Abort during land and the phase check:** a `guarded` helper around those two calls, not one abort-poller goroutine for the whole run. The existing abort tickers in `runStep`/`awaitRestart` and their "record halted before cancelling" ordering (tech-design.md:340) stay as they are.
- **Phase check made cancellable:** `PhaseCheck.Run` stops waiting when `ctx` ends, not a `ctx` added to `SessionHost.Prompt`. Adding it would change the port and all its fakes; the abandoned herdr call ends on its own timeout.
- **How the interrupt reaches the faces:** a `halt` event carrying `reason` and `resume`, not a new event kind. Both faces already render `halt` (tui/model.go:215), so neither face changes for it.
- **Background children (#16):** judge the exit code when `Wait` returns `ErrWaitDelay` and keep `WaitDelay`. Dropping `WaitDelay` would let a child holding stdout block the run forever.
- **How the TUI's exit reaches `Execute`:** an `OnExit func(error)` field on `tui.Face`, not a `Done()` channel that `Execute` would have to watch from a goroutine.

## Changes

1. **`internal/core/types.go`** (modify). Add `const ReasonInterrupted = "interrupted"` next to `ReasonAborted` (types.go:136). Serves #1 (the interrupted reason).

2. **`internal/core/ports.go`** (modify). Three `Repo` methods change signature:
   - `Repo.Run` becomes `Run(ctx context.Context, dir, command string, timeout time.Duration) (int, string, error)` (ports.go:75). Serves #1, #11 and #16 (killing the gate's process group).
   - `Repo.MergeNoFF` becomes `MergeNoFF(ctx context.Context, branch string) error` (ports.go:70). Serves #1 (a signal during the land merge leaves the primary tree clean).
   - `Repo.Commit` becomes `Commit(ctx context.Context, message string) (string, error)` (ports.go:72). Serves #1 (a signal during the landing commit, blocked in a hook, ends it at once).

3. **`internal/gitrepo/repo.go`** (modify).
   - Add `var gitTimeout = 10 * time.Minute`.
   - `run` (repo.go:38) uses `exec.CommandContext(ctx, "git", ...)` with `ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)`. It sets `cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)` always, `SysProcAttr{Setpgid: true}`, `cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }` and `cmd.WaitDelay = time.Second`. This copies notify.go:35-47.
   - When `run` fails and `ctx.Err()` is `context.DeadlineExceeded`, it returns `fmt.Errorf("git %s: timed out after %s: %w", strings.Join(args, " "), gitTimeout, context.DeadlineExceeded)`, so callers can tell a timeout with `errors.Is`. Otherwise it returns the existing error format.
   - The body described in the two bullets above lives in a new `runCtx(parent context.Context, dir string, env []string, args ...string) (string, error)`, which derives the timeout context from `parent`. `run(dir, env, args...)` is `runCtx(context.Background(), dir, env, args...)`, so every existing caller is unchanged. When `parent` has ended, `runCtx` returns `fmt.Errorf("git %s: interrupted: %w", strings.Join(args, " "), parent.Err())`.
   - `MergeNoFF(ctx, branch)` (repo.go:310):
     - It first reads `pre, err := r.HeadSHA("")` and returns that error if there is one.
     - It runs the merge through `runCtx(ctx, r.root, nil, "merge", "--no-ff", "--no-commit", branch)`.
     - When the merge fails and either `ctx.Err() != nil` or `errors.Is(mergeErr, context.DeadlineExceeded)` (its own 10-minute timeout killed it), it calls `r.AbortMerge()`. If that fails, it calls `r.ResetHard(pre)`. It returns `errors.Join(mergeErr, <the reset error, if any>)`, whose text says `interrupted` or `timed out after`.
     - Otherwise the conflict branch (repo.go:315-326) stays as it is.
   - Serves #1 (a signal during the merge: the git group is killed and the tree is back at the pre-merge HEAD) and #2 (a merge that times out leaves the tree at the pre-merge HEAD too).
   - `Commit(ctx, message)` (repo.go:334): the `commit` call (repo.go:338) runs through `runCtx(ctx, r.root, identity, "commit", "-q", "-m", message)`. `add -A` and `HeadSHA` stay on `run`. Serves #1.
   - `Run` (repo.go:354) takes `ctx` as its first parameter:
     - If `ctx.Err() != nil` before starting, it returns `-1, "", fmt.Errorf("interrupted: %w", ctx.Err())` without starting the command.
     - It sets `cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")`, because the gate, the red check and `headMoved` run git through `sh`.
     - It registers `stop := context.AfterFunc(ctx, kill)`, where `kill` is `syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)`. Right after `cmd.Wait()` returns it calls `timer.Stop()`, `stop()` and `kill()`, so background children left in the group are killed on every exit path.
     - It keeps the timed-out branch unchanged. After that branch, if `ctx.Err() != nil` it returns `-1, output, fmt.Errorf("interrupted: %w", ctx.Err())`.
     - It treats `errors.Is(err, exec.ErrWaitDelay)` like an `*exec.ExitError`: it returns `cmd.ProcessState.ExitCode(), output, nil`.
   - Serves #1 (gate child killed on a signal), #2 (git bounded, prompt off, process killed) and #16 (a child that holds stdout: exit 0 lands, a non-zero exit keeps its code, never blocked past `WaitDelay`).

4. **`internal/store/store.go`** (modify). `EnsureExcluded` (store.go:310) runs `git rev-parse --git-common-dir` the same way as change 3:
   - `var excludeTimeout = time.Minute` and `exec.CommandContext` with that timeout.
   - Env gets `GIT_TERMINAL_PROMPT=0`; `Setpgid`, `Cancel` kills the group, `WaitDelay = time.Second`.
   - On a deadline it returns `git rev-parse --git-common-dir: timed out after <excludeTimeout>`.
   - Serves #2 ("every git invocation").

5. **`internal/herdr/client.go`** (modify).
   - Add `var callTimeout = 30 * time.Second`, next to `paneBusyBudget` (client.go:20).
   - `exec(args...)` (client.go:37) becomes a wrapper over a new `execWithin(limit time.Duration, args ...string) ([]byte, error)`. That function builds `exec.CommandContext` with `context.WithTimeout(context.Background(), limit)`, `Setpgid`, a `Cancel` that kills the group and `WaitDelay = time.Second`, as notify.go:45-47 does.
   - When the deadline passed, `execWithin` returns `fmt.Errorf("herdr %s: timed out after %s", strings.Join(args[:min(len(args), 3)], " "), limit)`. The rest of the existing error mapping stays the same.
   - `call(out, args...)` (client.go:66) becomes a wrapper over `callWithin(limit, out, args...)`, which calls `execWithin`.
   - `Prompt` (client.go:250) with `wait` calls `callWithin(callTimeout+timeout, ...)`, so a waiting prompt (the phase check, phasecheck.go:37) is never cut short at 30 s.
   - Serves #2 (herdr timeout, the error names the command and the timeout).

6. **`internal/notify/notify.go`** (modify). In `Fire` (notify.go:48), when `errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState.Success()`, return as a success. Serves #16 (notify hook).

7. **`internal/core/land.go`** (modify).
   - `attempt` passes `ctx` to `g.Repo.MergeNoFF` (land.go:82) and to `g.Repo.Run` for the gate (land.go:105). A cancelled run returns a non-`ErrGate` error, which is already joined with `g.Repo.AbortMerge()` (land.go:110), so the fix loop at land.go:50 does not retry it.
   - Before `g.Plan.Tick` (land.go:114), `if err := ctx.Err(); err != nil { return Landing{}, "", "", errors.Join(fmt.Errorf("interrupted: %w", err), g.Repo.AbortMerge()) }`, so a stop that arrives as the gate passes never ticks or commits.
   - `g.Repo.Commit` (land.go:117) gets `ctx`. When a cancel kills a blocked commit, the existing `ResetHard("HEAD")` (land.go:119) clears the merge, so no landing commit is made and the tree is clean.
   - `redAtBase` becomes `redAtBase(ctx context.Context, phase Phase, item string)`. The setup (land.go:167) and the red run (land.go:170) get `ctx`. The deferred cleanup (land.go:166) gets `context.Background()`, so the red worktree is removed even after a cancel.
   - Serves #1 and #11 (the merge is reverted when a signal or abort arrives during the gate).

8. **`internal/core/gate.go`** (modify). `discover` passes `ctx` to `p.Repo.Run` (gate.go:127). Serves #11 (abort during the gate probe).

9. **`internal/core/session.go`** (modify) and **`internal/core/checks.go`** (modify). `headMoved` (session.go:479) and the check at checks.go:222 pass `context.Background()`. Both are already bounded by their 1 min and 30 s timeouts. This only follows the signature change.
   - `session.go`: add `func timedOut(err error) bool { return err != nil && strings.Contains(err.Error(), "timed out after") }`, following `blocked` (watchdog.go:353), which matches herdr errors by text the same way.
   - `tick` (session.go:338): `state, err := m.Host.State(s.Agent)` (session.go:348) and, right after it and before the `OpenQuestion` pause (session.go:352), `if timedOut(err) { return m.fail(s, err.Error()), true }`. A timed-out `State` fails the step with the herdr error, which names the command and the timeout. Any other `State` error keeps being ignored, and the backstop still covers it. Because this check comes before the pause, a step whose answer `hand` (loop.go:794) cannot deliver while herdr is wedged also fails, and `withdrawStep` (loop.go:848) then ends `hand`'s retry loop.
   - `internal/core/milestone.go:85` and `internal/app/unblock.go:93` pass their `ctx` to `Repo.Commit`.
   - Serves #2 (a timed-out herdr call becomes a step failure naming it, also while a question is open).

10. **`internal/core/phasecheck.go`** (modify). `PhaseCheck.Run` (phasecheck.go:30) runs `c.Dog.Notify(...)` in a goroutine that sends to a 1-buffered `errc`. It then `select`s on `errc` and `ctx.Done()`. On `ctx.Done()` it returns `CheckOutcome{Kind: phaseCheckSkipped, Reason: "interrupted: " + ctx.Err().Error()}`. Serves #11 (a phase-check wait takes effect within the poll interval).

11. **`internal/core/loop.go`** (modify).
    - Add a field `stopReason string`, guarded by `l.mu`.
    - Add `recordStop(reason string)`: under `l.mu`, return if `stopReason != ""`, otherwise set it; then call `l.setRun(RunHalted, reason)`. The halted record is written once whichever path sees the stop first.
    - Replace `abort(phase, step)` (loop.go:951) with `stop(ctx context.Context, phase, step string)`:
      - If `l.Store.Aborted(l.RunID)`: `recordStop(ReasonAborted)`, then `announceAbort(phase, step)`.
      - Otherwise: `reason := interruptReason(ctx)`, `recordStop(reason)`, then `l.emit(Event{Kind: "halt", Phase: phase, Step: step, Fields: {"reason": reason, "resume": "r-loop resume"}})`.
      - It fires no hook, the same as abort today.
    - Add `interruptReason(ctx) string`: `ReasonInterrupted`, followed by `": " + cause.Error()` when `context.Cause(ctx)` is neither nil nor `context.Canceled`.
    - Add `guarded(ctx context.Context, fn func(context.Context)) bool`:
      - Runs `fn` in a goroutine with a `context.WithCancel(ctx)` child and polls on a `time.NewTicker(l.poll())`.
      - On `done` it returns `ctx.Err() != nil`.
      - On `ctx.Done()` it waits for `done` and returns true.
      - On a tick where `l.Store.Aborted(l.RunID)` is true, it calls `recordStop(ReasonAborted)`, cancels the child, waits for `done` and returns true. The record is written before the cancel, as runStep does at loop.go:646.
    - `checkPhase` (loop.go:330) returns `bool`. It computes `out` inside `l.guarded(ctx, func(ctx) { out = l.watcher().BeforePhase(ctx, ph, base) })` and returns true at once when that stopped. It emits nothing after a stop.
    - `runPhase` (loop.go:235): `if l.checkPhase(ctx, ph, base) { l.stop(ctx, n, "check"); return "check", Outcome{}, true }`.
    - Land in `runPhase`:
      - The pre-land check (loop.go:284) becomes `if l.Store.Aborted(l.RunID) || ctx.Err() != nil { l.stop(ctx, n, "land"); return "land", Outcome{}, true }`.
      - Both `lander.Land` calls (loop.go:288 and 306) run through `l.guarded(ctx, func(ctx) { landing, err = lander.Land(ctx, ph) })`. When that returns true: `l.stop(ctx, n, "land"); return "land", Outcome{}, true`. This is checked before the `FailedStep` handling.
    - `runStep` (loop.go:585):
      - The start check becomes `if l.Store.Aborted(l.RunID) || ctx.Err() != nil { l.stop(ctx, ...); return Outcome{}, true }`.
      - Add a helper `stopStep(ctx, ref, out) (Outcome, bool)` that runs `emitStep`, `watcher().StepEnded`, `withdrawStep` and `l.stop(ctx, phase, kind)`, then returns `out, true`.
      - In the `done` case, when `ctx.Err() != nil`, return `l.stopStep(ctx, ref, out)`.
      - Add a `case <-ctx.Done():` that does `cancel(); out := <-done; return l.stopStep(ctx, ref, out)`.
      - In the abort ticker case, `l.setRun(RunHalted, ReasonAborted)` becomes `l.recordStop(ReasonAborted)` and the tail becomes `return l.stopStep(ctx, ref, <-done)` after `cancel()`.
    - `awaitRestart` (loop.go:450): the `ctx.Done()` case and the abort tick both call `l.stop(ctx, key.Phase, key.Kind)` and return `StepRef{}, false, true`.
    - Add `stopCode() int`: under `l.mu`, `4` when `strings.HasPrefix(l.stopReason, ReasonInterrupted)`, otherwise `1`.
    - `Run` (loop.go:159): `if aborted { return l.stopCode() }`. The invariant-question path keeps exit 1.
    - `Run`, right after `runPhase` returns with `aborted == false` (loop.go:159) and before the `out.State == StepOK` check and `l.block`: `if l.Store.Aborted(l.RunID) || ctx.Err() != nil { l.stop(ctx, ph.ID, stopStep); return l.stopCode() }`, where `stopStep` is the returned `step`, or `"land"` when it is empty. This catches a stop that raced a step's end, the remedy timer, a guarded land or phase check that finished before its next poll tick, or the last phase's return: the run never blocks the phase, emits `finished` or exits 0 or 1 in place of the stop. A phase that already landed stays landed (its commit exists); only the run stops.
    - Serves #1 (halted with an interrupted reason, exit 4, never keeps running), #11 (abort during land, gate-fix, gate probe, milestone report and phase check within the poll interval, exit 1) and #2 (an abort still works while host calls time out).

12. **`internal/face/tui/model.go`** (modify).
    - Add `aborting bool` to `Model`. `confirmStop` sets `m.aborting = true` after a successful `m.abort()`.
    - In `Update` (model.go:405), add before `case m.stopping:`: `case msg.Type == tea.KeyCtrlC && (m.stopping || m.aborting):`. This is independent of `m.Status`, so it still applies after the `aborted` event has set `Status` to `halted`. A halted run where no abort was requested keeps ignoring ctrl+c (model_test.go:634). It calls `m.abort()` once when `m.Status == "" && !m.aborting && m.RunID != "" && m.abort != nil`, and ignores the error, because the context then records an interrupted halt (see Assumptions). Then it returns `m, tea.Quit`.
    - `Face` gets `OnExit func(error)`. `Start` adds `tea.WithoutSignalHandler()` to `tea.NewProgram` (model.go:442). The goroutine does `err := prog.Run(); if f.OnExit != nil { f.OnExit(err) }` before `close(f.done)`, with `prog` captured locally.
    - Serves #11 (force-quit) and #1 (the display exiting stops the run; the only signal handler is the driver's).

13. **`internal/app/wire.go`** (modify). `Execute` (wire.go:256):
    - Creates `ctx, cancel := context.WithCancelCause(context.Background())`.
    - Sets up `sigs := make(chan os.Signal, 1)`, `signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)`, `quit := make(chan struct{})` and `defer func() { signal.Stop(sigs); close(quit) }()`. A goroutine loops `select { case s := <-sigs: ...; case <-quit: return }` — it watches `quit`, not `ctx.Done()`, so it keeps handling signals through watchdog cleanup and `Face.Close`. On each signal it calls `cancel(errors.New(signalName(s)))` (the first cause wins) and, when `w.TUI != nil`, `w.TUI.Stop()`. `Stop` quits the program, so a `Face.Close` waiting for `q` on the end screen returns. `signalName` maps the three signals to `"SIGINT"`, `"SIGTERM"` and `"SIGHUP"`.
    - Signals stay captured until `Execute` returns, so a later signal during shutdown only repeats the idempotent cancel and `Stop`, and the process is never killed outright.
    - When `w.TUI != nil` it sets `w.TUI.OnExit = func(err error) { cancel(displayExit(err)) }`. `displayExit(nil)` is `errors.New("display closed")`; otherwise it is `fmt.Errorf("display exited: %w", err)`.
    - After `w.run` returns: `if w.TUI != nil && context.Cause(ctx) != nil { w.TUI.Stop() }`, which covers a display that exited on its own. The existing `cancel()` becomes `cancel(nil)`.
    - Serves #1 (signals during any step or land, the TUI's `Run` error or exit halts the run) and #11 (force-quit reaches the loop as a cancel after the abort marker).

14. **Test fakes**:
    - `internal/core/fakes_test.go`: `fakeRepo.Run` (fakes_test.go:259), `fakeRepo.MergeNoFF` (fakes_test.go:234) and `fakeRepo.Commit` (fakes_test.go:244) get the new signatures.
    - `internal/gitrepo/repo_test.go:331`: `r.Commit` passes `context.Background()`.
   - `internal/gitrepo/repo_test.go`: every existing `MergeNoFF(branch)` call passes `context.Background()`.
    - `internal/core/session_test.go`: `scriptedHost` gets a `StateErr error` field that `State` (session_test.go:30) returns.
    - `internal/herdr/testdata/herdr` starts with `if [ -n "$HERDR_SLEEP" ]; then sleep "$HERDR_SLEEP"; fi`.
    - `internal/gitrepo/repo_test.go`: `TestRunExitAndOutput` and `TestRunTimesOut` (repo_test.go:404 and 416) pass `context.Background()`.

15. **`docs/task-loop-driver/tech-design.md`** (modify). The shared contract follows the code; five edits, nothing else in the file changes:
    - `Repo` port, `Commit` (tech-design.md:127): `Commit(message) (string, error)` becomes `Commit(ctx, message) (string, error)`; append to its parenthesis `; the commit is killed when ctx ends`.
    - `Repo` port, `MergeNoFF` (tech-design.md:125): `MergeNoFF(branch) error` becomes `MergeNoFF(ctx, branch) error`. Append to its parenthesis: `; when ctx ends, git's process group is killed and the primary tree is put back at the pre-merge HEAD, returning an error that says interrupted`.
    - `Repo` port, `Run` (tech-design.md:129-130): `Run(dir, command string, timeout)` becomes `Run(ctx, dir, command string, timeout)`. The parenthesis `(via `sh -c`)` becomes `(via `sh -c`, in its own process group with `GIT_TERMINAL_PROMPT=0`; the group is killed when ctx ends, on the timeout, and once the command exits; a command that exits while a background child still holds its output is judged by its exit code, never by the wait delay)`. After the `Repo` bullet's closing sentence, add one sentence: `Every other git call runs with GIT_TERMINAL_PROMPT=0 in its own process group, killed after a 10-minute per-call timeout with an error naming the command and the timeout; every herdr CLI call has a 30 s per-call timeout (a waiting Prompt gets its wait on top), with the same error shape.`
    - **Outcomes** (tech-design.md:342-343): after `Abort marker → exit `1` at once;` insert `SIGINT, SIGTERM or SIGHUP during a step, the remedy window, the phase check or land, or the TUI program exiting (its own exit or an error from Run) → the run is recorded `halted` with `interrupted: <signal | display closed | display exited: <err>>`, a `halt` event with that reason and `r-loop resume`, no hook, exit `4`; an abort or interrupt during land kills the gate's process group and aborts the merge, so the primary tree is clean;`. The existing `preflight refusal `4`` stays.
    - **Stop** (tech-design.md:506-508): after `any other key cancels.` insert `A second ctrl+c while the prompt is up, or any ctrl+c after `y` (even once the run shows halted), force-quits: it marks the run aborted if that was not yet done, quits the program and restores the terminal; the run exits 1. The driver owns SIGINT/SIGTERM/SIGHUP — the TUI installs no signal handler of its own.`
    - Serves codex-r1-3 (round-1 finding: the shared contract must match the `Repo.Run`/`MergeNoFF` signatures, the interrupted exit 4 and the two-stage ctrl+c). No test: it is prose, checked by the reviewers.

## Tests

Tests in `internal/gitrepo/repo_test.go`:
- `TestMergeNoFFCancelledMidMergeLeavesThePreMergeHead`:
  - Setup: the branch adds `a.slow`. `.gitattributes` on both sides has `*.slow filter=slow`, and repo config sets `filter.slow.clean=cat` and `filter.slow.smudge=sh -c 'echo $$ > <pid>; sleep 300'`. The smudge therefore blocks the merge's checkout.
  - The ctx is cancelled once `<pid>` exists.
  - `MergeNoFF` returns within 5 s with an error containing `interrupted`, and the smudge pid is gone within 2 s.
  - `git status --porcelain` is empty, `MERGE_HEAD` is absent and HEAD equals the pre-merge HEAD.
  - Covers #1 (a signal during the land merge leaves the primary tree clean).
- `TestMergeNoFFTimedOutLeavesThePreMergeHead`: the same slow-smudge setup, with `gitTimeout = 2s` restored in `t.Cleanup` and no cancel. `MergeNoFF` returns within 10 s with an error containing `git merge --no-ff --no-commit` and `timed out after 2s`, the smudge pid is gone within 2 s, `git status --porcelain` is empty, `MERGE_HEAD` is absent and HEAD equals the pre-merge HEAD. Covers codex-r2-2 (#2 and #1: a timed-out merge leaves the tree resumable).
- `TestCommitCancelledInAHangingHookMakesNoCommit`: a `pre-commit` hook `echo $$ > <pid>; sleep 300`, a staged change, and the ctx cancelled once `<pid>` exists. `Commit` returns within 5 s with an error containing `interrupted`, the hook pid is gone within 2 s, and HEAD is unchanged. Covers codex-r2-1 (#1: a signal during the landing commit).
- `TestRunThatPassesButLeavesABackgroundChildExitsZero`: the command is `echo $$ > <pid>; sleep 30 & echo ok`. `Run` returns `0`, output containing `ok` and a nil error within 5 s, and `syscall.Kill(-pgid, 0)` reports ESRCH within 2 s. Covers #16 (exit 0 with a child holding stdout, not blocked past the wait delay, the child killed).
- `TestRunThatFailsAndLeavesABackgroundChildKeepsItsExitCode`: `sleep 30 & exit 3` returns `3` and a nil error within 5 s. Covers #16 (a non-zero exit keeps its code).
- `TestRunCancelledKillsTheProcessGroup`: `echo $$ > <pid>; sleep 300`, with the ctx cancelled once the pid file exists. `Run` returns within 5 s with `errors.Is(err, context.Canceled)` and exit `-1`, and the group is gone. Covers #1 and #11 (the gate's process group is killed on cancel).
- `TestRunWithAnEndedContextStartsNothing`: with the ctx already cancelled, `touch <marker>` returns an error and the marker does not exist. Covers a signal that arrives during the merge, before the gate starts.
- `TestRunCommandsSeeTerminalPromptOff`: `echo $GIT_TERMINAL_PROMPT` outputs `0`. Covers #2 (git run through `sh`).
- `TestAHangingGitCallIsKilledAfterTheTimeoutNamingTheCommand`: `gitTimeout = 300ms`, restored in `t.Cleanup`, and a `pre-commit` hook `echo $$ > <pid>; sleep 300`. `CommitAll` returns within 5 s with an error containing `git commit` and `timed out after 300ms`, and the hook's pid is gone within 2 s. Covers #2 (bounded, killed not leaked, the error names the command and the timeout).
- `TestEveryGitCallRunsWithTerminalPromptOff`: a `pre-commit` hook writes `$GIT_TERMINAL_PROMPT` to a file, then `CommitAll` runs. The file holds `0`. Covers #2.

Tests in `internal/store/store_test.go`:
- `TestEnsureExcludedRunsGitWithTerminalPromptOff`: a fake `git` on `PATH` records `$GIT_TERMINAL_PROMPT` and prints `.git`. The recorded value is `0`. Covers #2 (every git invocation).
- `TestEnsureExcludedGivesUpOnAHangingGit`: `excludeTimeout = 200ms` and a fake `git` that runs `sleep 300`. The error contains `timed out after 200ms` and arrives within 5 s. Covers #2.

Tests in `internal/herdr/client_test.go`:
- `TestAWedgedHerdrCallFailsAfterTheCallTimeoutNamingTheCommand`: `callTimeout = 200ms` and `HERDR_SLEEP=300`. `c.State("a1")` fails within 5 s with an error containing `herdr agent get a1` and `timed out after 200ms`. Covers #2 (a timeout on every call; a wedged fake binary; the error names the command and the timeout).
- `TestAWaitingPromptGetsItsWaitOnTopOfTheCallTimeout`: `callTimeout = 200ms` and `HERDR_SLEEP=0.5`. `c.Prompt("a1", "x", true, 2*time.Second)` returns nil. Covers the phase-check prompt (phasecheck.go:37) not being cut at the call timeout.

Tests in `internal/notify/notify_test.go`:
- `TestAHookThatExitsZeroButLeavesABackgroundChildIsNotAFailure`: the hook is `sleep 30 &`. It emits no `notify-failed` event, writes no log entry and returns within 5 s. Covers #16 (notify).
- `TestAHookThatFailsAndLeavesABackgroundChildIsStillAFailure`: the hook is `sleep 30 & exit 3`. It emits one `notify-failed` event whose reason contains `exit status 3`. Covers #16 (no hook failure is swallowed).

Tests in `internal/core/land_test.go`:
- `TestLandGateCancelledWhileTheGateRunsKillsItsGroupAndAbortsTheMerge`: the gate is `echo $$ > <pid>; sleep 300`, with the ctx cancelled once the pid exists. `Land` returns within 5 s with an error that is not `ErrGate`. The group is gone, `e.assertUntouched(head)` passes and `MERGE_HEAD` is absent. Covers #1 and #11 (gate killed, merge reverted, primary tree clean).
- `TestLandCancelledAsTheGatePassesNeitherTicksNorCommits`: the gate is `true`. `LandGate.Repo` is a test type that embeds the real repo and overrides `Run` to call the embedded `Run`, then `cancel()` the land ctx, and return the embedded result. `Plan` is the env's `tickPlan` (land_test.go:55). `Land` returns an error containing `interrupted`, `tickPlan.ticks` is empty, `e.assertUntouched(head)` passes and `MERGE_HEAD` is absent. Covers codex-r2-1 (#1: no tick or commit after a cancel).
- `TestLandPassingGateAndCancelledDuringAHangingCommitHookLeavesACleanTree`: a `pre-commit` hook `echo $$ > <pid>; sleep 300` in the primary tree, the gate `true`, and the ctx cancelled once `<pid>` exists. `Land` returns within 5 s with an error that is not `ErrGate`, the hook pid is gone within 2 s, `e.assertUntouched(head)` passes and `MERGE_HEAD` is absent. Covers codex-r2-1 (#1: no landing commit, a clean tree).
- `TestLandGatePassingWithABackgroundChildLands`: the gate is `sleep 30 & echo green`. `Land` succeeds with `MergeSHA == e.head()` within 5 s. Covers #16 (lets the phase land).
- `TestLandGateRedWithABackgroundChildIsAGateFailureWithItsExitCode`: `sleep 30 & exit 3` gives `errors.Is(err, core.ErrGate)`, and the message contains `exited 3`. Covers #16.
- `TestLandGateRedWithABackgroundChildGetsAGateFixRound`: `FixRounds = 1`, `Runner = fixingRunner`, and the gate is `test -f fix1.txt || { sleep 30 & exit 3; }`. The phase lands with one `gate-fix` event for round 1. Covers #16 (gate-fix rounds run).

Tests in `internal/core/loop_test.go`:

These three tests share a helper, `landGate(r *loopRig) *LandGate`:
- It builds `LandGate{Repo: r.repo, Plan: &fakePlanSource{}, Store: r.store, Face: r.face, RunID: "run-1", TodoPath: "docs/x/todo.md", GateTimeout: time.Minute}`.
- It sets `r.repo.Touched = []string{"docs/x/todo.md", "code.go"}`.
- It assigns the gate to `r.loop.Lander`.

In each test, the named agent has behaviour `abort`: it marks the abort and never writes a sentinel.
- `TestAnAbortDuringAGateFixRoundStopsTheRun`:
  - Setup: phase 1 has `DoneWhen` `` `go test ./x` `` and `r.repo.RunExit = 1`. The gate has `FixRounds = 1`, `FixKind` gatefix (check `diff`), and `Runner = DefaultRunners(r.loop.Sessions, []StepKind{fixKind})["diff"]`. `rloop-p1-gatefix` has behaviour `abort`.
  - Run with `--phases 1`: exit `1` within 2 s. There is exactly one `Repo.MergeNoFF` call, so no later land attempt. There is one `aborted` event with `Step == "land"`.
  - Covers #11 (gate-fix round).
- `TestAnAbortDuringTheGateProbeStopsTheRun`:
  - Setup: `Suite = &GateProbe{Sessions: r.loop.Sessions, Repo: r.repo, Kind: StepKind{Name: "gate", Prompt: "gate", Check: "diff", Row: StepRow{Provider: "codex", Timeout: time.Hour}}, Plan: r.loop.Plan, RunID: "run-1", RunDir: r.store.dir, Face: r.face, Timeout: time.Minute}`. `rloop-p1-gate` has behaviour `abort`.
  - Run with `--phases 1`: exit `1` within 2 s. There is no `Repo.MergeNoFF` call, and one `aborted` event with `Step == "land"`.
  - Covers #11 (gate probe).
- `TestAnAbortDuringTheMilestoneReportStopsTheRun`:
  - Setup: `Boundary = &MilestoneBoundary{Plan: r.loop.Plan, Sessions: r.loop.Sessions, Repo: r.repo, Kind: StepKind{Name: "milestone", Prompt: "milestone", Check: "diff", Row: StepRow{Provider: "codex", Timeout: time.Hour}}, Topic: "x", RunDir: r.store.dir, RunID: "run-1", Face: r.face}`. `rloop-p3-milestone` has behaviour `abort`.
  - Run all phases: exit `1` within 2 s. The last run record is `halted/aborted`, and there is one `aborted` event with `Step == "land"`.
  - Covers #11 (milestone report).
- `TestAnAbortWhileLandRunsCancelsItAndRecordsTheAbortOnce`: a lander marks the abort, then waits on `ctx.Done()` or 5 s and records whether it was cancelled. The run exits `1` within 2 s, and the lander was cancelled. Exactly one run record has `Reason == ReasonAborted` and it is the last one. There is one `aborted` event with `Step == "land"`. Covers #11 (abort during land, recorded aborted, exit 1).
- `TestAnInterruptMidStepHaltsTheRunAsInterruptedWithExit4`: a plan runner calls `cancel(errors.New("SIGTERM"))` on a `context.WithCancelCause`, waits for `ctx.Done()` and returns failed. The run exits `4`. The last run record is `halted` with `interrupted: SIGTERM`, and there is one `halt` event with that reason and `resume`. There is no `phase-blocked` event, no `Notifier.Fire` call, and no session is opened for the other phases. Covers #1 (any step).
- `TestAnInterruptDuringLandHaltsTheRunAsInterrupted`: a lander cancels the run ctx and waits on `ctx.Done()` or 5 s. The run exits `4`, there is a `halt` event with `Step == "land"`, and the last run record is `halted` with prefix `interrupted`. Covers #1 (in land).
- `TestAnAbortStopsAStepWhileEveryHostStateCallFails`: a host that wraps `agentSim` returns an error from `State` (`herdr agent get x: timed out after 30s`). Implement has behaviour `abort`. The run exits `1` with the last run record `halted/aborted`. Covers #2 (abort still works after herdr timeouts).
- `TestAnAbortMarkedByALandThatThenSucceedsStillStopsTheRun`: one phase; the lander marks the abort and returns a successful `Landing` at once, before any poll tick. The run exits `1`, the last run record is `halted/aborted`, there is one `aborted` event and no `finished` event. Covers codex-r1-1 (#11: an abort during land is recorded aborted with exit 1 even when land wins the race).
- `TestAnInterruptAsTheLastStepEndsHaltsInsteadOfBlocking`: `RemedyWindow = 0`, one phase, implement fails, and `watcher.ended` cancels the run ctx with `errors.New("SIGTERM")` (so no wait sees it). The run exits `4`, the last run record is `halted` with `interrupted: SIGTERM`, and there is no `phase-blocked` event and no `Notifier.Fire` call. Covers codex-r1-4 (#1: a signal racing a step's end).
- `TestAnInterruptAsTheLastPhaseLandsDoesNotFinish`: one phase; the lander cancels the run ctx with `errors.New("SIGTERM")` and returns a successful `Landing` at once. The run exits `4`, there is no `finished` event, and the last run record is `halted` with `interrupted: SIGTERM`. Covers codex-r1-4 (#1: never exit 0 after a signal).
- `TestATimedOutGitMergeBlocksThePhaseNamingIt`: a lander returns `errors.New("git merge --no-ff --no-commit r-loop/phase-1: timed out after 10m0s")`. The `phase-blocked` reason contains `land: git merge --no-ff --no-commit r-loop/phase-1: timed out after 10m0s`. Covers #2 (a git timeout becomes a halt reason naming it).

Tests in `internal/core/loop_events_test.go`:
- `TestAnInterruptDuringTheRemedyWindowHaltsAtOnce`: `RemedyWindow = 2s`, implement fails, and `watcher.ended` cancels the run ctx. The run exits `4` within 1 s, with no `phase-blocked` event. Covers #1 (the remedy-window wait).

Tests in `internal/core/phasecheck_test.go`:
- `TestAnAbortDuringThePhaseCheckWaitStopsTheRun`: in the check rig, `dogHost.onPrompt` for `check phase` marks the abort, then blocks on a channel closed in `t.Cleanup` or after 5 s. The run exits `1` within 2 s, no `SessionHost.Open` call happens, and there is one `aborted` event with `Step == "check"`. Covers #11 (phase-check wait).
- `TestAnInterruptDuringThePhaseCheckWaitHaltsTheRun`: the same blocking `onPrompt`, with the run ctx cancelled 20 ms after the prompt. The run exits `4` within 2 s and the last run record is `halted` with prefix `interrupted`. Covers #1 and change 10.

Tests in `internal/core/session_test.go`:
- `TestATimedOutStateCallFailsTheStepNamingIt`: `r.host.StateErr = errors.New("herdr agent get x: timed out after 30s")`. `Wait` ends `failed` on the first poll with a reason containing `herdr agent get x: timed out after 30s`. Covers codex-r2-4 (#2: a timed-out poll becomes a step failure naming the command and timeout).
- `TestATimedOutStateCallFailsAStepWaitingOnAnAnswer`: the session's `OpenQuestion` is set, then `r.host.StateErr = errors.New("herdr agent get x: timed out after 30s")`. `Wait` ends `failed` on the first poll with that reason, instead of staying paused. Covers codex-r2-3 (#2: an undeliverable answer during a herdr wedge still ends the step).
- `TestTheBackstopStillFiresWhenEveryHostStateCallFails`: `r.host.StateErr = errors.New("herdr agent get x: connection refused")`. `Wait` ends `failed` with `backstop 1h0m0s` after 61 polls. Covers #2 (the backstop still works).
- `TestATimedOutHostCallAtSpawnFailsTheStepNamingIt`: `r.host.StartErr = errors.New("herdr agent start x: timed out after 30s")`. `singleRunner{sm: r.sm}.Run` ends `failed` with a reason containing that text. Covers #2 (a herdr timeout becomes a step failure naming it).

Tests in `internal/face/tui/model_test.go`:
- `TestASecondCtrlCAtTheStopPromptAbortsAndQuits`: ctrl+c, then ctrl+c. The second returns a command that yields `tea.QuitMsg`, and `abort` was called once. Covers #11 (force-quit).
- `TestCtrlCAfterAnAbortWasRequestedQuitsWithoutAbortingTwice`: ctrl+c, `y`, then ctrl+c. The last returns `tea.QuitMsg` and `abort` was called once. Covers #11 (after an abort was requested).
- `TestCtrlCAfterTheAbortedEventStillQuits`: ctrl+c, `y`, then `Apply` an `aborted` event (status becomes `halted`), then ctrl+c. The last key returns `tea.QuitMsg`, and `abort` was called once. Covers #11 (after an abort was requested, whatever order the events arrive in).
- `TestFaceReportsARunErrorToOnExit`: a `Face` whose input is an `io.Pipe` reader. After `Start`, the test calls `pw.CloseWithError(errors.New("tty gone"))`. `OnExit` receives, within 2 s, a non-nil error whose text contains `tty gone`. Covers #1 (an error from `Run` is reported).
- `TestFaceReportsItsProgramExitToOnExit`: a `Face` with a pipe input and `OnExit` sending to a channel. After `Start` then `Stop`, `OnExit` is called once, within 2 s. Covers #1 (the display exiting is reported).

Tests in a new file `internal/app/signal_test.go`:
- `TestSIGTERMDuringALongGateKillsTheGateAbortsTheMergeAndHaltsTheRun`:
  - Setup: `newResumeFixture` with a one-phase todo whose `**Done when:**` is `` `echo $$ > '<tmp>/gate.pid'; sleep 300` ``. Then `f.sim(w, sim)` and `w.Loop.Lander = w.Gate`.
  - Once `gate.pid` has content, the test calls `syscall.Kill(os.Getpid(), syscall.SIGTERM)`.
  - `Execute` returns `4` within 30 s, and the gate's process group is gone within 5 s.
  - The primary tree is clean: `git status --porcelain` is empty, `MERGE_HEAD` is absent and HEAD equals the pre-run HEAD.
  - The store's run status is `halted`, with a `halt` event whose reason is `interrupted: SIGTERM`.
  - Covers the #1 test obligation, and the process is not killed outright.
- `TestEachSignalHaltsALiveStepAsInterrupted`:
  - A table over `SIGINT`, `SIGTERM` and `SIGHUP`, one subtest each.
  - Setup: `newResumeFixture` with `noReviewConfig`, `sim.hang["rloop-p1-plan"]` and `--phases 1`.
  - Once the plan is prompted, the subtest sends the signal to `os.Getpid()`.
  - `Execute` returns `4` within 10 s. The run is `halted`, with a `halt` event whose reason is `interrupted: <SIGNAME>`. The test process is still alive.
  - Covers #1 (all three signals, never killed outright, the `signalName` mapping).
- `TestADisplayErrorHaltsTheRunNamingIt`: the same setup as `TestAClosedDisplayHaltsTheRunAsInterrupted`, but the test calls `pw.CloseWithError(errors.New("tty gone"))` instead of `Stop`. `Execute` returns `4` within 5 s, and the `halt` event's reason starts with `interrupted: display exited:` and contains `tty gone`. Covers #1 (an error from the TUI's `Run` halts the run).
- `TestAClosedDisplayHaltsTheRunAsInterrupted`: preflight with `--plain`, then `w.TUI = &tui.Face{In: <pipe>, Out: <buffer>}` and `sim.hang["rloop-p1-plan"]`. After the plan is prompted, the test calls `w.TUI.Stop()`. `Execute` returns `4` within 5 s, and the run is `halted` with a `halt` event whose reason is `interrupted: display closed`. Covers #1 (no phases without a display).
- `TestASignalWhileTheDisplayWaitsForQuitEndsExecute`: the same TUI setup as `TestAClosedDisplayHaltsTheRunAsInterrupted`, with `noReviewConfig` and `sim.fail["rloop-p1-plan"] = true`, so the run blocks the phase and `Face.Close` then waits for `q`. Once the store's run status is `halted` (the loop has returned), the test sends `SIGTERM` to `os.Getpid()`. `Execute` returns `1` (the block's code) within 30 s without any `q`, and the test process is still alive. Covers codex-r1-3 (#1: a signal during shutdown still stops the display; never killed outright).
- `TestAForceQuitFromTheDisplayRecordsTheRunAborted`: the same setup, with `w.TUI.Abort = func() error { return w.Store.MarkAbort(w.Loop.RunID) }`. After the plan is prompted, the test writes `"\x03"`, then `"\x03"`, into the pipe. `Execute` returns `1` within 5 s without any `q`, so Bubble Tea's `Run` has returned and restored the terminal. There is one `aborted` event and the status is `halted`. Covers #11 (force-quit is recorded aborted and the terminal is restored).

## Left out

- **A shared process-group helper package** for git, herdr, notify and `Repo.Run`: it would be a new package for a few lines, and the copies live in different adapters with different error texts.
- **A `ctx` on every other `Repo` method and on `SessionHost`:** only `Repo.Run`, `Repo.MergeNoFF` and `Repo.Commit` touch the primary tree at a point where a signal must leave it clean, and the per-call timeouts bound the rest.
- **A timeout sentinel error type shared by herdr and core:** `timedOut` matches the error text, as `blocked` already does for `agent_blocked`.
- **A separate delivery-failure path in `hand`:** the `tick` check already fails a step whose answer cannot be delivered, and `withdrawStep` ends `hand`.
- **A stop check between the phase loop and `finished`:** once every phase has landed the run has finished; a signal after that is outside "in any step or in land" (issue item #1).
- **Config keys for the git and herdr timeouts:** no obligation asks for them to be tunable.
- **A new `interrupted` event kind and face rendering for it:** the existing `halt` event carries the reason.
- **Signal handling for intake:** it already has its own (intake.go:52), and the phase does not cover it.
- **Firing `notify.onHalt` on an interrupt:** abort fires no hook (loop_events_test.go:1090), and an interrupt follows it.

## Assumptions

- The git per-call timeout is 10 minutes and the `EnsureExcluded` one is 1 minute. The herdr per-call timeout is 30 s; a waiting prompt gets its wait time on top.
- A signal that arrives before `Loop.Run` (during unblock or triage) keeps the existing behaviour of those paths. The context ends, and triage exits 4 as tech-design.md:810 says.
- After a gate exits, anything left in its process group is killed, whether it passed, failed or was cancelled.
- If `MarkAbort` fails during a force-quit, the run is recorded `halted` with `interrupted: display closed` (exit 4) instead of `aborted`.
- After an interrupt the TUI is stopped without waiting for `q`, and the report path is not printed.
- A signal that arrives after the loop has returned keeps the run's recorded outcome and exit code; it only stops the display.
- Merge cleanup after a cancelled gate (`AbortMerge`, then `ResetHard`) is bounded by the ordinary 10-minute git timeout, not a separate shorter one.
- The goroutine left behind by a cancelled phase check ends when its herdr call returns. The watchdog's check timeout plus the call timeout bound that.

## Gate

`go test ./internal/gitrepo/ ./internal/store/ ./internal/herdr/ ./internal/notify/ ./internal/core/ ./internal/face/tui/ ./internal/app/ -run '^(TestRunThatPassesButLeavesABackgroundChildExitsZero|TestRunThatFailsAndLeavesABackgroundChildKeepsItsExitCode|TestRunCancelledKillsTheProcessGroup|TestRunWithAnEndedContextStartsNothing|TestRunCommandsSeeTerminalPromptOff|TestAHangingGitCallIsKilledAfterTheTimeoutNamingTheCommand|TestEveryGitCallRunsWithTerminalPromptOff|TestEnsureExcludedRunsGitWithTerminalPromptOff|TestEnsureExcludedGivesUpOnAHangingGit|TestAWedgedHerdrCallFailsAfterTheCallTimeoutNamingTheCommand|TestAWaitingPromptGetsItsWaitOnTopOfTheCallTimeout|TestAHookThatExitsZeroButLeavesABackgroundChildIsNotAFailure|TestAHookThatFailsAndLeavesABackgroundChildIsStillAFailure|TestLandGateCancelledWhileTheGateRunsKillsItsGroupAndAbortsTheMerge|TestLandGatePassingWithABackgroundChildLands|TestLandGateRedWithABackgroundChildIsAGateFailureWithItsExitCode|TestLandGateRedWithABackgroundChildGetsAGateFixRound|TestAnAbortWhileLandRunsCancelsItAndRecordsTheAbortOnce|TestAnInterruptMidStepHaltsTheRunAsInterruptedWithExit4|TestAnInterruptDuringLandHaltsTheRunAsInterrupted|TestAnAbortStopsAStepWhileEveryHostStateCallFails|TestATimedOutGitMergeBlocksThePhaseNamingIt|TestAnInterruptDuringTheRemedyWindowHaltsAtOnce|TestAnAbortDuringThePhaseCheckWaitStopsTheRun|TestAnInterruptDuringThePhaseCheckWaitHaltsTheRun|TestTheBackstopStillFiresWhenEveryHostStateCallFails|TestATimedOutHostCallAtSpawnFailsTheStepNamingIt|TestASecondCtrlCAtTheStopPromptAbortsAndQuits|TestCtrlCAfterAnAbortWasRequestedQuitsWithoutAbortingTwice|TestFaceReportsItsProgramExitToOnExit|TestSIGTERMDuringALongGateKillsTheGateAbortsTheMergeAndHaltsTheRun|TestAClosedDisplayHaltsTheRunAsInterrupted|TestAForceQuitFromTheDisplayRecordsTheRunAborted|TestMergeNoFFCancelledMidMergeLeavesThePreMergeHead|TestAnAbortDuringAGateFixRoundStopsTheRun|TestAnAbortDuringTheGateProbeStopsTheRun|TestAnAbortDuringTheMilestoneReportStopsTheRun|TestCtrlCAfterTheAbortedEventStillQuits|TestFaceReportsARunErrorToOnExit|TestEachSignalHaltsALiveStepAsInterrupted|TestADisplayErrorHaltsTheRunNamingIt|TestAnAbortMarkedByALandThatThenSucceedsStillStopsTheRun|TestAnInterruptAsTheLastStepEndsHaltsInsteadOfBlocking|TestAnInterruptAsTheLastPhaseLandsDoesNotFinish|TestASignalWhileTheDisplayWaitsForQuitEndsExecute|TestMergeNoFFTimedOutLeavesThePreMergeHead|TestCommitCancelledInAHangingHookMakesNoCommit|TestLandCancelledAsTheGatePassesNeitherTicksNorCommits|TestLandPassingGateAndCancelledDuringAHangingCommitHookLeavesACleanTree|TestATimedOutStateCallFailsTheStepNamingIt|TestATimedOutStateCallFailsAStepWaitingOnAnAnswer)$'`
