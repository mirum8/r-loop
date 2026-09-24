status: planned

## Summary

Two gaps. (#10) The driver acts first and writes the record second, so a crash can leave committed or merged work that the store never mentions. (#11) A failed append only raises a face warning, and the run carries on without a record. The fix has four parts:

1. **Intent records.** Two new store-only events are written before each driver action on git:
   - `commit-intent` goes before the two commits resume has to reconcile: a step's worktree commit in `SessionManager.Finish`, and land's commit. It names the exact tree the commit is meant to contain. The milestone report's commit and the unblock walk's commit need none (see `## Assumptions`).
   - `merge-intent` goes before land's `MergeNoFF`.
   The order becomes intent → action → outcome (the `ok` step record, or the `landing` record).
2. **Resume reconciliation** runs before the checks that used to get in the way:
   - `reconcileLand` runs in `PrepareResume` right after the run is loaded. That is before `Wire` reads the todo, before the ticked-phase filter (`resume.go:96-97`) and before `w.clean()` (`resume.go:109`). It completes, aborts or records the land that the last `merge-intent` describes, and it acts only when the tree on disk, or the recovered commit, matches the tree the intent recorded.
   - `closeInterrupted` sees a step's `commit-intent` and records the step `ok` instead of `failed`, but only when the worktree still holds exactly the recorded tree. When the commit had not happened yet, it makes the commit first.
3. **Per-kind record policy.** `core.FatalRecord` sorts records into fatal and warn. Fatal means every step, run and landing record, plus every event resume reads to decide what to run. `core.RecordGuard` is a `Store` decorator that latches the first failed fatal append.
4. **Stopping after a fatal failure.**
   - The loop, `SessionManager.Spawn`/`Finish`, `LandGate.attempt` (at its start and again right before the merge) and `MilestoneBoundary.report` check the latch. So after a fatal failure nothing else is spawned, committed or merged, and the run halts with exit 2 and `record: <err>`. The error is shown in the face, and `Execute` prints it on stderr.
   - The app's own run-status and `run-list` writes before the loop go through `Wiring.recordFatal`, which gives the same face error and the same stderr line.

Choices:

- **Shape of an intent record:** an `Event` record with new kinds `merge-intent` and `commit-intent`, rather than a new `Record` kind or `StepState`. Resume already reads events (`baseline`, `agent-named`), so the store's schema, `RunState` and the `StepState` machine stay as they are.
- **Proving what a commit contains:** record the exact tree in every `commit-intent`: `Repo.Snapshot(dir)` for a step (`CommitAll` stages everything, untracked files included) and `Repo.IndexTree(todoRel)` for land (checked equal to `Snapshot("")`). Reconciliation acts only on that exact tree.
  - The other option was a digest of the todo alone. It misses an unrelated change staged or made after the crash, which `Commit` or `CommitAll` would silently include.
  - `Snapshot` already exists on the `Repo` port (`ports.go:68`, `repo.go:384`), and land already compares snapshots (`land.go:216-223`).
- **Resolving a merge left in progress:** complete it when a land `commit-intent` exists and both `Snapshot("")` and `IndexTree(todoRel)` equal its `tree`. Otherwise abort it. `IndexTree` is the tree `Commit(msg, todoRel)` would write (the real index plus the todo), so a change that exists only in the index is caught; `Snapshot` alone re-adds the working tree over the index and misses it. Before aborting, resume also refuses when the real index and the working tree differ at any path but the todo: `git merge --abort` resets the index, so such a staged copy would be lost.
  - This beats "always abort": a completed merge needs no gate re-run, and the tree match proves that nothing but r-loop's merge and tick is in the primary tree.
  - "Always complete" is not possible, because the gate result is unknown until that intent is written.
- **Recovering the landing SHA after the commit:** add a new concrete method `gitrepo.Repo.LandedCommit(base, tip, tree)`. It returns the first first-parent commit after `base` whose parents are exactly `base` and `tip` and whose tree is `tree`. The other options fail:
  - matching on the commit subject accepts an unrelated commit that happens to have the same message;
  - trusting `HEAD` breaks if the maintainer committed after the crash.
- **Telling r-loop's `MERGE_HEAD` from a foreign one:** compare `RevParse("MERGE_HEAD")` with `RevParse(<recorded branch>)` and `HEAD` with the recorded base. This beats recording the tip in the intent: the branch still exists at resume, and it needs no extra `HeadSHA` on the worktree during land.
- **Where reconciliation lives:** `internal/app/resume.go`, on the concrete `*gitrepo.Repo` and `*store.Store` that `PrepareResume` already holds. The other option was a core function behind new `Repo` port methods (`RevParse`, `LandedCommit`), which would mean growing the port and its fakes for a resume-only need. `IndexTree` is the one exception that joins the port, because land itself needs it (next choice).
- **Making land's commit hold exactly its intent's tree:** land records `IndexTree(todoRel)`, the tree `Commit(msg, todoRel)` writes, as the `commit-intent` tree, and refuses (abort, restore the todo, `ErrDirtyTree`) when it differs from `Snapshot("")`. The other option, recording `Snapshot("")` as before, lets a change staged apart from the working tree during the gate slip into the commit, so the committed tree would differ from the recorded one and `LandedCommit` could not recover the landing. The app adapter tests use real git, which is where a real `MERGE_HEAD` exists.
- **How the latch reaches every writer:** one `RecordGuard` built in `Wire` and passed as `Store` to the `SessionManager`, the `LandGate` and the `RunLoop`. The other option was threading `ErrRecord` through every return path of the session manager, the land gate and the question goroutine. The decorator gives one policy function and one latch that sees every fatal append, whichever goroutine makes it.
- **How the app's pre-loop run records fail:** one helper, `Wiring.recordFatal`, that emits the face `error` and returns `exit(2, "record: …")`. `fail` prints that on stderr. The other option was routing these writes through the latch, but then `Execute` would print the error a second time.
- **Exit code of a record halt:** 2, the code app code already uses for store errors (`resume.go:206`, `unblock.go:36`, `triage.go:142`). The alternative was 1, a blocked phase, but that would make the failure look like a failed step.

Policy (#11):

| Record | Policy | Why |
|---|---|---|
| step | fatal | resume reads it to decide what to re-run |
| run | fatal | resume and status read it |
| landing | fatal | resume reads it to decide what already landed |
| resume-read events: `merge-intent`, `commit-intent`, `run-list`, `step`, `baseline`, `snapshot`, `review-round`, `agent-named`, `restart`, `item-skipped`, `gate-discovered` | fatal | resume decides from them:<br>• scope: `resume.go:153`<br>• stopped step and agents: `resume.go:338`, `resume.go:240`<br>• claim and baseline: `resume.go:348`, `session.go:626`<br>• review round: `loop.go:473`<br>• restart count: `loop.go:143`<br>• skips: `loop.go:376`<br>• gate command: `gate.go:51` |
| every other event | warn | display, report and audit only |
| question | warn | the live question keeps being served, and a lost record only costs resume a withdrawal |
| signal | warn | the halt or warn still acts live |
| remedy | warn | `Remedies.decide` already refuses the restart on error (`remedies.go:107`) |

## Obligations

- O1 (#10 c1): a crash between a step's worktree commit and its `ok` record does not make resume re-run that step.
- O2 (#10 c2): a crash after the land merge and before the landing record leaves a record, written before the merge, that names the phase and the merge in progress. Resume detects it, completes or aborts the merge, and says which.
- O3 (#10 c3): a crash after the landing commit and before the landing record still shows the phase as landed, both on resume and in `report.md`, with its merge SHA recovered.
- O4 (#10 c4): in the normal path the order is intent → action → outcome. A recording fake store and repo assert this for the step commit and for land.
- O5 (#11 c1): when a step-state append (`running`, `stalled`, `waiting-input`) fails, the step ends `failed` or the run halts, with a `record:` reason.
- O6 (#11 c2): when a run-status append fails, the run halts with a non-zero exit and names the store error on stderr and in the face. This covers the loop's appends and the app's appends before the loop (`unblock.go:35`, `unblock.go:154`, `triage.go:115`, `triage.go:141`).
- O7 (#11 c3): with a store that fails every append, the run stops within one step. Nothing more is spawned, merged or committed.
- O8 (#11 c4): the policy for each record kind is decided and a test covers it. Every event resume reads is fatal (`run-list` written at `triage.go:234`, `step` written at `loop.go:1311`).
- O9 (watchdog warning 1): resume reconciles a `MERGE_HEAD` that matches this run's `merge-intent` before `w.clean()`. A foreign `MERGE_HEAD` still exits 4.
- O10 (watchdog warning 2): recovering a landing runs before the ticked-phase filter at `resume.go:96-97`.
- O11 (spec invariant: a transition is appended before the action it describes; tech-design `Store` contract): `tech-design.md` states the intent records, the reconciliation and the policy.
- O12 (phase 3 invariant: resume never discards or commits an uncommitted maintainer edit). Recovery commits, or records as landed, only the exact tree the intent recorded. An aborted merge never overwrites a todo it cannot prove is r-loop's own tick: resume refuses and names the file instead.
- O13 (edge case: a merge-intent with no merge on disk and no land commit-intent, i.e. a crash before the merge, or after a gate failure already aborted it). Resume does nothing, and the phase lands again.
- O14 (edge case: a commit-intent was recorded, the worktree `HEAD` still equals the recorded `head`, and its snapshot equals the recorded `tree`, i.e. a crash before `CommitAll`). Resume commits the step's work with the recorded message and records the step `ok`.
- O15 (edge case: the only phase left in the run list was recovered as landed). Resume exits 2 with "landed every phase", as it does today, and rewrites `report.md` first, so the report shows the recovered landing.
- O16 (edge case: the step's worktree changed after the crash, either through a new edit or through a commit whose tree is not the recorded one). Resume exits 2 naming the changed paths, and records nothing for the step.

## Changes

1. **`internal/core/ports.go` and `internal/core/fakes_test.go` — modify.** Serves O3, O12.
   - Add `IndexTree(paths ...string) (string, error)` to the `Repo` interface (`ports.go:55-78`), after `Snapshot` (`ports.go:68`).
   - `fakeRepo.IndexTree` records `Repo.IndexTree <paths joined by space>` and returns `f.Index, f.Err` when the new field `Index string` is set, else `f.Tree, f.Err`. It follows `fakeRepo.Snapshot` (`fakes_test.go:228-231`).

2. **`internal/core/records.go` — create.** Serves O7, O8, O4.
   - `const EventMergeIntent = "merge-intent"` and `const EventCommitIntent = "commit-intent"`. The existing exported event-kind constant `TriageSkipped` is the pattern.
   - `var resumeEvents = map[string]bool{EventMergeIntent: true, EventCommitIntent: true, "run-list": true, "step": true, "baseline": true, "snapshot": true, "review-round": true, "agent-named": true, "restart": true, "item-skipped": true, gateDiscovered: true}`. `gateDiscovered` is the constant at `gate.go:25`.
   - `func FatalRecord(rec Record) bool` returns true for `RecordStep`, `RecordRun` and `RecordLanding`, and for a `RecordEvent` whose `Event` is non-nil and whose `Event.Kind` is in `resumeEvents`. It returns false for every other case.
   - `type RecordGuard struct { Store; mu sync.Mutex; err error }` has:
     - `Append(runID string, rec Record) error`, which calls the inner `Append`. When that fails and `FatalRecord(rec)` is true, it latches the first such error. It always returns the inner error unchanged.
     - `Failed() error`, which returns the latched error under `mu`.
   - `func recordFailed(s Store) error` returns `g.Failed()` when `s` is a `*RecordGuard`, else nil.

3. **`internal/core/session.go` — modify.** Serves O1, O4, O5, O7, O12, O14.
   - `Spawn` (`session.go:134`): as the first statement, `if err := recordFailed(m.Store); err != nil { return s, fmt.Errorf("record: %w", err) }`, before `AddWorktree`.
   - `recordState` (`session.go:442`) now returns `error`. It returns nil for a reviewer, else the result of `m.recordAt`.
   - `tick` (`session.go:352`): at both calls, the `StepRunning` record when a stalled agent works again (`session.go:386`) and the `StepStalled` record (`session.go:404`):

     ```go
     if err := m.recordState(now, s, ...); err != nil && !errors.Is(err, errStepEnded) {
         return m.fail(s, "record: "+err.Error()), true
     }
     ```

     Keep the existing observer calls after it.
   - `Finish` (`session.go:556`) replaces the block at `session.go:563-567` with the following, kept under the same condition `out.State == StepOK && !done && !s.Ref.InPrimary && !s.Ref.KeepUncommitted`:
     1. `if err := recordFailed(m.Store); err != nil`, then `out.State, out.Reason = StepFailed, "record: "+err.Error()`.
     2. Otherwise `head, err := m.Repo.HeadSHA(s.Dir)`, then `tree, err := m.Repo.Snapshot(s.Dir)`. Either error gives `StepFailed`, `"commit: "+err.Error()`.
     3. Otherwise `msg := fmt.Sprintf("r-loop: phase %s %s", key.Phase, key.Kind)`, then `m.event(m.now(), s, Event{Kind: EventCommitIntent, Fields: map[string]string{"attempt": strconv.Itoa(key.Attempt), "head": head, "tree": tree, "dir": s.Dir, "message": msg}})`. On error: `StepFailed`, `"record: "+err.Error()`, and no commit.
     4. Otherwise `m.Repo.CommitAll(s.Dir, msg)`, with the existing `"commit: "` failure handling.

     The rest of `Finish` stays as it is: the snapshot on a non-ok outcome, then the terminal record.

4. **`internal/core/land.go` — modify.** Serves O2, O3, O4, O7, O9, O12.
   - At the start of `attempt` (`land.go:154`): `if err := recordFailed(g.Store); err != nil { return Landing{}, "", "", fmt.Errorf("record: %w", err) }`.
   - Right before `g.Repo.MergeNoFF` (`land.go:178`):
     0. `if err := recordFailed(g.Store); err != nil { return Landing{}, "", "", fmt.Errorf("record: %w", err) }`. This catches a fatal `gate-discovered` event that `Suite.Command` (`land.go:163`) appended through `recordEvent`, which hides the error (`gate.go:60`, `land.go:361-365`). The `GateProbe`'s `Sessions` is the guarded session manager (`wire.go:424-425`), so its latch is `g.Store`'s latch.
     1. `base, err := g.Repo.HeadSHA("")`. Return the error.
     2. `message := fmt.Sprintf("phase %s: %s", n, phase.Title)`, which is the same string as the `Commit` at `land.go:231`. That call now uses `message`.
     3. Append `Record{Kind: RecordEvent, Event: &Event{Kind: EventMergeIntent, Phase: n, Step: "land", Fields: {"phase": n, "branch": "r-loop/phase-"+n, "base": base, "message": message}}}` straight to `g.Store`, with `At` set on both the record and the event. It never goes to the face. On error: `return ..., fmt.Errorf("record: %w", err)`, with nothing merged.
   - After `g.Plan.Tick` succeeds (`land.go:228-230`) and before `g.Repo.Commit`:
     1. `tree, err := g.Repo.IndexTree(g.todoRel())`, the tree `Commit` will write, and `now, err := g.Repo.Snapshot("")`, the merged and ticked working tree. When `tree != now`, get `paths, _ := g.Repo.TreeDiff(tree, now)` and fail with `fmt.Errorf("%w: staged apart from the working tree: %s", ErrDirtyTree, strings.Join(paths, ", "))`, with the cleanup of step 3 and no commit. This follows the gate-change check at `land.go:216-223`.
     2. Append the event `{Kind: EventCommitIntent, Phase: n, Step: "land", Fields: {"phase": n, "tree": tree, "gateSkipped": strconv.FormatBool(landing.GateSkipped), "added": strconv.Itoa(landing.Added), "deleted": strconv.Itoa(landing.Deleted)}}`.
     3. When any step fails, return `errors.Join(fmt.Errorf("record: %w", err), os.WriteFile(todoAbs, before, 0o644), g.Repo.AbortMerge())`. This is the same cleanup as the tick-failure path at `land.go:229`.
   - The landing append at `land.go:145` is unchanged. It is the outcome record.

5. **`internal/core/milestone.go` — modify.** Serves O7.
   - In `report` (`milestone.go:62`), right after `if out.State != StepOK { … }` and before `b.Repo.Commit` (`milestone.go:89`): `if err := recordFailed(b.Sessions.Store); err != nil { return "record: " + err.Error() }`. This catches a fatal `step` event from `rec.finished` (`milestone.go:85`, `land.go:382-386`) that failed after the `ok` step record succeeded. `After` then runs `restore()` and records `report-skipped`, as for any other failed report (`milestone.go:38-43`).

6. **`internal/core/loop.go` — modify.** Serves O5, O6, O7, O8.
   - New method:

     ```go
     func (l *RunLoop) recordHalt(err error, phase, step string) int
     ```

     With `reason := "record: " + err.Error()`, it:
     1. calls `l.Face.Emit(Event{At: time.Now(), Kind: "error", Phase: phase, Step: step, Fields: map[string]string{"reason": reason}})` straight to the face, not through `l.emit`, whose own append would fail;
     2. calls `l.setRun(RunHalted, reason)`, best effort;
     3. calls `l.fire(l.Hooks.OnHalt, "halted", phase, step, reason)`;
     4. returns 2.
   - `Run` (`loop.go:124`):
     - After `l.setRun(RunRunning, "")` (`loop.go:158`): `if err := recordFailed(l.Store); err != nil { return l.recordHalt(err, "", "") }`.
     - Right after `step, out, aborted := l.runPhase(...)` (`loop.go:172`) and before `if aborted`: `if err := recordFailed(l.Store); err != nil { return l.recordHalt(err, ph.ID, step) }`.
     - After `l.setRun(RunFinished, "")` (`loop.go:205`), and after `l.setRun(RunHalted, firstReason)` (`loop.go:210`): the same check, returning `l.recordHalt(err, firstPhase, firstStep)` before the finished or halt event and its hook.
   - `runPhase`: right before `lander.Land` is first called (`loop.go:340`), `if err := recordFailed(l.Store); err != nil { return "land", Outcome{State: StepFailed, Reason: "record: " + err.Error(), Session: last}, false }`.
   - `runStep` ticker case (`loop.go:823`): before the `Aborted` check, add:

     ```go
     if err := recordFailed(l.Store); err != nil {
         if live := l.liveSession(); live != nil { l.Sessions.Stop(live) }
         cancel()
         out := <-done
         out.State, out.Reason, out.Stalled = StepFailed, "record: "+err.Error(), false
         l.emitStep(ref, out.State, out.Reason, out.Session)
         return l.ended(ref, out)
     }
     ```

     This ends a live step after a fatal append failed in another goroutine. That covers:
     - its `waiting-input` or `running` record from the question goroutine (`loop.go:920`, `loop.go:1091`);
     - a `step` event from `emitFields` (`loop.go:1311`).
   - `awaitRestart` (`loop.go:507`): the guard becomes `if l.RemedyWindow <= 0 || out.Halted || recordFailed(l.Store) != nil`.

7. **`internal/gitrepo/repo.go` — modify.** `RevParse` and `LandedCommit` are on the concrete adapter only; `IndexTree` implements the new `Repo` port method. Serves O2, O3, O9, O12.
   - `func (r *Repo) RevParse(ref string) (string, error)`: `git rev-parse --verify -q <ref>^{commit}`, trimmed. It follows `HeadSHA` at `repo.go:156`.
   - `func (r *Repo) IndexTree(paths ...string) (string, error)`: the tree the real index would hold after `git add -- <paths>` in the primary tree, without touching the real index. Extract the body of `Snapshot` (`repo.go:384-406`) into `func (r *Repo) tempTree(dir string, add ...string) (string, error)`, which seeds a temporary index from the real one (`seedIndex`, `repo.go:408`), runs `git <add…>` against it and returns `write-tree`. `Snapshot(dir)` becomes `r.tempTree(dir, "add", "-A")`, and `IndexTree(paths...)` becomes `r.tempTree("", append([]string{"add", "--"}, paths...)...)`. `tempTree` skips the add when it gets no arguments, so `IndexTree()` returns the real index's tree as it is. These are the same add arguments `Commit` uses (`repo.go:512-515`).
   - `func (r *Repo) LandedCommit(base, tip, tree string) (string, error)`: `git log --first-parent --reverse --format=%H%x00%P%x00%T <base>..HEAD`. It returns the SHA of the first line whose parents are exactly `base tip` and whose tree is `tree`, or `""` when none matches.

8. **`internal/app/resume.go` — modify.** Serves O1–O3, O9, O10, O12–O16.
   - New function:

     ```go
     func reconcileLand(repo *gitrepo.Repo, st *store.Store, run core.RunState, todoAbs string, out io.Writer) error
     ```

     1. Let `mi` be the last event in `run.Events` of kind `core.EventMergeIntent`. Return nil when there is none, or when `run.Landed` already has `mi.Phase`.
     2. Let `ci` be the last event after `mi` of kind `core.EventCommitIntent` with `Step == "land"` and `Phase == mi.Phase`. It may be absent.
     3. Work out `todoRel` as `filepath.Rel` of the `EvalSymlinks` of the repo root and of `todoAbs`, passed through `ToSlash`. This mirrors `LandGate.todoRel` (`land.go:305`).
     4. Call `repo.MergeInProgress()`. An error returns `exit(2, "%v", err)`.
     5. **When a merge is in progress:**
        1. Get `mh := RevParse("MERGE_HEAD")`, `tip := RevParse(mi.Fields["branch"])` and `head := repo.HeadSHA("")`. Any error returns `exit(2, …)`.
        2. When `mh != tip || head != mi.Fields["base"]`, return nil. It is a foreign merge, and `w.clean()` refuses it with exit 4 as before.
        3. **Complete** when `ci` is present, `repo.Snapshot("") == ci.Fields["tree"]` and `repo.IndexTree(todoRel) == ci.Fields["tree"]`:
           1. `sha, err := repo.Commit(context.Background(), mi.Fields["message"], todoRel)`. An error returns `exit(4, "complete phase %s's unfinished merge: %v", …)`.
           2. Append `core.Record{Kind: core.RecordLanding, At: time.Now(), Landing: &core.Landing{Phase: mi.Phase, MergeSHA: sha, GateSkipped: ci.Fields["gateSkipped"] == "true", Added: atoi(ci.Fields["added"]), Deleted: atoi(ci.Fields["deleted"])}}`. An error returns `exit(2, …)`.
           3. Print `completed phase <N>'s merge the crash left unfinished: landed as <sha[:7]>`.
           4. A `Snapshot` or `IndexTree` error returns `exit(2, …)`.
        4. **Otherwise abort:**
           1. First get `idx := repo.IndexTree()`, `wt := repo.Snapshot("")` and `paths := repo.TreeDiff(idx, wt)`, and drop `todoRel` from `paths`. Any error returns `exit(2, …)`. When `paths` is not empty, return `exit(4, "phase %s's unfinished merge: the index and the working tree differ at %s, and git merge --abort would lose the staged copy; stage or unstage them, then resume", mi.Phase, strings.Join(paths, ", "))` without aborting, so `MERGE_HEAD` and the index are kept.
           2. `repo.AbortMerge()`. An error returns `exit(4, "%v", err)`.
           3. Print `aborted phase <N>'s merge the crash left unfinished; the phase lands again`.
           4. When `repo.Dirty("")` then contains `todoRel`, return `exit(4, "phase %s's aborted merge left %s changed (it may hold the phase's tick); check it, restore it with git checkout -- %s, then resume", …)`.
           5. Any other leftover path, such as a maintainer's change staged after the crash, which `git merge --abort` keeps as an unstaged change, reaches `w.clean()`. There it exits 4 naming the path.
     6. **When no merge is in progress:** return nil when `ci` is absent (O13). Otherwise:
        1. `tip := RevParse(mi.Fields["branch"])`, then `sha, err := repo.LandedCommit(mi.Fields["base"], tip, ci.Fields["tree"])`. An error returns `exit(2, …)`, and `sha == ""` returns nil.
        2. Append the same `Landing`, built from `ci`.
        3. Print `phase <N> landed as <sha[:7]> before the crash; its landing is now recorded`.
   - `PrepareResume`: after the `RunFinished` check (`resume.go:72-74`) and before `Wire` (`resume.go:75`):
     1. `todoAbs := run.Todo`, joined onto `env.Dir` when it is relative. This is the same rule as `Wire` at `wire.go:359-362`.
     2. `if err := reconcileLand(repo, st, run, todoAbs, env.Stdout); err != nil { return nil, core.RunOptions{}, err }`.
     3. Then `if run, err = st.Load(id); err != nil { return nil, core.RunOptions{}, exit(2, "load run %s: %v", id, err) }`.
   - `resume` (`resume.go:98-100`): before returning "nothing to resume: … landed every phase", write `core.Report(run, w.Plan)` to `filepath.Join(w.Store.Dir(id), "report.md")`. A write error returns `exit(2, "%v", err)`. This follows `loop.go:1410` (O15).
   - `closeInterrupted` (`resume.go:265`): for each open key, look up `ci`, the last event of kind `core.EventCommitIntent` with `Phase == key.Phase`, `Step == key.Kind` and `Fields["attempt"] == strconv.Itoa(key.Attempt)`. When `ci` is present, call the new method `w.recoverCommit(run.ID, key, ci)` and move on to the next key. Otherwise keep the existing `failed(interrupted: driver died)` record.
   - New method:

     ```go
     func (w *Wiring) recoverCommit(runID string, key core.StepKey, ci core.Event) error
     ```

     With `dir := ci.Fields["dir"]`:
     1. `now, err := w.Repo.Snapshot(dir)`, with `exit(2, …)` on error.
     2. When `now != ci.Fields["tree"]`, get `paths, _ := w.Repo.TreeDiff(ci.Fields["tree"], now)` and return `exit(2, "phase %s %s: the worktree changed after the crash (%s); commit or discard those changes, then resume", key.Phase, key.Kind, strings.Join(paths, ", "))`. This is O16, and it follows `claim` at `resume.go:307-320`.
     3. `head, err := w.Repo.HeadSHA(dir)`, with `exit(2, …)` on error.
     4. When `head == ci.Fields["head"]`: `sha, err := w.Repo.CommitAll(dir, ci.Fields["message"])`, with `exit(2, …)` on error, then print `phase <N> <kind>: committed the work the crash left uncommitted (<sha[:7]>)`.
     5. Otherwise get `paths, err := w.Repo.TreeDiff(head, ci.Fields["tree"])`, with `exit(2, …)` on error. Worktrees share the object store, so the primary repo resolves the worktree's `head`. When `paths` is not empty, `HEAD` does not hold the recorded work (for example, an empty or unrelated commit was made on top of still-uncommitted work): return `exit(2, "phase %s %s: HEAD %s does not hold the work the crash left (%s); commit or discard it, then resume", key.Phase, key.Kind, head[:7], strings.Join(paths, ", "))`, recording nothing. Otherwise print `phase <N> <kind>: its work was committed before the crash (<head[:7]>)`. With the snapshot match from step 2, an empty diff proves that `HEAD`'s tree is the recorded tree and nothing is left uncommitted.
     6. Then append `core.Record{Kind: core.RecordStep, At: time.Now(), Step: &key, State: core.StepOK}`, with `exit(2, …)` on error.

9. **`internal/app/wire.go` — modify.** Serves O6, O7.
   - Add the field `records *core.RecordGuard` to `Wiring`. In `Wire`, set `w.records = &core.RecordGuard{Store: w.Store}` right after `w.Store` is built (`wire.go:378`).
   - Pass `w.records` as `Store` to the `SessionManager` (`wire.go:407`), the `LandGate` (`wire.go:435`) and the `RunLoop` (`wire.go:459`). Watch, Remedies, Dog and Ask keep `w.Store`: they write only warn kinds.
   - In `Execute`, after `w.Face.Close()` (`wire.go:305`): `if err := w.records.Failed(); err != nil { fmt.Fprintf(w.Env.Stderr, "r-loop: record: %v\n", err) }`. The print comes after the TUI has restored the terminal.

10. **`internal/app/unblock.go` — modify.** Serves O6, O8.
   - New method:

     ```go
     func (w *Wiring) recordFatal(rec core.Record) error
     ```

     1. It appends through `w.records.Store.Append(w.Loop.RunID, rec)`, the inner store, so the loop's latch and `Execute`'s print stay loop-only.
     2. On error it calls `w.Face.Emit(core.Event{At: time.Now(), Kind: "error", Fields: map[string]string{"reason": "record: " + err.Error()}})` and returns `exit(2, "record: %v", err)`.
     3. It follows `Wiring.record` at `unblock.go:144`.
   - `unblock.go:35` becomes `if err := w.recordFatal(core.Record{Kind: core.RecordRun, At: time.Now(), Run: core.RunFinished}); err != nil { return nil, nil, false, err }`.
   - `halt` (`unblock.go:152`): `if rerr := w.recordFatal(<the halted record>); rerr != nil { return errors.Join(rerr, err) }`, else `return err`. The record error goes first, because `fail` takes the exit code from the first `*ExitError` that `errors.As` finds (`wire.go:251-256`). A halt whose record failed therefore exits 2, not the halt's own 4 or 5, and stderr still carries both messages.

11. **`internal/app/triage.go` — modify.** Serves O6, O8.
   - `abortTriage` (`triage.go:115`): the halted-aborted append goes through `recordFatal`. When that fails, return its error.
   - `startAfterTriage` (`triage.go:141`): the finished append goes through `recordFatal`. When that fails, return `opts, false, err`.
   - `recordRunList` (`triage.go:227`) now returns `error`. It builds the event with `At = time.Now()`, appends it through `w.recordFatal(core.Record{Kind: core.RecordEvent, At: ev.At, Event: &ev})`, returns that error, and only on success calls `w.Face.Emit(ev)`.
   - Its callers at `triage.go:40` and `triage.go:138` return `opts, false, err` when it fails.

12. **`docs/task-loop-driver/tech-design.md` — modify.** Serves O11.
    - `Store` port (`tech-design.md:131-132`): add the policy table from `## Summary` and the `RecordGuard` latch. A fatal failure means no further spawn, merge or commit, the run halted with `record: <err>` in the face and on stderr, and exit 2.
    - Land (`tech-design.md:370-375`): add `merge-intent{phase, branch, base, message}`, appended before `MergeNoFF`, and `commit-intent{phase, tree, gateSkipped, added, deleted}`, appended after `Tick` and before `Commit`.
    - Session manager (`tech-design.md:284-295`): add the step `commit-intent{attempt, head, tree, dir, message}`, appended before `CommitAll`.
    - Resume (`tech-design.md:343-353`): add `reconcileLand` (complete on a tree match, otherwise abort, or record a landing whose parents and tree match; each printed; a foreign `MERGE_HEAD` still exits 4) and the step commit recovery, which is gated on the recorded tree.
    - Exit codes (`tech-design.md:341-344`): add "a fatal record failure → 2".

## Tests

Core tests go in `internal/core/records_test.go`, `package core`. They reuse `newRig` (`session_test.go:68`), `newLoopRig` (`loop_test.go:149`), `fakeRepo`, `fakeStore` and `callLog.Shared` (`fakes_test.go`). Each wraps the fake store as `&RecordGuard{Store: r.store}` and assigns the guard to every `Store` field it uses. Two test-local store wrappers are defined in that file:

- `orderStore`, which logs `append <record kind>/<event kind or step state>` into the shared `callLog`;
- `failingStore{Store; fail func(Record) bool}`, which returns `errors.New("disk full")` whenever `fail(rec)` is true.

The app tests go in `internal/app/resume_test.go` unless named otherwise, and use `newResumeFixture`, `seedKilledImplement` (`resume_test.go:916`), `ev` (`status_test.go:102`) and the `git` helper. They inject a crash by seeding the store and the git state exactly as a killed driver leaves them. The app tests that need a failing store use a test-local `failingStore{core.Store}` whose `Append` returns `errors.New("disk full")` for the record they name.

- `TestFatalRecordPolicyForEveryRecordKind`: a table.
  - Fatal: `step`, `run`, `landing`, and the events `merge-intent`, `commit-intent`, `run-list`, `step`, `baseline`, `snapshot`, `review-round`, `agent-named`, `restart`, `item-skipped` and `gate-discovered`.
  - Warn: `question`, `signal`, `remedy`, and the events `warning`, `phase-blocked` and `nudge`.

  It asserts `FatalRecord`. It also asserts that a `RecordGuard` over a failing store returns every error but latches only the fatal ones, and keeps the first. Covers O8.
- `TestFinishRecordsTheCommitIntentThenCommitsThenRecordsOk`: an ok step through `Finish`, with a shared log. It asserts the order `append event/commit-intent` → `Repo.CommitAll <dir> r-loop: phase 3 implement` → `append step/ok`, and that the intent carries `attempt`, `head`, `tree`, `dir` and `message`. Covers O4.
- `TestFinishCommitsNothingWhenTheCommitIntentCannotBeRecorded`: the store fails only the `commit-intent` event. The result is `failed`, `"record: disk full"`, with no `Repo.CommitAll` call. Covers O7 and O1.
- `TestLandRecordsMergeIntentMergesThenCommitIntentCommitsThenLanding`: a `LandGate` over `fakeRepo` with `Touched = {todoRel, "a.go"}`, `DoneWhen "`true`"`, a todo file under `RootDir` and `fakePlanSource`. It asserts the order `append event/merge-intent` → `Repo.MergeNoFF r-loop/phase-1` → `append event/commit-intent` → `Repo.Commit "phase 1: …"` → `append landing`. It also asserts the intent fields `branch`, `base`, `message` and `tree`. Covers O4.
- `TestLandMergesNothingWhenTheMergeIntentCannotBeRecorded`: the store fails the `merge-intent`. `Land` returns an error starting `record:`, with no `Repo.MergeNoFF` call. Covers O7.
- `TestLandMergesNothingWhenGateDiscoveryCannotBeRecorded`: a `LandGate` over `fakeRepo` for a phase with an empty `DoneWhen` and a test-local `Suite` whose `Command` appends `Event{Kind: gateDiscovered}` through `recordEvent(guard, nil, "run1", …)` and returns `"true"`. The guard wraps a `failingStore` that fails only `event/gate-discovered`. `Land` returns an error starting `record:`, no `merge-intent` is appended, and there is no `Repo.MergeNoFF` or `Repo.Commit` call. Covers O7.
- `TestLandUndoesTheTickWhenItsCommitIntentCannotBeRecorded`: the store fails the land `commit-intent`. Asserts no `Repo.Commit`, `Repo.AbortMerge` called, and the todo bytes restored. Covers O7 and O12.
- `TestAStalledRecordThatCannotBeAppendedEndsTheStepFailed`: follows `TestIdleForTheGraceStallsAndNudgesOnceThenWorkingResumes` (`session_test.go:787`). The store fails the `stalled` step record. `Wait` returns `failed` with `"record: disk full"`, and no nudge is prompted. Covers O5.
- `TestAResumedRunningRecordThatCannotBeAppendedEndsTheStepFailed`: stall first, then the agent works again. The store fails the second `running` record. The result is `failed`, `"record: disk full"`. Covers O5.
- `TestAWaitingInputRecordThatCannotBeAppendedHaltsTheRunWithARecordReason`: follows `TestABackstopInWaitingInputHaltsTheRunOnTheInvariant` (`loop_events_test.go:651`). The store fails the `waiting-input` step record. The live agent is interrupted, the step ends `failed` with `record: disk full`, `Run` returns 2, there is no `Land`, and a face `error` event has the reason `record: disk full`. Covers O5 and O7.
- `TestAStepEventThatCannotBeAppendedHaltsTheRun`: the store fails only `event/step` records, from phase 1's implement onward. Asserts no `Repo.CommitAll` for implement, no `Land`, nothing spawned for phase 2, `Run` returns 2, and an `error` event names `record: disk full`. Covers O8 and O7.
- `TestRunHaltsWhenTheRunningRunRecordCannotBeAppended`: the store fails every append. `Run` returns 2, there are no `Host.Open`, no `Repo.CommitAll` and no `Land` calls, the face has an `error` event with `record: disk full`, and the `halt-hook` fires. Covers O6 and O7.
- `TestRunStopsWithinOneStepWhenTheStoreStartsFailingMidPhase`: the store fails every append from phase 1's implement `spawned` record onward. There is exactly one `Host.Open` (the plan), no `Repo.CommitAll` for implement, no `Land`, and nothing is spawned for phases 2 or 3. `Run` returns 2. Covers O7.
- `TestRunExitsNonZeroWhenTheFinishedRunRecordCannotBeAppended`: the store fails only the `run/finished` record. `Run` returns 2, not 0, there is an `error` event, and no `finished` event or `done-hook`. Covers O6.
- `TestSpawnRefusesOnceAFatalRecordHasFailed`: latch the guard, then call `Spawn`. It fails with `record: disk full` and makes no `Repo.AddWorktree` or `Host.Open` call. Covers O7.
- `TestLandRefusesAChangeStagedApartFromTheWorkingTreeDuringTheGate`, in `internal/core/land_test.go`: real git through `newLandEnv`, `e.phaseWork(1, "one.txt", "1\n")`, and `phaseOne` with `DoneWhen` `` `git show :one.txt > .o && printf 'x\n' >> one.txt && git add one.txt && mv .o one.txt` ``, which leaves an extra line staged for `one.txt` while its working-tree copy matches the merge. `Land` returns an error that `errors.Is` `ErrDirtyTree` and names `one.txt`. `HEAD` is still the base, `MERGE_HEAD` is gone, the todo is not ticked, no landing and no land `commit-intent` are recorded. Covers O3 and O12.
- `TestMilestoneReportCommitsNothingOnceAStepRecordHasFailed`, in `internal/core/land_test.go`: follows `TestMilestoneReportSpawnedOnlyAfterTheLastPhaseInThePrimaryTree` (`land_test.go:986`). After `Land` of phase 1, set `g.Boundary.Sessions.Store = &core.RecordGuard{Store: failStepEvents{e.store}}`, a test-local wrapper that returns `errors.New("disk full")` for every `RecordEvent` whose `Event.Kind` is `step` and whose `Event.Step` is `milestone`. After `Land` of phase 2, `HEAD`'s subject is not `docs(report): milestone 1`, `git status --porcelain` is empty, and one `report-skipped` event has a reason containing `record: disk full`. Covers O7.
- Four tests in `internal/gitrepo/repo_test.go`, covering O2, O3, O9 and O12:
  - `TestRevParseResolvesMergeHeadAndABranch`: after a real `merge --no-ff --no-commit`, `RevParse("MERGE_HEAD")` equals the branch tip.
  - `TestLandedCommitFindsTheMergeWithTheRecordedParentsAndTree`: it returns the landing merge even with a later commit on top.
  - `TestLandedCommitIgnoresASameSubjectCommitWithOtherParentsOrTree`: it returns `""` for a commit with the landing subject but a single parent, and for a merge of the same parents whose tree differs.
  - `TestIndexTreeKeepsAStagedOnlyChange`: a tracked file is changed and staged, then its working-tree copy is restored to `HEAD`'s. `Snapshot("")` equals `HEAD^{tree}`, `IndexTree()` does not, and `TreeDiff(IndexTree(), Snapshot(""))` names the file. The real index is unchanged (`git diff --cached --name-only` still names the file).
- `TestResumeAfterACrashBetweenTheStepCommitAndItsOkRecordDoesNotRerunIt`: seed as in `seedKilledImplement`, then commit the worktree and append a `commit-intent` whose `head` is the pre-commit SHA and whose `tree` is the pre-commit snapshot. Resume records implement a1 as `ok`, prompts no agent, lands phase 1 once, and prints "its work was committed before the crash". Covers O1.
- `TestResumeCommitsAStepsWorkWhenTheCrashCameBeforeItsCommit`: the same seed with the work left uncommitted, and `head` and `tree` matching the worktree. Resume commits it with `r-loop: phase 1 implement`, records `ok`, prompts no agent, and prints "committed the work the crash left uncommitted". Covers O14.
- `TestResumeRefusesAStepWhoseWorktreeChangedAfterTheCrash`: the intent was recorded for the uncommitted work, and then `extra.txt` was written. Resume exits 2 naming `extra.txt`, records nothing `ok` for implement, and makes no commit. Covers O16 and O12.
- `TestResumeRefusesAStepWhoseHeadMovedToAnUnrelatedCommit`: the intent was recorded, and then a different change was committed in the worktree. Resume exits 2 naming the differing path, and the step is not recorded `ok`. Covers O16 and O12.
- `TestResumeRefusesAStepWhoseWorkIsStillUncommittedUnderAnEmptyCommit`: the intent was recorded for the uncommitted work, and then `git commit --allow-empty -m other` was run in the worktree, so `HEAD` moved while `Snapshot` still equals the recorded `tree`. Resume exits 2 with "does not hold the work the crash left" naming the step's changed path, makes no commit, and records nothing `ok` for implement. Covers O16, O12 and O1.
- `TestResumeAbortsAMergeTheCrashLeftDuringTheGate`: the seed has all steps `ok`, a `merge-intent` for phase 1, and a real `git merge --no-ff --no-commit r-loop/phase-1` in the primary tree. Resume prints "aborted phase 1's merge", leaves no `MERGE_HEAD`, and phase 1 lands again through the `landRecorder`. Covers O2 and O9.
- `TestResumeCompletesAMergeWhoseCommitWasIntended`: as above, plus the tick written and a land `commit-intent` with `tree` equal to `Snapshot("")`. Resume prints "completed phase 1's merge", `HEAD` is `phase 1: one` with two parents, `Landed` has that SHA, and the `landRecorder` is not called for phase 1. Covers O2.
- `TestResumeAbortsRatherThanCompleteAMergeWithAnUnrelatedStagedChange`: as above, but `other.txt` was staged after the intent. Resume does not commit, prints "aborted phase 1's merge", and exits 4 naming `other.txt`. `other.txt` keeps its content, and `HEAD` is still the base. Covers O12 and O2.
- `TestResumeRefusesAMergeWithAStagedOnlyChange`: as `TestResumeCompletesAMergeWhoseCommitWasIntended`, with a tracked file `notes.txt` committed on main before the merge. After the intent, `notes.txt` is changed and staged, and its working-tree copy restored, so `Snapshot("")` still equals the recorded `tree`. Resume exits 4 naming `notes.txt` and "git merge --abort would lose the staged copy". It makes no commit (`HEAD` is still the base), keeps `MERGE_HEAD`, keeps the staged change (`git diff --cached --name-only` names `notes.txt`), and records no landing for phase 1. Covers O12 and O2.
- `TestResumeRefusesAForeignMergeInProgress`: a `merge-intent` for phase 1, but `MERGE_HEAD` comes from another branch. Exit 4 with "unfinished merge", and `MERGE_HEAD` is kept. Covers O9.
- `TestResumeRefusesAnAbortedMergesUnprovenTodoChange`: a merge in progress and the todo ticked, but no land `commit-intent`. Resume aborts the merge, then exits 4 naming the todo and `git checkout --`, and leaves the todo's bytes unchanged. Covers O12.
- `TestResumeRecordsTheLandingOfACommitMadeBeforeTheCrash`: `run-list` `1,2`, a completed landing merge on main, and both land intents with the recorded `tree`, but no landing record. Resume prints "phase 1 landed as <sha> before the crash". `Landed[0].MergeSHA` equals that commit, phase 1 is not re-landed, phase 2 runs, and `report.md` contains `phase 1 <sha>`. Covers O3 and O10.
- `TestResumeOfARunWhoseLastPhaseLandedBeforeTheCrashReportsItLanded`: the same with `run-list` `1`. Resume exits 2 with "landed every phase", and `report.md` contains `phase 1 <sha>`. Covers O15 and O3.
- `TestResumeWithAMergeIntentButNoMergeLandsThePhaseAgain`: a `merge-intent` only, with a clean tree. Resume prints nothing about the merge, and phase 1 lands through the `landRecorder`. Covers O13.
- `TestRunNamesAFailedRunRecordOnStderrAndInTheFace`, in `internal/app/app_test.go`: preflight, then set `w.records = &core.RecordGuard{Store: failingStore{w.Store}}` and assign it to `w.Loop.Store`, `w.Loop.Sessions.Store` and `w.Gate.Store`. `Execute` returns 2, stderr contains `r-loop: record: disk full`, and the plain face output contains `!  record: disk full`. Covers O6.
- `TestAFinishedRunRecordThatFailsBeforeTheLoopIsNamedOnStderrAndInTheFace`, in `internal/app/unblock_test.go`: follows `TestARunWhoseEveryPhaseIsBlockedFinishesWithNothingToRun` (`unblock_test.go:209`), with `w.records.Store` replaced by a `failingStore` for `run/finished`. `Execute` returns 2, stderr has exactly one `r-loop: record: disk full` line, the face output has `!  record: disk full`, and no landing happens. Covers O6.
- `TestAHaltWhoseRunRecordFailsExitsTwoAndKeepsTheHaltReason`, in `internal/app/unblock_test.go`: follows `TestAStopDuringTheWalkHaltsTheRun` (`unblock_test.go:186`), with `w.records.Store` replaced by a `failingStore` for `run/halted`. `Execute` returns 2, not 4, and stderr contains both `record: disk full` and `stopped during the ## Resolve first walk`. Covers O6.
- `TestARunListThatCannotBeRecordedHaltsBeforeAnyStep`, in `internal/app/triage_test.go`: a `failingStore` for `event/run-list`. `Execute` returns 2, stderr and the face name `record: disk full`, and the sim host starts no agent. Covers O8.

## Left out

- A new `Record` kind or `StepState` for intents: events carry them, and nothing in the state machine needs a new state.
- A `CommitTouches` check when resume completes a merge: the `IndexTree` match proves that the commit will hold exactly the gate-passed merge and its tick.
- Verifying the completed commit's tree after `Commit`: the `IndexTree` check before it already equals what `Commit(msg, todoRel)` writes.
- The milestone report for a recovered or completed landing: no criterion asks for it.
- Removing the phase worktree after a recovered landing: no criterion asks for it.
- A `land-reconciled` event: the criteria ask resume to say which, and it prints that.
- A `RecordGuard` around Watch, Remedies, Dog and Ask: they append only warn kinds.
- A latch check in `runStep` before the `queued` record: `Spawn`'s check already stops the spawn.
- Extra plain-face handling: the `error` kind is already printed as `!  <reason>` (`plain.go:54`), and the TUI renders it (`model.go:165`).
- Fatal handling for `stale-interrupted`, `phase-blocked` and other display events: resume and the loop never decide from them.

## Assumptions

- **Watchdog warning 1 is resolved by O9.** `reconcileLand` runs before `w.clean()`. Only a `MERGE_HEAD` that equals the recorded branch tip, with `HEAD` at the recorded base, is completed or aborted. A foreign `MERGE_HEAD` reaches `clean()` and keeps its exit 4.
- **Watchdog warning 2 is resolved by O10.** Reconciliation runs in `PrepareResume` before `Wire` reads the todo. The recovered landing is therefore in the store before the ticked-phase filter drops the ticked phase.
- A crash between `Tick`'s write and the append of the land `commit-intent` leaves a tick nothing can prove. Resume aborts the merge and refuses, naming the todo (O12), and does not guess.
- The milestone report's commit and the unblock walk's commit get no `commit-intent`. The milestone report's `ok` step record and `step` event are already appended before its commit (`milestone.go:83-89`), so a crash between them leaves the report file uncommitted in the primary tree, and `w.clean()` on resume exits 4 naming it; resume never re-runs the report of a landed phase. The unblock commit (`unblock.go:93`) is followed only by `entry-resolved` events, which are warn and display-only, and a crash after it leaves the entries ticked in the committed todo, so nothing is re-run. Neither commit can follow a latched fatal failure: the milestone path checks the latch (Changes 5), and before the loop every fatal write goes through `recordFatal`, which returns at once.
- A record halt exits 2 and runs the `onHalt` hook with `R_LOOP_STATUS=halted`.
- A recovered or completed landing has an empty `GateOutput`. `GateSkipped`, `Added` and `Deleted` come from the land `commit-intent`.
- The step `commit-intent` records the step's absolute worktree `dir`. Resume commits there, and resume runs on the same machine.
- The existing test `TestFinishReportsFailureWhenTheOkStateCannotBeRecorded` (`session_test.go:970`) still passes unchanged. The failing store now fails at the intent, with the same `record: disk full` reason.

## Gate

`go test ./internal/core/ ./internal/gitrepo/ ./internal/app/ -run '^(TestFatalRecordPolicyForEveryRecordKind|TestFinishRecordsTheCommitIntentThenCommitsThenRecordsOk|TestFinishCommitsNothingWhenTheCommitIntentCannotBeRecorded|TestLandRecordsMergeIntentMergesThenCommitIntentCommitsThenLanding|TestLandMergesNothingWhenTheMergeIntentCannotBeRecorded|TestLandMergesNothingWhenGateDiscoveryCannotBeRecorded|TestLandUndoesTheTickWhenItsCommitIntentCannotBeRecorded|TestAStalledRecordThatCannotBeAppendedEndsTheStepFailed|TestAResumedRunningRecordThatCannotBeAppendedEndsTheStepFailed|TestAWaitingInputRecordThatCannotBeAppendedHaltsTheRunWithARecordReason|TestAStepEventThatCannotBeAppendedHaltsTheRun|TestRunHaltsWhenTheRunningRunRecordCannotBeAppended|TestRunStopsWithinOneStepWhenTheStoreStartsFailingMidPhase|TestRunExitsNonZeroWhenTheFinishedRunRecordCannotBeAppended|TestSpawnRefusesOnceAFatalRecordHasFailed|TestRevParseResolvesMergeHeadAndABranch|TestLandedCommitFindsTheMergeWithTheRecordedParentsAndTree|TestLandedCommitIgnoresASameSubjectCommitWithOtherParentsOrTree|TestIndexTreeKeepsAStagedOnlyChange|TestLandRefusesAChangeStagedApartFromTheWorkingTreeDuringTheGate|TestMilestoneReportCommitsNothingOnceAStepRecordHasFailed|TestResumeAfterACrashBetweenTheStepCommitAndItsOkRecordDoesNotRerunIt|TestResumeCommitsAStepsWorkWhenTheCrashCameBeforeItsCommit|TestResumeRefusesAStepWhoseWorktreeChangedAfterTheCrash|TestResumeRefusesAStepWhoseHeadMovedToAnUnrelatedCommit|TestResumeRefusesAStepWhoseWorkIsStillUncommittedUnderAnEmptyCommit|TestResumeAbortsAMergeTheCrashLeftDuringTheGate|TestResumeCompletesAMergeWhoseCommitWasIntended|TestResumeAbortsRatherThanCompleteAMergeWithAnUnrelatedStagedChange|TestResumeRefusesAMergeWithAStagedOnlyChange|TestResumeRefusesAForeignMergeInProgress|TestResumeRefusesAnAbortedMergesUnprovenTodoChange|TestResumeRecordsTheLandingOfACommitMadeBeforeTheCrash|TestResumeOfARunWhoseLastPhaseLandedBeforeTheCrashReportsItLanded|TestResumeWithAMergeIntentButNoMergeLandsThePhaseAgain|TestRunNamesAFailedRunRecordOnStderrAndInTheFace|TestAFinishedRunRecordThatFailsBeforeTheLoopIsNamedOnStderrAndInTheFace|TestAHaltWhoseRunRecordFailsExitsTwoAndKeepsTheHaltReason|TestARunListThatCannotBeRecordedHaltsBeforeAnyStep)$'`
