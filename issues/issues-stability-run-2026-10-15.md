# Stability run — backlog from the remaining-issues list of 15 Oct 2026

24 asks: #4–#14 from the severity list of issues left after the stall, prompt-delivery, abort and
evidence fixes, plus #15–#27 found later by `/test-app full` and the verifiers. 17 are work and are
below as 16 items (#4 and #5 are merged into one). The other 7 — three questions, one by design and
three already handled or specified — are in `issues-stability-run-2026-10-15-notes.md`. `[#n]` is
the list's own numbering.

Added 15 Oct 2026, after the commits: #28 (a test that leaks processes), verified against `main` `e80b114`.
#29 (the TTL reaper's `sleep` outliving `tui-session.sh stop`) lives in the skill-pack, so it is in
the notes only.

Verified against `r-loop` @ `main` `81998cd` **plus the uncommitted working tree** (stall fix,
codex first-prompt confirmation, reviewer close on abort, evidence ranges/prefix repair, fix-sentinel
swap). #6, #12, #21 and #23 are defects in or next to those uncommitted fixes: commit them first,
or fix these before committing.

- [x] [#12] Background work is only recognised when it's started through Claude's Agent or Bash tools  <!-- fixed: r-loop/phase-6 -->
      - A set background flag is time-bounded: an agent idle with background work pending is still nudged after a bounded multiple of StallGrace, never only at the step backstop
      - A test ends a turn after a never-finishing `run_in_background` command with no sentinel and asserts a nudge well before the backstop
      - A text-only reply to a nudge while background work is pending does not fail the step at once while the nudge cap (#6) is not reached

- [x] [#6] The new stall fix allows endless nudges  <!-- fixed: r-loop/phase-6 -->
      - The watch counts the nudges it sends; a nudge past a fixed cap fails the step with Stalled=true and a reason naming the count
      - The cap covers both the hook turn-end path and the herdr idle-quiet path, so codex sessions are bounded too
      - A test where the agent answers every nudge with a tool call and no sentinel fails at the cap, well before the backstop
      - ADR-62 and tech-design.md (stall rule) describe the new nudge-and-cap rule

- [x] [#21] Re-type the first prompt only while the agent is idle  <!-- fixed: r-loop/phase-10 -->
      - When the rollout never shows the marker but the host reports AgentWorking after the first Host.Prompt, the prompt is typed exactly once and the spawn or reviewer launch does not fail
      - A re-type happens only when Host.State reports idle or done; otherwise the prompt counts as received
      - When herdr saw the agent start but no rollout confirms the prompt, the step continues and one warning names the agent and the failed rollout lookup
      - When the agent never leaves idle and no rollout confirms it, the existing "prompt to <agent> not received" failure still happens

- [x] [#10] Stall blockers carry no evidence  <!-- fixed: r-loop/phase-10 -->
      - A step blocker for a failed or stalled step includes the normalised end of the agent's pane screen, as reviewer blockers already do (spec.html: "the reason and the end of the session's screen")
      - The blocker text names the step's agent and its last few tool calls (tool and target) seen by hooks
      - The transcript path, when known, is included so the watchdog can read the last assistant message
      - A test asserts that the stall blocker text routed to the watchdog contains the screen excerpt

- [x] [#15] Raw escape bytes on stderr  <!-- fixed: r-loop/phase-15 -->
      - `r-loop "$(printf 'x\033[2Jy\033]0;pwn\a.md')" --dry-run --plain` exits 2 and its stderr contains no 0x1b or 0x07 byte
      - A plan whose phase title carries ESC/OSC/BEL and no checklist items exits 2 with the title shown with control bytes removed
      - `r-loop status` errors (repo open, load run, plan read), the Getwd error and the usage line print no C0/C1 control bytes
      - An app test passes an error containing \x1b and \x07 through the stderr error path and finds neither

- [x] [#23] settleStatic: a cancelled static analyzer can still raise a blocker  <!-- fixed: r-loop/phase-7 -->
      - When the context is cancelled while the static analyzer runs, no blocker is raised: no question record and no blocked-on event
      - That round returns a failed outcome, not blocked, with an interrupted reason; the run still records halted/aborted
      - A static analyzer failure without cancellation still raises the reviewer blocker with retry/block/stop

- [x] [#7] An abort during a review is recorded as a reviewer blocker plus a step failure ("interrupted: context canceled"), not as `aborted`  <!-- fixed: r-loop/phase-7 -->
      - An abort while a reviewer is working records the step as failed with reason `interrupted: context canceled`, without the `reviewer <id>:` prefix
      - The run record is still halted/aborted and report.md still shows "aborted during phase-N/<step>"
      - The reviewer's own review-find event still records state failed
      - A real reviewer failure without cancellation keeps its `reviewer <id>:` prefix

- [x] [#13] markWaiting moves focus to the watchdog pane (host.Focus)  <!-- fixed: r-loop/phase-8 -->
      - With the default config, the watchdog entering watchdog-waiting (by ask_maintainer or by herdr reporting it blocked) does not run `herdr agent focus`
      - In an --unattended run, markWaiting never calls Focus
      - watchdog-waiting is still recorded and emitted to the face (amber state) exactly as before

- [x] [#8] The watchdog misdiagnoses problems, and its tool results aren't logged  <!-- fixed: r-loop/phase-8 -->
      - After a refused submit_triage, events.jsonl has an event for that call with accepted=false and the refusal reason text
      - After an accepted watchdog tool call, events.jsonl records its result (accepted, or the decision and any reason)
      - Errors from resume() or an absent handler ("no triage is open", "not available") are recorded as the call's result
      - The existing watchdog-call event is still recorded before the handler runs

- [x] [#17] Plan outside the repo is rejected with a raw git error  <!-- fixed: r-loop/phase-15 -->
      - A plan outside the repo exits 2 with one r-loop message naming the plan path and the repo root; stderr contains neither "ls-files" nor "fatal:"
      - A symlink inside the repo that resolves outside it is rejected the same way
      - A plan inside the repo, tracked or gitignored (ADR-84 local todo), still passes `--dry-run --plain`
      - Resume of a run whose recorded plan is outside the repo gives the same message

- [x] [#4] Let the watchdog read the pane before raising a stall blocker / [#5] Make re-nudging the same session the first remedy for a stall, not retry  <!-- fixed: r-loop/phase-4 -->
      - The watchdog can resolve a stall blocker with an action that types a continuation into the same live session instead of starting Attempt+1
      - After that action the driver watches the same session again, and a sentinel written then is judged as that step's outcome
      - The action is refused when the agent is gone, and retry/continue keep counting against watchdog.maxRestarts
      - ADR-62 and ADR-97 are amended, tech-design.md is updated, and watchdog.md lists the in-place re-nudge as the first remedy for a stall whose pane is alive

- [x] [#11] Prompt-side guard. Tell step agents to run tests in the foreground  <!-- fixed: r-loop/phase-6 -->
      - The shared sentinel/outcome prompt partials tell step agents to run builds and tests in the foreground and not to end a turn while background work they started is still running
      - The wording still allows review.md's wait for a forked slash command's task notification
      - render_test.go asserts the guard appears in the plan, implement, fix and gatefix renders

- [x] [#16] internal/app tests are slow: with -race one pass takes about 350s, so `-race -count=3` hits Go's default 10m timeout  <!-- fixed: r-loop/phase-16 -->
      - A session start with HookStartWait=5ms and no SessionStart hook returns within about 10ms, not after a full 200ms hookPoll
      - TestNoTriageTableIsTypedIntoTheWatchdogPaneWithoutAGate and TestUnattendedAndYesStartWithoutAGate each run in under half their current time without -race
      - Fake binaries reach the code under test without process-wide t.Setenv("PATH"), so fixture tests can run with t.Parallel and `go test -race -count=3 ./internal/app/` finishes inside the default 10m
      - TestALoserNamesAWinnerThatPublishesLate no longer sleeps a fixed 2.5s

- [ ] [#20] no_agent.sh dirty-tree check passes when the herdr server is unreachable
      - The dirty-tree check prints OK only when stderr names calc.go and "primary tree is not clean"; "herdr server unreachable" is avoided with a stub herdr first on PATH or reported as NOT RUN
      - With herdr unreachable and a clean tree, the check does not print OK
      - A relative TEST_APP_BIN (`bin/r-loop` from the repo root) works: BIN is made absolute against the caller's cwd before any cd
      - The refused dirty run still creates no run directory

- [x] [#18] Banner wording: "phases 3" uses the plural for one phase  <!-- fixed: r-loop/phase-15 -->
      - A single blocked phase renders "Resolve first: <entry> → phase 3"; two render "→ phases 3, 4"
      - The amber preflight line says "blocks phases 3, 4" for two phases and "blocks phase 3" for one
      - triage_test.go expectations and the tech-design.md banner template match the new wording

- [ ] [#24] Untested paths: a normal live run can't reliably trigger the nudge-reply path or the path repair
      - A live script with implement on claude and a committed prompt override has the agent start `sleep 60` with run_in_background and end its turn; hooks.jsonl holds a "background":true line, events.jsonl has no nudge or stall for that step, and the step ends ok
      - A live script whose override makes the agent end its first implement turn without a sentinel shows stalled → nudge → running → ok in events.jsonl, with no "no response to nudge"
      - A live script with a guaranteed finding and a fix.md override asking for `.r-loop/wt/phase-1/<file>:<line>` evidence records an "… read as …" warning and accepts the round
      - Each script prints NOT RUN without HERDR_PANE_ID, inside an r-loop run, or when the step provider is not claude, and reports when the agent ignored the steering rather than calling it a driver bug

- [x] [#28] Tests leak orphaned processes: Gradle writer loops from TestAnalyzeStopsADetachedGradleDaemonBeforeItWritesAfterTheTimeout  <!-- fixed: r-loop/phase-16 -->
      - After `go test -count=20 ./internal/analyze/ -run TestAnalyzeStopsADetachedGradleDaemon`, no `sh -c while :; do … late.html …` process is left running
      - The test's detached stub is killed in t.Cleanup even when Analyze's cancel-time descendant walk misses it, e.g. by recording the stub's pid or process group in a file and killing it
      - If the leak reproduces with the test's own cleanup removed, Analyze's cancel path (descendants() snapshot in internal/analyze/run.go) is fixed so a detached grandchild started before the timeout is always killed
