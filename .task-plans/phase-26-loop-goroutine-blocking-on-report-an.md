status: planned

## Summary

`RecordGuard`, the core `Store` wrapper that the app already puts in front of the loop, sessions and land gate, becomes the run's live view. Once `Follow(runID)` is called, it loads the run from disk once. From then on it folds every successful `Append` into an in-memory `RunState` and wakes a channel. The app routes every runtime appender through it: `Watch`, `Remedies`, `Watchdog` and the ask server.

With that view in place:

- **Report writer.** `RunLoop.Run` starts a background report writer. The writer renders `report.md` from the view's snapshot, never from `Store.Load`, and rewrites it at most once per `reportEvery`. Total store reads per run are the one `Follow` load plus one disk load per final write. That is O(N) however events are spaced.
- **Emits.** `emit`/`emitFields`/`setRun`/`show` no longer hold `l.mu` and no longer touch the report.
- **Landed phases.** A landing is visible in the view the moment `LandGate` appends it. `Watch.rejection` and `RunLoop.haltEnded` ask `RecordGuard.Landed` instead of loading. So does askmcp's `resolve`, through `RecordGuard.Latest` for attempts.
- **Report writes.** Each write is atomic (temp file, then rename), so a hook reading `R_LOOP_REPORT` never sees a torn file. `Run` does a disk-loaded write before each end-of-run hook. On return it first waits for its question workers and its hooks, then writes from disk once more. `Wiring.Execute` writes once more after the watchdog has stopped and the ask server has drained, so the report on exit holds every record. That write also covers a run that ends in unblock or triage and never reaches `Run`.
- **Hooks.** `fire` runs each hook on its own goroutine, chained so hooks stay ordered and never overlap. `Run` waits for the chain before it returns.

Choices:
- **Where the live view lives:** in the existing `RecordGuard`, chosen over a new wrapper type. `RecordGuard` already wraps every loop, session and land append (`wire.go:424`), so the change adds no type and only widens its wiring. A loop-private view would miss appends from `Watch`, `Watchdog`, `Remedies` and askmcp, which is finding codex-r1-2.
- **Report source:** the view's snapshot, chosen over a debounced `Store.Load`. A debounced load is still O(N²) reads when events arrive more than `reportEvery` apart (codex-r1-1).
- **`Load` behaviour:** unchanged (it still reads disk), chosen over serving every `Load` from the view. The app still appends through the bare store before `Run` (`resume.go:139`, `resume.go:481-612`, `unblock.go:147`), so a cached `Load` could go stale for checks, land and sessions.
- **When the final report is written:** in `Run` before end hooks and on return, and again in `Execute` after `Dog.Stop` and `Ask.Wait`. This was chosen over only the `Run` write, which misses late appends (codex-r1-3), and over only the `Execute` write, which would give end hooks an incomplete report.
- **Where the landed state comes from:** the view, updated at append time. This was chosen over a set updated after `Land` returns, which leaves a window during `Boundary.After` (codex-r1-4).
- **Hook execution:** chained goroutines, chosen over one free goroutine per hook, which would reorder and overlap hooks.
- **Lock scope for emits:** no lock, chosen over a separate emit mutex, which would still queue emitters behind a blocked `Face.Emit`. Store and faces serialise internally (`store.go:104`, `plain.go:21`, `tui/model.go:469`).

## Changes

1. **Modify `internal/core/types.go`** (DRY fold for the view; serves codex-r1-1, codex-r1-2 and #25 "reflects every appended record").
   - Add `func (st *RunState) Apply(rec Record) error`. Move the body of `store.apply`'s switch (`store.go:220-253`) here, with the same nil checks and errors: `"step record without a step"` and `unknown record kind %q`. It initialises `st.Steps` when nil and calls `st.Span` for step records.
   - Move `upsertQuestion` from `store.go:257` into this file.

2. **Modify `internal/store/store.go`.**
   - `apply` (`store.go:220`) becomes `if err := st.Apply(rec); err != nil { return err }; if rec.Kind == core.RecordStep { *order = append(*order, *rec.Step) }; return nil`.
   - Delete the store's `upsertQuestion`.
   - `Load` behaviour is unchanged.

3. **Modify `internal/core/records.go`: `RecordGuard`** (#25 all criteria, #32 "signal returns promptly").
   - Fields: `Store`, `mu sync.Mutex`, `err error`, `runID string`, `view *RunState` and `changed chan struct{}`.
   - `Append` holds `g.mu` across the inner `g.Store.Append` and the fold, so `Follow`'s load and the fold never double-apply or drop a record. On error, it keeps the existing `FatalRecord` bookkeeping. On success, with `g.view != nil && runID == g.runID`, it calls `g.view.Apply(rec)`, ignoring the error because the record was already accepted by the store. It then does a non-blocking send on `g.changed`.
   - `func (g *RecordGuard) Follow(runID string) (<-chan struct{}, error)`: under `g.mu`, if already following `runID`, it returns the existing channel. Otherwise it calls `g.Store.Load(runID)`. On error it returns the error and leaves the view unset. On success it sets `runID`, `view` and `changed = make(chan struct{}, 1)` and returns `changed`.
   - `func (g *RecordGuard) Snapshot() (RunState, bool)`: under `g.mu`, returns a copy with `maps.Clone` of `Steps` and `Spans` and `slices.Clone` of `Landed`, `Questions`, `Signals`, `Remedies`, `Events` and `Warnings`. It returns false when not following.
   - `func (g *RecordGuard) Landed(phase string) bool`: under `g.mu`, returns true when the view holds a landing for `phase`, and false when not following.
   - `func (g *RecordGuard) Latest(phase, kind string) (int, bool)`: under `g.mu`, returns the highest `Attempt` among the view's `Steps` keys with that phase and kind, with `true`. It returns `(0, false)` when not following.
   - `recordFailed` (`records.go:57`) is unchanged.

4. **Modify `internal/core/watch.go`** (#25 signal criterion, codex-r1-4).
   - `rejection` (`watch.go:488`) becomes `rejection(sig *Signal) string`, and the call at `watch.go:367` becomes `w.rejection(&sig)`.
   - Replace the `w.Store.Load(runID)` block (`watch.go:507-513`) with `if g, ok := w.Store.(*RecordGuard); ok && g.Landed(sig.Step.Phase) { return fmt.Sprintf("phase %s has landed", sig.Step.Phase) }`. The text and the order of checks stay the same.

5. **Modify `internal/core/loop.go`: guard, landed, report** (#25 all, codex-r1-1 to codex-r1-4).
   - In `Run`, after `l.runDir = l.Store.Dir(l.RunID)` (`loop.go:133`), add `if _, ok := l.Store.(*RecordGuard); !ok { l.Store = &RecordGuard{Store: l.Store} }`. The app already passes a guard, and core tests pass bare fakes. `recordFailed` keeps working on the same guard.
   - `haltEnded` (`loop.go:729`): replace the `Store.Load` condition with `if g, ok := l.Store.(*RecordGuard); ok && g.Landed(sig.Step.Phase) {`. The body stays the same.
   - Change `var reportEvery = time.Second`: it is a package variable so tests can shorten it. Add fields `reportStop chan struct{}`, `reportDone chan struct{}` and `reportClose func()`.
   - `emit` (`loop.go:1579`), `emitFields` (`loop.go:1508`) and `setRun` (`loop.go:1598`): remove `l.mu` lock/unlock and remove `l.writeReport()`. Append, the warning on append error, and `Face.Emit` stay as they are. In `show` (`loop.go:1590`), remove the lock and `l.writeReport()`.
   - `startReport()`:
     ```go
     func (l *RunLoop) startReport() {
         g, _ := l.Store.(*RecordGuard)
         changed, err := g.Follow(l.RunID)
         if err != nil {
             l.Face.Emit(Event{At: time.Now(), Kind: "warning", Fields: map[string]string{"reason": "report: " + err.Error()}})
             return
         }
         stop, done := make(chan struct{}), make(chan struct{})
         go func() {
             defer close(done)
             for {
                 select {
                 case <-stop:
                     return
                 case <-changed:
                 }
                 if st, ok := g.Snapshot(); ok {
                     l.renderReport(st)
                 }
                 select {
                 case <-stop:
                     return
                 case <-time.After(reportEvery):
                 }
             }
         }()
         l.reportClose = sync.OnceFunc(func() {
             close(stop)
             <-done
         })
     }
     ```
     `startReport` is called only after `Run` has made `l.Store` a guard. Core unit tests set `l.Store = &RecordGuard{Store: ...}` themselves.
   - `func (l *RunLoop) closeReport()`: calls `l.reportClose()` when it is non-nil, then `l.writeReport()`.
   - `func (l *RunLoop) WriteReport()`: returns when `l.RunID == ""` (no run was bound, `wire.go:598-601`). When `l.runDir == ""`, it sets `l.runDir = l.Store.Dir(l.RunID)` under `l.mu`, as `ServeQuestions` does (`loop.go:1000-1002`). This is the case for a run that `Wiring.run` finishes in unblock or triage without calling `Run` (`wire.go:376-386`). It then calls `l.writeReport()`.
   - `writeReport()` loads from disk: `st, err := l.Store.Load(l.RunID)`, then `l.renderReport(st)` on success, or a `report: ` warning on error.
   - `var writeReportFile = writeFileAtomic`: a package variable, like `reportEvery`, so a test can block the file write.
   - `renderReport(st RunState)`: runs `writeReportFile(l.reportPath(), []byte(Report(st, l.Plan)))` inside `quietly` (`panics.go:16`), so a panic on the writer goroutine becomes a `report: ` warning. It emits a `report: ` warning on any error.
   - `func writeFileAtomic(path string, data []byte) error`: follows the shape of `store.go:308` `writeAtomic` (temp file in the same directory, write, close, rename, remove the temp file on any error), but keeps the file mode that `os.WriteFile(path, data, 0o644)` gives today. The temp is `path + "." + rand.Text() + ".tmp"` (`crypto/rand`), opened with `os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)`, so the process umask applies to a new report. When `os.Stat(path)` succeeds, the temp file is `Chmod`ed to that file's `Mode().Perm()` before the rename, so an existing `report.md` keeps its mode. There is no unconditional `Chmod(0o644)`. The random name lets writers from different goroutines never share a temp file.
   - `func (l *RunLoop) endReport()`: `l.questions.Wait()`, then `l.waitHooks()`, then `l.closeReport()`. Question workers go first because one can halt the run and so fire a hook. The last write comes after both, so a record appended while an end hook or a question worker still runs is in the report when `Run` returns.
   - In `Run`, directly after the `if opts.Resume { ... }` block (`loop.go:141-145`), add `l.startReport()` then `defer l.endReport()`. This defer is registered before `defer cancel()` of the question context (`loop.go:154-157`), so it runs after that cancel and the question server has been told to stop.
   - Call `l.closeReport()` right before each end-of-run hook: `Run` before the OnDone fire (`loop.go:222`), `Run` before the OnHalt fire (`loop.go:231`), `recordHalt` after `setRun` (`loop.go:235`), and `ended` before its OnHalt fire (`loop.go:980`). `closeReport` stays safe to call again: `sync.OnceFunc` guards the stop, and each call writes the disk state again.

6. **Modify `internal/core/loop.go`: question workers** (#25 "reflects every appended record once the run ends").
   - Add field `questions sync.WaitGroup`.
   - `ServeQuestions` (`loop.go:993`): inside the `l.mu` section, before `go l.serveQuestions(ctx)`, call `l.questions.Add(1)`.
   - `serveQuestions` (`loop.go:1006`): the first statement is `defer l.questions.Done()`, so it runs after the existing recover handler. `go l.question(ctx, q)` (`loop.go:1022`) becomes `l.questions.Add(1)` followed by `go func() { defer l.questions.Done(); l.question(ctx, q) }()`. Each `Add` happens while the server's own count is held, so it never races `Wait`.
   - Every worker path ends once the context is cancelled: `askingSession` returns on `ctx.Done()` (`loop.go:1313-1315`), and `Watch.Route` does no waiting (`watch.go:196-206`, `questions.go:31-51`).

7. **Modify `internal/core/loop.go`: hooks** (#32 all criteria).
   - Add field `hooksDone chan struct{}`, guarded by `l.mu`.
   - `fire` (`loop.go:1621`) builds the env map exactly as today, then chains:
     ```go
     l.mu.Lock()
     prev, done := l.hooksDone, make(chan struct{})
     l.hooksDone = done
     l.mu.Unlock()
     go func() {
         defer close(done)
         if prev != nil {
             <-prev
         }
         if err := quietly(func() { l.Notifier.Fire(hook, env) }); err != nil {
             l.Face.Emit(Event{At: time.Now(), Kind: "notify-failed", Fields: map[string]string{"hook": hook, "status": status, "reason": err.Error()}})
         }
     }()
     ```
     The `l.Notifier == nil` early return stays.
   - `waitHooks()` reads `l.hooksDone` under `l.mu` and, when it is non-nil, waits on it. Each hook stays bounded by `notify.Shell.Timeout` (`notify.go:22-27`).

8. **Modify `internal/askmcp/watchdog.go`** (#25 signal criterion on the real MCP path, codex-r1-5).
   - In `resolve` (`watchdog.go:262`), before the `s.Store.Load` call (`watchdog.go:271`): `if g, ok := s.Store.(*core.RecordGuard); ok { if n, following := g.Latest(key.Phase, key.Kind); following { key.Attempt = n; return key, nil } }`. The existing `Load` path stays for a store that is not a following guard, which is the window before `Run` calls `Follow`.

9. **Modify `internal/app/wire.go`** (codex-r1-2, codex-r1-3).
   - Move `w.records = &core.RecordGuard{Store: w.Store}` (`wire.go:424`) above `w.Ask = ...` (`wire.go:432`), and build `w.Ask = &askmcp.Server{Store: w.records}`.
   - `Watch` (`wire.go:509`), `Remedies` (`wire.go:519`) and `Watchdog` (`wire.go:520`) get `Store: w.records`.
   - In `Execute`'s deferred shutdown (`wire.go:301-313`), call `w.Loop.WriteReport()` right after `w.Ask.Wait()`, before `w.release()` and `w.Face.Close()`.
   - App-level appends through `w.Store` before `Run` (`resume.go`, `unblock.go`) stay as they are: `Follow` in `Run` loads them.

10. **Modify `internal/core/fakes_test.go`.**
   - `fakeFace` gets `mu sync.Mutex`, taken in `Emit`, plus `func (f *fakeFace) seen(kind string) bool` under the same lock.
   - `fakeStore.Load` folds with `st.Apply(rec)`, keeping its own `LastStep` assignment.
   - Delete `fakes_test.go`'s `upsertQuestion`, since core now has one.

11. **Modify `internal/core/watch_test.go`.** `TestASignalForALandedPhaseIsRejected` (`watch_test.go:414`) wraps the store: `g := &RecordGuard{Store: store}`. It calls `g.Follow("run-1")` after the landing append, and builds the watch with `newWatch(store)` followed by `w.Store = g`. Assertions stay the same.

12. **Modify `internal/core/loop_events_test.go`.** `TestAPanicAnsweringALandStageQuestionIsRecordedWithoutACrash` (`loop_events_test.go:255`) calls `r.loop.WriteReport()` before the `os.Stat(... "report.md")` check.

13. **Create `internal/core/loop_io_test.go`** with the core tests below and these fakes:
    - `countingStore`: embeds `loopStore` and has `loads, read atomic.Int64`. `Load` adds 1 and `len(s.Records[runID])`, read under `s.mu`, then delegates.
    - `gatedQuestionStore`: embeds `loopStore` and has `entered, gate chan struct{}` and `once sync.Once`. `Append` of a `RecordQuestion` closes `entered` once and waits on `gate`, then delegates.
    - `withReportWriter(t, fn)`: sets `writeReportFile = fn` and restores it in `t.Cleanup`.
    - `gateFace`: embeds `*fakeFace` and has `entered, gate chan struct{}`. For an event of kind `"stuck"`, `Emit` closes `entered` and waits on `gate`. Every event is then passed to `fakeFace.Emit`.
    - `drainWatcher`: embeds `nopWatcher` and has `sigs []Signal`. `Drain()` returns and clears `sigs`.
    - `gatedNotifier`: embeds `*fakeNotifier` and has `block string`, `gate chan struct{}`, `reports map[string]string` and `mu sync.Mutex`. `entered chan struct{}` and `once sync.Once`. When `hook == block`, `Fire` closes `entered` once (when it is non-nil), then waits on `gate`. It then stores the text of `env["R_LOOP_REPORT"]` in `reports[hook]` (a read error stores `""`), then calls `fakeNotifier.Fire`.
    - `panicNotifier`: `Fire` panics with `"hook boom"`.
    - `failingPlan(r *loopRig)`: one phase, `threePhasePlan().Phases[0]`, with `Runners = {"plan-file": stepRunnerFunc(...)}` returning `Outcome{State: StepFailed, Reason: "tests red"}` (`stepRunnerFunc` at `loop_test.go:628`).
    - `withReportEvery(t, d)`: sets `reportEvery = d` and restores it in `t.Cleanup`.

14. **Modify `internal/core/records_test.go`**: add the guard tests below.

15. **Modify `internal/askmcp/watchdog_test.go`**: add the MCP test below.

16. **Modify `internal/app/watchdog_test.go`**: add the shutdown test and the MCP-signal-during-hook test below. The shutdown test uses `lateDog`, which embeds `*dogHost` and has `store core.Store` and `runID func() string`. `ClosePane` first appends `core.Record{Kind: core.RecordSignal, Signal: &core.Signal{Seq: 99, Kind: core.SignalWarn, Source: core.SourceWatchdog, Step: core.StepKey{Phase: "1", Kind: "implement"}, Reason: "late word"}}` to `store`, then delegates to `dogHost.ClosePane`.

17. **Modify `internal/notify/notify_test.go`**: add the real-shell test below.

18. **Modify `internal/app/unblock_test.go`**: add the early-finish report test below.

## Tests

Every timed wait uses `select` against `time.After`, so a regression fails the test instead of hanging it.

**`records_test.go`**
- `TestAFollowingGuardFoldsEveryAppendIntoItsView`: `store := &fakeStore{}` with one landing for phase `2` appended before. `g := &RecordGuard{Store: store}`, `ch, _ := g.Follow("run-1")`. Then `g.Append` a step record (phase 1 `plan`, attempt 1, running), a step record (phase 1 `plan`, attempt 2, running) and a landing for phase `1`. `g.Landed("1")` and `g.Landed("2")` are true, `g.Landed("3")` is false, and `g.Latest("1", "plan") == (2, true)`. `ch` is readable without blocking. The snapshot's `Landed` has length 2. Covers the view seeding from disk on follow or resume, and the fold for records appended outside the loop (#25 "every appended record").
- `TestAGuardThatIsNotFollowingKnowsNothing`: before `Follow`, `Landed` returns false, `Latest` returns `(0, false)` and `Snapshot` reports `false`. After `Follow` with the store's `Err` set, `Follow` returns the error and `Latest` still returns `(0, false)`. Covers the not-yet-following and failed-load paths.

**`watch_test.go`**
- `TestAcceptingASignalLoadsNoRunState`: `store := &countingStore{}`, `g := &RecordGuard{Store: store}` and `g.Follow("run-1")`. Then `g.Append` a landing for phase `1` and reset `store.loads`. With `w := &Watch{Store: g, Face: &fakeFace{}, Now: func() time.Time { return watchT0 }, Poll: time.Hour}` and `w.StepStarted(implementRef(2, 1), &Session{})`, a driver halt for phase 1 `implement` is rejected `phase 1 has landed`, a watchdog warn for phase 2 `implement` is accepted, and `store.loads == 0`. Covers #25 signal criterion on the Watch side, and codex-r1-4: the landing is seen the moment it is appended, before `Land` returns.

**`loop_io_test.go`**
- `TestSpacedEmitsReadTheStoreLinearly`: `withReportEvery(t, time.Millisecond)` and `store := &countingStore{loopStore: loopStore{dir: t.TempDir()}}`. `l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, RunID: "run-1", runDir: store.dir}`, then `l.startReport()`. For `i` from 1 to 100: `l.emit(Event{Kind: "human"})`, then wait (3 s cap) until `report.md` contains `human touches: i`. Then `l.closeReport()`. Asserts `store.read <= 300`; the base code reads 5 050. Covers #25 O(N) reads with refreshes spread apart (codex-r1-1).
- `TestARecordAppendedOutsideTheLoopReachesTheLiveReport`: guard over `loopStore{dir}`, `startReport()`. Then `guard.Append("run-1", Record{Kind: RecordLanding, Landing: &Landing{Phase: "9", MergeSHA: "m9"}})` with no loop emit. `report.md` contains `phase 9 m9` within 3 s, before `closeReport`. Covers #25 "bounded delay while live" for direct appenders (codex-r1-2).
- `TestTheReportCatchesUpWithinTheRefreshInterval`: the default `reportEvery`, guard over `loopStore{dir}`, `startReport()`. After `emit(human)` the report shows `human touches: 1` within 3 s. A second `emit(human)`, issued at once, shows `human touches: 2` within 3 s: the trailing refresh after the cooldown. Then `closeReport()`. Covers #25 "bounded delay while live".
- `TestABlockedFaceDoesNotHoldBackOtherEmitsOrRecords`: loop with `gateFace` and a guard over `loopStore`. `go l.emit(Event{Kind: "stuck"})`, then wait on `entered`. A goroutine calls `l.emit(note)`, then `l.emitStep(StepRef{Key: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Kind: StepKind{Name: "plan"}}, StepRunning, "", nil)`, then `l.setRun(RunRunning, "")`, and must finish within 1 s while the gate is still closed. The store then holds `note` and `step` event records and a `RecordRun` `running`. Covers #25 "lock not held during `Face.Emit`" and "step records still proceed".
- `TestASlowReportWriteDoesNotHoldBackEmits`: the loop uses a guard over `loopStore{dir}` and a `fakeFace`, with `runDir: dir`. `withReportWriter(t, fn)`, where `fn` closes `entered` once, waits on `gate`, then calls `writeFileAtomic`. `startReport()`, then `emit(human)`, then wait on `entered`: the writer is now blocked inside the report file write. A goroutine emits `human` twice, calls `l.emitStep(StepRef{Key: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Kind: StepKind{Name: "plan"}}, StepRunning, "", nil)` and `setRun(RunRunning, "")`, and must finish within 1 s while the gate is still closed. The store then holds three `human` events, the `step` event and a `RecordRun` `running`. Close `gate` and call `closeReport()`. Covers #25 "lock not held during report file I/O" and "step records still proceed".
- `TestTheLastReportWaitsForTheEndHooks`: guard over `loopStore{dir}` (after `Follow("run-1")`), `runDir: dir`, `Notifier: &gatedNotifier{fakeNotifier: &fakeNotifier{}, block: "done-hook", entered: ..., gate: ..., reports: map[string]string{}}`. `l.fire("done-hook", "finished", "", "", "")`, then wait on `entered`. `go l.endReport()`, which has not returned 100 ms later. Append `Record{Kind: RecordLanding, Landing: &Landing{Phase: "9", MergeSHA: "m9"}}` through the guard, then close the gate. `endReport` returns within 1 s, and `report.md` contains `phase 9 m9`. With the previous defer order, the last write ran before the hook finished, so the report missed that record. Covers #25 "reflects every appended record once the run ends" for appends during an end hook (codex-r2-1), and #32 "`onDone` completes before exit".
- `TestTheLastReportWaitsForAQuestionWorker`: `store := &gatedQuestionStore{loopStore: loopStore{dir}, entered: ..., gate: ...}`. Its `Append` of a `RecordQuestion` closes `entered` once and waits on `gate` before delegating. `l := &RunLoop{Plan: threePhasePlan(), Store: &RecordGuard{Store: store}, Face: &fakeFace{}, Kinds: []StepKind{{Name: "plan"}}, Ask: &fakeAskChannel{Asked: make(chan Question, 1)}, RunID: "run-1", runDir: dir}`. With `ctx, cancel := context.WithCancel(...)` and `l.ServeQuestions(ctx)`, send `Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: "1", Kind: "land"}, Text: "which db?"}` and wait on `entered`. Then `cancel()` and `go l.endReport()`, which has not returned 100 ms later. Close the gate: `endReport` returns within 1 s, and `report.md` contains `q1 phase 1 land: which db? → land-stage step (withdrawn`. Covers #25 "reflects every appended record once the run ends" for a question withdrawn during shutdown (codex-r2-4).
- `TestAReportWriteKeepsTheUmaskAndAnExistingMode`: `old := syscall.Umask(0o077)`, restored in `t.Cleanup`. `writeFileAtomic(p, "a")` on a new path gives mode `0600`. After `os.Chmod(p, 0o640)`, `writeFileAtomic(p, "b")` leaves mode `0640` and content `b`, and the directory holds only `report.md`. Covers the report's file mode matching today's `os.WriteFile(..., 0o644)` under a restrictive umask and an existing tighter mode (codex-r2-2).
- `TestWriteReportBeforeRunWritesNothing`: `(&RunLoop{Store: &loopStore{dir: t.TempDir()}, Face: &fakeFace{}}).WriteReport()`, with no `RunID`, creates no `report.md` in the directory and emits nothing. Covers `Execute` shutting down before a run was bound.
- `TestHandlingWatchdogSignalsLoadsNoRunState`: `store := &countingStore{loopStore: loopStore{dir: t.TempDir()}}` and `g := &RecordGuard{Store: store}`, then `g.Follow("run-1")`, a `g.Append` of a landing for phase 1, and a reset of `store.loads`. `l := &RunLoop{Plan: threePhasePlan(), Store: g, Face: face, RunID: "run-1", Watcher: &drainWatcher{sigs: [halt {Source: SourceDriver, Step: {Run: "run-1", Phase: "1", Kind: "implement", Attempt: 1}, Reason: "stale"}, warn {Source: SourceWatchdog, Step: {Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}, Reason: "slow"}]}}`, then `l.drainSignals()`. Asserts `store.loads == 0`, `l.haltLanded` and `l.halted != nil`. The face holds a `warning` whose reason starts with `halt for phase-1/implement after it ended` and a `warning` `slow`. Covers #25 signal criterion on the loop side.
- `TestASlowWarnHookDoesNotHoldTheLoop`: `newLoopRig`, `failingPlan(r)` and a `gatedNotifier{fakeNotifier: r.notifier, block: "warn-hook"}`. `go r.run(RunOptions{})`. `r.face.seen("halt")` within 2 s while the gate is closed. Run has not returned 100 ms later. Close the gate, and Run returns 1 within 2 s. `r.hooks() == ["warn-hook blocked", "halt-hook halted"]`, and `Fired[0]` equals exactly `{R_LOOP_RUN: run-1, R_LOOP_STATUS: blocked, R_LOOP_PHASE: 1, R_LOOP_STEP: plan, R_LOOP_REASON: tests red, R_LOOP_TODO: docs/x/todo.md, R_LOOP_REPORT: <dir>/report.md}`. `reports["halt-hook"]` contains `## Halt`. Covers #32 halt handling, "once per transition, same environment", and `onHalt` completing before return with a complete report.
- `TestAStepCompletesWhileASlowWarnHookRuns`: `newLoopRig` with a one-phase plan and `r.loop.Watcher = &fakeWatcher{log: r.shared, signals: make(chan Signal), restarts: make(chan Restart, 8)}`. The `plan-file` runner closes `planEntered`, waits on `release`, and returns `StepOK`. The `diff` runner closes `implEntered` and returns `StepOK`. Notifier: `gatedNotifier{block: "warn-hook"}`. `go r.run`, wait on `planEntered`, then send `Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1}, Reason: "slow"}` on `signals` within 1 s. Close `release`, and `implEntered` must close within 1 s while the gate is still closed. Close the gate: Run returns 0. Covers #32 "step completion is not delayed" (codex-r1-6).
- `TestAnAbortIsHandledWhileASlowWarnHookRuns`: same setup, but the `plan-file` runner waits on `stepCtx.Done()` or `release`, and returns `StepFailed` "stopped" on the context. After the warn is sent, call `r.store.MarkAbort("run-1")`. `r.face.seen("aborted")` within 1 s while the gate is still closed. Close the gate, and Run returns within 2 s. Covers #32 "abort is not delayed" (codex-r1-6).
- `TestRunWaitsForASlowDoneHook`: `newLoopRig` default (the run finishes with 0) and `gatedNotifier{block: "done-hook"}`. `go r.run`. `r.face.seen("finished")` within 5 s, and Run has not returned 100 ms later. Close the gate, and Run returns 0 within 2 s. `r.hooks()` ends with `done-hook finished`. `reports["done-hook"]` contains `## Landed`. Covers #32 "`onDone` completes before exit" with a complete report (codex-r1-7).
- `TestAWatchdogSignalIsHandledWhileAHookRuns`: `store := &loopStore{dir}`, `w := &Watch{Store: store, Face: face, Poll: time.Hour}` and `l := &RunLoop{Plan: threePhasePlan(), Store: store, Face: face, Notifier: &gatedNotifier{fakeNotifier: &fakeNotifier{}, block: "warn-hook", gate: ..., reports: map[string]string{}}, Hooks: Hooks{OnWarn: "warn-hook"}, Watcher: w, RunID: "run-1", runDir: store.dir}`. `l.fire("warn-hook", "warning", "1", "implement", "first")` returns within 1 s. After `w.StepStarted(implementRef(1, 1), &Session{})`, `w.Handle(Signal{Kind: SignalWarn, Source: SourceWatchdog, Step: StepKey{Phase: "1", Kind: "implement"}, Reason: "slow"})` returns `(true, "")` within 1 s. `l.drainSignals()` shows a `warning` `slow` while the gate is closed. Close the gate and `l.waitHooks()`. The fired reasons are `first` then `slow`. Covers #32 "signal returns promptly while a hook runs".
- `TestAPanickingHookIsReportedAndTheRunEnds`: `newLoopRig`, `failingPlan(r)` and `r.loop.Notifier = panicNotifier{}`. Run returns 1 within 2 s. There are two `notify-failed` events, statuses `blocked` then `halted`, with reasons containing `hook boom`. Covers the hook goroutine's panic path.

**`askmcp/watchdog_test.go`**
- `TestTheSignalToolResolvesTheAttemptFromTheLiveViewWithoutLoading`: `st := &memStore{steps: {phase 3 implement attempts 1 failed and 2 running}}`, `s := serveWatchdog(t, st)`, `g := &core.RecordGuard{Store: st}` and `g.Follow("run-7")`. Then `st.loadErr = errors.New("loaded")` and `s.Store = g`. A real MCP `signal` call for `phase-3/implement` returns `accepted: true` within 1 s, and the handler receives `Attempt: 2`. Any load would have failed the call with `loaded`. Covers #25 "no full store load" on the real MCP path, and #32 "signal call returns promptly" (codex-r1-5).

**`app/watchdog_test.go`**
- `TestTheFinalReportHoldsARecordAppendedWhileTheWatchdogCloses`: set up as in `TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5` (`watchdog_test.go:219-230`), with `sim.hang["rloop-p1-implement"] = true` and the same MCP halt call to end the run. The dog host is `&lateDog{dogHost: &dogHost{}, store: w.Store, runID: func() string { return w.Loop.RunID }}`. After `Execute` returns, `report.md` contains `warn from watchdog, phase 1 implement: late word`. On the base code, the report is last written before `Dog.Stop` runs `ClosePane`. Covers #25 "reflects every appended record once the run ends" across the app shutdown order (codex-r1-3).

- `TestASignalCallIsHandledWhileAWarnHookRuns`: `hook := filepath.Join(t.TempDir(), "hook.sh")` holds `touch <entered>; while [ ! -e <gate> ]; do sleep 0.05; done`, where `entered` and `gate` are paths in the same temp directory. The fixture is `newResumeFixture(t, noReviewConfig+"notify:\n  onWarn: sh "+hook+"\n")` with `sim.hang["rloop-p1-implement"] = true`, `--phases 1` and a `dogHost`, set up as in `TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5` (`watchdog_test.go:219-240`). `Execute` runs in a goroutine. After `step started phase-1/implement`, a go-sdk `signal` call `{kind: warn, step: phase-1/implement, reason: "slow", evidence: "docs/topic/todo.md:1"}` returns `accepted: true`, and `entered` exists within 2 s: the hook is now blocked. A second `signal` call `{kind: warn, reason: "slower"}` must return `accepted: true` within 1 s while `gate` is still absent. The store then holds a `warning` event with reason `slower` within 2 s, still with `gate` absent. A `signal` halt call ends the run. Create `gate`, and `Execute` returns 5 within 10 s. Covers #32 "a watchdog `signal` MCP call returns promptly while a hook is running", end to end through the MCP dispatch, the call record and attempt resolution.

**`app/unblock_test.go`**
- `TestARunFinishedBeforeTheLoopStillGetsItsReport`: `k := startWalk(t, nil)` and `k.w.Execute(core.RunOptions{Phases: []string{"1", "3"}})` returns 0, as in `TestARunWhoseEveryPhaseIsBlockedFinishesWithNothingToRun` (`unblock_test.go:220`). `report.md` in `k.w.Store.Dir(k.w.Loop.RunID)` exists and contains `- Pick the database still open: phase 1, 3 skipped\n`. The base code writes no report for this run. Covers #25 "`report.md` reflects every appended record once the run ends" for a run that never reaches `Run`.

**`notify/notify_test.go`**
- `TestASlowWarnHookDoesNotDelayTheHalt`: the loop from `runFakeLoop` (`notify_test.go:191`), with `Notifier: &Shell{Log, Emit: face.Emit, Timeout: 2 * time.Second}` and `Hooks{OnWarn: "sleep 30", OnHalt: "exit 1"}`, run in a goroutine. `face.kind("halt")` is non-empty within 1 s, and Run returns within 5 s. `notify-failed` holds exactly two events: status `blocked` with reason `timed out after 2s`, then status `halted` with a reason containing `exit status 1`. Covers #32 with a real shell: no delay to the halt, a timed-out or failed hook still emits `notify-failed`, and end hooks are bounded by the timeout before return.

**Updated tests:** `TestASignalForALandedPhaseIsRejected` and `TestAPanicAnsweringALandStageQuestionIsRecordedWithoutACrash` pin the landed rejection and the run-dir report under the new mechanisms.

After the gate passes, run `go test ./internal/...` and fix any existing test that assumed a synchronous report, a synchronous hook or a `Watch`/askmcp landed or attempt lookup through `Load`, keeping what each asserts.

## Left out

- Serving `Store.Load` from the view: app code appends through the bare store before `Run` (`resume.go:139`, `unblock.go:147`), so a cached `Load` could go stale for checks and land. No criterion needs faster `Load`.
- A new store port method (incremental or tail load): the guard's fold gives O(N) reads without one.
- Keeping an in-memory landed set on `RunLoop` and `Watch`: the view already holds landings from the moment they are appended.
- Panic recovery around the disk `Store.Load` in `writeReport`: the base code has none, and no criterion asks for it. `quietly` stays around the render and write, which run on the writer goroutine.
- A mutex serialising report writes: each write has its own random temp name, and the rename replaces `report.md` atomically.
- A config key for the refresh interval: `reportEvery` is a package variable only so tests can shorten it.
- A hook queue type or worker goroutine: chained goroutines keep order with one field.
- Changes to `internal/notify/notify.go`: `Shell.Fire` keeps its timeout, env and `notify-failed` behaviour.

## Assumptions

- `reportEvery` is 1 s.
- A new `report.md` gets `0o644` minus the process umask, as `os.WriteFile` gives it today, and an existing one keeps its mode.
- A run bound by preflight whose `Execute` fails before `run` (ask server or watchdog start) still gets a `report.md` from the records it has. The live report lags the last append by at most about 1 s plus one render.
- Between `Execute` starting the watchdog and `Run` calling `Follow`, askmcp's `resolve` still loads from disk, and `Watch` does not reject landed phases (`Landed` returns false while not following). No step is live in that window, so `Watch` rejects any signal as `is not running`.
- A record that fails to fold into the view (a malformed record the store still accepted) is left out of the live report and reappears in the disk-loaded final write.
- Routing `Watch`, `Remedies`, `Watchdog` and askmcp through `RecordGuard` also applies its `FatalRecord` bookkeeping to their appends. A failed append of a step, run, landing or resume event from them now halts the run, as it already does for the loop's own appends.
- Across goroutines, face event order and store record order no longer have to match. Events from one goroutine keep their order.
- Hooks run one at a time in fire order. A slow hook delays later hooks, never the loop, and each hook is bounded by `notify.Shell.Timeout`.
- A panic inside `Notifier.Fire` is reported as `notify-failed` with the panic text as reason.

## Gate

`go test ./internal/core/ ./internal/askmcp/ ./internal/app/ ./internal/notify/ -run '^(TestAFollowingGuardFoldsEveryAppendIntoItsView|TestAGuardThatIsNotFollowingKnowsNothing|TestAcceptingASignalLoadsNoRunState|TestSpacedEmitsReadTheStoreLinearly|TestARecordAppendedOutsideTheLoopReachesTheLiveReport|TestTheReportCatchesUpWithinTheRefreshInterval|TestABlockedFaceDoesNotHoldBackOtherEmitsOrRecords|TestASlowReportWriteDoesNotHoldBackEmits|TestTheLastReportWaitsForTheEndHooks|TestTheLastReportWaitsForAQuestionWorker|TestAReportWriteKeepsTheUmaskAndAnExistingMode|TestWriteReportBeforeRunWritesNothing|TestHandlingWatchdogSignalsLoadsNoRunState|TestASlowWarnHookDoesNotHoldTheLoop|TestAStepCompletesWhileASlowWarnHookRuns|TestAnAbortIsHandledWhileASlowWarnHookRuns|TestRunWaitsForASlowDoneHook|TestAWatchdogSignalIsHandledWhileAHookRuns|TestAPanickingHookIsReportedAndTheRunEnds|TestTheSignalToolResolvesTheAttemptFromTheLiveViewWithoutLoading|TestTheFinalReportHoldsARecordAppendedWhileTheWatchdogCloses|TestASignalCallIsHandledWhileAWarnHookRuns|TestARunFinishedBeforeTheLoopStillGetsItsReport|TestASlowWarnHookDoesNotDelayTheHalt|TestASignalForALandedPhaseIsRejected|TestAPanicAnsweringALandStageQuestionIsRecordedWithoutACrash)$'`
