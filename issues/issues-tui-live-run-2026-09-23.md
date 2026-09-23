# TUI — backlog from the live `/test-app` run of 23 Sep 2026

The live run of `todo-tiny.md` in the sandbox found 3 TUI bugs, and all 3 are real work, listed
below. Two of them rest on a partly wrong premise, and one reopens a design choice. Both are in
`issues-tui-live-run-2026-09-23-notes.md`. `[#n]` is the bug's number from the test report. Every
item has to keep to `DESIGN.md`: amber means only "waiting for you", only the live phase is bold,
and every row stays one line down to 80 columns.

Verified against `r-loop` @ `main` `2db8b3b`.

- [x] [#1] Stale detail pane after the run  <!-- fixed: r-loop/phase-1 -->
      - Once a step's terminal `step` event (ok or failed) has been applied, whatever its kind, the panel no longer shows `backstop <d> left` for that step
      - An ended step's elapsed value is `Ended - Started` and does not change on later ticks
      - After `finished`, `halt` or `aborted`, the panel shows neither the last step as live nor any `left` countdown; `TestAFinishedRunStopsTheClocks` is updated to match
      - A running or paused step renders as today, and the 120x40 and 70x30 golden frames stay byte-identical

- [x] [#2] Milestone missing from the steps row  <!-- fixed: r-loop/phase-2 -->
      - Whatever kind is named in the `PHASE N · <kind>` title always appears on that phase's steps line, including `milestone`, `gatefix` and the backlog `gate`
      - When phase N closes a milestone, the line reads `plan ✓ › implement ✓ › milestone` while the session is live (primary, not bold), then `milestone ✓` or `milestone ×` when it ends; a phase that closes no milestone shows no `milestone` entry
      - The steps line stays one row, cut with `…` at 80 columns and below, with no amber and no bold
      - The golden frames and `TestTheStepsLineTracksDoneFailedLiveAndPendingKinds` use a step list the real wiring can produce

- [x] [#3] Nothing shown during the phase check  <!-- fixed: r-loop/phase-3 -->
      - The core emits a Face-visible event when the watchdog's phase check starts, before the worktree is created and before the blocking `Notify`
      - From then until the check ends, the TUI panel shows a dim line naming the phase and saying the watchdog is checking the plan, with its elapsed time. It never shows "no step running", and never the previous phase's finished step
      - When the check ends, EVENTS gains one dim result line for `phase-check`, `phase-check-timeout` or `phase-check-skipped`; the check's own warnings stay amber `warning` lines
      - The plain face prints one line when the check starts and one with its result, and model, view and loop-event tests cover both
