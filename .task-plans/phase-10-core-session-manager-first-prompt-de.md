status: planned

## Summary

This phase makes two changes.

**#10, step blocker evidence.** A step blocker (`awaitRestart`, `internal/core/loop.go:795`) now carries two things from the failed attempt's session (`out.Session`):
- the normalised end of its pane screen, in `Blocker.Excerpt`, as reviewer blockers already do;
- an evidence block in a new `Blocker.Evidence` field: `agent: <name>`, `last tools: <tool target; …>` (the last five `PreToolUse` calls hooks saw) and `transcript: <path>` when it is known.

`blockerText` routes the evidence as its own paragraph between the actions line and the screen. To produce the tool history, `hookAgent` keeps a bounded ring of the last five `PreToolUse` tool/target pairs. `Stop` and `idle_prompt` do not clear the ring. `Session` remembers the transcript path that `recordTranscript` journals, whether it came from claude's hooks or from codex's rollout. `ReviewHalf.screen` moves to `SessionManager.screen`, so both blocker kinds read the pane the same way.

**#21, guarded first-prompt re-type.** `promptReceived` reads `Host.State` only after a failed `awaitRollout` wait, never straight after `Host.Prompt`. Each try has two such points: before the Enter and before the re-type.
- `idle` or `done`: it keeps today's Enter or re-type.
- Any other answer (`working`, `blocked`, `unknown`, `gone`, or a state error): the prompt counts as received. The session is marked `received`, and one warning names the agent and the rollout lookup that failed.
- Later prompts to that session (nudges, answers) are no longer rollout-confirmed, so the warning is emitted once per session.
- When the agent stays idle through every try, the existing `prompt to <agent> not received` error is unchanged.

Choices:
- **Where the evidence goes: a new `Blocker.Evidence` field, or folded into `Reason` or `Excerpt`.** A new field. `hold` collapses `Reason` to one line (`internal/core/blockers.go:120`), and `Reason` is the `blocked-on` event's TUI text. `blockerText` cuts `Excerpt` from the front to 4096 bytes (`internal/core/blockers.go:274-280`), so evidence placed before the screen could be cut away.
- **Tool history: a ring on `hookAgent`, or a scan of the hook feed at blocker time.** The ring. `HookFeed.Read` is consumed once by `drainHooks` (`internal/core/hooks.go:123-143`), so nothing is left to scan later.
- **Transcript path: a field on `Session` set by `recordTranscript`, `hookAgent.transcript`, or a store scan of `agent-transcript` events.** The `Session` field. `hookAgent.transcript` only holds claude's path, while codex's path comes from `Rollouts.Find` inside `recordTranscript` (`internal/core/session.go:357-372`). A store scan would mean a full `Store.Load` per blocker. `stepEvidence` calls `recordTranscript` again after its own hook drain, so a path first heard after `Finish` untracked the session is not lost.
- **The screen helper: lift it to `SessionManager`, or copy it into loop.go.** Lift it. It is the same code with the same guards (`internal/core/review.go:643-652`), and both callers already hold a `SessionManager`.
- **After a state-confirmed first prompt: keep rollout-confirming later prompts, or treat the session as received.** Treat it as received. The prompt now counts as received, and `unreceived` exists only to confirm the first prompt. Keeping it would add a 10 s wait and another warning to every nudge and answer.
- **A state error: fail the delivery, or count the prompt as received.** Count it as received, with the error in the warning. #21 says a re-type happens only on `idle` or `done`. The watch then fails a timed-out or gone agent (`internal/core/session.go:494-501`).
- **Warning channel: `Face.Emit` like `hookStart`, or `Session.warnings` (merged into the outcome at `Finish`).** `Face.Emit`. The step continues and the warning concerns delivery now. `Session.warnings` would surface only when the step ends. The `hookStart` emit block is extracted into `SessionManager.warn` and called from both places.

## Why this approach

**#10.** The triage proposed two things: lift `ReviewHalf.screen` into a `SessionManager` helper and fill `Excerpt` from `out.Session`, and add a ring of recent `PreToolUse` tool/target pairs plus an evidence line with the agent, the tools and the transcript path. This plan follows both. It adds one part the triage left open: the evidence goes in its own `Blocker.Evidence` field, because neither existing field can carry it.
- `Reason` is whitespace-collapsed by `hold` (`internal/core/blockers.go:120`) and shown on a TUI row.
- `Excerpt` is front-truncated (`internal/core/blockers.go:274-280`).

The pane is still up when `awaitRestart` raises the blocker:
- `Finish` (`internal/core/session.go:824-870`) ends the session but neither interrupts nor closes it.
- The failed attempt's workspace is closed only when the next attempt starts (`internal/core/loop.go:1087-1089`).

So `Host.Screen` at raise time reads the screen the agent stalled on. A dead agent's `Screen` error gives an empty excerpt, the same as for reviewers. The ring is needed because `hookAgent` holds only the current `tool`/`target`, and `Stop` and `idle_prompt` clear them (`internal/core/hooks.go:98-117`). The transcript path is needed on `Session` because codex's comes only from `Rollouts.Find` (`internal/core/session.go:362-363`).

**#21.** The triage proposed reading `Host.State` after each failed `awaitRollout`, never straight after `Prompt`. `idle`/`done` keeps the Enter or re-type; `working`/`blocked` counts as received with one warning. The check sits before the Enter as well as before the re-type. This plan follows that, with two additions:
- **What "otherwise" covers.** `unknown`, `gone` and a state error also count as received, because #21 allows a re-type only on `idle` or `done`.
- **No later rollout confirmation.** A session counted received this way is no longer rollout-confirmed for later prompts (`unreceived`, `internal/core/hooks.go:262-274`). Without that, every nudge to a codex agent whose rollout lookup is broken would wait 10 s, read the state and warn again.

Reading the state only after the 10 s `submitWait` keeps 429975b's protection. herdr's `Host.Prompt` returns as soon as herdr sees the agent `working` or `blocked` (`internal/herdr/client.go:375-377`), and that brief startup state is exactly what 429975b stopped trusting as delivery.

**Files outside `hooks.go` and `loop.go`:**
- `types.go` holds `Blocker`.
- `blockers.go` holds `blockerText`.
- `session.go` holds `Session` and `recordTranscript`.
- `review.go` loses the screen helper that moves to `SessionManager`.
- `docs/task-loop-driver/tech-design.md:1187-1195` states the `core.Blocker` contract and the routed text, which this phase changes.
- The tests go where their rigs live: `transcript_test.go` for the codex first-prompt rigs and `blockers_test.go` for the source-rig blocker tests.

## Changes

1. **`internal/core/types.go` (modify).**
   - `Blocker` (`types.go:110-113`) becomes `Source, Phase, Step, Reason, Excerpt, Evidence string; Actions []string`. `Evidence` is a step blocker's agent, recent tools and transcript lines. Serves #10.

2. **`internal/core/blockers.go` (modify).**
   - In `blockerText` (`blockers.go:272`), right after the header and actions line, add `if b.Evidence != "" { text += "\n\n" + b.Evidence }`, before the excerpt block. The excerpt and versions logic is unchanged.
   - The routed text becomes `<header>: <reason>\nactions: …; resolve it with resolve_blocker` + `\n\n<evidence>` + `\n\n<excerpt>` + `\n\n<versions>`, each part only when non-empty.
   - Serves #10.

3. **`internal/core/session.go` (modify).**
   - `Session` (`session.go:78-96`) gets two fields:
     - `received atomic.Bool`, next to `transcribed`. It is set when herdr's state alone confirmed the first prompt. Serves #21.
     - `transcript string`, guarded by `s.mu`, holding the journalled transcript path. Serves #10.
   - `recordTranscript` (`session.go:357`): right after the `CompareAndSwap` guard at `session.go:366` succeeds, set `s.mu.Lock(); s.transcript = path; s.mu.Unlock()`. Serves #10.
   - Add `func (m *SessionManager) screen(s *Session) string`, the body of `ReviewHalf.screen` (`review.go:643-652`) with the guard widened to `s == nil || s.Agent == "" || s.Pane == ""`. It returns `normaliseScreen(raw)` (`dialogs.go:291`), or `""` on a `Host.Screen` error. Serves #10.

4. **`internal/core/review.go` (modify).**
   - Delete `ReviewHalf.screen` (`review.go:643-652`).
   - `review.go:622` becomes `b.Excerpt = h.Sessions.screen(r.s)`.
   - Reviewer behaviour is unchanged.

5. **`internal/core/hooks.go` (modify).**
   - Add `type hookCall struct{ tool, target string }` and `const recentTools = 5`.
   - `hookAgent` (`hooks.go:13-32`) gets `recent []hookCall`.
   - In `apply`, in the `PreToolUse` case (`hooks.go:98-99`), add `a.recent = append(a.recent, hookCall{ev.Tool, ev.Target})`, then `if len(a.recent) > recentTools { a.recent = a.recent[1:] }`. `Stop` and `idle_prompt` leave `recent` alone. `hookReset` (`hooks.go:406-416`) already rebuilds the agent with only `edits` and `since`, so a new session under that name starts with an empty ring. Serves #10.
   - Add `func toolCall(c hookCall, dir string) string`:
     - For `c.tool == "Bash"`: `target := strings.Join(strings.Fields(strings.SplitN(strings.TrimSpace(c.target), "\n", 2)[0]), " ")`, the command's first line with its whitespace collapsed, as `activityText` does (`hooks.go:480-481`).
     - For a tool whose target is a file path, `c.tool == "Read" || isEditTool(c.tool)` (`hooks.go:52-58`, the tools `activityText` shows as paths at `hooks.go:476-479`): `target := relPath(dir, c.target)` (`hooks.go:497`), otherwise kept verbatim. A file path can hold repeated spaces, and collapsing them would name a different file.
     - For any other tool (Grep and Glob patterns, a Task description, an MCP tool): `target := c.target`, unchanged. The hook tap fills these from `pattern` or `description` (`internal/hooktap/hooktap.go:61-65`), which are not paths, so `relPath` would rewrite an absolute-looking pattern into a different search.
     - Return `c.tool` when `target == ""`, else `c.tool + " " + target`.
   - Add `func (m *SessionManager) stepEvidence(s *Session) string`:
     - Return `""` when `s == nil || s.Agent == ""`.
     - `lines := []string{"agent: " + s.Agent}`.
     - When `m.Hooks != nil`: call `_ = m.drainHooks()` (the `hookNotice` pattern, `hooks.go:458`), then `m.recordTranscript(s)` (`session.go:357`). `Finish` drained hooks and recorded transcripts before it untracked the session (`session.go:825-827`), so a hook whose transcript path arrives after that is only seen by this drain. `recordTranscript` copies it into `s.transcript` and journals it, as it would have done in `Finish`. Collect `toolCall(c, s.Dir)` for each `c` in the agent's `recent` through `m.hookAgent`, oldest first. Append `"last tools: " + strings.Join(calls, "; ")`, or `"last tools: none seen by hooks"` when there are none.
     - Read `s.transcript` under `s.mu`; when it is non-empty, append `"transcript: " + path`.
     - Return `strings.Join(lines, "\n")`.
     - Serves #10.
   - Extract `func (m *SessionManager) warn(s *Session, reason string)` from `hookStart`'s tail (`hooks.go:443-456`): return if `m.Face == nil`; key `s.Ref.Key`, or `reviewerKey(key, s.Reviewer)` for a reviewer; emit `Event{At: m.now(), Kind: "warning", Phase, Step, Fields{"reason": reason}}`. `hookStart` calls `m.warn(s, fmt.Sprintf("hook events: no SessionStart from %s within %s (hook tap %s hook-tap); its turn ends follow herdr", s.Agent, wait, m.HookTap))`, so its text is unchanged. Serves #21.
   - `unreceived` (`hooks.go:268`): the condition becomes `s.Agent == agent && s.rollout && !s.transcribed.Load() && !s.received.Load()`. Serves #21 (one warning, no re-confirmation).
   - Add `func (m *SessionManager) takenUp(s *Session) bool`:
     ```go
     state, err := m.Host.State(s.Agent)
     if err == nil && (state == AgentIdle || state == AgentDone) {
         return false
     }
     seen := "reports it " + string(state)
     if err != nil {
         seen = "state failed (" + err.Error() + ")"
     }
     s.received.Store(true)
     m.warn(s, fmt.Sprintf("prompt to %s: no rollout shows it (lookup by %s found nothing); herdr %s, so it counts as received", s.Agent, s.Sentinel, seen))
     return true
     ```
     Serves #21.
   - `promptReceived` (`hooks.go:290-306`): both `if m.awaitRollout(s) { return nil }` checks become `if m.awaitRollout(s) || m.takenUp(s) { return nil }`. Nothing else changes: the `Host.Prompt` and `SendKeys` error returns, `promptTries` and the final `not received` error stay as they are. The state is therefore read only after a full failed rollout wait: before the Enter, and before the next re-type. Serves #21.

6. **`internal/core/loop.go` (modify).**
   - In `awaitRestart`, after `reason` is set (`loop.go:786-789`) and before the `for` loop, add `excerpt := l.Sessions.screen(out.Session)`, then `evidence := l.Sessions.stepEvidence(out.Session)`. The screen is read first, so evidence's hook drain also takes in anything the agent did up to that read.
   - The blocker at `loop.go:795` becomes `Blocker{Source: sourceStep, Phase: key.Phase, Step: key.Kind, Reason: reason, Excerpt: excerpt, Evidence: evidence, Actions: stepActions}`.
   - Both are taken once per failed attempt: a blocker raised again after a failed `continue` re-read describes the same attempt.
   - Serves #10.

7. **`docs/task-loop-driver/tech-design.md` (modify).**
   - At `tech-design.md:1187-1190`, the contract becomes `core.Blocker{Source, Phase, Step, Reason, Excerpt, Evidence string; Actions []string}`. Add after the `Excerpt` sentence: "`Evidence` is a step blocker's `agent: <name>`, `last tools: <tool target; …>` (the last five `PreToolUse` calls, when a hook feed is wired) and `transcript: <path>` (when journalled), one per line; a step blocker's `Excerpt` is its failed attempt's pane screen."
   - At `tech-design.md:1194-1195`, the routed text becomes "… with `resolve_blocker`, a blank line and the evidence, a blank line and the excerpt …".
   - Serves #10 (contract kept true).

8. **Existing tests (modify).**
   - `freshCodexRig` (`transcript_test.go:459`): the host script becomes `func(int) AgentState { return AgentIdle }`, an agent that never leaves idle. Its old first-poll `working` modelled herdr's startup state, which is no longer read at that point. Under the new design the first state read happens after the rollout wait, so `working` there would mean "taken up". The four existing `TestAFreshCodexPrompt…` tests keep their assertions.
   - `TestAFailedStepRaisesAStepBlockerAndRetryRerunsItWithTheAddendum` (`blockers_test.go:808`): copy `(*seen)[0].b`, set the copy's `Evidence` to `""`, and compare the copy with the existing expected `Blocker`. The test is about the retry and its addendum, not the evidence.

## Tests

Test-only helpers, added to `internal/core/blockers_test.go`:
- `type evidenceHost struct { *eventsHost; feed *fakeHookFeed; agent string; hooks, screenHooks []HookEvent; screen string; screenErr, paneErr error }`.
  - `Prompt` delegates to `eventsHost.Prompt`. Then, when `role(agent) == h.agent` and the text does not start with `r-loop:`, it pushes each of `hooks` to `feed` with `Agent` set to the real agent name.
  - `Screen` records `SessionHost.Screen <agent>`, pushes each of `screenHooks` to `feed` with `Agent` set to the real agent name (a hook that arrives at blocker time), and returns `screen, screenErr`.
  - `AgentPane` returns `"", paneErr` when `paneErr != nil && role(agent) == h.agent`, else delegates.
- `func evidenceRig(t *testing.T, agentRole, behaviour string) (*eventsRig, *evidenceHost, *[]raised)`. It builds `newSourceRig(t, resolveAs("block"))`, sets `r.host.behaviour[agentRole] = behaviour`, installs `&evidenceHost{eventsHost: r.ehost, agent: agentRole}` as `r.loop.Sessions.Host`, and returns them.

Every test has `// given`, `// when` and `// then` blocks. In the `#21` tests, `P` is the call string `SessionHost.Prompt rloop-2kuxv-p3-implement "do phase 3" false 0s` and `E` is `SessionHost.SendKeys rloop-2kuxv-p3-implement enter`. Call lists are taken with `callsOf(r, "SessionHost.Prompt ")`, `callsOf(r, "SessionHost.SendKeys ")` and `callsOf(r, "SessionHost.State ")` and compared with `assertEqual` against hard-coded lists; an empty list is `[]string(nil)`. In the `#10` tests, the agent name in `Evidence` and routed text goes through `role(...)`. Each `#10` test first asserts `assertEqual(t, "blockers", len(*seen), 1)`.

**#21, in `internal/core/transcript_test.go`, built on `freshCodexRig`:**
- `TestAFreshCodexPromptNoRolloutEverShowsIsRetypedThenFailsTheSpawn`: the existing test, run with the rig's new always-idle script. Pins: an agent that never leaves idle with no rollout still fails the spawn with `prompt to <agent> not received`, after 3 prompts and 3 Enters. Its two count assertions become `assertEqual` on the call lists: prompts `[P, P, P]`, Enters `[E, E, E]`. Covers #21 criterion 4.
- `TestAFreshCodexPromptNoRolloutShowsWhileHerdrReportsItBusyIsTypedOnceAndTheSpawnSucceeds`: a table with rows `working`, `blocked`, `unknown` and `gone`. Each row scripts that state for every poll and spawns with no rollout. Asserts: the spawn error is nil, prompts `[P]`, Enters `[]string(nil)`. Covers #21 criteria 1 and 2 (states other than idle/done count as received).
- `TestAFreshCodexPromptWhoseHerdrStateCannotBeReadCountsAsReceived`: `r.host.StateErr = errors.New("herdr: timed out")`, Face set. Asserts: the spawn error is nil, prompts `[P]`, Enters `[]string(nil)`, and `warnings(face)` equals `["prompt to rloop-2kuxv-p3-implement: no rollout shows it (lookup by <runDir>/phase-3/implement-a1.sentinel found nothing); herdr state failed (herdr: timed out), so it counts as received"]`. The test builds the path with `filepath.Join(r.runDir, "phase-3", "implement-a1.sentinel")`. Covers #21 criterion 2 (the error path).
- `TestAFreshCodexPromptHerdrReportsWorkingAfterTheEnterIsNotRetyped`: the script returns `idle` on poll 1 and `working` after that; no rollout. Asserts: the spawn error is nil, prompts `[P]`, Enters `[E]`. Covers #21 criterion 2 (the check before the re-type, after an Enter).
- `TestAFreshCodexPromptHerdrReportsDoneIsRetypedUntilTheTriesRunOut`: the script is always `done`; no rollout. Asserts: the spawn error contains `prompt to rloop-2kuxv-p3-implement not received`, prompts `[P, P, P]`, Enters `[E, E, E]`. Covers #21 criterion 2 (`done` keeps the re-type).
- `TestAFreshCodexPromptCountedReceivedByHerdrStateWarnsOnceNamingTheAgentAndTheRolloutLookup`: the script is always `working`, Face set, no rollout. Asserts: `warnings(face)` equals `["prompt to rloop-2kuxv-p3-implement: no rollout shows it (lookup by <runDir>/phase-3/implement-a1.sentinel found nothing); herdr reports it working, so it counts as received"]`. Covers #21 criterion 3.
- `TestHerdrStateIsNotReadWhileTheRolloutConfirmsTheFirstPrompt`: the script is always `working`. `rollouts.files` holds the marker with `misses: 1`, so the first lookup misses and the next finds it. Asserts: State calls `[]string(nil)`, prompts `[P]`, transcripts `[codexRollout]`. Covers the watchdog's warning: the startup `working` state is not read as delivery while the rollout wait runs.
- `TestALaterPromptToACodexAgentCountedReceivedByHerdrStateIsTypedWithoutARolloutWait`: the script is always `working`, Face set, no rollout. Spawns, then calls `r.sm.prompt(stepAgent, "r-loop: go on", nil)`. Asserts: that call returns nil; prompts `[P, "SessionHost.Prompt rloop-2kuxv-p3-implement \"r-loop: go on\" false 0s"]`; State calls `["SessionHost.State rloop-2kuxv-p3-implement"]`; `len(warnings(face))` is 1. Covers #21 criterion 3 (one warning) and the `received` mark.
- `TestACodexReviewerWhoseRolloutNeverShowsWhileHerdrReportsItWorkingRunsItsReview`: `newReviewRig(t, Reviewer{Provider: "codex"})` with `rolloutResolve(r)`, `r.sm.Rollouts = &fakeRollouts{files: map[string]rolloutFile{}}` and `r.host.script` always `working`. `r.behave` writes an ok review with 0 findings. Asserts: `assertState(t, out, StepOK, "")` and `r.callsFrom("SessionHost.Prompt " + codexReviewer)` equals `["SessionHost.Prompt rloop-2kuxv-p3-implemen-8lgad-r1 \"review r1\" false 0s"]`. Covers #21 criterion 1 (the reviewer launch).

**#10, in `internal/core/blockers_test.go`, built on `evidenceRig`:**
- `TestAStalledStepsBlockerRoutesItsAgentAndTheEndOfItsPaneScreenToTheWatchdog`:
  - Given: `evidenceRig(t, "rloop-p2-plan", "stall")` with `host.screen = "• Ran go test ./...   \n  └ ok  r-loop/internal/core\n\n› Improve documentation\n\n\n"`.
  - Then: `role((*seen)[0].q.Text)` equals `"blocker b1 from phase-2/plan (step): stalled: no response to nudge\nactions: retry, switch, block, stop, continue; resolve it with resolve_blocker\n\nagent: rloop-p2-plan\n\n• Ran go test ./...\n  └ ok  r-loop/internal/core\n\n› Improve documentation"`.
  - Covers #10 criteria 1 and 4 and the routed order.
- `TestAFailedStepsBlockerRoutesItsLastFiveToolCallsAndTheEndOfItsPaneScreenToTheWatchdog`:
  - Given: `evidenceRig(t, "rloop-p2-implement", "fail")`, with `r.loop.Sessions.Hooks = host.feed = &fakeHookFeed{}` and `host.screen = "› Improve documentation\n"`. `host.hooks` holds `PreToolUse` events at `t0`, in order:
    - `Read /etc/hosts`
    - `Edit <root>/.r-loop/wt/phase-2/two  spaces.go` (two spaces in the name)
    - `Bash "go test ./internal/core/\necho done"`
    - `Grep "<root>/.r-loop/wt/phase-2/a.go"` (an absolute-looking search pattern)
    - `Read <root>/.r-loop/wt/phase-2/internal/core/blockers.go`
    - `mcp__r-loop__ask_watchdog` with an empty target
    - then `Stop`

    `<root>` is `r.repo.RootDir`.
  - Then: `role((*seen)[0].q.Text)` equals `"blocker b1 from phase-2/implement (step): tests red\nactions: retry, switch, block, stop, continue; resolve it with resolve_blocker\n\nagent: rloop-p2-implement\nlast tools: Edit two  spaces.go; Bash go test ./internal/core/; Grep " + r.repo.RootDir + "/.r-loop/wt/phase-2/a.go; Read internal/core/blockers.go; mcp__r-loop__ask_watchdog\n\n› Improve documentation"` (the root is the test's own fixture path, spliced in as data).
  - Covers #10 criteria 1 and 2 for a failed step: the screen of a failed (not stalled) step reaches the watchdog, along with the bound of five, oldest first, `Stop` keeping the ring, relative paths kept verbatim, a search pattern left unrewritten, the first Bash line, and a bare tool name.
- `TestAFailedStepsBlockerGivesTheTranscriptPathItsHooksReported`: the same rig, with one hook `PreToolUse Read <root>/.r-loop/wt/phase-2/a.go` with `Session: "s-1"` and `Transcript: "/home/.claude/projects/-repo/s-1.jsonl"`. Asserts `role((*seen)[0].q.Text)` equals `"blocker b1 from phase-2/implement (step): tests red\nactions: retry, switch, block, stop, continue; resolve it with resolve_blocker\n\nagent: rloop-p2-implement\nlast tools: Read a.go\ntranscript: /home/.claude/projects/-repo/s-1.jsonl"`. Covers #10 criterion 3 (claude) in the routed text.
- `TestAFailedStepsBlockerRoutesATranscriptPathFirstHeardAtBlockerTime`: `evidenceRig(t, "rloop-p2-implement", "fail")` with `r.loop.Sessions.Hooks = host.feed = &fakeHookFeed{}`, no `hooks`, and `screenHooks` holding one `PreToolUse Read <root>/.r-loop/wt/phase-2/a.go` with `Session: "s-1"` and `Transcript: "/home/.claude/projects/-repo/s-1.jsonl"`. Asserts `role((*seen)[0].q.Text)` equals `"blocker b1 from phase-2/implement (step): tests red\nactions: retry, switch, block, stop, continue; resolve it with resolve_blocker\n\nagent: rloop-p2-implement\nlast tools: Read a.go\ntranscript: /home/.claude/projects/-repo/s-1.jsonl"`. Covers #10 criterion 3 for a hook drained only after `Finish` untracked the session.
- `TestAFailedCodexStepsBlockerGivesItsRolloutPath`: `evidenceRig(t, "rloop-p2-implement", "fail")` with `r.loop.Sessions.Resolve` returning `ProviderArgs{Kind: provider, Rollout: true}` and `r.loop.Sessions.Rollouts = &fakeRollouts{files: map[string]rolloutFile{filepath.Join(r.store.dir, "phase-2", "plan-a1.sentinel"): {codexRollout, "019a"}, filepath.Join(r.store.dir, "phase-2", "implement-a1.sentinel"): {codexRollout, "019a"}}}` (each agent's sentinel is its marker; `r.loop.runDir` is `r.store.dir`, `loop_events_test.go:432`). Asserts `role((*seen)[0].q.Text)` equals `"blocker b1 from phase-2/implement (step): tests red\nactions: retry, switch, block, stop, continue; resolve it with resolve_blocker\n\nagent: rloop-p2-implement\ntranscript: " + codexRollout`. Covers #10 criterion 3 (codex) in the routed text and the `Session.transcript` field.
- `TestAFailedStepsBlockerWithNoHookFeedNamesOnlyItsAgent`: `evidenceRig(t, "rloop-p2-implement", "fail")` with no hooks and no rollout. Asserts `role(Evidence)` equals `"agent: rloop-p2-implement"`. Covers #10 when the transcript is unknown and no hook feed is wired.
- `TestAFailedStepsBlockerWhoseScreenCannotBeReadCarriesNoExcerpt`: `host.screenErr = errors.New("pane gone")`, behaviour `fail`. Asserts: `Excerpt == ""` and `role(Evidence)` equals `"agent: rloop-p2-implement"`. Covers #10 for a dead pane.
- `TestAStepBlockerOfASpawnThatNamedNoAgentCarriesNoEvidenceOrScreen`: `host.paneErr = errors.New("herdr down")` on `rloop-p2-implement`, so `freeAgent` fails before naming the agent. Asserts: `Evidence == ""`, `Excerpt == ""`, and `r.calls("SessionHost.Screen ")` is empty. Covers #10's empty-agent guard; the source is `session.go:193-196`.
- `TestAStepBlockerOfARunnerThatPanickedBeforeAnySessionCarriesNoEvidenceOrScreen`: `r.loop.Runners["diff"] = stepRunnerFunc(func(context.Context, StepRef, Observer) Outcome { panic("boom") })`. Asserts: `Evidence == ""`, `Excerpt == ""`, and no `SessionHost.Screen` call. Covers #10's nil-session guard; the source is `runnerPanicked`, `loop.go:1226`.

## Left out

- **A per-target length cap in the evidence.** The hook tap already caps `Target` at 512 bytes (`internal/hooktap/hooktap.go:15,67`), so five calls stay bounded.
- **Evidence on reviewer blockers.** #10 covers step blockers, and reviewer blockers already carry the screen (`review.go:620-622`).
- **Checking codex's composer on screen instead of herdr state.** The backlog notes call it new per-provider screen parsing at a higher cost; herdr state meets every #21 criterion.
- **Polling `Host.State` more than once per check.** One read after a full 10 s rollout wait is past herdr's startup transient. The watch's stall detection catches a prompt that was lost anyway.
- **Recording the delivery warning in the run store.** `hookStart`'s equivalent warning goes only to the face (`hooks.go:443-456`).
- **Describing the first-prompt rollout confirmation in tech-design.md.** It is not described there today, and no contract in the doc changes.
- **Reading the transcript path from journalled `agent-transcript` events.** That needs a full `Store.Load` per blocker, and the `Session` already learns the path.
- **A question to the watchdog.** Every choice above is settled by the repository, the backlog item or the triage.

## Assumptions

- **The watchdog's first warning applies and is resolved.** `Host.State` is read only after a failed `awaitRollout` (`hooks.go:276-288`): before the Enter and before the re-type, never straight after `Host.Prompt`. `TestHerdrStateIsNotReadWhileTheRolloutConfirmsTheFirstPrompt` pins this.
- **The watchdog's second warning applies and is resolved.** `hookAgent` gets the bounded `recent` ring, and the screen helper moves to `SessionManager` for both blocker kinds.
- **"Last few" tool calls means the last five, listed oldest first.** The newest call, the one the agent stalled in, is last.
- **"Otherwise counts as received" covers every non-idle, non-done answer.** That includes `unknown`, `gone` and a state error. A `gone` agent or a timed-out state call is then failed by the watch (`session.go:494-501`).
- **"One warning" is one per session.** After a state-confirmed first prompt, the session is no longer rollout-confirmed, so later nudges and answers neither wait nor warn.
- **The `last tools` line is printed only when a hook feed is wired (`m.Hooks != nil`).** When the feed saw no tool call, the line reads `none seen by hooks`.
- **The failed attempt's pane is still open when its blocker is raised.** `Finish` does not close it, and `closeWorkspaces` runs only for the next attempt (`loop.go:1087-1089`).

## Gate

`go test ./internal/core/ -count=1 -run '^(TestAFreshCodexPromptNoRolloutEverShowsIsRetypedThenFailsTheSpawn|TestAFreshCodexPromptNoRolloutShowsWhileHerdrReportsItBusyIsTypedOnceAndTheSpawnSucceeds|TestAFreshCodexPromptWhoseHerdrStateCannotBeReadCountsAsReceived|TestAFreshCodexPromptHerdrReportsWorkingAfterTheEnterIsNotRetyped|TestAFreshCodexPromptHerdrReportsDoneIsRetypedUntilTheTriesRunOut|TestAFreshCodexPromptCountedReceivedByHerdrStateWarnsOnceNamingTheAgentAndTheRolloutLookup|TestHerdrStateIsNotReadWhileTheRolloutConfirmsTheFirstPrompt|TestALaterPromptToACodexAgentCountedReceivedByHerdrStateIsTypedWithoutARolloutWait|TestACodexReviewerWhoseRolloutNeverShowsWhileHerdrReportsItWorkingRunsItsReview|TestAStalledStepsBlockerRoutesItsAgentAndTheEndOfItsPaneScreenToTheWatchdog|TestAFailedStepsBlockerRoutesItsLastFiveToolCallsAndTheEndOfItsPaneScreenToTheWatchdog|TestAFailedStepsBlockerGivesTheTranscriptPathItsHooksReported|TestAFailedStepsBlockerRoutesATranscriptPathFirstHeardAtBlockerTime|TestAFailedCodexStepsBlockerGivesItsRolloutPath|TestAFailedStepsBlockerWithNoHookFeedNamesOnlyItsAgent|TestAFailedStepsBlockerWhoseScreenCannotBeReadCarriesNoExcerpt|TestAStepBlockerOfASpawnThatNamedNoAgentCarriesNoEvidenceOrScreen|TestAStepBlockerOfARunnerThatPanickedBeforeAnySessionCarriesNoEvidenceOrScreen)$'`
