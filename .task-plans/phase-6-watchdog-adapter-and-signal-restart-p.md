status: planned

## Summary

This phase makes one change that fixes backlog items #6, #26 and #27 (the prompt lists them as 6, 27 and 28; the criteria text is what counts). All code is in `internal/core`.

- **#6, dead watchdog.** When a prompt to the watchdog fails for any reason other than `agent_blocked`, the watchdog asks `Host.State`. If the agent is gone, a new `Watchdog.lost` marks it gone exactly once, records and emits one `watchdog-unreachable` event, and calls `OnGone` once.
  - `PhaseCheck.Run` reports a check whose watchdog vanished as `phase-check-skipped` ("watchdog unreachable"), not `phase-check-timeout`.
  - `runPhase` checks `dogGone` right after the phase check, so a phase whose check found the watchdog gone spawns no step and never lands.
- **#26, full signal buffer.** `Watch.forward` never blocks.
  - A warn that finds the 64-slot buffer full is dropped. The drop is recorded (`signal-dropped` event) and returned to the caller as an error.
  - A halt that finds it full is parked in `Watch.parked`. `Watch.Signals()` moves parked halts into the channel each time the loop reads it, so a halt is never lost. A halt held between steps that meets a full buffer at the next `StepStarted` is parked the same way.
  - Because nothing waits, `Accept`/`Handle` return at once, even after the loop's `Run` has returned. The watchdog's drain goroutine can no longer block while holding the send lock.
- **#27, restart_step.** `Restart` gains a `Reply chan string`. `Remedies.Restart` sends the request and then waits for the loop's answer. `awaitRestart` answers every restart it receives:
  - `""` once it has queued attempt N+1;
  - otherwise its reason: `run halted: <halt reason>`, `restart limit N reached`, or `phase-X/Y attempt N is not waiting for a restart`.

  `restart_step` is accepted only on `""`.

Choices:

- **How to detect a gone watchdog.** Option taken: ask `Host.State` after a failed prompt. Rejected: matching `agent_not_found` in the error text. `State` is the port's own way to say "gone" (herdr maps every `*not_found` code to `AgentGone`, `internal/herdr/client.go:300-301`), and the spec says the watchdog is recorded unreachable "only once `Host.State` reports it gone" (`docs/task-loop-driver/todo.md:442`).
- **Whether to probe the watchdog at phase boundaries.** Option taken: no probe; the next Post, Notify or phase-check prompt detects the dead pane. Rejected: a `Host.State` probe inside `Watch.Gone()`. Settled by the watchdog (q2, citing `issues/issues-reliability-review-2026-09-23.md:42`): `Gone()` stays free of side effects, and a test pins the ordering.
- **How a vanished-watchdog check is reported.** Option taken: `phase-check-skipped` with the existing reason `watchdog unreachable` (`internal/core/phasecheck.go:32`). Rejected: a new outcome kind. Both faces and `report.go:109-113` already render `skipped`.
- **Where a halt waits when the buffer is full.** Option taken: a `parked` slice in `Watch`, moved into the channel by `Signals()`. Rejected:
  - a bounded send timeout: a halt could still be lost after the timeout;
  - a pump goroutine: it leaks after `Run` returns;
  - evicting a queued warn: this reorders halts and needs a second lock.

  The loop already calls `Signals()` at every select, so this adds no loop code.
- **How the loop's verdict reaches `Remedies.Restart`.** Option taken: a buffered reply channel carried in `Restart`. Rejected: copying the loop's checks into Remedies. Remedies cannot see a halt that is pending in the signal channel (`drainHalts`, `internal/core/loop.go:581-592`).

## Changes

Build order:

1. **`internal/core/watchdog.go` (modify)**, serves #6 c1 and c4, #26 c3, and the invariant that `Stop` never fires `OnGone` (`docs/task-loop-driver/todo.md:442`).
   - Add `func (d *Watchdog) lost(reason string) error`:
     1. Take `d.wait` (the same serialisation as `markWaiting`, `watchdog.go:306-319`).
     2. Under `d.mu`, read `skip := d.gone || d.stopping`. If `skip`, unlock `d.wait` and return nil. `d.gone` covers "already gone". `d.stopping` covers a `Stop` in progress: `Stop` sets `stopping` (`watchdog.go:390-391`) before it waits for the outbox (`watchdog.go:406-407`) and sets `gone` only after (`watchdog.go:411`), so an in-flight outbox prompt that returns `agent_not_found` during `Stop` must not fire `OnGone`.
     3. Call `d.emit("watchdog-unreachable", map[string]string{"reason": reason}, func() { d.gone = true })`, which appends the record before it applies the change (`watchdog.go:333-345`).
     4. Unlock `d.wait`. If emit failed, return its error and leave `gone` false (keeps `TestUnreachableIsRecordedBeforeTheWatchdogIsDropped` green).
     5. Otherwise call `d.OnGone()` when it is non-nil, outside the lock, and return nil.

     The live check and the emit happen under one lock, so concurrent callers (the drain goroutine and a Notify) produce exactly one event and one `OnGone`.
   - Change the tail of `promptWith` (`watchdog.go:274-288`) to:
     ```go
     if !blocked(err) {
         if err == nil {
             return d.markResumed(true)
         }
         if state, serr := d.Host.State(d.agent()); serr != nil || state != AgentGone {
             return err
         }
     }
     if lerr := d.lost(err.Error()); lerr != nil {
         return lerr
     }
     return err
     ```
     Consequences:
     - A non-blocked error with the agent gone is marked gone.
     - A transient error (a timeout, or a failed `State` call) returns the prompt error and leaves the watchdog live.
     - The blocked-and-gone path (`watchdog.go:257-258` breaks out of the retry loop) now goes through `lost`.
     - The `ctx.Err()` checks above stay as they are, so a cancelled `NotifyContext` never calls `State`.

2. **`internal/core/watch.go` (modify)**, serves #26 c1, c2 and c4, and #26 c3 through the non-blocking `OnGone` path.
   - Add the field `parked []Signal`, guarded by `w.mu`.
   - Add `var errSignalDropped = errors.New("the signal queue is full; signal dropped")`, and import `errors` and `strconv`.
   - `Signals()` (`watch.go:87-90`) becomes:
     ```go
     w.init()
     w.mu.Lock()
     w.unpark()
     w.mu.Unlock()
     return w.signals
     ```
   - In `StepStarted` (`watch.go:224-230`), replace the non-blocking send of the held halt with: `w.halt.Step = key; w.parked = append(w.parked, *w.halt); w.halt = nil; w.unpark()`. A halt held between steps (`holdHalt`, `watch.go:355-362`) that meets a full buffer is parked for the new step instead of staying in `w.halt`, where the live step would run without it and a later `StepStarted` would retarget it. Serves #26 c4. `w.mu` is already held there, and `unpark` needs no relock.
   - New `func (w *Watch) unpark()`, called with `w.mu` held: while `parked` is non-empty, send `parked[0]` without blocking (`select` with `default`). On success drop it from `parked`; on `default` return.
   - `forward(sig Signal) bool` replaces `forward(sig, stop)` (`watch.go:365-375`):
     1. Lock `w.mu` and call `unpark()`.
     2. If `parked` is empty, try a non-blocking send and return true on success.
     3. If `sig.Kind != SignalHalt`, return false.
     4. Otherwise append `sig` to `parked` and return true.

     A send on a buffered channel with `default` never blocks, so holding `w.mu` is safe. `StepStarted` already sends under `w.mu` (`watch.go:224-230`).
   - `accept(sig Signal, check string)` drops the `stop` parameter:
     - `Accept` (`watch.go:300-302`) calls `w.accept(sig, "")`.
     - `tick` (`watch.go:286`) calls `w.accept(sig, c.Name())`.
     - In the accepted branch (`watch.go:321-327`): `if !w.forward(sig) { return sig, w.dropped(sig, runID) }`.
     - In the rejected branch (`watch.go:351`): `if !w.forward(fwd) { if err := w.dropped(fwd, runID); !errors.Is(err, errSignalDropped) { return sig, err } }`, then `return sig, nil`. The caller already hears the rejection.
   - New `func (w *Watch) dropped(sig Signal, runID string) error`:
     1. Build `ev := Event{At: w.now(), Kind: "signal-dropped", Phase: sig.Step.Phase, Step: sig.Step.Kind, Fields: map[string]string{"seq": strconv.Itoa(sig.Seq), "kind": string(sig.Kind), "source": string(sig.Source), "reason": sig.Reason}}`.
     2. Append it as `Record{Kind: RecordEvent, At: ev.At, Event: &ev}`. On failure return `fmt.Errorf("record dropped signal: %w", err)`.
     3. If `w.Face != nil`, call `w.Face.Emit(ev)`. This follows the `signal-rejected` emit at `watch.go:328-330`.
     4. Return `fmt.Errorf("signal %d: %w", sig.Seq, errSignalDropped)`.

     `Handle` (`watch.go:292-298`) is unchanged: the error becomes `(false, "signal N: the signal queue is full; signal dropped")`.

3. **`internal/core/phasecheck.go` (modify)**, serves #6 c3.
   - In `Run`'s `case err := <-errc:` (`phasecheck.go:43-47`), check `if !c.Dog.live() { return CheckOutcome{Kind: phaseCheckSkipped, Reason: "watchdog unreachable"} }` first, then keep the existing timeout-on-error and ran outcomes. This covers both ways the check meets a vanished watchdog:
     - its own prompt failed and `lost` ran;
     - it waited on the send lock while the drain goroutine marked the watchdog gone, so `promptWith` returned nil early (`watchdog.go:242-244`).

4. **`internal/core/loop.go` (modify)**, serves #6 c2 and #27 c1–c3.
   - Add `Reply chan string` to `Restart` (`loop.go:37-40`), and add:
     ```go
     func (rs Restart) answer(reason string) {
         if rs.Reply != nil {
             rs.Reply <- reason
         }
     }
     ```
     `Reply` is buffered (capacity 1) by `Remedies`, so `answer` never blocks. A nil `Reply` is a restart with nobody waiting, as in `loop_events_test.go:226`.
   - In `awaitRestart` (`loop.go:505-529`), answer every restart received on `Restarts()`:
     - `rs.Step != key`: `rs.answer(fmt.Sprintf("phase-%s/%s attempt %d is not waiting for a restart", rs.Step.Phase, rs.Step.Kind, rs.Step.Attempt))`, then `continue`.
     - `drainHalts` true: `rs.answer("run halted: " + out.Reason)` before returning.
     - Restart limit: `rs.answer(reason)`, where `reason := fmt.Sprintf("restart limit %d reached", l.MaxRestarts)` is the string already put in the `restart-refused` event.
     - Success: `rs.answer("")` after the `restart` event is emitted (`loop.go:528`), just before `return next, true, false`.
   - Keep the select's case order: `Signals()` is evaluated before `Restarts()`. The pending-halt test depends on Go evaluating select operands in source order.
   - In `runPhase`, directly after the `checkPhase` block (`loop.go:246-249`), add:
     ```go
     if l.dogGone(n) {
         return "check", Outcome{State: StepFailed, Reason: "watchdog: " + watchdogGone, Halted: true}, false
     }
     ```
     This follows `runStep`'s guard (`loop.go:660-662`). `Run` then blocks the phase, and the next iteration's `dogGone` (`loop.go:155`) breaks, so the run exits 5 with reason `watchdog: the watchdog is gone` (`loop.go:178-179`). This also covers a resumed phase whose steps are all ok, which would otherwise go straight to land without passing through `runStep`.

5. **`internal/core/remedies.go` (modify)**, serves #27 c1–c3.
   - Replace the send at `remedies.go:186-192` with:
     ```go
     reply := make(chan string, 1)
     select {
     case r.Watch.restarts <- Restart{Step: key, Addendum: addendum, Provider: provider, Remedy: remedy, Reply: reply}:
     case <-closed:
         return false, "run halted"
     }
     reason := <-reply
     return reason == "", reason
     ```
     The loop answers on every path after it receives (step 4), and nothing between its receive and its answer blocks, so the receive on `reply` is bounded.

6. **Tests** (see `## Tests`). They also need these existing tests adjusted, which the Restart handshake and #6 c3 require:
   - `internal/core/remedies_test.go`:
     - Add `func takeRestart(w *Watch) <-chan Restart`. It starts a goroutine that receives one `Restart`, sends `""` on its `Reply`, sets `Reply = nil` and forwards it on the returned channel (capacity 1). Clearing `Reply` keeps struct comparisons working.
     - Replace each `go func() { <-w.Restarts() }()` and each `got := make(chan Restart, 1); go func() { got <- <-w.Restarts() }()` with `takeRestart(w)` or `got := takeRestart(w)`, at `remedies_test.go:117-118` and `:200`.
   - `internal/core/unattended_test.go`: make the same replacement at lines 22-23, 39, 63, 118, 131, 144-145, 178 and 204.
   - `internal/core/watch_test.go:362,380`: leave these as they are. Their `Restart` is refused before any send.
   - `internal/core/phasecheck_test.go:373-399` (`TestACheckTimeoutIsRecordedWithoutAHalt`): see `## Tests`.

## Tests

Write these first. All are in package `core` and use the existing fakes.

**`internal/core/watchdog_test.go`**

Add two helper types:
- `vanishingHost struct { fakeSessionHost; gone atomic.Bool }`. When `gone`, `Prompt` returns `errors.New("herdr agent prompt: herdr: agent_not_found: no agent named rloop-wd-run-1")`; otherwise it defers to `fakeSessionHost.Prompt`. When `gone`, `State` returns `AgentGone, nil`; otherwise it defers to `fakeSessionHost.State`.
- `vanishingLander struct { *fakeLander; host *vanishingHost }`. Its `Land` calls `fakeLander.Land`, then sets `host.gone`, and never prompts the watchdog.

1. **`TestAWatchdogWhoseAgentIsNotFoundIsMarkedGoneOnce`** covers #6 c1. It has subtests `Post` and `Notify`.
   - Setup: a `vanishingHost` with `gone` set, `store := &fakeStore{}`, `face := &fakeFace{}`, and `dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})` with `dog.Face = face`. `dog.OnGone` increments an `atomic.Int32` and sends on a `chan struct{}` of capacity 2.
   - `Post`: call `dog.Post("a")`, wait for the `OnGone` signal (fail after 2 s), call `dog.Post("b")`, then `dog.Stop()` to drain.
   - `Notify`: call `dog.Notify("a", false, 0)` (it returns an error containing `agent_not_found`), then `dog.Notify("b", false, 0)`.
   - Both subtests assert:
     - the `OnGone` count is 1;
     - `recordedKinds(store)` and `emittedKinds(face)` both equal `[]string{"watchdog-unreachable"}`;
     - `dog.Gone()` is true;
     - exactly one `SessionHost.Prompt` call was made.
2. **`TestATransientPromptErrorLeavesTheWatchdogLive`** covers #6 c4. It uses `checkHost` (`phasecheck_test.go:69-81`) with `err = errors.New("herdr agent prompt: timed out after 30s")`, in two subtests:
   - `agent idle`: `States` maps `rloop-wd-run-1` to `AgentIdle`;
   - `state unreadable`: `Err = errors.New("herdr: connection refused")`.

   Each subtest counts `OnGone` calls and calls `dog.Notify("a", false, 0)` twice. It asserts:
   - both calls return an error containing `timed out`;
   - `dog.live()` is true;
   - `store.Records["run-1"]` is empty;
   - `OnGone` count is 0;
   - two prompts were made.
2a. **`TestAnAgentNotFoundDuringStopDoesNotFireOnGone`** covers the invariant that `Stop` never fires `OnGone` (`docs/task-loop-driver/todo.md:442`).
   - Add the type `stallingHost struct { fakeSessionHost; entered, release chan struct{} }`. `Prompt` closes `entered`, waits on `release`, then returns `errors.New("herdr agent prompt: herdr: agent_not_found: no agent named rloop-wd-run-1")`. `State` returns `AgentGone, nil`.
   - Setup: `host := &stallingHost{entered: make(chan struct{}), release: make(chan struct{})}`, `store := &fakeStore{}`, `dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})`; `dog.OnGone` increments an `atomic.Int32`.
   - Action: `dog.Post("a")`; wait on `entered` (fail after 2 s); run `dog.Stop()` in a goroutine; poll every 1 ms, for at most 2 s, until `dog.mu`-guarded `dog.stopping` is true; `close(host.release)`; wait for `Stop` to return (fail after 2 s).
   - Assertions: `OnGone` count is 0; `recordedKinds(store)` holds no `watchdog-unreachable`.

3. **`TestAWatchdogPaneKilledDuringLandHaltsAtTheNextPhasesCheckBeforeAnyStep`** covers #6 c2 and c3. This is the ordering test the watchdog asked for.
   - Setup:
     - `r := newLoopRig(t)` with a `vanishingHost`;
     - `dog := newWatchdog(host, &r.store.fakeStore, ProviderArgs{Kind: "claude"})`, `dog.Face = r.face`;
     - `w := &Watch{Store: r.store, Face: r.face, PhaseCheck: &PhaseCheck{Dog: dog, Repo: r.repo, Timeout: time.Minute}}`. `Dog` is deliberately unset so no async Post can race the check;
     - `dog.OnGone` counts calls, then calls `w.WatchdogGone()`;
     - `r.loop.Watcher = w`, `r.loop.Lander = &vanishingLander{fakeLander: r.lander, host: host}`;
     - `code := r.run(RunOptions{})`.
   - Assertions:
     - `code == 5`;
     - the phases of `r.events("phase-start")` equal `["1","2"]`;
     - `r.calls("Land ")` equals `["1"]`;
     - no `RecordStep` record in `r.store.Records["run-1"]` has `Step.Phase == "2"`;
     - `r.events("phase-check-timeout")` is empty;
     - `r.events("phase-check-skipped")` has exactly one event, with `Phase == "2"` and `Fields["reason"] == "watchdog unreachable"`;
     - `watchdog-unreachable` appears once in `r.kinds()`, before that `phase-check-skipped`, which comes before `halt`;
     - `OnGone` count is 1;
     - the last `r.runRecords()` entry has `Reason == "watchdog: the watchdog is gone"`.
4. **`TestAGoneWatchdogsHaltDoesNotBlockAPhaseCheckBehindAFullSignalQueue`** covers #26 c3 and c4.
   - Setup:
     - `store := &fakeStore{}`, `w := newWatch(store)`, `w.StepStarted(implementRef(2, 1), nil)`;
     - 64 calls to `w.Accept(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: implementRef(2, 1).Key, Reason: "w"})`;
     - `host := &goneHost{}` with `host.gone.Store(true)`, `dog := newWatchdog(host, store, ProviderArgs{Kind: "claude"})`, `dog.OnGone = w.WatchdogGone`, `defer dog.Stop()`.
   - Action: call `dog.Post("step ended phase-2/implement failed")`, then run `dog.Notify("check phase 3", true, time.Minute)` in a goroutine.
   - Assertions:
     - Notify returns within 2 s (on the base code it deadlocks behind the drain goroutine);
     - after that, 64 calls to `receive(t, w)` return warns;
     - the 65th returns `Kind == SignalHalt`, `Reason == "the watchdog is gone"`, `Step == implementRef(2, 1).Key`.

**`internal/core/watch_test.go`**

5a. **`TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull`** covers #26 c4 for the held-halt path.
   - Setup: `store := &fakeStore{}`, `w := newWatch(store)`, `w.StepStarted(implementRef(2, 1), &Session{})`; 64 `w.Accept` warns (`SourceWatchdog`, step `implementRef(2, 1).Key`, reason `"w"`), with nothing reading `Signals()`; `w.StepEnded(implementRef(2, 1), Outcome{State: StepOK})`; `w.Accept(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Phase: "2", Kind: "implement"}, Reason: "too late"})`, which is rejected and held as in `TestARejectedWatchdogSignalBetweenStepsHaltsTheNextStep` (`watch_test.go:318`).
   - Action: `next := StepRef{Key: StepKey{Run: "run-1", Phase: "3", Kind: "plan", Attempt: 1}}`, `w.StepStarted(next, &Session{})`, `defer w.StepEnded(next, Outcome{State: StepOK})`.
   - Assertions: 64 `receive(t, w)` calls give warns; the 65th gives `Kind == SignalHalt`, `Step == next.Key`, `Reason == "watchdog signal rejected: phase-2/implement is ok"`; then `noSignal(t, w)`.

5. **`TestASignalOnAFullQueueReturnsAtOnceAndTheWarnIsReportedDropped`** covers #26 c1.
   - Setup: `w := newWatch(store)` and `w.StepStarted(implementRef(2, 1), nil)`, with nothing reading `Signals()`. 64 `w.Handle` warns (`SourceWatchdog`, step `implementRef(2, 1).Key`) each return `true`.
   - Action: a 65th `Handle` warn, called in a goroutine.
   - Assertions:
     - it returns within 2 s with `ok == false` and a reason containing `the signal queue is full`;
     - `store.Records["run-1"]` holds exactly one `RecordEvent` with `Kind == "signal-dropped"` and `Fields["seq"] == "65"`.
6. **`TestAHaltOnAFullQueueIsDeliveredAfterTheQueuedSignals`** covers #26 c1 and c4.
   - Setup: the same 64 warns as test 5.
   - Action: `w.Handle` a halt (`SourceWatchdog`, same step, reason `"wrong turn"`).
   - Assertions:
     - it returns `true, ""` within 2 s;
     - 64 `receive` calls give warns, the 65th gives the halt with reason `wrong turn`, then `noSignal(t, w)`;
     - no `signal-dropped` event was recorded.
7. **`TestASignalAfterTheLoopReturnsDoesNotBlock`** covers #26 c2.
   - Setup: `r := newLoopRig(t)`, `r.host.behaviour["rloop-p1-implement"] = "fail"`, `w := &Watch{Store: r.store, Face: r.face, Poll: time.Hour}`, `r.loop.Watcher = w`.
   - Action: `r.run(RunOptions{Phases: []string{"1"}})` (non-zero exit; phase 1 blocked). Then, in a goroutine, 65 `w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "implement"}, Reason: "late"})` calls, as the MCP `signal` tool makes them. Each is accepted because the step ended within `Poll`.
   - Assertions: all 65 return within 2 s, and the 65th is `false` with the dropped reason.

**`internal/core/phasecheck_test.go`**

8. **`TestACheckTimeoutIsRecordedWithoutAHalt`** (modified) covers #6 c3 and c4 at the check level.
   - Drop the `"blocked and gone"` case: with the watchdog gone, the check is now the skipped case that test 3 pins.
   - Drop the `r.dogHost.States = …AgentGone` line.
   - Keep one plain body with `r.dogHost.err = errors.New("herdr agent prompt: herdr: timeout: no answer within 10m0s")`. `State` then returns `AgentUnknown`, so the watchdog stays live.
   - Keep the existing assertions: exit 0, one `phase-check-timeout` for phase 1, phase 1 landed, and the `timed out — landed` report line.

**`internal/core/remedies_test.go`**

9. **`TestARestartTheLoopRefusesIsNotAcceptedWithTheLoopsReason`** covers #27 c2.
   - Setup: `failedImplement`, and `rem := newRemedies(w, store, "restart")`. A goroutine plays the loop: `rs := <-w.Restarts(); rs.Reply <- "run halted: watchdog: wrong turn"`.
   - Assertion: `rem.Restart("phase-2/implement", "", "", "")` returns `false, "run halted: watchdog: wrong turn"`.
10. **`TestARestartTheLoopRefusesAtItsRestartLimitIsNotAccepted`** covers #27 c1 and c2 end to end.
    - Setup: a copy of `TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum` (`remedies_test.go:279`) with `r.loop.MaxRestarts = 0`. `newRemedies` keeps `MaxRestarts: 2`, so Remedies' own limit passes and the loop refuses.
    - Assertions:
      - the restart reason is `restart limit 0 reached` and `ok == false`;
      - the exit is 1;
      - `r.agents()` equals `["rloop-p2-plan", "rloop-p2-implement"]`;
      - `r.events("restart")` is empty;
      - `r.events("restart-refused")` has length 1.
11. **`TestARestartAfterAnAuthorisedRestartRemedyIsQueuedForTheHeldStep`** (modified to use `got := takeRestart(w)`) covers #27 c1. `ok` is true only after the loop's `""` answer, and the forwarded `Restart` equals the `want` value.
12. **`TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum`** (existing, unchanged) covers #27 c3: `reason == ""` and attempt 2 is spawned with the addendum.

**`internal/core/loop_events_test.go`**

13. **`TestARestartTheLoopTakesWhileAHaltIsPendingIsAnsweredWithTheHalt`** covers #27 c2 (the pending-halt refusal).
    - Add the type `lateHaltWatcher struct { *fakeWatcher; armed atomic.Bool; halts chan Signal }`:
      - `Signals()` returns `halts` once `armed`, else nil;
      - `Restarts()` sets `armed` and returns `fakeWatcher.restarts`.
    - Setup:
      - `r := newEventsRig(t)`;
      - `r.loop.RemedyWindow = time.Minute`, `r.loop.Sessions.Poll = time.Hour`, `r.loop.runDir = r.store.Dir("run-1")`, `r.loop.restarts = map[string]int{}`;
      - `key := StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}`;
      - `w := &lateHaltWatcher{fakeWatcher: r.watcher, halts: make(chan Signal, 1)}`, with the halt `{SignalHalt, SourceWatchdog, key, "wrong turn"}` in `halts`;
      - `reply := make(chan string, 1)`, and `r.watcher.restarts <- Restart{Step: key, Addendum: "again", Reply: reply}`;
      - `r.loop.Watcher = w`;
      - `kind` is the entry of `r.loop.Kinds` named `implement`, and `out := Outcome{State: StepFailed, Reason: "backstop"}`.
    - Action: `_, ok, aborted := r.loop.awaitRestart(context.Background(), StepRef{Key: key, Kind: kind}, kind, &out)`.
    - Assertions:
      - `ok == false` and `aborted == false`;
      - `<-reply == "run halted: watchdog: wrong turn"`;
      - `out.Halted` is true;
      - `r.events("restart")` is empty.
14. **`TestAStaleRestartIsAnsweredAsNotWaiting`** covers #27 c1 and c2 for a restart the loop receives but does not take.
    - Setup: like `TestAStaleRestartForAnEarlierAttemptIsIgnored` (`loop_events_test.go:711`), with a `RemedyWindow` of 30 ms and implement attempts 1 and 2 failing. The `ended` hook, on attempt 1, sends two restarts for `ref.Key`: the first with `first := make(chan string, 1)`, the second with `stale := make(chan string, 1)`.
    - Assertions:
      - the exit is 1;
      - `<-first == ""`;
      - `stale` holds `"phase-2/implement attempt 1 is not waiting for a restart"` (non-blocking read that fails when empty);
      - the agents are plan, implement and implement-a2.

## Left out

- **A liveness probe in `Watch.Gone()`:** the watchdog chose no probe (q2); detection happens at the next Post, Notify or check prompt.
- **A "loop done" channel on `Watch` closed when `Run` returns:** once `forward` never blocks, #26 c2 holds without it.
- **A bounded-wait timeout in `forward`:** a non-blocking send plus a parked halt meets "bounded" and "never lost" without a timer to tune.
- **Moving the `OnGone` call outside the send lock:** with a non-blocking `forward`, `OnGone` can no longer block, which is what #26 c3 asks for.
- **Rendering `signal-dropped` in the plain or TUI face:** no criterion asks for it. The drop is recorded and returned to the caller.
- **A `restart-refused` event for the pending-halt refusal:** the halt itself blocks the phase and is reported. The caller gets the reason on `Reply`.
- **Changes to `askmcp/watchdog.go`:** `restart_step` and `signal` already pass the handler's `(bool, string)` through (`internal/askmcp/watchdog.go:127-128,161-162`).
- **Adding `Reply` to the existing `fakeWatcher` sends in `loop_events_test.go`:** `answer`'s nil check covers restarts nobody waits on.

## Assumptions

- **What "before the next phase starts" (#6 c2) means:** no step of the next phase is spawned and it never lands. When that phase's check prompt is the first contact after the pane died, its `phase-start` and `phase-check-skipped` events are recorded before the halt. The watchdog confirmed this (q2).
- **How a check whose watchdog vanished is reported:** `phase-check-skipped` with reason `watchdog unreachable`. A check cut short by `Watchdog.Stop` (the watchdog is also not live) is reported the same way.
- **The drop event:** kind `signal-dropped`, fields `seq`, `kind`, `source` and `reason`. The caller's reason is `signal <seq>: the signal queue is full; signal dropped`.
- **The loop's refusal reasons:**
  - pending halt: `run halted: <outcome reason>`, e.g. `run halted: watchdog: wrong turn`;
  - limit: `restart limit <N> reached`;
  - wrong step: `phase-<N>/<kind> attempt <A> is not waiting for a restart`.
- **Transient `State` failures:** when the prompt error is not `agent_blocked` and `State` itself fails, the prompt's error is returned and the watchdog stays live.

## Gate

`go test ./internal/core/ -count=1 -run '^(TestAWatchdogWhoseAgentIsNotFoundIsMarkedGoneOnce|TestATransientPromptErrorLeavesTheWatchdogLive|TestAWatchdogPaneKilledDuringLandHaltsAtTheNextPhasesCheckBeforeAnyStep|TestAGoneWatchdogsHaltDoesNotBlockAPhaseCheckBehindAFullSignalQueue|TestAnAgentNotFoundDuringStopDoesNotFireOnGone|TestAHaltHeldBetweenStepsIsDeliveredToTheNextStepWhenTheQueueIsFull|TestASignalOnAFullQueueReturnsAtOnceAndTheWarnIsReportedDropped|TestAHaltOnAFullQueueIsDeliveredAfterTheQueuedSignals|TestASignalAfterTheLoopReturnsDoesNotBlock|TestACheckTimeoutIsRecordedWithoutAHalt|TestARestartTheLoopRefusesIsNotAcceptedWithTheLoopsReason|TestARestartTheLoopRefusesAtItsRestartLimitIsNotAccepted|TestARestartAfterAnAuthorisedRestartRemedyIsQueuedForTheHeldStep|TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum|TestARestartTheLoopTakesWhileAHaltIsPendingIsAnsweredWithTheHalt|TestAStaleRestartIsAnsweredAsNotWaiting)$'`
