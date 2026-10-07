status: planned

## Summary

The phase adds the `static` reviewer (ADR-89): in every review round of a step whose check is `diff` (implement and gatefix), the driver runs `Analyzer.Analyze(ctx, worker.Dir)` in its own goroutine while the reviewer panes work. The round waits for it together with the panes. The analysis's findings are written as `<kind>-findings-static-r<N>.json`, recorded as a `review-find` event and handled from there like any reviewer's file: they go to the fix half and the verdict check, into the next round's prior findings, and into resume's rebuilt paths. An `Analyze` error raises a `reviewer` blocker on `<kind>-rv-static` (retry, skip, block, stop), or fails the step when nothing can raise one. `fix.md` tells the step session how to judge analyzer hits. `app` wires `analyze.New(cfg.Analyze.Timeout)` when `analyze.enabled` is set.

Choices taken:

- **How the analyzer reaches `ReviewHalf`.** Chosen: a new `SessionManager.Analyzer` field, which `DefaultRunners` copies into `ReviewHalf.Analyzer`. The other option was a third `DefaultRunners` parameter. The field won because it changes none of `DefaultRunners`' 8 call sites (`internal/core/loop_test.go:182`, `:678`, `:1598`, `verdict_test.go:347`, `review_test.go:839`, `internal/app/wire.go:530`, `:565`).
- **Who makes `static`'s ids fit core's id scheme.** Chosen: core prefixes each id with `static-r<N>-` when it writes the file. The other option was to have the analyzer number findings per round. Core won because only core knows the round, and the `analyze` package stays round-agnostic (`internal/analyze/sarif.go:262` keeps `s<n>`).
- **Where the "no other reviewers" round is let through.** Chosen: `singleRunner` drops its `len(row.Reviewers) > 0` guard, and `ReviewHalf.Run` itself returns early when it has neither reviewers nor `static`. The other option was to teach `singleRunner` about the analyzer. Putting the decision in `Run` keeps it in one place.
- **When the analysis starts and is joined.** Chosen: the goroutine starts right after the round's `review-round` event and before `h.open`, and it is joined right after `WaitAll`, before `h.join`. The other option was to start it after the panes open. Starting first gives the most overlap, and joining before `h.join` means no early return from `join` can leak the goroutine. An early return from `h.open` cancels the analysis and waits for it.
- **How a failed analysis is retried.** Chosen: the retry calls the same goroutine helper and waits for it at once, in a loop inside a new `settleStatic`. The other option was to reuse the pane reviewers' `recover`/`reopen`. Those functions are built around a `reviewerRun` with a pane, a session and `keys`/`switch` actions, none of which `static` has.
- **How `fix.md` speaks of `static`.** Chosen: a paragraph rendered only when one of the `FindingsFiles` belongs to `static`. The other option was an unconditional paragraph. That would talk about a reviewer that does not exist in plan-step fixes.
- **A configured reviewer named `static`.** Chosen: the config reader rejects the name (exit 2). The other option was to fail the step at run time. A user's config is input crossing a trust boundary and is validated at load, like the existing duplicate-name check (`internal/config/reader.go:615`).

## Why this approach

The phase proposes that `ReviewHalf` take an `Analyzer` and run it in a goroutine beside the panes. The plan follows that, with the field named `ReviewHalf.Analyzer` as in tech-design §Milestone 18. It differs from the proposal in five places, each forced by the code:

- **`analyze.New(cfg)` becomes `analyze.New(cfg.Analyze.Timeout)`.** The constructor takes a duration, `func New(timeout time.Duration) *Analyzer` (`internal/analyze/run.go:30`).
- **The ids are prefixed in core.** The watchdog's warning applies: `analyze` numbers findings `s1…sN` and restarts at `s1` in every round (`internal/analyze/sarif.go:262`). Core requires a findings file's ids to start with `<reviewer>-r<round>-` (`internal/core/evidence.go:367-372`), and earlier rounds' files sit beside the current one in prior findings and on resume. So `review.go` writes `static-r<N>-<id>`, for example `static-r1-s1`, which passes `checkFindingsFile` and keeps ids unique across rounds.
- **`internal/core/runners.go` changes, although `Files:` does not list it.** `singleRunner.Run` only enters the review half when `len(row.Reviewers) > 0` (`internal/core/runners.go:23`), so "a step with no other reviewers still runs the round" cannot hold without changing that line. `DefaultRunners` (`internal/core/runners.go:32`) is also where `ReviewHalf` is built, so it must pass the analyzer in.
- **`internal/core/gatecommand_test.go` changes, although `Files:` does not list it.** The phase's `Done when: go test -race ./...` is red before any change. `TestGateCommandFromTheRealTodoLinesPassesOnACorrectTree` reads `docs/task-loop-driver/todo.md` at fixed line numbers 535, 549, 565, 578 and 607 (`internal/core/gatecommand_test.go:106`), and the `Done when:` lines of phases 32, 33, 34, 35 and 37 have since moved to 542, 556, 572, 585 and 614. The test now finds each phase's `Done when:` line by its `### Phase <n> ` heading. All five commands pass on the current tree (checked by hand).
- **`internal/config/reader.go` and `internal/config/reader_test.go` change, although `Files:` lists neither.** A reviewer with ID `static` would write the same findings file and blocker step as the analyzer. Reviewer ids are validated in `reader.go:609-618`, which is the one place that sees every row's reviewers before a run starts.

The fake `semgrep` added to `app_test.go`'s `fakeProviders` is needed by the `Done when:` too. With `analyze.enabled` defaulting to `true` (`internal/config/defaults.yaml` / `reader.go:676`), every app test that drives a review round through `Wire` (`resume_test.go`, `unattended_test.go`, `ask_test.go` and others; the sim writes `code.txt` into the worktree, `resume_test.go:126`) would run the real analyzer. With no build modules in the fixture, that means the real `semgrep scan --config p/default`, which fetches rules over the network (`internal/analyze/run.go`, `semgrep`). A deterministic fake on `PATH` keeps the real analyzer code path running in those tests with no network. It goes in a second `PATH` entry because `toolchainPath` keeps only the first one (`internal/app/app_test.go:1008`), so the toolchain tests still see "semgrep not installed".

## Changes

1. **`internal/core/session.go` (modify).** Add the field `Analyzer Analyzer` to `SessionManager` (`session.go:31-45`), after `Dialogs`. *Serves: the analyzer reaches `ReviewHalf`; `app` can wire it.*

2. **`internal/core/review.go` (modify).** Add the `encoding/json` import.
   - `type ReviewHalf struct { Sessions *SessionManager; Store Store; Analyzer Analyzer; obs Observer }`.
   - Add `const staticReviewer = "static"` and `var staticActions = []string{actionRetry, actionSkip, actionBlock, actionStop}`, the latter beside the existing `reviewerActions` (`loop.go:508`).
   - Add `func findingsPath(worker *Session, id string, round int) string`, which returns `filepath.Join(stepDir(worker), fmt.Sprintf("%s-findings-%s-r%d.json", worker.Ref.Key.Kind, id, round))`. It replaces the two existing copies of that format, at `review.go:57` (resume rebuild) and `review.go:697` (`vars["FindingsPath"]`), and is used for `static`'s file. That makes three call sites.
   - Add `func (h ReviewHalf) static(worker *Session) bool { return h.Analyzer != nil && worker.Ref.Kind.Check == "diff" }`. *Serves: nil means no `static`; only `diff` steps run it; plan steps never do.*
   - Add the goroutine helper.
     - `type staticRun struct { done chan struct{}; cancel context.CancelFunc; analysis Analysis; err error }`.
     - `func (h ReviewHalf) analyze(ctx context.Context, worker *Session) *staticRun`. It derives `ctx, cancel := context.WithCancel(ctx)` and starts a goroutine. That goroutine runs `defer close(run.done)`, then a deferred `recover()` that sets `run.err = fmt.Errorf("%w in analyze: %v", errPanic, v)` (`errPanic` is at `panics.go:9`), then `run.analysis, run.err = h.Analyzer.Analyze(ctx, worker.Dir)`.
     - `func (r *staticRun) wait() *staticRun`, which waits on `<-r.done`, calls `r.cancel()` and returns `r`.
     - `func (r *staticRun) stop()`, which calls `r.cancel()` and waits on `<-r.done`.
     - *Serves: the analysis runs in its own goroutine beside the panes; nothing leaks on early return; a panic in an analyzer's report parser does not kill the driver.*
   - Change `Run` (`review.go:30-124`).
     - Replace the early return at `review.go:38` with `if len(rows) == 0 && !h.static(worker) { return Outcome{State: StepOK, Session: worker} }`.
     - In the resume rebuild (`review.go:55-60`), after the `rows` loop for round `n`, add `if h.static(worker) { rd.prior = append(rd.prior, findingsPath(worker, staticReviewer, n)) }`.
     - In each round, after `obs.Reviewing(worker, rd.n)`, set `var static *staticRun; if h.static(worker) { static = h.analyze(ctx, worker) }`.
     - If `h.open` fails, run `if static != nil { static.stop() }` before `return out`.
     - After `WaitAll` and the `r.out` assignment, run `if static != nil { static.wait() }`.
     - After `h.join` succeeds, call `staticFile, staticN, out := h.settleStatic(ctx, worker, static, rd)` and return on `StepFailed`.
     - Build `files []FindingsFile` in the existing loop over `runs` (`review.go:93-100`): for each non-skipped `r`, append `FindingsFile{Reviewer: r.s.Reviewer, Path: r.s.Ref.Vars["FindingsPath"].(string)}`. Then append `*staticFile` when it is non-nil.
     - Add `findings += staticN`.
     - Call `h.fix(ctx, worker, files, rd, verdictPath, spent, obs)`.
     - Replace the `rd.prior` loop at `review.go:117-119` with `for _, f := range files { rd.prior = append(rd.prior, f.Path) }`.
     - *Serves: a step with no other reviewers still runs the round; the round joins on the analysis; `static`'s file reaches the fix half and the next round's prior findings; a resume rebuilds `static`'s earlier paths; zero findings keep a round clean.*
   - Change `fix`'s signature to `func (h ReviewHalf) fix(ctx context.Context, worker *Session, files []FindingsFile, rd reviewRound, verdictPath string, spent time.Duration, obs Observer) (bool, Outcome)`. `paths` is built from `files[i].Path`, and `vars["FindingsFiles"] = files`. The rest is unchanged. *Serves: the verdict must answer `static`'s ids, through `fixHalf.findings` and then `verdictCheck` (`evidence.go:432-460`).*
   - Add `func (h ReviewHalf) foundStatic(worker *Session, round int, state StepState, n int, command string) error`. It records `h.event(worker, "review-find", {"round", "reviewer": staticReviewer, "state": string(state), "findings": strconv.Itoa(n), "command": strings.TrimSpace("analyze " + command)})`, following `found` (`review.go:477`). *Serves: the `review-find` event.*
   - Add `func writeStaticFindings(path string, round int, fs []Finding) error`. It sets `out := make([]Finding, 0, len(fs))` and, for each `f`, sets `f.ID = fmt.Sprintf("%s-r%d-%s", staticReviewer, round, f.ID)` and appends it. It then runs `data, err := json.Marshal(Findings{Reviewer: staticReviewer, Findings: out})` and returns `os.WriteFile(path, data, 0o644)` (or the marshal error). Starting from a non-nil `out` makes a zero analysis write `"findings":[]`, which `parseFindings` accepts (`evidence.go:306`). *Serves: the findings format, `reviewer: static`, ids that fit `<reviewer>-r<round>-`.*
   - Add `func (h ReviewHalf) settleStatic(ctx context.Context, worker *Session, run *staticRun, rd reviewRound) (*FindingsFile, int, Outcome)`. It returns `nil, 0, Outcome{}` when `run == nil`. Otherwise it loops:
     - **No error.** It runs `path := findingsPath(worker, staticReviewer, rd.n)`. If `writeStaticFindings` fails, it returns `h.Sessions.fail(worker, "reviewer static: "+err.Error())`. If `foundStatic(..., StepOK, len(run.analysis.Findings), run.analysis.Command)` fails, it returns `sm.fail(worker, "record: "+err.Error())`. Otherwise it returns `&FindingsFile{Reviewer: staticReviewer, Path: path}, len(...), Outcome{}`.
     - **Error.** It sets `reason := "reviewer static: " + run.err.Error()` and records `foundStatic(..., StepFailed, 0, "")`, with a record error failing the step as above. If `h.raiser() == nil`, it returns `Outcome{State: StepFailed, Reason: reason, Session: worker}`, as `join` does at `review.go:445-447`. Otherwise it raises `Blocker{Source: sourceReviewer, Phase: worker.Ref.Key.Phase, Step: reviewerKey(worker.Ref.Key, staticReviewer).Kind, Reason: reason, Actions: staticActions}` and switches on `res.Action`:
       - `actionSkip`: records `h.event(worker, "reviewer-skipped", {"reviewer": staticReviewer, "reason": reason})` and returns `nil, 0, Outcome{}`.
       - `actionRetry`: sets `run = h.analyze(ctx, worker).wait()` and continues the loop.
       - `actionStop`: returns `Outcome{State: StepFailed, Reason: stoppedAt(res.ID, reason), Session: worker, Halted: true}`.
       - Any other action: returns `Outcome{State: StepFailed, Reason: reason, Session: worker, blocked: true}`.
     - This mirrors `recover` (`review.go:495-514`).
     - *Serves: the blocker with retry, skip, block and stop; a missing raiser fails the step; a findings file that cannot be written fails the step.*

3. **`internal/core/runners.go` (modify).** At line 23, the condition becomes `out.State == StepOK && row.Rounds > 0 && r.review != nil`. At line 32, the runner is built with `ReviewHalf{Sessions: sm, Store: sm.Store, Analyzer: sm.Analyzer}.Run`. *Serves: a row with no reviewers still runs the `static` round, including on resume (`runners.go:17`); `SessionManager.Analyzer` reaches every `diff` runner, gatefix's included (`wire.go:565`).*

4. **`internal/prompts/templates/fix.md` (modify).** Insert the following directly after the findings-file list's `{{- end}}` (line 6):

   ```
   {{- range .FindingsFiles}}{{if eq .Reviewer "static"}}

   `static`'s findings come from static analyzers the driver ran on the change, not from a model reading it. A rule hit is not a defect by itself: judge each one against the code like any other finding. A false positive is `not-real`, with the `path:line` that shows it as its evidence.
   {{- end}}{{end}}
   ```

   *Serves: the `fix.md` wording.*

5. **`internal/config/reader.go` (modify).** In the reviewer loop, after the duplicate check at line 615, add `if id == "static" { return row, errAt(l.file, it, "%sreviewers: the name %q is reserved for the analyzers, give the reviewer another name", p, id) }`. *Serves: a configured reviewer cannot collide with `static`'s findings file and blocker step.*

6. **`internal/app/wire.go` (modify).** Before `sm := &core.SessionManager{` (line 518), add `var analyzer core.Analyzer` and `if cfg.Analyze.Enabled { analyzer = analyze.New(cfg.Analyze.Timeout) }`, and import `r-loop/internal/analyze`. Add `Analyzer: analyzer,` to the literal, after `Label: cfg.Label,` (line 528). Because the literal precedes `core.DefaultRunners(sm, kinds)` (line 530) and the gatefix runner (line 565), both runners get it. The nil interface stays nil when analysis is disabled. *Serves: `app` wires `analyze.New` when enabled, nil otherwise.*

7. **`internal/core/gatecommand_test.go` (modify).** See `## Tests`. *Serves: `Done when` green.*

8. **`internal/app/app_test.go` (modify).** In `fakeProviders` (`app_test.go:84-99`), create a second temp dir `tools` holding an executable `semgrep` with this script:

   ```
   #!/bin/sh
   while [ $# -gt 0 ]; do
     if [ "$1" = --output ]; then printf '{"runs":[]}' > "$2"; fi
     shift
   done
   ```

   Then set `PATH` to `bin + sep + tools + sep + os.Getenv("PATH")`. Add the wiring tests listed below. *Serves: `Done when` green with no network; the wiring tests.*

## Tests

Write these first. Every test uses `// given`, `// when`, `// then` blocks. Core review tests use `newReviewRig` (`review_test.go:31`) and add two helpers to `review_test.go`:

- `func (r *reviewRig) runAnalyzed(a Analyzer) Outcome` runs `ReviewHalf{Sessions: r.sm, Store: r.store, Analyzer: a}.Run(context.Background(), r.worker.Ref, r.worker, &recObserver{})`.
- `func (r *reviewRig) runAnalyzedRaising(a Analyzer, resolve func(n int, b Blocker) Resolution) (Outcome, *raisingObserver)` mirrors `runRaising` (`review_test.go:1384`).

The fake analyzer is `&fakeAnalyzer{callLog: callLog{Shared: r.shared}, Results: …, Errs: …}` (`fakes_test.go:539`). The one finding used throughout is `Finding{ID: "s1", Title: "golangci-lint/errcheck: unchecked error", Detail: "error\nunchecked error\na.go:3", Files: []string{"a.go"}}` with `Command: "golangci-lint"`. The analyzer error is `errors.New("golangci-lint: exit status 3")`.

**`internal/core/review_test.go`**

- `TestTheAnalysisIsWrittenAsStaticsFindingsFileWithRoundPrefixedIDs` — with one `claude` reviewer at 0 findings, `Rounds = 1` and an analysis with the one finding, `r.onFix` writes `entry("static-r1-s1", "out-of-scope", "P3", false, "")`. Then the bytes of `<runDir>/phase-3/implement-findings-static-r1.json` equal `{"reviewer":"static","findings":[{"id":"static-r1-s1","title":"golangci-lint/errcheck: unchecked error","detail":"error\nunchecked error\na.go:3","files":["a.go"]}]}`. *(findings format, `reviewer: static`, id scheme)*
- `TestTheAnalysisIsRecordedAsAStaticReviewFind` — with one `claude` reviewer and an analysis of zero findings with `Command: "golangci-lint, semgrep"`, the second `review-find` event's fields equal `{"step":"implement","round":"1","reviewer":"static","state":"ok","findings":"0","command":"analyze golangci-lint, semgrep"}`. *(review-find event)*
- `TestEachRoundAnalyzesTheStepsWorktree` — one `claude` reviewer whose `behave` sets `r.repo.TreeChanges = nil` and writes 0 findings, and `Results: []Analysis{{Findings: <the one finding>}, {}}`. `onFix` sets `TreeChanges = ["a.go"]` and writes `entry("static-r1-s1", "real", "P1", true, "")`, so round 1's fix leads into round 2. Then `r.callsFrom("Analyzer.Analyze")` equals `["Analyzer.Analyze /repo/.r-loop/wt/phase-3", "Analyzer.Analyze /repo/.r-loop/wt/phase-3"]`, the second `review-find` with `reviewer` `static` has `round` `2` and `state` `ok`, and the outcome is `StepOK`. *(Analyze(ctx, worker.Dir) each round)*
- `TestStaticsFindingsFileJoinsTheFixHalf` — with one `claude` reviewer at 0 findings, `Rounds = 1`, `r.repo.TreeChanges = nil`, and an `onFix` that sets `TreeChanges = ["a.go"]` and writes `entry("static-r1-s1", "out-of-scope", "P3", false, "")`, `r.fixes[0]["FindingsFiles"]` equals `[]FindingsFile{{"claude", <dir>/implement-findings-claude-r1.json}, {"static", <dir>/implement-findings-static-r1.json}}` and the outcome is `StepOK`. *(fix half's FindingsFiles)*
- `TestAVerdictThatLeavesAStaticFindingUnansweredFailsTheFix` — with the same setup but `onFix` writing `writeVerdict(t, vars)` with no entries, the outcome is `StepFailed` with `Reason == "evidence missing: no verdict for finding static-r1-s1"`. *(the verdict must answer static's ids)*
- `TestZeroStaticFindingsLeaveACleanRoundClean` — with one `claude` reviewer at 0 findings and `fakeAnalyzer{Results: []Analysis{{}}}`, there is 1 `review-clean`, `len(r.fixes) == 0`, and the static file's bytes equal `{"reviewer":"static","findings":[]}`. *(zero findings keep a clean round clean; nil findings written as a list)*
- `TestTheAnalysisRunsWhileTheReviewerPanesWork` — uses a test type `gatedAnalyzer{started, prompted chan struct{}}`. Its `Analyze` closes `started`, then selects on `prompted` (returning a zero analysis) or a 10 s `time.After` (returning `errors.New("the reviewer was never prompted")`). `r.behave` selects on `started` or a 10 s deadline (`t.Error("the analysis had not started when the reviewer was prompted")`), then closes `prompted` and calls `writeReview(t, vars, "ok", 0)`. Then the outcome is `StepOK` and the static `review-find` has `state` `ok`. *(beside the panes; the round joins on it)*
- `TestAFailedPaneOpenCancelsTheAnalysisBeforeTheStepFails` — uses `r.sm.Host = &splitFailHost{scriptedHost: r.host, failOn: 1}` with one `claude` reviewer, and a test type `blockingAnalyzer{errs chan error}` (buffer 1) whose `Analyze` waits on `<-ctx.Done()`, sends `ctx.Err()` and returns it. After `runAnalyzed`, a non-blocking receive from `errs` (helper `receivedNow(ch chan error) error`, returning `errors.New("nothing received")` on `default`) equals `context.Canceled`, and the outcome is `StepFailed` with reason `reviewer claude: pane_not_found`. *(no goroutine outlives an early return)*
- `TestAPlanStepNeverRunsTheAnalysis` — sets `r.worker.Ref.Kind.Check = "plan-file"` with one `claude` reviewer at 0 findings. Then `r.callsFrom("Analyzer.Analyze")` is empty, no `review-find` has `reviewer` `static`, and `os.Stat` of `implement-findings-static-r1.json` returns `fs.ErrNotExist`. *(plan steps never run it)*
- `TestANilAnalyzerAddsNoStaticReviewer` — `runAnalyzed(nil)` with one `claude` reviewer at 0 findings records exactly one `review-find` (reviewer `claude`), and no static file exists. *(nil = no static reviewer)*
- `TestAStepWithNoOtherReviewersStillRunsTheStaticRound` — `newReviewRig(t)` with no reviewers and a zero analysis gives 1 `review-round`, one `review-find` with `reviewer` `static`, 1 `review-clean`, and `r.count("SessionHost.Split") == 0`. *(no other reviewers)*
- `TestDefaultRunnersRunTheStaticRoundForARowWithoutReviewers` — `newReviewRig(t)` with no reviewers, `r.sm.Analyzer = analyzer` (zero analysis), and the host script writing the implement sentinel as in `TestDefaultRunnersRunTheReviewHalfBeforeTheCommit` (`review_test.go:829`). `DefaultRunners(r.sm, []StepKind{ref.Kind})["diff"].Run(...)` returns `StepOK`, and the store holds one `review-find` with `reviewer` `static`. *(runner guard; SessionManager.Analyzer reaches ReviewHalf)*
- `TestTheNextRoundsPriorFindingsIncludeStaticsFile` — one `claude` reviewer whose `behave` sets `r.repo.TreeChanges = nil` and writes 0 findings, and `Results: []Analysis{{Findings: <the one finding>}, {}}`. `onFix` sets `TreeChanges = ["a.go"]` and writes `entry("static-r1-s1", "real", "P1", true, "")`. Then `r.reviews[1]["PriorFindings"]` equals `"- <dir>/implement-findings-claude-r1.json\n- <dir>/implement-findings-static-r1.json"`. *(next round's prior findings)*
- `TestAResumedRoundListsStaticsEarlierFilesAmongThePriorFindings` — one `claude` reviewer at 0 findings, `r.worker.Ref.ReviewFrom = 2`, `PrevRoundTree = "tree-r1"` and a zero analysis. Then `r.reviews[0]["PriorFindings"]` equals `"- <dir>/implement-findings-claude-r1.json\n- <dir>/implement-findings-static-r1.json"`. *(resume rebuild)*
- `TestAnAnalyzeErrorRaisesAStaticReviewerBlocker` — `runAnalyzedRaising(&fakeAnalyzer{Errs: []error{<error>, nil}}, then(Resolution{Action: "skip"}))` with one `claude` reviewer gives `obs.blockers` equal to `[]Blocker{{Source: "reviewer", Phase: "3", Step: "implement-rv-static", Reason: "reviewer static: golangci-lint: exit status 3", Actions: []string{"retry", "skip", "block", "stop"}}}`. *(blocker shape and actions)*
- `TestAnAnalyzeErrorWithoutARaiserFailsTheStep` — `runAnalyzed` with `Errs: []error{<error>}` returns `StepFailed` with `Reason == "reviewer static: golangci-lint: exit status 3"`, and the static `review-find` fields equal `{"step":"implement","round":"1","reviewer":"static","state":"failed","findings":"0","command":"analyze"}`. *(no raiser fails the step)*
- `TestBlockOnAStaticBlockerFailsTheStepWithItsReason` — resolving `block` gives `out.State == StepFailed`, `out.Reason == "reviewer static: golangci-lint: exit status 3"` and `out.blocked == true`. *(block)*
- `TestStopOnAStaticBlockerHaltsTheStep` — resolving `stop` gives `out.Halted == true` and `out.Reason == "stopped by the watchdog at b1: reviewer static: golangci-lint: exit status 3"`. *(stop)*
- `TestAPanickingAnalyzerFailsTheStepAsAnAnalyzeError` — a test type `panickingAnalyzer` whose `Analyze` panics with `"boom"`. `runAnalyzed` returns `StepFailed` with `Reason == "reviewer static: panic in analyze: boom"`. *(panic in the goroutine)*
- `TestAStaticFindingsFileThatCannotBeWrittenFailsTheStep` — `os.MkdirAll(<dir>/implement-findings-static-r1.json)` first. A zero analysis then gives `StepFailed` with `Reason == "reviewer static: open <dir>/implement-findings-static-r1.json: is a directory"`. *(write error)*

**`internal/core/blockers_test.go`**, with the helper `func analyzeInLoop(r *eventsRig, errs ...error) *fakeAnalyzer`. It sets `r.loop.Kinds[1].Row.Rounds = 1` (implement, no reviewers), `r.loop.Sessions.Analyzer = &fakeAnalyzer{callLog: callLog{Shared: r.shared}, Errs: errs}` and `r.loop.Runners = DefaultRunners(r.loop.Sessions, r.loop.Kinds)`, then returns the analyzer.

- `TestAStaticBlockerResolvedRetryAnalyzesTheRoundAgain` — `newSourceRig(t, resolveAs("retry"))` with `analyzeInLoop(r, <error>, nil)` and `r.run(RunOptions{Phases: []string{"2"}})` gives exit `0`; `*seen` holds one blocker with `Source` `reviewer` and `Step` `implement-rv-static`; `r.calls("Analyzer.Analyze ")` equals `[wt, wt]` with `wt := filepath.Join(r.repo.RootDir, ".r-loop", "wt", "phase-2")`; and the `review-find` states for reviewer `static` are `["failed", "ok"]`. *(retry analyzes again for this round)*
- `TestAStaticBlockerResolvedSkipFinishesTheRoundWithoutStatic` — `resolveAs("skip")` with `analyzeInLoop(r, <error>)` gives exit `0`; one `reviewer-skipped` with fields `{"step":"implement","reviewer":"static","reason":"reviewer static: golangci-lint: exit status 3"}`; `r.calls("Analyzer.Analyze ")` equals `[wt]`; and one `review-clean`. *(skip)*

**`internal/prompts/render_test.go`**

- `TestFixExplainsThatStaticFindingsComeFromAnalyzers` — `render(t, New(t.TempDir()), "fix", with("FindingsFiles", []core.FindingsFile{{Reviewer: "static", Path: "/runs/r1/phase-7/implement-findings-static-r1.json"}}))` contains "`static`'s findings come from static analyzers the driver ran on the change", "A rule hit is not a defect by itself" and "A false positive is `not-real`, with the `path:line` that shows it as its evidence". *(fix.md wording)*
- `TestFixWithoutAStaticFileLeavesTheAnalyzerNoteOut` — `render(..., "fix", fullVars())` (claude and codex files) does not contain "A rule hit is not a defect by itself". *(the paragraph is conditional)*

**`internal/app/app_test.go`**

Both tests drive a real run through `Wire` with the resume fixture: `newResumeFixture(t, config)`, then `f.firstRun(newSim(), "--phases", "1")` (`resume_test.go:204`, `:227`). In that run, the sim's implement step writes `code.txt` into the worktree, the sim answers the `claude` reviewer with an empty findings file, and the real `analyze.Analyzer` finds no build module and runs the fake `semgrep` from `fakeProviders`. A helper `reviewFinds(st core.RunState, reviewer string) []map[string]string` returns the `Fields` of the `review-find` events (via `stepEvents`, `resume_test.go:258`) whose `reviewer` matches.

- `TestAWiredRunReviewsWithTheAnalyzerWhenAnalyzeIsEnabled` — with `reviewConfig` (`resume_test.go:445`), `reviewFinds(f.load(id), "static")` equals `[]map[string]string{{"step":"implement","round":"1","reviewer":"static","state":"ok","findings":"0","command":"analyze semgrep"}}`, and the exit code is `0`. *(app wires the analyzer into ReviewHalf when enabled; this fails if `Analyzer` is set on `sm` after `DefaultRunners`)*
- `TestAWiredRunReviewsWithoutTheAnalyzerWhenAnalyzeIsOff` — with `reviewConfig + "analyze:\n  enabled: false\n"`, `reviewFinds(f.load(id), "static")` is empty, `reviewFinds(f.load(id), "claude")` has one entry with `state` `ok` (the round ran), and the exit code is `0`. *(nil otherwise)*

**`internal/config/reader_test.go`**

- `TestAReviewerNamedStaticIsRejected` — `d.writeProject(t, "steps:\n  implement:\n    reviewers:\n      - name: static\n        provider: claude\n        model: opus\n        effort: high\n")` gives `d.loadErr(t, `config.yaml:4: steps.implement.reviewers: the name "static" is reserved for the analyzers, give the reviewer another name`)`. *(name collision)*

**`internal/core/gatecommand_test.go`**

- `TestGateCommandFromTheRealTodoLinesPassesOnACorrectTree` (modified) — the line-number table is replaced by subtests over phases `32`, `33`, `34`, `35`, `37`, each named `phase-<n>`. A helper `doneWhenOf(t, lines []string, phase string) string` returns the first `**Done when:** ` line after the line starting with `### Phase <n> `, and calls `t.Fatalf` when it finds none. The rest of the test (go shim, env, `sh -c`) is unchanged. *(Done when green)*

## Left out

- A `DefaultRunners` analyzer parameter — the `SessionManager` field reaches every runner with no call-site churn.
- Removing a stale `static` findings file at the start of a round — pane reviewers remove theirs (`review.go:400`) because `judge` reads that file back to decide the round. `static`'s file is written from the analysis in memory and never read back to judge a round.
- Making a nil `Finding.Files` into `[]` — the only `Analyzer`, `analyze.Analyzer`, always sets `Files` non-nil (`internal/analyze/sarif.go:253`, `:292`).
- `keys` and `switch` actions and a screen excerpt on `static`'s blocker — `static` has no pane, provider or model (ADR-89).
- Counting the analysis time into the fix half's `spent` — the fix half's backstop covers sessions' time, and `analyze.timeout` already bounds the analysis.
- Face changes — the TUI and plain faces already render any reviewer id from `review-find` (`internal/face/tui/model.go:417`, `internal/face/plain/plain.go:82`), and the report lists it (`internal/core/report.go:319`).
- A per-row switch for `static` — ADR-89 and tech-design allow only `analyze.enabled` and `analyze.timeout`.

## Assumptions

- A row with `rounds: 0` has no review rounds, so it never runs `static`. ADR-89 says "in every review round", and there are none.
- `static`'s `review-find` is recorded after the panes' `review-find` events of the round. When there is no raiser and a pane reviewer has failed, the step fails with the pane's reason before `static`'s event is recorded, as the first failure does in `join` (`review.go:445-447`).
- A failed analysis, or one whose `Command` is empty (no changed files), is recorded with `command` `analyze`.
- `static` runs for gatefix too: its check is `diff` (`wire.go:533`), and ADR-89 names "implement and gatefix".
- The name `static` is reserved on every row, plan rows included, so that changing a row's check can never create a collision.
- The analyzers write only into build output that git ignores or into temp directories (`internal/analyze/run.go`). If a project leaves `target/` or `build/` untracked and not ignored, `checkTree` reports it as a tree change. That belongs to the `analyze` package and is out of this phase.
- A retried analysis ignores the resolution's `Addendum`, since `static` has no prompt to carry it.
- Watchdog warning 1 (ids and `analyze.New(cfg)`) is resolved under `## Why this approach` and in Changes 2 and 6. Watchdog warning 2 (the red `gatecommand_test`) is resolved by Change 7.
