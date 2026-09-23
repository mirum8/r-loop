status: planned

## Summary

When a step's `HEAD` moved, the step is still failed. For a step in the primary checkout, though, the reason now depends on who made the new commits. Every step pane starts with `GIT_COMMITTER_NAME=r-loop <agent>`, so any commit its agent makes carries that name. When a primary-checkout step's `HEAD` is no longer its `StartSHA`, `judge` lists `StartSHA..HEAD` with `git log` through `Repo.Run`:

- If any listed commit has the step's own committer name, the step committed, and the reason stays today's `step committed before review`.
- Otherwise the reason is `HEAD moved from outside the step: <sha> <subject>, …`.

Worktree steps keep today's check and reason unchanged.

The gate step (the only primary-checkout step whose failure blocks a phase) now reaches the remedy window:

- `GateProbe.discover` returns a typed `*FailedStep` error.
- `RunLoop.runPhase` recognises it with `errors.As`. It then runs the same `ended` → `awaitRestart` path that pipeline steps use: `Hold`, `StepEnded`, the window, `restart_step`, and a new attempt key.
- It then calls `Land` again.

`awaitRestart` already records the addendum and provider in the `restart` event. The next `discover` reads them back from the store, so the new attempt runs with them.

Choices:

- **Commit attribution:** mark the step's commits with `GIT_COMMITTER_NAME` (the maintainer's answer to q2). This beat two alternatives:
  - Treating every move as external would misattribute a gate agent's real commit.
  - The file-overlap heuristic misreads the maintainer's `git commit -a`.
- **Scope of the new check:** `InPrimary` steps only. Worktree steps keep today's behaviour, as the maintainer decided, and nobody but the step commits in a step worktree.
- **How the loop learns that a gate step failed:** a typed error, `*FailedStep`, wrapped under `ErrNoGate`. It beat making the gate a pipeline step run by `runAttempts`. That would move gate discovery out of `LandGate.attempt`, which has to run it before the merge (`land.go:76-80`).
- **How a restart's addendum and provider reach the new gate attempt:** `discover` reads the `restart` event that `awaitRestart` already appends (`loop.go:466-470`). This beat a callback or a field on `GateProbe` set from the loop. The loop sees only the `Lander` interface, and the store is already the hand-off, so resume gets it for free.
- **Which gate failures get the window:** every failed gate step, as for pipeline steps (`runAttempts` holds on any failure). This beat holding only on the external-move reason, which would need a reason-string match or a second flag. The watchdog decides whether to restart.
- **Reason when `HEAD` moved but the range lists no commit** (a reset or checkout to a non-descendant): `HEAD moved from outside the step: HEAD is now <head[:7]>`. This beat reusing today's reason, because no step commit exists in the range.
- **`git log` failure:** the step fails with `head log: <err>`, or `head log: exit <code>: <output>`, the same shape as the `head: ` error at `session.go:452`. This beat silently falling back to either reason.

## Changes

1. **`internal/core/session.go` — modify.**
   - `Spawn` (`session.go:165-170`) adds `"GIT_COMMITTER_NAME": "r-loop " + s.Agent` to the `OpenSpec.Env` map. `s.Agent` is set at `session.go:155`, before `Open`. Serves the attribution rule: a step's own commit is recognisable.
   - `judge` (`session.go:454-456`): when `head != s.StartSHA`:
     - If `!s.Ref.InPrimary`, it returns `m.fail(s, "step committed before review")` exactly as today.
     - Otherwise it returns `m.fail(s, m.headMoved(s, head))`.
   - New method `func (m *SessionManager) headMoved(s *Session, head string) string`:
     - It runs `m.Repo.Run(s.Dir, "git log --no-color --format=%h%x09%cn%x09%s "+shellQuote(s.StartSHA+".."+head), time.Minute)`. `shellQuote` is defined at `checks.go:200` and is used the same way at `land.go:161`.
     - On `err != nil` it returns `"head log: " + err.Error()`. On `code != 0` it returns `fmt.Sprintf("head log: exit %d: %s", code, strings.TrimSpace(out))`.
     - Otherwise it splits the output into lines and each non-empty line with `strings.SplitN(line, "\t", 3)`. If any line's second field equals `"r-loop " + s.Agent`, it returns `"step committed before review"`. Otherwise it collects `sha + " " + subject` per line.
     - With no lines, the reason is `"HEAD moved from outside the step: HEAD is now " + short`, where `short` is `head[:7]` when `len(head) > 7`, else `head`.
     - Otherwise the reason is `"HEAD moved from outside the step: " + strings.Join(entries, ", ")`.
   - Serves: item 1 (reason names the commits and says `HEAD` moved from outside), item 2 (a real commit keeps today's reason), and the log-failure and empty-range edge cases.

2. **`internal/core/gate.go` — modify.**
   - New exported type:
     ```go
     type FailedStep struct {
         Ref     StepRef
         Outcome Outcome
     }

     func (f *FailedStep) Error() string {
         return fmt.Sprintf("%s step %s: %s", f.Ref.Key.Kind, f.Outcome.State, f.Outcome.Reason)
     }
     ```
     For the gate this is today's text, `gate step failed: <reason>` (`gate.go:95`).
   - `discover`, `gate.go:94-96`: replaces `fmt.Errorf("gate step %s: %s", …)` with `return "", &FailedStep{Ref: ref, Outcome: out}`, after the `ResetHard` restore that is already there.
   - `Command`, `gate.go:46-49`: wraps with `fmt.Errorf("%w: %w", ErrNoGate, err)` (was `%v`), so `errors.As` reaches `*FailedStep` and `errors.Is(err, ErrNoGate)` still holds. The message text is unchanged.
   - `discover`, after `ref.Vars = StepVars(...)` (`gate.go:75`): it walks `st.Events` from the end and looks for an event with `Kind == "restart"`, `Fields["step"] == "phase-"+phase.ID+"/"+p.Kind.Name` and `Fields["attempt"] == strconv.Itoa(ref.Key.Attempt)`. On the first match:
     - It sets `ref.Vars["Addendum"] = ev.Fields["addendum"]`.
     - When `ev.Fields["provider"] != ""`, it sets `ref.Kind.Row.Provider, ref.Kind.Row.Model, ref.Kind.Row.Effort = ev.Fields["provider"], ev.Fields["model"], ev.Fields["effort"]`. Those are the fields `awaitRestart` writes (`loop.go:466-469`).
     - It then stops.

     Add `strconv` to the imports.
   - Serves item 3: a restarted gate attempt runs with the watchdog's addendum and provider.

3. **`internal/core/loop.go` — modify `runPhase`** (`loop.go:287-290`). The single `Land` call becomes:
   ```go
   landing, err := lander.Land(ctx, ph)
   for err != nil {
       var failed *FailedStep
       if !errors.As(err, &failed) {
           break
       }
       out := failed.Outcome
       if _, aborted := l.ended(failed.Ref, out); aborted {
           return "land", out, true
       }
       _, ok, aborted := l.awaitRestart(ctx, failed.Ref, failed.Ref.Kind, &out)
       if !ok {
           if aborted || out.Halted {
               out.Session = last
               return "land", out, aborted
           }
           break
       }
       landing, err = lander.Land(ctx, ph)
   }
   if err != nil {
       return "land", Outcome{State: StepFailed, Reason: "land: " + err.Error(), Session: last}, false
   }
   ```
   - It reuses `ended` (`loop.go:643`, which does `Hold` + `StepEnded`, so the watchdog is told) and `awaitRestart` (`loop.go:414`, which handles the window, `restart_step`, `MaxRestarts`, halts, aborts, the `restart` event and `Release`).
   - `awaitRestart`'s returned ref is not used, because `GateProbe` computes attempt `prior+1` from the store (`gate.go:66`) and reads the `restart` event (change 2).
   - Add `errors` to `loop.go`'s imports; it does not import it today.
   - Serves item 3: the phase is not marked blocked while the gate can still be restarted. It is blocked only after the window closes, with today's `land: …` reason, or with the halt's reason and exit 5.

4. **`docs/task-loop-driver/tech-design.md` — modify** the two-signal rule (`tech-design.md:237-239`). The parenthesis `(an agent commit is failed(step committed before review))` becomes:

   > (a moved `HEAD` fails the step; in a step worktree, and in the primary checkout when a commit in `StartSHA..HEAD` has the step pane's committer name `r-loop <agent>` (set as `GIT_COMMITTER_NAME` at spawn), the reason is `failed(step committed before review)`; in the primary checkout with no such commit it is `failed(HEAD moved from outside the step: <sha> <subject>, …)`, or `… HEAD is now <sha>` when the range is empty; a failed gate step is held for the remedy window and can be restarted before its phase is blocked)

   This keeps the shared contract true for later phases (review finding codex-r1-1).

5. **`internal/core/session_test.go`, `internal/core/gate_test.go`, `internal/core/remedies_test.go` — modify:** add the tests below.

## Tests

The tests in `gate_test.go` use `newLandEnv`/`e.probe` (real git, `gate_test.go:201`). Each extends `probeHost` with a per-test hook that runs inside `Prompt` before the sentinel is written.

1. `TestGateProbeHeadMovedByTheMaintainerNamesTheCommits` (`gate_test.go`) — covers item 1, every external commit named. During the step the hook runs:

   ```
   gitCmd(t, e.root, "commit", "-q", "--allow-empty", "-m", "maintainer one")
   gitCmd(t, e.root, "commit", "-q", "--allow-empty", "-m", "maintainer two")
   ```

   Both commits use committer `test`, not the step's name. The sentinel is `ok`. `p.Command` returns an error that is `errors.Is(ErrNoGate)` and `errors.As(*core.FailedStep)`, with `Outcome.State == StepFailed` and `Outcome.Reason == "HEAD moved from outside the step: " + gitCmd(t, e.root, "log", "-1", "--format=%h") + " maintainer two, " + gitCmd(t, e.root, "log", "-1", "--format=%h", "HEAD~1") + " maintainer one"`. That is `git log` order, newest first.
2. `TestGateProbeAStepsOwnCommitFailsAsCommittedBeforeReview` (`gate_test.go`) — covers item 2 and the marking. The hook makes a maintainer commit as in test 1, then a second empty commit through `exec.Command("git", "-C", e.root, "commit", "-q", "--allow-empty", "-m", "agent work")`. That command's env is `os.Environ()` plus `GIT_AUTHOR_NAME=test`, `GIT_AUTHOR_EMAIL=test@local`, `GIT_COMMITTER_EMAIL=test@local` and every entry of the last `opened` `OpenSpec.Env`. The test asserts:
   - The `FailedStep` reason is exactly `step committed before review`.
   - `host.opened[0].Env["GIT_COMMITTER_NAME"]` starts with `r-loop rloop-` and ends with `-p1-gate`.
3. `TestGateProbeHeadResetToAnEarlierCommitNamesTheNewHead` (`gate_test.go`) — covers the empty-range edge case. Before `p.Command`, the test makes a second commit with `gitCmd(... "commit", "--allow-empty", "-m", "second")`. During the step the hook runs `gitCmd(t, e.root, "reset", "-q", "--soft", "HEAD~1")`. The reason must be `"HEAD moved from outside the step: HEAD is now " + gitCmd(t, e.root, "rev-parse", "HEAD")[:7]`.
4. `TestAnInPrimaryStepWhoseHeadLogFailsNamesTheLogError` (`session_test.go`) — covers the log-failure error path.
   - Setup: `newRig` with `ref.InPrimary = true`, `r.repo.RunExit = 128` and `r.repo.RunOutput = "fatal: bad revision\n"`. The host script sets `r.repo.SHA = "sha-moved"` and writes an `ok` sentinel.
   - `Wait` returns `StepFailed` with reason `head log: exit 128: fatal: bad revision`.
   - `r.callsFrom("Repo.Run")` has one entry, containing `sha-start..sha-moved`.
5. `TestAWorktreeStepWithAMovedHeadFailsAsCommittedBeforeReviewWithoutReadingTheLog` (`session_test.go`) — covers "worktree steps keep today's behaviour".
   - Setup: the default worktree ref. `r.repo.RunOutput = "abc1234\ttest\tmaintainer work\n"`. The script moves `r.repo.SHA` and writes an `ok` sentinel.
   - The reason is `step committed before review` and `r.count("Repo.Run") == 0`.
6. `TestGateProbeRestartRunsTheNextAttemptWithTheAddendumAndProvider` (`gate_test.go`) — covers item 3, change 2.
   - Setup: seed `e.store` with a `RecordStep` for `StepKey{Run: "run1", Phase: "1", Kind: "gate", Attempt: 1}` in state `failed`. Also seed a `RecordEvent` `restart` with `{"step": "phase-1/gate", "attempt": "2", "addendum": "ignore the stale lock", "provider": "codex", "model": "gpt-5", "effort": "high"}`.
   - Replace `p.Sessions.Resolve` with a closure that records `provider, model, effort`, and set `p.Sessions.Prompts` to a `*reportPrompts`.
   - After `p.Command` succeeds:
     - the recorded vars' `Addendum` is `ignore the stale lock`;
     - the resolve arguments are `codex gpt-5 high`;
     - the recorded vars' `Sentinel` ends with `gate-a2.sentinel`.
7. `TestAFailedGateStepIsRestartedInTheRemedyWindowWithoutBlockingThePhase` (`remedies_test.go`, same rig as `TestAnAuthorisedRestartRerunsTheStepAsANewAttemptWithTheAddendum` at `remedies_test.go:277`) — covers item 3, change 3.
   - `r.loop.RemedyWindow = time.Minute`, with `w := &Watch{Store: r.store}` as `r.loop.Watcher` and `rem := newRemedies(w, r.store, "restart")`, so `restart` is on the allow-list. `r.loop.Lander` is a test `gateLander` whose first `Land` returns:
     ```go
     fmt.Errorf("%w: %w", ErrNoGate, &FailedStep{
         Ref:     StepRef{Key: StepKey{Run: "run-1", Phase: "2", Kind: "gate", Attempt: 1}, Kind: StepKind{Name: "gate"}},
         Outcome: Outcome{State: StepFailed, Reason: "HEAD moved from outside the step: abc1234 maintainer work"},
     })
     ```
     Its second `Land` appends a landing and returns it.
   - A goroutine waits for `w.holding()` to return kind `gate`, then calls `rem.Restart("phase-2/gate", "retry", "", "")` and sends its reason on a channel.
   - Expected:
     - the exit code is 0;
     - the restart reason is `""`;
     - there are no `phase-blocked` events;
     - there is one `restart` event with `step` `phase-2/gate` and `attempt` `2`;
     - `Land` is called twice.
8. `TestAFailedGateStepWithNoRestartBlocksThePhaseAfterTheWindow` (`remedies_test.go`) — covers "the window closes → blocked as today". The same lander always returns the `FailedStep` error, `RemedyWindow = 20 * time.Millisecond`, and there is no restart. Expected: exit 1, and one `phase-blocked` event whose `reason` starts with `land: no gate: gate step failed: HEAD moved from outside the step`.
9. `TestAHaltInTheGateRemedyWindowBlocksThePhaseWithExit5` (`remedies_test.go`) — covers the halted branch. The same lander is used with `RemedyWindow = time.Minute`. Once the gate is held, the goroutine calls `w.Handle(Signal{Kind: SignalHalt, Source: SourceWatchdog, Step: StepKey{Run: "run-1", Phase: "2", Kind: "gate", Attempt: 1}, Reason: "stop"})`. Expected: exit 5, and one `phase-blocked` event with reason `watchdog: stop`.

The existing `TestAnAgentCommitFailsTheStep`, `TestFixHalfFailures` "head moved" and `TestGateProbeFailedSessionIsNoGate` keep passing unchanged. They pin today's worktree reason and the gate error text.

## Left out

- **A new `Repo` port method for listing commits:** `Repo.Run` already runs git in a directory. The maintainer ruled out a new port method.
- **A git hook to mark commits:** an env var in the pane does the same without overriding the repository's hooks.
- **The remedy window for the milestone step:** a failed milestone report is recorded as `report-skipped` and never blocks the phase (`milestone.go:33-38`), so nothing needs a restart there. It still gets the new reason through `judge`.
- **`GIT_AUTHOR_NAME` marking:** the committer name alone decides attribution.
- **Changing `spec.html`:** ADR-58 says "an agent never commits, and a moved branch fails the step". That still holds, because a moved branch still fails the step; only the reason changes, and `tech-design.md` carries that (change 4).

## Assumptions

- Attribution rule (the watchdog's first warning): resolved by the maintainer's answer to q2 (option A). The step's pane sets `GIT_COMMITTER_NAME=r-loop <agent>`, and `judge` reads `%cn` over `StartSHA..HEAD`.
- The restart warning (the watchdog's second warning): resolved by change 3. The land path takes a `*FailedStep` through `ended` (`Hold`) and `awaitRestart` (window, `restart_step`, new attempt key via `GateProbe`'s `prior+1`, `Release`) before the phase is blocked.
- The step still fails when `HEAD` moved from outside; "not failed as its own fault" is carried by the reason and by the restart path, not by a new step state. Item 3 speaks of "a step that failed this way".
- Every failed gate step gets the remedy window, not only the external-move case, matching how pipeline steps are held on any failure.
- An agent that overrides `GIT_COMMITTER_NAME` itself is out of reach of any rule. Such commits read as external.
- The short SHA in the empty-range reason is the first 7 characters of `head`.

## Gate

`go test ./internal/core/ -run '^(TestGateProbeHeadMovedByTheMaintainerNamesTheCommits|TestGateProbeAStepsOwnCommitFailsAsCommittedBeforeReview|TestGateProbeHeadResetToAnEarlierCommitNamesTheNewHead|TestAnInPrimaryStepWhoseHeadLogFailsNamesTheLogError|TestAWorktreeStepWithAMovedHeadFailsAsCommittedBeforeReviewWithoutReadingTheLog|TestGateProbeRestartRunsTheNextAttemptWithTheAddendumAndProvider|TestAFailedGateStepIsRestartedInTheRemedyWindowWithoutBlockingThePhase|TestAFailedGateStepWithNoRestartBlocksThePhaseAfterTheWindow|TestAHaltInTheGateRemedyWindowBlocksThePhaseWithExit5)$'`
