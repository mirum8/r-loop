status: planned

## Summary

The phase check prompt stops presenting `Files: none` / `Risk: none` as a backlog item's claims. `PhaseCheck` gains a `Backlog bool` field, set in the wiring from the loaded plan the same way `SessionManager.ItemGates` is (`internal/app/wire.go:337`). When it is set, `checkText` swaps the `Files:` and `Risk:` lines for one `Backlog item:` line. The watchdog's "Checking a phase" section gets one bullet: for such a check, it still derives the cut and warns on a real mismatch between the item and the code, but never because the item has no `Files:` or `Risk:` line. A plan-file phase goes through the current code path unchanged, including the `none` fallback.

Choices:
- **Where the backlog flag reaches the check:** a `PhaseCheck.Backlog` field set in `startWatchdog` (`internal/app/wire.go:428`), not a new parameter on `Watch.BeforePhase` / `PhaseCheck.Run` fed from the loop (`internal/core/watch.go:159,172`). The flag is fixed for the whole run, and the field follows the existing `ItemGates: pl.Backlog` pattern. A parameter would change a port-facing signature and every caller and fake for a constant value.
- **What a backlog check sends in place of the two lines:** one explicit line, `Backlog item: an issues file names no files and no risk`, not dropping the lines silently. The watchdog needs a marker it can key its rule on. Without one, a missing `Files:` line looks like a malformed prompt. The line itself avoids the literal `Files:` / `Risk:` tokens, so the prompt carries no claim to compare against.
- **Keeping the `check phase <N> …` header:** kept unchanged, not a new `check item …` header. The header is matched by prefix in `internal/core/phasecheck_test.go:98,130` and `internal/app/watchdog_test.go:252`, and by the watchdog template (`internal/prompts/templates/watchdog.md:44`). The item is still phase N to the driver.

Obligations (the floor):
1. For a backlog item, the check prompt carries neither `Files:` nor `Risk:` (open item 1; source: `internal/plan/backlog.go:49-51` never sets `Phase.Files`/`Risk`, and `internal/core/phasecheck.go:43-50` falls back to `none`).
2. The watchdog prompt tells it to judge a backlog item against the code and not to warn about the missing lines (open items 1–2; `internal/prompts/templates/watchdog.md:44-46`).
3. A warn on a backlog item's check is still accepted and still reaches the plan prompt and the report (open item 2; `Watch.Handle` during `checking`, `internal/core/phasecheck_test.go:117`).
4. A plan-file phase with explicit `Files:`/`Risk:` gets the same prompt as today (open item 3; `internal/core/phasecheck_test.go:92`).
5. A plan-file phase with no `Files:`/`Risk:` still gets `Files: none` / `Risk: none`, since "checked exactly as today" covers the fallback (open item 3; `internal/core/phasecheck.go:45-50`).
6. The flag is actually wired: a run over an issues file sets `PhaseCheck.Backlog`, and a run over a todo leaves it false (watchdog warning; `internal/app/wire.go:428`, `internal/core/types.go:60`).
7. ADR-53 (spec.html:2558): the check may only warn, the phase runs regardless, and the threshold lives in the watchdog prompt. Nothing here changes the warn-only rule or the halt rejection.

## Changes

1. **Modify `internal/core/phasecheck.go`** (obligations 1, 4, 5, 7)
   - `PhaseCheck` gets a new field `Backlog bool`, after `Timeout`.
   - `Run` passes it on: `checkText(ph, filepath.Join(c.Repo.Root(), wt), base, c.Backlog)`.
   - `checkText(ph Phase, worktree, base string, backlog bool) string`: when `backlog` is true, return
     `fmt.Sprintf("check phase %s worktree %s base %s\nBacklog item: an issues file names no files and no risk\n\n%s", ph.ID, worktree, base, ph.Block)`.
     Otherwise run the existing body unchanged: the `none` fallbacks and the same format string.
   - No other signature changes. `Watch.BeforePhase` (`internal/core/watch.go:159-173`) stays as it is.

2. **Modify `internal/app/wire.go`** (obligation 6)
   - At line 428: `w.Watch.PhaseCheck = &core.PhaseCheck{Dog: w.Dog, Repo: w.Loop.Sessions.Repo, Timeout: wd.CheckTimeout, Backlog: w.Plan.Backlog}`. `w.Plan` is the same loaded plan that `preflight.go:104` reads, following `ItemGates: pl.Backlog` at `wire.go:337`.

3. **Modify `internal/prompts/templates/watchdog.md`** (obligations 2, 7)
   - Under `## Checking a phase`, insert a new bullet directly after the line at `:45` ("First read the phase block … compare that with its `Files:` and `Risk:` lines."), with this exact text:
     `- A check that carries a \`Backlog item\` line in place of those lines is an issues-file item, which names neither by design: derive the files and the depth the same way, and warn only where the item disagrees with the code — never because it has no \`Files:\` or \`Risk:\` line.`
   - Leave the other bullets unchanged, including the warn-only / never-halt bullet at `:47`.

## Tests

Write these first. The fixtures reuse `newCheckRig` (`internal/core/phasecheck_test.go:50`), `indexOf` (`:87`), `planWarnings` (`:66`), `report` (`:77`), `render`/`fullVars` in `internal/prompts/render_test.go`, and `newResumeFixture`/`dogHost` in `internal/app`.

In `internal/core/phasecheck_test.go`:
- `TestABacklogItemCheckCarriesTheBlockWithoutFilesOrRisk`: `newCheckRig`, then set `Phases[0].Files = nil` and `Risk = ""`, and `r.watch.PhaseCheck.Backlog = true`. Run with `Phases: {"1"}`. Find the `SessionHost.Prompt rloop-wd-run-1 "check phase 1` call. It must contain `check phase 1`, `Backlog item: an issues file names no files and no risk` and `### Phase 1 — Core types`. It must contain neither `Files:` nor `Risk:`. Covers 1.
- `TestAPlanFilePhaseWithoutFilesOrRiskStillSaysNone`: `newCheckRig`, then set `Files = nil` and `Risk = ""`, and leave `Backlog` false. The check prompt contains `Files: none` and `Risk: none`, and does not contain `Backlog item`. Covers 5.
- `TestAWarnDuringABacklogItemCheckReachesThePlanPrompt`: `newCheckRig` with `Backlog = true`. `onPrompt` signals `SignalWarn` on step `check` with reason `item: internal/core/a.go:3 already does it` when the text starts with `check phase 1`. Assert the signal is accepted, the run exits 0, `planWarnings` is `- item: internal/core/a.go:3 already does it`, and the report contains `phase 1 phase check: item: internal/core/a.go:3 already does it — landed`. Covers 3 and 7.
- The existing `TestPhaseCheckCreatesTheWorktreeThenWaitsOnTheCheckPromptBeforeThePlanSpawns` stays unchanged and pins 4. It is in the gate.

In `internal/prompts/render_test.go`:
- `TestWatchdogPhaseCheckJudgesABacklogItemAgainstTheCode`: render `watchdog` with `fullVars()`. It must contain `carries a \`Backlog item\` line in place of those lines`, `warn only where the item disagrees with the code` and `never because it has no \`Files:\` or \`Risk:\` line`. Covers 2.

In `internal/app/watchdog_test.go`:
- `TestABacklogRunWiresThePhaseCheckForBacklogItems`: `newResumeFixture(t, noReviewConfig)`. Write `issues-x-2026-09-23.md` as `"# X\n\n- [ ] [#1] first\n      - crit\n"`, then `f.commit()`. Call `f.preflight(filepath.Join(f.root, "issues-x-2026-09-23.md"), "--plain", "--phases", "1")` and fatal on error. Set `w.Dog.Host = &dogHost{}` and call `w.startWatchdog(context.Background())`, fatal on error. Assert `w.Watch.PhaseCheck != nil && w.Watch.PhaseCheck.Backlog`. This shape was verified to wire and start against a scratch test. Covers 6.
- Extend `TestExecuteSendsThePhaseCheckToTheWatchdogBeforeThePlanStep` (`internal/app/watchdog_test.go:232`): add `|| pc.Backlog` to the condition at `:247`, so a todo run's check is not flagged as a backlog. Covers 6 and 4.

## Left out

- A `Backlog` field on `core.Phase`, set by the plan reader: this is a second place for a per-run fact that `Plan.Backlog` already holds.
- A parameter on `Watch.BeforePhase`/`PhaseCheck.Run`: the flag is fixed for the run, so the field is enough (see Summary).
- Any change to how `internal/plan/backlog.go` parses items, for example synthesising `Files:`: the issues format leaves them out by design, and the open items say not to invent them.
- A spec or ADR edit: ADR-53 puts the check's threshold in the watchdog prompt, and that prompt is what changes.

## Assumptions

- The watchdog's warning about the notes ("the check 'already has' the backlog flag, but it doesn't") is correct. `Backlog` is only on `core.Plan` (`internal/core/types.go:60`), and `PhaseCheck.Run` receives only the `Phase` (`internal/core/watch.go:172`). The plan plumbs it as a `PhaseCheck` field set in `internal/app/wire.go:428`, the first of the two routes the warning names. The warning's other points are resolved as follows: `phasecheck.go` and `watchdog.md` are both changed, with tests for both. The phase's real cut is the six files named above.
- `wire.go:428` reads `w.Plan.Backlog`. `w.Plan` is the plan preflight loaded and uses at `internal/app/preflight.go:104`, the same value as `pl` at `wire.go:337`.
- The exact wording of the `Backlog item:` line and of the template bullet is fixed as given above. Tests pin it.

## Gate

`go test ./internal/core/ -run '^(TestABacklogItemCheckCarriesTheBlockWithoutFilesOrRisk|TestAPlanFilePhaseWithoutFilesOrRiskStillSaysNone|TestAWarnDuringABacklogItemCheckReachesThePlanPrompt|TestPhaseCheckCreatesTheWorktreeThenWaitsOnTheCheckPromptBeforeThePlanSpawns)$' && go test ./internal/prompts/ -run '^TestWatchdogPhaseCheckJudgesABacklogItemAgainstTheCode$' && go test ./internal/app/ -run '^(TestABacklogRunWiresThePhaseCheckForBacklogItems|TestExecuteSendsThePhaseCheckToTheWatchdogBeforeThePlanStep)$'`
