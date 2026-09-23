status: planned

## Summary

A reviewer pane is split with `Host.Split(target, direction, worker.Dir)` (`internal/core/review.go:193`), and that call carries no environment, so a reviewer's shell (and a skill it runs, such as `/test-app`) cannot tell it is inside a live run. This phase widens the `SessionHost.Split` port with an `env map[string]string` argument. The review half passes `R_LOOP_RUN`, `R_LOOP_PHASE` and `R_LOOP_STEP`, with the same values the step session gets (`internal/core/session.go:165-170`), plus `R_LOOP_REVIEWER` = the reviewer's ID. The herdr adapter turns the map into `--env K=V` flags on `herdr pane split`, as `Client.Open` already does for `workspace create`. The watchdog and intake splits pass `nil` and keep getting no environment. The step session's `Open` is not touched. The repo's `/test-app` skill is changed to treat `R_LOOP_RUN` as the signal that it is inside a live run.

This was checked live on the installed herdr 0.9.0 on 2026-09-23: `herdr pane split --env R_LOOP_PROBE=yes42` followed by `pane run <pane> 'echo probe=$R_LOOP_PROBE'` printed `probe=yes42` in the split pane and an empty value in the root pane. `herdr agent start` runs the agent from that pane's interactive shell (per its `--help`), so the agent process inherits the variables.

Choices:
- **How the env reaches the split: widen `Split(pane, direction, cwd string)` to `Split(pane, direction, cwd string, env map[string]string)`.** This beat adding a second port method `SplitEnv`. It adds no new concept, and it follows `OpenSpec.Env` (a map, `nil` means none). A second method would leave two ways to split a pane on the port and in every fake.
- **Where the adapter's flag loop lives: extract the existing sorted `--env` loop from `Client.Open` (`internal/herdr/client.go:83-91`) into `envFlags(env map[string]string) []string`, and call it from both `Open` and `Split`.** This beat copying the loop. The two must emit the same flag format and change together.
- **Values of the three shared variables: the worker's `StepKey` (`R_LOOP_STEP` = the reviewed step's kind, e.g. `implement`, not `implement-rv-codex`).** This beat the reviewer's own derived kind. The item says the variables are set "as a step session's are".
- **Name of the reviewer variable: `R_LOOP_REVIEWER`, value `Reviewer.ID()` (`internal/core/kinds.go:27`: the `name`, or the provider).** This beat `R_LOOP_REVIEWER_AGENT` (the herdr agent name). The ID is what the round's events, findings files and `report.md` already use for "the reviewer".
- **How the fakes observe env: add a `Splits []map[string]string` slice to `fakeSessionHost` and leave its call-log line unchanged.** This beat appending `%v` of the env to the `SessionHost.Split …` log line. Unchanged lines keep every existing call-log assertion as it is (`review_test.go:339-363`, `watchdog_test.go:44,110,139`, `intake_test.go:50`), and the slice follows `Opened []OpenSpec`.
- **`/test-app` detection: `R_LOOP_RUN` set, or the working directory under `.r-loop/wt/`.** This beat dropping the path check. The r-loop binary driving a run can be older than this phase and still split reviewers with no environment, so the path check stays as the fallback.

## Changes

1. **Modify `internal/core/ports.go:51`.** Change the `SessionHost` method to `Split(pane, direction, cwd string, env map[string]string) (string, error)`. Obligation: O1 (the port can carry a reviewer's env).

2. **Modify `internal/core/review.go` `ReviewHalf.open` (`:193`).** Replace the call with:
   ```go
   pane, err := sm.Host.Split(target, direction, worker.Dir, map[string]string{
   	"R_LOOP_RUN":      worker.Ref.Key.Run,
   	"R_LOOP_PHASE":    worker.Ref.Key.Phase,
   	"R_LOOP_STEP":     worker.Ref.Key.Kind,
   	"R_LOOP_REVIEWER": rv.ID(),
   })
   ```
   The map literal follows the step's own at `internal/core/session.go:165-170`. Every round re-splits through this one call (round 2 onward closes the previous panes first, `review.go:176-180`), so every round's reviewer gets the env. A stacked second reviewer gets it too. The split's error path (`review.go:194-196`, `failed(reviewer <id>: …)`) is unchanged. Obligations: O1, O2, O5.

3. **Modify `internal/core/watchdog.go:105` and `internal/core/intake.go:47`.** Add a trailing `nil` argument: `d.Host.Split(d.Pane, "right", d.Root, nil)` and `in.Host.Split(in.Pane, "right", in.Root, nil)`. Obligation: O4 (other panes keep no env).

4. **Modify `internal/herdr/client.go`.**
   - Add `func envFlags(env map[string]string) []string`. It holds the sort-keys-then-`"--env", k+"="+v` loop moved out of `Open` (`:83-91`). It returns nil for a nil or empty map.
   - `Open` becomes `args = append(args, envFlags(spec.Env)...)`, which gives the same argv as today.
   - `Split(pane, direction, cwd string, env map[string]string)` appends `envFlags(env)...` after `--cwd <cwd>` and before `--no-focus`. The argv becomes `pane split (--pane <p>|--current) --direction <d> --cwd <cwd> [--env K=V…] --no-focus`, the same flag placement as `workspace create`.

   Obligations: O1, O3, O4.

5. **Update the test doubles to the new signature.** No behaviour changes beyond recording in `fakeSessionHost`.
   - `internal/core/fakes_test.go`: add a `Splits []map[string]string` field next to `Opened`. `Split(pane, direction, cwd string, env map[string]string)` keeps its `f.record("SessionHost.Split %s %s %s", …)` line and appends `env` to `f.Splits`.
   - `internal/core/review_test.go:498` `splitFailHost.Split`: add `env` and forward it to `h.scriptedHost.Split(pane, direction, cwd, env)`.
   - `internal/core/land_test.go:633` `reportHost.Split`: add the `env map[string]string` parameter.
   - `internal/app/intake_test.go:51` `intakeHost.Split`: add the parameter.
   - `internal/app/resume_test.go:138` `simHost.Split`: add the parameter.
   - `internal/app/watchdog_test.go:81` `dogHost.Split`: add the parameter, and keep its record line.
   - `internal/herdr/client_test.go:87,98,312` and `internal/herdr/live_test.go` existing `c.Split(...)` calls: add a trailing `nil`.

   Obligation: O6 (the build stays green).

6. **Modify `docs/task-loop-driver/tech-design.md:104-105`.** Change the port line to ``Split(pane, direction, cwd string, env map[string]string) (pane string, error)`` (an empty `pane` splits the pane herdr calls current; `env` becomes `--env K=V` on the split pane's shell, `nil` for none). Add one sentence to the Milestone 4 **Shape** bullet (`:404`): each reviewer pane is split with `R_LOOP_RUN`, `R_LOOP_PHASE`, `R_LOOP_STEP` (the reviewed step's) and `R_LOOP_REVIEWER`. Obligation: O6 (the contract doc matches the port).

7. **Modify `.claude/skills/test-app/SKILL.md:66` and `.claude/skills/test-app/references/subagent-prompt.md:119`.** The "inside an r-loop review" condition becomes: `R_LOOP_RUN` is set (`[ -n "$R_LOOP_RUN" ]`, which a reviewer pane now has and which also names the run and, through `R_LOOP_REVIEWER`, the reviewer), or the working directory is under `.r-loop/wt/` (the fallback for a run driven by an older binary). The rest of the rule is unchanged: no-agent tier and golden harness only, live checks reported as NOT RUN (inside an r-loop review). Obligation: O2 (the skill detects the run from the environment).

## Tests

Write these first. Each fails to compile or fails on base.

- `internal/core/review_test.go` **`TestReviewerPanesAreSplitWithTheStepsRunPhaseStepAndReviewerEnv`**. Use `newReviewRig(t, Reviewer{Provider: "claude"}, Reviewer{Provider: "codex", Name: "second"})` with the behaviour of `TestReviewSplitsStartsThenPromptsAndReplacesPanesWithFreshAgentsInRound2` (round 1 has findings, the fix is real, round 2 is clean), then run it. Assert that `r.host.Splits` equals, with `reflect.DeepEqual`, four maps in order: round 1 `{R_LOOP_RUN: run-1, R_LOOP_PHASE: 3, R_LOOP_STEP: implement, R_LOOP_REVIEWER: claude}` then the same with `R_LOOP_REVIEWER: second`, then those two again for round 2. Also assert that no map has an `R_LOOP_SENTINEL` key. This pins the right values, the stacked (`down`) reviewer and the round-2 re-split. Obligations: O1, O5.
- `internal/core/review_test.go` **`TestAReviewerOfARetriedStepGetsTheSameStepEnv`**. Use `newReviewRig(t, Reviewer{Provider: "codex"})` with `r.worker.Ref.Key.Attempt = 2` and a clean round. Assert that `r.host.Splits[0]` is `{R_LOOP_RUN: run-1, R_LOOP_PHASE: 3, R_LOOP_STEP: implement, R_LOOP_REVIEWER: codex}`. The attempt appears nowhere, as in the step's own env. Obligation: O1.
- `internal/core/watchdog_test.go` **`TestWatchdogStartSplitsThenStartsThenPromptsWithoutWait`** (extend). After the existing call assertion, assert `len(host.Splits) == 1 && host.Splits[0] == nil`. Obligation: O4.
- `internal/core/intake_test.go` **`TestIntakeInsideHerdrSplitsBesideTheDriverAndClosesThePane`** (extend). Assert `len(host.Splits) == 1 && host.Splits[0] == nil`. Obligation: O4.
- `internal/core/session_test.go` **`TestSpawnRecordsSpawnedBeforeOpenThenStartsPromptsAndRecordsRunning`** (existing, unchanged). It already pins the step's `Open` env as exactly `R_LOOP_PHASE`, `R_LOOP_RUN`, `R_LOOP_SENTINEL`, `R_LOOP_STEP` (`:273`), and it is in the gate as the regression pin. Obligation: O3.
- `internal/herdr/client_test.go` **`TestSplitPassesEnvAsSortedEnvFlagsBeforeNoFocus`**. Use `fake(t, <pane_split json>)`, then call `c.Split("w3A:p1", "right", "/repo/wt", map[string]string{"R_LOOP_STEP": "implement", "R_LOOP_RUN": "r1"})`. Assert the argv `pane split --pane w3A:p1 --direction right --cwd /repo/wt --env R_LOOP_RUN=r1 --env R_LOOP_STEP=implement --no-focus` and the returned pane. This proves the driver's env becomes herdr flags. Obligation: O1, O2.
- `internal/herdr/client_test.go` **`TestSplitByPaneID`** and **`TestSplitCurrentPaneWhenPaneIsEmpty`** (existing, now called with `nil`). Their unchanged argv (no `--env`) pins that a nil env adds no flag, which is the watchdog and intake path. Obligation: O4.
- `internal/herdr/client_test.go` **`TestOpenCreatesWorkspaceAndParsesIDs`** (existing, unchanged). It pins that `Open`'s argv is unchanged after the `envFlags` extraction. Obligation: O3.
- `internal/app/watchdog_test.go` **`TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5`** and `internal/app/resume_test.go` **`TestResumeAfterAKilledDriverClosesItsStaleWatchdogAndStartsItsOwn`** (existing, unchanged). Running them compiles the `internal/app` test package, which holds the widened `intakeHost`, `simHost` and `dogHost` doubles, so a missed signature fails the gate. They also pin that the wired watchdog still splits beside the driver (`watchdog_test.go:157`, `resume_test.go:1088`). Obligations: O4, O6.
- `internal/herdr/live_test.go` **`TestLiveHerdr`** (extend; it runs only with `R_LOOP_LIVE_HERDR=1`). Change the `extra` split to `c.Split(ws.RootPane, "down", dir, map[string]string{"R_LOOP_LIVE_SPLIT": suffix})`. Before closing it, run `c.exec("pane", "run", extra, "echo split-env=$R_LOOP_LIVE_SPLIT")`, then `c.exec("pane", "wait-output", "--match", "split-env="+suffix, "--timeout", "10000", extra)`, and `t.Fatalf` on either error. This proves the variable reaches the process in the split pane against the installed herdr 0.9.0. Obligation: O2 (the "reaches the reviewer's process" proof).

## Left out

- `R_LOOP_SENTINEL` for reviewers. The item asks only for run, phase, step and a reviewer name. The step's sentinel is not the reviewer's, and the reviewer already gets its own sentinel path in its prompt (`review.go:291`).
- `R_LOOP_ROUND` and the reviewer's agent name. No open item asks for them.
- A `SplitSpec` struct parallel to `OpenSpec`. One extra map argument carries everything, and a struct would add a type for a single field.
- Env on the watchdog and intake panes. The phase check says they keep getting none, and nothing asks otherwise.
- A resume change. Resume does not split reviewer panes (no `Split` call in `internal/app` outside tests), and a resumed step's review half goes through `ReviewHalf.open`.

## Assumptions

- The watchdog's warning is resolved in the plan, not waived. The port is widened (Change 1), the herdr adapter passes `--env` on `pane split` (Change 4), an adapter argv test pins it (`TestSplitPassesEnvAsSortedEnvFlagsBeforeNoFocus`), and the live test proves the shell sees it (`TestLiveHerdr`). The other callers `intake.go:47` and `watchdog.go:105` pass `nil`, and tests pin that (Change 3). The core fakes are updated (Change 5).
- The live proof (`TestLiveHerdr`) cannot run in the gate. It needs a herdr server and `R_LOOP_LIVE_HERDR=1`, so in the gate it skips and passes. The same check was run by hand against herdr 0.9.0 while planning (see Summary). The gate still fails on base because every other named test is new or calls the widened `Split`.
- "A variable naming the reviewer" means `R_LOOP_REVIEWER=<Reviewer.ID()>`, following the `R_LOOP_*` naming of the step env.
- The obligations are numbered as follows. O1: the reviewer's split carries `R_LOOP_RUN`, `R_LOOP_PHASE` and `R_LOOP_STEP` with the step's values, plus `R_LOOP_REVIEWER`. O2: a reviewer-run skill detects the live run from the env, proven down to the process. O3: the step session env is unchanged. O4: the watchdog and intake splits get no env. O5: every round and every stacked reviewer gets the env. O6: the build and the contract doc stay consistent.

## Gate

`go test ./internal/core/ ./internal/herdr/ ./internal/app/ -run 'TestExecuteStartsTheWatchdogAndAHaltThroughItsMCPSurfaceExits5|TestResumeAfterAKilledDriverClosesItsStaleWatchdogAndStartsItsOwn|TestReviewerPanesAreSplitWithTheStepsRunPhaseStepAndReviewerEnv|TestAReviewerOfARetriedStepGetsTheSameStepEnv|TestWatchdogStartSplitsThenStartsThenPromptsWithoutWait|TestIntakeInsideHerdrSplitsBesideTheDriverAndClosesThePane|TestSpawnRecordsSpawnedBeforeOpenThenStartsPromptsAndRecordsRunning|TestSplitPassesEnvAsSortedEnvFlagsBeforeNoFocus|TestSplitByPaneID|TestSplitCurrentPaneWhenPaneIsEmpty|TestOpenCreatesWorkspaceAndParsesIDs|TestLiveHerdr'`
