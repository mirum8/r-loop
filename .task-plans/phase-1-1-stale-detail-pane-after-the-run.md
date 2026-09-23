status: planned

## Summary

The TUI detail panel keeps showing the run's last step as live, with a `backstop <d> left` line, after that step has ended and after the run has ended. The clocks already stop: `Step.Elapsed` uses `Ended` (`internal/face/tui/model.go:58-63`) and `Model.clock()` uses `ended` (`model.go:308-313`). What is wrong is how the panel presents it. There are two changes, both in `internal/face/tui`:

1. `panel` draws the `backstop` line only while the step has not ended.
2. `Model.end` clears `m.Live`, so after `finished`, `halt` or `aborted` the panel shows the existing dim `no step running` line.

Choices:
- **What an ended step shows in place of the backstop line:** nothing, the line is dropped. The alternative was a new `ended HH:MM:SS` field. It lost because `DESIGN.md` does not define that field, and the `state` line (`ok`/`failed`) plus the frozen `elapsed` already say the step ended.
- **The panel after the run ends:** clear `m.Live` (option A). The alternative was keeping the last step in the past tense (option B). The maintainer chose A (watchdog answer to q1). A needs no new state wording and matches what `replay` already does (`model.go:211`).
- **Where the ended check lives:** in `panel`, at the backstop line (`view.go:131-135`). The alternative was changing `Step.Remaining` to report the step as ended. It lost because `Remaining` returns a duration and a paused flag, and adding a third outcome to its signature would serve one caller.

## Changes

1. **Modify `internal/face/tui/view.go`**, in `func (m Model) panel(w int) []string` (`view.go:109`). Wrap the backstop block at `view.go:131-135` in `if s.Ended.IsZero() { ... }`:
   ```go
   if s.Ended.IsZero() {
       backstop := "paused"
       if left, paused := s.Remaining(m.clock()); !paused {
           backstop = left.Truncate(time.Second).String() + " left"
       }
       add(th.Text, field("backstop", backstop))
   }
   ```
   - A step's `Ended` is set exactly when its terminal `ok`/`failed` `step` event is applied (`model.go:258-259`). That holds for every kind, including kinds that arrive through `stepRecorder` with no `queued` event (milestone, land: `internal/core/land.go:237-262`, `internal/core/milestone.go:70`).
   - Nothing else in the panel changes. A running or paused step has a zero `Ended`, so it renders byte-for-byte as today, and both golden frames (a `waiting-input` step, `backstop paused`) stay identical.
   - Serves obligations 1 and 4.

2. **Modify `internal/face/tui/model.go`**, in `func (m *Model) end(status string, at time.Time)` (`model.go:301-306`). Add `m.Live = nil`.
   - `end` is the single path for `finished` (`model.go:192-193`), `halt` (`:194-199`), `aborted` (`:200-202`) and the face's own `closedMsg` (`:334-337`). The `closedMsg` path also clears the panel, which is right: the run is over there too.
   - `panel` already renders `no step running` when `m.Live == nil` (`view.go:136-137`).
   - `m.done`, `Status`, `Blocked`, `Resume`, `Feed` and the header clock (`ended`) are untouched, so the status line and EVENTS stay as they are.
   - Serves obligation 3.

3. **Modify `internal/face/tui/model_test.go`.** Update `TestAFinishedRunStopsTheClocks` (`model_test.go:424-437`) and add the new tests listed under `## Tests`. They use the existing helpers `newModel`, `step`, `recorded` and `at` (`model_test.go:21-76`). Serves obligations 1-4.

No other file changes. The goldens in `internal/face/tui/testdata/` are not regenerated.

## Tests

All tests live in `internal/face/tui/model_test.go`, package `tui`.

- **`TestAnEndedStepShowsNoBackstopCountdown`** (new, table-driven). Cases:
  - `{"plan","ok"}`
  - `{"implement","failed"}`
  - `{"milestone","ok"}`

  For each case, build `newModel` from these events:
  1. `core.Event{At: at(0), Kind: "phase-start", Phase: "3", Fields: {"phase":"3"}}`
  2. `step(0, 3, kind, "running", "claude", "opus", "high", "ws-9")`, with no `queued` event before it, the way `stepRecorder` emits
  3. `step(4, 3, kind, state, "claude", "opus", "high", "ws-9")`

  Then apply `Update(tickMsg(at(30)))`. Assert that the view:
  - contains `"PHASE 3 · "+kind` and `"state      "+state`
  - contains neither `"backstop"` nor `" left"`

  Pins obligation 1 for every kind and both terminal states. It fails on the base code, which prints `backstop 3h56m0s left`.

- **`TestAnEndedStepsElapsedStaysEndedMinusStarted`** (new).
  - Events: `step(31, 2, "implement", "running", ...)`, then `step(40, 2, "implement", "ok", ...)`.
  - Apply `Update(tickMsg(at(60)))` and assert the view contains `"elapsed 9m0s"`.
  - Apply `Update(tickMsg(at(90)))` and assert the view still contains `"elapsed 9m0s"` and that `m.Live.Elapsed(at(90)) == 9*time.Minute`.

  Pins obligation 2.

- **`TestAFinishedRunStopsTheClocks`** (updated). Keep the setup: `recorded()` without its last event, `finished` at `at(90)`, then `Update(tickMsg(at(120)))`.
  - Assert the view contains `"started 14:00 · 1h30m0s"` and `"no step running"`.
  - Assert the view contains none of `"PHASE 2 · implement"`, `"elapsed"` or `" left"`.
  - Assert `next.(Model).Live == nil`.

  The old `"elapsed 59m0s"` and `"3h1m0s left"` expectations are removed. Pins obligation 3 for `finished` and satisfies the item's "update the test" clause.

- **`TestAHaltedOrAbortedRunShowsNoLiveStep`** (new). Two cases on `recorded()` without its last event, so the live step is `implement`, `running`:
  - `core.Event{At: at(90), Kind: "halt", Fields: {"blocked":"2","resume":"r-loop resume"}}`
  - `core.Event{At: at(90), Kind: "aborted", Phase: "2", Step: "implement"}`

  After applying each case, apply `Update(tickMsg(at(120)))`. Assert that the view:
  - contains `"no step running"` and `"halted"`
  - contains neither `"PHASE 2 · implement"` nor `" left"`

  Pins obligation 3 for `halt` and `aborted`, including a halt that arrives while a step is still `running`.

- **Existing tests, unchanged, in the gate as regression checks for obligation 4:**
  - `TestFrameAt120x40` and `TestFrameAt70x30StacksTheRailAboveThePanel`: the goldens stay byte-identical.
  - `TestBackstopCountsDownAndPausesWhileAQuestionIsOpen`: a running step counts down and a waiting step pauses.
  - `TestElapsedTicksEverySecond`: a running step's elapsed advances on ticks.

## Left out

- **A new `ended` field in the panel.** No obligation asks for it, and `DESIGN.md` does not define it.
- **Removing the `Ended` branch in `Step.Remaining`** (`model.go:69-71`). It is harmless, and no obligation touches it.
- **Resetting `Ended` when a later non-`queued` event arrives for the same step key in `step()`** (`model.go:232`). Every loop re-run emits `queued` first (`internal/core/loop.go:579`), which already starts a fresh `Step`. No named caller sends a same-key run after a terminal event, so no obligation needs this.
- **Clearing `m.Live` on `phase-start`.** That is issue [#3]'s scope, not this phase.

## Assumptions

- **The watchdog's `Files: none` warning.** It is resolved by naming the files this phase changes: `internal/face/tui/view.go`, `internal/face/tui/model.go` and `internal/face/tui/model_test.go`. The goldens `internal/face/tui/testdata/frame-{120x40,70x30}.golden` are not edited, and the golden tests in the gate prove they stay byte-identical.
- **The backstop line after an ended step is dropped, not replaced.** The panel is one line shorter for an ended step. That is within layout limits, since the panel only gets shorter.
- **The face's own `ended` status on close (`closedMsg`) also clears `m.Live`, because it goes through the same `end()`.** The item does not name it, and the run is over in that case too.

## Gate

`go test ./internal/face/tui/ -run '^(TestAnEndedStepShowsNoBackstopCountdown|TestAnEndedStepsElapsedStaysEndedMinusStarted|TestAFinishedRunStopsTheClocks|TestAHaltedOrAbortedRunShowsNoLiveStep|TestFrameAt120x40|TestFrameAt70x30StacksTheRailAboveThePanel|TestBackstopCountsDownAndPausesWhileAQuestionIsOpen|TestElapsedTicksEverySecond)$'`
