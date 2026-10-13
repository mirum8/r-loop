# Driver hooks — notes on the suggestions of 13 Oct 2026

Read against `r-loop` @ `main` `0e188e3`. The 6 buildable suggestions are in
`issues-driver-hooks-2026-10-13.md`. This file covers the 2 that did not become work, the
suggestions whose premise the code contradicts, and the ones that touch architecture or the
estimate.

## Questions — need an answer before they can become work

**[#7] Watchdog state — which failure is this meant to fix?**
The watchdog does load the run's hook settings, but its pane gets no environment
(`internal/core/watchdog.go:109`), so the tap drops its events (`internal/hooktap/hooktap.go:28`).
The premise that the amber row would gain a precise signal mostly does not hold. The row is
already driven explicitly, not guessed: the watchdog calls the `ask_maintainer` MCP tool before
every question to you (`internal/askmcp/watchdog.go:396`, which runs `markWaiting`), and herdr
refusing a prompt with `agent_blocked` also marks it (`watchdog.go:284`). A `Notification` hook
would only catch a permission dialog or a question asked without that tool call.
Timing the driver's messages to an idle watchdog adds little too. `Post`/`Notify` queue the
messages and type them one at a time (`watchdog.go:134-223`), herdr waits only for the prompt
to be submitted, and claude queues input that arrives mid-turn. There is also a structural cost:
hook state lives in `SessionManager`, the feed has a single read cursor, and the watchdog has no
handle to either. If you have seen a concrete problem, such as an unflagged watchdog dialog or a
message lost mid-turn, it is worth building around that; otherwise this is not worth the change.

## Already built, or built differently than the suggestion assumes

**[#8] Earlier plan-drift warnings — already mid-step**
`files-outside-plan` does not wait for judgement. It is a watch check (`internal/core/checks.go:51`,
`checks.go:92-112`) that runs every 10 seconds while a step is live (`internal/core/watch.go:13`,
`watch.go:352-374`). It compares the worktree's diff, including uncommitted and untracked files,
against the phase's `Files:`, and warns once per attempt; the loop raises the warning while the step
still runs (`internal/core/loop.go:1039`). A hook trigger would save at most about 10 seconds and
would still miss files changed through shell commands, so it could only sit beside the diff check.
Nothing to do unless 10 seconds turns out to be too slow.

**[#3] Stalls — a long `go test` is already not a stall**
While claude runs a long Bash command its spinner is up, herdr reports `working`, and working time
resets the quiet counter (`internal/core/session.go:441-450`). Detecting "a `PreToolUse` with no
`PostToolUse`" is not possible today anyway: `PostToolUse` is registered only for edit tools
(`internal/providers/registry.go:188`). What hooks really add is the other half — nudging at once
when a turn ends with no sentinel — which is what the backlog item asks for.

**[#4] Startup and hook health — fully broken hooks already fall back**
An agent counts as hook-enabled only after it sends a first event (`internal/core/hooks.go:92`).
Hooks that never fire at all (wrong path, settings ignored) leave the agent on herdr's turn-end
rule, just silently. The case that costs time is a partial failure: some events arrive but `Stop`
does not, and `settle` then waits the full 2-minute grace before judging
(`internal/core/session.go:486-495`). The backlog item covers both the warning and that case. Also
note that the settings file names the binary resolved once at run start (`internal/app/wire.go:802`):
a reinstall that removes the old path breaks hooks for the rest of the run.

**[#6] Transcripts — not on `agent-named`**
`agent-named` is written before the agent is started (`internal/core/session.go:280-286`,
`internal/core/review.go:545-550`), before any hook can have fired, and codex agents send no
claude hooks at all. The paths need a later event: from the first hook for claude, from a rollout
lookup for codex. The glob over `~/.codex/sessions` in `internal/quota/quota.go:69-86` can be reused;
matching a rollout to an agent cannot, since that code reads rate limits only.

## Moves architecture or the estimate

**[#1] Prompt delivery** — in the backlog, flagged here.
The press-Enter-on-stall logic lives inside the herdr adapter (`internal/herdr/client.go:387-395`),
but hook events are read only by core's `SessionManager`, through a single read cursor. Confirming a
prompt from hooks means moving the fallback into core — a `Prompt` that reports "stalled" without
pressing Enter — and touching every caller of `Host.Prompt`. It also widens ADR-93, which says hooks
are used for four things only. This is the largest item here.

**[#2] Answer hand-off** — in the backlog, flagged here.
A small change in `RunLoop.hand` and `declined()`, but it adds a fifth use of hooks to ADR-93's list,
so the ADR needs amending with it.

**[#3] Stalls** — in the backlog, flagged here.
An immediate nudge changes when ADR-62's single nudge fires. The further step in the suggestion — a
blocking `Stop` hook telling claude to write its sentinel — is left out of the backlog item on
purpose: the tap would have to print a decision and know the sentinel path, which reverses ADR-93's
"prints nothing, always exits 0", and it would replace the driver's nudge with one the agent's own
harness sends. That needs its own ADR decision first.
