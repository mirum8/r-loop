status: planned

## Summary

This phase makes the driver enforce what a step may write, and makes a half-written sentinel stop failing a step.

- **#18: the todo or backlog file.** Every diff-based evidence check (`plan-file` and `diff`, so plan, implement and gatefix) now fails when the step's tree diff touches the run's todo or backlog file, and the reason names that file. Land adds a second guard for phase branches that no check ever judged. It merges with the todo held to main's copy: `MergeNoFF` checks main's version of the todo back out after the merge, and resolves a conflict confined to the todo the same way. This covers a branch that edited, ticked or deleted the todo, including one whose edit would have conflicted with a todo change on main. The phase is then ticked exactly once, and a backlog item the branch inserted can't shift which item gets ticked.
- **#19: the milestone report.** A milestone report step whose tree diff holds any path besides its report is skipped, and all of its changes are discarded through the existing `report-skipped` → `restore()` path. The report commit is staged by path, and `CommitTouches` then has to show exactly the report. `After` records main's HEAD before the report step. Every failure, including a commit the agent made itself, a refused report commit and a panic, resets the primary tree to that SHA.
- **#24: the sentinel.** A malformed sentinel gets a bounded grace period of 30 s, counted from when the driver first sees it malformed. A later valid read ends the step on its content. If it is still malformed after the grace period, the step fails with `sentinel unreadable: <parse error>`. Both prompt partials, `sentinel` and `outcome`, tell the agent to write the file atomically. This also resolves the watchdog's warning.

Choices:

1. **Where the todo guard lives:** in `stepChanges` (shared by `plan-file` and `diff`) vs. a new check name. Chose `stepChanges`: every diff-based step gets the guard with no config key, and `config/reader.go:115` keeps its fixed check list.
2. **#18 criterion 2:** only reject at evidence time vs. also hold the todo to main's copy at land. Chose both. The evidence rejection is criterion 1. The land restore covers branches committed without the new check: a run resumed on a newer binary skips a step that is already `ok` (`internal/core/loop.go:307`), so its branch is never judged again.
3. **#18 criterion 3:** make backlog `Tick` match items by title vs. land main's todo. Chose landing main's todo. Item IDs are positions everywhere (run list, resume, status), so the fix belongs where the insertion comes from: the step (now rejected) and the branch (now restored). Title matching in `Tick` alone would fix none of the other readers.
4. **Milestone report with extra paths:** fail and discard everything vs. commit the report and discard the rest. Chose fail and discard. It reuses `After`'s existing `report-skipped` + `restore()` path (`internal/core/milestone.go:38-43`), and a report written next to stray edits is not trusted.
5. **Where the todo is held to main's copy:** after a successful merge in land (read, compare, write back) vs. inside `MergeNoFF` through a `keep ...string` parameter. Chose `MergeNoFF`. `MergeNoFF` aborts every conflict itself (`internal/gitrepo/repo.go:530-541`), so a self-tick that conflicts with a todo change on main can only be resolved there. `git checkout HEAD -- <todo>` also restores a deleted todo and main's file mode. The parameter is variadic, so every other caller compiles unchanged.
6. **Undoing a rejected report:** `ResetHard("HEAD~1")` after a refused commit vs. resetting to the SHA recorded before the step. Chose the recorded SHA. It also undoes a commit the agent made itself (the judge fails it at `internal/core/session.go:481-486`, and today's `restore()` resets only to the moved HEAD), and a failed reset can't leave the refused commit behind.
7. **Report commit exactness:** commit by path plus a `CommitTouches` check vs. changing `gitrepo.Repo.Commit` to `git commit --only`. Chose by path plus the check. Land calls `Commit(msg, todo)` during a merge, where git refuses partial commits, so `Commit` itself can't change. The check also catches a path the agent staged and then reverted in the worktree, which `Snapshot` cannot see.
8. **Grace clock:** measured from the first malformed sighting vs. adding up poll intervals. Chose the first sighting: the first sighting always gets at least one more tick, whatever the poll interval.
9. **Behaviour during the grace period:** fall through to the normal state, stall and backstop logic vs. return early. Chose to fall through, so the agent's state is still polled. When the agent is gone, the sentinel is read again before deciding. Content that turned valid between the two reads ends the step, and content still malformed fails at once with the parse error, because nothing will rewrite it.
10. **Grace length:** a constant vs. a config key. Chose the constant `sentinelGrace = 30 * time.Second`, because no item asks for a key.

## Changes

Build in this order.

1. **Modify `internal/core/land.go`**
   - Add `func repoRel(root, p string) string`. Its body is today's `(*LandGate).todoRel` (`internal/core/land.go:430-446`) with the root passed in, plus a first line `if p == "" { return "" }`.
   - `todoRel` becomes `return repoRel(g.Repo.Root(), g.TodoPath)`. Serves: #18 criterion 1, because session evidence resolves the todo path the same way land does, symlinks included.
   - The merge becomes `g.Repo.MergeNoFF(ctx, fmt.Sprintf("r-loop/phase-%s", n), g.todoRel())` (`internal/core/land.go:241`). It always keeps the todo; there is no pre-merge check and no event.
   - The rest of `attempt` is unchanged. After the merge the todo on disk and in the index is main's, so `before == originalTodo`, and every restore and abort path works as today.
   - Serves: #18 criteria 2 and 3.

1a. **Modify `internal/core/ports.go` and `internal/gitrepo/repo.go`**
   - The `Repo` port (`internal/core/ports.go:72`) becomes `MergeNoFF(ctx context.Context, branch string, keep ...string) error`.
   - In `(*Repo).MergeNoFF` (`internal/gitrepo/repo.go:492`):
     - On `mergeErr == nil` with `len(keep) > 0`, run `git checkout HEAD -- <keep...>`. If that fails, return `errors.Join(err, r.AbortMerge())`.
     - In the conflict branch (`internal/gitrepo/repo.go:530-541`), when `len(conflicts) > 0` and every conflict is in `keep`, run the same checkout and return nil when it succeeds (the merge stays staged, conflict resolved to main's copy). A failed checkout joins with `r.AbortMerge()` as above.
     - Any other conflict aborts exactly as today.
   - `fakeRepo.MergeNoFF` (`internal/core/fakes_test.go:252`) takes the new variadic parameter and records it: `f.record("Repo.MergeNoFF %s", strings.TrimSpace(branch+" "+strings.Join(keep, " ")))`.
   - Every existing call without `keep` compiles and behaves as before.
   - Serves: #18 criteria 2 and 3 (a conflicting self-tick and a deleted todo).

2. **Modify `internal/core/evidence.go`**
   - `EvidenceContext` (`internal/core/evidence.go:17-26`): the line `PlanPath string` becomes `PlanPath, TodoPath string`. `TodoPath` is repo-relative; `""` means no guard.
   - `stepChanges` (`internal/core/evidence.go:153-163`): after computing `changed`, loop over it. When `ctx.TodoPath != "" && p == ctx.TodoPath`, return `nil, "step changed " + p + ", the run's plan file; only the driver edits it"`.
   - `diffCheck` and `planChangedOnlyItself` both call `stepChanges`, so both fail with that reason, and they check it before their own path checks.
   - A plan step whose diff is only its `PlanPath` still passes.
   - Serves: #18 criteria 1 and 4.
   - `Judge` (`internal/core/evidence.go:531-541`): split the first case in two.
     - `case sErr != nil: return StepFailed, "sentinel unreadable: " + sErr.Error()`
     - `case s.Outcome != "ok" && s.Outcome != "failed": return StepFailed, "sentinel unreadable"`
     - The other cases stay as they are.
   - Serves: #24 criterion 2.

3. **Modify `internal/core/session.go`**
   - Add `sentinelGrace = 30 * time.Second` to the const block (`internal/core/session.go:19-22`).
   - `watch` (`internal/core/session.go:287-292`) gains `malformedAt time.Time`.
   - `tick` (`internal/core/session.go:356-371`): replace `if !errors.Is(err, ErrNoSentinel) { return m.judge(s, sentinel, err), true }` with:
     - `malformed := errors.Is(err, ErrSentinelMalformed)`
     - `if err == nil { return m.judge(s, sentinel, nil), true }`
     - `if malformed { if w.malformedAt.IsZero() { w.malformedAt = now }; if now.Sub(w.malformedAt) >= sentinelGrace { return m.judge(s, sentinel, err), true } }`
   - In the `state == AgentGone` branch, first read the sentinel again: `if sentinel, err := ReadSentinel(s.Sentinel); !errors.Is(err, ErrNoSentinel) { return m.judge(s, sentinel, err), true }`, then the existing `"agent gone"` failure. A sentinel completed between the first read and the state call is judged by its valid content, and one still malformed fails with its parse error.
   - Nothing else in `tick` changes. Inside the grace period, a malformed sentinel is treated like an absent one.
   - Serves: #24 criteria 1 and 2.
   - `evidence` (`internal/core/session.go:536`): set `TodoPath: repoRel(m.Repo.Root(), str("TodoPath"))`. Every step's `Vars["TodoPath"]` is the absolute todo path, set in `StepVars` (`internal/core/loop.go:1659`), which the loop, gate fix, gate probe and milestone all use.
   - Serves: #18 criterion 1.

4. **Modify `internal/core/milestone.go`**
   - In `report` (`internal/core/milestone.go:64-123`), bind `report := fmt.Sprintf("docs/%s/reports/milestone-%d-%s.md", b.Topic, m.Number, kebab(m.Name))`, and set `ref.Vars["ReportPath"] = report`.
   - After the `out.State != StepOK` return (`internal/core/milestone.go:113-115`) and before `recordFailed`, add:
     - `now, err := b.Repo.Snapshot("")`; on error return `"snapshot: " + err.Error()`.
     - `changed, err := b.Repo.TreeDiff(s.StartTree, now)`; on error return `"tree diff: " + err.Error()`.
     - Collect every `p != report` into `extra`. When `extra` is non-empty, return `"milestone report changed " + strings.Join(extra, ", ") + " besides " + report`.
   - The commit becomes `sha, err := b.Repo.Commit(ctx, fmt.Sprintf("docs(report): milestone %d", m.Number), report)`.
   - Then `touched, err := b.Repo.CommitTouches(sha)`. When `err != nil || len(touched) != 1 || touched[0] != report`:
     - Set `reason := fmt.Sprintf("report commit %s touched %s, not only %s", sha, strings.Join(touched, ", "), report)`.
     - If `err != nil`, append `"; touches: " + err.Error()`.
     - Return the reason. `report` itself resets nothing.
   - `After` (`internal/core/milestone.go:25-44`), right after the `cleanTree` check, records `branch, err := b.Repo.HeadBranch()` and `base, err := b.Repo.HeadSHA("")`. On either error it records `report-skipped` with reason `"head: " + err.Error()` and returns without spawning.
   - `restore` becomes `restore(branch, base string) error`. It first calls `b.Repo.HeadBranch()`, and if the primary tree is no longer on `branch` (or the call fails), it returns `fmt.Errorf("the primary tree left %s for %s; not resetting", branch, now)` without touching anything. The other branch's work is kept, and the next landing's branch guard (`internal/core/land.go:106-113`) halts naming it. Otherwise it calls `b.Repo.ResetHard(base)` instead of `ResetHard("HEAD")`, then removes leftovers as today. `After` calls `b.restore(branch, base)` whenever `report` returns a reason, appends a restore error as `"; restore: …"` as today, then records `report-skipped`.
   - That undoes, in one reset, a report commit whose touches were refused, a commit the agent made itself (judged `HEAD moved …` or `step committed before review`, `internal/core/session.go:481-486`), and a panic after a commit.
   - Import `strings`.
   - Serves: #19 criteria 1, 2 and 3.

5. **Modify `internal/prompts/render.go`**
   - In both `{{define "outcome"}}` and `{{define "sentinel"}}` (`internal/prompts/render.go:21-44`), insert this new paragraph right after the paragraph that starts "As your last action":
     `Write the sentinel atomically: put the JSON in a temporary file in the same directory, then rename that file onto ` + "`" + `{{.Sentinel}}` + "`" + `. The driver may read the sentinel at any moment, and must never see it half-written.`
   - That covers plan and implement (`sentinel`), and gatefix, gate and milestone (`outcome`). It also covers review, review-ui and fix, which pick one of the two partials by `ReviewedKind` (`internal/prompts/templates/review.md:67`, `internal/prompts/templates/fix.md:28`, `internal/prompts/templates/review-ui.md:46`).
   - Serves: #24 criterion 3 and the watchdog's warning.

6. **Modify `docs/task-loop-driver/tech-design.md`** so the design contract matches the code:
   - **Sentinel** bullet (`docs/task-loop-driver/tech-design.md:162-163`): a malformed sentinel is re-read for 30 s from its first sighting (at once when the agent is gone), then fails with `sentinel unreadable: <parse error>`; agents write it atomically (temp file in the same directory, then rename).
   - **Shipped checks** (`docs/task-loop-driver/tech-design.md:225-231`): `plan-file` and `diff` fail with `step changed <todo>, the run's plan file; only the driver edits it` when the diff touches the run's todo.
   - **Land** (`docs/task-loop-driver/tech-design.md:386-387`): `MergeNoFF(r-loop/phase-<N>, <todo>)` keeps main's copy of the todo, resolving a conflict confined to it.
   - **Milestone boundary** (`docs/task-loop-driver/tech-design.md:397-399`): a diff beyond the report is `report-skipped` and discarded; the commit is staged by the report's path and must touch only it; any failure resets the primary tree to the HEAD recorded before the step, unless the step left the starting branch, which is then left alone.
   - Serves: keeps the #18, #19 and #24 contracts in the design doc.

## Tests

Write these first. Package `core` tests use `fakeRepo`/`rig`. Package `core_test` tests in `land_test.go` use real git through `newLandEnv` (`internal/core/land_test.go:361`).

- **`TestDiffCheckFailsNamingTheRunsPlanFileWhenTheStepChangedIt`** (`internal/core/evidence_test.go`, package `core`)
  - Setup: `EvidenceContext{Repo: &fakeRepo{Tree: "tree-now", TreeChanges: []string{"a.go", "docs/todo.md"}}, Worktree: "/wt", StartTree: "tree-start", TodoPath: "docs/todo.md"}`.
  - Asserts: `runCheck(t, "diff", ctx)` returns `false, "step changed docs/todo.md, the run's plan file; only the driver edits it"`.
  - Covers: #18 criterion 1.
- **`TestPlanFileCheckFailsNamingTheRunsPlanFileWhenThePlanStepChangedIt`** (`internal/core/evidence_test.go`)
  - Setup: `planCtx(goodPlan, planPath, "docs/todo.md")` (`internal/core/evidence_test.go:31`) with `TodoPath = "docs/todo.md"`.
  - Asserts: the same reason.
  - Covers: #18 criterion 1 for plan steps.
- **`TestPlanFileCheckStillAcceptsAPlanStepThatChangedOnlyItsPlan`** (`internal/core/evidence_test.go`)
  - Setup: `planCtx(goodPlan, planPath)` with `TodoPath = "docs/todo.md"`.
  - Asserts: returns `true, ""`.
  - Covers: #18 criterion 4.
- **`TestJudge`** (modify `internal/core/evidence_test.go:479`)
  - The `"malformed"` row passes `fmt.Errorf("%w: unexpected end of JSON input", ErrSentinelMalformed)` and expects `"sentinel unreadable: sentinel malformed: unexpected end of JSON input"`.
  - Add a row `"unknown outcome"`: `Sentinel{Outcome: "done"}, nil` → `StepFailed, "sentinel unreadable"`.
  - Covers: #24 criterion 2 (reason carries the parse error).
- **`TestAnImplementStepThatChangedTheTodoFailsNamingIt`** (`internal/core/session_test.go`)
  - Setup: rig (`internal/core/session_test.go:68`) with a `ref := r.ref(1)` whose `Vars["TodoPath"] = "/repo/docs/todo.md"`, spawned via `r.sm.Spawn`, and `r.repo.TreeChanges = []string{"a.go", "docs/todo.md"}`. The script writes an ok sentinel.
  - Asserts: `Wait` returns `StepFailed` with reason `"evidence missing: step changed docs/todo.md, the run's plan file; only the driver edits it"`.
  - Covers: #18 criterion 1 end to end (absolute todo → repo-relative).
- **`TestAHalfWrittenSentinelThatTurnsValidEndsTheStepByItsContent`** (`internal/core/session_test.go`)
  - Table: `"empty"` → `""`, `"truncated"` → `{"outcome":"o`.
  - Setup: the script writes the half-written body on poll 1 and a valid ok sentinel on poll 2, returning `AgentWorking`; `TreeChanges = []string{"a.go"}`.
  - Asserts: `out.State == StepOK`, `out.Reason == ""`.
  - Covers: #24 criterion 1.
- **`TestASentinelStillMalformedAfterTheGraceFailsWithTheParseError`** (`internal/core/session_test.go`)
  - Setup: the script writes `{"outcome":` on every poll, returning `AgentWorking`. The rig clock steps 1 min per call, which is past the 30 s grace period.
  - Asserts: `out.State == StepFailed`, and `out.Reason` has the prefix `"sentinel unreadable: "` and contains `"unexpected end of JSON input"`.
  - Covers: #24 criterion 2.
- **`TestAGoneAgentThatFinishedItsSentinelEndsByItsContent`** (`internal/core/session_test.go`)
  - Setup: the script writes `{"outcome":"o` and returns `AgentWorking` on poll 1. On poll 2 it writes a valid ok sentinel and returns `AgentGone`. `TreeChanges = []string{"a.go"}`.
  - Asserts: `out.State == StepOK`.
  - Covers: #24 criterion 1 (the write-then-exit race).
- **`TestAGoneAgentLeavingAHalfWrittenSentinelFailsWithTheParseError`** (`internal/core/session_test.go`)
  - Setup: the script writes `{"outcome":` and returns `AgentWorking` on poll 1, then returns `AgentGone`.
  - Asserts: the reason has the prefix `"sentinel unreadable: "`, not `"agent gone"`.
  - Covers: #24 criterion 2 for the gone-agent path.
- **`TestLandRestoresATodoThePhaseBranchTickedAndTicksItOnce`** (`internal/core/land_test.go`, package `core_test`)
  - Setup:
    - `e.phaseWork(1, "feature.txt", "new\n")`.
    - Write `strings.Replace(todoText, "- [ ] p1 item", "- [x] p1 item", 1)` to the worktree's `docs/demo/todo.md` and `e.repo.CommitAll(".r-loop/wt/phase-1", "self tick")`.
    - `g := e.gate(); g.Plan = plan.Reader{}`.
  - Asserts:
    - `g.Land(ctx, phaseOne(""))` succeeds.
    - `git show HEAD:docs/demo/todo.md` equals `todoText` with only `p1 item` ticked, and `strings.Count` of `"- [x]"` is 1.
    - `feature.txt` is in HEAD.
    - The primary tree is clean.
  - Covers: #18 criterion 2 (the real `Tick` returns `ErrNothingToTick` on the base code).
- **`TestLandTicksOnceWhenTheBranchSelfTickConflictsWithMainsTodo`** (`internal/core/land_test.go`)
  - Setup:
    - Branch phase-1 as above: `feature.txt` plus the todo with `- [x] p1 item`, committed.
    - Then on main, write `strings.Replace(todoText, "- [ ] p1 item", "- [ ] p1 item, reworded", 1)` to `e.todo` and `gitCmd` add and commit it.
    - `g.Plan = plan.Reader{}`.
  - Asserts:
    - `Land(phaseOne(""))` succeeds.
    - HEAD's todo is main's text with `- [x] p1 item, reworded`, and `strings.Count` of `"- [x]"` is 1.
    - `feature.txt` is in HEAD, and the tree is clean.
  - Covers: #18 criterion 2 when the self-tick conflicts (fails with `ErrMergeConflict` on the base code).
- **`TestLandRestoresATodoThePhaseBranchDeleted`** (`internal/core/land_test.go`)
  - Setup: branch phase-1 writes `feature.txt` and runs `gitCmd(t, e.worktree(1), "rm", "-q", "docs/demo/todo.md")`, then commits. `g.Plan = plan.Reader{}`.
  - Asserts:
    - `Land(phaseOne(""))` succeeds.
    - HEAD's todo is `todoText` with `p1 item` ticked.
  - Covers: #18 criterion 2 for a deleted todo.
- **`TestLandTicksTheSameBacklogItemWhenThePhaseBranchInsertedOne`** (`internal/core/land_test.go`)
  - Setup:
    - On main, write `"# Backlog\n\n- [ ] first item\n- [ ] second item\n- [ ] third item\n"` to `e.todo`, then `git add -A` and commit.
    - The phase-2 worktree (`AddWorktree(".r-loop/wt/phase-2", "r-loop/phase-2", "main")`) writes `feature.txt` and the backlog with `- [ ] inserted item` as its first item, then `CommitAll`.
    - `g.Plan = plan.Reader{}`.
    - Land `core.Phase{ID: "2", Title: "second item", Items: []core.Item{{Text: "second item"}}}`.
  - Asserts: HEAD's backlog is `"# Backlog\n\n- [ ] first item\n- [x] second item  <!-- fixed: r-loop/phase-2 -->\n- [ ] third item\n"`.
  - Covers: #18 criterion 3.
- **`TestMilestoneReportThatChangedAnotherPathIsSkippedAndDiscarded`** (`internal/core/land_test.go`)
  - Test-helper change: `reportHost.Prompt` (`internal/core/land_test.go:973`) gains an outcome `"extra"`: it writes the report, writes `a.txt` = `"scribbled\n"` and a new `stray.txt`, and sentinel `ok`.
  - Setup: `e.boundaryGate("extra")`, land phases 1 and 2.
  - Asserts:
    - `HEAD` equals the phase-2 `landing.MergeSHA` (no `docs(report)` commit).
    - `a.txt` is `"one\n"`, `stray.txt` is absent, and `git status --porcelain` is empty.
    - One `report-skipped` event whose reason contains `a.txt` and `stray.txt`.
  - Covers: #19 criterion 1.
- **`TestMilestoneReportCommitCarryingAStagedPathIsUndone`** (`internal/core/land_test.go`)
  - Test-helper change: `reportHost.Prompt` gains an outcome `"staged"`. It writes the report, writes `a.txt` = `"scribbled\n"`, runs `exec.Command("git", "-C", cwd, "add", "a.txt")`, writes `a.txt` back to `"one\n"`, and writes sentinel `ok`.
  - Asserts:
    - `HEAD` equals the phase-2 landing.
    - The tree is clean and `a.txt` is `"one\n"`.
    - One `report-skipped` event whose reason contains `"touched"` and `a.txt`.
  - Covers: #19 criterion 2 (the report commit is never left touching anything but the report).
- **`TestMilestoneReportThatLeftTheBranchResetsNothing`** (`internal/core/land_test.go`)
  - Test-helper change: `reportHost.Prompt` gains an outcome `"checkout"`. It runs `git -C cwd checkout -qb other`, writes `a.txt` = `"scribbled\n"`, commits it on `other` with the same `-c user.name/-c user.email` flags as `"commit"`, and writes sentinel `ok`.
  - Asserts:
    - `git rev-parse main` equals the phase-2 `landing.MergeSHA`.
    - `git rev-parse other` is still the agent's commit, whose `a.txt` is `"scribbled\n"`.
    - One `report-skipped` event whose reason contains `left main for other`.
  - Covers: #19 criterion 1's cleanup never destroys another branch.
- **`TestMilestoneReportAgentCommitIsUndone`** (`internal/core/land_test.go`)
  - Test-helper change: `reportHost.Prompt` gains an outcome `"commit"`. It writes the report, writes `a.txt` = `"scribbled\n"`, runs `exec.Command("git", "-C", cwd, "-c", "user.name=agent", "-c", "user.email=agent@example.com", "commit", "-qam", "agent")`, and writes sentinel `ok`.
  - Asserts:
    - `HEAD` equals the phase-2 `landing.MergeSHA`, so the agent's commit is gone.
    - `a.txt` is `"one\n"`, the report is absent, and the tree is clean.
    - One `report-skipped` event.
  - Covers: #19 criterion 1 (on the base code, `restore()` resets only to the agent's commit, which stays on main).
- **`TestMilestoneReportSpawnedOnlyAfterTheLastPhaseInThePrimaryTree`** (existing, `internal/core/land_test.go:1064`, unchanged)
  - Asserts: a report-only step commits `docs(report): milestone 1` touching exactly the report, with a clean tree.
  - Covers: #19 criteria 2 and 3.
- **`TestEveryStepPromptTellsTheAgentToWriteTheSentinelAtomically`** (`internal/prompts/render_test.go`)
  - Cases:
    - Every name in `stepTemplates` with `fullVars()` (`ReviewedKind` `"implement"`).
    - `review`, `review-ui` and `fix` with `ReviewedKind` `"milestone"` (the `outcome` partial).
  - Asserts: the rendered text contains `"temporary file in the same directory"` and ``"then rename that file onto `/runs/r1/phase-7/plan-a1.sentinel`"``.
  - Covers: #24 criterion 3, for plan, implement and land-stage prompts (the watchdog's warning).

## Left out

- **Title matching in `plan.tickBacklog`:** positions are the item ID in the run list, resume and status. Holding the todo to main's copy at merge and the evidence check close every source this phase names.
- **A warning when the branch changed the todo:** no criterion needs it. Emitted after the no-commit merge, it would claim a landing that a later gate or commit failure undoes.
- **Reading and rewriting the merged todo in land:** it can't handle a deleted todo or a conflict, both of which `MergeNoFF`'s `keep` covers.
- **A new `report-only` check name or config key:** the milestone guard lives in `MilestoneBoundary.report`, the only caller that needs it. The gate probe also uses `report`, but its report lives in the run dir and it restores the tree itself (`internal/core/gate.go:115`).
- **Making `gitrepo.Repo.Commit` use `commit --only`:** land commits by path during a merge, where git refuses partial commits.
- **A config key for the sentinel grace period:** no item asks for one.
- **Resetting the grace clock when the sentinel disappears between two malformed reads:** no source writes, deletes and rewrites a sentinel. Atomic rename leaves no gap.
- **Switching `checks.go`'s `todoRel` (`internal/core/checks.go:161`) to `repoRel`:** the watch warning `plan-touched` works as it is, and no item needs it changed.

## Assumptions

- Commits the maintainer makes to main mid-run that insert backlog items are outside #18. The item's source is a step editing the file (`issues-reliability-review-2026-09-23-notes.md` #18), and that path is closed by the evidence check and the land restore.
- The todo guard compares one path, the run's `TodoPath` made repo-relative. A todo outside the repository never matches a tree-diff path, so it gets no guard, as today.
- Ignored paths are outside the report guard. Every evidence check measures changes with `Snapshot`, which leaves ignored files out (`internal/gitrepo/repo.go:392-393`). An ignored file can't enter the report commit either, because it is staged by the report's path.
- The grace period is 30 s. With the default 10 s poll, a malformed sentinel gets at least three more reads.
- A malformed sentinel within the grace period doesn't pause the stall or backstop clocks. The default stall grace of 2 min is longer than the sentinel grace.
- The atomic-write sentence goes into both partials word for word, so user prompt overrides under `.r-loop/prompts/` get it too.
- The watchdog's warning is resolved by change 5 and by the land-stage cases in `TestEveryStepPromptTellsTheAgentToWriteTheSentinelAtomically`.

## Gate

`go test ./internal/core/ -run '^(TestDiffCheckFailsNamingTheRunsPlanFileWhenTheStepChangedIt|TestPlanFileCheckFailsNamingTheRunsPlanFileWhenThePlanStepChangedIt|TestPlanFileCheckStillAcceptsAPlanStepThatChangedOnlyItsPlan|TestJudge|TestAnImplementStepThatChangedTheTodoFailsNamingIt|TestAHalfWrittenSentinelThatTurnsValidEndsTheStepByItsContent|TestASentinelStillMalformedAfterTheGraceFailsWithTheParseError|TestAGoneAgentThatFinishedItsSentinelEndsByItsContent|TestAGoneAgentLeavingAHalfWrittenSentinelFailsWithTheParseError|TestLandRestoresATodoThePhaseBranchTickedAndTicksItOnce|TestLandTicksOnceWhenTheBranchSelfTickConflictsWithMainsTodo|TestLandRestoresATodoThePhaseBranchDeleted|TestLandTicksTheSameBacklogItemWhenThePhaseBranchInsertedOne|TestMilestoneReportThatChangedAnotherPathIsSkippedAndDiscarded|TestMilestoneReportCommitCarryingAStagedPathIsUndone|TestMilestoneReportAgentCommitIsUndone|TestMilestoneReportThatLeftTheBranchResetsNothing|TestMilestoneReportSpawnedOnlyAfterTheLastPhaseInThePrimaryTree)$' && go test ./internal/prompts/ -run '^TestEveryStepPromptTellsTheAgentToWriteTheSentinelAtomically$'`
