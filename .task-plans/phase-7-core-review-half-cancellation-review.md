status: planned

## Summary

When the step context is cancelled (abort or interrupt), the review half now gives up at once with one unprefixed reason, `interrupted: context canceled`. It does not report the reviewer's failure, and it does not ask the watchdog. A new helper in `internal/core/review.go`, `interrupted(ctx, worker)`, builds that outcome. Three places in `review.go` call it once `ctx.Err() != nil`:

- **`join`**, after every reviewer's `review-find` is recorded. This covers #7 for the first round, including a native exec review that failed because of the cancel.
- **`recover`**, at the top of its retry loop. This covers #7 when a reviewer the watchdog retried is the one working at the abort. Without it, the abort raises a second blocker.
- **`settleStatic`**, at the top of its loop, before it accepts or reports the analysis. It records the static `review-find` as failed and never reaches `raiser.raise`. This covers #23 on the first run and on a retried analysis. It also covers an analysis that returns a nil error after the cancel. The real analyzer can do that: `Analyze` runs `tidy` under `context.WithoutCancel` and returns the saved nil error (`internal/analyze/run.go:55-59`). Without the check, that round would end clean, and `Finish` would commit and record the step `ok` in a run that is being aborted.

Real failures are unchanged. A reviewer's failure keeps its `reviewer <id>:` prefix, and the static analyzer's keeps the `reviewer static:` blocker with retry/block/stop. The runner hands the failed outcome to `Finish`, which records the step. `runStep` then routes it to `stopStep`, which records the run halted/aborted and emits the `aborted` event that report.md renders. Nothing in loop.go changes.

Choices:
- **Where the reason is fixed: once per site that turns a failure into the step's outcome, or at each place that builds a `reviewer <id>:` string.** Per outcome site (`join`, `recover`, `settleStatic`). The prefix is built at more than eight places (`review.go:486, 508, 526, 555, 602, 655, 675, 678`, `nativereview.go:45`), and `judge` must keep building it, because `review-find` and real blockers still need it.
- **The guard's shape: a `ReviewHalf`-local helper `interrupted(ctx, worker) Outcome`, or the expression inlined at each site.** The helper. Three sites must give the same reason shape and change together. It uses `ctx.Err()`, like `waitAll`'s cancel branch (`session.go:451`).
- **#23's check: in `settleStatic` before the raise, or a generic ctx guard in `hold` (`blockers.go:119`).** `settleStatic`. `hold` also serves land, gate and step blockers, and its withdrawn path (`TestAnInterruptOrAnAbortWithdrawsTheBlocker`, `blockers_test.go:638`) depends on recording the question first. The phase check said to avoid touching it.
- **When the context is cancelled: is the static `review-find` recorded failed before the return, or is the return made before it?** It is recorded first, as `join` does for reviewers (#7's third bullet). The event then says what happened to the analyzer in that round.
- **`Stalled` on the interrupted outcome: carry `first.stalled`, or leave it false.** Leave it false. `waitAll`'s cancel branch never sets it (`session.go:451`), and a cancelled reviewer did not stall.

## Why this approach

**#7.** The triage proposed returning `'interrupted: ' + ctx.Err()` at the cancelled branch of `join` (`review.go:575`), without the prefix or `Stalled`. It would leave `judge`/`found` alone, so `review-find` still records failed, and add the same ctx check on the `open()` path. The phase check agreed and said to override the reason once at `review.go:575`, not at each prefix site. This plan follows that for `join`. It splits the combined condition `first != nil && (h.raiser() == nil || ctx.Err() != nil)` in two: cancellation comes first and gives the interrupted outcome, and a missing raiser keeps `first.reason`.

The plan does **not** add a separate check in `open()`. A cancel can reach `open` in two ways:
- **A failing `ClosePane` (`review.go:381`).** The call takes no context, so a cancel cannot cause it.
- **A native exec review (`nativeReviews`, `nativereview.go:31-48`).** Its runner returns `interrupted: context canceled` (`internal/reviewexec/run.go:41`). With a raiser, the `failed` closure (`review.go:386-392`) stores that as `r.fail` and returns no failure. The round then reaches `join`, where the new check catches it. Test 7 pins this.

The no-raiser branch (`review.go:387-388`) cannot be reached in a real run:
- `raiser()` is nil only when `l.holds()` is false (`blockers.go:54-56`).
- `wire.go:605` always sets `Loop.Watcher`.
- `watchdog.blockerTimeout` must be a positive duration (`internal/config/reader.go:297-299`).

The plan also adds the guard in `recover` (`review.go:614`), which the triage did not name. Its source is real: the watchdog resolves a reviewer blocker `retry`, and then the maintainer aborts while the reopened reviewer works. `reopen` → `await` → `waitAll` returns `interrupted` (`review.go:722`). `judge` sets `r.fail` with the prefix (`review.go:602`). The loop at `review.go:614` then calls `raiser.raise` with a cancelled ctx (`review.go:624`). That records a question, emits `blocked-on`, and ends the step with the prefixed reason and `blocked: true`. This is #7's symptom, in a reviewer that is working.

**#23.** The triage and the phase check both put the ctx check in `settleStatic`, after `foundStatic` and before `raiser.raise` (`review.go:280-288`). It returns a failed outcome with reason `interrupted: <ctx.Err()>`, so `runStep` routes it to `stopStep` and the run records aborted. This plan follows that, with #7's helper, and records the static `review-find` as failed before returning. It moves the check one step earlier, to the top of the loop (`review.go:268`), so the check runs before the success branch too. The real analyzer can return a nil error after the cancel (`internal/analyze/run.go:55-59`: `tidy` runs under `context.WithoutCancel` and the saved nil error is returned). An error-branch-only check would let that round end clean, and the step would be committed and recorded `ok` in a run that is being aborted. At the loop top, the check also covers a retried analysis (`review.go:290-291`) that the abort cancels. It does not touch `hold` (`blockers.go:119-165`), for the reason given under Summary.

**The run record and report.md need no change.** On abort, `runStep`'s ticker branch records `aborted` and cancels the step (`loop.go:1206-1209`). `stopStep` → `stop` → `announceAbort` (`loop.go:1271-1281`, `1718-1727`, `1738-1741`) emits the `aborted` event. `haltLines` renders that event as `aborted during phase N <step>` (`report.go:185-186`). All of this happens whatever the step's reason is. Tests 8, 9 and 11 pin it.

**Files outside `review.go`:**
- `review_test.go`, `nativereview_test.go`, `loop_test.go`, `loop_events_test.go` and `blockers_test.go` take the tests, each beside the rig it uses: `newReviewRig`, `execReviewRig`, `newLoopRig`+`abortingReviewerHost`, `newEventsRig` (the only loop rig with a watcher, so a raiser exists), and `newSourceRig` for test 13, whose watcher settles a real blocker `retry`.

## Changes

1. **`internal/core/review.go` (modify).**
   - Add, next to `reviewerKey` (`review.go:730`):
     ```go
     func interrupted(ctx context.Context, worker *Session) Outcome {
     	return Outcome{State: StepFailed, Reason: "interrupted: " + ctx.Err().Error(), Session: worker}
     }
     ```
     It follows the shape of `waitAll`'s cancel branch (`session.go:451`). Serves #7 and #23.
   - **`settleStatic`** (`review.go:263`): make the first statement inside `for {` (`review.go:268`), before `if run.err == nil {` (`review.go:269`):
     ```go
     if ctx.Err() != nil {
     	if err := h.foundStatic(worker, rd.n, StepFailed, 0, ""); err != nil {
     		return nil, 0, sm.fail(worker, "record: "+err.Error())
     	}
     	return nil, 0, interrupted(ctx, worker)
     }
     ```
     The outcome has `blocked` and `Halted` false, and no `raise` call is made. The check covers three cases: an analyzer error after the cancel, a nil-error result after the cancel (the real analyzer's `tidy` path, `internal/analyze/run.go:55-59`), and a retried analysis (`review.go:290-291`), which loops back to the top. The existing success branch, the error branch and their `foundStatic` calls are otherwise unchanged. With no cancel, an analyzer error still raises the `reviewer static: <err>` blocker with `staticActions`. Serves #23 (all three bullets).
   - **`join`** (`review.go:561`): replace the single condition at `review.go:575-577` with:
     ```go
     if first != nil && ctx.Err() != nil {
     	return 0, interrupted(ctx, worker)
     }
     if first != nil && h.raiser() == nil {
     	return 0, Outcome{State: StepFailed, Reason: first.reason, Session: worker, Stalled: first.stalled}
     }
     ```
     The `judge`/`found` loop above it (`review.go:563-574`) is untouched, so each reviewer's `review-find` still records `failed`. Serves #7 (bullets 1, 3, 4).
   - **`recover`** (`review.go:611`): make the first statement inside `for r.fail != nil {` (`review.go:614`):
     ```go
     if ctx.Err() != nil {
     	return interrupted(ctx, worker)
     }
     ```
     Serves #7 bullet 1, for a reviewer that is working after a retry.

## Tests

All tests are in package `core`. They use the existing rigs, `then`, `raisingObserver`, `recordsForStep`, `assertEqual` and `assertState`, and add no dependency. Each has `// given`, `// when` and `// then` blocks. New test fakes:
- **In `review_test.go`:** `type cancelingAnalyzer struct{ cancel context.CancelFunc }`. Its `Analyze(ctx, dir)` calls `a.cancel()`, waits on `<-ctx.Done()`, and returns `Analysis{}, fmt.Errorf("interrupted: %w", ctx.Err())`, the error shape of `internal/analyze/run.go:226`.
- **In `review_test.go`:** helper `func (r *reviewRig) runCanceledAnalysis() (Outcome, *raisingObserver)`. It makes `ctx, cancel := context.WithCancel(context.Background())` and `obs := &raisingObserver{resolve: then(Resolution{Action: "block"})}`, then returns `ReviewHalf{Sessions: r.sm, Store: r.store, Analyzer: cancelingAnalyzer{cancel: cancel}}.Run(ctx, r.worker.Ref, r.worker, obs), obs`. The `block` resolution keeps the base code from looping on `retry`.
- **In `review_test.go`:** helper `func failCodexThenCancel(t *testing.T, r *reviewRig, cancel context.CancelFunc)`. It sets `r.behave` so that codex's first render writes the failed sentinel, exactly as `failCodexOnce` does (`review_test.go:1441-1457`): `writeFindings(t, vars, "failed", 0)` plus sentinel `{"outcome":"failed","reason":"/review is not available in this session"}`. Codex's second render calls `cancel()`. Any other render calls `writeReview(t, vars, "ok", 0)`.
- **In `nativereview_test.go`:** `type cancelingReviewRunner struct{ cancel context.CancelFunc }`. Its `RunReview(ctx, dir, command, timeout)` calls `r.cancel()` and returns `"", fmt.Errorf("interrupted: %w", ctx.Err())`. Add `context`, `fmt` and `time` to that file's imports.
- **In `loop_events_test.go`:** `type abortingAnalyzer struct{ store *loopStore; err error }`. Its `Analyze(ctx, dir)` calls `a.store.MarkAbort("run-1")`, waits on `<-ctx.Done()`, and returns `Analysis{Findings: []Finding{}}, a.err`. With `err` nil, it stands in for the real analyzer returning a clean result after a cancelled `tidy` (`internal/analyze/run.go:55-59`).
- **In `loop_events_test.go`:** helper `func abortDuringAnalysis(t *testing.T, err error) (*eventsRig, int)`. It builds `r := newEventsRig(t)` and sets `r.loop.BlockerTimeout = time.Minute`, `r.loop.Kinds[1].Row.Rounds = 1` (implement, Check `diff`, no reviewers) and `r.loop.Sessions.Analyzer = abortingAnalyzer{store: r.store, err: err}`. It then rebuilds `r.loop.Runners = DefaultRunners(r.loop.Sessions, r.loop.Kinds)`, because `DefaultRunners` captures `Analyzer` (`runners.go:32`). It returns `r, r.run(RunOptions{Phases: []string{"2"}})`.
- **In `blockers_test.go`**, next to `analyzeInLoop` (`blockers_test.go:1409`): helper `func retryOnceThenBlock() func(Blocker) Resolution`. It keeps an `atomic.Int32` counter and returns `Resolution{Action: "retry", Citation: "always"}` on the first call and `Resolution{Action: "block", Citation: "always"}` after that. The `block` keeps the base code, which raises a second blocker, from retrying forever.
- **In `blockers_test.go`:** helper `func abortDuringRetriedAnalysis(t *testing.T) (*eventsRig, *[]raised)`. It builds `r, seen := newSourceRig(t, retryOnceThenBlock())` (real `RunLoop` blocker handling; the fake Watcher's route settles each question, `blockers_test.go:778-802`) and sets `r.loop.Kinds[1].Row.Rounds = 1`. It sets `r.loop.Sessions.Analyzer` to an `analyzerFunc` (`review_test.go:2145`) with an `atomic.Int32` call counter:
  - the first call returns `Analysis{}, errors.New("golangci-lint: exit status 3")`;
  - every later call does `r.store.MarkAbort("run-1")`, waits on `<-ctx.Done()`, and returns `Analysis{}, errors.New("interrupted: context canceled")`.
  It then rebuilds `r.loop.Runners = DefaultRunners(r.loop.Sessions, r.loop.Kinds)` and calls `r.run(RunOptions{Phases: []string{"2"}})`, and returns `r, seen`.
- **In `blockers_test.go`:** helper `func blockersOf(rs []raised) []Blocker`, which returns each `raised.b`. Add `sync/atomic` to that file's imports.

The tests:

1. **`TestAReviewCanceledByAnAbortRaisesNoReviewerBlocker`** (`review_test.go:902`, existing; modify its expectation only). The expected reason at `review_test.go:913` changes from `"reviewer codex: interrupted: context canceled"` to `"interrupted: context canceled"`. It pins that, with a raiser, an abort while the reviewer works fails the step with the unprefixed reason and raises no blocker. #7 bullet 1.
2. **`TestAReviewCanceledByAnAbortStillRecordsTheReviewersFindAsFailed`** (`review_test.go`, new). Given `newReviewRig(t, Reviewer{Provider: "codex"})`, a cancellable ctx, `r.behave = func(map[string]any) { cancel() }` and a `raisingObserver` resolving `block`. When `ReviewHalf{Sessions: r.sm, Store: r.store}.Run(ctx, …)` runs. Then `r.findsBy("codex")` equals `[]map[string]string{{"step": "implement", "round": "1", "reviewer": "codex", "state": "failed", "findings": "0", "command": "/codex-review"}}`. #7 bullet 3.
3. **`TestAnAbortWhileARetriedReviewerWorksFailsTheStepAsInterrupted`** (`review_test.go`, new). Given `newReviewRig(t, Reviewer{Provider: "codex"})`, a cancellable ctx, `failCodexThenCancel(t, r, cancel)` and a `raisingObserver` resolving `then(Resolution{Action: "retry"}, Resolution{Action: "block"})`. When `Run(ctx, …)` runs. Then `assertState(t, actualOut, StepFailed, "interrupted: context canceled")`. #7 bullet 1, on the `recover` path.
4. **`TestAnAbortWhileARetriedReviewerWorksRaisesNoSecondBlocker`** (`review_test.go`, new). Same given and when as test 3. Then `obs.blockers` equals exactly `[]Blocker{{Source: "reviewer", Phase: "3", Step: "implement-rv-codex", Reason: "reviewer codex: /review is not available in this session", Actions: []string{"retry", "switch", "block", "stop"}}}`. The real failure's blocker keeps its prefix, and the abort raises none. #7 bullets 1 and 4.
5. **`TestACanceledAnalysisRaisesNoStaticBlocker`** (`review_test.go`, new). Given `newReviewRig(t)` with no reviewers. When `_, obs := r.runCanceledAnalysis()`. Then `len(obs.blockers)` is 0. Under `raiser.raise`, `hold` is the only producer of the question record and the `blocked-on` event. #23 bullet 1.
6. **`TestACanceledAnalysisFailsTheRoundAsInterruptedNotBlocked`** (`review_test.go`, new). Given `newReviewRig(t)`. When `out, _ := r.runCanceledAnalysis()`. Then `assertState(t, out, StepFailed, "interrupted: context canceled")`, `assertEqual(t, "blocked", out.blocked, false)` and `assertEqual(t, "halted", out.Halted, false)`. #23 bullet 2.
7. **`TestAnExecReviewCanceledByAnAbortFailsTheStepAsInterrupted`** (`nativereview_test.go`, new). Given `ctx, cancel := context.WithCancel(context.Background())`, `r := execReviewRig(t, cancelingReviewRunner{cancel: cancel}, Reviewer{Provider: "codex"})` and a `raisingObserver` resolving `block`. When `ReviewHalf{Sessions: r.sm, Store: r.store}.Run(ctx, r.worker.Ref, r.worker, obs)` runs. Then `assertState(t, out, StepFailed, "interrupted: context canceled")`. #7 bullet 1, for the native-exec route through `open`, which the triage flagged.
8. **`TestAnAbortDuringAReviewRecordsTheStepFailedAsInterruptedWithoutTheReviewerPrefix`** (`loop_test.go`, new). Given `newLoopRig(t)` with `Kinds[0].Row.Rounds = 1`, `Kinds[0].Row.Reviewers = []Reviewer{{Provider: "codex"}}` and `Sessions.Host = abortingReviewerHost{r.host}`, as at `loop_test.go:2027-2033`. When `r.run(RunOptions{Phases: []string{"1"}})`. Then the last record of `recordsForStep(r.store, StepKey{Run: "run-1", Phase: "1", Kind: "plan", Attempt: 1})` has `State` `StepFailed` and `Reason` `"interrupted: context canceled"`. #7 bullet 1, end to end.
9. **`TestAnAbortDuringAReviewReportsWhereTheRunWasAborted`** (`loop_test.go`, new). Same given and when as test 8. Then `strings.Contains(r.report(t), "- aborted during phase 1 plan")` is true. #7 bullet 2 (report.md).
10. **`TestAnAbortDuringTheStaticAnalysisRecordsNoBlockerQuestionOrBlockedOnEvent`** (`loop_events_test.go`, new). Given and when: `r, _ := abortDuringAnalysis(t, errors.New("interrupted: context canceled"))`. Then `st, _ := r.store.Load("run-1")` has `len(st.Questions) == 0`, and `len(r.events("blocked-on")) == 0`. With a real `RunLoop` raiser, these are the two side effects `hold` writes. #23 bullet 1.
11. **`TestAnAbortDuringTheStaticAnalysisHaltsTheRunAsAborted`** (`loop_events_test.go`, new). Given and when: `r, code := abortDuringAnalysis(t, errors.New("interrupted: context canceled"))`. Then `code` is 1, and the last of `r.runRecords()` has `Run == RunHalted` and `Reason == ReasonAborted`. #23 bullet 2 (run record).
12. **`TestAnAbortAsTheStaticAnalysisSucceedsRecordsTheStepFailedAsInterrupted`** (`loop_events_test.go`, new). Given and when: `r, _ := abortDuringAnalysis(t, nil)`, an analysis that returns a clean, nil-error result once the step ctx is cancelled. Then the last record of `recordsForStep(r.store, StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1})` has `State` `StepFailed` and `Reason` `"interrupted: context canceled"`. The step is not recorded `ok`, so `Finish` does not commit it. #23 bullet 2, for the nil-error cancel path (codex-r1-1).
13. **`TestAnAbortDuringARetriedStaticAnalysisFailsAsInterruptedAfterOnlyTheFirstBlocker`** (`blockers_test.go`, new). Given and when: `r, seen := abortDuringRetriedAnalysis(t)`. The first analysis fails for real, the watchdog resolves its blocker `retry`, and the abort lands during the retried analysis (`review.go:290-291`). Then:
    - `assertEqual(t, "routed blockers", blockersOf(*seen), []Blocker{{Source: "reviewer", Phase: "2", Step: "implement-rv-static", Reason: "reviewer static: golangci-lint: exit status 3", Actions: []string{"retry", "block", "stop"}}})`;
    - `assertEqual(t, "stored questions", len(r.questionsOf(t)), 1)`, which reads the store and fails on a load error (`blockers_test.go:1102-1108`);
    - `assertEqual(t, "blocked-on events", len(r.events("blocked-on")), 1)`;
    - the last record of `recordsForStep(r.store, StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1})` has `State` `StepFailed` and `Reason` `"interrupted: context canceled"`.
    #23 bullets 1 and 2 on the retried-analysis path, and bullet 3 (the real failure still raises the static blocker with retry/block/stop) through real blocker handling (codex-r2-10).

Existing tests that already pin the unchanged real-failure paths. They are kept as they are and run in the gate:
14. **`TestAnAbortDuringAReviewClosesTheReviewerPaneAndLeavesTheStepSession`** (`loop_test.go:2027`): an abort during a review leaves the last run record halted/aborted. #7 bullet 2.
15. **`TestInvalidFindingsFileFailsTheStepNamingIt`** (`review_test.go:719`): with no cancel and no raiser, a real reviewer failure keeps `reviewer codex: …`, and its `review-find` is failed. #7 bullet 4.
16. **`TestAFailedReviewerRaisesAReviewerBlockerWithoutASkip`** (`review_test.go:1463`): with a raiser and no cancel, a real failure's blocker keeps `reviewer codex: …`. #7 bullet 4.
17. **`TestAFailedExecReviewRaisesARetryableBlockerNamingTheCommandAndError`** (`nativereview_test.go:91`): a real native exec failure keeps its prefix and raises a blocker. #7 bullet 4.
18. **`TestAnAnalyzeErrorRaisesAStaticReviewerBlocker`** (`review_test.go:2343`): with no cancel, an analyzer error raises the `reviewer static:` blocker with `retry, block, stop`. #23 bullet 3.
19. **`TestAnAnalyzeErrorWithoutARaiserFailsTheStep`** (`review_test.go:2358`): with no cancel and no raiser, an analyzer error keeps `reviewer static: …`, and the static `review-find` is failed. #23 bullet 3.

## Left out

- **A ctx check in `open()`'s no-raiser `failed` branch (`review.go:387-388`).** A real run always has a raiser (`wire.go:605`, positive `blockerTimeout` at `internal/config/reader.go:297-299`). With a raiser, the native-exec cancel reaches `join` (test 7).
- **A generic ctx guard in `hold` (`blockers.go:119`).** It would change land, gate and step blockers and their withdrawn logic, and no item needs it.
- **Rewriting the `reviewer <id>:` prefix sites (`review.go:486, 508, 526, 555, 602, 655, 675, 678`).** Real failures and `review-find` need the prefix. The reason is overridden once, where the outcome is chosen.
- **`context.Cause` in the reason.** #7 asks for `interrupted: context canceled`. The step ctx is a plain `WithCancel` (`loop.go:1108`), and `waitAll` uses `ctx.Err()` (`session.go:451`). The run-level reason keeps its cause via `interruptReason` (`loop.go:1700-1706`).
- **Docs.** The spec and tech-design describe the abort as a halted/aborted run and say nothing about a review step's reason. Nothing there changes.

## Assumptions

- **The triage's "same ctx check on the open() path"** is met through `join` (test 7), not by a check inside `open`. The reasons are under `## Why this approach`.
- **The phase check's #7 warning** (override once at `review.go:575`, leave `judge`/`found` alone) is followed. The `recover` guard is added on top of it for the retried-reviewer source named under `## Why this approach`.
- **The phase check's #23 warning** (check in `settleStatic` before the raise, sharing #7's reason shape, no guard in `hold`) is followed. The check sits at the top of the loop, so it also catches a nil-error result after the cancel.
- **A cancelled static analysis still records its `review-find` with `state: failed`** before the interrupted return. That matches #7's reviewer behaviour, and #23 does not say otherwise.
- **`TestAReviewCanceledByAnAbortRaisesNoReviewerBlocker`'s old expected reason** pinned the defect #7 removes, so its expectation is updated rather than kept.

## Gate

`go test ./internal/core/ -count=1 -run '^(TestAReviewCanceledByAnAbortRaisesNoReviewerBlocker|TestAReviewCanceledByAnAbortStillRecordsTheReviewersFindAsFailed|TestAnAbortWhileARetriedReviewerWorksFailsTheStepAsInterrupted|TestAnAbortWhileARetriedReviewerWorksRaisesNoSecondBlocker|TestACanceledAnalysisRaisesNoStaticBlocker|TestACanceledAnalysisFailsTheRoundAsInterruptedNotBlocked|TestAnExecReviewCanceledByAnAbortFailsTheStepAsInterrupted|TestAnAbortDuringAReviewRecordsTheStepFailedAsInterruptedWithoutTheReviewerPrefix|TestAnAbortDuringAReviewReportsWhereTheRunWasAborted|TestAnAbortDuringTheStaticAnalysisRecordsNoBlockerQuestionOrBlockedOnEvent|TestAnAbortDuringTheStaticAnalysisHaltsTheRunAsAborted|TestAnAbortAsTheStaticAnalysisSucceedsRecordsTheStepFailedAsInterrupted|TestAnAbortDuringARetriedStaticAnalysisFailsAsInterruptedAfterOnlyTheFirstBlocker|TestAnAbortDuringAReviewClosesTheReviewerPaneAndLeavesTheStepSession|TestInvalidFindingsFileFailsTheStepNamingIt|TestAFailedReviewerRaisesAReviewerBlockerWithoutASkip|TestAFailedExecReviewRaisesARetryableBlockerNamingTheCommandAndError|TestAnAnalyzeErrorRaisesAStaticReviewerBlocker|TestAnAnalyzeErrorWithoutARaiserFailsTheStep)$'`
