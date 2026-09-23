status: planned

## Summary

The phase makes every driver action on the primary tree refuse instead of clobbering, and makes a fresh run refuse leftovers instead of reusing them.

- **Land** checks three things before it merges: no `MERGE_HEAD`, the checked-out branch equals the branch recorded at run start, and the tree is clean. It checks them at land start and again right before `MergeNoFF` when gate discovery ran, since the probe session works in the primary tree. It takes a `Snapshot` right after the merge and another before the tick. If they differ, something edited the tree while the gate ran, so land refuses and aborts. It commits the merge plus the todo by path, never with `add -A`. Cleanup after a merge uses `git merge --abort` (`reset --merge`), and cleanup after the commit uses `reset --keep`. Neither of them overwrites an edit that is not in the landing. `reset --hard` is no longer used in land.
- **Gate probe and milestone report** refuse to start on a dirty primary tree. Their `reset --hard` then only throws away what their own session wrote.
- **Start branch**: it goes into `meta.json` (`branch`) at preflight. Land and resume compare `HeadBranch()` against it.
- **Leftovers**: preflight refuses a fresh run when `.r-loop/wt/phase-N` or `r-loop/phase-N` exists for any phase in its run list. Resume never calls this check.
- **Item-skipped**: the path removes the phase's worktree and force-deletes its branch. A failed removal or deletion blocks the phase (exit 1) instead of finishing the run, so the run stays resumable. On resume, a phase that already has an `item-skipped` event is not re-run, but its cleanup is replayed, so a crash or a failed removal after the event still ends with no worktree and no branch. Removal and branch deletion in `gitrepo` are no-ops on something already gone, and a failed branch lookup is an error, never "gone".
- **git**: a failure caused by an existing `index.lock` is retried with bounded backoff inside `gitrepo`. An attempt that failed on the lock does not count as started, and `MergeNoFF` decides whether a lock is its own from a check made right before the attempt that actually started, so an interrupted `MergeNoFF` never deletes a lock another process holds and always removes the one its own killed merge left. `AbortMerge` failures and a leftover `MERGE_HEAD` are named explicitly, in land, preflight and resume.

Choices:
- **Where the leftover check lives.** Options: preflight (fresh-run only), `AddWorktree`, or the start of `RunLoop.Run`. Taken: **preflight**, in `Preflight` after `w.clean()`. `AddWorktree` runs twice per phase and must reuse the phase's own worktree (watchdog warning 1), so the check can't go there. `RunLoop.Run` can't cleanly tell a fresh run from a resume before triage. Triage only drops or folds phases into a head that keeps its ID (`internal/core/triage.go:113-130`), so the preflight run list covers every phase the run will work.
- **Where the start branch is stored.** Options: a `meta.json` field or a new record kind. Taken: **`meta.json` `branch`**. It is written once at `Store.Create`, needs no new record kind and no replay logic, and `Load` already reads meta (`internal/store/store.go:125-131`).
- **Which branch `RunLoop.Run` uses as base.** Options: the recorded branch or `HeadBranch()` as today. Taken: **leave `loop.go:121` as is**. Preflight records `HeadBranch()` moments before `Run`, and resume now refuses unless `HeadBranch()` equals the recorded branch, so the two are always equal.
- **How land spots an edit made while the gate runs.** Options: compare two `Snapshot` trees, or add a new "unstaged paths" port method. Taken: **`Snapshot` + `TreeDiff`**. Both already exist on the port and give the exact changed paths.
- **Undoing a refused landing commit.** Options: a new `ResetKeep` port method, or changing `ResetHard` to `--keep`. Taken: **new `ResetKeep`**. Probe and milestone restores must still drop their own session's edits, which `--keep` does not do.
- **Staging the landing commit.** Options: variadic `paths` on `Commit`, or keep `add -A` and rely on the checks. Taken: **variadic `paths`**. It closes the race between the pre-tick check and the commit, and the milestone and unblock call sites stay unchanged.
- **Deleting an unmerged phase branch on item-skipped.** Options: `DeleteBranch(branch, force bool)`, or always use `-D`. Taken: **`force bool`**. `TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne` pins `-d` as a deliberate safety for landed phases.
- **Where the unfinished-merge wording lives.** Options: `gitrepo.AbortMerge`, or a wrapper in `core/land.go`. Taken: **`gitrepo.AbortMerge`**, with sentinel `core.ErrUnfinishedMerge`. That one place also covers `MergeNoFF`'s own conflict-path abort. It follows `core.ErrMergeConflict` (`internal/gitrepo/repo.go:385-391`).
- **Where the index.lock retry lives.** Options: every git call in `runCtxStarted`, or chosen callers. Taken: **`runCtxStarted`**. Every git call goes through it, and a command that failed on the lock changed nothing.
- **How `MergeNoFF` learns lock ownership for the retried attempt.** Options: a `beforeStart func()` parameter on `runCtxStarted`, called right before each attempt's `cmd.Start()`, or a separate retry loop inside `MergeNoFF`. Taken: **`beforeStart`**. It keeps one retry loop, and only `MergeNoFF` passes a non-nil hook.
- **What a failed item-skipped cleanup does.** Options: block the phase, or keep the warning. Taken: **block**. A warning lets the run finish, and a finished run is never resumed, so the replay would never happen.
- **How `DeleteBranch` tells a missing branch from a failed lookup.** Options: `BranchExists` (`branch --list`, empty on missing, an error otherwise), or the exit code of `rev-parse --verify`. Taken: **`BranchExists`**. It is added in this phase anyway, and it needs no exit-code parsing.

## Changes

Build in this order.

### 1. `internal/core/ports.go` — modify
- `Repo` interface: change `Commit(ctx context.Context, message string) (string, error)` to `Commit(ctx context.Context, message string, paths ...string) (string, error)`. (#3-1)
- `DeleteBranch(branch string) error` → `DeleteBranch(branch string, force bool) error`. (#4-4)
- Add `ResetKeep(ref string) error` after `ResetHard`. (#3-4)
- Add `MergeInProgress() (bool, error)` after `AbortMerge`. (#21-2, #21-3)

### 2. `internal/core/types.go` — modify
- `RunMeta` (`types.go:134`): add `Branch string`.
- `RunState` (`types.go:167`): add `Branch string` next to `ID, Todo`. (#3-2)

### 3. `internal/core/land.go` — modify
- Add sentinels to the `var (...)` block at `land.go:31-34`:
  `ErrDirtyTree = errors.New("primary tree is not clean")` and `ErrUnfinishedMerge = errors.New("unfinished merge")`.
- Add a helper used by land, the probe and the milestone:
  ```go
  func cleanTree(repo Repo) error {
      dirty, err := repo.Dirty("")
      if err != nil {
          return err
      }
      if len(dirty) > 0 {
          return fmt.Errorf("%w: %s", ErrDirtyTree, strings.Join(dirty, ", "))
      }
      return nil
  }
  ```
- `attempt` (`land.go:72`): as its first statements, before `Suite.Command`, add a guard `g.guard()` that returns `(Landing{}, "", "", err)` on error. `func (g *LandGate) guard() error` does this, in order:
  1. `merging, err := g.Repo.MergeInProgress()`. On an error, return it. If `merging`, return `fmt.Errorf("%w: the primary tree holds an unfinished merge (MERGE_HEAD); commit it or run git merge --abort, then resume", ErrUnfinishedMerge)`. (#21-2)
  2. `st, err := g.Store.Load(g.RunID)`. On an error, return `fmt.Errorf("load run: %w", err)`. If `st.Branch != ""`: `head, err := g.Repo.HeadBranch()`. On an error, return it. If `head != st.Branch`, return `fmt.Errorf("%w: the primary tree is on %s, but the run started on %s", ErrLanding, head, st.Branch)`. (#3-2)
  3. `return cleanTree(g.Repo)`. (#3-1)
- In `attempt`, right after `Suite.Command` succeeds (inside `if itemGate`, after `land.go:80`), call `g.guard()` again and return `(Landing{}, "", "", err)` on error. The gate probe runs an agent session in the primary tree, which can check out another branch or leave a merge; this second guard is the one that runs immediately before `MergeNoFF`. (#3-2, #21-2)
- Right after `MergeNoFF` succeeds (`land.go:81`): `merged, err := g.Repo.Snapshot("")`. On an error, return `errors.Join(fmt.Errorf("snapshot: %w", err), g.Repo.AbortMerge())`.
- Replace the tick/commit block (`land.go:114-129`) with this, in order:
  1. The existing `ctx.Err()` check stays as is.
  2. `now, err := g.Repo.Snapshot("")`. On an error, abort the same way as above. If `now != merged`: `paths, _ := g.Repo.TreeDiff(merged, now)`, then return `errors.Join(fmt.Errorf("%w: changed while the gate ran: %s", ErrDirtyTree, strings.Join(paths, ", ")), g.Repo.AbortMerge())`. (#3-1, #3-4)
  3. `todoAbs := g.TodoPath`. If it is not absolute, `todoAbs = filepath.Join(g.Repo.Root(), todoAbs)`. `before, err := os.ReadFile(todoAbs)`. On an error, return `errors.Join(fmt.Errorf("tick: %w", err), g.Repo.AbortMerge())`.
  4. `g.Plan.Tick(...)`. On an error, return `errors.Join(fmt.Errorf("tick: %w", err), g.Repo.AbortMerge(), os.WriteFile(todoAbs, before, 0o644))`. This replaces `ResetHard("HEAD")`. (#3-4)
  5. `sha, err := g.Repo.Commit(ctx, msg, g.todoRel())`. On an error, return `errors.Join(fmt.Errorf("commit: %w", err), g.Repo.AbortMerge(), os.WriteFile(todoAbs, before, 0o644))`. The order matters: abort first, then restore the todo bytes. With the todo already staged, `reset --merge` restores it, and with it unstaged, the explicit write undoes the tick. (#3-1, #3-4)
  6. On the `CommitTouches` refusal (`land.go:124-128`), change `g.Repo.ResetHard("HEAD~1")` to `g.Repo.ResetKeep("HEAD~1")`. (#3-4)
- Every other `g.Repo.AbortMerge()` join in `attempt` stays. The wording comes from `gitrepo.AbortMerge` (step 9). (#21-4)

### 4. `internal/core/gate.go` — modify
- `GateProbe.discover` (`gate.go:65`): as its first statement, add `if err := cleanTree(p.Repo); err != nil { return "", err }`. It comes before any record or spawn. `Command` already wraps the error as `ErrNoGate` (`gate.go:57-59`). `ResetHard("HEAD")` at `gate.go:112` stays: the tree was clean at the start, so it only drops the probe session's edits. (#3-4)

### 5. `internal/core/milestone.go` — modify
- `After` (`milestone.go:24`): after `closed` returns ok and before `b.report(...)`, add:
  `if err := cleanTree(b.Repo); err != nil { recordEvent(... Event{Kind: "report-skipped", ..., Fields: {"milestone": ..., "reason": err.Error()}}); return }`
  Use the same fields as the existing report-skipped event at `milestone.go:37`, and return without calling `restore`. `restore` stays unchanged and still runs only after a report that started on a clean tree. (#3-3)

### 6. `internal/core/workspaces.go` — modify
- `removeWorktree(phase string)` → `removeWorktree(phase string, force bool) error`, passing `force` to `repo.DeleteBranch(branch, force)` (`workspaces.go:23`). It still emits the same warning events, and now also returns the `RemoveWorktree` error (`workspaces.go:19-21`) or the `DeleteBranch` error (`workspaces.go:23-25`), wrapped as `fmt.Errorf("remove worktree %s: %w", wt, err)` and `fmt.Errorf("delete branch %s: %w", branch, err)`, and nil on success.
- The call at `loop.go:333` becomes `l.removeWorktree(n, false)`, ignoring the return: a landed phase keeps today's warning-only behaviour, pinned by `TestAWorktreeThatWillNotGoIsAWarningAndKeepsTheBranch`. (#4-4)

### 7. `internal/core/loop.go` — modify
- Add, next to `itemSkipped`:
  ```go
  func (l *RunLoop) skipCleanup(n string) (string, Outcome, bool) {
      if err := l.removeWorktree(n, true); err != nil {
          return "plan", Outcome{State: StepFailed, Reason: "item skipped, but its cleanup failed: " + err.Error()}, false
      }
      return "", Outcome{State: StepOK}, false
  }
  ```
  A failed outcome goes to `l.block` (`loop.go:173`), which emits `phase-blocked` and exits 1, so the run is not `finished` and resume replays the cleanup. (#4-4)
- Both item-skipped returns (`loop.go:254-256` and `loop.go:286-288`) become `l.closeWorkspaces(n, everyWorkspace)` followed by `return l.skipCleanup(n)`. (#4-4)
- `runPhase` (`loop.go:239`): as its first statement, add `if skippedBefore(prior, ph.ID) { return l.skipCleanup(ph.ID) }`, with:
  ```go
  func skippedBefore(prior RunState, phase string) bool {
      return slices.ContainsFunc(prior.Events, func(e Event) bool { return e.Kind == "item-skipped" && e.Phase == phase })
  }
  ```
  Without this, a resume would re-run a skipped item whose worktree and plan are now gone. It goes before `checkPhase`, so no worktree is re-created. The `removeWorktree` call replays cleanup that a crash after the `item-skipped` event, or a failed `RemoveWorktree`/`DeleteBranch`, left undone; with the gitrepo no-ops below it succeeds silently when nothing is left. (#4-4)

### 8. `internal/core/fakes_test.go` — modify
- `fakeRepo.Commit(ctx, message string, paths ...string)`: record `"Repo.Commit %q"` exactly as today.
- `fakeRepo.DeleteBranch(branch string, force bool)`: record `"Repo.DeleteBranch %s"`, plus the suffix `" --force"` when `force` is true.
- Add `fakeRepo.ResetKeep(ref)`, recording `"Repo.ResetKeep %s"` and returning `f.Err`.
- Add `fakeRepo.MergeInProgress()`, recording `"Repo.MergeInProgress"` and returning `false, f.Err`.

### 9. `internal/gitrepo/repo.go` — modify
- Add `var lockBackoff = []time.Duration{50 * time.Millisecond, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond}`, next to `gitTimeout` (`repo.go:27`). (#21-1)
- `runCtxStarted` gets a new parameter: `runCtxStarted(parent context.Context, dir string, env []string, beforeStart func(), started *bool, args ...string)`. `runCtx` (`repo.go:46`) passes `nil, nil`. (#21-1)
- `runCtxStarted` (`repo.go:49`): move the current body, from `ctx, cancel :=` through the returns, into `runOnce(parent, dir, env, beforeStart, started, args) (string, string, error)`, which also returns the stderr text. `runOnce` calls `beforeStart()`, when non-nil, immediately before `cmd.Start()` (`repo.go:62`). `runCtxStarted` then loops:
  ```go
  for i := 0; ; i++ {
      out, stderr, err := runOnce(...)
      locked := err != nil && strings.Contains(stderr, "index.lock': File exists")
      if locked && started != nil {
          *started = false
      }
      if err == nil || i == len(lockBackoff) || !strings.Contains(stderr, "index.lock': File exists") {
          return out, err
      }
      select {
      case <-parent.Done():
          return out, fmt.Errorf("git %s: interrupted: %w", strings.Join(args, " "), parent.Err())
      case <-time.After(lockBackoff[i]):
      }
  }
  ```
  The early `parent.Err()` check stays at the top of `runCtxStarted`. Resetting `*started` on a lock failure matters: git refused before touching anything, so `MergeNoFF`'s interrupted path (`repo.go:366-374`) must not treat that attempt as its own and remove the lock another process holds when the context is cancelled during backoff. (#21-1)
- `MergeNoFF` (`repo.go:359-362`): delete the pre-merge `os.Stat` and `lockWasAbsent :=` lines. Declare `var lockWasAbsent bool` and pass `beforeStart = func() { _, err := os.Stat(lockPath); lockWasAbsent = errors.Is(err, os.ErrNotExist) }` to `runCtxStarted`. Each attempt overwrites it, so when the started attempt is cancelled, `lockWasAbsent` describes the lock at that attempt's start: a lock that appeared afterwards belongs to its killed merge and is removed, and a foreign lock present at its start is kept. (#21-1, #21-4)
- `AbortMerge` (`repo.go:394`): on an error, return `fmt.Errorf("%w: git merge --abort failed, so the primary tree still holds an unfinished merge; finish it with git merge --abort: %v", core.ErrUnfinishedMerge, err)`. (#21-4)
- `MergeNoFF`'s interrupted path (`repo.go:375-377`) becomes:
  ```go
  if abortErr := r.AbortMerge(); abortErr != nil {
      if resetErr := r.ResetHard(pre); resetErr != nil {
          return errors.Join(mergeErr, abortErr, resetErr)
      }
  }
  return mergeErr
  ```
  (#21-4)
- Add `MergeInProgress() (bool, error)`. It runs `rev-parse --git-path MERGE_HEAD`, makes the path absolute against `r.root`, as `repo.go:351-356` does for the lock path, and `os.Stat`s it: true if it exists, false on `os.ErrNotExist`, and the error otherwise. (#21-2, #21-3)
- Add `ResetKeep(ref string) error`, which runs `r.git("", "reset", "--keep", "-q", ref)`. (#3-4)
- `Commit(ctx, message string, paths ...string)` (`repo.go:399`): with no `paths`, run `add -A` as today. Otherwise run `r.git("", append([]string{"add", "--"}, paths...)...)`. (#3-1)
- `DeleteBranch(branch string, force bool)` (`repo.go:186`): first `exists, err := r.BranchExists(branch)`. On an error, return it. If `!exists`, return nil. Otherwise pass `-D` when `force` is true, `-d` otherwise. (#4-4)
- `RemoveWorktree(dir)` (`repo.go:178`): first `_, found, err := r.worktreeBranch(r.path(dir))`; on an error return it; if not found, run `worktree prune` and return its error, without `worktree remove`. (#4-4)
- Add `BranchExists(branch string) (bool, error)`: `out, err := r.git("", "branch", "--list", branch)`, then `strings.TrimSpace(out) != ""`. This is not on the core port; only `app` calls it. (#4-1)

### 10. `internal/store/store.go` — modify
- `meta` (`store.go:49`): add ``Branch string `json:"branch,omitempty"` ``.
- `Create` writes `meta{Todo: m.Todo, Started: m.Started, Branch: m.Branch}`.
- `Load` sets `st.Branch = m.Branch` next to `store.go:131`. (#3-2)

### 11. `internal/app/preflight.go` — modify
- `clean()` (`preflight.go:164`): as its first statement, add a `MergeInProgress` check. On an error, `exit(2, "%v", err)`. If it returns true, return `exit(4, "primary tree holds an unfinished merge (MERGE_HEAD): commit it or run git merge --abort, then retry")`. The check comes before the dirty listing, so the generic message never shows for a merge. It is shared by preflight and resume. (#21-3)
- Add `func (w *Wiring) leftovers(list []core.Phase) error`. For each phase in `list`:
  - `wt := ".r-loop/wt/phase-" + ph.ID`. If `os.Stat(filepath.Join(w.Repo.Root(), wt))` succeeds, record `wt`.
  - `branch := "r-loop/phase-" + ph.ID`. If `w.Repo.BranchExists(branch)` returns true, record `branch`. An error from it becomes `exit(2, "%v", err)`.
  - If anything is recorded, return `exit(4, "leftover from an earlier run: %s; resume that run with r-loop resume, or remove them (git worktree remove --force <dir>, git branch -D <branch>)", strings.Join(found, ", "))`.
- `Preflight` (`preflight.go:56-63`): after `w.clean()`, call `w.leftovers(list)`. Then `branch, err := w.Repo.HeadBranch()`, with an error becoming `exit(2, "head branch: %v", err)`. Pass `Branch: branch` into `core.RunMeta`. (#4-1, #4-2, #4-3, #3-2)

### 12. `internal/app/resume.go` — modify
- `resume()` (`resume.go:93-95`): right after `w.clean()` succeeds, if `run.Branch != ""`: `head, err := w.Repo.HeadBranch()`, with an error becoming `exit(2, "head branch: %v", err)`. If `head != run.Branch`, return `exit(4, "primary tree is on %s, but run %s started on %s; check out %s, then resume", head, id, run.Branch, run.Branch)`. (#3-2)

### 13. Existing tests that change with the signatures
- `internal/gitrepo/repo_test.go:847` `TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne`: both calls pass `false`.
- `internal/core/workspaces_test.go` `TestASkippedItemClosesItsWorkspacesButKeepsItsUnmergedBranch`: delete it. It pins the old behaviour that #4-4 reverses, and `TestASkippedItemRemovesItsWorktreeAndForceDeletesItsBranch` replaces it.
- After the change, run `go test ./...`. If any existing test starts a second fresh run over a phase worktree or branch left behind by its first run, change it to remove the leftover first with `git worktree remove --force` and `git branch -D`, via the test's own `git` helper.

## Tests

Write these first.

### `internal/gitrepo/repo_test.go`
- `TestAGitCallRetriesWhileIndexLockIsHeldThenSucceeds`: create `.git/index.lock`, and remove it from a goroutine after 150 ms. `r.git("", "add", "-A")` over a new file returns nil and the file is staged. (#21-1)
- `TestAGitCallGivesUpOnAHeldIndexLockAfterBoundedRetries`: set `lockBackoff = []time.Duration{time.Millisecond, time.Millisecond}` and restore it with `t.Cleanup`. With the lock held throughout, `add -A` returns an error containing `index.lock`, within 5 s. (#21-1)
- `TestAbortMergeThatFailsSaysThePrimaryTreeStillHoldsTheMerge`: `MergeNoFF` a side branch, set a short `lockBackoff`, create `index.lock`. `AbortMerge()` returns an error that `errors.Is(core.ErrUnfinishedMerge)` and contains "unfinished merge", and `MergeInProgress()` still returns true. (#21-4)
- `TestMergeInProgressSeesMergeHead`: returns false on a clean repo, true after `MergeNoFF`, and false after `AbortMerge`. (#21-2, #21-3)
- `TestResetKeepUndoesACommitAndKeepsUnrelatedEdits`: commit `feature.txt`, then write `a.txt` = "mine\n". `ResetKeep(base)`: HEAD is back at `base`, `feature.txt` is gone, and `a.txt` is "mine\n". (#3-4)
- `TestResetKeepRefusesToOverwriteALocalEdit`: commit `feature.txt`, then edit `feature.txt` locally. `ResetKeep(base)` errors, HEAD is unchanged, and the edit is intact. (#3-4)
- `TestCommitWithPathsStagesOnlyThoseOnTopOfTheMerge`: `MergeNoFF` a side branch that adds `feature.txt`, then write `todo.md` and untracked `notes.txt`. `Commit(ctx, "land", "todo.md")` gives a commit whose `CommitTouches` is `[feature.txt todo.md]`, and `notes.txt` is still untracked with its bytes unchanged. (#3-1)
- `TestBranchExists`: `r-loop/phase-1` gives false; after `git branch r-loop/phase-1` it gives true. (#4-1)
- `TestDeleteBranchForcedDeletesAnUnmergedBranch`: an unmerged branch with a commit is gone after `DeleteBranch(b, true)`. (#4-4)
- `TestRemoveWorktreeAndDeleteBranchAreNoOpsWhenAlreadyGone`: `RemoveWorktree(".r-loop/wt/phase-9")` that was never added returns nil. After `AddWorktree(".r-loop/wt/phase-2", "r-loop/phase-2", "HEAD")` and `RemoveWorktree` of it, `DeleteBranch("r-loop/phase-2", true)` deletes the branch, and a second `DeleteBranch("r-loop/phase-2", true)` returns nil. (#4-4)
- `TestACancelledLockRetryDoesNotClaimTheCommandStarted`: create `.git/index.lock`, set `lockBackoff = []time.Duration{time.Second, time.Second}` with `t.Cleanup` restore, and cancel the context from a goroutine after 100 ms. `var started bool; runCtxStarted(ctx, r.root, nil, nil, &started, "add", "-A")` returns an error that `errors.Is(context.Canceled)`, `started` is false, and `.git/index.lock` still exists. (#21-1)
- `TestAMergeRetriedAfterAForeignLockClearsRemovesItsOwnLockWhenCancelled`: `r, dir, head, pidFile := slowMerge(t)`, create `.git/index.lock`, set `lockBackoff = []time.Duration{300 * time.Millisecond, 300 * time.Millisecond}` with `t.Cleanup` restore, and remove the lock from a goroutine after 100 ms. Run `r.MergeNoFF(ctx, "side")` in a goroutine, `waitPID(t, pidFile)` (the retry started the merge), then cancel. `MergeNoFF` returns an error containing "interrupted" within 5 s, `waitProcessGone(t, pid)`, then `assertCleanMerge(t, dir, head)`: no `index.lock`, no `MERGE_HEAD`, HEAD unchanged. (#21-1, #21-4)
- `TestDeleteBranchReportsAFailedLookup`: `git branch r-loop/phase-2`, then set `gitTimeout = time.Nanosecond` with `t.Cleanup` restore. `DeleteBranch("r-loop/phase-2", true)` returns a non-nil error. After restoring `gitTimeout`, `r-loop/phase-2` still exists. (#4-4)
- `TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne` (existing, updated to pass `false`): `-d` stays the default safety.

### `internal/core/land_test.go` (package `core_test`, real git via `newLandEnv`)
- `memStore` gets a `branch string` field, and `Load` sets `st.Branch = s.branch`.
- `TestLandRefusesADirtyPrimaryTreeAndLeavesTheFileAlone`: `phaseWork(1, ...)`, then untracked `notes.txt` = "mine\n" and `a.txt` edited to "edited\n". `Land(phaseOne("true"))` returns an error that `errors.Is(core.ErrDirtyTree)` and names `notes.txt` and `a.txt`. HEAD is unchanged, there is no `MERGE_HEAD` (`assertNoMergeHead`), both files keep their bytes, and there are no ticks and no landing. (#3-1)
- `TestLandRefusesWhenThePrimaryTreeLeftTheStartBranch`: `e.store.branch = "main"`, `phaseWork(1, ...)`, then `git checkout -q -b other` in the primary tree. `Land` returns an error that `errors.Is(core.ErrLanding)` and contains both "other" and "main". HEAD is unchanged and there is no `MERGE_HEAD`. (#3-2)
- `TestLandRechecksTheStartBranchAfterGateDiscovery`: `e.store.branch = "main"`, `phaseWork(1, ...)`, `g := e.gate()`, and `g.Suite = suiteFunc(...)` that runs `gitCmd(t, e.root, "checkout", "-q", "-b", "other")` and returns `"true", nil`. `g.Land(ctx, phaseOne(""))` returns an error that `errors.Is(core.ErrLanding)` and contains both "other" and "main". HEAD of `other` equals the pre-land head, and there is no `MERGE_HEAD`. (#3-2)
- `TestLandRefusesAnUnfinishedMergeInThePrimaryTree`: `phaseWork(1, ...)`, plus a `side` branch adding `side.txt`, then `git merge --no-ff --no-commit side` in the primary tree. `Land` returns an error that `errors.Is(core.ErrUnfinishedMerge)` and contains "MERGE_HEAD". The side merge is still in progress: `.git/MERGE_HEAD` exists and holds `side`'s SHA. (#21-2)
- `TestLandRefusesAnEditMadeWhileTheGateRanAndKeepsIt`: `phaseWork(1, "feature.txt", ...)`, then `Land(phaseOne("printf mine > notes.txt"))`. It returns an error that `errors.Is(core.ErrDirtyTree)` and names `notes.txt`. `notes.txt` is "mine", HEAD is unchanged, there is no `MERGE_HEAD`, the todo equals `todoText`, and there are no ticks. (#3-1, #3-4)
- `TestLandCommitsOnlyTheMergeAndTheTickNotALateMaintainerFile`: a wrapper `lateFileRepo{*gitrepo.Repo}` whose `Commit` writes `notes.txt` = "mine\n" and then calls the embedded `Commit`. `Land(phaseOne("true"))` succeeds, `git show --name-only HEAD` does not list `notes.txt`, and `notes.txt` is untracked with "mine\n". (#3-1)
- `TestLandTickFailureKeepsAMaintainerEdit`: `phaseWork(1, "feature.txt", "new\n")`, and `g.Plan` set to a plan wrapper whose `Tick` writes `a.txt` = "mine\n" and returns `errors.New("disk full")`. `Land` returns an error containing "tick:". `a.txt` is "mine\n", there is no `MERGE_HEAD`, HEAD is unchanged, `feature.txt` is absent from the primary tree, and the todo equals `todoText`. (#3-4)
- `TestLandCommitFailureKeepsAMaintainerEditAndUndoesTheTick`: a wrapper `Repo` whose `Commit` writes `a.txt` = "mine\n" and returns an error without committing. `Land` returns an error containing "commit:". `a.txt` is "mine\n", the todo equals `todoText`, there is no `MERGE_HEAD`, and HEAD is unchanged. (#3-4)
- `TestLandRefusedLandingCommitIsUndoneKeepingAMaintainerEdit`: a wrapper `Repo` whose `CommitTouches` writes `a.txt` = "mine\n" and returns `[]string{"docs/demo/todo.md"}`. `Land` returns an error that `errors.Is(core.ErrLanding)`. HEAD equals the pre-land head, and `a.txt` is "mine\n". (#3-4)
- `TestLandCarriesAFailedMergeAbortInItsError`: a wrapper `Repo` whose `AbortMerge` calls the embedded one and then returns `fmt.Errorf("%w: still merging", core.ErrUnfinishedMerge)`. `Land(phaseOne("exit 1"))` returns an error that is both `errors.Is(core.ErrGate)` and `errors.Is(core.ErrUnfinishedMerge)`. (#21-4)
- `TestMilestoneReportRefusesADirtyTreeAndKeepsTheMaintainerFile`: `phaseWork(1, ...)`, `phaseWork(2, ...)`, `boundaryGate("ok")`. Land phase 1 with `g.Repo` as returned. Then set `g.Repo` to a wrapper `{*gitrepo.Repo}` whose `CommitTouches` writes `notes.bin` = `[]byte{0, 1, 2, 'x', '\n'}` before delegating, and land phase 2, which succeeds. `g.Boundary.Repo` stays `e.repo`. `host.opened` is empty. One `report-skipped` event whose reason contains "primary tree is not clean" and "notes.bin". `notes.bin` has the same bytes. (#3-3)

### `internal/core/gate_test.go`
- `TestGateProbeRefusesADirtyPrimaryTreeWithoutStartingASession`: `e.probe("ok", "test -f a.txt")`, then untracked `notes.txt` = "mine\n" and `a.txt` edited. `Command` returns an error that `errors.Is(core.ErrNoGate)` and `errors.Is(core.ErrDirtyTree)` and names both files. `host.opened` is empty, both files keep their bytes, and there is no `step` event. (#3-4)

### `internal/core/workspaces_test.go` (package `core`)
- `TestASkippedItemRemovesItsWorktreeAndForceDeletesItsBranch`: `ItemGates = true`, `behaviour["rloop-p2-plan"] = "already-done"`, run with `Phases: {"2"}`. Calls include `Repo.RemoveWorktree .r-loop/wt/phase-2`, followed by `Repo.DeleteBranch r-loop/phase-2 --force`, after `SessionHost.Close ws-1`. One `worktree-removed` event for phase 2. (#4-4)

- `TestASkippedItemWhoseWorktreeWillNotGoBlocksThePhase`: `ItemGates = true`, `behaviour["rloop-p2-plan"] = "already-done"`, `r.loop.Sessions.Repo = removeFailRepo{r.repo}`, run with `Phases: {"2"}`. The exit is 1, one `phase-blocked` event for phase 2 whose reason contains "item skipped, but its cleanup failed" and "worktree is locked", and no `finished` event. (#4-4)
- `TestASkippedItemWhoseBranchWillNotGoBlocksThePhase`: the same, with a new `deleteFailRepo{*loopRepo}` whose `DeleteBranch` records through the embedded one and returns `errors.New("branch is busy")`. The exit is 1, and the `phase-blocked` reason contains "delete branch r-loop/phase-2" and "branch is busy". (#4-4)

### `internal/core/loop_test.go` (package `core`)
- `TestAnItemSkippedBeforeIsNotRerunOnResume`: seed `r.store` with a step record for `plan` attempt 1 = `StepOK` of phase 2, and an event record `{Kind: "item-skipped", Phase: "2"}`. Run with `RunOptions{Phases: {"2"}, Resume: true}`. The exit is 0, there are no `Repo.AddWorktree` calls, no `SessionHost.Prompt` calls, and no `Land ` calls. The calls include `Repo.RemoveWorktree .r-loop/wt/phase-2` followed by `Repo.DeleteBranch r-loop/phase-2 --force`: the seeded state is exactly what a crash after the `item-skipped` event, or a failed removal, leaves behind. (#4-4)
- `TestAnItemSkippedBeforeWhoseCleanupStillFailsStaysBlocked`: the same seeded state and options, with `r.loop.Sessions.Repo = removeFailRepo{r.repo}`. The exit is 1, one `phase-blocked` event for phase 2 whose reason contains "worktree is locked", no `finished` event, and no `Repo.AddWorktree` calls. (#4-4)

### `internal/store/store_test.go`
- `TestCreateRecordsTheStartBranchAndLoadReadsIt`: `Create(RunMeta{Todo: ..., Branch: "main"})`, and `Load(id).Branch == "main"`. A `meta.json` rewritten without `branch` loads with `Branch == ""`. (#3-2)

### `internal/app/app_test.go`
- `TestPreflightRecordsTheStartBranch`: after `f.preflight(f.todo, "--plain")`, `store.New(f.root).Load(w.Loop.RunID).Branch == "main"`. (#3-2)
- `TestALeftoverPhaseWorktreeIsRefusedWithExit4`: `f.commit()`, then write `.git/info/exclude` = `.r-loop/\n`. Run `git worktree add -q -b r-loop/phase-2 .r-loop/wt/phase-2`, then write `.r-loop/wt/phase-2/wip.txt` = "wip". `f.preflight(f.todo, "--plain")` exits 4. The error contains `.r-loop/wt/phase-2` and `r-loop/phase-2`. `wip.txt` is unchanged, and `.r-loop/runs` holds no run directory. (#4-1, #4-3)
- `TestALeftoverPhaseBranchOnAnOlderBaseIsRefusedWithExit4`: `f.commit()`, `git branch r-loop/phase-2`, then write and commit `later.txt` so that main moves on. Preflight exits 4 and names `r-loop/phase-2`. (#4-1, #4-2)
- `TestAnUnfinishedMergeIsNamedInPreflight`: `f.commit()`, a `side` branch adding `side.txt`, then `git merge --no-ff --no-commit side`. Preflight exits 4. The error contains "unfinished merge" and "git merge --abort", and does not contain "primary tree is not clean". (#21-3)

### `internal/app/resume_test.go`
- `TestResumeNamesAnUnfinishedMerge`: `newResumeFixture(t, noReviewConfig)`, then `firstRun` with `rloop-p1-implement` failing, so the exit is 1. Then a `side` branch in `f.root` and `git merge --no-ff --no-commit side`. `f.resume(newSim())` exits 4. The error contains "unfinished merge" and "git merge --abort", and does not contain "primary tree is not clean". (#21-3)
- `TestResumeRefusesWhenThePrimaryTreeLeftTheStartBranch`: the same halted first run, then `git checkout -q -b other`. `f.resume(newSim())` exits 4 and the error contains "other" and "main". (#3-2)
- `TestResumeAfterFailedImplementRerunsOnlyImplementAsAttempt2OverItsWork` (existing, regression pin): resume still reuses the halted phase's own worktree, so the leftover check never runs on resume. This is watchdog warning 1.

## Left out

- A leftover check inside `AddWorktree`: the phase check and step spawn both call it for the same worktree, and resume reuses it by design (watchdog warning 1). Preflight covers every fresh run.
- A base-ancestry check on an existing branch in `AddWorktree`: a fresh run refuses any existing `r-loop/phase-N`. Within a run, a branch is created only by that run's own phase check, from the current base, and resume's branch is the run's own work.
- Switching `RunLoop.Run` to the recorded branch: it always equals `HeadBranch()` once preflight records it and resume enforces it.
- A new record kind for the start branch: `meta.json` already holds per-run constants.
- Restricting the milestone commit to the report path: that is #19's scope. With the new clean-tree precondition, `add -A` there only sees the report session's output.
- Changing `unblock.go`'s commit: it already refuses a dirty tree (`unblock.go:66`) and is not a land-stage operation.
- `index.lock` handling beyond the retry, such as deleting a stale lock: the item asks only for a bounded retry, and deleting another process's lock is unsafe.

## Assumptions

- **Tree check granularity.** Land refuses any dirty path at land start. It does not try to tell "outside the phase's changes" apart from inside them, because the primary tree has no legitimate dirty path before a merge: `.r-loop/runs/` and `.r-loop/wt/` are excluded. A dirty path that the merge would touch would also make `git merge` fail.
- **Files the gate writes.** Land refuses a file the gate command writes into the primary tree that is neither ignored nor excluded, since it is indistinguishable from a maintainer edit made during the gate. It names the file and leaves it in place. Before this phase, such a file was swept into the landing commit.
- **Edits during an in-primary session.** An edit made in the primary tree while a gate-probe or milestone-report session is running is treated as that session's work: the session writes into the same tree, so its edits and a maintainer's can't be told apart. Their restore still uses `reset --hard`, and the tree is verified clean before they start, which is the mechanism #3-4 names ("refuse to start on a dirty tree instead").
- **A path staged by another git process mid-land.** Land refuses dirt present at land start and edits made while the gate runs. A path that another git process stages between the final snapshot and `git commit` is outside #3-1, which covers files dirty when land starts; `Commit` stages only the todo by path.
- **Old runs without a branch.** A run whose `meta.json` predates this phase has no `branch`, so land and resume skip the branch comparison for it rather than refusing it.
- **The skipped item's plan.** The item-skipped path force-deletes the phase branch, and with it the committed skip plan. Its status and evidence survive in the `item-skipped` event and the run report.
- **The double-failure path is untested.** In `MergeNoFF`'s interrupted path, the case where both `merge --abort` and `reset --hard` fail is changed to carry the unfinished-merge error. No test covers it: making both fail after the driver removes the killed merge's lock cannot be staged deterministically. The wording itself is covered by `TestAbortMergeThatFailsSaysThePrimaryTreeStillHoldsTheMerge`.
- **The backoff schedule.** 50 ms doubling to 1.6 s, six retries, about 3.2 s in total. It is a package variable so tests can shorten it, like `gitTimeout`.
- **Exit codes.** Leftovers, an unfinished merge and a branch mismatch exit 4, like the existing dirty-tree refusal. A `HeadBranch` or `BranchExists` read failure exits 2, like the other read failures in preflight.
- **The item number.** The phase text lists the index.lock item as #22, but the backlog file numbers it #21 (`issues-reliability-review-2026-09-23.md:131`). This plan implements the criteria as listed.

## Gate

`go test ./internal/gitrepo/ ./internal/core/ ./internal/store/ ./internal/app/ -run '^(TestAGitCallRetriesWhileIndexLockIsHeldThenSucceeds|TestAGitCallGivesUpOnAHeldIndexLockAfterBoundedRetries|TestAbortMergeThatFailsSaysThePrimaryTreeStillHoldsTheMerge|TestMergeInProgressSeesMergeHead|TestResetKeepUndoesACommitAndKeepsUnrelatedEdits|TestResetKeepRefusesToOverwriteALocalEdit|TestCommitWithPathsStagesOnlyThoseOnTopOfTheMerge|TestBranchExists|TestDeleteBranchForcedDeletesAnUnmergedBranch|TestRemoveWorktreeAndDeleteBranchAreNoOpsWhenAlreadyGone|TestACancelledLockRetryDoesNotClaimTheCommandStarted|TestAMergeRetriedAfterAForeignLockClearsRemovesItsOwnLockWhenCancelled|TestDeleteBranchReportsAFailedLookup|TestDeleteBranchDeletesAMergedBranchAndRefusesAnUnmergedOne|TestLandRefusesADirtyPrimaryTreeAndLeavesTheFileAlone|TestLandRefusesWhenThePrimaryTreeLeftTheStartBranch|TestLandRechecksTheStartBranchAfterGateDiscovery|TestLandRefusesAnUnfinishedMergeInThePrimaryTree|TestLandRefusesAnEditMadeWhileTheGateRanAndKeepsIt|TestLandCommitsOnlyTheMergeAndTheTickNotALateMaintainerFile|TestLandTickFailureKeepsAMaintainerEdit|TestLandCommitFailureKeepsAMaintainerEditAndUndoesTheTick|TestLandRefusedLandingCommitIsUndoneKeepingAMaintainerEdit|TestLandCarriesAFailedMergeAbortInItsError|TestMilestoneReportRefusesADirtyTreeAndKeepsTheMaintainerFile|TestGateProbeRefusesADirtyPrimaryTreeWithoutStartingASession|TestASkippedItemRemovesItsWorktreeAndForceDeletesItsBranch|TestASkippedItemWhoseWorktreeWillNotGoBlocksThePhase|TestASkippedItemWhoseBranchWillNotGoBlocksThePhase|TestAnItemSkippedBeforeIsNotRerunOnResume|TestAnItemSkippedBeforeWhoseCleanupStillFailsStaysBlocked|TestCreateRecordsTheStartBranchAndLoadReadsIt|TestPreflightRecordsTheStartBranch|TestALeftoverPhaseWorktreeIsRefusedWithExit4|TestALeftoverPhaseBranchOnAnOlderBaseIsRefusedWithExit4|TestAnUnfinishedMergeIsNamedInPreflight|TestResumeNamesAnUnfinishedMerge|TestResumeRefusesWhenThePrimaryTreeLeftTheStartBranch|TestResumeAfterFailedImplementRerunsOnlyImplementAsAttempt2OverItsWork)$'`
