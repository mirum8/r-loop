status: planned

## Summary

Two things change for a claude session that reports hooks. First, a prompt the driver types into its pane counts as delivered only when that agent's `UserPromptSubmit` comes back carrying the same text (#1). Second, an answer, a withdrawal or a decline follow-up is typed only once the hooks say the asking turn has ended (#2). Sessions that never sent a hook event (codex, the watchdog, intake, the fixer, and any claude session before its first event) keep today's herdr path unchanged.

- **Where confirmation lives:** in core, as `SessionManager.prompt(agent, text, gate)` beside the hook state in `internal/core/hooks.go`. It does not live in the herdr adapter. Core's `SessionManager` owns the only hook read cursor (`internal/core/hooks.go:62-77`), so the adapter has no view of the hooks.
- **How the host reports a stall without pressing Enter:** a new `SessionHost.Submit(agent, text string) error`. It returns an error that wraps a new `core.ErrPromptStalled` on herdr's `agent_prompt_stalled`. The other option was a `retry bool` parameter on `Prompt`, which would touch every `Prompt` caller and every test host. The alternative of an optional interface checked at run time, the pattern at `internal/core/watchdog.go:259-272`, was rejected because a host without the method would silently drop the obligation.
- **What "matching" means:** the tap keeps the first 512 bytes of the `prompt` field, the same cap as every other field, and marks the line `prompt_cut` when it cut the text. Both texts are normalised by collapsing every run of whitespace to one space and trimming the ends.
  - An uncut prompt must equal the sent text after normalising.
  - A cut prompt must be a non-empty prefix of the normalised sent text.

  Guessing a cut from the length was rejected: a complete prompt of 508 to 512 bytes looks the same as a cut one. A bare prefix rule was rejected because a shorter prompt typed into the pane would confirm a longer one. Removing all whitespace was rejected because it makes `go test` equal `gotest`.
- **What "after the send" means, and which event confirms:** before `Submit`, `prompt` registers the text and the send time `m.now()` on the agent's hook state. `hookLog.apply` marks the send confirmed when it applies a `UserPromptSubmit` from that agent that matches the registered text and is stamped at or after the send. The mark is sticky until `prompt` returns, so a different prompt typed into the pane later in the same read cannot erase it.
  - Comparing against only the last `UserPromptSubmit` was rejected because of that erasure.
  - A per-agent event counter was rejected because a line written before the send but read after it would pass as new.
- **Two prompts to one agent at once:** `prompt` holds a per-agent lock from the send until it is confirmed or fails. A hand-off passes its gate to `prompt`, which re-checks it after taking the lock and draining the hooks. If the agent is no longer ready (another prompt got in first, or a tool ran after the `Stop`), `prompt` returns `errTurnNotOver` without typing, and the hand-off waits again. Without it, a decline follow-up that falls back after the stall grace can overlap the stall nudge (`internal/core/dialogs.go:146-149` releases the question before `declined` runs; `internal/core/session.go:429-469` then nudges). Keeping a list of recent `UserPromptSubmit`s per agent was rejected: it would confirm each prompt but would still let two `agent prompt` calls type into one pane at the same time.
- **What happens when no confirmation arrives:** one Enter, then a second wait. If that also fails, the error is `prompt to <agent> not submitted: no matching UserPromptSubmit`. On a stall, the agent's hooks are checked at once and Enter is pressed only if no matching `UserPromptSubmit` was seen. This mirrors herdr's own single retry (`internal/herdr/client.go:387-395`), so stalled and unconfirmed prompts take one path.
- **What the hand-off gate is:** one small type, `handOff`, used by both `hand()` and `declined()`. On every check it drains the hooks. Then:
  - An agent with hook events that is idle is ready (herdr is asked only whether it is gone).
  - An agent with hook events that is busy is not ready, and herdr is not asked, until the session's stall grace has passed on `m.now()`. After that, herdr's not-`working` rule takes over.
  - An agent with no hook events uses today's herdr rule.

  The gate reads the shared hook state, and every `drainHooks` call that applies events wakes all waiting gates through a broadcast channel. That channel is closed and replaced in `drainHooks` (`internal/core/hooks.go:62-77`). So a `Stop` that any reader drains (the session wait, `hookNotice`, `treeBlame`, a prompt confirmation, another gate) wakes the hand-off at that same read. Relying on the 200 ms poll alone was rejected because it would leave the hand-off up to one poll behind another reader's read.
- **How often the gate checks:** an agent with hook events waits for the broadcast or for `hookPoll` (200 ms, so its own reads still happen when no other reader runs). This holds after the grace fallback too, so a late `Stop` never waits for the session poll. Agents without hook events sleep the session poll interval and do not wake on the broadcast, so other agents' hook traffic never multiplies their herdr calls.
- **A panic inside `hand`:** its recovery still records the withdrawal, and types it through `SessionManager.prompt` with the hand-off's gate. For an agent with hook events, the gate is re-checked under the typing lock after a fresh drain, so a busy agent (including one that ran a tool after its `Stop`) gets nothing typed. A second panic is caught by the surrounding `quietly` and also types nothing. Skipping the typing entirely was rejected: an agent whose turn has ended would then sit waiting until its stall nudge.
- **The phase check's warning:** first prompts stay on herdr's check. "Has hook events" is still decided from the events themselves, not from the provider's settings; the reasons are under `## Assumptions`.

## Why this approach

The triage proposed the same split for #1: a capped `prompt` in the tap, `HookEvent` carrying it, `hookAgent` recording the last `UserPromptSubmit`, and a host call that reports `agent_prompt_stalled` as a typed error. This plan follows it, with three choices made concrete:

- **A new port method, not a flag on `Prompt`.** `Prompt` has 13 production call sites (`internal/core/watchdog.go:102`, `internal/core/intake.go:58`, `internal/core/fixer.go:384` and others), and most of them must stay unchanged. A separate method touches only the five root test hosts the compiler names.
- **One Enter when herdr says `working` but no `UserPromptSubmit` comes.** The item says herdr's `working` alone does not confirm a prompt. The cheapest recovery for text left sitting in the input box is the same single Enter herdr already uses (`internal/herdr/client.go:390`).
- **`m.now()` for the send time.** The tap stamps lines with the real clock (`internal/hooktap/hooktap.go:56`). In production `m.now()` is that clock, and tests pin both with fixed data.

For #2 the triage proposed one gate on `SessionManager`, drained on each check, with a short poll and a herdr fallback after the stall grace. This plan builds that as `handOff`, and adds a broadcast from `drainHooks` so a `Stop` read by any reader wakes it at that read. Today `hand()` (`internal/core/loop.go:1392-1419`) and `declined()` (`internal/core/dialogs.go:170-193`) each run their own `Host.State` loop at the session poll. Both become one gate check plus their own after-checks:

- In `hand()`, a gone agent withdraws the question and `agent_blocked` is retried.
- In `declined()`, a blocked pane is not typed into.

Both now type through `SessionManager.prompt`. That way an answer to a hook agent is also confirmed by its `UserPromptSubmit` (#1).

The phase has no `Files:` line; every file this plan touches, and why:

- `internal/core/ports.go`: the port gains `Submit` and `ErrPromptStalled`; `HookEvent` gains `Prompt` and `PromptCut`.
- `internal/herdr/client.go`: the adapter implements `Submit`.
- `internal/core/session.go`, `internal/core/review.go`: their prompts to step and reviewer sessions move to `SessionManager.prompt`.
- `internal/core/land_test.go`, `internal/app/intake_test.go`, `internal/app/resume_test.go`, `internal/app/watchdog_test.go`: their root test hosts must implement the widened port.
- `internal/core/loop_events_test.go`, `internal/core/session_test.go`: `eventsHost` gets a `Submit` that applies its `Prompt` effects (typed, refusals), and `stepClock` gets a `set` method for the grace tests.
- `docs/task-loop-driver/spec.html`: ADR-93 says hooks are used "for four things only" (`docs/task-loop-driver/spec.html:4156`). This phase adds two uses, so the ADR is amended, as the triage and `issues/issues-driver-hooks-2026-10-13-notes.md` require.
- `docs/task-loop-driver/tech-design.md`: it lists the `SessionHost` contract (`docs/task-loop-driver/tech-design.md:103-131`) and the answer delivery rule (`docs/task-loop-driver/tech-design.md:601-607`).

## Changes

1. **`internal/core/ports.go`** (modify)
   - After `ErrMergeConflict` (`internal/core/ports.go:9`), add `var ErrPromptStalled = errors.New("prompt stalled")`.
   - `HookEvent` (`internal/core/ports.go:16-20`) gains the fields `Prompt string` and `PromptCut bool`, on the lines after `Type`.
   - `SessionHost` (`internal/core/ports.go:55-72`) gains `Submit(agent, text string) error`, on the line after `Prompt`.
   - Serves: #1 HookEvent carries the prompt; #1 the stall is reported without an Enter.

2. **`internal/hooktap/hooktap.go`** (modify)
   - The `hook` input struct (`internal/hooktap/hooktap.go:33-45`) gains ``Prompt string `json:"prompt"` ``.
   - `line` (`internal/hooktap/hooktap.go:17-25`) gains ``Prompt string `json:"prompt,omitempty"` `` and ``PromptCut bool `json:"prompt_cut,omitempty"` ``.
   - `Tap` sets `Prompt: capped(hook.Prompt), PromptCut: len(hook.Prompt) > fieldCap` in the `line` literal at `internal/hooktap/hooktap.go:56`.
   - `Feed.Read` copies `Prompt: l.Prompt, PromptCut: l.PromptCut` into the `core.HookEvent` at `internal/hooktap/hooktap.go:126`.
   - Reuses `capped` and `fieldCap` (`internal/hooktap/hooktap.go:15`, `:68-77`).
   - Serves: #1 the tap keeps a capped copy per agent, and says when it cut it.

3. **`internal/herdr/client.go`** (modify)
   - Move the `started` slice from `PromptContext` (`internal/herdr/client.go:376`) into a package function `startedArgs() []string`. It returns `--until working --until blocked --timeout <submitTimeout ms>`. `PromptContext` uses it in its three places (`:376`, `:393` and the `started...` spreads).
   - Add `func (c Client) Submit(agent, text string) error`:
     - It runs `c.callWithinContext(context.Background(), callTimeout+submitTimeout, &out, append([]string{"agent", "prompt", agent, text, "--wait"}, startedArgs()...)...)`.
     - On a herdr `Error` with code `agent_prompt_stalled` it returns `fmt.Errorf("%w: %w", core.ErrPromptStalled, err)` and presses nothing.
     - Every other result is returned as is.
   - `Prompt` and `PromptContext` behave exactly as today.
   - Serves: #1 no Enter from the adapter for hook sessions; #1 sessions without hooks keep `Prompt` unchanged.

4. **`internal/core/hooks.go`** (modify)
   - `hookAgent` (`internal/core/hooks.go:11-16`) gains `expect string`, `expectSince time.Time` and `matched bool`: the prompt now being typed into the agent, when it was sent, and whether its `UserPromptSubmit` has been applied.
   - `hookLog` (`internal/core/hooks.go:18-22`) gains `changed chan struct{}`.
   - In `hookLog.apply`, the `"UserPromptSubmit"` case (`internal/core/hooks.go:42-43`) becomes `a.busy, a.notice = true, ""`, followed by `if a.expect != "" && !ev.At.Before(a.expectSince) && sameText(a.expect, ev.Prompt, ev.PromptCut) { a.matched = true }`. `matched` is only ever set here, never cleared by a later event.
   - In `drainHooks` (`internal/core/hooks.go:62-77`), after applying the events: if `len(events) > 0 && h.changed != nil`, call `close(h.changed)` and set `h.changed = nil`.
   - Add `func (m *SessionManager) hookChanged() <-chan struct{}`. Under `m.hooks.mu`, it creates `h.changed` when nil and returns it. A waiter that fetched the channel before a drain is woken by that drain.
   - Add the variable `var hookPoll = 200 * time.Millisecond` and the variable `var submitWait = 10 * time.Second`. Both are vars so tests can set them, the pattern of `reportEvery` at `internal/core/loop.go:126`.
   - Add `var errTurnNotOver = errors.New("turn not over")`.
   - Add `func sameText(sent, seen string, cut bool) bool`:
     1. `norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }`; `a, b := norm(sent), norm(seen)`.
     2. If `b == ""`, return false.
     3. If `!cut`, return `a == b`.
     4. Otherwise return `strings.HasPrefix(a, b)`.
   - Add `func (m *SessionManager) expectPrompt(agent, text string, since time.Time)`. Under `m.hooks.mu` it sets the agent's `expect`, `expectSince` and `matched = false` (the agent exists: `prompt` calls it only for an agent with hook events). Calling it with `""` clears the expectation.
   - Add `func (m *SessionManager) submitted(agent string) bool`. It calls `_ = m.drainHooks()`, then returns the agent's `matched` through `m.hookAgent`.
   - Add `func (m *SessionManager) awaitSubmit(agent string, wait time.Duration) bool`. It takes `deadline := time.Now().Add(wait)` at entry, then loops: return true when `submitted(agent)`; return false when `time.Now()` is not before `deadline`; otherwise sleep `hookPoll`. With `wait` 0 it checks exactly once.
   - `SessionManager` (`internal/core/session.go:31-49`) gains `typing map[string]*sync.Mutex`. Add `func (m *SessionManager) typingLock(agent string) *sync.Mutex`: under `m.mu` it creates the map and the agent's mutex on first use, and returns that mutex.
   - Add `func (m *SessionManager) prompt(agent, text string, gate *handOff) error`. `gate` is nil for every caller except `hand` and `declined`.
     1. `lock := m.typingLock(agent); lock.Lock(); defer lock.Unlock()`.
     2. `_ = m.drainHooks()`. If `seen, _ := m.hookTurn(agent); !seen`, return `m.Host.Prompt(agent, text, false, 0)`. This is today's path, also taken when `m.Hooks` is nil.
     3. If `gate != nil`: `if _, ready, err := gate.check(); err != nil || !ready { return errTurnNotOver }`.
     4. `m.expectPrompt(agent, text, m.now())`, then `defer m.expectPrompt(agent, "", time.Time{})`.
     5. `err := m.Host.Submit(agent, text)`; `stalled := errors.Is(err, ErrPromptStalled)`. If `err != nil && !stalled`, return `err`.
     6. Set `wait := submitWait`; if `stalled`, set `wait = 0`. If `m.awaitSubmit(agent, wait)`, return nil. This is the stalled-but-seen case: no Enter.
     7. If `err := m.Host.SendKeys(agent, "enter"); err != nil`, return `err`. If `m.awaitSubmit(agent, submitWait)`, return nil.
     8. Return `fmt.Errorf("prompt to %s not submitted: no matching UserPromptSubmit", agent)`.
   - Add the gate:
     ```go
     type handOff struct {
         m         *SessionManager
         agent     string
         fast      bool
         busySince time.Time
         changed   <-chan struct{}
     }
     func (m *SessionManager) handOff(agent string) *handOff { return &handOff{m: m, agent: agent} }
     func (g *handOff) check() (AgentState, bool, error)
     func (g *handOff) wait()
     ```
     `check` does the following:
     1. `g.changed = g.m.hookChanged()`. This is taken before the drain, so any later drain wakes the next `wait`.
     2. `_ = g.m.drainHooks()`, then `seen, busy := g.m.hookTurn(g.agent)`, then `grace := g.m.StallGrace`. If `grace <= 0`, set `grace = defaultStallGrace`, as in `tick` (`internal/core/session.go:389-392`).
     3. Set `g.fast = seen`. If `seen && busy`: `now := g.m.now()`; when `g.busySince` is zero, set it to `now`. If `now.Sub(g.busySince) < grace`, return `"", false, nil` without calling herdr.
     4. Otherwise (not busy, or busy past the grace): if `!busy`, reset `g.busySince` to zero.
     5. Call `state, err := g.m.Host.State(g.agent)`. On error, return `state, false, err`.
     6. If `seen && !busy`, return `state, state != AgentGone, nil`.
     7. Otherwise return `state, state != AgentWorking && state != AgentGone, nil`.

     `wait` does the following:
     - If `g.fast`, it runs `select { case <-g.changed: case <-time.After(hookPoll): }`.
     - Otherwise it sleeps `g.m.Poll`, or `defaultPoll` when that is not above 0.
   - Reuses `drainHooks`, `hookAgent` and `hookTurn` (`internal/core/hooks.go:62-95`).
   - Serves: every #1 item except the tap; every #2 item.

5. **`internal/core/session.go`** (modify)
   - The start prompt (`internal/core/session.go:303`) becomes `return m.prompt(s.Agent, text, nil)`.
   - The nudge (`internal/core/session.go:469`) becomes `m.prompt(s.Agent, nudge(grace, s.Ref.AskURL != ""), nil)`, with the same error wrapping.
   - Serves: #1 for later prompts to hook sessions (the nudge); the start prompt stays on herdr because the agent has no events yet (see `## Assumptions`).

6. **`internal/core/review.go`** (modify)
   - The fix-round prompt to the worker (`internal/core/review.go:193`) becomes `err = sm.prompt(worker.Agent, text, nil)`.
   - The reviewer prompt (`internal/core/review.go:559`) becomes `err = sm.prompt(s.Agent, text, nil)`.
   - Serves: #1 (review findings typed into a hook worker are confirmed).

7. **`internal/core/loop.go`** (modify `hand`, `internal/core/loop.go:1371-1421`)
   - Declare `gate := l.Sessions.handOff(open.agent)` as the first statement, before the deferred recover, so the recover can use it. Remove `poll`, the `Host.State` call and `time.Sleep(poll)`. The loop runs `for l.asking(id)`:
     1. `state, ready, err := gate.check()`.
     2. If `err == nil && state == AgentGone`, call `l.undeliverable(open, "agent gone")` and return.
     3. If `err == nil && ready`, run the existing `open.s.live` block, now calling `l.Sessions.prompt(open.agent, text, gate)` instead of `l.Sessions.Host.Prompt(open.agent, text, false, 0)`. The existing failure branch's condition `err != nil && !blocked(err)` becomes `err != nil && !blocked(err) && !errors.Is(err, errTurnNotOver)`, so a turn that is not over is waited out, not withdrawn.
     4. `gate.wait()`.
   - In the deferred recover (`internal/core/loop.go:1379-1383`), the `open.s.live` body becomes `l.Sessions.prompt(open.agent, answerMessage(...unchanged...), gate)`. An agent without hook events keeps today's typed withdrawal through herdr. For one with hook events, the gate is re-checked under the typing lock after a fresh drain, so nothing is typed while it is busy. A panic from that re-check (for example `Host.State` again) is caught by the surrounding `quietly`, and nothing is typed.
   - Serves: #2 answer and withdrawal (both reach `hand`, `internal/core/loop.go:1314` and `:1333`, and its panic recovery); #2 within one hook read; #2 fallback after the stall grace.

8. **`internal/core/dialogs.go`** (modify `declined`, `internal/core/dialogs.go:158-194`)
   - Declare `gate := l.Sessions.handOff(open.agent)` before the loop and remove `poll`, the `Host.State` call and `time.Sleep(poll)`:
     1. `state, ready, err := gate.check()`.
     2. If `err == nil && state == AgentGone`, return.
     3. Inside `open.s.live`, return without typing when `err != nil || !ready || state == AgentBlocked`. Otherwise call `typeErr = l.Sessions.prompt(open.agent, declinedMessage(q.ID), gate)`.
     4. The warning branch's condition `typeErr != nil && !blocked(typeErr)` (`internal/core/dialogs.go:188`) becomes `typeErr != nil && !blocked(typeErr) && !errors.Is(typeErr, errTurnNotOver)`. The note is unchanged. The loop ends with `gate.wait()`.
   - Serves: #2 the decline follow-up uses the same gate.

9. **`internal/core/fakes_test.go`** (modify)
   - `fakeSessionHost` gains `Submit(agent, text string) error`. It records `SessionHost.Submit %s %q` and returns `f.Err`, the same shape as `Prompt` at `internal/core/fakes_test.go:103-106`.
   - `fakeHookFeed` (`internal/core/fakes_test.go:600-613`) gains `queued []HookEvent` and `reads int`, plus two methods:
     - `push(evs ...HookEvent)`, which appends under `mu`.
     - `readCount() int`, read under `mu`.

     `Read` increments `reads`, then returns `queued` (and clears it) followed by `next()` when `next` is set.
   - Existing tests must pass unchanged. None of them sends a prompt to an agent that has hook events through `fakeSessionHost.Submit`: the hook-enabled review tests (`internal/core/hooks_test.go:67-100`) end with no fix round. Run `go test ./internal/...` to confirm.
   - Serves: test support for every core test below.

9a. **`internal/core/loop_events_test.go`, `internal/core/session_test.go`** (modify)
   - `eventsHost` gains `func (h *eventsHost) Submit(agent, text string) error`. It records `SessionHost.Submit %s %q`, then returns `h.Prompt(agent, text, false, 0)`. Herdr typing through `Submit` thus has the same effects as through `Prompt` (`internal/core/loop_events_test.go:302-326`): it sets `typed`, records `SessionHost.Typed`, and applies the `refuse` count.
   - `stepClock` (`internal/core/session_test.go:44-55`) gains `func (c *stepClock) set(t time.Time)`, which assigns `c.t` under `c.mu`. With `step` 0 it is a clock the test moves.
   - Serves: tests that assert a hand-off's outcome and the grace boundary.

10. **`internal/core/land_test.go`, `internal/app/intake_test.go`, `internal/app/resume_test.go`, `internal/app/watchdog_test.go`** (modify)
    - `reportHost`, `intakeHost`, `simHost` and `dogHost` each gain `func (h *X) Submit(agent, text string) error { return h.Prompt(agent, text, false, 0) }`.
    - After this, `go vet ./...` must report no other `SessionHost` implementation missing `Submit`. Add the same one-line method to any type it names.
    - Serves: compile only; no behaviour change.

11. **`docs/task-loop-driver/spec.html`** (modify ADR-93, `docs/task-loop-driver/spec.html:4145-4171`)
    - Status line: append ` · amended 2026-10-13 — prompt confirmation and answer hand-off; amends <a href="#adr-76">ADR-76</a>`.
    - Decision: "for four things only" becomes "for six things only". After the turn-end clause, append two more items:
      - whether a typed prompt was submitted: once a session has reported a hook, a prompt counts as delivered only on its `UserPromptSubmit` carrying the prompt's text (the tap keeps the first 512 bytes), stamped after the send. On herdr's `agent_prompt_stalled`, Enter is pressed only when none was seen. A session's first prompt, typed before any hook can fire, keeps herdr's check.
      - when an answer, a withdrawal or a decline follow-up may be typed: for such a session, only after the asking turn's `Stop` or `idle_prompt`, acted on at the hook read that brings it (any reader's read wakes the waiting hand-off, and it reads the hooks itself every 200 ms), with herdr's not-`working` rule back once the session has stayed busy past the stall grace. Prompts to one session are typed one at a time.
    - Consequences: append "A declined dialog's follow-up waits for `idle_prompt`, about a minute, since claude sends no `Stop` for a turn interrupted by esc."
    - Serves: the ADR-93 amendment the triage requires.

12. **`docs/task-loop-driver/tech-design.md`** (modify)
    - In the `SessionHost` list after `Prompt(...)` (`docs/task-loop-driver/tech-design.md:116-117`), add: `` `Submit(agent, text string) error` (herdr `agent prompt --wait --until working --until blocked`, no Enter retry: `agent_prompt_stalled` returns an error wrapping `core.ErrPromptStalled`; core uses it for sessions that have reported a hook, spec ADR-93) ``.
    - In **Delivery** (`docs/task-loop-driver/tech-design.md:601-607`), append: "For an agent that has reported a hook, it types only after that agent's `Stop` or `idle_prompt`, waking at any hook read and reading the hooks itself every 200 ms, and falls back to the herdr rule once the agent has stayed busy past the stall grace; the prompt counts as typed only on a matching `UserPromptSubmit` (ADR-93)."
    - Serves: keeps the contracts doc true.

## Tests

All core tests live in `internal/core/hooks_test.go` (package `core`), next to the existing hook tests. They use a local test host:

```go
type claudeHost struct {
    SessionHost
    feed     *fakeHookFeed
    onSubmit map[string][]HookEvent
    onEnter  []HookEvent
    stall    bool
    panics   string
    statePanics bool
}
```

- `State` panics with `"boom"` when `statePanics` is set; otherwise it forwards.
- `Submit` panics with `"boom"` when `text == panics`. Otherwise it forwards to the embedded host. If that returns an error, it returns the error. Otherwise it pushes `onSubmit[text]` onto `feed`, and returns `ErrPromptStalled` when `stall` is set, else nil.
- `SendKeys` forwards and pushes `onEnter`.

Together these model claude writing its `UserPromptSubmit` line when the text is submitted, by the original Enter or by the extra one.

Four helpers keep each test's blocks short:
- `promptSubmit(agent, prompt string, at time.Time) HookEvent`, an uncut `UserPromptSubmit`.
- `cutPromptSubmit(agent, prompt string, at time.Time) HookEvent`, the same with `PromptCut: true`.
- `shortSubmitWait(t)`, which sets `submitWait = 10 * time.Millisecond` and restores it with `t.Cleanup`.
- `slowHookPoll(t)`, which sets `hookPoll = time.Hour` and restores it with `t.Cleanup`, so a gate can wake only through the broadcast.

Prompt tests build `&SessionManager{Host: host, Hooks: feed, Now: func() time.Time { return t0 }}` with `t0 := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)` and `host := &claudeHost{SessionHost: inner, …}` over `inner := &fakeSessionHost{}`. The agent is `rloop-p3-implement`, and the feed is pre-queued with `HookEvent{At: t0.Add(-time.Minute), Agent: "rloop-p3-implement", Event: "Stop"}` so the agent has hook events and is idle. Calls are read from `inner.Calls()`. Every test has `// given`, `// when` and `// then` blocks, and names its compared values `actual…` and `expected…`.

**Tap and adapter**

- `TestTapKeepsACappedUserPromptSubmitPromptAndMarksACut` (`internal/hooktap/hooktap_test.go`). Table:
  - row "short": `{"hook_event_name":"UserPromptSubmit","prompt":"r-loop: answer to q1 (by watchdog): sqlite"}` → `actualPrompt == "r-loop: answer to q1 (by watchdog): sqlite"`, `actualCut == false`.
  - row "exactly the cap": a prompt of `strings.Repeat("x", 512)` → `strings.Repeat("x", 512)`, `false`.
  - row "long": a prompt of `strings.Repeat("x", 600)` → `strings.Repeat("x", 512)`, `true`.

  Each row taps into a temp file and reads one event with `NewFeed`. Covers: #1 the tap keeps a capped copy, `HookEvent` carries it, and it says whether the text was cut.
- `TestSubmitTypesThePromptAndReturnsOnceTheAgentStarts` (`internal/herdr/client_test.go`). `fake` with an ok result; `c.Submit("a1", "go")` returns nil. Its argv is `agent prompt a1 go --wait --until working --until blocked --timeout 10000`. Covers: #1 the host submit path.
- `TestSubmitReportsAStalledPromptWithoutPressingEnter` (`internal/herdr/client_test.go`). `stalledPrompt(t, true)`; `c.Submit("a1", "go")`. Then `errors.Is(err, core.ErrPromptStalled)`, and the calls are exactly `[[agent prompt a1 go --wait --until working --until blocked --timeout 10000]]`, so no `send-keys` and no `wait`. Covers: #1 no Enter on a stall.
- `TestSubmitReturnsAnAgentBlockedErrorAsHerdrReportsIt` (`internal/herdr/client_test.go`). `fakeExit` with `agent_blocked`; `c.Submit("a1", "go")` returns a herdr `Error` with `Code == "agent_blocked"`, and `errors.Is(err, core.ErrPromptStalled)` is false. Covers: the error the hand-off's `agent_blocked` retry keys on reaches core intact.

**Prompt confirmation**

- `TestAPromptToAHookAgentIsDeliveredByAMatchingUserPromptSubmit`. Table, with `onSubmit` mapping the row's sent text to its reported `UserPromptSubmit` at `t0`:
  - row "the typed text": sent and reported `r-loop: answer to q1 (by watchdog): sqlite`.
  - row "its first 512 bytes": sent `strings.Repeat("ab ", 300)`, reported with `cutPromptSubmit` as `strings.Repeat("ab ", 300)[:512]`.
  - row "a rune the cap split": sent `strings.Repeat("é", 300)`, reported with `cutPromptSubmit` as `strings.Repeat("é", 255)`.
  - row "line breaks collapsed and trailing space trimmed": sent `"fix round 2\n\nfindings: a.json\n"`, reported `"fix round 2\nfindings: a.json"`.

  Then: err is nil, and `inner.Calls()` is exactly `[SessionHost.Submit rloop-p3-implement "<sent>"]` (no `SendKeys`, no `Prompt`). Covers: #1 delivered on a matching `UserPromptSubmit`.
- `TestAPromptToAHookAgentIsNotDeliveredWithoutAMatchingUserPromptSubmit`. Table, using `shortSubmitWait`; each row names its sent text and its `onSubmit` events:
  - row "herdr reports working, no hook arrives": sent `r-loop: answer to q1 (by watchdog): sqlite`, no events.
  - row "another text": the same sent text, reported `r-loop: answer to q2 (by watchdog): postgres` at `t0`.
  - row "a shorter prompt the text starts with": the same sent text, reported `r-loop: answer to q1` at `t0`.
  - row "the same words run together": sent `go test ./...`, reported `gotest./...` at `t0`.
  - row "a complete prompt near the cap the text starts with": sent `strings.Repeat("x", 600)`, reported uncut `strings.Repeat("x", 508)` at `t0`.
  - row "from before the send": the same sent text, reported identically at `t0.Add(-time.Second)`.
  - row "another agent's": the same sent text, reported identically by `rloop-p3-plan` at `t0`.
  - row "stalled and still unsubmitted after Enter": `stall: true`, no events.

  Then: `actualErr.Error() == "prompt to rloop-p3-implement not submitted: no matching UserPromptSubmit"`, and `inner.Calls()` is exactly `[SessionHost.Submit rloop-p3-implement "<sent>", SessionHost.SendKeys rloop-p3-implement enter]`. Covers: #1 herdr working alone is not confirmation; the match rules for text, time and agent; the error path.
- `TestAStalledPromptWhoseUserPromptSubmitWasSeenGetsNoEnter`. `stall: true`, `onSubmit` the matching `UserPromptSubmit` at `t0`. Then: err is nil, and the calls are exactly `[SessionHost.Submit rloop-p3-implement "<sent>"]`. Covers: #1 no Enter and no "not submitted" error when the stall's `UserPromptSubmit` was already seen.
- `TestAStalledPromptStaysConfirmedWhenAnotherPromptFollowsItsUserPromptSubmitInTheSameRead`. `stall: true`, `onSubmit` holding, in order, the matching `UserPromptSubmit` at `t0` and `promptSubmit("rloop-p3-implement", "what are you doing?", t0.Add(time.Second))`. Both are applied by one drain. Then: err is nil, and the calls are exactly `[SessionHost.Submit rloop-p3-implement "<sent>"]`. Covers: #1 no Enter and no "not submitted" error when the match was already read, even if a prompt typed into the pane follows it.
- `TestAStalledPromptWithoutAUserPromptSubmitIsDeliveredByOneEnter`. `stall: true`, `onEnter` the matching `UserPromptSubmit` at `t0`. Then: err is nil, and the calls are exactly `[Submit …, SendKeys rloop-p3-implement enter]`. Covers: #1 the Enter fallback still works for hook sessions.
- `TestAPromptToAnAgentWithoutHookEventsGoesThroughHerdrPrompt`. Table:
  - row "no hook feed": `Hooks` nil.
  - row "a feed with only another agent's events": a pre-queued `Stop` for `rloop-p3-plan`.

  Then: err is nil, and the calls are exactly `[SessionHost.Prompt rloop-p3-implement "go" false 0s]`. Covers: #1 sessions without hooks (codex, the watchdog, first prompts) keep herdr's path unchanged.
- `TestPromptsToOneAgentAreTypedOneAtATime`.
  - Given `submitWait` left at 10 s, and `onSubmit` holding only `"nudge"` → `promptSubmit(agent, "nudge", t0)` (the follow-up's line is pushed by the test).
  - When `go` prompt A (`"follow up"`) runs; after `waitFor` shows `inner.Calls()` holding its Submit, `go` prompt B (`"nudge"`) runs; then `waitFor` `feed.readCount()` to grow by 3.
  - Then `inner.Calls()` is still exactly `[SessionHost.Submit rloop-p3-implement "follow up"]`. After `feed.push(promptSubmit(agent, "follow up", t0))`, both prompts return nil (received over a channel with a 5 s deadline), and `inner.Calls()` is exactly `[Submit … "follow up", Submit … "nudge"]` with no `SendKeys`.

  Covers: overlapping prompts to one agent (a decline follow-up and a stall nudge) are typed one at a time, each confirmed by its own event without an extra Enter.
- `TestAHandOffPromptIsNotTypedWhenAnotherPromptMadeTheAgentBusyFirst`.
  - Given `submitWait` left at 10 s, the idle agent, `onSubmit` holding only `"follow up"` → `promptSubmit(agent, "follow up", t0)`, and `gate := sm.handOff("rloop-p3-implement")`.
  - When `go` prompt A (`"nudge"`, no gate) runs. After `waitFor` shows its Submit, `go` prompt B (`"follow up"`, `gate`) runs, then `waitFor` `feed.readCount()` to grow by 3. Then `feed.push(promptSubmit(agent, "nudge", t0))`.
  - Then A returns nil and B returns `errTurnNotOver` (both received over channels with a 5 s deadline), and `inner.Calls()` is exactly `[SessionHost.Submit rloop-p3-implement "nudge"]`.

  Covers: #2 a hand-off that was ready is not typed once another prompt has started a new turn.

**Hand-off**

The hand-off tests use `newRouterRig`. In each one, unless the test says otherwise:
- `r.loop.Sessions.Now` is `clock.Now` with `clock := &stepClock{t: t0}` (step 0, moved only by `clock.set`).
- `r.loop.Sessions.Poll` is `time.Hour`, so no herdr poll can drive the hand-off.
- `r.loop.Sessions.Hooks` is a `fakeHookFeed` pre-queued with `promptSubmit(askingAgent, "implement phase 3", t0.Add(-time.Minute))`, so the agent is busy.
- `r.loop.Sessions.Host` is `&claudeHost{SessionHost: r.ehost, feed: feed, onSubmit: {<row text>: [promptSubmit(askingAgent, <row text>, t0.Add(time.Hour))]}}`.
- herdr reports the agent idle (the rig's `r.ehost.idle`).
- No session wait runs.

The rows are:
- "answer": the trigger is `r.route(t)` then `r.router.Answer("q1", "sqlite", "docs/x/spec.html:2")`. The text is `r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite`.
- "withdrawal": the trigger is `r.route(t)` then `go r.loop.questionPanicked(routedQuestion(), "boom")`. The text is `r-loop: answer to q1 (by r-loop): withdrawn (panic in question q1: boom); continue without an answer`.

"Delivered" below always means all three of: `r.calls("SessionHost.Submit ")` equals exactly `[askingAgent + " " + strconv.Quote(<text>)]`; `r.typed()` equals exactly the same single entry; and `waitFor` sees `r.session.OpenQuestion.Load()` false (the step is released). "Nothing typed" means `r.calls("SessionHost.Submit ")`, `r.typed()` and `r.calls("SessionHost.SendKeys ")` are all empty and `r.session.OpenQuestion.Load()` is true.

- `TestHerdrIdleDoesNotHandTextToAHookAgentThatIsStillBusy` (table: answer, withdrawal). When the trigger runs, then after `waitFor` `feed.readCount() >= 3`: nothing typed. Covers: #2 herdr idle while the hooks say busy does not deliver, for an answer and a withdrawal.
- `TestTextForAHookAgentIsTypedAtItsStopWithoutAHerdrPollOrASessionWait` (table: answer, withdrawal). When the trigger runs and then `feed.push(HookEvent{At: t0, Agent: askingAgent, Event: "Stop"})`: delivered. Covers: #2 typed after the `Stop`, with a herdr poll of an hour and no session-wait tick.
- `TestTextForAHookAgentIsTypedAtAStopAnotherReaderTook`.
  - Given `slowHookPoll(t)`, so the gate's own reads come only an hour apart.
  - When the answer is given, `waitFor` `feed.readCount() >= 2` (the gate has drained, found the agent busy, and is waiting), then `feed.push(Stop)` and `r.loop.Sessions.hookNotice("rloop-p3-plan")` (the dialog path's reader drains the `Stop`).
  - Then delivered.

  Covers: #2 a `Stop` read by another hook consumer hands the text off at that read, not at the gate's next poll.
- `TestAnAnswerToAHookAgentWhoseTurnNeverEndsIsTypedOnlyOnceTheStallGraceHasPassed`.
  - Given `r.loop.Sessions.StallGrace = 2 * time.Minute`, the busy agent, and no `Stop` ever pushed.
  - When the answer is given and `waitFor` `feed.readCount() >= 3`.
  - Then nothing typed. After `clock.set(t0.Add(3 * time.Minute))`: delivered.

  Covers: #2 a busy flag that never clears falls back to herdr only after the stall grace, so the answer is never held forever.
- `TestAnAnswerToAHookAgentIsTypedAtAStopThatComesAfterTheStallGrace`.
  - Given `StallGrace = 2 * time.Minute`, the busy agent, and herdr reporting it working (`r.ehost.idle[askingAgent] = false`, set under `r.host.mu`).
  - When the answer is given, `waitFor` `feed.readCount() >= 1`, then `clock.set(t0.Add(3 * time.Minute))`, `waitFor` `feed.readCount()` to grow by 2, then `feed.push(Stop)`.
  - Then delivered (with `Poll` an hour).

  Covers: #2 once the fallback is active, a `Stop` is still acted on within the 200 ms hook poll, not the session poll.
- `TestAnAnswerRefusedAsBlockedIsTypedAgainForAHookAgent`.
  - Given `r.ehost.refuse[askingAgent] = 1` and a pre-queued `Stop` (agent idle).
  - When the answer is given.
  - Then `waitFor` `r.typed()` to hold one entry; `r.calls("SessionHost.Submit ")` is exactly two identical answer entries, `r.calls("SessionHost.Refused ")` is exactly `[askingAgent]`, and the question is released.

  Covers: #2 the `agent_blocked` retry still works when the answer goes through hook confirmation.
- `TestAPanicBeforeTheAskingTurnEndsWithdrawsWithoutTypingIntoTheBusyPane`.
  - Given the busy agent, and a feed `next` that returns nil on its first call and panics with `"boom"` on its second.
  - When the answer is given.
  - Then `waitFor` `r.session.OpenQuestion.Load()` false. `r.questions()` holds one question with `AnsweredBy == "withdrawn"` and `Answer == "panic in answer hand-off q1: boom"`, and `r.calls("SessionHost.Submit ")`, `r.typed()`, `r.calls("SessionHost.Prompt ")` and `r.calls("SessionHost.SendKeys ")` are all empty.

  Covers: #2 the panic recovery never types a withdrawal while the hooks say busy.
- `TestAPanicAfterAStopTypesNoWithdrawalWhenAToolRanSince`.
  - Given a pre-queued `Stop` (agent idle), `statePanics` set, and a feed `next` that returns nil on its first call and `HookEvent{At: t0, Agent: askingAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}` on its second.
  - When the answer is given.
  - Then the same assertions as the test above: the question is released and withdrawn with `Answer == "panic in answer hand-off q1: boom"`, and nothing is submitted, typed, prompted or keyed.

  Covers: #2 the recovery re-checks the gate after a fresh drain, so a tool that ran after the `Stop` keeps the withdrawal out of the pane.
- `TestAPanicAfterTheAskingTurnEndedTypesTheWithdrawalAfterHookConfirmation`.
  - Given a pre-queued `Stop` (agent idle), `panics` set to the answer row's text, and `onSubmit` mapping the withdrawal text `r-loop: answer to q1 (by r-loop): withdrawn (panic in answer hand-off q1: boom); continue without an answer` to its `UserPromptSubmit` at `t0.Add(time.Hour)`.
  - When the answer is given.
  - Then delivered with the withdrawal text, and `r.questions()` holds the question withdrawn with `Answer == "panic in answer hand-off q1: boom"`.

  Covers: #2 the panic recovery's withdrawal is typed after the turn end, confirmed through #1's path.
- `TestAnAnswerToAnAgentWithNoHookEventsIsTypedOnceHerdrStopsReportingWorking`.
  - Given `Poll` left at the rig's 1 ms, `Sessions.Host` set to `r.ehost`, a feed holding only `promptSubmit("rloop-p3-plan", "plan phase 3", t0)`, and herdr reporting `askingAgent` working (`idle` false).
  - When the answer is given and `waitFor` `feed.readCount() >= 3`.
  - Then `r.typed()` and `r.calls("SessionHost.Submit ")` are empty. After `r.ehost.idle[askingAgent] = true` (under `r.host.mu`), `r.waitTyped(t, 1)` equals exactly `[askingAgent + ` "r-loop: answer to q1 (by watchdog, citing docs/x/spec.html:2): sqlite"`]`, and `r.calls("SessionHost.Submit ")` is still empty.

  Covers: #2 agents with no hook events keep today's herdr polling, and #1 their herdr-only submit.

**Decline follow-up**

Both decline tests use `newDialogRig`, with:
- `r.loop.Sessions.Now = clock.Now` (`clock := &stepClock{t: t0}`) and `r.loop.Sessions.Poll = time.Hour`.
- A feed pre-queued with `HookEvent{At: t0, Agent: dialogAgent, Event: "PreToolUse", Tool: "Bash", Target: "go test ./..."}`.
- `r.loop.Sessions.Host = &claudeHost{SessionHost: r.dhost, feed: feed, onSubmit: {<decline text>: [promptSubmit(dialogAgent, <decline text>, t0.Add(time.Hour))]}}`, where the decline text is the literal `r-loop: the watchdog declined that request (dialog d1). Do not retry it; finish your task another way, or write a failed sentinel naming what you needed.`.
- `serve`, `raise` and `waitDog(t, 1)`.

When, in both:
1. `r.router.AnswerDialog("d1", []string{"esc"}, "decline", "")` returns `decisionAuthorised`.
2. `r.dhost.set(dialogAgent, AgentIdle, "Interrupted · What should Claude do instead?")`.
3. `waitFor` `feed.readCount()` to grow by 3.

Then, in both, at this point `r.calls("SessionHost.Submit ")` and `r.agentPrompts(dialogAgent)` are both empty. "The follow-up is delivered" means `waitFor` gives `r.calls("SessionHost.Submit ")` exactly `[dialogAgent + " " + strconv.Quote(<decline text>)]`, then `waitFor` gives one `note` event whose `reason` is `dialog d1 declined: told rloop-p2-implement to finish another way`.

- `TestADeclineFollowUpToAHookAgentWaitsForItsIdlePromptWhileHerdrSaysIdle`. After `feed.push(HookEvent{At: t0, Agent: dialogAgent, Event: "Notification", Type: "idle_prompt", Notice: "Claude is waiting for your input"})`: the follow-up is delivered. Covers: #2 `declined()` uses the same gate, ending on `idle_prompt`.
- `TestADeclineFollowUpToAHookAgentWhoseTurnNeverEndsIsTypedOnceTheStallGraceHasPassed`. With `r.loop.Sessions.StallGrace = 2 * time.Minute` and no `idle_prompt` ever pushed: after `clock.set(t0.Add(3 * time.Minute))`, the follow-up is delivered. Covers: #2 the gate's fallback holds for the decline path too.

## Left out

- **`SessionStart` to make agents count as having hook events before their first prompt.** That is phase 3's #4. Its `SessionStart` line will make first prompts take this path with no change here (see `## Assumptions`).
- **A blocked-then-typed retry test for `declined`.** It keys on the same `blocked(typeErr)` (`internal/core/dialogs.go:188`) over the same `SessionManager.prompt` error as `hand`. `TestAnAnswerRefusedAsBlockedIsTypedAgainForAHookAgent` and `TestSubmitReturnsAnAgentBlockedErrorAsHerdrReportsIt` pin that error's path.
- **The fixer's prompt** (`internal/core/fixer.go:384`) and **intake's** (`internal/core/intake.go:58`). Both are first prompts on sessions outside `SessionManager`'s hook state, so no obligation reaches them.
- **The watchdog's prompts** (`internal/core/watchdog.go:102`, `:259-272`). Its pane gets no `R_LOOP_AGENT`, so the tap records nothing for it (`internal/hooktap/hooktap.go:28-31`), and the item keeps it on herdr.
- **A config key for `hookPoll` or `submitWait`.** No obligation asks for one.
- **Clearing the busy flag when a decline's esc is pressed.** It would invent a turn end that no hook reported. Waiting for `idle_prompt` is the documented cost (ADR-93 consequences).

## Assumptions

- **The phase check's warning is resolved this way: first prompts stay on herdr's check in this phase.** Examples are the step start prompt (`internal/core/session.go:303`), the reviewer prompt (`internal/core/review.go:559`) and the fixer prompt (`internal/core/fixer.go:384`). "Has hook events" stays tied to the agent's own events, not to the provider's settings having hooks.
  - Keying it off the settings would make a fully broken hook setup (wrong binary path, ignored settings) fail every step's first prompt with "not submitted". That reverses ADR-93's "a hook that never fires costs only the grace wait" (`docs/task-loop-driver/spec.html:4171`).
  - `hookLog.apply` creates an agent entry for any event name (`internal/core/hooks.go:37-41`). So once phase 3's #4 writes a `SessionStart` line before the first prompt, `SessionManager.prompt` gives first prompts hook confirmation with no further change. The start and reviewer prompts already go through it.
  - #1 therefore covers nudges, review findings, answers, withdrawals and decline follow-ups.
- **The `UserPromptSubmit` hook input's `prompt` field holds the full submitted text**, including text herdr pastes into the pane. Claude may trim its ends or re-flow line breaks, so matching collapses whitespace runs to one space. It never joins words, so `go test` and `gotest` stay distinct.
- **The tap and the driver run on the same machine.** `m.now()` before `Submit` and the tap's `time.Now()` stamp share one wall clock, so "at or after the send" is a plain time comparison.
- **Each agent has at most one pending confirmation.** `prompt` serialises the driver's typing into one agent behind its per-agent lock, and the confirmation is a sticky mark set when the matching line is applied. So neither a later driver prompt nor text the maintainer types into the pane can undo it.
- **A hook-feed read error during `prompt` or the gate is ignored, as `hookNotice` and `treeBlame` already do** (`internal/core/hooks.go:98`, `:168`). The first such error is still warned about only by the session wait (`internal/core/session.go:366-374`).
- **For an agent that has hook events and has ended its turn, herdr reporting `working` does not hold the hand-off.** The hooks decide, and herdr is consulted only for `gone` and, in `declined`, for `blocked`. Claude queues input that arrives mid-turn, so a lagging herdr state costs nothing.
- **A declined dialog's follow-up to a claude agent now waits for `idle_prompt` (about 60 s), or for the stall grace if that never comes.** Claude sends no `Stop` for a turn interrupted by esc. Both waits are shorter than the 2-minute stall that ADR-62's amendment set out to avoid, and the item binds `declined()` to the same gate.
- **`submitWait` is 10 s, matching herdr's `submitTimeout`** (`internal/herdr/client.go:25`). `hookPoll` is 200 ms.

## Gate

`go test ./internal/hooktap/ ./internal/herdr/ ./internal/core/ -run '^(TestTapKeepsACappedUserPromptSubmitPromptAndMarksACut|TestSubmitTypesThePromptAndReturnsOnceTheAgentStarts|TestSubmitReportsAStalledPromptWithoutPressingEnter|TestSubmitReturnsAnAgentBlockedErrorAsHerdrReportsIt|TestAPromptToAHookAgentIsDeliveredByAMatchingUserPromptSubmit|TestAPromptToAHookAgentIsNotDeliveredWithoutAMatchingUserPromptSubmit|TestAStalledPromptWhoseUserPromptSubmitWasSeenGetsNoEnter|TestAStalledPromptStaysConfirmedWhenAnotherPromptFollowsItsUserPromptSubmitInTheSameRead|TestAStalledPromptWithoutAUserPromptSubmitIsDeliveredByOneEnter|TestAPromptToAnAgentWithoutHookEventsGoesThroughHerdrPrompt|TestPromptsToOneAgentAreTypedOneAtATime|TestAHandOffPromptIsNotTypedWhenAnotherPromptMadeTheAgentBusyFirst|TestHerdrIdleDoesNotHandTextToAHookAgentThatIsStillBusy|TestTextForAHookAgentIsTypedAtItsStopWithoutAHerdrPollOrASessionWait|TestTextForAHookAgentIsTypedAtAStopAnotherReaderTook|TestAnAnswerToAHookAgentWhoseTurnNeverEndsIsTypedOnlyOnceTheStallGraceHasPassed|TestAnAnswerToAHookAgentIsTypedAtAStopThatComesAfterTheStallGrace|TestAnAnswerRefusedAsBlockedIsTypedAgainForAHookAgent|TestAPanicBeforeTheAskingTurnEndsWithdrawsWithoutTypingIntoTheBusyPane|TestAPanicAfterAStopTypesNoWithdrawalWhenAToolRanSince|TestAPanicAfterTheAskingTurnEndedTypesTheWithdrawalAfterHookConfirmation|TestAnAnswerToAnAgentWithNoHookEventsIsTypedOnceHerdrStopsReportingWorking|TestADeclineFollowUpToAHookAgentWaitsForItsIdlePromptWhileHerdrSaysIdle|TestADeclineFollowUpToAHookAgentWhoseTurnNeverEndsIsTypedOnceTheStallGraceHasPassed)$'`
