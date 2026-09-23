# TUI — notes on the live `/test-app` run of 23 Sep 2026

Read against `r-loop` @ `main` `2db8b3b`. All three bugs are real and are in
`issues-tui-live-run-2026-09-23.md`. This file records where the report's description of the
cause is not what the code does, and the design choices the fixes have to make.

## Questions — need an answer before they can become work

**[#2] Should the steps line have a `land` stage?**
Production never draws one. `startTUI` fills the steps line from the pipeline kinds only
(`internal/app/wire.go:462-471`, default `[plan, implement]`), and landing emits `landed`, not a
`land` step event. But the golden frames, which `DESIGN.md` calls authoritative, show
`steps plan ✓ › implement › land` (`internal/face/tui/testdata/frame-120x40.golden:4`). They do
because the test fixture injects a fake `land` kind (`model_test.go:71`, `:289-303`). The fix
has to pick one: add a real `land` stage ticked by `landed`, or drop the phantom `land` from
the fixtures and frames. The item is written so either choice satisfies it. Nothing in
`DESIGN.md`, `tech-design.md` or `spec.html` says where `milestone` sits on the line either.
Appending the kinds that ran after the pipeline is the natural reading.

**[#1] After the run ends: clear the panel, or keep the last step in the past tense?**
Either choice satisfies the item. Clearing `Live` on `finished` would show `no step running`.
Keeping the last step would show `done`-style fields with no countdown. `DESIGN.md:91-108`
describes only a live step.

## Already built, or built differently than the report assumes

**[#1] The clocks do stop, but the wording says they don't.**
The report read `backstop 9m19s left` as a countdown still running. It isn't. A terminal `step`
event freezes the step at `s.Ended` (`internal/face/tui/model.go:258-265`), and `finished`
freezes the run clock (`model.go:192-193`, `:301-313`). The 9m19s is exactly the 10m backstop
minus the milestone's 40s, computed at its end. The real defect is presentation. `end()` never
clears `m.Live`, so the panel keeps the finished step with a `backstop … left` line
(`view.go:113`, `:131-135`). One existing test asserts exactly that:
`TestAFinishedRunStopsTheClocks` (`model_test.go:424-437`) expects `3h1m0s left` after
`finished`, so the fix must change it. The milestone's ok event does reach the Face, through
`stepRecorder` (`internal/core/milestone.go:73-81` → `land.go:237-262`), and is handled like
plan and implement.

**[#3] The rail row was live, and the check's result does reach the TUI.**
The report says the phase showed as `·` with nothing live. The row was in fact the one bold
row: `phase-start` sets `m.Current` (`model.go:146-147`), and the rail styles that row as live
(`view.go:95-96`). Only its glyph stayed `·`, because the phase is still unticked. The missing
piece is a start event. `checkPhase` (`internal/core/loop.go:310-353`) emits nothing before
`BeforePhase` blocks on the watchdog (`phasecheck.go:33-36`). At the end it emits `phase-check`
to the Face (`loop.go:344-352`), and `Model.Apply` has no case for it, so it is dropped.
The same gap has a second symptom: `phase-start` never clears `m.Live`. From phase 2 on, the
panel would show the previous phase's finished step during the check.

## Moves architecture or the estimate

Nothing here does. All three fixes live in `internal/face/tui`, plus one new event in
`internal/core/loop.go` for #3 and matching lines in the plain face. No port, contract or
state machine changes. #3 also means adding the new event to `tech-design.md` (around
`:508`/`:538`), with one sentence in `DESIGN.md`.

**Overlap with `issues-tui-ux-2026-09-22.md`.** #3 touches that backlog's `[#8]`, the step
suffix on the live rail row. As `[#8]` is worded, "unticked phases keep their glyphs" would keep
the `·` on a phase being checked. Whichever lands second should give the live row the
in-progress glyph and a `check` suffix during the check. `[#10]` there (rebuilding the live step
on resume) touches the same `m.Live` handling as #1 and #3 here.
