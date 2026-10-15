status: planned

## Summary

The stall watch gets a nudge cap and a bound on background work. It also tells step agents to keep builds and tests in the foreground.

- **Nudge cap (#6).** `watch` counts the nudges it delivers. `stallAndNudge` checks the count first: at `maxNudges = 3` it fails the step with `Stalled=true` and `stalled: no sentinel after 3 nudges` instead of typing a fourth nudge. Both the hook turn-end path and the herdr idle-quiet path reach `stallAndNudge`, so one check covers claude and codex. At a turn end, the cap first runs the same hand-off check the nudge would have run, so a tool that starts before the nudge would be typed keeps the step running (ADR-93).
- **Background work (#12).** The hook `background` flag stops resetting at every turn. Once a session starts a tool with `run_in_background`, the flag stays set until that session ends (or a new one starts). While it is set:
  - quiet time always counts, measured against `backgroundGraces × StallGrace` (3 × 2m = 6m) instead of being frozen;
  - a turn end is still not nudged at once;
  - any turn after a nudge counts as an answer, whether or not it used a tool.

  The #6 cap bounds the answered nudges.
- **Foreground guard (#11).** One paragraph is added to both shared partials, `outcome` and `sentinel`. It says: run builds and tests in the foreground; never end a turn while background work you started is running; the one wait allowed is a task notification the prompt tells you to wait for.

Choices taken:

- **Where the cap lives:** the top of `stallAndNudge`, not one check per path in `tick`. It is the only function that types a nudge, so one counter covers both paths.
- **What counts:** delivered nudges only, incremented after the `errTurnNotOver` and delivery-error returns. A nudge that was never typed is not a nudge the agent ignored.
- **Cap value:** a constant, not a config key. The item asks for a fixed cap, and no story needs it tuned.
- **How background is bounded:** a sticky bool, with quiet counted against k × grace. The other option was a `backgroundAt` time that expires k × grace after the tool starts. Expiry breaks the third #12 criterion: a build that runs past the expiry, answered with text ("still waiting on the build"), would fail at once with `no response to nudge`. The sticky flag keeps every text reply an answer until the cap.
- **k = 3:** the default bound is 6m. That is above the 60s `sleep` the #24 live script runs in the background, and a quarter of a fully answered step's worst case under the 1h plan backstop.
- **Prompt guard placement:** the sentence is copied into both partials, not put in a new shared sub-template. The two partials already repeat their sentinel paragraphs word for word (`internal/prompts/render.go:21-47`), and the guard follows that pattern.

## Why this approach

**#6.** The item and the triage propose a counter on `watch`, checked at the top of `stallAndNudge`, plus amendments to ADR-62 and tech-design. This plan follows that proposal.

- **Why the cap is needed on both paths.**
  - Hook path: a reply that used a tool clears `nudged` and `quiet` (`internal/core/session.go:526-528`), so every stall after it gets a fresh nudge.
  - herdr path: `AgentWorking` clears `stalled` (`internal/core/session.go:545-552`), and the fail check `w.stalled || (seen && w.nudged)` (`internal/core/session.go:565`) needs `seen`. `seen` is false without hooks, so codex is re-nudged every grace, for as long as it keeps going `working`.
- **Why one check covers both.** Both paths end in `stallAndNudge` (`internal/core/session.go:541`, `:570`), the only caller of the nudge prompt (`:580`). This resolves the phase check's second warning: one counter, both paths.
- **Where the count goes up.** After the `errTurnNotOver` return (`:581-584`) and the delivery-error return (`:585-589`). An untyped nudge does not count.
- **Docs.** ADR-62 still says "nudged once" and "at most two grace periods" (`docs/task-loop-driver/spec.html:2904`, `:2922-2923`). tech-design says "after that one nudge" (`docs/task-loop-driver/tech-design.md:337-342`). 1838b45 already broke both without a docs change, so this phase amends them. That justifies the two doc files outside the core code.

**#12.** The triage proposed a `backgroundAt` time kept across turns, quiet counted against k × grace, and `acted || backgroundPending` as an answer. This plan takes the behaviour but not the timestamp:

- **The fault.** The flag resets at every turn (`internal/core/hooks.go:83`). While it is set, the turn-end nudge is skipped (`internal/core/session.go:535`) and quiet stops counting (`:556`). A never-finishing background command therefore waits for the 1h/4h backstop (`internal/config/defaults.yaml:13`, `:37`).
- **Why no timestamp.** Nothing reads one: the bound is measured on `w.quiet`, which already exists.
- **Why the flag never expires.** No hook tells us when background work finishes. An expiring flag turns a text reply after the expiry into an instant `no response to nudge`, which is the fault #12's third criterion forbids. A flag for the rest of the session, bounded by k × grace per stall and the cap per watch, keeps every stall finite.
- **Cost.** Later turn ends in such a session are nudged after 6m of quiet rather than at the next tick. That is still bounded, and the 1838b45 behaviour — a background build is not nudged within the bound — is kept.
- **Phase-check warning one (the title).** Correct: `internal/hooktap/hooktap.go:54` reads `run_in_background` from any tool's input, not just Agent/Bash. This plan fixes the time bound and leaves tool coverage alone.

**#11.** The triage proposed one sentence in both partials, with an exception for a task notification the prompt asks for. This plan follows it.

- The exception keeps `internal/prompts/templates/review.md:27` valid ("A slash command that forks into the background reports through a task notification: wait for it").
- `plan.md:101`, `implement.md:29`, `design.md:57` and `review-plan.md:61` include `sentinel`. `gatefix.md:23`, `gate.md:8` and `milestone.md:8` include `outcome`. `fix.md:34` includes either, by `ReviewedKind`. So the guard has to be in both partials.

## Changes

1. **`internal/core/hooks.go` — modify.**
   - In `hookLog.apply`'s deferred block, line 83 `a.acted, a.background = false, false` becomes `a.acted = false`. `background` (field at `:31`) now stays set for the rest of the session once a `PreToolUse` with `Background` arrives (`:86`).
   - `hookReset` (`:406-416`) already rebuilds `hookAgent` from scratch on every start, so a new session (restart, Attempt+1) starts with the flag cleared. No change there.
   - `hookWork` (`:395-398`) is unchanged.
   - Serves #12 (the flag's effect lasts across turns; a text-only reply while background work is pending).

2. **`internal/core/session.go` — modify.**
   - **Constants.** Add `maxNudges = 3` and `backgroundGraces = 3` to the `const` block at `:19-24`, after `sentinelGrace`.
   - **`watch`.** Add the field `nudges int` after `nudged bool` (`:388`).
   - **`tick`, turn-end branch.** Line 526 `answered := w.nudged && acted` becomes `answered := w.nudged && (acted || background)`. The rest of the branch (`:524-541`) is unchanged.
   - **`tick`, quiet counting.** Lines 555-558: the `case AgentIdle, AgentBlocked, AgentDone:` body becomes `w.quiet += dt` (the `if !background` guard is removed).
   - **`tick`, quiet bound.** Lines 562-570 become:
     ```go
     limit := grace
     if background {
         limit = backgroundGraces * grace
     }
     if w.quiet < limit {
         return Outcome{}, false
     }
     if w.stalled || (seen && w.nudged) {
         out := m.fail(s, "stalled: no response to nudge")
         out.Stalled = true
         return out, true
     }
     return m.stallAndNudge(w, now, "no sentinel and no activity for "+limit.String(), nil)
     ```
   - **`stallAndNudge` (`:573`).** The first statements after `s := w.s` become:
     ```go
     if w.nudges >= maxNudges {
         if gate != nil {
             if _, ready, err := gate.check(); err != nil || !ready {
                 return Outcome{}, false
             }
         }
         out := m.fail(s, fmt.Sprintf("stalled: no sentinel after %d nudges", w.nudges))
         out.Stalled = true
         return out, true
     }
     ```
     The `gate` check keeps ADR-93's rule that a turn-end nudge yields to a tool that starts before it is typed. The check is `handOff.check` (`internal/core/hooks.go:321-338`), the same one `prompt` runs at `internal/core/hooks.go:233-237`, with the same reading of `err != nil || !ready`. When it holds, the cap does not fail a working agent: the tick returns with nothing recorded. That turn end is used up (`w.turnEnd` already moved on at `:525`), so the next turn end or stall checks the cap again. The quiet path passes `gate == nil` (`:570`) and fails at once, because the agent has been idle for the whole bound.
     These statements run before `w.stalled, w.nudged, w.quiet = true, true, 0` and before `recordState`, so the step is never recorded `stalled` for a nudge that is not sent. Add `w.nudges++` right after the `if err != nil { … nudge not delivered … }` block (`:585-589`) and before `m.event(now, s, Event{Kind: "nudge"})` (`:590`).
   - **Patterns reused.** The fail shape matches the existing `stalled: no response to nudge` exits (`:537-539`, `:566-568`), and `fmt` is already imported.
   - **Scope of the count.** The watch is created per `waitAll` (`:419-425`), so the count is per session wait — per half and per review round, as tech-design's "the same rule applies per half and per round" (`docs/task-loop-driver/tech-design.md:345-346`) requires. Reviewers go through the same `tick`, so they are capped too.
   - Serves #6 (count, cap, both paths) and #12 (bounded quiet while background work is pending, any reply as an answer).

3. **`internal/prompts/render.go` — modify.** In both the `outcome` (`:21-33`) and `sentinel` (`:34-47`) definitions, insert one paragraph right after the line `Write the sentinel atomically: … must never see it half-written.` (`:26`, `:39`), separated by one blank line on each side. The paragraph is exactly:

   `Run builds and tests in the foreground, and never end your turn while background work you started is still running: the driver takes a turn that ends with no sentinel for a stall. The one wait allowed is for a task notification this prompt tells you to wait for.`

   The paragraph has no backticks, so it goes straight into the raw string. Serves #11 (the guard; review.md's wait still allowed).

4. **`internal/core/hooks_test.go` — modify.**
   - Add the const `bgNudgeText = "r-loop: no sentinel and no activity for 6m0s. If your work is done, write the sentinel now. If you are blocked, write a failed sentinel with the reason."` beside `graceNudgeText` (`:1003`).
   - Add these fixture helpers after `recordNudgePoll` (`:1484`). They fake herdr and the hook feed, the I/O boundary, and hold every schedule conditional so the test bodies have none.
     - `type pollStep struct { state AgentState; events []HookEvent }`.
     - `scheduleHost(r *rig, feed *fakeHookFeed, steps ...pollStep)`. It sets `r.host.script` so that poll `n` pushes `steps[n-1].events` and returns `steps[n-1].state`. Past the last step it pushes nothing and returns `AgentIdle`.
     - `replyToEveryNudge(r *rig, feed *fakeHookFeed, reply ...HookEvent) *[]int`. It sets `r.host.onSubmit` to append `r.host.polls` to the returned slice, then push `promptSubmit(agent, text, t1)` followed by `reply…`. It is installed after `hookStallRig` spawned the step, so only nudges reach it, and the slice holds the host poll at each nudge.
     - `afterNthNudge(r *rig, n int, steps ...func() AgentState)`. It wraps the current `r.host.onSubmit`, calling it first and recording `r.host.polls` at the `n`-th call. It wraps the current `r.host.script`: before the `n`-th nudge it delegates; at the `k`-th poll after that nudge it returns `steps[k-1]()`; past the last step it returns `AgentIdle`.
     - `toolUse(tool, target string) []HookEvent`, which returns the `PreToolUse` and `PostToolUse` pair for `stepAgent` at `t0`.
     - `backgroundToolUse(target string) []HookEvent`, the same pair for `Bash` with `Background: true` on the `PreToolUse`.
   - Add the six core tests listed under `## Tests`, after `TestAHookAgentQuietPastTheGraceWhileItsBackgroundTaskRunsIsNotStalled`.
   - In that existing test, change `n == replied+8` (`:1600`) to `n == replied+4`. With quiet now counted against the 6m bound, eight idle minutes would draw a second nudge. Four idle minutes are still past the 2m grace and inside the bound, which is what the test is named for.
   - Serves #6 and #12.

5. **`internal/core/session_test.go` — modify.** Add the herdr-path cap test beside `TestAnAgentWithoutHookEventsThatWorksAgainRestartsItsQuietAndIsNudgedAfterAFreshGrace` (`:1443`), using `hooklessStallRig` (`:1419`). Serves #6 (codex sessions are bounded too).

6. **`internal/prompts/render_test.go` — modify.** Add the two prompt tests after `TestPromptsThatWriteNoTestsCarryNoTestRules` (`:1177`), in the table style of `TestPromptsThatWriteTestsCarryTheTestRules` (`:1162`), using `render`, `fullVars` and `with`. Serves #11.

7. **`docs/task-loop-driver/tech-design.md` — modify.**
   - **Stall rule (`:337-342`).** Replace the text from `` `watchdog.stallGrace` (default 2 min), no sentinel, no open question → `stalled`, `` through `` `stallGrace` of quiet, fails it the same way; `` with:

     `` `watchdog.stallGrace` (default 2 min), no sentinel, no open question → `stalled`, and the driver sends one fixed nudge (below); `working` again → `running`; still quiet a further `stallGrace` after the nudge → `failed(stalled: no response to nudge)`; for a session that reports hooks (ADR-93), a turn end (`Stop` or `idle_prompt`) with no sentinel is nudged at the next tick, a turn after the nudge that used a tool answers it, and an unanswered nudge's next turn end with no sentinel, or a further `stallGrace` of quiet, fails it the same way; once a hook session has started any tool with `run_in_background`, for the rest of that session its turn ends are not nudged, its quiet is measured against 3 × `stallGrace`, and any turn after a nudge answers it; an answered nudge returns the step to `running` and a later stall is nudged again, at most 3 nudges per half and round: the stall after the third → `failed(stalled: no sentinel after 3 nudges)`, except a turn end where a tool starts before the nudge would be typed, which keeps the step running; ``

     The text after that (`` a `SessionEnd` with reason … ``) is unchanged.
   - **Nudge (`:355-359`).** After `` `no sentinel and no activity for <grace>` `` add ` (3 × <grace> while background work is pending)`.
   - Serves #6 (docs) and #12 (the line the triage asked for).

8. **`docs/task-loop-driver/spec.html` — modify ADR-62.**
   - **Status line (`:2905-2908`).** Append before `</p>`:

     ` · amended 2026-10-15 — a nudge the agent answers (it works again, a hook session's next turn uses a tool, or any turn once the session has started background work) lets a later stall be nudged again, at most three nudges per half and round, and the stall after the third fails <code>stalled: no sentinel after 3 nudges</code>, unless it is a turn end and a tool starts before the nudge would be typed; a session that has started a tool with <code>run_in_background</code> is not nudged at a turn end and is nudged after three grace periods of quiet instead of one`
   - **Consequences (`:2922-2923`).** Replace `A stall costs at most two grace periods before the loop moves on.` with:

     `An unanswered stall costs at most two grace periods before the loop moves on (two of three grace periods each once the session has started background work), and an agent that answers every nudge without writing its sentinel fails at the stall after its third.`
   - Serves #6 (docs).

## Tests

Core tests run with the existing fakes: `scriptedHost`, `fakeHookFeed`, `stepClock`, `hookStallRig`, `hooklessStallRig`, `assertState`, `assertEqual`, `assertCalls`, `prefixed`, `stepPrompt`, `submitCall`. Each has `// given`, `// when`, `// then` blocks and runs under `waitCtx(t)`.

Every expected value below — nudge polls, final poll count, reason, calls — was checked by running these tests against a scratch copy of Changes 1–2. Each hook test sets `r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now`, so one host poll is about one fake minute, against the 1h backstop of `rig.ref` (`internal/core/session_test.go:115`).

- `TestAHookAgentThatAnswersEveryNudgeWithAToolCallFailsAtTheNudgeCap` (hooks_test.go) — #6 criteria 1 and 3, hook path.
  - **Given:** `hookStallRig`; the 1-minute clock; `scheduleHost(r, feed, pollStep{AgentIdle, []HookEvent{stop(stepAgent)}})`; `actualNudgePolls := replyToEveryNudge(r, feed, append(toolUse("Bash", "go test ./..."), stop(stepAgent))...)`.
  - **Then:**
    - `assertState(t, out, StepFailed, "stalled: no sentinel after 3 nudges")`;
    - `out.Stalled == true`;
    - `*actualNudgePolls == []int{3, 4, 5}`;
    - `r.host.polls == 6`, so it failed at the sixth poll, about 6 fake minutes into a 1h backstop;
    - `len(r.events("nudge")) == 3`;
    - `SessionHost.Submit` calls equal `[submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, hookNudgeText), submitCall(stepAgent, graceNudgeText), submitCall(stepAgent, graceNudgeText)]`.
- `TestANudgeNotTypedBecauseAToolStartedDoesNotCountTowardTheNudgeCap` (hooks_test.go) — #6 criterion 1 edge: only delivered nudges count (`errTurnNotOver`, `internal/core/hooks.go:236`).
  - **Given:** `hookStallRig`, the 1-minute clock, and `scheduleHost` with three steps:
    - `pollStep{AgentWorking, []HookEvent{stop(stepAgent)}}`;
    - `pollStep{AgentWorking, []HookEvent{{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}}}`, which stops the first turn-end nudge from being typed;
    - `pollStep{AgentWorking, []HookEvent{{At: t0, Agent: stepAgent, Event: "PostToolUse", Tool: "Bash", Target: "go test ./..."}, stop(stepAgent)}}`.

    `AgentWorking` keeps quiet at zero until that tool's turn ends; after that, `AgentIdle`. Use the same `replyToEveryNudge` tool reply.
  - **Then:**
    - `assertState(t, out, StepFailed, "stalled: no sentinel after 3 nudges")`;
    - `*actualNudgePolls == []int{5, 6, 7}`;
    - `r.host.polls == 8`;
    - `len(r.events("nudge")) == 3`;
    - Submit calls equal `[submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, hookNudgeText), submitCall(stepAgent, graceNudgeText), submitCall(stepAgent, graceNudgeText)]`.

    The first delivered nudge is the turn-end text: the attempt that was not typed did not use up a slot.
- `TestAToolThatStartsBeforeTheCapStallIsTypedKeepsTheStepRunning` (hooks_test.go) — #6 edge (finding codex-r1-1): after three delivered nudges, a turn end followed by a tool start does not fail the step (ADR-93's exemption, `internal/core/hooks.go:233-237`).
  - **Given:** `hookStallRig`; the 1-minute clock; `scheduleHost(r, feed, pollStep{AgentIdle, []HookEvent{stop(stepAgent)}})`; `replyToEveryNudge` with the `go test` tool reply; then `afterNthNudge(r, 3, …)` with three steps:
    - push `append(toolUse("Bash", "go vet ./..."), stop(stepAgent))...` and return `AgentWorking`;
    - push `HookEvent{At: t0, Agent: stepAgent, Event: "PreToolUse", Tool: "Bash", Target: "go build ./..."}` and return `AgentWorking`;
    - `r.writeSentinel(t, s, "ok", "")`, push the matching `PostToolUse` and `stop(stepAgent)`, and return `AgentWorking`.
  - **Then:**
    - `assertState(t, out, StepOK, "")`;
    - `r.host.polls == 9`;
    - `len(r.events("nudge")) == 3`;
    - Submit calls equal `[submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, hookNudgeText), submitCall(stepAgent, graceNudgeText), submitCall(stepAgent, graceNudgeText)]`.

    Without the `gate` check at the cap the outcome is `failed` `stalled: no sentinel after 3 nudges` at poll 7; this was checked on the scratch copy.
- `TestAnAgentWithoutHookEventsThatWorksAfterEveryNudgeFailsAtTheNudgeCap` (session_test.go) — #6 criteria 2 and 3, herdr path (codex, no hooks).
  - **Given:** `hooklessStallRig(t, func(n int) AgentState { if n%5 == 2 { return AgentWorking }; return AgentIdle })`, whose clock steps 30s per call.
  - **Then:**
    - `assertState(t, out, StepFailed, "stalled: no sentinel after 3 nudges")`;
    - `out.Stalled == true`;
    - `statesBeforeEach(r.shared.Calls(), stepPrompt(graceNudgeText)) == []int{6, 11, 16}`;
    - `r.host.polls == 21`, about 10.5 fake minutes into a 1h backstop;
    - `prefixed(r.shared.Calls(), "SessionHost.Prompt")` equals `[stepPrompt("do phase 3"), stepPrompt(graceNudgeText), stepPrompt(graceNudgeText), stepPrompt(graceNudgeText)]`;
    - `prefixed(r.shared.Calls(), "SessionHost.Submit")` is nil.

    The base code re-nudges every five polls until `backstop 1h0m0s`.
- `TestAHookAgentIdleAfterANeverFinishingBackgroundCommandIsNudgedAfterTheBackgroundBound` (hooks_test.go) — #12 criteria 1 and 2.
  - **Given:** `hookStallRig`; the 1-minute clock; `scheduleHost(r, feed, pollStep{AgentIdle, append(backgroundToolUse("npm run dev"), stop(stepAgent))})`, so the background turn ends at poll 1; `actualNudgePolls := replyToEveryNudge(r, feed)` (the agent never answers).
  - **Then:**
    - `*actualNudgePolls == []int{6}`: the nudge comes about 5 fake minutes after the turn end, inside 3 × `StallGrace`;
    - `r.host.polls == 11`;
    - Submit calls equal `[submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, bgNudgeText)]`;
    - `len(r.events("nudge")) == 1`;
    - `assertState(t, out, StepFailed, "stalled: no response to nudge")`;
    - `out.Stalled == true`.

    The base code makes no nudge and ends `backstop 1h0m0s` at poll 61.
- `TestATextOnlyReplyToANudgeWhileBackgroundWorkIsPendingCountsAsAnAnswerUntilTheNudgeCap` (hooks_test.go) — #12 criterion 3, bounded by #6.
  - **Given:** as above, but `actualNudgePolls := replyToEveryNudge(r, feed, stop(stepAgent))` (a turn with no tool call).
  - **Then:**
    - `assertState(t, out, StepFailed, "stalled: no sentinel after 3 nudges")`;
    - `*actualNudgePolls == []int{6, 11, 16}`;
    - `r.host.polls == 21`;
    - `len(r.events("nudge")) == 3`;
    - Submit calls equal `[submitCall(stepAgent, "do phase 3"), submitCall(stepAgent, bgNudgeText), submitCall(stepAgent, bgNudgeText), submitCall(stepAgent, bgNudgeText)]`.
- `TestAHookAgentQuietPastTheGraceWhileItsBackgroundTaskRunsIsNotStalled` (hooks_test.go, existing; sentinel moved to `replied+4`) — #12: within the bound, background work that runs past the grace still draws no extra nudge.
  - **Then:** the state is `StepOK` and there is 1 `nudge` event.
- `TestStepPromptsTellTheAgentToRunBuildsAndTestsInTheForeground` (render_test.go) — #11 criteria 1 and 3.
  - **Given:** a table of `plan`, `implement`, `fix` and `gatefix`, each rendered with `fullVars()`, one subtest per name.
  - **Then:** each `actual` render contains `Run builds and tests in the foreground` and `never end your turn while background work you started is still running`.
- `TestTheForegroundGuardStillLetsAReviewerWaitForItsForkedCommand` (render_test.go) — #11 criterion 2.
  - **Given:** `review` rendered with `fullVars()` (`ReviewRan` false).
  - **Then:** `actual` contains both `The one wait allowed is for a task notification this prompt tells you to wait for.` and `A slash command that forks into the background reports through a task notification: wait for it`.

## Left out

- **A config key for the cap or for k:** the item asks for a fixed cap, and no story needs either tuned.
- **A `backgroundAt` timestamp, or an expiring flag:** the bound is measured on the existing `w.quiet`, and expiry would make a text reply during a long build fail at once, against #12's third criterion.
- **Detecting when background work finishes:** no hook reports it, and the sticky flag plus k × grace plus the cap bounds every stall without it.
- **Widening hooktap's background detection:** it already reads `run_in_background` from any tool (`internal/hooktap/hooktap.go:54`); #12's title misstates the defect.
- **Mentioning background work in the nudge text:** no criterion asks for it; the cause already names the quiet time it waited.
- **A shared sub-template for the guard sentence:** the two partials already repeat their paragraphs word for word; one more repeated sentence follows that pattern.
- **Editing ADR-93's description of the single turn-end nudge (`spec.html:4127-4129`) or the "A step stalls" story (`:990-998`):** the item names ADR-62 and tech-design. ADR-62's dated amendment governs the stall rule, and earlier ADRs stay as written.
- **Moving the inline schedules of the herdr cap test and the existing background test into helpers:** both keep the inline `script` style their neighbours use (`internal/core/session_test.go:1443`, `internal/core/hooks_test.go:1578`), and their assertions pin the behaviour either way.
- **A separate test that reviewers are capped:** reviewers run the same `tick` and `stallAndNudge`, so no separate code path exists to pin.

## Assumptions

- **The cap is 3 delivered nudges per watch (one half, one review round).** The step fails at the stall after the third with `stalled: no sentinel after 3 nudges`. An unanswered nudge still fails sooner with `stalled: no response to nudge`.
- **The background bound is k = 3 × `StallGrace`.** At the default 2m that is 6m, above #24's 60s background `sleep`. A fully answered step with background work then fails after about 4 × 6m, under the 1h plan backstop.
- **herdr reports a claude pane idle while only background work runs.** With a set flag, a turn end is no longer nudged at the next tick, so the bound runs on herdr-idle quiet. The 1838b45 test (`internal/core/hooks_test.go:1578`) models it the same way.
- **The background flag stays set until the session ends.** A later turn end in that session is nudged after 6m of quiet rather than at the next tick.
- **Phase-check warning one (the title)** is handled under `## Why this approach`: the fix is the time bound, not tool coverage.
- **Phase-check warning two (the herdr path and ADR-62)** is resolved by the cap at the top of `stallAndNudge` and the ADR-62/tech-design amendments in Changes 7–8.

## Gate

`go test ./internal/core/ -run '^(TestAHookAgentThatAnswersEveryNudgeWithAToolCallFailsAtTheNudgeCap|TestANudgeNotTypedBecauseAToolStartedDoesNotCountTowardTheNudgeCap|TestAToolThatStartsBeforeTheCapStallIsTypedKeepsTheStepRunning|TestAnAgentWithoutHookEventsThatWorksAfterEveryNudgeFailsAtTheNudgeCap|TestAHookAgentIdleAfterANeverFinishingBackgroundCommandIsNudgedAfterTheBackgroundBound|TestATextOnlyReplyToANudgeWhileBackgroundWorkIsPendingCountsAsAnAnswerUntilTheNudgeCap|TestAHookAgentQuietPastTheGraceWhileItsBackgroundTaskRunsIsNotStalled)$' && go test ./internal/prompts/ -run '^(TestStepPromptsTellTheAgentToRunBuildsAndTestsInTheForeground|TestTheForegroundGuardStillLetsAReviewerWaitForItsForkedCommand)$'`
