status: planned

## Summary

Every goroutine the driver runs gets a `recover` at its root. Each recovery records the panic and then continues the way that site already handles a failure. So a step-runner panic becomes a failed step. A land panic halts the run before any other phase can land. When the landing commit does not exist yet, the merge is aborted and the todo restored first. When it does exist, it is left in place, and `r-loop resume` records it through the existing crash reconciliation. A phase-check panic becomes a skipped check. A question or hand-off panic withdraws the question. A watch-check panic disables that check for the step. A watchdog-delivery panic marks the watchdog gone, and the existing gone-dog path halts the run. A question-server panic cancels the run context too, and in both cases the existing interrupt path writes the halted record. A panic in the main goroutine is caught in `app.Wiring.Execute`, which first stops the TUI (restoring the terminal), then records the run halted, prints the stack to stderr and exits 2.

The shared pieces are one helper `panicked(phase, step, where, v)`, one helper `quietly(fn)` and one sentinel `errPanic`. `panicked` returns an `error` Event and an error `panic in <where>: <value>` that wraps `errPanic`. The Event's `reason` field holds that text and its `stack` field holds the stack. `quietly` runs one call with its own recover and returns a second panic as an error. Every call a recovery makes after catching a panic (recording, the face, git, the session host, callbacks) goes through `quietly`, because that call may be the very dependency that just panicked, and a panic raised inside a deferred recover would crash the process.

Choices:
- **Recovery per site, or one generic `go` wrapper:** per site. Each site continues differently (an Outcome, an error, a withdrawal, gone, a halt), so a shared launcher could only forward. The helper shares just the recording.
- **How a fatal panic in a core goroutine halts the run:** the loop cancels its own run context, and `Run` owns a `context.WithCancelCause`. The existing `stop` path then records `halted` / `interrupted: <cause>` and emits the `halt` event. The other option, a new halt hook from core into app, would add a port that the existing interrupt path makes unnecessary.
- **Second panic inside a recovery: `quietly` per call, or trust the recording code:** `quietly`. Every recovery calls back into the Store, Face, Repo, SessionHost, AskChannel or a Check, any of which may be where the first panic came from. A panic in a deferred recover is not caught by that recover, so trusting them would bring the crash back.
- **Where the land panic aborts the merge:** inside `LandGate.attempt`, which is the only code that knows `todoAbs`, `originalTodo` and whether the commit happened. A generic recover in `guarded` cannot tell a pre-merge panic from a mid-merge or post-commit one. `guarded` still recovers everything outside `attempt`, so an arbitrary `Lander` cannot crash the run.
- **Land panic: block the phase, record the landing in-run, or halt:** halt. After a panic in `Land`, the primary tree may hold a landing commit whose `landing` record is missing. The panic may have come from `Repo.Commit` after git advanced HEAD, from `CommitTouches` (so the commit-shape check never ran), or from `Store.Append` of the landing record (land.go:145, outside `attempt`). Blocking the phase would let later phases land, and `reconcileLand` (app/resume.go:100-111) only examines the newest `merge-intent`, so that commit would never be recorded. Recording it in-run would mean re-running the shape check against the `Repo` that just panicked, and cannot cover a panicking `Store.Append`. Halting keeps this phase's `merge-intent` the newest one. On resume `reconcileLand` records the commit if `LandedCommit` (app/resume.go:268-283) proves it has parents `base` and the phase tip and the tree recorded in `commit-intent`, which is what `TestResumeRecordsTheLandingOfACommitMadeBeforeTheCrash` (app/resume_test.go:1421) already pins. A halted run is resumable, so no work is lost.
- **Watch-check panic: skip the check or halt:** skip that check for the rest of the step and keep the others ticking. Halting would throw away a working run over one faulty detector, and the step backstop still bounds the step.
- **How a question panic reaches the asking agent:** through the existing `hand` delivery, as an `r-loop: answer to <id>` message saying the question is withdrawn. The agent ended its turn waiting for exactly that message (ADR-76). Withdrawing silently would leave the step idle until its backstop, and would leave the open-question count up, so the backstop would then halt the run on `invariant: a question never kills a step`.
- **Panic event kind:** the existing `error` kind with an extra `stack` field. Both faces already render `error` from `reason` alone (`internal/face/plain/plain.go:54`, `internal/face/tui/model.go:165`). A new kind would fall into plain's default branch (`plain.go:63`), which prints every field, including the stack.

## Changes

Build in this order.

1. **Create `internal/core/panics.go`.**
   - `var errPanic = errors.New("panic")`
   - `func panicked(phase, step, where string, v any) (Event, error)`. It builds `err := fmt.Errorf("%w in %s: %v", errPanic, where, v)` and returns `Event{Kind: "error", Phase: phase, Step: step, Fields: map[string]string{"reason": err.Error(), "stack": string(debug.Stack())}}, err`. It is always called from inside a deferred recover, so `debug.Stack()` includes the frames where the panic started.
   - `func quietly(fn func()) (err error)`. A deferred recover sets `err = fmt.Errorf("%w in recovery: %v", errPanic, r)`; otherwise it runs `fn()` and returns nil. Every recovery below makes its follow-up calls through it.
   - Imports: `errors`, `fmt`, `runtime/debug`. Core stays standard-library-only, so the boundary test (`internal/core/boundary_test.go`) still passes.
   - Serves all three items.

2. **Modify `internal/core/loop.go`.**
   - **`RunLoop` fields:** add `cancel context.CancelCauseFunc`, guarded by `l.mu`.
   - **`halt`.** Add `func (l *RunLoop) halt(err error)`: read `l.cancel` under `l.mu`, and if it is non-nil call `cancel(err)`. Used by `serveQuestions` and the land calls.
   - **`Run` (loop.go:124).** First statements: `ctx, cancel := context.WithCancelCause(ctx)`, `defer cancel(nil)`, `l.mu.Lock(); l.cancel = cancel; l.mu.Unlock()`. They come before the `ServeQuestions` call (loop.go:151). Everything below uses this `ctx`, and `context.Cause` still returns a parent's signal cause, so `interruptReason` (loop.go:1206) is unchanged for signals. Serves item 2 (halt).
   - **`stopReviewers`.** Add `func (l *RunLoop) stopReviewers(owner *Session)`: if `owner == nil`, return. Otherwise lock `owner.mu`, clone `owner.reviewers`, unlock, and call `l.Sessions.Stop(rv)` for each reviewer whose `Agent != ""`. Replace the two identical inline blocks at loop.go:830-839 and loop.go:861-870 with `l.stopReviewers(owner)`. This is the third use, so it is extracted.
   - **`runStep`, goroutine at loop.go:773.** Becomes:
     ```go
     go func() {
         defer func() {
             if r := recover(); r != nil {
                 done <- l.runnerPanicked(ref, r)
             }
         }()
         done <- runner.Run(stepCtx, ref, &loopObserver{l: l, ref: ref})
     }()
     ```
     `done` is 1-buffered and a panic skips the normal send, so only one value is ever sent.
   - **`runnerPanicked`.** Add `func (l *RunLoop) runnerPanicked(ref StepRef, v any) Outcome`:
     1. `ev, err := panicked(ref.Key.Phase, ref.Key.Kind, "step runner", v)`, then `quietly(func() { l.emit(ev) })`.
     2. `out := Outcome{State: StepFailed, Reason: err.Error()}`.
     3. If `live := l.liveSession(); live != nil`: `quietly(func() { live.end(); l.Sessions.Stop(live); l.stopReviewers(live) })`, then `out.Session = live`. This follows the halt branch at loop.go:801-839.
     4. `var rerr error`, then `if qerr := quietly(func() { rerr = l.Sessions.record(ref.Key, StepFailed, out.Reason) }); qerr != nil { rerr = qerr }`. If `rerr != nil && !errors.Is(rerr, errStepEnded)`, append `"; record: " + rerr.Error()` to `out.Reason`.
     5. Return `out`.

     The existing `case out := <-done` branch then emits the step, runs `ended` (remedy window, watcher `StepEnded`) and blocks the phase as for any failed step. Serves item 1.
   - **`guarded` (loop.go:604).** New signature `func (l *RunLoop) guarded(ctx context.Context, phase, step string, fn func(context.Context)) (bool, error)`. The goroutine becomes `defer close(done)` followed by a deferred recover. On a panic it does `ev, err := panicked(phase, step, step, r)`, then `quietly(func() { l.emit(ev) })`, then `perr = err`. Every return reads `perr` after `<-done` and returns `(<same bool as today>, perr)`. Serves item 2 (the land and phase-check goroutine from the warning).
   - **`runPhase` land calls (loop.go:363 and loop.go:384).** Both become:
     ```go
     stopped, perr := l.guarded(ctx, n, "land", func(child context.Context) { landing, err = lander.Land(child, ph) })
     if perr == nil && errors.Is(err, errPanic) {
         perr = err
     }
     if perr != nil {
         l.halt(perr)
         l.stop(ctx, n, "land")
         return "land", Outcome{}, true
     }
     if stopped && err != nil {
         // existing l.stop(ctx, n, "land") and return
     }
     ```
     `perr` is set by a panic anywhere in `Land` that `guarded` caught (for example `Store.Append` of the landing record, land.go:145), and `errors.Is(err, errPanic)` holds when `attempt` recovered its own panic and returned it. `ctx` here is `Run`'s cancel-cause context, so `stop` records `halted` with `interrupted: panic in land: <v>` and emits one `halt` event, and `Run` exits 4 (`stopCode`, loop.go:1225). No later phase lands. A gate-fix panic is not a land panic: `fix` turns it into a failed Outcome and `Land` returns `ErrGate` with the reason as text, so the phase blocks as for any failed gate-fix. Serves item 2 (the land warning, and the halted record when the run cannot continue).
   - **`checkPhase` (loop.go:428).**
     ```go
     stopped, perr := l.guarded(ctx, n, "check", func(child context.Context) { out = l.watcher().BeforePhase(child, ph, base) })
     if stopped {
         return true
     }
     if perr != nil {
         out = CheckOutcome{Kind: phaseCheckSkipped, Reason: perr.Error()}
     }
     ```
   - **`serveQuestions` (loop.go:941).** Add a deferred recover at the top. It does `ev, err := panicked("", "", "question server", r)`, `quietly(func() { l.emit(ev) })`, then `l.halt(err)`. The main loop's existing `ctx.Done` handling (`runStep` loop.go:788, `guarded` loop.go:618, and `Run` loop.go:182) then calls `stop`. `stop` writes the `halted` run record with `interrupted: panic in question server: <v>` and the `halt` event, and the run exits 4. Serves item 2 (the run cannot continue, so a halted record is written).
   - **`question` (loop.go:955).** First statement: `defer func() { if r := recover(); r != nil { l.questionPanicked(q, r) } }()`.
   - **`questionPanicked`.** Add `func (l *RunLoop) questionPanicked(q Question, v any)`:
     1. `ev, err := panicked(q.Step.Phase, q.Step.Kind, "question "+q.ID, v)`, then `quietly(func() { l.emit(ev) })`.
     2. Under `l.mu`, read `open, tracked := l.asked[q.ID]`. If `tracked && open.answered`, unlock and return: an answer is already being handed over.
     3. If `tracked`, set `open.answered = true` and `l.asked[q.ID] = open`. Unlock.
     4. If the watcher implements `interface{ Withdraw(id string) }`, call `quietly(func() { w.Withdraw(q.ID) })`.
     5. `quietly(func() { l.recordWithdrawn(q, err.Error()) })`.
     6. `quietly(func() { l.Ask.Answer(q.ID, "r-loop: question withdrawn: "+err.Error(), "withdrawn", "") })`. The error is ignored, as in `withdraw` (loop.go:1110). `question` itself calls `Ask.Answer` (loop.go:959) and `withdraw` (loop.go:1115), so this call may panic again; `quietly` drops that second panic.
     7. If `tracked`, call `l.hand(open, answerMessage(q.ID, "withdrawn ("+err.Error()+"); continue without an answer", "r-loop", ""))` synchronously. It runs in the dying question goroutine, and `hand` recovers its own panics.

     Serves item 2 (question).
   - **`hand` (loop.go:1035).** First statement is a deferred recover. It does `ev, err := panicked(open.q.Step.Phase, open.q.Step.Kind, "answer hand-off "+open.q.ID, r)`, then `quietly(func() { l.emit(ev) })`, then `quietly(func() { l.undeliverable(open, err.Error()) })`. `undeliverable` (loop.go:1079) already claims the question, records it withdrawn and releases the step. `Session.live` unlocks with `defer` (session.go:91), so a panic inside `Host.Prompt` leaves no lock held. Serves the answer-hand goroutine from the warning (loop.go:997).

3. **Modify `internal/core/land.go`.**
   - **`attempt` (land.go:154).** The results become `(_ Landing, _, _ string, err error)`. Immediately after `base` is read (land.go:184-187), before the `merge-intent` record, declare `var rejected bool`, then register this deferred recover:
     ```go
     defer func() {
         r := recover()
         if r == nil {
             return
         }
         ev, perr := panicked(n, "land", "land", r)
         quietly(func() { g.emit(ev) })
         err = perr
         var head string
         var herr error
         if qerr := quietly(func() { head, herr = g.Repo.HeadSHA("") }); qerr != nil {
             herr = qerr
         }
         if herr != nil {
             err = errors.Join(perr, herr)
             return
         }
         if head != base {
             if rejected {
                 var rerr error
                 if qerr := quietly(func() { rerr = g.Repo.ResetKeep("HEAD~1") }); qerr != nil {
                     rerr = qerr
                 }
                 err = errors.Join(perr, rerr)
             }
             return
         }
         var restore error
         if qerr := quietly(func() {
             merging, merr := g.Repo.MergeInProgress()
             restore = errors.Join(os.WriteFile(todoAbs, originalTodo, 0o644), merr)
             if merging {
                 restore = errors.Join(restore, g.Repo.AbortMerge())
             }
         }); qerr != nil {
             restore = errors.Join(restore, qerr)
         }
         err = errors.Join(perr, restore)
     }()
     ```
     - In the commit-shape check (land.go:325-328), the first statement inside the `if` is `rejected = true`, before the `reason` is built and `ResetKeep("HEAD~1")` runs.
     - Whether the landing commit exists is judged by HEAD against `base`, not by a flag set after `Repo.Commit` returns. So a panic inside `Commit` after git has already advanced HEAD, or in `CommitTouches` (land.go:323), leaves the commit in place: nothing is rewritten, nothing is recorded as landed, and the error is returned. The loop then halts the run (see `runPhase` above). The commit was preceded by its `commit-intent` event (land.go:302-305), so `reconcileLand` records it on resume only if `LandedCommit` proves its parents and tree (app/resume.go:268-283), and otherwise leaves the phase to land again.
     - A panic on the rejected path (`rejected` set, HEAD still past `base`) retries `ResetKeep("HEAD~1")` and returns the panic error, which is what that path does without a panic. If the reset had already finished, HEAD equals `base` and the restore branch runs.
     - If HEAD cannot be read, the tree is left alone and the error is joined, because guessing could discard a landed commit.
     - Otherwise (HEAD equals `base`) the todo is restored before the merge is aborted, the same order the existing failure returns use (land.go:270). `originalTodo` equals HEAD's copy because `guard` has just checked that the tree is clean. `MergeInProgress` is used so that a panic inside `MergeNoFF` itself is covered.
     - A panic before `base` is read touches no tree and is recovered by `guarded`.
     - Serves item 2 (the land warning: abort the merge the way a failed Land does, and never let a later phase land past an unrecorded landing commit).
   - **`fix` (land.go:402).** The result becomes `(out Outcome)`. After `rec` is built, register a deferred recover. It does `ev, err := panicked(n, g.FixKind.Name, "gate-fix", r)`, then `quietly(func() { g.emit(ev) })`, then `out = Outcome{State: StepFailed, Reason: err.Error()}`. Next it persists the terminal state: `var rerr error`, `if qerr := quietly(func() { rerr = g.Store.Append(g.RunID, Record{Kind: RecordStep, At: time.Now(), Step: &key, State: StepFailed, Reason: out.Reason}) }); qerr != nil { rerr = qerr }`, and `if rerr != nil { out.Reason += "; record: " + rerr.Error() }`. This is the same `RecordStep` shape `fix` already appends for `queued` (land.go:426); `stepRecorder.finished` only emits a `step` event and writes no step record (land.go:460-464). Then it calls `quietly(func() { rec.finished(ref, out) })`. Change `out := g.Runner.Run(ctx, ref, rec)` to `out = ...`. `Land` then returns `ErrGate: gate-fix round N ended failed: panic in gate-fix: <v>`. Serves item 1 (the gate-fix runner is a step runner).

4. **Modify `internal/core/milestone.go`.** In `report` (milestone.go:63), the result becomes `(reason string)`. After `rec` (milestone.go:77), add `var s *Session` and a deferred recover:
   1. `ev, err := panicked(phase.ID, b.Kind.Name, "milestone report", r)`, then `quietly(func() { recordEvent(b.Sessions.Store, b.Face, b.RunID, ev) })`.
   2. If `s != nil`, call `quietly(func() { b.Sessions.Stop(s) })`.
   3. `reason = err.Error()`. Then persist the terminal state: `var rerr error`, `if qerr := quietly(func() { rerr = b.Sessions.record(ref.Key, StepFailed, reason) }); qerr != nil { rerr = qerr }`, and `if rerr != nil && !errors.Is(rerr, errStepEnded) { reason += "; record: " + rerr.Error() }` (session.go:666). This writes the failed `RecordStep` unless the session already recorded a terminal state, which is the same rule the step-runner recovery follows.
   4. `quietly(func() { rec.finished(ref, Outcome{State: StepFailed, Reason: reason}) })`.

   `s, err := b.Sessions.Spawn(ctx, ref)` stays as it is (same scope, so `s` is reused). `After` (milestone.go:24) then runs its existing `restore()` and the `report-skipped` event, and `Land` still returns the landing. Serves item 2 (the milestone report inside `guarded`, from the warning).

5. **Modify `internal/core/phasecheck.go`.** The goroutine at phasecheck.go:39 gets a deferred recover. It does `ev, err := panicked(ph.ID, "check", "phase check", r)`, then `quietly(func() { recordEvent(c.Dog.Store, c.Dog.Face, c.Dog.RunID, ev) })`, then `errc <- err` (`errc` is 1-buffered, phasecheck.go:38). In `case err := <-errc`, the first check is `if errors.Is(err, errPanic) { return CheckOutcome{Kind: phaseCheckSkipped, Reason: err.Error()} }`. Serves the phase-check notify goroutine from the warning.

6. **Modify `internal/core/watch.go`.**
   - **`tick` (watch.go:297).** Keep `broken := make([]bool, len(w.Checks))`. For each check index `i`, skip it if `broken[i]`; otherwise build the `CheckContext` and set `broken[i] = !w.runCheck(c, ctx)`.
   - **`runCheck`.** Add `func (w *Watch) runCheck(c Check, ctx CheckContext) (ok bool)`. It holds the existing body (`for _, sig := range c.Run(ctx) { w.accept(sig, c.Name()) }`, then `return true`) plus a deferred recover. On a panic, the recover does `key := ctx.Step.Key`, `name := "unnamed"`, `quietly(func() { name = c.Name() })`, then `ev, _ := panicked(key.Phase, key.Kind, "watch check "+name, r)`, then `quietly(func() { recordEvent(w.Store, w.Face, key.Run, ev) })`, then `ok = false`. `tick` calls `c.Name()` itself (watch.go:313), so `Name` may be what panicked; `quietly` keeps a second panic from it out of the recover.
   - The existing `defer close(t.done)` stays, so `StepEnded` never hangs.
   - Serves item 2 (watch-tick).

7. **Modify `internal/core/watchdog.go`.**
   - **`deliver` (watchdog.go:148).** Two changes:
     - After `defer close(d.drained)`, register a deferred recover. The panic may have come from `d.emit` itself (a panicking `Store.Append` or `Face.Emit`, watchdog.go:350-361), so the recover marks the watchdog gone before it records anything:
       1. `ev, err := panicked("", "", "watchdog delivery", r)`.
       2. `d.mu.Lock()`, `marked := !d.gone && !d.stopping`, `if marked { d.gone = true }`, `d.mu.Unlock()`. It does not take `d.wait`, which a panic inside `markWaiting` or `markResumed` may have left held. From here `Post` is a no-op, `promptWith` returns at `d.live()` (watchdog.go:241), and the loop's `dogGone` check (loop.go:746) halts the run before the next step even if `OnGone` fails.
       3. Record both events, each through its own `quietly`, collecting every failure:
          ```go
          var rerr error
          for _, rec := range []Event{ev, {Kind: "watchdog-unreachable", Fields: map[string]string{"reason": err.Error()}}} {
              var eerr error
              qerr := quietly(func() { eerr = d.emit(rec.Kind, rec.Fields, func() {}) })
              rerr = errors.Join(rerr, eerr, qerr)
          }
          ```
       4. If `rerr != nil && d.Face != nil`, `quietly(func() { d.Face.Emit(Event{At: time.Now(), Kind: "warning", Fields: map[string]string{"reason": "watchdog: " + rerr.Error()}}) })`.
       5. If `marked && d.OnGone != nil`, `quietly(d.OnGone)`.
     - The `d.send.Lock(); d.flush(); d.send.Unlock()` at watchdog.go:160 becomes `func() { d.send.Lock(); defer d.send.Unlock(); d.flush() }()`, so a panic in `flush` does not leave `send` held, which would deadlock `Notify` and `NotifyContext`.
   - **`lost` (watchdog.go:288).** Wrap the `d.wait` critical section in a closure with `defer d.wait.Unlock()`, returning `(marked bool, err error)`: `marked` is false when `gone || stopping`, and `err` is the `d.emit("watchdog-unreachable", …)` result. Afterwards, `if !marked || err != nil { return err }`, then call `OnGone`. Behaviour is unchanged, but a panic from `Store` or `Face` inside `emit` no longer leaves `d.wait` locked for later `AskMaintainer` and `Resume` calls from the watchdog's MCP tools.
   - Once the recover has run, `d.gone` is true and `OnGone` (`Watch.WatchdogGone`, wired at app/wire.go:507) halts the run through the existing gone path, which writes the halted record.
   - Serves item 2 (watchdog-deliver, and the halted record).

8. **Modify `internal/askmcp/server.go`.** `trackTool` (server.go:150) returns a handler with named results `(res *mcp.CallToolResult, out Out, err error)`. After `defer s.wg.Done()`, register a deferred recover that sets `err = fmt.Errorf("r-loop: panic in tool: %v", r)`. It serves every run-time MCP tool (`ask_watchdog` and all the watchdog tools in askmcp/watchdog.go). Their handlers run in the SDK's own jsonrpc2 goroutines, which have no recover (go-sdk v1.8.0 `internal/jsonrpc2/conn.go:646`), so a panic in `RunLoop.Deliver`, `Watch.Handle` and the like would otherwise kill the process. The calling agent receives the error. Serves "one panic in any goroutine".

9. **Modify `internal/app/wire.go`.**
   - **`Execute` (wire.go:262).** It gets the named result `(code int)` and recovers at its own boundary, with no wrapper function:
     1. First statement: `onPanic := func() { if r := recover(); r != nil { code = w.panicked(r) } }`, then `defer onPanic()`. This outermost recover catches panics in the cleanup (`Dog.Stop`, `Ask.Wait`, `release`, `Face.Close`, `records.Failed`).
     2. After the signal setup (wire.go:263-287), move today's tail (wire.go:297-309: the cause-gated `TUI.Stop`, `Dog.Stop`, `cancel(nil)`, `Ask.Wait`, `release`, `Face.Close`, `records.Failed`) unchanged into one deferred closure, so it still runs after a panic in the chain.
     3. Then `defer onPanic()` again. Defers run last-in first-out, so a panic in the chain is recovered here first: the run is recorded halted and the TUI stopped before the cleanup runs, and `Face.Close` does not wait for `q` on a live display.
     4. The `Ask.Serve` / `startWatchdog` / `startTUI` / `run` chain (wire.go:288-296) stays inline; `code := 2` (wire.go:288) becomes `code = 2`.

     `recover` works in `onPanic` because the deferred call invokes `onPanic` directly. A panic in the cleanup that follows a recovered chain panic calls `panicked` a second time: the second halted record and halt event repeat the first, and `TUI.Stop` is idempotent (model.go:481).
     - Add a deferred recover as the first statement of the signal goroutine (wire.go:272): `cancel(fmt.Errorf("panic in signal handler: %v", r))`. The loop then halts as `interrupted: panic in signal handler: <v>`, and `Execute` stops the TUI because the cause is set (wire.go:298).
   - **`quietly`.** Add `func quietly(fn func()) { defer func() { _ = recover() }(); fn() }` to wire.go. It is used by `panicked` three times; `internal/core`'s helper is unexported.
   - **`panicked`.** Add `func (w *Wiring) panicked(v any) int`. The terminal is restored before anything that can fail, because the fatal panic may have come from the store or the face:
     1. `reason := fmt.Sprintf("panic: %v", v)` and `stack := debug.Stack()`.
     2. `if w.TUI != nil { quietly(w.TUI.Stop) }`. This quits the program and waits for it, and Bubble Tea's shutdown restores the terminal.
     3. `quietly(func() { w.recordFatal(core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunHalted, Reason: reason}) })` (app/unblock.go:153). That function already reports its own failure on the face.
     4. `quietly(func() { w.Face.Emit(core.Event{At: time.Now(), Kind: "halt", Fields: map[string]string{"reason": reason, "resume": "r-loop resume"}}) })`.
     5. `fmt.Fprintf(w.Env.Stderr, "r-loop: %s\n%s", reason, stack)`, then return 2.

     After a chain panic, the deferred cleanup (`Dog.Stop`, `Ask.Wait`, `release`, `Face.Close`) runs as today. `TUI.Stop` is idempotent (model.go:481), and `Face.Close` does not wait for `q` because the program has already quit.
   - Import `runtime/debug`.
   - Serves item 3 (the main goroutine), plus the signal goroutine from the warning.

10. **No change to `internal/face/tui/model.go`.** The program goroutine (model.go:455) runs `prog.Run`. Bubble Tea v1.3.10 recovers panics in the model and in commands, restores the terminal and returns `ErrProgramPanic` (bubbletea tea.go:356-359 and 634-638). `OnExit` then halts the run as `interrupted: display exited: …`, as the spec requires (tech-design.md:350). A new test pins that behaviour. Serves item 3 and the TUI warning.

## Tests

Write these first. Every name starts with `TestAPanic`.

- **`internal/core/loop_test.go` › `TestAPanickingStepRunnerFailsTheStepWithThePanicValue`**
  - Setup: `newLoopRig`. The `"plan-file"` runner is a `stepRunnerFunc`. For phase `"1"` it calls `obs.Started(s)`, where `s := &Session{Ref: ref, Agent: "step-agent", Pane: "step-pane"}` with `s.setReviewers([]*Session{{Agent: "reviewer-one"}})`, then does `panic("boom")`. For other phases it delegates to `singleRunner{sm: r.loop.Sessions}`.
  - `Run` returns 1, and the process survives.
  - `phase-blocked` for phase 1 has reason `panic in step runner: boom`.
  - The step records for phase 1 `plan` attempt 1 end in `failed`.
  - An `error` event has that reason and a non-empty `stack`.
  - `SessionHost.Interrupt` calls include `step-agent` and `reviewer-one`.
  - Phase 2 lands (`r.calls("Land ")` contains `2`).
  - Covers item 1.
- **`internal/core/loop_test.go` › `TestAPanicInLandHaltsTheRunBeforeAnotherPhaseLands`**
  - Setup: `r.loop.Lander` is a `landerFunc` that appends `ph.ID` to a `landed` slice and panics `"boom"` for phase 1, and returns `Landing{Phase: ph.ID}` otherwise.
  - Exit is 4.
  - `landed` is exactly `["1"]`, so phase 2 never reaches `Land`.
  - The last run record is `RunHalted` with reason `interrupted: panic in land: boom`, and exactly one `halt` event has that reason.
  - An `error` event has reason `panic in land: boom` and a non-empty `stack`.
  - Covers item 2 (a panic anywhere in `Land`, including the landing-record append, halts before another phase lands).
- **`internal/core/loop_test.go` › `TestAPanicInThePhaseCheckSkipsItAndThePhaseRuns`**
  - Setup: a test watcher embedding `*fakeWatcher` whose `BeforePhase` panics `"boom"`. Run phase 1.
  - Exit is 0.
  - A `phase-check-skipped` event for phase 1 has reason `panic in check: boom`, and an `error` event is recorded.
  - Covers item 2 (`guarded` → `checkPhase`).
- **`internal/core/loop_test.go` › `TestAPanicInTheQuestionServerHaltsTheRunAsInterrupted`**
  - Setup: `r.loop.Ask` is a test `AskChannel` embedding `*fakeAskChannel` whose `Questions()` panics `"boom"`. The `"plan-file"` runner blocks on `<-ctx.Done()` and returns failed.
  - Exit is 4.
  - The last run record is `RunHalted` with reason `interrupted: panic in question server: boom`.
  - Exactly one `halt` event has that reason.
  - An `error` event has a stack.
  - Covers item 2 (the run cannot continue, so a halted record is written).
- **`internal/core/loop_events_test.go` › `TestAPanicRoutingAQuestionWithdrawsItAndTheStepGoesOn`**
  - Setup: `newEventsRig`, `behaviour["rloop-p2-implement"] = "ask"`, and `r.watcher.route` panics `"boom"`. Run phase 2.
  - Exit is 0.
  - The implement states are `queued, spawned, running, waiting-input, running, ok`.
  - The `q1` question record ends `AnsweredBy == "withdrawn"`, with `Answer` equal to `panic in question q1: boom`.
  - `SessionHost.Typed` got `r-loop: answer to q1 (by r-loop): withdrawn (panic in question q1: boom); continue without an answer`.
  - `AskChannel.Answer` was called for `q1` with `withdrawn`.
  - An `error` event is recorded.
  - Covers item 2 (question goroutine).
- **`internal/core/loop_events_test.go` › `TestAPanicTypingAnAnswerWithdrawsTheQuestion`**
  - Setup: as above, but `route` calls `r.loop.Deliver(q.ID, "sqlite", "watchdog", "")`, and `r.loop.Sessions.Host` is a test wrapper around the `*eventsHost` whose `Prompt` panics `"boom"` for text that starts with `r-loop: answer to `. `Run` runs in a goroutine with a cancellable context.
  - Wait until the store holds a `q1` record with `AnsweredBy == "withdrawn"` and `Answer` equal to `panic in answer hand-off q1: boom`, and an `error` event with that reason.
  - Then cancel. `Run` returns 4.
  - Covers the answer-hand goroutine from the warning, and item 2.
- **`internal/core/loop_events_test.go` › `TestAPanicAnsweringALandStageQuestionIsRecordedWithoutACrash`**
  - Setup: `r := newEventsRig(t)`, then `r.loop.Ask` is a test `AskChannel` embedding `r.ask` whose `Answer` panics `"boom"`. `q := Question{ID: "q-gatefix", Step: StepKey{Run: "run-1", Phase: "1", Kind: "gatefix", Attempt: 1}, Text: "which?"}`, as in `TestALandStageQuestionIsWithdrawnAtOnce` (loop_events_test.go:1167).
  - `r.loop.question(context.Background(), q)` runs in a goroutine and returns within 1s. The recovery's own `Ask.Answer` panics again, and the test binary survives.
  - An `error` event has reason `panic in question q-gatefix: boom`.
  - The last `q-gatefix` question record has `AnsweredBy == "withdrawn"` and `Answer == "panic in question q-gatefix: boom"`.
  - Covers item 2 (question goroutine, with a second panic in the recovery).
- **`internal/core/phasecheck_test.go` › `TestAPanicInThePhaseCheckNotifySkipsTheCheck`**
  - Setup: `newCheckRig`, with `dogHost.onPrompt` panicking `"boom"` when the text starts with `check phase`. Run phase 1.
  - Exit is 0.
  - A `phase-check-skipped` event has reason `panic in phase check: boom`.
  - An `error` event is in `r.store`.
  - Covers the phase-check notify goroutine from the warning.
- **`internal/core/watch_test.go` › `TestAPanickingWatchCheckIsRecordedAndOtherChecksKeepTicking`**
  - Setup: `newWatch(store)` with `Poll = time.Millisecond`. `Checks` holds a test check named `bad`, whose `Run` counts its calls atomically and panics `"boom"`, and a `fakeCheck{name: "good", seen: make(chan CheckContext, 1)}`. Call `StepStarted(implementRef(1, 1), nil)`.
  - Receive from `seen` three times, each within 2s.
  - `bad` ran exactly once.
  - The store holds exactly one `error` event, with reason `panic in watch check bad: boom` and a non-empty `stack`.
  - `StepEnded` returns within 1s.
  - Covers item 2 (watch-tick).
- **`internal/core/watch_test.go` › `TestAPanickingWatchCheckNameIsRecordedWithoutACrash`**
  - Setup: `newWatch(store)` with `Poll = time.Millisecond`. `Checks` holds a test check whose `Run` returns one `SignalWarn` and whose `Name` panics `"boom"`, and a `fakeCheck{name: "good", seen: make(chan CheckContext, 1)}`. Call `StepStarted(implementRef(1, 1), nil)`.
  - Receive from `seen` three times, each within 2s.
  - The store holds exactly one `error` event, with reason `panic in watch check unnamed: boom`.
  - `StepEnded` returns within 1s.
  - Covers item 2 (watch-tick, with a second panic in the recovery).
- **`internal/core/watchdog_test.go` › `TestAPanicDeliveringToTheWatchdogMarksItGoneAndFreesItsLocks`**
  - Setup: `newWatchdog` over a `checkHost` whose `onPrompt` panics `"boom"`, with `OnGone` counting calls. Call `d.Post("hello")`.
  - Wait up to 2s for `d.Gone()`.
  - `OnGone` ran once.
  - The store holds an `error` event with reason `panic in watchdog delivery: boom` and a stack, and a `watchdog-unreachable` event with the same reason.
  - `d.Notify("after", false, 0)` returns within 1s, so `send` is free.
  - `d.Stop()` returns within 1s, so `drained` is closed.
  - Covers item 2 (watchdog-deliver).
- **`internal/core/watchdog_test.go` › `TestAPanicDeliveringToTheWatchdogHaltsTheRunWithAHaltedRecord`**
  - Setup: `r := newLoopRig(t)`, `host := &checkHost{onPrompt: func(text string) { if text == "phase landed" { panic("boom") } }}`, `dog := newWatchdog(host, &r.store.fakeStore, ProviderArgs{Kind: "claude"})`, `dog.Face = r.face`, `w := &Watch{Store: r.store, Face: r.face, Dog: dog}`, `dog.OnGone = w.WatchdogGone`, `r.loop.Watcher = w`. `r.loop.Lander` is a `landerFunc` that calls `r.lander.Land(ctx, ph)`, then `dog.Post("phase landed")`, then waits up to 2s for `dog.Gone()` and returns the landing. This follows `goneRig` and `goneLander` (watchdog_test.go:895-911).
  - `r.run(RunOptions{})` returns 5.
  - `r.calls("Land ")` is exactly `["1"]`, and no phase 2 step record exists.
  - The last run record is `RunHalted` with reason `watchdog: the watchdog is gone`, and exactly one `halt` event is recorded.
  - An `error` event and a `watchdog-unreachable` event both have reason `panic in watchdog delivery: boom`, and both come before the `halt` event.
  - Covers item 2 (watchdog-deliver: the run cannot continue, so a halted record is written).
- **`internal/core/watchdog_test.go` › `TestAPanicRecordingTheWatchdogsLossStillMarksItGone`**
  - Table test over two cases: `store`, where `newWatchdog` gets a test `Store` embedding `*fakeStore` whose `Append` panics `"boom"` the first time it gets a `watchdog-unreachable` event; and `face`, where `d.Face` is a test `Face` whose `Emit` panics `"boom"` the first time it gets a `watchdog-unreachable` event.
  - Setup per case: `host := &goneHost{}`, `host.gone.Store(true)`, `OnGone` counting calls, then `d.Post("step ended")`, as in `TestAGoneWatchdogsHaltDoesNotBlockAPhaseCheckBehindAFullSignalQueue` (watchdog_test.go:263-280). The delivery reaches `lost` (watchdog.go:280), whose `emit` panics.
  - Within 2s, `d.Gone()` is true and `OnGone` ran exactly once.
  - The store holds an `error` event with reason `panic in watchdog delivery: boom`.
  - `d.AskMaintainer("q", nil, "")` returns within 1s, so `d.wait` is free, and `d.Stop()` returns within 1s.
  - Covers item 2 (watchdog-deliver, when the panic comes from the store or the face).
- **`internal/core/land_test.go` › `TestAPanicAfterTheMergeRestoresTheTodoAndAbortsTheMerge`**
  - Setup: `newLandEnv`, `phaseWork(1, "one.txt", "1\n")`. The gate uses a test `PlanSource` embedding `*tickPlan` whose `Tick` calls the real `tickPlan.Tick` and then panics `"boom"`. `DoneWhen` is empty.
  - `Land` returns an error containing `panic in land: boom`.
  - `e.assertUntouched(head)` passes: same HEAD, clean tree, todo byte-for-byte, no landing.
  - `.git/MERGE_HEAD` does not exist.
  - `e.face` holds an `error` event with that reason.
  - Covers item 2 (the land warning: mid-merge abort).
- **`internal/core/land_test.go` › `TestAPanickingGateFixRunnerFailsItsStepAndTheLand`**
  - Setup: `newLandEnv`, `phaseWork(1, …)`, and `g := e.gate()` with `FixRounds = 1` and `Runner` a `core.StepRunner` that panics `"boom"`. The phase is `phaseOne("false")`.
  - `Land` returns an error that satisfies `errors.Is(err, core.ErrGate)` and contains `gate-fix round 1 ended failed: panic in gate-fix: boom`.
  - The last `RecordStep` in `e.store` for `gatefix` attempt 1 is `failed`, with a reason containing `panic in gate-fix: boom`. The runner panics before recording anything, so only the recovery can write it.
  - `e.assertUntouched(head)` passes.
  - Covers item 1 (gate-fix runner).
- **`internal/core/land_test.go` › `TestAPanickingMilestoneReportIsSkippedAndThePrimaryTreeIsClean`**
  - Setup: add a `"panic"` outcome to `reportHost.Prompt`, which writes `scribbled\n` to `a.txt` and then panics `"boom"`. With `e.boundaryGate("panic")`, land phases 1 and 2.
  - `Land 2` returns no error, and `landing.MergeSHA == e.head()`.
  - `git status --porcelain` is empty.
  - Exactly one `report-skipped` event has reason `panic in milestone report: boom`.
  - The last `RecordStep` in `e.store` for `milestone` attempt 1 is `failed`, with a reason containing `panic in milestone report: boom`.
  - Covers item 2 (the milestone report inside `guarded`).
- **`internal/core/land_test.go` › `TestAPanicAfterTheLandingCommitKeepsItUnrecorded`**
  - Table test over two cases, each a test `core.Repo` embedding `*gitrepo.Repo` set as `g.Repo` on `g := e.gate()`: `commit`, whose `Commit` calls the real `Commit` and then panics `"boom"`; and `touches`, whose `CommitTouches` panics `"boom"`.
  - Setup per case: `newLandEnv`, `phaseWork(1, "one.txt", "1\n")`, `before := e.head()`. The phase is `phaseOne("")`.
  - `Land` returns an error containing `panic in land: boom`.
  - `e.store.landings()` is empty, so the commit is not recorded as landed without the shape check.
  - `e.head() != before`, and `git rev-parse HEAD^1` is `before`. `git status --porcelain` is empty and `.git/MERGE_HEAD` does not exist.
  - The store's last `commit-intent` event for phase 1 has `tree` equal to `git rev-parse HEAD^{tree}`, which is the state `reconcileLand` records on resume (app/resume_test.go:1421).
  - `e.face` holds an `error` event with reason `panic in land: boom`.
  - Covers item 2 (the post-commit branch of the land recovery, including a panic before the shape check).
- **`internal/core/land_test.go` › `TestAPanicRecordingTheLandingLeavesTheStateResumeReconciles`**
  - Setup: `newLandEnv`, `phaseWork(1, "one.txt", "1\n")`, `before := e.head()`. `g := e.gate()` with `g.Store` a test `core.Store` embedding `e.store` whose `Append` panics `"boom"` for a `RecordLanding` record.
  - Call `g.Land(ctx, phaseOne(""))` inside a function that recovers, as `guarded` does. The recovered value is `"boom"`.
  - `e.head() != before`, `git rev-parse HEAD^1` is `before`, and `e.store.landings()` is empty.
  - The store holds a `merge-intent` for phase 1 with `base == before`, followed by a `commit-intent` whose `tree` equals `git rev-parse HEAD^{tree}`.
  - Together with `TestAPanicInLandHaltsTheRunBeforeAnotherPhaseLands` (no later land) and `TestResumeRecordsTheLandingOfACommitMadeBeforeTheCrash` (app/resume_test.go:1421), this pins that the commit is recorded on resume.
  - Covers item 2 (a panic appending the landing record).
- **`internal/core/land_test.go` › `TestAPanicWhileRejectingTheLandingCommitResetsIt`**
  - Setup: `newLandEnv`, `phaseWork(1, "one.txt", "1\n")`, `head := e.head()`. `g := e.gate()` with `g.Repo` a test `core.Repo` embedding `*gitrepo.Repo` whose `CommitTouches` returns `[]string{"one.txt"}, nil` and whose `ResetKeep` panics `"boom"` on its first call and calls the real `ResetKeep` after that.
  - `g.Land(ctx, phaseOne(""))` returns an error containing `panic in land: boom`.
  - `e.assertUntouched(head)` passes: HEAD back at `head`, clean tree, todo byte-for-byte, no landing.
  - Covers item 2 (the rejected branch of the land recovery).
- **`internal/app/signal_test.go` › `TestAPanicInTheDriverStopsTheDisplayAndHaltsTheRun`**
  - Setup: `newResumeFixture(t, noReviewConfig)`, a sim with `fail["rloop-p1-plan"] = true`, and `installSignalTestDisplay(w)`. `w.Loop.Notifier` is a test `core.Notifier` whose `Fire` panics `"boom"`, which happens on the main goroutine when phase 1 blocks. Run `Execute` in a goroutine, with `Env.Stderr` set to a buffer.
  - `Execute` returns 2 within 10s.
  - `f.load(runID).Status == core.RunHalted`.
  - stderr contains `r-loop: panic: boom`.
  - The display output contains `\x1b[?1049l` (alt screen left).
  - Covers item 3.
- **`internal/app/signal_test.go` › `TestAPanicInExecuteCleanupStillRestoresTheTerminal`**
  - Setup: `newResumeFixture(t, noReviewConfig)`, a default sim so phase 1 lands, and `installSignalTestDisplay(w)`. After `f.sim(w, sim)`, set `w.Dog.Host` to a test `core.SessionHost` embedding the sim's host whose `Close` and `ClosePane` panic `"boom"`. Run `Execute` in a goroutine, with `Env.Stderr` set to a buffer.
  - `Execute` returns 2 within 10s. It does not wait for `q`, because `Dog.Stop` panics before `Face.Close`.
  - stderr contains `r-loop: panic: boom`.
  - The display output contains `\x1b[?1049l`.
  - Covers item 3 (a panic in `Execute`'s cleanup).
- **`internal/app/signal_test.go` › `TestAPanicInTheFaceStillRestoresTheTerminal`**
  - Setup: `newResumeFixture(t, noReviewConfig)`, a sim with `fail["rloop-p1-plan"] = true`, and `installSignalTestDisplay(w)`. Then `pf` is a test `core.Face` embedding `w.TUI` whose `Emit` panics `"boom"` for `phase-blocked` and `halt` events and delegates the rest; set `w.Face = pf` and `w.Loop.Face = pf`. Run `Execute` in a goroutine, with `Env.Stderr` set to a buffer.
  - Phase 1 blocks, the loop's `phase-blocked` emit panics on the main goroutine, and `panicked`'s own `halt` emit panics again.
  - `Execute` returns 2 within 10s.
  - stderr contains `r-loop: panic: boom`.
  - The display output contains `\x1b[?1049l`.
  - `f.load(runID).Status == core.RunHalted`.
  - Covers item 3 (the terminal is restored even when the face is what panics).
- **`internal/askmcp/server_test.go` › `TestAPanickingToolHandlerBecomesAToolError`**
  - Setup: `s := &Server{}`. Call `trackTool(s, h)` directly, where `h` panics `"boom"`.
  - The call returns an error containing `panic in tool: boom`.
  - `s.wg.Wait()` returns at once, so `Done` ran.
  - Covers "any goroutine" (MCP handlers).
- **`internal/face/tui/model_test.go` › `TestAPanicInsideTheDisplayRestoresTheTerminalAndReportsIt`**
  - Setup: `f := &Face{In: in, Out: &out, OnExit: func(err error) { exits <- err }}`, then `f.Start(...)` as the test at model_test.go:810 does. Send `tea.BatchMsg{func() tea.Msg { panic("boom") }}` through `f.prog.Send` (read `f.prog` under `f.mu`).
  - `OnExit` receives an error with `errors.Is(err, tea.ErrProgramPanic)` within 2s.
  - `out` contains `\x1b[?1049l`.
  - Covers item 3 and the TUI warning.

## Left out

- **A generic `goSafe(fn)` launcher:** every site needs its own continuation, so it would only forward.
- **Recording the landing in-run after a post-commit land panic:** it cannot cover a panicking landing-record append, and it would have to re-run the commit-shape check against the `Repo` that just panicked. Halting and letting `reconcileLand` record it covers every case.
- **Changing `reconcileLand` to walk every unresolved `merge-intent`:** halting keeps the panicking phase's intent the newest one, so the existing reconciliation already reaches it.
- **A new `panic` event kind:** plain's default branch would print the stack field inline.
- **Restarting the question server after a panic:** halting via the run context meets "if the run cannot continue, a halted record is written" with no new state.
- **Stopping the gate-fix agent on a runner panic:** `LandGate` holds no handle to the runner's session; see Assumptions.
- **Recovery in the intake MCP tool (`internal/askmcp/intake.go:30`):** it runs before any run exists and outside the TUI, so it cannot lose a run or leave the terminal raw.
- **Re-panicking after recovery:** every obligation is met by recording and continuing or halting, and re-panicking would bring back the crash.

## Assumptions

- **Fatal panic in a core goroutine:** the question server and land are the two. Both halt through the run context, so the run is recorded `halted` / `interrupted: panic in <where>: <v>` and exits 4. This is the same shape the spec gives a panicking display (`interrupted: display exited: <err>`, exit 4, tech-design.md:350). The TUI then shows the halted run until `q`, like any halt, and the terminal is restored on quit.
- **Panic in `Execute`'s cleanup after the run ended:** it is still recorded `halted` with `panic: <v>`. The cleanup did not finish (watchdog pane, lock or display may be left), so the run did not end cleanly, and a later `resume` finds nothing left to land.
- **Panic in the main goroutine:** exits 2, the spec's code for a fatal driver error such as a failed record (tech-design.md:350). The reason is `panic: <v>`, and the stack goes to stderr after the TUI has stopped.
- **Reason text:** the reason carried into step records, block reasons and halt reasons is `panic in <where>: <value>`. The stack is kept only in the recorded `error` event's `stack` field, not in reasons.
- **Recovered step-runner panic:** it is an ordinary failed step, not `Halted`, so the remedy window and watchdog restarts still apply to it.
- **Land panic:** every land panic halts the run with `interrupted: panic in land: <v>` and exit 4, including one before the merge whose tree `attempt` fully restored. A single rule costs one resume, and the loop cannot tell from `Land`'s error alone whether a landing commit was left unrecorded. A gate-fix runner panic is not a land panic; it blocks the phase like any failed gate-fix.
- **Halted reason after a watchdog-delivery panic:** it stays the gone path's `watchdog: the watchdog is gone`. The panic value is on record in the `error` and `watchdog-unreachable` events just before it, and the item asks only that a halted record is written.
- **Watch check that panicked:** it is skipped for the rest of that step, and the next step starts with all checks enabled.
- **Signal goroutine recovery (app/wire.go:272):** it gets no test. The goroutine calls only `cancel` and the concrete `tui.Face.Stop`, and there is no seam to make them panic without adding a test-only hook.
- **TUI program goroutine (face/tui/model.go:455):** it needs no code change. Bubble Tea recovers the program's panics and restores the terminal, and the only other call in that goroutine is `OnExit`, the app's `cancel` closure.
- **askmcp `go srv.Serve(ln)` and the shutdown goroutine (askmcp/server.go:124-125):** they run only `net/http` code, which recovers handler panics itself. Tool handlers are covered by `trackTool`.
- **Sessions still open after a panic:** a gate-fix or milestone-report agent whose session handle was never returned (the panic came before `Spawn` returned, or came inside the runner) is left running. The phase is blocked or the report skipped, and `restore()` cleans the primary tree for the report. Resume already interrupts leftover agents (`stale-interrupted`).
- **A second panic inside a recovery handler:** `quietly` drops it. When the dropped call was the recording itself, the first panic may go unrecorded, but the process survives and the recovery still takes its continuation (failed Outcome, withdrawal, gone, halt). The only unguarded code in a recovery is `panicked` (formatting and `debug.Stack`) and plain field and lock updates.

## Gate

`go test ./internal/core/ ./internal/app/ ./internal/askmcp/ ./internal/face/tui/ -run '^TestAPanic'`
