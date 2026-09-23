status: planned

## Summary

While the watchdog checks a phase, nothing reaches either face. `RunLoop.checkPhase` calls `BeforePhase` without emitting anything first (`internal/core/loop.go:310-312`). `PhaseCheck.Run` then creates the worktree and blocks on `Notify` (`internal/core/phasecheck.go:33-38`). The TUI keeps showing the previous phase's finished step until the check returns, and the plain face prints nothing.

The phase makes three changes:

1. **Core.** `checkPhase` emits a new `phase-check-start` event through `l.emit` (store, then face) before it calls `BeforePhase`. That is before the worktree is created and before the blocking `Notify`.
2. **TUI.** On `phase-check-start`, the model enters a checking state and drops the previous step. The panel then shows one dim line, `phase <N> · watchdog checking the plan · <elapsed>`, in place of the step block. The three result kinds (`phase-check`, `phase-check-timeout`, `phase-check-skipped`) end the checking state and each add one dim EVENTS line. The check's warnings are ordinary `warning` events, so they stay amber through the existing `case "warning"` (`internal/face/tui/model.go:154-155`).
3. **Plain face.** It gets one explicit case for all four kinds and prints `<time>  phase <N>  phase check  <detail>`.

Choices:
- **Where the start event is emitted.** In `RunLoop.checkPhase`, through `l.emit`. The alternative was inside `Watch.BeforePhase`, through `w.Face`. The loop is the component that already emits the check's result event (`loop.go:352`), and `l.emit` appends to the store before it shows the event (`loop.go:1126-1135`). That keeps the append-before-action invariant with no new code path.
- **When the start event is emitted.** Only when `l.Watcher != nil`. The alternative was always. With no watcher, `nopWatcher.BeforePhase` returns an empty outcome (`loop.go:43-45`), no result event ever follows (`loop.go:337-339`), and a start would leave a check that never ends. Production always sets the watcher (`internal/app/wire.go:394`), including with `--no-watchdog`, where `Watch.BeforePhase` returns `phase-check-skipped` (`internal/core/watch.go:159-162`).
- **A watcher that returns no outcome after a start.** The `Watcher` port allows `BeforePhase` to return the zero `CheckOutcome` (`loop.go:28`), and `fakeWatcher.BeforePhase` does (`loop_events_test.go:24-27`). When a start was emitted, the loop turns an empty outcome into `phase-check-skipped` with reason `no phase check`. The alternative was leaving it with no result. That lost because the start would then never be closed, which breaks the rule that every start gets one result line.
- **Event kind name.** A new `phase-check-start`, following `phase-start`. The alternative was a `state` field on the existing kinds. That lost because `report.go:107-112` keys on the kind alone and would misread a start as a result.
- **How the TUI holds the check.** Two unexported fields on `Model`, `checking string` and `checkFrom time.Time`. The alternative was reusing `Live *Step` with a pseudo-kind `check`. That lost because the panel would then draw `provider`, `session` and `backstop` lines a check does not have, and the steps line would gain a `check` kind (`view.go:159-162`).
- **The result line when the watchdog warned.** `phase check warned`. The alternative was repeating the `result` field, which is every warning joined with `; ` (`loop.go:347-350`). That lost because it duplicates the amber lines already in the feed, and the dim result line must not carry the warnings.
- **Result wording shared between the faces.** A small unexported `checkDetail` function in each face package. The alternative was one exported helper in core. That lost because core would gain presentation text for two callers, and the faces already format their own lines.

## Changes

1. **Modify `internal/core/phasecheck.go`.** Add `phaseCheckStart = "phase-check-start"` to the `const` block at `phasecheck.go:11-16`, beside `phaseCheckRan`. Serves obligation O1.

2. **Modify `internal/core/loop.go`**, in `func (l *RunLoop) checkPhase(ctx context.Context, ph Phase, base string)` (`loop.go:310`). Between `n := ph.ID` (`:311`) and `out := l.watcher().BeforePhase(ctx, ph, base)` (`:312`), insert:
   ```go
   if l.Watcher != nil {
       l.emit(Event{Kind: phaseCheckStart, Phase: n, Fields: map[string]string{"phase": n}})
   }
   ```
   - `l.emit` (`loop.go:1126`) appends the event to the store, then emits it to the Face, then rewrites the report.
   - `runPhase` calls `checkPhase` right after `phase-start` (`loop.go:232-234`), so the start follows `phase-start` and precedes `Repo.AddWorktree` and `Notify` in `PhaseCheck.Run` (`phasecheck.go:33-38`).
   - Right after that `BeforePhase` call, add:
     ```go
     if l.Watcher != nil && out.Kind == "" {
         out = CheckOutcome{Kind: phaseCheckSkipped, Reason: "no phase check"}
     }
     ```
     This makes sure a start always gets its result. The existing `if out.Kind == "" { return }` (`loop.go:337-339`) now returns only when there is no watcher, so no start was emitted either. The result event at `loop.go:340-352` stays as it is.
   - I checked this in a scratch copy with `go test ./internal/core/ ./internal/app/`. One run hit `TestAStepQuestionReachesTheWatchdogAndItsAnswerReleasesTheStep` once. It is a pre-existing flake: the same test passed 6 of 6 isolated runs, and `go test ./internal/core/ -count=3` passed both with the change and on the base.
   - Serves O1, O2 and O7.

3. **Modify `internal/face/tui/model.go`.**
   - **`Model` struct** (`model.go:100-121`). After `done map[stepID]string` (`:105`), add:
     ```go
     checking   string
     checkFrom  time.Time
     ```
   - **`Apply`** (`model.go:144-205`). Change `case "step":` (`:152-153`) to clear the check first:
     ```go
     case "step":
         m.checking = ""
         m.step(ev)
     ```
     Add two cases before `case "warning":`:
     ```go
     case "phase-check-start":
         m.checking, m.checkFrom, m.Live = ev.Phase, ev.At, nil
     case "phase-check", "phase-check-timeout", "phase-check-skipped":
         m.checking = ""
         m.log(ev, toneDim, "phase check "+checkDetail(ev))
     ```
     `m.log` (`model.go:269-278`) prefixes `HH:MM  phase <N>: `, because the result events carry `Phase` and no `Step` (`loop.go:352`).
   - **New function** after `landedText` (`model.go:290-299`):
     ```go
     func checkDetail(ev core.Event) string {
         f := ev.Fields
         switch ev.Kind {
         case "phase-check":
             if f["result"] == "no disagreement" {
                 return "found no disagreement"
             }
             return "warned"
         case "phase-check-timeout":
             return withReason("timed out", f["reason"])
         }
         return withReason("skipped", f["reason"])
     }

     func withReason(text, reason string) string {
         if reason == "" {
             return text
         }
         return text + ": " + reason
     }
     ```
     `withReason` has two call sites. It is kept because a timeout's reason (`phasecheck.go:37`) and a skip's reason (`phasecheck.go:30,34`) are both optional: core sets `reason` only when `out.Reason != ""` (`loop.go:343-345`), and `Watch` without a `PhaseCheck` returns a skip with no reason (`watch.go:161`).
   - **`end`** (`model.go:301-306`). Insert `m.checking = ""` as the first statement, before `m.Status = status`. This covers `finished`, `halt`, `aborted` and `closedMsg`. It was checked with `git merge-file` against `r-loop/phase-1`'s `end()` (which adds `m.Live = nil` after `m.Current = ""`), and the two merge without a conflict.
   - **`replay`** (`model.go:207-215`). After `m.ended = time.Time{}` (`:212`), add `m.checking = ""`. A run that stopped mid-check has `phase-check-start` in its store with no result, and resume replays that history (`model.go:375`).
   - Serves O2, O3, O4 and O5.

4. **Modify `internal/face/tui/view.go`**, in `func (m Model) panel(w int) []string` (`view.go:110`). Replace `if s := m.Live; s != nil {` (`:114`) with:
   ```go
   if m.checking != "" {
       add(th.Label, fmt.Sprintf("phase %s · watchdog checking the plan · %s", m.checking, m.clock().Sub(m.checkFrom).Truncate(time.Second)))
   } else if s := m.Live; s != nil {
   ```
   - The rest of the block, the `else` with `no step running` (`:136-138`) and EVENTS all stay unchanged.
   - `th.Label` is the dim `on-surface-dim` style (`theme.go:34`), the same style as `no step running`.
   - `add` truncates to the panel width with `…` (`view.go:113`), so the line stays one row at every width.
   - This merges with `r-loop/phase-1`'s panel hunk (`view.go:128-137`) without a conflict, checked with `git merge-file`.
   - Serves O2 and O3.

5. **Modify `internal/face/plain/plain.go`**, in `func (f *Face) Emit(ev core.Event)` (`plain.go:19`). Add a case before `case "warning", "error":` (`:39`):
   ```go
   case "phase-check-start", "phase-check", "phase-check-timeout", "phase-check-skipped":
       fmt.Fprintf(f.Out, "%s  phase %s  phase check  %s\n", ev.At.Format("15:04:05"), ev.Phase, checkDetail(ev))
   ```
   Add `checkDetail` and `withReason` after `where` (`plain.go:67-72`). They are the same as the TUI's, with one more case at the top of `checkDetail`'s switch: `case "phase-check-start": return "watchdog checking the plan"`. The warnings keep going through `case "warning", "error"` as `!` lines. Serves O6.

6. **Modify the tests** `internal/core/loop_events_test.go`, `internal/face/tui/model_test.go`, `internal/face/tui/view_test.go` and `internal/face/plain/plain_test.go`, as listed under `## Tests`. No golden frame changes: `recorded()` (`model_test.go:43-66`) has no check events.

Obligations:
- **O1.** Core emits a Face-visible `phase-check-start` before `Repo.AddWorktree` and before the check `Notify`. It is appended to the store before the action (spec invariant: a transition is appended before the action it describes).
- **O2.** From the start until the result, the panel shows a dim line naming the phase, saying the watchdog is checking the plan, and giving the elapsed time. It never shows `no step running` or the previous phase's step.
- **O3.** The checking state ends on the check's result, on a step event, on `finished`, `halt` or `aborted`, and on resume replay.
- **O4.** Each of `phase-check`, `phase-check-timeout` and `phase-check-skipped` adds exactly one dim EVENTS line. Reason present and reason absent are both handled. `phase-check-start` adds no EVENTS line.
- **O5.** The check's warnings (`warning` events, `Step: "check"`, from `loop.go:324` via `l.warn`) stay amber.
- **O6.** The plain face prints one line at the start and one line for each result kind.
- **O7.** No start is emitted when there is no watcher. When there is a watcher, a start is always followed by exactly one result, including when `BeforePhase` returns an empty outcome.

## Tests

Write these first. All go in the existing test files and use the existing rigs and helpers.

**`internal/core/loop_events_test.go`**
- `TestThePhaseCheckStartReachesTheFaceBeforeTheWorktreeAndTheCheckPrompt`
  - Setup: `r := newCheckRig(t)` (`phasecheck_test.go:50`), then `r.run(RunOptions{Phases: []string{"1"}})`.
  - In `r.shared.Calls()`, use `indexOf` (`phasecheck_test.go:87`) to locate `Face.Emit phase-start`, `Face.Emit phase-check-start`, `Repo.AddWorktree .r-loop/wt/phase-1` and `SessionHost.Prompt rloop-wd-run-1 "check phase 1`. Assert all four are found and appear in exactly that order.
  - Assert `r.events("phase-check-start")` has one event, with `Phase == "1"` and `Fields["phase"] == "1"`.
  - Assert `r.store.Records["run-1"]` holds exactly one `RecordEvent` whose `Event.Kind == "phase-check-start"`.
  - Assert the event was stored before it was shown: with `start` as the index of `Face.Emit phase-check-start`, `calls[start-1]` starts with `Store.Append run-1` (`fakeStore.Append` records it, `fakes_test.go:280`).
  - Covers O1.
- `TestEachPhaseCheckStartIsFollowedByOneResult`
  - Three subtests. For each, collect the kinds in `r.face.Events` that start with `phase-check`, and assert they equal `[]string{"phase-check-start", <want>}`.
  - `ran`: `newCheckRig(t)`. Want `phase-check`.
  - `timed out`: `newCheckRig(t)` with `r.dogHost.err = errors.New("herdr agent prompt: herdr: timeout: no answer within 10m0s")` and `r.dogHost.States = map[string]AgentState{"rloop-wd-run-1": AgentGone}`, as in `phasecheck_test.go:228-229`. Want `phase-check-timeout`.
  - `skipped`: `newLoopRig(t)` with `r.loop.Watcher = &Watch{Store: r.store, Face: r.face}`, as in `phasecheck_test.go:248-249`. Want `phase-check-skipped`.
  - Covers O1 and O7.
- `TestAWatcherWithNoOutcomeStillEndsTheCheck`
  - `newEventsRig(t)`, whose `fakeWatcher.BeforePhase` returns `CheckOutcome{}` (`loop_events_test.go:24-27`). Run phase 2 with `r.run(RunOptions{Phases: []string{"2"}})`.
  - Assert the kinds in `r.face.Events` that start with `phase-check` equal `[]string{"phase-check-start", "phase-check-skipped"}`.
  - Assert the skip event has `Phase == "2"` and `Fields["reason"] == "no phase check"`.
  - Covers O7.
- `TestNoPhaseCheckStartWithoutAWatcher`
  - `newLoopRig(t)` with `r.loop.Watcher` left nil. Run phase 1.
  - Assert `r.events("phase-check-start")` is empty.
  - Covers O7.

**`internal/face/tui/model_test.go`**
- `TestAPhaseCheckReplacesThePreviousStepInThePanelUntilItEnds`
  - Setup: `m := newModel(recorded()[:10])`, which runs through phase 1 landing and `phase-start 2` at `at(22)`. Apply `{At: at(22), Kind: "phase-check-start", Phase: "2", Fields: {"phase": "2"}}`.
  - Assert `m.Live == nil`.
  - `Update(tickMsg(at(24)))`. Assert the view contains `phase 2 · watchdog checking the plan · 2m0s` and contains none of `no step running`, `PHASE 1` or `backstop`.
  - Apply `{At: at(25), Kind: "phase-check", Phase: "2", Fields: {"phase": "2", "result": "no disagreement"}}`. Assert the view no longer contains `watchdog checking the plan`.
  - Covers O2 and O3 (result).
- `TestEachPhaseCheckResultIsOneDimFeedLine`
  - Table. Each case builds `newModel([]core.Event{ev})` with `ev.At = at(5)`, `ev.Phase = "2"`. Assert `len(m.Feed) == 1`, that `Feed[0].Text` has the given suffix, and the given tone.
  - `phase-check` with `result: "no disagreement"` → `phase 2: phase check found no disagreement`, `toneDim`.
  - `phase-check` with `result: "Files: none leaves out core; Risk: none is too low"` → `phase 2: phase check warned`, `toneDim`.
  - `phase-check-timeout` with `reason: "herdr: timeout: no answer within 10m0s"` → `phase 2: phase check timed out: herdr: timeout: no answer within 10m0s`, `toneDim`.
  - `phase-check-skipped` with `reason: "watchdog unreachable"` → `phase 2: phase check skipped: watchdog unreachable`, `toneDim`.
  - `phase-check-skipped` with no fields → `phase 2: phase check skipped`, `toneDim`.
  - `warning` with `Step: "check"`, `reason: "Risk: none is too low"` → `phase 2 check: Risk: none is too low`, `toneWarn`.
  - Last, assert `newModel([]core.Event{{At: at(5), Kind: "phase-check-start", Phase: "2"}})` has an empty `Feed`.
  - Covers O4 and O5.
- `TestTheCheckingLineClearsOnAStepOnTheRunsEndAndOnResume`
  - Base: `newModel(recorded()[:10])`, then apply `phase-check-start` for phase 2 at `at(22)`.
  - Subtest `step`: apply `step(23, 2, "plan", "running", "claude", "opus", "high", "ws-5")`. Assert the view contains `PHASE 2 · plan` and lacks `watchdog checking the plan`.
  - Subtests `finished`, `halt` and `aborted`: apply `{At: at(23), Kind: <kind>}`. Assert the view lacks `watchdog checking the plan`.
  - Subtest `resume`: `m := replay(newModel(nil), append(recorded()[:10], <the phase-check-start event>))`. Assert the view lacks `watchdog checking the plan`.
  - Covers O3.

**`internal/face/tui/view_test.go`**
- `TestTheCheckingLineIsDimAndItsWarningsAmber`
  - Setup: `m := coloured(false)`, then `m.Now = at(4)`. Apply in order:
    - `phase-start` for phase 2 at `at(2)`;
    - `phase-check-start` for phase 2 at `at(2)`;
    - `{At: at(3), Kind: "warning", Phase: "2", Step: "check", Fields: {"reason": "Risk: none is too low"}}`.
  - First view. Assert `\x1b\[38;2;138;146;158mphase 2 · watchdog checking the plan · 2m0s` matches, and that `\x1b\[38;2;224;16[34];88[0-9;]*m[^\x1b]*Risk: none is too low` matches (the amber regex from `view_test.go:289`).
  - Apply `{At: at(3), Kind: "phase-check", Phase: "2", Fields: {"phase": "2", "result": "Risk: none is too low"}}`.
  - Second view. Assert `\x1b\[38;2;138;146;158m {3}14:03  phase 2: phase check warned` matches, and that the amber regex followed by `phase check warned` does not.
  - Covers O2 (dim, elapsed), O4 (dim) and O5 (amber).

**`internal/face/plain/plain_test.go`**
- `TestThePhaseCheckIsOneLineWhenItStartsAndOneWithItsResult`
  - Table. Each case emits one event with `At: at`, `Phase: "4"` to a fresh `Face`, and compares the output exactly.
  - `phase-check-start` → `14:03:09  phase 4  phase check  watchdog checking the plan\n`
  - `phase-check` with `result: "no disagreement"` → `14:03:09  phase 4  phase check  found no disagreement\n`
  - `phase-check` with `result: "Risk: none is too low"` → `14:03:09  phase 4  phase check  warned\n`
  - `phase-check-timeout` with `reason: "herdr: timeout"` → `14:03:09  phase 4  phase check  timed out: herdr: timeout\n`
  - `phase-check-skipped` with `reason: "watchdog unreachable"` → `14:03:09  phase 4  phase check  skipped: watchdog unreachable\n`
  - `phase-check-skipped` with no fields → `14:03:09  phase 4  phase check  skipped\n`
  - Covers O6.

Existing tests stay green unchanged. This was checked by applying change 2 to a scratch copy: `go test ./...` passed every package with the start event alone, and `go test ./internal/core/ -count=3` and `go test ./internal/app/` passed with the empty-outcome skip added, including `TestCleanRunLandsEveryUntickedPhaseInOrder` (`loop_test.go:241`), which runs with a nil watcher. The TUI goldens and `TestPlainFaceAndTUIModelShowTheSameRun` see no check events.

## Left out

- **Clearing the checking state on `phase-start`.** A new `phase-start` cannot follow an unclosed check. `checkPhase` returns, having emitted its result, before `runPhase` moves on, and the next phase's `phase-start` comes after that. The one unclosed case, a store holding a start from a stopped run, is handled by `replay`.
- **A shared `checkDetail` in core.** It would add presentation text to core for two callers. The two six-line copies in the face packages are the smaller cost.
- **A `DESIGN.md` update.** The checking line uses the existing dim `Label` style and the existing panel slot, so it adds no new token, component or state colour.
- **A `Watcher` interface method for the start.** The loop already owns event emission around `BeforePhase`, so a new port method would only forward.
- **Report or status changes.** `report.go:107-112` and `report.go:297` ignore the new kind: it is not one of the three result kinds and does not end in `-skipped`. `internal/app/status.go:145-150` switches only on the kinds it knows. Neither needs a line of code.

## Assumptions

- **Watchdog warning, "Files: none".** Resolved. `## Changes` names every file: `internal/core/phasecheck.go`, `internal/core/loop.go`, `internal/face/tui/model.go`, `internal/face/tui/view.go`, `internal/face/plain/plain.go`, and their tests `internal/core/loop_events_test.go`, `internal/face/tui/model_test.go`, `internal/face/tui/view_test.go`, `internal/face/plain/plain_test.go`.
- **Watchdog warning, "Risk: none" and the corrected phase 1 / phase 2 overlap.** Resolved.
  - Phase 2 has landed on `main` (155b2a6), and this worktree carries its steps-line change.
  - Phase 1 (`r-loop/phase-1`, ed30260) is still off `main`. It edits `Model.step`, `Model.end` (adding `m.Live = nil`) and the backstop block in `panel`.
  - This plan does not touch `Model.step`. It puts `m.checking = ""` in `end` as the first statement, and it changes only the `if` line at `view.go:114`.
  - Both the planned `model.go` and `view.go` edits were run through `git merge-file` against phase 1's versions, and both merged with zero conflicts.
  - The combined behaviour is consistent. Phase 1 clears `Live` on end, and this phase clears `checking` on end and `Live` at check start. After either lands, an ended run shows `no step running`.
- **Watchdog warning, the checking state has to clear on phase-start, first step and finished/halt/aborted.** Resolved.
  - First step: the `step` case.
  - Finished, halt and aborted: `end()`.
  - The check's own result and resume replay are covered too.
  - `phase-start` is left out, for the reason under `## Left out`.
- **Result wording.** The faces say `found no disagreement` when `result` is exactly `no disagreement` (`loop.go:347`), and `warned` for any other `phase-check` result.
- **Elapsed time.** The elapsed time uses `m.clock()`, the same clock the step block uses (`view.go:130`), so it stops when the run ends.
- **The gap after the result.** Between the check's result and the phase's first `step` event, the panel shows `no step running`. That gap is only the in-process time from `checkPhase` returning to the first `emitStep` (`loop.go:234-259`). O2 covers the check window, and the previous phase's step is not shown during the gap because `Live` was cleared at the check's start.

## Gate

`go test ./internal/core/ ./internal/face/tui/ ./internal/face/plain/ -run '^(TestThePhaseCheckStartReachesTheFaceBeforeTheWorktreeAndTheCheckPrompt|TestEachPhaseCheckStartIsFollowedByOneResult|TestAWatcherWithNoOutcomeStillEndsTheCheck|TestNoPhaseCheckStartWithoutAWatcher|TestAPhaseCheckReplacesThePreviousStepInThePanelUntilItEnds|TestEachPhaseCheckResultIsOneDimFeedLine|TestTheCheckingLineClearsOnAStepOnTheRunsEndAndOnResume|TestTheCheckingLineIsDimAndItsWarningsAmber|TestThePhaseCheckIsOneLineWhenItStartsAndOneWithItsResult)$'`
