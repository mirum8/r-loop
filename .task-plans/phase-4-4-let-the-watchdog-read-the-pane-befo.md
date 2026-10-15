status: planned

## Summary

The phase adds one step-blocker action, `nudge`. It continues a stalled step in its own live session instead of a fresh one. The watchdog picks it with `resolve_blocker(id, "nudge", addendum?)`. The driver then starts attempt N+1, and that attempt **adopts** attempt N's session: same agent name, pane, workspace, worktree, `StartSHA`, `StartTree` and ask URL. Nothing is opened or started. The driver types a fixed continuation into the pane. It names the new attempt's sentinel `<kind>-a<N+1>.sentinel` and ends with the watchdog's addendum. The same watch then runs on that session, so a sentinel written afterwards is judged as attempt N+1's outcome.

`nudge` is offered only when the work half stalled and its pane is still standing. It is refused when the agent is gone. It is authorised on the watchdog's word, and it shares `watchdog.maxRestarts` with `retry`, `switch` and `continue`. ADR-62 and ADR-97 are amended in place, and tech-design.md, the spec's blocker paragraphs, `watchdog.md` (which lists `nudge` first) and the `resolve_blocker` description are updated to match.

The #4 half (read the pane before acting) is already built and is not rebuilt; see `## Assumptions`.

Choices:
- **Same session as a new attempt, or reopen the failed one** → new `StepKey` with `Attempt+1` that adopts the session. Reopening breaks the "never a transition out of `failed`" invariant (tech-design.md:65-67, states.go:42-48); a new attempt keeps it.
- **Where adoption lives** → `StepRef.Adopt *Session`, read at the top of `SessionManager.Spawn`, which hands off to a new `adopt` method. A separate `singleRunner` branch would add a second spawn entry point that every runner must remember. `Spawn` is the single place every step attempt already starts (runners.go:11).
- **Sentinel path** → a new per-attempt path `<kind>-a<N+1>.sentinel`, with the continuation naming it and the old path. Reusing the old file would break the documented one-sentinel-per-attempt naming (tech-design.md:202). Naming both paths in the text tells the agent which one is now read.
- **Who types** → the driver, with fixed text in `internal/core`, as with the nudge (session.go:706) and answers (loop.go:1458). ADR-62 rejects the watchdog writing to a session (spec.html:2919-2920). Only the addendum is the watchdog's, as `retry`'s addendum already is.
- **Where "agent gone" is refused** → in `RunLoop.SettleBlocker`, which already refuses `keys` with no pane (blockers.go:347-349). That way the watchdog's `resolve_blocker` call itself comes back `refused` and the blocker stays open, rather than the blocker being settled and then failing. A death after settling is caught when the continuation fails to be typed (`Spawn` error → attempt N+1 fails → a new blocker).
- **How the agent is known at settle time** → a new `Blocker.Agent` field. The alternative, `openAsk.agent`, is cleared for an ended session (blockers.go:141-142), and setting it there would switch on the `keys` and pane-moved paths for step blockers.
- **How "stalled in the work half" is known** → an unexported `Outcome.nudgeable`, set by `singleRunner` right after `Wait`. The alternative was inferring it from `review-round` events through a `Store.Load`, which adds an error path that would be swallowed. A stall in a fix round or a reviewer must not offer `nudge`: the continuation would ask for the work-half sentinel while the agent was fixing findings.
- **Questions from the adopted session** → `Session.askAttempt` remembers the attempt its MCP URL was issued for. `askingSession` and `questionPanicked` match it, and `question` rewrites `q.Step.Attempt` to the live attempt. The alternative, re-pointing the URL in the `askmcp` adapter, is a port change plus an adapter change for one case. The agent's MCP config cannot change mid-session (askmcp/server.go:203-209).
- **Budget** → `nudge` is a `restartAction`. Not counting it would let stall → nudge → stall loop forever: the per-session nudge cap resets with each new watch (session.go:397-410).
- **Authorisation** → always (`citationAlways`), attended or not. A nudge in place is the least disruptive routine fix, and ADR-97 makes routine fixes the watchdog's own call. The memory note "unstoppable driver" says the maintainer's consent is never required for one.

## Why this approach

The item proposed "an action that types a continuation into the same live session instead of starting Attempt+1". The triage proposed a step-blocker action that creates Attempt+1 adopting the live session (same agent, pane, baseline, StartSHA), with the driver typing a fixed continuation plus the addendum, refused when `Host.State` is gone, counting against `maxRestarts`. This plan follows the triage, because "instead of starting Attempt+1" cannot be met literally. A stall has already been recorded `failed` by `SessionManager.Finish` (session.go:840-893) before `awaitRestart` raises the blocker (runners.go:13,26; loop.go:766-773). `failed` is terminal (states.go:42-48; tech-design.md:65-67). So the in-place continuation is a new attempt that keeps the session, not a new session.

The phase-check warnings and how each is handled:
1. *"the same session" must be a new StepKey Attempt+1 that adopts the live pane/agent, baseline and StartSHA.* This is what `adopt` does. It copies `Dir`, `StartSHA` and `StartTree` from attempt N, so the evidence diff and the `step committed before review` check (session.go:721-727) still measure from the step's start.
2. *Agent names and snapshots carry -a<attempt> (session.go:186,193).* `adopt` skips `freeAgent` and keeps the old name. It records an `agent-named` event for attempt N+1 with that name, so `r-loop resume` interrupts the right agent (internal/app/resume.go:549-555). Snapshots stay keyed by `<kind>-a<attempt>` (session.go:902-909), as for any attempt.
3. *ADR-62 rejects the watchdog writing to the session.* Kept: the driver types fixed text built by `continuation()` in core. The amendment to ADR-62 says so.
4. *stepActions (loop.go:593) and the watchdog tool schema both need the new action.* `stepActions` stays as it is. `awaitRestart` puts `nudge` in front of it only for a nudgeable stall. The MCP schema has no action enum (askmcp/watchdog.go:114-124), so only the tool's `Description` string (askmcp/watchdog.go:453) changes.

Files outside core, all needed by the open items:
- `internal/askmcp/watchdog.go`: the tool description is how the watchdog learns the action exists.
- `internal/prompts/templates/watchdog.md`: item 4 names it.
- `docs/task-loop-driver/spec.html` and `docs/task-loop-driver/tech-design.md`: item 4 names them.

The phase has no `Files:` line.

Other things the plan has to handle:
- `runStep` closes the workspaces of earlier attempts (loop.go:1088-1090). It must not close the workspace that attempt N+1 adopts.
- The adopted session's hook state is not reset. The typed continuation starts a new turn, so the fresh watch (`turnEnd` 0) sees a busy turn first, exactly as a fix round does (review.go:190-200).

## Changes

1. **`internal/core/types.go`** (modify). Add `Agent string` to `Blocker` (types.go:110-113), on the existing string field line, giving `Source, Phase, Step, Reason, Excerpt, Evidence, Agent string`. It names the agent a `nudge` would type into, and it is set only when `nudge` is among `Actions`. Serves: refusal when the agent is gone.

2. **`internal/core/blockers.go`** (modify):
   - Add `actionNudge = "nudge"` beside `actionContinue` (blockers.go:21).
   - `restartAction` (blockers.go:585-587) also returns true for `actionNudge`. This makes `retriesUsed` count a resolved `nudge`, puts `nudge` under the `n >= rem.MaxRestarts` refusal (`nudge limit <n> reached for <target>`), and lets it count in the `lastResort` rule. Serves: budget.
   - `QuestionRouter.ResolveBlocker` switch (blockers.go:504-536) gains `case actionNudge: res.Addendum, res.Citation, authorised = addendum, citationAlways, true`. Serves: authorisation and the addendum.
   - `RunLoop.SettleBlocker` (blockers.go:341): right after the existing `actionKeys`/`open.agent` check, and before `run`, add:
     ```go
     if res.Action == actionNudge {
         state, err := l.Sessions.Host.State(open.b.Agent)
         if err != nil {
             return fmt.Errorf("blocker %s: state of %s: %w", res.ID, open.b.Agent, err)
         }
         if state == AgentGone {
             return fmt.Errorf("agent %s of blocker %s is gone: take retry for a fresh session", open.b.Agent, res.ID)
         }
     }
     ```
     The router turns the error into `refused` with that reason (blockers.go:554-556), and the blocker stays open. Serves: refusal when the agent is gone, and a failed state query as a separate error.

3. **`internal/core/session.go`** (modify):
   - `StepRef` (session.go:65-75): add `Adopt *Session`, the earlier attempt's session that this attempt continues in place.
   - `Session` (session.go:77-98): add unexported `askAttempt int`, the attempt its ask URL was issued for when that is not `Ref.Key.Attempt`.
   - `Outcome` (session.go:152-161): add unexported `nudgeable bool`.
   - Add the method below beside `asker` (session.go:106). It accepts the live attempt, and also the adopted ask attempt for the worker's own kind.
     ```go
     func (s *Session) asksAs(key StepKey) bool {
         k := s.Ref.Key
         adopted := key.Kind == k.Kind && s.askAttempt > 0 && key.Attempt == s.askAttempt
         return k.Run == key.Run && k.Phase == key.Phase && (key.Attempt == k.Attempt || adopted) && (key.Kind == k.Kind || strings.HasPrefix(key.Kind, k.Kind+"-rv-"))
     }
     ```
   - `Spawn` (session.go:163): as its first statement, `if ref.Adopt != nil { return m.adopt(ref) }`.
   - New `func (m *SessionManager) adopt(ref StepRef) (*Session, error)`, placed after `Spawn`. Let `prev := ref.Adopt`. It does the following, in order:
     1. Build `s := &Session{Ref: ref, Subject: ref.Subject, Dir: prev.Dir, StartSHA: prev.StartSHA, StartTree: prev.StartTree, Workspace: prev.Workspace, Pane: prev.Pane, Agent: prev.Agent, rollout: prev.rollout, startedAt: prev.startedAt}`.
     2. Set `s.askAttempt = prev.askAttempt`; if that is 0, set `s.askAttempt = prev.Ref.Key.Attempt`.
     3. Set `s.Ref.AskURL = prev.Ref.AskURL`.
     4. Copy `prev.transcript` under `prev.mu` into `s.transcript`, and `s.transcribed.Store(prev.transcribed.Load())`.
     5. `s.received.Store(true)`. The session already took a prompt, so `prompt` types once via `Host.Prompt` instead of the re-type loop (hooks.go:271-283).
     6. `if err := recordFailed(m.Store); err != nil { return s, fmt.Errorf("record: %w", err) }` (as session.go:165).
     7. Set `s.Sentinel = filepath.Join(ref.RunDir, "phase-"+key.Phase, fmt.Sprintf("%s-a%d.sentinel", key.Kind, key.Attempt))`.
     8. `if err := m.record(key, StepSpawned, ""); err != nil { return s, err }`.
     9. Append an `agent-named` event with exactly the fields `start` writes (session.go:297): `attempt`, `agent`, `worktree` (`s.Dir`), `base` (`ref.Base`), `start_sha`. Return its `Store.Append` error at once, as `start` does (session.go:298-300). Both writes happen before the continuation is typed, so a failed write types nothing (append before the action it describes).
     10. `m.track(s)`.
     11. `m.prompt(s.Agent, continuation(key.Attempt, s.Sentinel, prev.Sentinel, addendum, s.Ref.AskURL != ""), nil)`, where `addendum, _ := ref.Vars["Addendum"].(string)`. On error, `return s, fmt.Errorf("nudge: %w", err)`.
     12. `return s, m.record(key, StepRunning, "")`.

     It does not call `AddWorktree`, `baseline`, `freeAgent`, `Host.Open`, `Host.Start`, `hookReset` or `hookStart`. Serves: same session, same agent and starting point, and a new sentinel judged as attempt N+1.
   - Extract `blockedHint(ask bool) string` from `nudge` (session.go:706-711). It returns the existing two `blocked` sentences. `nudge` calls it, with unchanged text.
   - New `continuation(attempt int, sentinel, old, addendum string, ask bool) string`. It returns `fmt.Sprintf("r-loop: the watchdog continues this step in this session as attempt %d. Finish the work you were doing; when it is done, write the sentinel to %s, not %s. ", attempt, sentinel, old) + blockedHint(ask)`. When `addendum != ""` it appends `"\n\nNote from the watchdog: " + addendum`. Serves: fixed driver text naming the new sentinel, plus the addendum.

4. **`internal/core/runners.go`** (modify). In `singleRunner.Run`'s `else` branch (runners.go:20-21), after `out = r.sm.Wait(ctx, s, obs)`, add `out.nudgeable = out.Stalled`. `Finish` keeps the field. Serves: offering `nudge` only for a work-half stall.

5. **`internal/core/loop.go`** (modify):
   - `awaitRestart` (loop.go:777):
     - Before the `for` loop, build `b := Blocker{Source: sourceStep, Phase: key.Phase, Step: key.Kind, Reason: reason, Excerpt: excerpt, Evidence: evidence, Actions: stepActions}`.
     - If `out.nudgeable && out.Session != nil && out.Session.Pane != ""`, set `b.Actions = append([]string{actionNudge}, stepActions...)` and `b.Agent = out.Session.Agent`.
     - Inside the loop, `l.raise(ctx, b, main)` with `b.Reason = reason` refreshed each pass, replacing the literal at loop.go:797.
     - The action switch (loop.go:810-811) becomes `case actionRetry, actionSwitch, actionContinue, actionNudge:`. A `nudge` then takes the existing `l.restarts[step] >= l.MaxRestarts` refusal (`restart-refused`, loop.go:819-824) and the `l.restarts[step]++`.
     - After `next := l.ref(...)` (loop.go:855), add `if res.Action == actionNudge { next.Adopt = out.Session }`. `next.Vars["Addendum"] = res.Addendum` stays shared, and the `restart` event keeps its fields, with `remedy` = blocker id and `citation` = `always`. Serves: the action, adoption, and the budget.
   - `runStep` (loop.go:1088): the condition becomes `if key.Attempt > 1 && ref.Adopt == nil`. Earlier attempts were already closed when the adopted attempt itself started. Serves: the adopted pane stays open.
   - `askingSession` (loop.go:1655): replace the inline key predicate with `s.asksAs(key)`.
   - `questionPanicked` (loop.go:1411): replace the inline key predicate with `s.asksAs(q.Step)`.
   - `question` (loop.go:1362): after `s := l.askingSession(ctx, q.Step)` and its nil check, add `q.Step.Attempt = s.Ref.Key.Attempt`. Records and the watch then see the live attempt. Serves: the adopted session's `ask_watchdog` reaches the watchdog, and its answer is typed back.

6. **`internal/askmcp/watchdog.go`** (modify). In the `resolve_blocker` `Description` (askmcp/watchdog.go:453), change `with one of its actions: retry, keys, switch, continue, block or stop.` to `with one of its actions: nudge, retry, keys, switch, continue, block or stop. nudge types a continuation into the stalled step's own live session, with the addendum after it, and watches that session again; it is authorised on its own and refused when the agent is gone;`. The rest of the string is unchanged. Serves: the watchdog can choose the action.

7. **`internal/prompts/templates/watchdog.md`** (modify). Under `## Clearing blockers`, insert this as the first sub-bullet under "Then fix what you are authorised to fix, with `resolve_blocker(id, action, …)`:" (watchdog.md:179), above the `retry` bullet:
   `  - \`nudge\` is the first remedy for a stall whose pane is alive: the driver types a fixed continuation into the stalled agent's own session, naming the sentinel of a new attempt, with your \`addendum\` after it, and watches that session again, so the agent keeps its context, subagents and background jobs. Take it when the pane shows the agent was working toward its sentinel and went quiet; take \`retry\` when the session is wedged or on the wrong track. It is offered only for a stall before the review half, refused when the agent is gone, authorised on your word, and shares the step's retries with \`retry\`, \`switch\` and \`continue\`.`
   Serves: watchdog.md lists the in-place re-nudge first.

8. **`docs/task-loop-driver/spec.html`** (modify):
   - **ADR-62 Status** (spec.html:2909). Before `</p>`, append ` · amended 2026-10-15 — a step whose work half stalls raises a blocker that also offers <code>nudge</code>: the driver, never the watchdog, types a fixed continuation into the same live session, naming the sentinel of a new attempt, with the watchdog's addendum after it, and watches that session again; the re-run is attempt N+1 adopting the session's agent, pane, worktree and starting point, so no record leaves <code>failed</code>; refused when the agent is gone`.
   - **ADR-97 Status** (spec.html:4253). Before `</p>`, append ` · amended 2026-10-15 — <code>nudge</code> on a stalled step's blocker is the first remedy while its pane is alive, authorised on the watchdog's word, attended or not, and shares <code>watchdog.maxRestarts</code> with <code>retry</code>, <code>switch</code> and <code>continue</code>`.
   - **"A step fails" paragraph** (spec.html:950-951). After `The watchdog reads the pane and diagnoses, then fixes what it finds (<a href="#adr-97">ADR-97</a>):`, insert ` for a stall whose pane is alive, first a <code>nudge</code>, the driver typing a fixed continuation into that same session;`.
   - **Part 7 `resolve_blocker` line** (spec.html:4591-4592). Add `nudge` to the listed actions, first, in the same `<code>` style.
   - Serves: ADR-62 and ADR-97 amended.

9. **`docs/task-loop-driver/tech-design.md`** (modify):
   - Line 65-67: after `never reopened.`, add `A \`nudge\` is a re-run too: attempt N+1 adopts attempt N's live session (agent, pane, workspace, worktree, \`StartSHA\`, \`StartTree\`, ask URL) instead of spawning one, and its sentinel is \`<kind>-a<N+1>.sentinel\`.`
   - Stall rule (line 350-351): after `a failed step's session is left standing.`, add `A stall in a step's work half raises a step blocker that offers \`nudge\` first (Milestone 21 sources table).`
   - Sources table `step` row (line 1215): the actions become `nudge (a work-half stall whose pane is open), retry, switch, block, stop, continue`.
   - After the `continue` paragraph (line 1236), add a paragraph: `\`nudge\` (\`awaitRestart\`) is a restart to attempt N+1 whose \`StepRef.Adopt\` is attempt N's session; \`Spawn\` then opens and starts nothing, records \`spawned\`, \`agent-named\` (same agent) and \`running\`, and types \`continuation()\` — \`r-loop: the watchdog continues this step in this session as attempt <N+1>. Finish the work you were doing; when it is done, write the sentinel to <new>, not <old>.\` plus the nudge's blocked sentence and \`Note from the watchdog: <addendum>\` — and the step is watched and judged as any attempt. \`runStep\` closes no workspace for it. It is listed only when \`singleRunner\`'s \`Wait\` returned a stall, and \`SettleBlocker\` refuses it when \`Host.State(Blocker.Agent)\` is \`gone\` or fails.`
   - Authorisation table: add a row `| \`nudge\` | always, attended or not, while the target is within \`retry\`'s budget, which counts its nudges too (ADR-62, ADR-97 as amended 2026-10-15); refused when the agent is gone | at the budget → refused |`.
   - Serves: tech-design.md updated.

10. **Tests** in `internal/core/blockers_test.go`, `internal/core/session_test.go`, `internal/core/hooks_test.go`, `internal/prompts/render_test.go` and `internal/askmcp/watchdog_test.go`, as below. Also update the existing literal in `TestAStalledStepsBlockerRoutesItsAgentAndTheEndOfItsPaneScreenToTheWatchdog` (blockers_test.go:1857) to `actions: nudge, retry, switch, block, stop, continue`. Its plan step stalls in its work half, so the new action is now offered there. Run `go test ./internal/core/ ./internal/prompts/ ./internal/askmcp/`, and update only literals that list the actions of a stalled work-half step.

## Tests

Shared test fixtures, all added to `internal/core/blockers_test.go`. They keep each test's data in the test.
- `nudgeHost`: embeds `*eventsHost` and has an `onContinue func(agent string)` field. Its `Prompt` does the following: for text starting `r-loop: the watchdog continues`, it records `SessionHost.Continued %s %q` (agent, text), then returns `continueErr` when that field is set, otherwise calls `onContinue(agent)` and returns nil; for text starting `r-loop: answer to `, it calls `eventsHost.Prompt` and then `onAnswer(agent)` when that is set; any other text it delegates to `eventsHost.Prompt`.
- `nudgeRig(t, resolve func(Blocker) Resolution) (*eventsRig, *nudgeHost, *[]raised)`: built on `newSourceRig`. It sets `behaviour["rloop-p2-implement"] = "stall"` and installs the `nudgeHost`.
- `writeA2(r, outcome)`: writes the given sentinel JSON to `filepath.Join(r.store.dir, "phase-2", "implement-a2.sentinel")` and calls `r.repo.setChanges("code.go")`.
- `storedEvents(store, kind)`: returns the recorded events of a kind.
- `nudgeable()`: the fixture `Blocker{Source: "step", Phase: "2", Step: "implement", Reason: "stalled: no response to nudge", Agent: "rloop-p2-implement", Actions: []string{"nudge", "retry", "switch", "block", "stop", "continue"}}`.

Each test has `// given`, `// when`, `// then` blocks.

- `TestAWorkHalfStallRaisesAStepBlockerThatOffersNudgeFirstWithItsAgent`: newSourceRig resolving `block`, with implement on `stall`. The first raised blocker, with `Evidence` cleared and `Agent` passed through `role`, equals `Blocker{Source: "step", Phase: "2", Step: "implement", Reason: "stalled: no response to nudge", Agent: "rloop-p2-implement", Actions: [nudge retry switch block stop continue]}`. Covers: the action is offered for a stall, first, with the agent.
- `TestAReviewHalfStallRaisesAStepBlockerWithoutNudge`: implement row `Rounds = 1`, and `r.loop.Runners["diff"] = singleRunner{sm: r.loop.Sessions, review: <returns Outcome{State: StepFailed, Reason: "stalled: no response to nudge", Session: worker, Stalled: true}>}`. The stub is the runner's injected review seam (runners.go:7), and the default behaviour writes an ok work sentinel. The blocker's `Actions` equal `[retry switch block stop continue]` and its `Agent` is `""`. Covers: no nudge for a stall outside the work half.
- `TestANudgedStepTypesTheContinuationIntoTheStalledAgentsPane`: resolving `Resolution{Action: "nudge", Citation: "always", Addendum: "finish the store tests"}`, with `onContinue` calling `writeA2(ok with commit)`. `r.calls("SessionHost.Continued ")` equals exactly one entry: `rloop-p2-implement "r-loop: the watchdog continues this step in this session as attempt 2. Finish the work you were doing; when it is done, write the sentinel to <dir>/phase-2/implement-a2.sentinel, not <dir>/phase-2/implement-a1.sentinel. If you are blocked, write a failed sentinel with the reason.\n\nNote from the watchdog: finish the store tests"`. Here `<dir>` is `r.store.dir`, and the expected string is written out in the test with that directory. Covers: driver-typed fixed continuation naming the new sentinel, plus the addendum.
- `TestANudgedStepKeepsItsAgentWorkspaceAndStartingPoint`: the same setup. Then:
  - `r.agents()` equals `[rloop-p2-plan rloop-p2-implement]`;
  - `len(r.host.Opened)` is `2`;
  - the recorded `agent-named` event for implement with `attempt` `2` has fields `{attempt: 2, agent: <the attempt-1 agent's full name>, worktree: r.repo.RootDir+"/.r-loop/wt/phase-2", base: "", start_sha: "sha"}`.

  Covers: same session, same agent name, same starting point.
- `TestASentinelWrittenAfterANudgeIsJudgedAsTheNewAttemptsOutcome`: the same setup. The exit code is `0`. `assertStepRecords` for `{run-1 2 implement 2}` gives `[queued spawned running ok]` with empty reasons. For attempt 1 it gives `[queued spawned running stalled failed]` with the last reason `stalled: no response to nudge`. Covers: the sentinel is judged as attempt N+1, and no transition out of failed.
- `TestAFailedSentinelAfterANudgeFailsTheNewAttemptAndKeepsItsWorkspaceOpen`: the resolver returns `nudge` for the first blocker and `block` for the second. `onContinue` writes `{"outcome":"failed","reason":"gave up"}` via `writeA2`. Then:
  - exit `1`;
  - the second blocker's `Reason` is `gave up` and its `Actions` are `[retry switch block stop continue]`;
  - `r.events("workspace-closed")` is empty;
  - `r.calls("SessionHost.Close ")` is empty.

  Covers: the adopted workspace is not closed, and a failed sentinel is judged for attempt N+1.
- `TestANudgeAtTheRestartLimitIsRefusedAndBlocksTheStalledPhase`: `MaxRestarts = 1`; the resolver always returns `nudge`; `onContinue` does nothing. Then:
  - exit `3`;
  - exactly one `restart-refused` event, with fields `{step: phase-2/implement, reason: restart limit 1 reached}`;
  - one `SessionHost.Continued` call;
  - agents `[rloop-p2-plan rloop-p2-implement]`.

  Covers: the loop counts nudges against `watchdog.maxRestarts`.
- `TestANudgedAgentsQuestionReachesTheWatchdogAndItsAnswerIsTypedBack`:
  - Setup: `onContinue` sets `r.host.idle["rloop-p2-implement"] = false` under `r.host.mu`, and sends `Question{ID: "q1", Step: StepKey{Run: "run-1", Phase: "2", Kind: "implement", Attempt: 1}, Text: "which db?", Options: [sqlite postgres]}` to `r.ask.Asked`. The route answers blockers with `nudge` via `SettleBlocker`. For a non-blocker question it sets the agent idle again, starts `go r.loop.Deliver(q.ID, "sqlite", "watchdog", "spec.html:1")`, and returns true. `onAnswer` calls `writeA2(ok with commit)`. The test runs through `runWithin`.
  - Assertions: exit `0`. `r.calls("SessionHost.Typed ")` equals `[rloop-p2-implement "r-loop: answer to q1 (by watchdog, citing spec.html:1): sqlite"]`. The last recorded `q1` question has `Step` `{run-1 2 implement 2}`.
  - Covers: the adopted session's asks still route, keyed to the live attempt.
- `TestAContinuationThatCannotBeTypedFailsTheNewAttemptAndRaisesANewBlocker`:
  - given: a `nudgeRig` whose resolver returns `nudge` for the first blocker and `block` for the second, with `continueErr = errors.New("herdr agent prompt: agent gone")`. This is the agent dying after `SettleBlocker`'s live check.
  - when: `r.run`.
  - then:
    - exit `1`;
    - the second blocker's `Reason` is `nudge: herdr agent prompt: agent gone` and its `Actions` are `[retry switch block stop continue]`;
    - `assertStepRecords` for `{run-1 2 implement 2}` gives `[queued spawned failed]` with reasons `["", "", "nudge: herdr agent prompt: agent gone"]`;
    - `r.agents()` is `[rloop-p2-plan rloop-p2-implement]`;
    - `len(r.host.Opened)` is `2`;
    - `r.calls("Repo.CommitAll ")` holds only the plan step's commit.

  Covers: the check-to-use race on the typing path.
- `TestANudgedAttemptJudgesItsEvidenceFromTheStalledAttemptsBaseline`:
  - given: a `nudgeRig` resolving `nudge`. Its route sets `r.repo.Tree = "tree-after-stall"` before it settles the blocker: the worktree changed after attempt 1's baseline `tree` was taken. `onContinue` calls `writeA2(ok with commit)`.
  - when: `r.run`.
  - then: exit `0`; `slices.Contains(r.calls("Repo.TreeDiff "), "tree tree-after-stall")` is `true`; `slices.Contains(r.calls("Repo.TreeDiff "), "tree-after-stall tree-after-stall")` is `false`. The diff check calls `TreeDiff(StartTree, now)` (evidence.go:229), so a re-snapshot at adoption would show up as the second form.
  - Covers: the adopted attempt keeps the stalled attempt's baseline, so the work done before the stall counts as its evidence.
- `TestAHeadMovedBeforeANudgeFailsTheAdoptedAttemptAsCommittedBeforeReview`:
  - given: a `nudgeRig` whose resolver returns `nudge` and then `block`. The route sets `r.repo.SHA = "moved"` before it settles the first blocker: the agent committed while the blocker was open. `onContinue` calls `writeA2(ok with commit)`.
  - when: `r.run`.
  - then: exit `1`; the second blocker's `Reason` is `step committed before review`; `assertStepRecords` for attempt 2 ends `failed` with that reason.
  - Covers: the adopted attempt keeps the original `StartSHA`, so the moved HEAD is not taken as a new starting point.
- `TestNudgeIsAuthorisedOnTheWatchdogsWordWithItsAddendum`: a table with rows `attended, restart not allow-listed` (`r.rem.Allow = nil`) and `unattended` (`r.router.Dog.Unattended = true`, `r.rem.Allow = nil`). `newBlockerRig` and `r.dhost.set("rloop-p2-implement", AgentIdle, "")` are set up, and the test opens `nudgeable()`. Then `r.resolve("b1", "nudge", addendum("finish the store tests"))` returns `authorised` with an empty reason. The resolution equals `Resolution{ID: "b1", Action: "nudge", By: "watchdog", Citation: "always", Addendum: "finish the store tests"}`, and `r.waitingEvents()` is `0`. Covers: authorisation without consent.
- `TestNudgeIsRefusedWhileTheStalledAgentIsGoneOrItsStateFails`: a table:
  - row `gone`: `r.dhost.set("rloop-p2-implement", AgentGone, "")`; the reason is `agent rloop-p2-implement of blocker b1 is gone: take retry for a fresh session`;
  - row `state fails`: `r.loop.Sessions.Host` is a test `stateErrHost{*dialogHost}` whose `State("rloop-p2-implement")` returns `errors.New("herdr down")`; the reason is `blocker b1: state of rloop-p2-implement: herdr down`.

  For both rows the decision is `refused`, `stillHeld(t, ch)` holds, and `r.events("blocker-resolved")` is empty. Covers: refused when the agent is gone, and the state error kept distinct.
- `TestNudgeSharesTheRetryBudget`: a table with these rows:
  - `{before: nudge, now: retry, reason: retry limit 1 reached for phase-2/implement}`;
  - `{before: retry, now: nudge, reason: nudge limit 1 reached for phase-2/implement}`;
  - `{before: nudge, now: continue, reason: continue limit 1 reached for phase-2/implement}`;
  - `{before: continue, now: nudge, reason: nudge limit 1 reached for phase-2/implement}`.

  It uses `newBlockerRig`, `r.rem.MaxRestarts = 1`, a seeded `blocker-resolved` event `{id: b0, action: <before>, by: watchdog, source: step, step: phase-2/implement}`, `r.dhost.set("rloop-p2-implement", AgentIdle, "")`, and opens `nudgeable()`. `r.resolve("b1", <now>)` returns `refused` with the row's reason. `stillHeld(t, ch)` holds, and `r.events("blocker-resolved")` is empty. Covers: retry/continue and nudge count against `watchdog.maxRestarts`.
- `TestAnAdoptionWhoseRecordFailsNeverTypesTheContinuation` (internal/core/session_test.go): a table.
  - Row `spawned record`: `r.store.Err = errors.New("disk full")`.
  - Row `agent-named event`: `r.sm.Store = failingEventStore{fakeStore: r.store, kind: "agent-named"}` (review_test.go:921).

  given: `newRig`, then `s := r.spawn(t, 1)`, then `n := len(r.shared.Calls())`, then the row's failing store. when: `r.sm.Spawn(ctx, ref)` with `ref := r.ref(2)` and `ref.Adopt = s`. then: the error contains `disk full`, and a helper `hostCallsSince(r, n)` (the `SessionHost.` entries of `r.shared.Calls()[n:]`) equals an empty slice, so no continuation is typed and nothing is opened or started. Covers: append-before-action on the adoption path.
- `TestAnAdoptedHookSessionTakesTheContinuationAsANewTurnAndEndsOk` (internal/core/hooks_test.go):
  - given: `r, feed, s := hookStallRig(t)`, then `n := len(r.shared.Calls())` and `feed.push(stop(stepAgent))`, so the retained hook state holds an ended turn. `r.host.onSubmit` pushes `promptSubmit(agent, text, t1)` for every submitted text (as `recordNudgePoll`, hooks_test.go:1484). `ref := r.ref(2)`, `ref.Adopt = s`, `a2, err := r.sm.Spawn(waitCtx(t), ref)`. `r.host.script` returns `AgentWorking` at poll 1; at poll 2 it calls `r.writeSentinel(t, a2, "ok", "")` and pushes `stop(stepAgent)`.
  - when: `out := r.sm.Wait(waitCtx(t), a2, &recObserver{})`.
  - then:
    - `err` is nil;
    - `assertState(t, out, StepOK, "")`;
    - `len(r.events("nudge"))` is `0`;
    - `r.count("SessionHost.Start")` and `r.count("SessionHost.Open")` are each `1`;
    - `hostCallsSince(r, n)` filtered to `SessionHost.Submit` equals exactly one entry, for `stepAgent`, whose text is the full continuation for attempt 2: the `a2` and `s` sentinel paths, and the `ask` sentence matching the rig's ask URL. Here `n` is `len(r.shared.Calls())`, taken right after `hookStallRig`. The expected string is written out literally in the test.

  Covers: a hook session adopted with its old turn history is watched again from the continuation's turn, with no premature nudge, and its new sentinel is judged.
- `TestWatchdogListsTheInPlaceNudgeFirstForAStalledLivePane` (internal/prompts/render_test.go): render `watchdog` with `fullVars()`. The text contains the exact nudge bullet from Change 7. Its index is below the index of `` - `retry` runs it again``. Covers: watchdog.md lists the re-nudge first.
- `TestResolveBlockerPassesNudgeThroughWithItsDescription` (internal/askmcp/watchdog_test.go): following `TestResolveBlockerPassesContinueThroughWithItsDescription` (watchdog_test.go:344). Calling `resolve_blocker` with `{id: b3, action: nudge, addendum: finish the tests}` gives `decision` `authorised`, and the handler sees `b3|nudge|finish the tests`. The `resolve_blocker` description contains `nudge types a continuation into the stalled step's own live session`. Covers: the watchdog can choose the action.

## Left out

- **A `nudge` choice in `blockerOptions`, the maintainer-facing option labels** (blockers.go:41-43). `nudge` is always authorised, so it never reaches the `ask` path that lists those options.
- **Making the blocker timeout (`expire`) pick `nudge`.** The item asks for a watchdog action. With no answer, the hold keeps today's retry → block → stop order.
- **`nudge` for reviewer, land, gatefix, gate-probe or milestone blockers.** The item is about a stalled step. A reviewer is reopened per round, and the others have no step session to continue.
- **`nudge` for a stall in a fix round.** The continuation would ask for the work-half sentinel while the agent was fixing findings; `continue` already resumes at the recorded round.
- **Re-pointing the ask URL in the `askmcp` adapter.** The core-side `askAttempt` match does the job without a port change.
- **A new ADR.** Both ADRs take dated in-place amendments, as ADR-62's own 2026-10-15 amendment does (spec.html:2909).
- **Editing the unattended line of watchdog.md.** The nudge bullet is common to both modes and says it is authorised on the watchdog's word.
- **Rebuilding pane reading for #4.** See `## Assumptions`.

## Assumptions

- **#4 is already done and is not rebuilt.** watchdog.md:178 tells the watchdog to read the excerpt and the pane before acting on a blocker. A stalled session is left standing (tech-design.md:350-351; `TestStillIdleAfterTheNudgeFailsAsStalledAndLeavesTheSession`, session_test.go:1074). Step blockers already carry the pane screen, agent, last tools and transcript (`stepEvidence`, hooks.go:485; loop.go:793-794).
- **`nudge` counts against `watchdog.maxRestarts` like `retry`/`switch`/`continue`.** The item's "retry/continue keep counting" is read as "nothing stops counting". An uncounted nudge could loop without bound (see Summary).
- **`nudge` is authorised on the watchdog's word, attended or unattended.** It is the lightest routine fix, and per ADR-97 routine fixes need no consent.
- **The addendum also goes into attempt N+1's `Vars["Addendum"]`**, as for `retry`. A later fix round of that attempt shows it as the note from the previous attempt.
- **The `restart` event for a nudge keeps today's fields** (`remedy` = blocker id, `citation` = `always`). The `blocker-resolved` event's `action: nudge` and the attempt-2 `agent-named` event with the old agent name record that the step continued in place.
- **The continuation is typed with `m.prompt(agent, text, nil)`**, as the fix-round prompt is (review.go:191). If the agent is working at that moment, the text is queued in its composer, as an idle-quiet nudge already is (session.go:610).

## Gate

`go test ./internal/core/ ./internal/prompts/ ./internal/askmcp/ -run '^(TestAWorkHalfStallRaisesAStepBlockerThatOffersNudgeFirstWithItsAgent|TestAReviewHalfStallRaisesAStepBlockerWithoutNudge|TestANudgedStepTypesTheContinuationIntoTheStalledAgentsPane|TestANudgedStepKeepsItsAgentWorkspaceAndStartingPoint|TestASentinelWrittenAfterANudgeIsJudgedAsTheNewAttemptsOutcome|TestAFailedSentinelAfterANudgeFailsTheNewAttemptAndKeepsItsWorkspaceOpen|TestANudgeAtTheRestartLimitIsRefusedAndBlocksTheStalledPhase|TestANudgedAgentsQuestionReachesTheWatchdogAndItsAnswerIsTypedBack|TestAContinuationThatCannotBeTypedFailsTheNewAttemptAndRaisesANewBlocker|TestANudgedAttemptJudgesItsEvidenceFromTheStalledAttemptsBaseline|TestAHeadMovedBeforeANudgeFailsTheAdoptedAttemptAsCommittedBeforeReview|TestNudgeIsAuthorisedOnTheWatchdogsWordWithItsAddendum|TestNudgeIsRefusedWhileTheStalledAgentIsGoneOrItsStateFails|TestNudgeSharesTheRetryBudget|TestAnAdoptionWhoseRecordFailsNeverTypesTheContinuation|TestAnAdoptedHookSessionTakesTheContinuationAsANewTurnAndEndsOk|TestWatchdogListsTheInPlaceNudgeFirstForAStalledLivePane|TestResolveBlockerPassesNudgeThroughWithItsDescription)$'`
