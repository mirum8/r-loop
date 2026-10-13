# Driver hooks — backlog from the suggestions of 13 Oct 2026

8 suggestions on using claude hooks to improve the driver's own work; 6 are work and are below.
The other 2 — a question about the watchdog and one already built — are in
`issues-driver-hooks-2026-10-13-notes.md`, with what the code says about each. `[#n]` is the
suggestion's own numbering.

Verified against `r-loop` @ `main` `0e188e3`.

- [x] [#1] Prompt delivery  <!-- fixed: r-loop/phase-1 -->
      - The hook tap keeps a capped copy of the `UserPromptSubmit` prompt text per agent, and `core.HookEvent` carries it
      - For a session that reports hooks, a prompt counts as delivered only when a matching `UserPromptSubmit` from that agent arrives after the send; herdr reporting working alone is not confirmation
      - When herdr reports `agent_prompt_stalled` but the matching `UserPromptSubmit` was already seen, no Enter is pressed and no "not submitted" error is returned
      - Sessions without hooks (codex, the watchdog) keep today's herdr-only submit path unchanged

- [x] [#2] Answer hand-off  <!-- fixed: r-loop/phase-1 -->
      - For an agent that reports hooks, a watchdog answer (and a withdrawal) is typed only after the asking turn's `Stop` or `idle_prompt` has been read; herdr idle while hooks say busy does not deliver it
      - Delivery happens within one hook read of the `Stop`, without depending on a 2s herdr poll or a session-wait tick running
      - The decline follow-up in `declined()` uses the same gate
      - Agents with no hook events keep today's herdr polling, and a busy flag that never clears falls back to herdr after the stall grace, so an answer is never held forever

- [x] [#3] Stalls  <!-- fixed: r-loop/phase-3 -->
      - With hooks on, a step whose last hook event is `Stop` (or `idle_prompt`) and that has no sentinel gets the nudge on the next tick instead of after the 2-minute grace; it counts as the step's single nudge
      - A second turn end with still no sentinel, or a further grace of quiet, fails the step with `stalled: no response to nudge`, as today
      - Sessions without hooks keep today's herdr-state stall rule exactly, and tests cover both paths

- [x] [#4] Startup and hook health  <!-- fixed: r-loop/phase-3 -->
      - The run's claude settings register a `SessionStart` hook, and `hooks.jsonl` gets a `SessionStart` line for each claude agent before its first prompt
      - A claude agent that sends no `SessionStart` within a fixed time after start produces exactly one warning naming the agent and the hook-tap executable, and its turn end is then decided by herdr even if hook events arrive later
      - An agent counted as hook-enabled is busy from the moment its prompt is typed, so a sentinel written before any `UserPromptSubmit` is read cannot be judged early
      - Providers without hooks and runs without a hook feed never get the warning

- [x] [#5] Gone agents  <!-- fixed: r-loop/phase-3 -->
      - The run's claude settings register a `SessionEnd` hook, and the tap records its `reason`
      - A step with no sentinel whose agent reports `SessionEnd` with reason exit, logout, prompt_input_exit or other fails on the next tick with a reason naming it (e.g. `agent exited: logout`); with a sentinel present it is judged instead
      - `SessionEnd` with reason clear or resume does not fail the step
      - A test with a herdr fake that never reports gone shows the step ending from the hook alone, well before the stall grace or the backstop

- [ ] [#6] Transcripts for post-mortems
      - When a claude agent sends its first hook, the run log gets one non-fatal `agent-transcript` event with the agent name, `session_id` and `transcript_path`
      - For a codex worker or reviewer, the same event names its rollout file under `~/.codex/sessions`, matched by the session's worktree and start time, not by "newest file"
      - A blocked or aborted step's line in `report.md` lists the transcript paths of its worker and every reviewer recorded for that phase and step; a step with none renders as today
