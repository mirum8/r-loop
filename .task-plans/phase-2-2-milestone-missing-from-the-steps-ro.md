status: planned

## Summary

The TUI steps line is built only from `Header.Steps` (`internal/face/tui/view.go:157-173`), which `internal/app/wire.go:463-466` fills with the pipeline kinds, so `milestone`, `gatefix` and the backlog `gate` never appear, even while they are the live step named in the `PHASE N · <kind>` title (`view.go:114`, `Step.Label` at `internal/face/tui/model.go:44-56` starts with `s.Kind`). Every one of those kinds reaches the Face as a `step` event whose `Step` is the kind name (`internal/core/land.go:258-267` via `stepRecorder`, used by `milestone.go:73-81`, `land.go:210-213` for `gatefix`, `gate.go:66-80` for `gate`), so `m.Live` already holds it. The fix is in `pipeline` only: the rendered kinds are `Header.Steps` plus the live step's kind when it is not one of them, and a live step that has ended is drawn from its end state. A phase that closes no milestone never gets a `milestone` step event (`internal/core/milestone.go:29-32`), so it never gets the entry; a `gatefix` that ran before the milestone (`land.go:50-68`: gate fix rounds, then `Boundary.After`) drops off the line once `milestone` is live, so the line reads exactly `plan ✓ › implement ✓ › milestone`. The test fixtures drop the phantom `land` kind and the two golden frames are regenerated.

Choices:

- **Where the extra kind comes from:** the live step's own kind, appended in `pipeline` when absent from `Header.Steps` (taken) vs. adding `milestone`/`gatefix`/`gate` to `Header.Steps` in `wire.go` vs. remembering every non-pipeline kind seen per phase. Static entries would show `milestone` on phases that close none (whether a phase closes one is decided at run time, `milestone.go:41-56`); a per-phase history adds a model field and would keep `gatefix ✓` on the line after the milestone starts, which breaks the required `plan ✓ › implement ✓ › milestone`.
- **Ended live step:** the live case applies only while the live step's state is neither `ok` nor `failed` (taken) vs. deleting the `done` entry whenever a non-terminal event arrives. The guard is one condition in `pipeline`; `done` already records `ok`/`failed` for the same key (`model.go:258-265`), and a retry (`Attempt+1`, state `running`) still reads as live because its state is not terminal.
- **Cutting a row wider than the panel:** drop whole kinds from the left of the live kind, marking the cut with a dim `…`, then right-cut whatever is still too wide (taken) vs. the current plain right-cut (`ansi.Truncate`, `view.go:116`) vs. a left cell-cut. A configured pipeline plus `milestone` (`wire.go:463-466` passes every pipeline name) is wider than the 47-cell side-by-side panel at 80 columns, and a right-cut drops the live kind the title names; a left cell-cut keeps the tail but can still cut a live kind that sits mid-pipeline and splits kinds mid-word.
- **The `len(m.Steps) > 0` guard at `view.go:115`:** remove it (taken) vs. keep it. With it, an empty pipeline (`Header.Steps` empty; `core.Pipeline` at `internal/core/kinds.go:51-68` accepts an empty entry list) hides the title's kind; without it the line always has at least the live kind.

## Changes

1. **Modify `internal/face/tui/view.go`**
   - `panel` (`view.go:115-117`): drop the `if len(m.Steps) > 0` guard; the steps line is always drawn for a live step as `label := th.Text.Render(fmt.Sprintf("%-10s ", "steps"))` followed by `m.pipeline(s, w-lipgloss.Width(label))`, the whole still wrapped in the existing `ansi.Truncate(…, w, "…")`. Serves: title kind always on the line; one row cut with `…`.
   - `pipeline` (`view.go:157-173`) becomes `func (m Model) pipeline(s *Step, w int) string`:
     - build `kinds := m.Steps`; when `!slices.Contains(kinds, s.Kind)`, set `kinds = append(slices.Clone(m.Steps), s.Kind)`; iterate `kinds` instead of `m.Steps`, and record `live`, the index of `s.Kind` in `kinds`.
     - change the first case to `kind == s.Kind && s.State != string(core.StepOK) && s.State != string(core.StepFailed)` so an ended live step falls through to the existing `done` cases (`th.Landed` `kind ✓`, `th.Failed` `kind ×`).
     - with `sep := th.Idle.Render(" › ")`, set `line := strings.Join(parts, sep)`; then `for drop := 1; lipgloss.Width(line) > w && drop <= live; drop++ { line = th.Idle.Render("…") + sep + strings.Join(parts[drop:], sep) }`; return `ansi.Truncate(line, w, "…")`. Kinds before the live one go first, whole, so the live kind stays; only when the live kind plus what follows it is still wider than `w` does the right-cut take pending kinds after it.
     - styles stay `th.Current` (primary, no bold), `th.Landed`, `th.Failed`, `th.Idle`; no amber (`th.Warn`/`HeaderWaiting`) and no bold (`th.Live`) are used. Add `"slices"` to the imports.
     Serves: title kind always on the line, also when the row is cut; milestone live then `✓`/`×`; no milestone entry for a phase that closes none; one row cut with `…`; no amber, no bold.
   - Every existing `m.pipeline(m.Live)` call in tests becomes `m.pipeline(m.Live, 200)` (wide enough never to cut).

2. **Modify `internal/face/tui/model_test.go`**
   - `newModel` (`model_test.go:71`): `Steps: []string{"plan", "implement"}` — the default pipeline the real wiring produces (`wire.go:463-466`, default `[plan, implement]`).
   - Rewrite `TestTheStepsLineTracksDoneFailedLiveAndPendingKinds` (`model_test.go:289-310`) without the `land` kind (see Tests).
   - Add the new model tests below.
   - `model_test.go:196` (`gate-fix` event with `Step: "land"`) stays: it tests feed text, and `land.go:53` really emits `Step: "land"` on `gate-fix`.

3. **Modify `internal/face/tui/view_test.go`** — add `TestTheStepsLineStaysOneRowWithNoAmberOrBold`.

4. **Regenerate `internal/face/tui/testdata/frame-120x40.golden` and `frame-70x30.golden`** with `go test ./internal/face/tui/ -run 'TestFrameAt' -update`, then check the diff by hand with `git diff -U0 internal/face/tui/testdata/`: each file must change exactly one line, `steps      plan ✓ › implement › land` → `steps      plan ✓ › implement` (120x40 line 4, inside the rail row `  p  2 config reader        │  `; 70x30 line 10). Any other changed line is a regression to fix in code, not to accept into the golden.

`internal/face/tui/model.go` is not changed.

## Tests

Write these first; all live in `internal/face/tui`. `step(...)`, `recorded()`, `at(...)`, `newModel(...)`, `ansiStrip(...)` are the existing helpers in `model_test.go`. `recorded()[:9]` ends with phase 1 planned, implemented and landed, live step phase 1 `implement` `ok`; `recorded()[:11]` ends with phase 2 `plan` running.

- `TestTheStepsLineTracksDoneFailedLiveAndPendingKinds` (rewritten, `model_test.go`):
  - `newModel(recorded()[:11])` → `ansiStrip(m.pipeline(m.Live)) == "plan › implement"` (pending kinds dim).
  - `newModel(recorded())` → `"plan ✓ › implement"` (done + live).
  - apply `step(50, 2, "implement", "failed", "codex", "gpt-5", "medium", "ws-4")` → `"plan ✓ › implement ×"` (an ended live step shows its end state).
  - apply the existing retry event (attempt 2, running, round 1, half `fix`) → `"plan ✓ › implement"` and `m.Live.Label() == "implement a2 · review r1/2 fix"` (a retry after a failure reads as live).
  Covers: real step lists in the fixture; ended live step; retry.
- `TestTheStepsLineShowsTheMilestoneWhileItRunsAndWhenItEnds` (new, `model_test.go`): from `recorded()[:9]`, apply `step(22, 1, "milestone", "running", "claude", "opus", "high", "ws-m")` → pipeline `"plan ✓ › implement ✓ › milestone"` and `m.View()` contains `PHASE 1 · milestone`. Then from that model, `step(23, 1, "milestone", "ok", …)` → `"plan ✓ › implement ✓ › milestone ✓"`; separately `step(23, 1, "milestone", "failed", …)` → `"plan ✓ › implement ✓ › milestone ×"`. Covers: milestone live, ✓, ×.
- `TestTheStepsLineShowsNoMilestoneForAPhaseThatClosesNone` (new, `model_test.go`):
  - `newModel(recorded()[:9])` (phase 1 landed, no milestone event) → pipeline `"plan ✓ › implement ✓"`.
  - from `recorded()[:9]` + phase 1 `milestone` running + `ok` + `{Kind: "phase-start", Phase: "2"}` + `step(24, 2, "plan", "running", …)` → `"plan › implement"` (phase 1's milestone does not carry into phase 2).
  Covers: no entry without a milestone.
- `TestTheStepsLineShowsEveryKindTheTitleNames` (new, `model_test.go`), table over events appended to `recorded()[:9]`:
  - `gatefix` running → View contains `PHASE 1 · gatefix`, pipeline `"plan ✓ › implement ✓ › gatefix"`; then `gatefix` ok → `"plan ✓ › implement ✓ › gatefix ✓"`.
  - backlog `gate` running → `PHASE 1 · gate`, `"plan ✓ › implement ✓ › gate"`.
  - `gatefix` running, `gatefix` ok, `milestone` running (the order `land.go:50-68` produces) → exactly `"plan ✓ › implement ✓ › milestone"`.
  - empty pipeline: `NewModel(Header{RunID: "r1", Todo: "todo.md", Started: t0}, plan(), NewTheme(lipgloss.NewRenderer(io.Discard), false))` + `step(1, 1, "milestone", "running", …)` → View contains `steps      milestone`.
  Covers: title kind always on the line, including `gatefix`, `gate`, `milestone`, and with no pipeline kinds; the milestone line's exact shape after a gate fix.
- `TestTheStepsLineStaysOneRowWithNoAmberOrBold` (new, `view_test.go`): a TrueColor model (`lipgloss.NewRenderer(io.Discard)` + `SetColorProfile(termenv.TrueColor)`, as `coloured` does at `view_test.go:106-114`) with `Header{RunID: "r1", Todo: "todo.md", Started: t0, Steps: []string{"plan", "implement", "document"}}` (a configured three-kind pipeline), fed `phase-start` 1, `plan` running/ok, `implement` running/ok, `document` running/ok and `milestone` running, all phase 1. The uncut steps line is `steps      plan ✓ › implement ✓ › document ✓ › milestone` (56 cells). Table over width (height 40) → expected stripped steps line:
  - 80 (side-by-side, panel 47, 36 cells after the label) → `steps      … › document ✓ › milestone`;
  - 70 (stacked, 66 cells) → `steps      plan ✓ › implement ✓ › document ✓ › milestone` (uncut);
  - 50 (stacked, 46 cells, 35 after the label) → `steps      … › document ✓ › milestone`.
  For every width:
  - exactly one line of `m.View()` contains `steps`, and the line after it contains `provider` (one row);
  - `fits(t, view, width, 40)`;
  - the stripped steps line equals the expected line above, so the cut widths show both `…` and `milestone`;
  - the steps line matches `\x1b\[38;2;110;159;196mmilestone` (primary, no bold on the live kind);
  - the steps line has no amber (`\x1b\[38;2;224;16[34];88`) and no bold: for each SGR `\x1b\[([0-9;]*)m`, walk its `;`-split params, skipping the four params after a `38`/`48` followed by `2`; no remaining param equals `1`.
  A second case, width 30 (stacked, 26 cells, 15 after the label), with the same pipeline and only `phase-start` 1 + `plan` running: the stripped steps line is `steps      plan › impleme…` — the live kind is first, nothing lies before it to drop, and the pending kinds after it are right-cut.
  Covers: one row cut with `…` at 80 and below, in both layouts; the live kind survives the cut; no amber; no bold; live milestone in primary.
- `TestFrameAt120x40`, `TestFrameAt70x30StacksTheRailAboveThePanel` (existing, `view_test.go:63-88`), run against the regenerated goldens. Covers: golden frames use a real step list.

## Left out

- A per-phase record of extra kinds on `Model`: the live step's kind is enough for every open item, and a history would keep `gatefix ✓` on the milestone line.
- A `land` stage ticked by `landed`: no open item asks for it and the notes allow dropping the phantom; the real wiring emits `landed`, not a `land` step.
- Changing `internal/app/wire.go:463-466`: `Header.Steps` stays the pipeline kinds; the extra kind comes from the live step.
- Clearing `m.Live` on `finished`/`halt` and fixing the retry's stale `Ended`: that is issue [#1], its own phase.
- Changing `model_test.go:196`'s `Step: "land"`: it matches the real `gate-fix` event (`internal/core/land.go:53`) and is not a steps list.

## Assumptions

- Watchdog warning "Files: none understates the cut": resolved. The plan names every file it changes — `internal/face/tui/view.go`, `model_test.go`, `view_test.go` and both goldens — and the entry comes from the phase's live `step` event, not a static list. `internal/face/tui/model.go` needs no change because `m.Live` already carries the kind (`model.go:229-267`), and `internal/app/wire.go` is not changed because `Header.Steps` correctly stays the pipeline kinds.
- Watchdog warning "Risk: none overstates": resolved by step 4 of Changes — the regenerated goldens are checked by hand with `git diff -U0` and may change only the one steps line each; the width/one-row/no-amber/no-bold properties are pinned by `TestTheStepsLineStaysOneRowWithNoAmberOrBold`, and `fits` keeps checking the frames.
- The extra kind is placed after the pipeline kinds; neither `DESIGN.md:103-105`, `tech-design.md` nor the spec says where `milestone` sits, and the notes call appending the natural reading.
- Reviewer panes need no entry: they emit no `step` events to the Face (`internal/core/review.go` has no `emit`/`recordEvent` call); their rounds reach the Face as the worker's own kind with `round`/`half` fields, which `Step.Label` already shows.

## Gate

`go test ./internal/face/tui/ -run '^(TestTheStepsLineTracksDoneFailedLiveAndPendingKinds|TestTheStepsLineShowsTheMilestoneWhileItRunsAndWhenItEnds|TestTheStepsLineShowsNoMilestoneForAPhaseThatClosesNone|TestTheStepsLineShowsEveryKindTheTitleNames|TestTheStepsLineStaysOneRowWithNoAmberOrBold|TestFrameAt120x40|TestFrameAt70x30StacksTheRailAboveThePanel)$'`
