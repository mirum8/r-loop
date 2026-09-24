status: planned

## Summary

One change covers three backlog items. Each one closes a race or a gap between the loop, the `SessionManager`, the `Watch` and the question router.

- **#9 (halt while spawning).** Every step-state record the `SessionManager` writes goes through `recordAt`. `recordAt` becomes the single guard: under a new `SessionManager.mu`, it refuses to append any state for a step key that already has a terminal record (`ok` or `failed`), and returns `errStepEnded`. The loop's halt path now writes its `failed` record through that same guard (`l.Sessions.record`), replacing the direct `Store.Append`. This has four effects:
  - A halt that lands during `Spawn` makes the later `spawned` or `running` record fail, so `Spawn` returns before opening a workspace, or right after starting the agent. The loop then interrupts the agent that `Spawn` left behind.
  - A runner that already recorded `ok` or `failed` makes the halt's record fail. The loop then acts on the stored outcome and never writes a second terminal record.
  - `Finish` recording after a halt adopts the stored `failed` state.
  - Stall and resume records after a halt are refused.
  - A halt the `Watch` accepted and queued while the step was live, but that the loop did not read before the runner's result won the select, is drained right after an `ok` step ends (`Watch.Drain`). It blocks the phase before it advances or lands, the last phase included. `Drain` first waits for every `Watch.accept` already in flight, so a halt classified against the live step whose signal record is still being appended is not missed.
  - A halt that the loop processes interrupts the step's reviewer agents as well as its worker.
- **#12 (land-stage steps), option B, settled by the watchdog (q3):**
  - A question whose step kind is not one of the loop's own kinds (or their `-rv-` reviewers) is withdrawn at once. That covers `gatefix`, `gate` and `milestone`, so nothing polls for it.
  - Signals that name a land-stage step stay rejected back to the caller, as the code does today.
  - A held halt is re-targeted at the next started step only when that step is in the phase the halt named. Otherwise it is delivered with its named step, and the loop blocks that phase (`haltEnded`).
  - A halt still held, queued or parked when a phase ends, for example a rejected halt naming a land-stage step, is taken by the loop before the next phase starts and after the last phase (`Watch.Drain`), so an accepted or rejected watchdog halt always halts the run.
  - The `gatefix`, `gate` and `milestone` prompts render the sentinel section without the `ask_watchdog` paragraph.
- **#22 (question re-recorded as open after withdrawal):**
  - The `open` question record moves inside the session's `live` critical section, so a withdrawal can only come after it.
  - `QuestionRouter` gets `Withdraw(id)`. It deletes the open entry and leaves a tombstone that makes a later `Route` a no-op.
  - The loop calls `Withdraw` through the `Watch` whenever it withdraws an unanswered question, before it records the withdrawal, so a `Route` that runs while the record is being appended finds the tombstone.
  - `Route` checks the tombstone and registers the open entry in one critical section, so a racing `Withdraw` either comes first (and `Route` does nothing) or deletes the entry afterwards.

Choices:

- **Where the #9 guard lives:** in `SessionManager.recordAt` (taken), or in a per-step fence object passed from the loop into the runner. The guard in `recordAt` won: it adds no type, no field on `StepRef` and no `Observer` method. Every step record already flows through it, and the stored terminal state is the single source of truth.
- **How the guard knows a step ended:** read the store through the existing `terminal(key)` at `session.go:571` (taken), or keep an in-memory `map[StepKey]StepState`. Reading the store won: it reuses existing code, needs no second copy of state, and needs no cleanup.
- **A halt processed after the runner recorded `ok`:** the loop acts on `ok`, then calls `haltEnded(sig)` and stops the phase (taken). The alternatives were to act on `failed` and append a second terminal record (forbidden by #9), or to drop the halt. `haltEnded` is exactly what happens today when the runner's result wins the select. The phase gets blocked and the run exits 5.
- **Land-stage steps (#12):** withdraw and reject (option B), or join the loop's observer path (option A). The watchdog chose B, adding that the three land-stage prompts drop the `ask_watchdog` paragraph.
- **How the prompts drop the paragraph:** a second partial, `outcome`, defined beside `sentinel` in `render.go` (taken), or a template variable switch. A variable would have to be set by every caller under `missingkey=error`. A second partial touches only the renderer and the three templates, and `.r-loop/prompts` overrides that use `sentinel` keep working.
- **How withdrawal reaches the router (#22):** an inline type assertion `interface{ Withdraw(id string) }` on the watcher, as `dogGone` does at `loop.go:610` (taken), or a new method on the `Watcher` interface. The assertion won: no change to the interface, to `nopWatcher` or to the test fakes.
- **Queued halts after the runner's result wins the select:** drain the `Watch`'s queue after an `ok` step ends and at every phase boundary through one `Watch.Drain` (taken), or give the `done` branch priority by re-checking `Signals()` before it. A re-check before `ended` still misses a halt forwarded between it and `StepEnded`; draining after `ended`, once `Watch.live` is cleared, catches every signal forwarded while the step was live, and later halts go to the held path, which the same `Drain` empties.
- **How `Drain` sees a halt that `accept` is still recording:** a count of in-flight accepts on `Watch`, with a `sync.Cond` on `w.mu` that `Drain` waits on (taken), or holding `w.mu` across the whole of `accept`. `accept` calls `Store.Append`, `Release`, `forward`, `target` and `holdHalt`, which all take `w.mu`, so holding it across `accept` would self-deadlock and serialise every signal behind a store write. A `sync.WaitGroup` was rejected because an `Add` from zero that races a `Wait` is a documented misuse.
- **Where reviewers are interrupted on a halt:** in the loop's halt branch after `<-done`, from the worker session's `reviewers` (taken), or by passing a stop into `ReviewHalf`. After `<-done` the runner has returned, so `ReviewHalf.open` has finished and `reviewers` holds every pane it split, including ones opened after the halt. No new parameter or type.
- **A question withdrawn before `Route`:** a tombstone checked first in `Route`, which returns `true` (taken), or holding the session lock across `Route`. `Route` calls `Dog.Notify`, which types into a pane. Holding `s.mu` across it would block `Finish`'s `s.end()`.

## Changes

1. **`internal/prompts/render.go` (modify)** — serves #12 (watchdog addition).
   - In the `sentinel` const (`render.go:21-33`), add a second definition before `{{define "sentinel"}}`, named `{{define "outcome"}}`.
   - Its body is the current `sentinel` body with the `ask_watchdog` paragraph (line 26) and the blank line before it removed. It keeps the `## Reporting the outcome` heading, the sentinel paragraph and the `{{- if .Addendum}}` block, byte-for-byte as they are today.
   - Leave the `sentinel` definition's text unchanged.
   - `Render` is unchanged: it already parses the const before the body (`render.go:51-53`).
2. **`internal/prompts/templates/gatefix.md`, `gate.md`, `milestone.md` (modify)** — serves #12 (watchdog addition). Replace the last line, `{{template "sentinel" .}}`, with `{{template "outcome" .}}`. No other template changes.
3. **`internal/core/session.go` (modify)** — serves #9, all four criteria.
   - Add `mu sync.Mutex` as the last field of `SessionManager` (`session.go:28-40`). `sync` is already imported.
   - Add the package-level `var errStepEnded = errors.New("step already ended")` next to `timedOut`.
   - Rewrite `recordAt` (`session.go:633`). It locks `m.mu` with a deferred unlock. If `_, done := m.terminal(key); done`, it returns `errStepEnded`. Otherwise it appends exactly as today.
   - `record`, `recordState`, `Spawn`'s `spawned` and `running` records (`session.go:166`, `:183`) and `Finish` all go through it unchanged, so:
     - `Spawn` returns `errStepEnded` before `Host.Open` when the halt came first.
     - `Spawn` returns it from the `running` record when the halt came during `Open`, `start` or the prompt.
   - In `Finish` (`session.go:545-569`), change only the last `record` call. If `errors.Is(err, errStepEnded)`, set `out.State, _ = m.terminal(key)` and `return out`. Any other error keeps today's `StepFailed, "record: "+err.Error()`.
4. **`internal/core/loop.go` (modify)** — serves #9, #12 and #22.
   - **Halt branch of `runStep` (`loop.go:704-727`)** — serves #9. Replace it with this sequence:
     1. Set `reason := "watchdog: " + sig.Reason`, `live := l.liveSession()`, and call `live.end()` when `live != nil`.
     2. Call `err := l.Sessions.record(key, StepFailed, reason)`.
     3. If `errors.Is(err, errStepEnded)`, the runner already recorded a terminal state:
        - Take `out := <-done`.
        - When `out.State != StepOK`, set `out.Stalled, out.Halted = false, true`. The stored reason is kept, so the phase blocks with exit 5 and no remedy window opens.
        - Call `l.emitStep(ref, out.State, out.Reason, out.Session)`, then `o, aborted := l.ended(ref, out)`.
        - When `out.State == StepOK`, call `l.haltEnded(sig)`.
        - Return `o, aborted`.
     4. Otherwise:
        - Call `l.Sessions.Stop(live)` when `live != nil`, then `cancel()` and `out := <-done`.
        - When `live == nil && out.Session != nil && out.Session.Pane != ""`, call `l.Sessions.Stop(out.Session)`. This interrupts an agent that `Spawn` opened or started before the halt.
        - Set `owner := live`, and `owner = out.Session` when `live == nil`. When `owner != nil`, copy its reviewers under its lock (`owner.mu.Lock(); rvs := slices.Clone(owner.reviewers); owner.mu.Unlock()`) and call `l.Sessions.Stop(rv)` for each `rv` with `rv.Agent != ""`. Serves #9 criterion 4: a halt during the review half interrupts every reviewer agent (`review.go:204` sets them before any `Start`), whether it came while `ReviewHalf.open` was starting them or while `WaitAll` (`review.go:78`) was waiting on them.
        - Set `out.State, out.Reason, out.Stalled, out.Halted = StepFailed, reason, false, true`.
        - If `err != nil`, append `"; record: " + err.Error()`, as today.
        - Call `emitStep` and `ended`, as today.
   - **`done` branch of `runStep` (`loop.go:695-700`)** — serves #9 criterion 3. Replace `return l.ended(ref, out)` with:

     ```go
     o, aborted := l.ended(ref, out)
     if !aborted && o.State == StepOK {
         l.drainSignals()
     }
     return o, aborted
     ```

     A halt that the `Watch` forwarded while the step was live (`watch.go:398-414` puts it in the 64-slot `signals` buffer, or in `parked`) and that lost the select to `done` is applied here through `haltEnded`, so the stored `ok` stays the only terminal record and the phase is blocked before `runPhase` advances or lands it. A failed step's queued halt is left to `awaitRestart` (`loop.go:508`, `drainHalts` at `:519`) or to the phase-boundary drain below.
   - **`runPhase`** — serves #9 criterion 3. Right after `if aborted || out.State != StepOK { return kind.Name, out, aborted }` (`loop.go:285-287`), add `if slices.Contains(l.blocked, n) { return kind.Name, out, false }`. A phase whose halt arrived after its step recorded `ok` then neither advances nor lands. `Run` sees `out.State == StepOK` and does not block it a second time (`loop.go:177-179`).
   - **`Run` and a new `drainSignals`** — serves #9 criterion 3, #12 criteria 3–4 and the tech-design rule that a rejected watchdog signal halts the run (`docs/task-loop-driver/tech-design.md:564`).
     - Add, after `dogGone` (`loop.go:609-618`):

       ```go
       func (l *RunLoop) drainSignals() {
           d, ok := l.watcher().(interface{ Drain() []Signal })
           if !ok {
               return
           }
           for _, sig := range d.Drain() {
               if sig.Kind == SignalHalt {
                   l.haltEnded(sig)
               } else {
                   l.warn(sig)
               }
           }
       }
       ```

     - Call `l.drainSignals()` in two places in `Run`:
       - as the first statement of the `for _, ph := range list` body (`loop.go:158`), before the `pending` check;
       - once right after that loop ends, before `if h := l.halted; …` (`loop.go:185`).

     A halt held during a phase's land (a rejected watchdog signal naming `gatefix`, `gate` or `milestone`), or a halt still queued after a failed step with no remedy window, is then applied before the next phase starts, and after the last phase, once the phase's own block (if any) is already recorded. `haltEnded` skips a phase already in `l.blocked`, so there is no double block. Otherwise it blocks the named phase, skips its pending dependents, and sets `l.halted`, so the run exits 5. A halt with an empty phase blocks nothing but still sets `l.halted`, so the exit is 5.
   - **`question` (`loop.go:810-829`)** — serves #12 criteria 1–2 and #22.
     - At the top, compute `base, _, _ := strings.Cut(q.Step.Kind, "-rv-")`. When `!slices.ContainsFunc(l.Kinds, func(k StepKind) bool { return k.Name == base })`:
       - call `l.recordWithdrawn(q, "land-stage step")`;
       - call `l.Ask.Answer(q.ID, fmt.Sprintf("r-loop: phase-%s/%s cannot ask the watchdog; this question is withdrawn.", q.Step.Phase, q.Step.Kind), "withdrawn", "")`;
       - return, before `askingSession`.
     - Inside the `s.live` closure, call `l.recordQuestion(q)` first, then `l.track` and the `openQuestion`/`stepState` lines as today.
     - Delete the `l.recordQuestion(q)` line after the closure. The `question` event emit and `l.watcher().Route(ctx, q)` stay after the closure.
   - **`withdraw` (`loop.go:959-962`)** — serves #22 criteria 1–2. As its first statement, before `recordWithdrawn`, add: `if w, ok := l.watcher().(interface{ Withdraw(id string) }); ok { w.Withdraw(q.ID) }`. The tombstone is then in place before the withdrawn record is appended, so a `Route` that runs while that append is in progress returns without notifying the watchdog.
5. **`internal/core/questions.go` (modify)** — serves #22 criterion 2.
   - Add the field `withdrawn map[string]bool` beside `open` (`questions.go:26-27`).
   - `Route` (`questions.go:30-39`): replace the `Dog.live()` check and the open-map registration with one critical section. Lock `r.mu`, then:
    1. If `r.withdrawn[q.ID]`, unlock and `return true`.
    2. If `r.Dog == nil || !r.Dog.live()`, unlock and `return false`. `Dog.live` takes only the dog's own `mu` (`watchdog.go:398-402`), so nesting it under `r.mu` cannot deadlock.
    3. Initialise `open` when nil, set `r.open[q.ID] = true`, and unlock.

    The `Dog.Notify` call and its failure path (`questions.go:40-44`) stay as they are, outside the lock.

    Registration is the moment of routing: a `Withdraw` before it leaves a tombstone that `Route` sees under the same lock, and a `Withdraw` after it deletes the entry. No interleaving can re-create an open entry for a withdrawn id.
   - Add `func (r *QuestionRouter) Withdraw(id string)`, after `close`. It locks `r.mu`, initialises `withdrawn` when nil, runs `delete(r.open, id)`, and sets `r.withdrawn[id] = true`.
6. **`internal/core/watch.go` (modify)** — serves #22 criterion 2 and #12 criterion 4.
   - Add `func (w *Watch) Withdraw(id string)` after `Route` (`watch.go:193-203`). It calls `w.Router.Withdraw(id)` when `w.Router != nil`.
   - Add two fields after `halt` in `Watch` (`watch.go:52`): `accepting int` and `settled *sync.Cond`. In `init` (`watch.go:69-76`), set `w.settled = sync.NewCond(&w.mu)` inside the `once.Do`.
   - In `accept` (`watch.go:320-329`), add `w.accepting++` in the first locked block, right after `w.seq++`. Right after that block's `w.mu.Unlock()`, add:

     ```go
     defer func() {
         w.mu.Lock()
         w.accepting--
         w.settled.Broadcast()
         w.mu.Unlock()
     }()
     ```

     Every return path of `accept` then ends the count after its forward, park or hold is done.
   - Add `func (w *Watch) Drain() []Signal` after `holdHalt` (`watch.go:377-385`). It calls `w.init()`, locks `w.mu` with a deferred unlock, runs `for w.accepting > 0 { w.settled.Wait() }`, and then returns, in this order: every signal it can receive from `w.signals` without blocking, then `w.parked` (set to `nil`), then `*w.halt` when `w.halt != nil` (set to `nil`). It serves #9 criterion 3 and #12 criteria 3–4: a halt forwarded while the step was live but not read by the loop, or held while no loop step is live, such as one naming a land-stage step, is applied after the step or at the phase boundary instead of being lost after the last phase.
  - In `StepStarted` (`watch.go:242-247`), change the held-halt block so that it rewrites `w.halt.Step = key` only when `w.halt.Step.Phase == "" || w.halt.Step.Phase == key.Phase`. The halt is still appended to `w.parked` and unparked in both cases. A halt naming another phase is then delivered with its named step, and `runStep` sends it to `haltEnded` (`loop.go:709-712`).
7. **Existing tests (modify)** — follow the #12 criterion-4 behaviour change.
   - `internal/core/watch_test.go`: `TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull` (`:150`) and `TestARejectedWatchdogSignalBetweenStepsHaltsTheNextStep` (`:457`) now expect the delivered halt's `Step` to equal `StepKey{Phase: "2", Kind: "implement"}` (the named step), not `next.Key`. Keep their `Reason` assertion.
   - `internal/prompts/render_test.go`: `TestStepTemplatesSendRealChoicesToTheWatchdog` (`:326`) iterates `[]string{"plan", "implement", "review", "review-ui", "fix"}` instead of `stepTemplates`.

## Tests

Write these first. Core tests use the existing rigs: `newEventsRig` (`loop_events_test.go:202`), `newWatch`/`receive`/`noSignal` (`watch_test.go:30-58`), `newRouterRig`/`loopQuestion` (`questions_test.go:23`, `:314`), and `waitFor`. The step key under test is `StepKey{Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}` unless stated.

The halt tests use a test-local host type `spawnHaltHost`, which embeds `*eventsHost` and has a `hook` string naming one method. On the first call to that method for the phase-1 implement agent it does three things:

1. Sends `Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: key, Reason: "wrong turn"}` on `r.watcher.signals`.
2. Waits with `waitFor` until `r.store` holds a `RecordStep` for `key` with `StepFailed`.
3. Delegates to the embedded host.

"The phase-1 implement agent" means an `AgentPane` or `Start` name containing `p1-implement`, or an `Open` label containing `p1 implement`.

**`internal/core/loop_events_test.go`:**

- `TestAHaltBeforeTheWorkspaceOpensLeavesOneFailedRecordAndOpensNothing` (hook `AgentPane`). Pins the following, covering #9 criteria 1–2:
  - exit 5;
  - the step records for `key` are exactly `[queued, failed "watchdog: wrong turn"]`;
  - no `SessionHost.Open` has a `p1 implement` label;
  - no `SessionHost.Start` names the implement agent.
- `TestAHaltWhileTheAgentStartsIsTheLastRecordAndInterruptsTheAgent` (hook `Start`). Pins the following, covering #9 criteria 1–2:
  - exit 5;
  - the step records for `key` are exactly `[queued, spawned, failed "watchdog: wrong turn"]`, with no `running` record;
  - `r.calls("SessionHost.Interrupt ")` contains `rloop-p1-implement`.
- `TestAHaltAfterTheRunnerRecordedOkLeavesOnlyTheOkRecord`. The runner, `okThenHalt`, is registered for every kind's `Check`. It delegates to `singleRunner{sm: r.loop.Sessions}` for kinds other than `implement`. For `implement` it runs `out := sm.Finish(&Session{Ref: ref}, Outcome{State: StepOK})`, then sends the halt on `r.watcher.signals`, then returns `out`. Pins the following, covering #9 criterion 3:
  - exit 5;
  - the step records for `key` are exactly `[queued, ok]`, with no `failed`;
  - `r.calls("Land ")` has no `1`;
  - a `warning` event's reason starts with `halt for phase-1/implement after it ended`.
- `TestAHaltAfterTheRunnerRecordedFailedKeepsItsRecordAndHalts`. Same as the previous test, but `okThenHalt` finishes with `Outcome{State: StepFailed, Reason: "sentinel failed: x"}`, and `r.loop.RemedyWindow = time.Hour`. Pins the following, covering #9 criterion 3:
  - exit 5, returned without waiting for the window;
  - the step records for `key` are exactly `[queued, failed "sentinel failed: x"]`.
- `TestAHaltMidRunLeavesExactlyOneFailedRecordAndInterruptsTheAgent`. Uses `r.host.behaviour["rloop-p1-implement"] = "hold"`, with the halt sent from `r.watcher.started` as `TestAWatchdogHaltRecordsTheFailedStepBeforeStoppingTheSession` does (`:1079`). Pins #9 criterion 4:
  - exit 5;
  - exactly one `failed` record for `key`, and it is the last record for `key`;
  - `SessionHost.Interrupt` was called for `rloop-p1-implement`.
- `TestAHaltDuringTheReviewHalfInterruptsEveryReviewer`. A table with two cases, `opening` and `waiting`. `kind := r.loop.Kinds[1]` (implement) gets `Row.Reviewers = []Reviewer{{Provider: "claude"}}` and `Row.Rounds = 1`, and `r.loop.Runners = map[string]StepRunner{"diff": singleRunner{sm: r.loop.Sessions, review: review}}`, as `TestReviewHookRunsOnlyForAnOkStepWithReviewersAndRounds` does (`loop_test.go:1228`). For phase 1, `review` builds `rv := &Session{Agent: "rloop-p1-implement-rv-claude", Pane: "pane-rv", Reviewer: "claude"}`; for other phases it returns `Outcome{State: StepOK, Session: s}`.
  - `opening`: send `Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "wrong turn"}` on `r.watcher.signals`, `waitFor` the `failed` record for `key`, then `s.setReviewers([]*Session{rv})`, and return `Outcome{State: StepFailed, Reason: "interrupted", Session: s}`. This is a reviewer started after the halt.
  - `waiting`: `s.setReviewers([]*Session{rv})`, send the halt, block on `<-ctx.Done()`, and return `Outcome{State: StepFailed, Reason: "interrupted: context canceled", Session: s}`.

  Pins the following, covering #9 criterion 4:
  - exit 5;
  - `r.calls("SessionHost.Interrupt ")` contains both `rloop-p1-implement` and `rloop-p1-implement-rv-claude`;
  - exactly one `failed` record for `key`, and it is the last record for `key`.
- `TestALandStageQuestionIsWithdrawnAtOnce`. Runs a table over the kinds `gatefix`, `gate`, `milestone` and `gatefix-rv-codex`, asserting afterwards that `r.calls("Watcher.Route ")` is empty. For each, run `r.loop.question(context.Background(), Question{ID: "q-<kind>", Step: StepKey{Run: "run-1", Phase: "1", Kind: kind, Attempt: 1}, Text: "which?"})` in a goroutine, and require it to return within 1 s. Pins the following, covering #12 criteria 1–2:
  - the stored question has `AnsweredBy == "withdrawn"` and `Answer == "land-stage step"`;
  - `r.calls("AskChannel.Answer ")` holds the id with `withdrawn`;
  - `r.calls("Watcher.Route ")` is empty.

**`internal/core/watch_test.go`:**

- `TestASignalNamingALandStageStepIsRejectedToTheCaller`. Start and end `implementRef(2, 1)` as `StepOK`. Then `w.Handle` a `warn` and a `halt` naming `StepKey{Run: "run-1", Phase: "2", Kind: "gatefix"}`. Each returns `false, "phase-2/gatefix is not running"`. Covers #12 criterion 3.
- `TestAHeldHaltIsNotRetargetedToAStepOfAnotherPhase`. After phase-2 implement ended `ok`, `Accept` a watchdog halt naming `StepKey{Phase: "2", Kind: "gatefix"}`, then `noSignal`. Then `StepStarted` phase-3 plan. The received halt has `Step == StepKey{Phase: "2", Kind: "gatefix"}`, not the phase-3 key. Covers #12 criterion 4.
- `TestAHeldHaltIsDeliveredToTheNextStepOfItsPhase`. With nothing live, `Accept` a watchdog halt naming `StepKey{Phase: "3", Kind: "plan"}`. Then `StepStarted` `StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "implement", Attempt: 1}}`. The received halt's `Step` equals that key. Covers #12 criterion 4, same-phase side.
- `TestDrainReturnsHeldAndQueuedSignalsOnce`. `StepStarted` `implementRef(2, 1)`, then `Accept` a watchdog halt naming that step (it is forwarded into `signals`), then `StepEnded` it as `StepOK`, then `Accept` a watchdog halt naming `StepKey{Phase: "2", Kind: "gatefix"}` (rejected and held). `w.Drain()` returns exactly two signals, both `SignalHalt`: first the one naming `implementRef(2, 1).Key`, then the held one naming `StepKey{Run: "run-1", Phase: "2", Kind: "gatefix"}`. A second `Drain()` returns none, and a following `StepStarted` of phase 3 delivers nothing (`noSignal`). Covers #9 criterion 3 and #12 criteria 3–4.
- Modified: `TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull` and `TestARejectedWatchdogSignalBetweenStepsHaltsTheNextStep`, as in Changes 7. Covers #12 criterion 4.

**`internal/core/loop_test.go`** (with a real `w := &Watch{Store: r.store}` as `r.loop.Watcher` on `newLoopRig`):

- `TestAHaltQueuedAsTheStepEndsOkBlocksThePhaseBeforeItLands`. A table over phases `1` and `3` (the last phase). Each case runs 20 iterations on a fresh rig, because the loop's select picks `done` or the queued halt at random and both must give the same result. The runner, registered for `plan-file` and `diff`, delegates to `singleRunner{sm: r.loop.Sessions}` except for implement in the case's phase, where it runs `obs.Started(&Session{Ref: ref})`, then `out := sm.Finish(&Session{Ref: ref}, Outcome{State: StepOK})`, then `w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "stop"})` (forwarded into the buffered `signals`, since the step is live), then returns `out`. Pins the following in every iteration, covering #9 criterion 3:
  - exit 5;
  - the step records for the implement key are exactly `[queued, ok]`;
  - `r.calls("Land ")` does not contain the case's phase;
  - a `phase-blocked` event for the case's phase.
- `TestAHaltStillBeingRecordedWhenTheLastPhaseEndsHaltsTheRun`. A table with two cases, `step` and `land`, both on phase `3` (the last). `w := &Watch{Store: hook}` is `r.loop.Watcher`, where `hook` is a test-local `signalHookStore` that embeds `*loopStore` (`r.store`). Its `Append` of a `RecordSignal` closes `recording` once, blocks on `release`, then delegates. The loop keeps `r.store`.
  - `step`: the runner is registered as in `TestAHaltQueuedAsTheStepEndsOkBlocksThePhaseBeforeItLands`. For phase-3 implement it runs `obs.Started(&Session{Ref: ref})`, starts `go w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: ref.Key, Reason: "stop"})`, waits `<-recording` (the halt is classified against the live step), then returns `sm.Finish(&Session{Ref: ref}, Outcome{State: StepOK})`.
  - `land`: `r.loop.Lander` is a `landerFunc` that, for phase `3`, starts `go w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "3", Kind: "gatefix"}, Reason: "stop"})`, waits `<-recording`, then delegates to `r.lander.Land`.
  - In both, a test goroutine closes `release` 50 ms after `<-recording`, so the step or the land has finished while the append is still blocked.

  Pins the following, covering #9 criterion 3 and #12 criterion 3:
  - exit 5;
  - no run record with `RunFinished`;
  - `step`: `r.calls("Land ")` does not contain `3`, and the step records for phase-3 implement are exactly `[queued, ok]`;
  - `land`: a `warning` event's reason starts with `halt for phase-3/gatefix after it ended`.
- `TestAHaltHeldDuringTheLastPhasesLandHaltsTheRun`. `r.loop.Lander` is a `landerFunc` that, for phase `3`, first calls `w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "3", Kind: "gatefix"}, Reason: "stop"})`, then delegates to `r.lander.Land`. Pins the following, covering #12 criteria 3–4:
  - exit 5;
  - a `warning` event's reason starts with `halt for phase-3/gatefix after it ended`;
  - a `phase-blocked` event for phase `3`.
- `TestAHaltHeldDuringALandIsAppliedBeforeTheNextPhaseStarts`. Same, but the halt is accepted during phase `1`'s land. Pins the following, covering #12 criteria 3–4:
  - exit 5;
  - in `r.face` event order, the `warning` naming `phase-1/gatefix` comes before the `phase-start` event of phase `2`;
  - no step of phase 2 received the halt: no `failed` step record reason `watchdog: …` exists for phase `2`.

**`internal/core/questions_test.go`:**

- `TestAQuestionWithdrawnWhileItIsAdmittedIsStoredWithdrawn`. Uses a test-local `admitHookStore` that embeds `*loopStore`. Its `Append` of a `RecordQuestion` with `AnsweredBy == ""` closes `admitting` once, then blocks on `release`, then delegates. Set `r.loop.Store` to it and build `loopQuestion`'s setup (a `Watch` with a real `QuestionRouter`), but run `r.loop.question` in a goroutine. The sequence:
  1. `<-admitting`.
  2. Start a goroutine that runs `s.end(); r.loop.withdrawStep(s.Ref.Key, StepFailed)` and closes `ended`.
  3. Wait for `ended` or 50 ms, then `close(release)`, then `<-ended`, and wait for the question goroutine.

  Pins the following, covering #22 criteria 1–2:
  - the stored question has `AnsweredBy == "withdrawn"`;
  - `router.open["q1"]` is false (read under `router.mu`).
- `TestARouteDuringTheWithdrawalRecordNeverReachesTheWatchdog`. Builds a `QuestionRouter` on a fresh `fakeSessionHost` as `TestAStepQuestionReachesTheWatchdogAndItsAnswerReleasesTheStep` does (`questions_test.go:333`), sets `r.loop.Watcher = &Watch{Store: r.store, Router: router}` and `r.loop.runDir = r.store.dir`. A test-local `withdrawHookStore` embeds `*loopStore`; its `Append` of a `RecordQuestion` with `AnsweredBy == "withdrawn"` closes `withdrawing` once, blocks on `release`, then delegates. With it as `r.loop.Store`, run `r.loop.withdraw(q, StepFailed)` in a goroutine for `q := Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}, Text: "which db?"}`. After `<-withdrawing`, `router.Route(context.Background(), q)` returns `true`; then `close(release)` and wait for the goroutine. Pins the following, covering #22 criteria 1–2:
  - `host.Calls()` is empty;
  - `router.open["q1"]` is false (read under `router.mu`);
  - the stored question has `AnsweredBy == "withdrawn"`.
- `TestAQuestionWithdrawnBeforeRoutingNeverReachesTheWatchdog`. Uses `newRouterRig`: `r.router.Withdraw("q1")`, then `r.router.Route(ctx, routedQuestion())` returns `true`. Pins #22 criterion 2:
  - `r.host.Calls()` is empty, so the watchdog was not prompted;
  - `r.router.open["q1"]` is false.
- `TestAWithdrawalRacingRouteNeverLeavesAnOpenEntry`. On `newRouterRig`, run 200 iterations, each with a fresh id `q-<i>`. Start `r.router.Route(ctx, q)` and `r.router.Withdraw(q.ID)` in two goroutines released together by one closed channel, then wait for both. After each iteration, `r.router.open[q.ID]` is false (read under `r.router.mu`). Run under `go test -race` in the gate. Covers #22 criterion 2 against the interleaving where `Withdraw` falls between the tombstone check and the registration.
- `TestWithdrawingARoutedQuestionClosesItsRouterEntry`. Runs `loopQuestion` (routed), then `s.end()` and `r.loop.withdrawStep(s.Ref.Key, StepFailed)`. Pins #22 criterion 2:
  - `router.open["q1"]` is false;
  - `router.Answer("q1", "sqlite", "docs/x/spec.html:1")` returns `false`.
- `TestAnAdmittedQuestionIsRecordedOpenOnceBeforeItIsRouted`. Uses a hook store that counts open `RecordQuestion` appends and snapshots `len(host.Calls())` of the watchdog's host at each one. Pins #22 criterion 3:
  - exactly one open record;
  - the snapshot is 0;
  - after `loopQuestion`, `host.Calls()` has length 1.

**`internal/prompts/render_test.go`:**

- `TestLandStagePromptsDoNotOfferAskWatchdog`. For `gatefix`, `gate` and `milestone` rendered with `fullVars()`: the text has no `ask_watchdog`, and still has `{"outcome":"ok","reason":""}` and the `Sentinel` path. For `plan` and `implement`: the text has `` call the `ask_watchdog` tool ``. Covers #12 (watchdog addition).
- Modified: `TestStepTemplatesSendRealChoicesToTheWatchdog`, as in Changes 7. `TestAddendumSectionOnlyWhenSet` and `TestStepTemplatesCarryTheSentinelParagraph` are unchanged and keep covering the `outcome` partial's addendum and sentinel text.

## Left out

- Joining the land-stage steps to the loop's observer path, so their questions are routed and their signals applied: the watchdog chose withdraw/reject (q3).
- A `StepRef` fence or an `Observer` method for spawn-time halts: the store-backed guard in `recordAt` meets every #9 criterion with no new type.
- An in-memory terminal-state map in `SessionManager`: `terminal()` already answers from the store.
- An extra `halted` check right before `Host.Open`: the `spawned` record sits just before `Open`, and a halt in that window is covered by refusing the `running` record and interrupting the agent afterwards.
- A `Withdraw` method on the `Watcher` interface: the inline type assertion reaches it without touching `nopWatcher` or the fakes.
- Changing `nudge` (`session.go:445`), which still names `ask_watchdog` for a stalled land-stage agent that has an ask URL: the watchdog asked only for the prompts. A stray ask is withdrawn at once, and the stall backstop bounds the idle wait.
- Draining queued signals in the `done` branch after a failed step: `awaitRestart` already takes them within the remedy window (`loop.go:508`), and the phase-boundary drain takes them otherwise; applying them there would block the phase twice.
- Propagating a `Store.Load` error out of the `recordAt` guard (it keeps `terminal()`'s treat-as-not-ended behaviour): what the driver does when the store fails is backlog item #10b (`issues/issues-reliability-review-2026-09-23.md:71`). Refusing the append on a load error would leave a halted step with no terminal record at all.
- Applying a land-stage halt during `Land` itself, so the named phase never lands: that is option A, which the watchdog declined (q3). The held halt is applied at the phase boundary instead.

## Assumptions

- A land-stage withdrawal is recorded with `Answer: "land-stage step"`. The ask server is told `r-loop: phase-<N>/<kind> cannot ask the watchdog; this question is withdrawn.`
- A held halt whose named step has an empty phase (a malformed name) is still re-targeted at the next started step. It names no phase, so no other phase is involved, and tech-design says a rejected watchdog signal halts the run.
- When a halt finds the step already recorded `failed` by its runner, the stored reason is kept, and the outcome is marked `Halted`, so the phase blocks with exit 5 and no remedy window opens.
- When `Finish` finds a halt's `failed` record after committing an `ok` step, the commit stays on the phase branch. Ordering of the commit record is #10a's scope.
- A `Watch.accept` whose `Store.Append` never returns makes `Drain` wait with it. A store that does not return blocks every record the run makes, and what the driver does then is backlog item #10b (`issues/issues-reliability-review-2026-09-23.md:71`).
- The halt's `failed` record now uses `SessionManager.now()` rather than `time.Now()`, like every other step record.

## Gate

`go test -race -count=1 ./internal/core/ ./internal/prompts/ -run '^(TestAHaltBeforeTheWorkspaceOpensLeavesOneFailedRecordAndOpensNothing|TestAHaltWhileTheAgentStartsIsTheLastRecordAndInterruptsTheAgent|TestAHaltAfterTheRunnerRecordedOkLeavesOnlyTheOkRecord|TestAHaltAfterTheRunnerRecordedFailedKeepsItsRecordAndHalts|TestAHaltMidRunLeavesExactlyOneFailedRecordAndInterruptsTheAgent|TestALandStageQuestionIsWithdrawnAtOnce|TestASignalNamingALandStageStepIsRejectedToTheCaller|TestAHeldHaltIsNotRetargetedToAStepOfAnotherPhase|TestAHeldHaltIsDeliveredToTheNextStepOfItsPhase|TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull|TestARejectedWatchdogSignalBetweenStepsHaltsTheNextStep|TestAQuestionWithdrawnWhileItIsAdmittedIsStoredWithdrawn|TestAQuestionWithdrawnBeforeRoutingNeverReachesTheWatchdog|TestWithdrawingARoutedQuestionClosesItsRouterEntry|TestAnAdmittedQuestionIsRecordedOpenOnceBeforeItIsRouted|TestLandStagePromptsDoNotOfferAskWatchdog|TestStepTemplatesSendRealChoicesToTheWatchdog|TestDrainReturnsHeldAndQueuedSignalsOnce|TestAHaltDuringTheReviewHalfInterruptsEveryReviewer|TestAHaltQueuedAsTheStepEndsOkBlocksThePhaseBeforeItLands|TestARouteDuringTheWithdrawalRecordNeverReachesTheWatchdog|TestAHaltStillBeingRecordedWhenTheLastPhaseEndsHaltsTheRun|TestAHaltHeldDuringTheLastPhasesLandHaltsTheRun|TestAHaltHeldDuringALandIsAppliedBeforeTheNextPhaseStarts|TestAWithdrawalRacingRouteNeverLeavesAnOpenEntry)$'`
