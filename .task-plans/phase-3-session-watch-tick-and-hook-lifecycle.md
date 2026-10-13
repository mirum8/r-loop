status: planned

## Summary

The run's claude settings gain `SessionStart` and `SessionEnd` hooks, and the tap records a `SessionEnd`'s `reason`. The changes in core:

- **#4 — startup.** A claude step or reviewer agent's first prompt waits up to 10 s after `Host.Start` for a `SessionStart`. If one arrives, the agent is hook-enabled from its first prompt, and that prompt is confirmed by its `UserPromptSubmit` like any later one.
  - If none arrives, the agent is *pinned to herdr*. It gets exactly one warning at that moment, naming it and the hook-tap executable. For the rest of the session, herdr decides its turn end, its prompt confirmation and its answer hand-off, whatever hook events arrive later.
  - Every start begins a fresh hook lifecycle for its agent name. A reviewer reopened under the same name inherits nothing from the process before it.
  - Every prompt typed through `SessionManager.prompt` makes a hook-enabled agent busy at once.
- **#3 — stalls.** In the watch tick, a hook-enabled agent whose turn has ended (`Stop` or `idle_prompt`) with no sentinel gets the nudge on that tick, unless a question or a decline follow-up is pending for it. This is the step's single nudge. The nudge is typed only if the turn is still over under the typing lock. After it, the next turn end with no sentinel, or a grace of herdr quiet, fails the step with `stalled: no response to nudge`. Sessions without hook events keep today's herdr rule unchanged.
- **#5 — gone agents.** A `SessionEnd` with reason `exit`, `logout`, `prompt_input_exit` or `other` ends the step on the next tick, pinned agent or not. The sentinel is judged if one exists; otherwise the step fails with `agent exited: <reason>`. `clear` and `resume` change nothing.

Choices, and why each option won:

- **When the SessionStart is checked.** Taken: block in the start path, before the first prompt, for at most `hookStartWait` (10 s). Rejected: checking the window later, in the watch tick. That would type the first prompt before the decision is made, so a broken tap would either fail the prompt ("not submitted") or flip the agent into hook mode mid-turn.
- **Which SessionStart counts.** Taken: any `SessionStart`, so the tap needs no `source`. The window runs only from `Host.Start` to the first prompt. In it, claude can send only `startup`, or `resume` when a provider's flags resume a session, and either one proves the tap works. Rejected: counting only `startup`, which adds a field across tap, feed and core and would pin a healthy resumed session.
- **How core knows an agent should send hooks.** Taken: a new `ProviderArgs.Hooks bool`, set in `Wiring.resolve` when the provider's kind is `claude` and it gets the run's hook settings through its `settingsFlag`. Rejected: `settingsFlag` alone, which would mark a custom non-claude provider that takes a settings file but runs no claude hooks. Rejected: kind alone, which would mark a custom claude provider launched without the settings. Rejected: setting it in `providers.ToCore`, which intake also calls, with settings that carry no hooks (`internal/app/intake.go:105-111`).
- **Where "pinned to herdr" lives.** Taken: a `pinned` flag on `hookAgent`, honoured inside `hookTurn`. Prompt confirmation, the hand-off gate, settle and the new nudge all read `hookTurn`. Rejected: checking the flag at each of those four call sites, where any one that is missed silently flips the agent back into hook mode. The exit reason is read regardless of the pin, because an exit is not a turn end.
- **How the tap carries the reason.** Taken: a dedicated `Reason` field on the JSONL line and on `core.HookEvent`. Rejected: reusing `Type`, which already means `notification_type` (`internal/core/hooks.go:75`).
- **One turn end, one action.** Taken: the watch keeps `turnEnd`, the hook turn number (`hookAgent.turn`) it last acted on. Rejected: comparing `Stop` timestamps, because an `idle_prompt` after a `Stop` belongs to the same turn but has a later time.
- **"Single nudge" on the hook path.** Taken: a new `watch.nudged`, set by every nudge and read only for hook-enabled sessions. Rejected: never resetting `w.stalled` on herdr `working`, which would drop the `Resumed` transition that ADR-62 and the TUI rely on (`internal/core/session.go:442-451`).
- **The nudge typed into a turn that restarted.** Taken: the hook nudge passes a hand-off gate (`m.handOff`) to `prompt`. `prompt` re-checks the turn under the typing lock after a fresh drain (`internal/core/hooks.go:175-179`). On `errTurnNotOver`, `stallAndNudge` undoes the stall: it records `running` and calls `Resumed`. Rejected: a pre-check outside the lock, which leaves a window and duplicates the gate.
- **A declined dialog's `idle_prompt`.** Taken: `Session.declining`, an `atomic.Int32` counting pending decline follow-ups. While it is above 0, the turn-end nudge holds off. It is a counter because two reviewers of one worker share the owner session (`internal/core/dialogs.go:74`). Rejected: nudging only on `Stop`, which contradicts "(or `idle_prompt`)". Rejected: a bool, which the first follow-up to finish clears while another is still pending.
- **How the warning is delivered.** Taken: `SessionManager` gains a `Face`, the pattern of `GateProbe`, `Watch`, `Remedies` and `Watchdog` (`internal/app/wire.go:544`, `:585`, `:599-600`). `hookStart` emits the warning on it the moment it pins the agent, so the warning is shown exactly once on every path.
  - Rejected: holding the warning on the session until its first watch. That loses it when the start fails, when another reviewer's start fails first (`internal/core/review.go:430-440`), or when a resumed review never watches the worker (`internal/core/runners.go:17-19`).
  - Rejected: journalling it as a new record kind, which no obligation asks for. The existing hook-health warning is face-only too (`internal/core/hooks.go:401-405`).
- **A new process under a reused agent name.** Taken: `hookReset`, called right before every `Host.Start` that `SessionManager` and `ReviewHalf` make.
  - What it does: it drains the feed, then replaces the agent's entry with a fresh one. The fresh entry keeps the edit records for tree blame and stamps `since`. `apply` drops any event stamped before `since`.
  - Why: `ReviewHalf.reopen` closes a failed reviewer's pane and starts a replacement under the same name (`internal/core/review.go:676-713`, with `place` calling `freeAgent` at `:493`). A late line from the old process, such as its `SessionEnd` at pane close, is ignored.
  - Rejected: deleting the entry, which loses those edit records, and which would let a late old-process line create a new entry that looks heard.
- **Which `SessionEnd` reasons end a step.** Taken: exactly the four the item names. Rejected: "anything but clear and resume", which would kill a step on any reason claude adds later.
- **Where the exit check sits.** Taken: first in `tick`, before the sentinel branches and before `Host.State`. An exited agent's turn is over, so its sentinel, ok or not, is judged at once, and herdr is not asked.

## Why this approach

**#3.** The triage proposed this approach:
- after `drainHooks` in the tick, nudge on the same stalled/nudge path when the hooks say the agent is seen, not busy, and has no sentinel;
- then fail on the next turn end or a grace of quiet;
- track which `Stop` triggered, so one turn end yields one nudge.

This plan follows it and settles four points:

- **What "one turn end" is.** It is phase 1's per-agent `turn` counter (`internal/core/hooks.go:55-60`), compared against `watch.turnEnd`.
- **The nudge text.** Today's text says "no activity for 2m0s" (`internal/core/session.go:550-556`), which is false for a nudge sent seconds after a `Stop`. `nudge` takes its cause as a parameter, so the grace nudge stays byte-identical.
- **One function for both nudges.** The grace nudge's body (`internal/core/session.go:465-479`) becomes `stallAndNudge`, which both paths call, because the two must change together.
- **Turns that restart after a `Stop`.** Claude may run tools after a `Stop` without a new prompt (ADR-93, `docs/task-loop-driver/spec.html:4161-4162`). A `PreToolUse` that lands between the tick's decision and the typing would put the nudge into a live turn. `prompt` already re-checks a gate under the typing lock (`internal/core/hooks.go:175-179`), so the hook nudge passes one.

The triage missed a race. A declined dialog's follow-up waits for `idle_prompt` (`internal/core/dialogs.go:159-192`). The new nudge would fire at that same `idle_prompt`, take the typing lock first, and then fail the step at the nudge's own turn end, before the follow-up is typed. `DeliverKeys` releases the question before `declined` runs (`internal/core/dialogs.go:146-150`), so the existing `OpenQuestion` suppression (`internal/core/session.go:430`) does not cover it. Hence the `Session.declining` counter.

**#4.** The triage proposed this approach:
- register `SessionStart`;
- mark a claude agent "expected-hooked" at `Start`;
- set busy when the prompt is typed;
- warn and pin when there is no `SessionStart` within a window;
- apply all of it only for providers whose settings carry hooks, and only when `m.Hooks != nil`.

This plan follows it, but checks the window synchronously before the first prompt instead of on the tick:

- `prompt` decides between herdr and hook confirmation from `hookTurn` (`internal/core/hooks.go:172`). If an expected agent counted as hook-enabled before its first event, a broken tap would fail every claude step's first prompt with "not submitted" (`internal/core/hooks.go:200`). If it did not count, the first prompt would go through herdr, and the agent would turn hook-enabled mid-turn when the `SessionStart` was read.
- Waiting before the first prompt removes both outcomes, and costs nothing in the healthy case: herdr's `Start` returns only after `awaitReady` (`internal/herdr/client.go:252`).

The phase check's three warnings:

1. **Busy before `UserPromptSubmit`.** `expectPrompt` (`internal/core/hooks.go:129-133`) also sets `busy = true` when it registers a non-empty text. Every hook-path prompt through `SessionManager.prompt` calls it, first prompt or not (`internal/core/hooks.go:180`). A `SessionStart` creates the agent's entry with `busy == false`, but no tick runs before the first prompt, because `Wait` follows `Spawn` (`internal/core/runners.go:11-21`).
2. **Late events must not flip a pinned agent.** `hookTurn` returns `seen == false` for a pinned agent. `prompt` (`internal/core/hooks.go:172`), settle (`internal/core/session.go:490`), the new nudge and `handOff.turn` all go through it. `handOff.turn` calls `hookAgent` directly today (`internal/core/hooks.go:238`) and is switched to `hookTurn`.
3. **`SessionStart` from compact, clear or resume.** No `SessionStart` touches `busy` or `turn`, so a mid-turn compaction keeps the agent busy. The source is not recorded, because the health decision is taken once, before the first prompt, and a compact or clear can only follow a prompt. See `## Assumptions`.

**#5.** The triage proposed this approach:
- register `SessionEnd`, record its `reason`, and store the reason on `hookAgent`;
- in the tick, judge the sentinel if one exists, else fail with `agent exited: <reason>`;
- ignore `clear` and `resume`.

This plan follows it with two differences:

- The check comes first in the tick, so an ok sentinel is judged at once, without settle's herdr or hook wait.
- The exit is honoured for a pinned agent too. Item #4 moves only the turn end to herdr, and #5 asks for a next-tick failure whenever the agent reports a fatal `SessionEnd`.

**Reused names.** The triage did not cover a reviewer retry. `ReviewHalf.reopen` (`internal/core/review.go:676-713`) closes the failed pane and calls `place`, whose `freeAgent` (`internal/core/review.go:493`, `internal/core/session.go:227-243`) returns the same name once that pane is gone. That is why every start resets the agent's hook lifecycle first.

**Fixer, intake and the watchdog stay out.** The fixer's prompt goes straight to herdr (`internal/core/fixer.go:384`), and its watch reads only herdr state (`internal/core/fixer.go:433-437`). No hook ever decides anything for it, and it has no handle on the session manager's single hook cursor. Intake's settings carry no hooks (`internal/app/intake.go:105-108`). The watchdog's pane has no `R_LOOP_AGENT` (`internal/hooktap/hooktap.go:30-33`).

**Files.** The phase has no `Files:` line. Every file this plan touches, and why:

- `internal/core/ports.go` — `HookEvent` gains `Reason`.
- `internal/core/types.go` — `ProviderArgs` gains `Hooks`.
- `internal/hooktap/hooktap.go` — the tap reads `reason`.
- `internal/providers/registry.go` — the settings register the two hooks.
- `internal/app/wire.go` — it sets `ProviderArgs.Hooks` and `SessionManager.HookTap`.
- `internal/core/hooks.go`, `internal/core/session.go` — the hook state and the tick.
- `internal/core/review.go` — the reviewer start path resets the hook lifecycle and waits for the `SessionStart` too.
- `internal/core/dialogs.go` — the decline hold.
- Tests:
  - `internal/hooktap/hooktap_test.go`, `internal/app/app_test.go`, `internal/core/hooks_test.go`, `internal/core/session_test.go` — new tests.
  - `internal/providers/settings_test.go` — its expected JSON is the settings contract and must gain the two hooks.
- `docs/task-loop-driver/spec.html`:
  - ADR-93 lists the hooks and says the driver uses them "for six things only" (`:4156`). It also says a first prompt keeps herdr's check (`:4166`) and that a hook that never fires costs only the grace wait (`:4179`).
  - ADR-62's nudge timing changes for hook sessions, as `issues/issues-driver-hooks-2026-10-13-notes.md` flags.
- `docs/task-loop-driver/tech-design.md` — the stall rule (`:325-327`) and the nudge text (`:339-344`).

## Changes

1. **`internal/core/ports.go`** (modify `HookEvent`, `internal/core/ports.go:18-24`)
   - Add the field `Reason string` on the line after `Type`.
   - Serves: #5 the tap records the reason.

2. **`internal/core/types.go`** (modify `ProviderArgs`, `internal/core/types.go:349-355`)
   - Add the field `Hooks bool`: true when the session is a claude session launched with the run's hook settings.
   - Serves: #4 the startup wait and warning only for providers whose settings carry hooks.

3. **`internal/hooktap/hooktap.go`** (modify)
   - The `hook` input struct (`internal/hooktap/hooktap.go:35-48`) gains ``Reason string `json:"reason"` ``.
   - `line` (`internal/hooktap/hooktap.go:17-27`) gains ``Reason string `json:"reason,omitempty"` ``.
   - The `line` literal in `Tap` (`internal/hooktap/hooktap.go:59`) adds `Reason: capped(hook.Reason)`. It reuses `capped` (`internal/hooktap/hooktap.go:71-79`).
   - `Feed.Read` (`internal/hooktap/hooktap.go:129`) copies `Reason: l.Reason` into the `core.HookEvent`.
   - Serves: #5 the reason is recorded. `SessionStart` lines need no change, since the tap records any event name.

4. **`internal/providers/registry.go`** (modify `WriteStatusSettings`, `internal/providers/registry.go:180-193`)
   - Add `"SessionStart": []any{map[string]any{"hooks": hook}}` and `"SessionEnd": []any{map[string]any{"hooks": hook}}` to the `hooks` map. Neither has a matcher, so every source and every reason reaches the tap.
   - Serves: #4 and #5 the run's claude settings register the hooks.

5. **`internal/app/wire.go`** (modify)
   - The `core.SessionManager` literal (`internal/app/wire.go:523-535`) gains `Face: w.Face`. `w.Face` is set above it, at `:505-510`.
   - `writeStatusSettings` (`internal/app/wire.go:802-816`): after `w.Loop.Sessions.Hooks = hooktap.NewFeed(hookFile)`, add `w.Loop.Sessions.HookTap = exe`.
   - `resolve` (`internal/app/wire.go:1033-1040`): replace `return providers.ToCore(...), nil` with:
     1. `args := providers.ToCore(p, model, effort, askURL, mcpConfigPath, dir)`;
     2. `args.Hooks = p.Kind == "claude" && p.SettingsFlag != "" && w.settings != ""`;
     3. `return args, nil`.
   - Serves:
     - #4 the warning reaches the face and names the hook-tap executable.
     - #4 providers without hooks never get the warning: codex has no `settingsFlag`, a custom non-claude kind is excluded, and a run without the settings is excluded.

6. **`internal/core/hooks.go`** (modify)
   1. `hookAgent` (`internal/core/hooks.go:13-22`) gains these fields:
      - `started bool`: a `SessionStart` was applied.
      - `pinned bool`: no `SessionStart` within the window, so herdr decides.
      - `exit string`: the fatal `SessionEnd` reason.
      - `heard bool`: an event of this process was applied.
      - `since time.Time`: the start of this process; events stamped earlier are dropped.
   2. Add `func (h *hookLog) agent(name string) *hookAgent`. It creates `h.agents` and the agent's `&hookAgent{edits: map[string]time.Time{}}` on first use, and returns the entry. `apply` (`internal/core/hooks.go:47-54`) uses it in place of its inline creation. Callers hold `h.mu`.
   3. In `apply`, right after the entry is fetched and before the `wasBusy`/`defer` lines (`internal/core/hooks.go:55-60`), add `if ev.At.Before(a.since) { return }`, then `a.heard = true`. `apply` also gains two cases:
      - `case "SessionStart": a.started = true`. Nothing else changes, so `busy` and `turn` survive a mid-turn compaction or a clear.
      - `case "SessionEnd":` a `switch ev.Reason` with `case "exit", "logout", "prompt_input_exit", "other": a.exit = ev.Reason`. Any other reason changes nothing.
   4. In `expectPrompt` (`internal/core/hooks.go:129-133`), after the existing assignment, add `if text != "" { a.busy = true }`. The deferred clearing call (`internal/core/hooks.go:181`) passes `""` and leaves `busy` alone.
   5. `hookTurn` (`internal/core/hooks.go:285-288`) becomes `func (m *SessionManager) hookTurn(agent string) (seen, busy bool, turn int)`. It reads `busy`, `turn`, `heard` and `pinned` through `hookAgent`, and returns `seen && heard && !pinned, busy, turn`. An entry made by `apply` is always heard, so agents that were never reset behave as today. Its callers change:
      - `prompt` (`internal/core/hooks.go:172`) becomes `if seen, _, _ := m.hookTurn(agent); !seen {`.
      - In `handOff.turn` (`internal/core/hooks.go:237-238`), `var turn int` and the `hookAgent` call are replaced by `seen, busy, turn := g.m.hookTurn(g.agent)`.
   6. Add `func (m *SessionManager) hookExit(agent string) string`. It returns the agent's `exit` through `hookAgent`, pinned or not.
   7. Add `func (m *SessionManager) hookReset(agent string)`:
      1. If `m.Hooks == nil`, return.
      2. Call `_ = m.drainHooks()`, so every line already written is applied to the old process.
      3. Under `m.hooks.mu`, run `a := m.hooks.agent(agent)` and then `*a = hookAgent{edits: a.edits, since: m.now()}`.

      The agent now counts as unheard. Until an event of the new process arrives, `prompt`, settle, the gate and the nudge treat it as an agent without hooks, so a codex replacement of a claude reviewer gets herdr's paths.
   8. Add `var hookStartWait = 10 * time.Second` beside `hookPoll` and `submitWait` (`internal/core/hooks.go:31-34`).
   9. Add `func (m *SessionManager) hookStart(s *Session, hooks bool)`:
      1. If `!hooks || m.Hooks == nil`, return.
      2. Set `deadline := time.Now().Add(hookStartWait)`, then loop. This is the real-clock pattern of `awaitSubmit` (`internal/core/hooks.go:142-153`).
         - `_ = m.drainHooks()`.
         - If `m.hookAgent(s.Agent, func(a *hookAgent) { started = a.started })` reports `started`, return.
         - If `time.Now()` is not before `deadline`, break.
         - Otherwise, `time.Sleep(hookPoll)`.
      3. Under `m.hooks.mu`, set `m.hooks.agent(s.Agent).pinned = true`.
      4. If `m.Face != nil`, emit the warning:
         ```go
         key := s.Ref.Key
         if s.Reviewer != "" {
             key = reviewerKey(key, s.Reviewer)
         }
         m.Face.Emit(Event{
             At:     m.now(),
             Kind:   "warning",
             Phase:  key.Phase,
             Step:   key.Kind,
             Fields: map[string]string{"reason": fmt.Sprintf("hook events: no SessionStart from %s within %s (hook tap %s hook-tap); its turn ends follow herdr", s.Agent, hookStartWait, m.HookTap)},
         })
         ```
         The key is the one `showActivity` uses (`internal/core/hooks.go:353-356`).
   - Serves:
     - #4:
       - the window and the pin;
       - exactly one warning, shown at once;
       - herdr decides even when hook events arrive later (warning 2);
       - busy from typing (warning 1);
       - a `SessionStart` never resets busy (warning 3);
       - each start's health is decided on its own events.
     - #5: the reason is stored; `clear` and `resume` are ignored; a pinned agent's exit still counts; an exit belongs only to the process that reported it.

7. **`internal/core/session.go`** (modify)
   1. New fields:
      - `SessionManager` (`internal/core/session.go:31-50`) gains `HookTap string` after `Hooks`, and `Face Face`.
      - `Session` (`internal/core/session.go:69-83`) gains `declining atomic.Int32` beside `OpenQuestion, Reviewing` (`:74`).
      - `watch` (`internal/core/session.go:307-315`) gains `turnEnd int` and `nudged bool`.
   2. In `start` (`internal/core/session.go:260-305`):
      - Directly before `m.Host.Start` (`:286`), add `m.hookReset(s.Agent)`.
      - Directly after it succeeds and before the `ref.ReviewFrom > 0` return (`:289`), add `m.hookStart(s, args.Hooks)`.
   3. Make this the first thing `tick` does after computing `grace` (`internal/core/session.go:390-393`), before `ReadSentinel` (`:394`):
      ```go
      if reason := m.hookExit(s.Agent); reason != "" {
          if sentinel, err := ReadSentinel(s.Sentinel); !errors.Is(err, ErrNoSentinel) {
              return m.judge(s, sentinel, err), true
          }
          return m.fail(s, "agent exited: "+reason), true
      }
      ```
      This mirrors the `AgentGone` branch (`:413-418`).
   4. In `tick`, after the backstop check (`internal/core/session.go:438-440`) and before `switch state` (`:441`), add:
      ```go
      seen, busy, turn := m.hookTurn(s.Agent)
      if seen && !busy && turn > w.turnEnd && s.declining.Load() == 0 && (s.owner == nil || s.owner.declining.Load() == 0) {
          w.turnEnd = turn
          if w.nudged {
              out := m.fail(s, "stalled: no response to nudge")
              out.Stalled = true
              return out, true
          }
          return m.stallAndNudge(w, now, "your turn ended with no sentinel", m.handOff(s.Agent))
      }
      ```
      It sits below the dialog block (`:419-429`) and the `OpenQuestion`/`Reviewing` early return (`:430-432`). So an open dialog, an `ask_watchdog` turn end and a review half are never nudged.
   5. Change the grace branch:
      - `if w.stalled {` (`internal/core/session.go:460`) becomes `if w.stalled || (seen && w.nudged) {`.
      - The nudge block (`:465-479`) becomes `return m.stallAndNudge(w, now, "no sentinel and no activity for "+grace.String(), nil)`.
   6. Add `func (m *SessionManager) stallAndNudge(w *watch, now time.Time, cause string, gate *handOff) (Outcome, bool)`. It holds today's lines 465-479, with these changes:
      - The first statement becomes `w.stalled, w.nudged, w.quiet = true, true, 0`.
      - The prompt call becomes `err := m.prompt(s.Agent, nudge(cause, s.Ref.AskURL != ""), gate)`.
      - Directly after it, add:
        ```go
        if errors.Is(err, errTurnNotOver) {
            w.stalled, w.nudged = false, false
            if err := m.recordState(now, s, StepRunning); err != nil && !errors.Is(err, errStepEnded) {
                return m.fail(s, "record: "+err.Error()), true
            }
            w.obs.Resumed(s)
            return Outcome{}, false
        }
        ```
      - The existing `if err != nil` failure follows unchanged.
   7. `nudge` (`internal/core/session.go:550-556`) becomes `func nudge(cause string, ask bool) string`, returning `"r-loop: " + cause + ". If your work is done, write the sentinel now. " + blocked`.
   8. In `settle`, line `internal/core/session.go:490` becomes `if seen, busy, _ := m.hookTurn(s.Agent); seen {`.
   - Serves:
     - #3: the nudge on the next tick, counted as the single nudge; the second turn end or a further grace fails; no nudge into a turn that restarted; sessions without hooks are unchanged, because `seen` is false for them and `w.nudged` is read only when `seen`.
     - #4: the per-start reset, the window, and the pinned agent's settle.
     - #5: `agent exited: <reason>` on the next tick without herdr, and the sentinel is judged when present.

8. **`internal/core/review.go`** (modify)
   - `launch` (`internal/core/review.go:533-552`):
     - Directly before `sm.Host.Start` (`:548`), add `sm.hookReset(s.Agent)`.
     - Directly after it succeeds, add `sm.hookStart(s, r.args.Hooks)`.

     This also covers the reopen path (`internal/core/review.go:711`).
   - Serves: #4 for every reviewer agent, and a fresh lifecycle for a reopened reviewer (#4 and #5).

9. **`internal/core/dialogs.go`** (modify)
   - In `DeliverKeys` (`internal/core/dialogs.go:146-148`), the `rule == declineRule` branch becomes `open.s.declining.Add(1)` followed by `go l.declined(open)`. This is still before `l.release(open.s)` (`:150`).
   - In `declined` (`internal/core/dialogs.go:159`), the first statement becomes `defer open.s.declining.Add(-1)`. It runs on every return: typed, gone, the warning path, and the panic recovery.
   - Serves: #3 the turn-end nudge never overtakes a pending decline follow-up, including two reviewers' follow-ups on one owner.

10. **`docs/task-loop-driver/spec.html`** (modify)
    - **ADR-93** (`docs/task-loop-driver/spec.html:4145-4183`):
      - Status line: append ` · amended 2026-10-13 — session start and end, and the turn-end nudge; amends <a href="#adr-62">ADR-62</a>`.
      - The hook list (`:4151-4153`) becomes `<code>UserPromptSubmit</code>, <code>PreToolUse</code>, <code>PostToolUse</code> (edit tools), <code>Notification</code>, <code>Stop</code>, <code>SessionStart</code> and <code>SessionEnd</code> hooks`. After "prints nothing and always exits 0", add "; it keeps a <code>SessionEnd</code>'s <code>reason</code>".
      - "for six things only" (`:4156`) becomes "for nine things only".
      - "a session's first prompt, typed before any hook can fire, keeps herdr's check" (`:4166-4167`) becomes "a claude session's first prompt waits up to 10 s after its start for its <code>SessionStart</code>, and is then confirmed the same way".
      - After "with herdr's not-<code>working</code> rule back once the session has stayed busy past the stall grace" (`:4170-4171`), insert:
        - "; startup health — a claude session launched with these settings that sends no <code>SessionStart</code> within those 10 s gets one warning naming it and the hook tap, and herdr decides its turn ends, prompt confirmation and hand-offs for the rest of the session, whatever hooks arrive later; no <code>SessionStart</code> changes turn state, a typed prompt makes the session busy at once, and every start forgets the hooks of an earlier process under the same name"
        - "; the stall nudge — a session whose turn ends (<code>Stop</code> or <code>idle_prompt</code>) with no sentinel gets <a href="#adr-62">ADR-62</a>'s single nudge at the next tick, unless a question or a decline follow-up is pending for it or a tool starts before it is typed, and fails as stalled at its next turn end with no sentinel or after a grace of quiet"
        - "; the exit — a <code>SessionEnd</code> with reason <code>exit</code>, <code>logout</code>, <code>prompt_input_exit</code> or <code>other</code> ends the step at the next tick, judged by its sentinel when one exists and otherwise failed <code>agent exited: &lt;reason&gt;</code>; <code>clear</code> and <code>resume</code> are ignored"
      - Consequences (`:4179`): "A hook that never fires costs only the grace wait." becomes "A hook tap that never fires costs 10 s per claude session start and one warning, and that session falls back to herdr."
    - **ADR-62** (status line, `docs/task-loop-driver/spec.html:2915-2918`): append ` · amended by <a href="#adr-93">ADR-93</a> (2026-10-13) — a session that reports hooks gets its nudge at the tick after a turn end with no sentinel, with the text <code>your turn ended with no sentinel</code>, and fails at its next such turn end or after a grace of quiet`.
    - Serves: keeps ADR-93 and ADR-62 true.

11. **`docs/task-loop-driver/tech-design.md`** (modify)
    - After "`stallGrace` after the nudge → `failed(stalled: no response to nudge)`;" (`docs/task-loop-driver/tech-design.md:327`), insert: "for a session that reports hooks (ADR-93), a turn end (`Stop` or `idle_prompt`) with no sentinel is nudged at the next tick, and after that one nudge its next turn end with no sentinel, or a further `stallGrace` of quiet, fails it the same way; a `SessionEnd` with reason `exit`, `logout`, `prompt_input_exit` or `other` and no sentinel → `failed(agent exited: <reason>)`;".
    - In **Nudge** (`docs/task-loop-driver/tech-design.md:339-344`), after the quoted text, add: "A turn-end nudge says `your turn ended with no sentinel` in place of `no sentinel and no activity for <grace>`."
    - Serves: keeps the contracts doc true.

## Tests

### Shared fixtures (`internal/core/hooks_test.go`)

**Times.**
- `t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)`.
- `t1 := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)` is the stamp of every `UserPromptSubmit` a host pushes, so it is never before a send on any test clock.

**The starting host: `startingHost`.** It is `struct{ SessionHost; feed *fakeHookFeed; pushes [][]HookEvent; errs []error; n int }`. It stands in for claude writing its startup hook lines when herdr starts it.
- Its `Start` handles the n-th call (0-based): if `n < len(errs)` and `errs[n] != nil`, it returns `Agent{}, errs[n]`. Otherwise it forwards the call, then pushes `pushes[n]` onto `feed` when `n < len(pushes)`.
- Every other method forwards.

**The step rig: `hookStepRig(t *testing.T, onStart ...HookEvent) (*rig, *fakeHookFeed, *fakeFace)`.** It builds `newRig(t)` and sets:
- `r.sm.Hooks = feed`, an empty `&fakeHookFeed{}`;
- `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{onStart}}`, so the step's `onStart` lines appear only after its `Host.Start`, past the start's reset;
- `r.sm.Face = face`, a `&fakeFace{}`;
- `r.sm.HookTap = "/usr/local/bin/r-loop"`;
- `r.sm.Now = (&stepClock{t: t0}).Now`, a clock that never moves, so neither the stall grace nor the backstop can elapse;
- `r.sm.Resolve`, which returns `ProviderArgs{Kind: "claude", Hooks: true}, nil`;
- `r.host.onSubmit`, which pushes `promptSubmit(agent, text, t1)` onto the feed.

The rig's step agent is `rloop-2kuxv-p3-implement`.

**Event helpers.**
- `sessionStart(agent string) HookEvent` is `HookEvent{At: t0, Agent: agent, Event: "SessionStart"}`.
- `sessionEnd(agent, reason string) HookEvent` is `HookEvent{At: t0, Agent: agent, Event: "SessionEnd", Reason: reason}`.

**Timing helpers.** `shortHookStartWait(t)` sets `hookStartWait = 10 * time.Millisecond`, and `fastHookPoll(t)` sets `hookPoll = time.Millisecond`. Both restore the old value with `t.Cleanup`, the pattern of `shortSubmitWait` (`internal/core/hooks_test.go:355-359`).

**Wait context.** `waitCtx(t) context.Context` has a 5 s timeout and is cancelled in `t.Cleanup`. Every hook-rig `Wait` uses it, so a regression ends in `interrupted: context deadline exceeded` instead of hanging.

**Call and event helpers.**
- `warnings(face *fakeFace) []string` returns the `reason` of each emitted `warning` event.
- `pinnedWarning(agent string) string` returns `"hook events: no SessionStart from " + agent + " within 10ms (hook tap /usr/local/bin/r-loop hook-tap); its turn ends follow herdr"`.
- `prefixed(calls []string, prefix string) []string` keeps the calls that start with `prefix`.
- `statesBeforeEach(calls []string, call string) []int` returns, for each occurrence of `call`, how many `SessionHost.State ` calls come before it.

**Nudge texts.**
- `hookNudgeText` is the constant `"r-loop: your turn ended with no sentinel. If your work is done, write the sentinel now. If you are blocked, write a failed sentinel with the reason."`.
- `graceNudgeText` is the same text with `no sentinel and no activity for 2m0s` in place of `your turn ended with no sentinel`.

**Recording the nudge.** Tests that note when the nudge is sent set `r.host.onSubmit` to push `promptSubmit(agent, text, t1)`, and, when `text == hookNudgeText`, to set `actualNudgePoll = r.host.polls`. The hook nudge's gate makes one `Host.State` call before the Submit, so a nudge decided at poll N is submitted at poll N+1.

**The decline-and-watch rig: `declineWatchRig(t *testing.T, agents ...string) (*dialogRig, *fakeHookFeed)`.**
- It builds `newDialogRig(t)` and calls `slowHookPoll(t)`, so a hand-off wakes only through another reader's drain.
- It sets `r.loop.Sessions.Now = (&stepClock{t: t0}).Now` and `r.loop.Sessions.Poll = time.Millisecond`.
- No `Host.Start` runs in this rig, so nothing resets these agents. `r.loop.Sessions.Hooks` is a feed queued with `HookEvent{At: t0, Agent: <agent>, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}` for each agent.
- `r.loop.Sessions.Host` is `&claudeHost{SessionHost: r.dhost, feed: feed, onSubmit: …}`, where `onSubmit` maps:
  - `declinedMessage("d<i+1>")` to `promptSubmit(agents[i], <that text>, t1)`;
  - `hookNudgeText` to `promptSubmit(agents[len(agents)-1], hookNudgeText, t1)`.
- It calls `r.serve(t)`.

**`watchSession(t, r *dialogRig, s *Session) (<-chan Outcome, *recObserver, context.CancelFunc)`.**
- It sets `s.Sentinel = filepath.Join(t.TempDir(), "implement-a1.sentinel")`. The file is never written.
- It starts `r.loop.Sessions.Wait(ctx, s, obs)` in a goroutine, which sends the outcome on the returned channel.

**`idlePrompt(agent string) HookEvent`** is `HookEvent{At: t0, Agent: agent, Event: "Notification", Type: "idle_prompt", Notice: "Claude is waiting for your input"}`.

Each test has `// given`, `// when` and `// then` blocks, and names its compared values `actual…` and `expected…`.

### Tap, settings and wiring

- `TestTapRecordsSessionStartAndTheSessionEndReason` (`internal/hooktap/hooktap_test.go`). Each row taps one line into a temp file and reads it with `NewFeed`.

  | input | expected |
  |---|---|
  | `{"hook_event_name":"SessionStart","source":"startup"}` | `Event == "SessionStart"`, `Reason == ""` |
  | `{"hook_event_name":"SessionEnd","reason":"logout"}` | `Event == "SessionEnd"`, `Reason == "logout"` |

  Covers: #4 a `SessionStart` line; #5 the tap records the reason.

- `TestWriteStatusSettingsPointsTheStatuslineAndHooksAtTheTaps` (`internal/providers/settings_test.go:40`, modified). The expected JSON gains `"SessionEnd":[{"hooks":` + hook + `}],"SessionStart":[{"hooks":` + hook + `}],` between `PreToolUse` and `Stop`, in json's sorted key order.

  Covers: #4 and #5 the run's claude settings register both hooks.

- `TestResolveMarksOnlyAClaudeProviderGivenTheHookSettingsAsHooked` (`internal/app/app_test.go`).
  - Each row builds `w := &Wiring{Registry: providers.NewRegistry(nil, nil, dir), settings: <row settings>}` and calls `w.resolve(<row provider>, "", "", "", "", "")`.
  - `dir` is a temp dir holding `pdev.yaml` with `kind: pdev\ndoneSignal: sentinel\nsettingsFlag: "--settings {settings}"\n`, written by a helper `registryDirWithPdev(t)`.

  | provider | settings | expected `actualHooks` |
  |---|---|---|
  | `claude` | `/run/claude.settings.json` | `true` |
  | `codex` | `/run/claude.settings.json` | `false` |
  | `pdev`, a custom kind with a `settingsFlag` | `/run/claude.settings.json` | `false` |
  | `claude` | `""` | `false` |

  Covers: #4 providers without hooks and runs without the hook settings are never counted as hooked.

### Startup and hook health (#4)

- **`TestAClaudeAgentsFirstPromptIsTypedAfterItsSessionStartIsRead`**
  - Given:
    - `fastHookPoll`.
    - A `hookStepRig(t)` whose feed `next` returns nil on reads 1 to 3, and `sessionStart("rloop-2kuxv-p3-implement")` on read 4.
    - `onSubmit` records `actualReadsAtSubmit = feed.readCount()` and pushes the matching `UserPromptSubmit`.
  - When: `r.spawn(t, 1)`.
  - Then:
    - `actualReadsAtSubmit == 5`: the reset's drain, three start-wait reads, then the prompt's own drain.
    - `prefixed(r.shared.Calls(), "SessionHost.Submit")` is exactly `[SessionHost.Submit rloop-2kuxv-p3-implement "do phase 3"]`.
    - `prefixed(..., "SessionHost.Prompt")` is empty.

  Covers: #4 the `SessionStart` is read before the first prompt, and the first prompt is confirmed by hooks.

- **`TestAClaudeAgentWithoutASessionStartWarnsOnceNamingItAndTheHookTap`**
  - Given: `shortHookStartWait`, `fastHookPoll`, and a table of the `onStart` lines:
    - "no hook events": none;
    - "other hook events only": `HookEvent{At: t0, Agent: agent, Event: "PreToolUse", Tool: "Read", Target: "/repo/a.go"}`.
  - When: each row spawns, sets `r.repo.TreeChanges = []string{"a.go"}`, writes an ok sentinel, scripts herdr `AgentIdle`, and calls `Wait` twice.
  - Then: `warnings(face)` is exactly `[pinnedWarning("rloop-2kuxv-p3-implement")]`.

  Covers: #4 exactly one warning, naming the agent and the hook-tap executable.

- **`TestAWorkerWithoutASessionStartIsWarnedAboutEvenWhenItsStartFails`**
  - Given: `shortHookStartWait`, `fastHookPoll`, `hookStepRig(t)`, and `r.prompts.Err = errors.New("prompt template missing")`.
  - When: `_, actualErr := r.sm.Spawn(context.Background(), r.ref(1))`.
  - Then:
    - `actualErr.Error()` is `"spawn: prompt template missing"`.
    - `warnings(face)` is exactly `[pinnedWarning("rloop-2kuxv-p3-implement")]`.

  Covers: #4 the warning is not lost when the start fails before any watch.

- **`TestAReviewerWithoutASessionStartIsWarnedAboutWhenAnotherReviewersStartFails`**
  - Given:
    - `shortHookStartWait`, `fastHookPoll`, and `r := newReviewRig(t, Reviewer{Provider: "codex"}, Reviewer{Provider: "claude"})`.
    - `feed := &fakeHookFeed{}`, set as `r.sm.Hooks`, with `face := &fakeFace{}` as `r.sm.Face` and `r.sm.HookTap = "/usr/local/bin/r-loop"`.
    - `r.sm.Resolve` wraps the rig's own resolver and sets `Hooks = true` on its result.
    - `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, errs: []error{nil, errors.New("start refused")}}`: the codex reviewer starts, sending no `SessionStart`, and the claude reviewer's start fails.
  - When: `out := r.run()`.
  - Then:
    - `actualReason == "reviewer claude: start refused"`.
    - `warnings(face)` is exactly `[pinnedWarning("rloop-2kuxv-p3-implemen-8lgad-r1")]`.

  Covers: #4 a pending warning is not lost when the review half aborts before any reviewer is prompted.

- **`TestAReopenedReviewerIsNotFailedByItsPredecessorsSessionEnd`**
  - Given:
    - `r := newReviewRig(t, Reviewer{Provider: "codex"})`, with `feed := &fakeHookFeed{}` as `r.sm.Hooks`, and `r.sm.Resolve` wrapped to set `Hooks = true`.
    - `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: ...}`:
      - the first reviewer start pushes `HookEvent{At: t1, Agent: "rloop-2kuxv-p3-implemen-8lgad-r1", Event: "SessionStart"}`;
      - the second pushes `HookEvent{At: time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC), Agent: "rloop-2kuxv-p3-implemen-8lgad-r1", Event: "SessionEnd", Reason: "other"}` (the old process's line at pane close, stamped before the new start), then the same `SessionStart`.
    - `r.behave`: on the first codex review it pushes `HookEvent{At: t1, Agent: "rloop-2kuxv-p3-implemen-8lgad-r1", Event: "SessionEnd", Reason: "logout"}` and writes nothing. On the second it calls `writeReview(t, vars, "ok", 0)`.
  - When: `out, obs := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))`.
  - Then:
    - `actualState == StepOK`.
    - `len(obs.blockers) == 1`, and `obs.blockers[0].Reason == "reviewer codex: agent exited: logout"`.

  Covers: #5 an exit belongs to the process that reported it, including a late line from it; #4 the replacement's own `SessionStart` decides its health.

- **`TestAReopenedReviewerIsHookEnabledThoughItsPredecessorHadNoSessionStart`**
  - Given:
    - `shortHookStartWait`, `fastHookPoll`, and `r := newReviewRig(t, Reviewer{Provider: "codex"})`, with `failCodexOnce(t, r)`.
    - `feed := &fakeHookFeed{}` as `r.sm.Hooks`, `face := &fakeFace{}` as `r.sm.Face`, `r.sm.HookTap = "/usr/local/bin/r-loop"`, and `r.sm.Resolve` wrapped to set `Hooks = true`.
    - `r.sm.Host = &startingHost{SessionHost: r.host, feed: feed, pushes: [][]HookEvent{{}, {{At: t1, Agent: "rloop-2kuxv-p3-implemen-8lgad-r1", Event: "SessionStart"}}}}`.
  - When: `out, _ := r.runRaising(then(Resolution{Action: "retry", By: "watchdog"}, Resolution{Action: "block", By: "watchdog"}))`.
  - Then:
    - `actualState == StepOK`.
    - `prefixed(r.shared.Calls(), "SessionHost.Prompt rloop-2kuxv-p3-implemen-8lgad-r1")` is exactly `[SessionHost.Prompt rloop-2kuxv-p3-implemen-8lgad-r1 "review r1" false 0s]`: the timed-out first process, on herdr.
    - `prefixed(..., "SessionHost.Submit rloop-2kuxv-p3-implemen-8lgad-r1")` is exactly `[SessionHost.Submit rloop-2kuxv-p3-implemen-8lgad-r1 "review r1"]`: the replacement, confirmed by its hooks.
    - `warnings(face)` is exactly `[pinnedWarning("rloop-2kuxv-p3-implemen-8lgad-r1")]`.

  Covers: #4 a pin does not outlive the process it was taken for, and each start gets its own health decision.

- **`TestAnAgentWithoutASessionStartIsJudgedAtHerdrIdleWhenHookEventsArriveLater`**
  - Given:
    - `shortHookStartWait`, `fastHookPoll`, and an empty feed.
    - `r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now` and `r.repo.TreeChanges = []string{"a.go"}`.
    - A script that, on poll 1, writes an ok sentinel and pushes `promptSubmit(agent, "do phase 3", t1)`, so the hooks say busy. It returns `AgentIdle` on every poll.
  - When: spawn, then `Wait`.
  - Then: `actualState == StepOK`, and `r.events("turn-overran-sentinel")` is empty.

  Covers: #4 a pinned agent's turn end is decided by herdr despite later hook events (warning 2, settle).

- **`TestAnAgentWithoutASessionStartIsNudgedOnlyOnHerdrQuietThroughHerdrPrompt`**
  - Given:
    - `shortHookStartWait`, `fastHookPoll`, an empty feed, and the minute-stepping clock.
    - A script that, on poll 1, pushes `promptSubmit(agent, "do phase 3", t1)` and `HookEvent{At: t0, Agent: agent, Event: "Stop"}`. It returns `AgentIdle` always.
  - When: spawn, then `Wait`.
  - Then:
    - `actualReason == "stalled: no response to nudge"`.
    - `prefixed(..., "SessionHost.Prompt")` is exactly the first prompt and `SessionHost.Prompt rloop-2kuxv-p3-implement "<graceNudgeText>" false 0s`.
    - `prefixed(..., "SessionHost.Submit")` is empty.
    - `len(r.events("nudge")) == 1`.

  Covers: #4 a pinned agent keeps the herdr stall rule and herdr prompt confirmation (warning 2).

- **`TestAHandOffToAnAgentWithoutASessionStartFollowsHerdrThoughItsHooksSayBusy`**
  - Given: `shortHookStartWait`, `fastHookPoll`, `feed := &fakeHookFeed{}`, and `sm := hookPromptManager(&fakeSessionHost{States: map[string]AgentState{askingAgent: AgentIdle}}, feed, t0)`.
  - When:
    1. `sm.hookStart(&Session{Agent: askingAgent}, true)`;
    2. `feed.push(promptSubmit(askingAgent, "implement phase 3", t0))`;
    3. `actualState, actualReady, actualErr := sm.handOff(askingAgent).check()`.
  - Then: `AgentIdle`, `true`, `nil`.

  Covers: #4 the hand-off gate stays on herdr for a pinned agent (warning 2).

- **`TestAPromptToAnAgentWithoutASessionStartGoesThroughHerdrPromptThoughItHasHookEvents`**
  - Given: the same setup with `inner := &fakeSessionHost{}`, then `sm.hookStart(...)` and `feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})`.
  - When: `actualErr := sm.prompt(askingAgent, "go", nil)`.
  - Then: `actualErr` is `nil`, and `inner.Calls()` is exactly `[SessionHost.Prompt rloop-p3-implement "go" false 0s]`.

  Covers: #4 prompt confirmation stays on herdr for a pinned agent (warning 2).

- **`TestNoSessionStartWarningForAProviderWithoutHooksOrARunWithoutAHookFeed`**
  - Given: `shortHookStartWait`, `fastHookPoll`, an empty feed, and a table:
    - "provider without hooks": `Resolve` returns `ProviderArgs{Kind: "codex"}`;
    - "run without a hook feed": `Hooks: true`, with `r.sm.Hooks = nil`.
  - When: each row spawns, writes an ok sentinel, sets `TreeChanges`, and calls `Wait`.
  - Then: `warnings(face)` is empty.

  Covers: #4 no warning for providers without hooks or runs without a feed.

- **`TestASentinelWrittenBeforeATypedPromptsUserPromptSubmitIsReadIsNotJudged`**
  - Given: `hookStepRig(t, sessionStart(agent))`, `s := r.spawn(t, 1)`, `feed.push(HookEvent{At: t0, Agent: agent, Event: "Stop"})`, then `r.host.onSubmit = nil`, and `TreeChanges`.
  - When:
    1. Run `r.sm.prompt(agent, "r-loop: answer to q1 (by watchdog): sqlite", nil)` in a goroutine, sending its result on `prompted`.
    2. `waitFor` that Submit to appear in `r.shared.Calls()`.
    3. Write the ok sentinel.
    4. Run `r.sm.Wait(waitCtx(t), s, &recObserver{})` in a goroutine, sending its outcome on `waited`.
    5. `waitFor` `feed.readCount()` to grow by 3.
  - Then:
    - A non-blocking receive from `waited` gets nothing.
    - After `feed.push(promptSubmit(agent, "r-loop: answer to q1 (by watchdog): sqlite", t1), HookEvent{At: t0, Agent: agent, Event: "Stop"})`, `waited` yields `StepOK` within 5 s, and `receiveErr(t, prompted)` is nil.

  Covers: #4 busy from the moment the prompt is typed (warning 1).

- **`TestASessionStartMidTurnDoesNotEndTheTurn`**
  - Given:
    - `hookStepRig(t, sessionStart(agent))` (its `SessionStart` arrives with the start, as every startup one does below), a spawn, and `TreeChanges`.
    - A script that, on poll 1, writes an ok sentinel and pushes `sessionStart(agent)`, as a compaction does. On poll 3 it pushes `HookEvent{At: t0, Agent: agent, Event: "Stop"}`. It returns `AgentIdle` always.
  - When: `Wait`.
  - Then: `actualState == StepOK`, and `r.host.polls` equals `4`. Had the `SessionStart` reset `busy`, the step would be judged at poll 2.

  Covers: warning 3, a mid-turn `SessionStart` does not reset busy.

### Stalls (#3)

Unless a test says otherwise, the rig is `hookStepRig(t, sessionStart(agent))`, then `s := r.spawn(t, 1)` and `r.repo.TreeChanges = []string{"a.go"}`.

- **`TestAHookAgentWhoseTurnEndsWithoutASentinelIsNudgedAtTheNextTick`**
  - Given:
    - Herdr reports `AgentWorking` on every poll.
    - The script pushes the row's turn end on poll 1. On poll 5 it writes an ok sentinel and pushes `Stop`.
    - `onSubmit` records `actualNudgePoll`.
    - The table rows:
      - "Stop": `HookEvent{At: t0, Agent: agent, Event: "Stop"}`;
      - "idle_prompt": `idlePrompt(agent)`.
  - When: `Wait(waitCtx(t), s, obs)`.
  - Then:
    - `actualNudgePoll == 3`: decided at poll 2, the tick after the read, plus the gate's State call.
    - `prefixed(..., "SessionHost.Submit")` is exactly the first prompt and `SessionHost.Submit rloop-2kuxv-p3-implement "<hookNudgeText>"`.
    - `len(r.events("nudge")) == 1`, and `obs.stalled == 1`.

  Covers: #3 the nudge on the next tick, for `Stop` and for `idle_prompt`, with the clock never near the grace.

- **`TestAHookAgentThatEndsASecondTurnWithoutASentinelFailsAsStalled`**
  - Given: `AgentWorking` always. The script pushes `Stop` on poll 1, and again on poll 5.
  - When: `Wait`.
  - Then: `actualState == StepFailed`, `actualReason == "stalled: no response to nudge"`, `out.Stalled` is true, and `len(r.events("nudge")) == 1`.

  Covers: #3 a second turn end fails, and the first nudge counts as the single nudge.

- **`TestAHookAgentQuietForAGraceAfterItsTurnEndNudgeFailsWithoutASecondNudge`**
  - Given:
    - `r.sm.Now = (&stepClock{t: t0, step: time.Minute}).Now`.
    - The script pushes `Stop` on poll 1.
    - Herdr reports `AgentWorking` up to and including poll 4, and `AgentIdle` after.
  - When: `Wait`.
  - Then:
    - `actualReason == "stalled: no response to nudge"`.
    - `r.host.polls == 6`.
    - `len(r.events("nudge")) == 1`, and `obs.resumed == 1`.

    The clock steps a minute per `Now()` call, and how ticks map to polls:

    | tick | polls | herdr | quiet | what happens |
    |---|---|---|---|---|
    | 2 | 2 and 3 (the gate) | working | — | the nudge, plus one more `Now()` call in `expectPrompt` |
    | 3 | 4 | working | reset | the agent resumes |
    | 4 | 5 | idle | 1 minute | nothing yet |
    | 5 | 6 | idle | 2 minutes | quiet reaches the 2-minute grace: the step fails here, not before |

  Covers: #3 a further grace of quiet after the turn-end nudge fails the step at the grace boundary, instead of nudging again.

- **`TestATurnEndNudgeIsNotTypedWhenAToolStartsBeforeIt`**
  - Given:
    - `AgentWorking` always.
    - The script pushes `Stop` on poll 1, and `HookEvent{At: t0, Agent: agent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}` on poll 2: during that tick's own State call, after the drain that read the `Stop`.
    - On poll 3 it writes an ok sentinel and pushes `Stop`.
  - When: `Wait`.
  - Then:
    - `actualState == StepOK`.
    - `prefixed(..., "SessionHost.Submit")` holds only the first prompt, and `len(r.events("nudge")) == 0`.
    - `r.steps()` is exactly `[StepSpawned, StepRunning, StepStalled, StepRunning]`.
    - `obs.stalled == 1`, and `obs.resumed == 1`.

  Covers: #3 no nudge is typed into a turn that restarted between the decision and the typing.

- **`TestAHookAgentsTurnEndDuringAnOpenQuestionIsNotNudged`**
  - Given: `s.OpenQuestion.Store(true)`, `AgentWorking` always. The script pushes `Stop` on poll 1 and writes an ok sentinel on poll 5.
  - When: `Wait`.
  - Then: `actualState == StepOK`, `len(r.events("nudge")) == 0`, and `prefixed(..., "SessionHost.Submit")` holds only the first prompt.

  Covers: an `ask_watchdog` turn end is still not nudged.

- **`TestADeclineFollowUpIsTypedBeforeAnyTurnEndNudge`**
  - Given: `r, feed := declineWatchRig(t, dialogAgent)`, `r.raise(t, r.s)`, and `r.waitDog(t, 1)`.
  - When:
    1. `AnswerDialog("d1", []string{"esc"}, "decline", "")` returns `decisionAuthorised`.
    2. `r.dhost.set(dialogAgent, AgentIdle, "Interrupted · What should Claude do instead?")`.
    3. `waited, obs, cancel := watchSession(t, r, r.s)`.
    4. `waitFor` `feed.readCount()` to grow by 3.
    5. `feed.push(idlePrompt(dialogAgent))`.
    6. `waitFor` one `SessionHost.Submit ` call.
    7. `waitFor` `feed.readCount()` to grow by 3 more.
    8. `cancel()`, then receive from `waited`.
  - Then:
    - `r.calls("SessionHost.Submit ")` is exactly `[dialogAgent + " " + strconv.Quote(declinedMessage("d1"))]`.
    - `r.events("nudge")` is empty, and `obs.stalled == 0`.

  Covers: the decline race; through the real `DeliverKeys`, `declined` and session watch, the follow-up comes first and no nudge, nudge event or stall happens.

- **`TestASecondReviewersDeclineFollowUpStillComesBeforeAnyNudgeWhenTheFirstFinishes`**
  - Given:
    - Reviewer sessions `rvA := &Session{Ref: r.s.Ref, Agent: "rloop-p2-implement-rv-codex", Reviewer: "codex", owner: r.s}` and `rvB := &Session{Ref: r.s.Ref, Agent: "rloop-p2-implement-rv-claude", Reviewer: "claude", owner: r.s}`.
    - `r, feed := declineWatchRig(t, rvA.Agent, rvB.Agent)`, and `r.dhost.set(dialogAgent, AgentWorking, "")`.
    - For each reviewer in turn: `r.dhost.set(<agent>, AgentBlocked, <its screen>)`, `r.raise(t, <rv>)`, `r.waitDog(t, <n>)`, then `AnswerDialog("d<n>", []string{"esc"}, "decline", "")` returns `decisionAuthorised`, then `r.dhost.set(<agent>, AgentIdle, "Interrupted · What should Claude do instead?")`. The screens are `"Run tests?\n> yes"` and `"Edit a.go?\n> yes"`.
    - `waited, obs, cancel := watchSession(t, r, rvB)`.
  - When:
    1. `feed.push(idlePrompt(rvA.Agent))`, then `waitFor` one `SessionHost.Submit ` call.
    2. `waitFor` `feed.readCount()` to grow by 3.
    3. `feed.push(idlePrompt(rvB.Agent))`, then `waitFor` two `SessionHost.Submit ` calls.
    4. `waitFor` `feed.readCount()` to grow by 3 more.
    5. `cancel()`, then receive from `waited`.
  - Then:
    - `r.calls("SessionHost.Submit ")` is exactly `[rvA.Agent + " " + strconv.Quote(declinedMessage("d1")), rvB.Agent + " " + strconv.Quote(declinedMessage("d2"))]`.
    - `r.events("nudge")` is empty, and `obs.stalled == 0`.

  Covers: the hold counts pending follow-ups, so the first one to finish does not release the second.

- **`TestAnAgentWithoutHookEventsIsNudgedAtTheStallGraceAndFailsAGraceLater`** (`internal/core/session_test.go`)
  - Given:
    - `newRig(t)` with its default codex `Resolve`, and `r.sm.Hooks = &fakeHookFeed{queued: []HookEvent{{At: t0, Agent: "rloop-2kuxv-p3-plan", Event: "Stop"}}}`.
    - A spawn, then `r.sm.Now = (&stepClock{t: t0, step: 30 * time.Second}).Now`, so each tick is 30 s.
    - `AgentIdle` always.
  - When: `Wait`.
  - Then:
    - `statesBeforeEach(r.shared.Calls(), "SessionHost.Prompt rloop-2kuxv-p3-implement \"" + graceNudgeText + "\" false 0s")` is `[4]`: no nudge before 2 minutes of quiet, one at the boundary.
    - `r.host.polls == 8`.
    - `actualReason == "stalled: no response to nudge"`.
    - `prefixed(..., "SessionHost.Submit")` is empty.

  Covers: #3 sessions without hook events keep today's herdr stall timing in a run with a hook feed.

- **`TestAnAgentWithoutHookEventsThatWorksAgainRestartsItsQuietAndIsNudgedAfterAFreshGrace`** (`internal/core/session_test.go`)
  - Given: the same as above, but herdr reports `AgentWorking` on polls 2 and 7 and `AgentIdle` otherwise.
  - When: `Wait`.
  - Then:
    - `statesBeforeEach(...)` is `[6, 11]`.
    - `r.host.polls == 15`.
    - `obs.resumed == 1`, and `actualReason == "stalled: no response to nudge"`.

  Covers: #3 working resets quiet, a resumed agent gets today's fresh nudge, and the stall still fails a grace after it. The existing herdr path is pinned exactly.

### Gone agents (#5)

These use the stall rig, with herdr reporting `AgentWorking` on every poll, so the fake never reports gone.

- **`TestASessionEndWithAnExitReasonFailsTheStepWhileHerdrNeverReportsGone`**
  - Given: the script pushes `sessionEnd(agent, <row>)` on poll 1. Rows: `exit`, `logout`, `prompt_input_exit`, `other`.
  - When: `Wait(waitCtx(t), s, &recObserver{})`.
  - Then:
    - `actualState == StepFailed`, and `actualReason == "agent exited: <row>"`.
    - `r.host.polls == 1`: it ends at the next tick without asking herdr.
    - `len(r.events("nudge")) == 0`.

  Covers: #5 the step fails on the next tick naming the reason, from the hook alone, with the clock never near the grace or the backstop.

- **`TestASessionEndWithASentinelPresentJudgesTheSentinel`**
  - Given: on poll 1 the script runs the row's `write func(r *rig, s *Session)` and pushes `sessionEnd(agent, "logout")`. There is no `Stop`. Rows:
    - "ok sentinel": `r.writeSentinel(t, s, "ok", "")` → `actualState == StepOK`, reason prefix `""`.
    - "half-written sentinel": `r.writeRawSentinel(t, s, `{"outcome":`)` → `StepFailed`, reason prefix `"sentinel unreadable: "`.
  - When: `Wait`.
  - Then: the row's expected state, and `actualReason` has the row's expected prefix.

  Covers: #5 with a sentinel present, the sentinel is judged instead.

- **`TestASessionEndFromAnAgentPinnedToHerdrFailsTheStepAtTheNextTick`**
  - Given: `shortHookStartWait`, `fastHookPoll`, and `hookStepRig(t)` with an empty feed, so the agent is pinned. The script pushes `sessionEnd(agent, <row>)` on poll 1. Rows: `exit`, `logout`, `prompt_input_exit`, `other`.
  - When: `Wait`.
  - Then: `actualReason == "agent exited: <row>"`, and `r.host.polls == 1`.

  Covers: #5 holds after a startup timeout too.

- **`TestASessionEndFromAnAgentPinnedToHerdrJudgesItsSentinel`**
  - Given: the pinned rig. The script writes an ok sentinel and pushes `sessionEnd(agent, "logout")` on poll 1.
  - When: `Wait`.
  - Then: `actualState == StepOK`, and `r.host.polls == 1`.

  Covers: #5 sentinel precedence for a pinned agent.

- **`TestASessionEndForClearOrResumeDoesNotEndTheStep`**
  - Given: the script pushes `sessionEnd(agent, <row>)` on poll 1. On poll 3 it writes an ok sentinel and pushes `HookEvent{At: t0, Agent: agent, Event: "Stop"}`. Rows: `clear`, `resume`.
  - When: `Wait`.
  - Then: `actualState == StepOK`.

  Covers: #5 `clear` and `resume` do not fail the step.

### Existing tests

Existing tests must pass unchanged, except `TestWriteStatusSettingsPointsTheStatuslineAndHooksAtTheTaps`, which is modified above. Run `go test ./...` to confirm. Any other failure is a defect in the change, not in the test.

## Left out

- **A blocking `Stop` hook that tells claude to write its sentinel.** The notes leave it out on purpose: it reverses ADR-93's "prints nothing, always exits 0" (`issues/issues-driver-hooks-2026-10-13-notes.md`, the `[#3] Stalls` entry under "Moves architecture").
- **The startup wait and warning for the fixer, intake and the watchdog.**
  - The fixer's prompt and watch use herdr only (`internal/core/fixer.go:384`, `:433-437`), so no hook decides anything for it, and it has no handle on the session manager's single hook cursor.
  - Intake's settings carry no hooks (`internal/app/intake.go:105-108`).
  - The watchdog's pane has no `R_LOOP_AGENT` (`internal/hooktap/hooktap.go:30-33`).
- **The `SessionStart` source.** See `## Assumptions`.
- **Journalling the startup warning.** The existing hook-health warning is face-only (`internal/core/hooks.go:401-405`).
- **A config key for `hookStartWait`.** The item asks for a fixed time.
- **A test of the `HookTap` assignment in `writeStatusSettings`.** It is one line of wiring, and the warning text that uses it is pinned by the core tests.
- **Ignoring a pinned agent's hook events for activity, notices and tree blame.** Those are telemetry, not turn end.
- **Failing on `SessionEnd` reasons the item does not name.** See `## Assumptions`.

## Assumptions

- **Claude writes its `SessionStart` line before it accepts its first prompt.** Herdr's `Start` returns only after `awaitReady` (`internal/herdr/client.go:252`), so the line is normally in `hooks.jsonl` by the first drain. 10 s comfortably covers the tap's 5 s hook timeout (`internal/providers/registry.go:181`).
- **The source is not recorded (the phase check's warning 3).** The health window runs only from `Host.Start` to the first prompt. In that window claude can send only `startup`, or `resume` when a provider's flags resume a session. Either proves the tap runs. A `compact` or `clear` can only follow a prompt, by which time the decision is taken. And because no `SessionStart` changes `busy` or `turn`, a mid-turn compaction keeps the agent busy (`TestASessionStartMidTurnDoesNotEndTheTurn`).
- **A pinned agent stays pinned for the session, even if a `SessionStart` arrives later.** Its `SessionEnd` still ends the step, because #4 moves only the turn end to herdr.
- **`SessionEnd` fails a step only for `exit`, `logout`, `prompt_input_exit` and `other`, as the item lists.** Any other reason is ignored, and herdr `gone` or the stall rule still ends such a step.
- **The tap and the driver share one wall clock.** `hookReset` stamps `since` with `m.now()`, and the tap stamps lines with `time.Now()` (`internal/hooktap/hooktap.go:59`). Phase 1's `expectSince` relies on the same assumption.
- **A reset keeps the old process's edit records.** Tree blame filters them by the round's start (`internal/core/hooks.go:369-371`), so a reopened reviewer's predecessor still gets the blame for its own edits.
- **A hook-feed read error swallowed by `hookStart`'s drain is not warned about again**, as with `prompt`, `hookNotice` and `treeBlame` today (`internal/core/hooks.go:136`, `:291`, `:361`). An unreadable feed then pins the agent, and the agent's warning names the tap.
- **For a hook-enabled session, herdr's `working` still resets `w.stalled` and emits `Resumed`**, as ADR-62 shows it. `w.nudged` alone carries "the single nudge is spent". A nudge withdrawn by the gate records `running` again, because the `stalled` transition was already appended before the action it describes.

## Gate

`go test ./internal/hooktap/ ./internal/providers/ ./internal/app/ ./internal/core/ -run '^(TestTapRecordsSessionStartAndTheSessionEndReason|TestWriteStatusSettingsPointsTheStatuslineAndHooksAtTheTaps|TestResolveMarksOnlyAClaudeProviderGivenTheHookSettingsAsHooked|TestAClaudeAgentsFirstPromptIsTypedAfterItsSessionStartIsRead|TestAClaudeAgentWithoutASessionStartWarnsOnceNamingItAndTheHookTap|TestAWorkerWithoutASessionStartIsWarnedAboutEvenWhenItsStartFails|TestAReviewerWithoutASessionStartIsWarnedAboutWhenAnotherReviewersStartFails|TestAReopenedReviewerIsNotFailedByItsPredecessorsSessionEnd|TestAReopenedReviewerIsHookEnabledThoughItsPredecessorHadNoSessionStart|TestAnAgentWithoutASessionStartIsJudgedAtHerdrIdleWhenHookEventsArriveLater|TestAnAgentWithoutASessionStartIsNudgedOnlyOnHerdrQuietThroughHerdrPrompt|TestAHandOffToAnAgentWithoutASessionStartFollowsHerdrThoughItsHooksSayBusy|TestAPromptToAnAgentWithoutASessionStartGoesThroughHerdrPromptThoughItHasHookEvents|TestNoSessionStartWarningForAProviderWithoutHooksOrARunWithoutAHookFeed|TestASentinelWrittenBeforeATypedPromptsUserPromptSubmitIsReadIsNotJudged|TestASessionStartMidTurnDoesNotEndTheTurn|TestAHookAgentWhoseTurnEndsWithoutASentinelIsNudgedAtTheNextTick|TestAHookAgentThatEndsASecondTurnWithoutASentinelFailsAsStalled|TestAHookAgentQuietForAGraceAfterItsTurnEndNudgeFailsWithoutASecondNudge|TestATurnEndNudgeIsNotTypedWhenAToolStartsBeforeIt|TestAHookAgentsTurnEndDuringAnOpenQuestionIsNotNudged|TestADeclineFollowUpIsTypedBeforeAnyTurnEndNudge|TestASecondReviewersDeclineFollowUpStillComesBeforeAnyNudgeWhenTheFirstFinishes|TestAnAgentWithoutHookEventsIsNudgedAtTheStallGraceAndFailsAGraceLater|TestAnAgentWithoutHookEventsThatWorksAgainRestartsItsQuietAndIsNudgedAfterAFreshGrace|TestASessionEndWithAnExitReasonFailsTheStepWhileHerdrNeverReportsGone|TestASessionEndWithASentinelPresentJudgesTheSentinel|TestASessionEndFromAnAgentPinnedToHerdrFailsTheStepAtTheNextTick|TestASessionEndFromAnAgentPinnedToHerdrJudgesItsSentinel|TestASessionEndForClearOrResumeDoesNotEndTheStep)$'`
