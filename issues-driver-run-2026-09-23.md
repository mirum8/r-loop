# Driver — backlog from watching the live fix run of 23 Sep 2026

These are problems seen while run `20260923-153001` worked through
`issues-tui-live-run-2026-09-23.md`. Each item is real work. The evidence and the open design
choices are in `issues-driver-run-2026-09-23-notes.md`. `[#n]` numbers the observations in the
order they were made.

Verified against `r-loop` @ `main` `feaf292`.

Second batch (`[#n/2]`), from watching run `20260923-171431`, which fixed the first six. Only these
items were checked, against `main` `cb62983`.

- [x] [#1] The /review skill is not available in this session, so I'll perform the same report-only review directly  <!-- fixed: r-loop/phase-1 -->
      - A codex reviewer actually runs codex's own review command, rather than reading `/review` as prose inside the round prompt and falling back to an ad-hoc review
      - When a reviewer's `review` command cannot run in its session, the round records that as a reviewer failure or a warning the Face shows, never as a clean review
      - The claude reviewer's `/code-review` path is checked the same way, and the plain face and `report.md` name which command each reviewer really ran

- [x] [#2] Every issues-file item gets a "Files: none understates the cut" warning from the phase check  <!-- fixed: r-loop/phase-2 -->
      - For a backlog item, the phase check does not present `Files: none` and `Risk:` as the plan's claims, because an issues file carries neither by design
      - The watchdog still reports a real mismatch between an item and the code, but not the absence of a `Files:` line on a backlog item
      - A plan-file phase with an explicit `Files:` line is checked exactly as today

- [x] [#3] Mark test runs' phases, so a sandbox run under `/test-app` can't be mistaken for the real run  <!-- fixed: r-loop/phase-3 -->
      - Two unlabelled runs on one machine never clash over a herdr agent name, whatever their phase numbers. Every step, reviewer and gate agent name carries something unique to its run, still within herdr's name limit
      - `r-loop resume` still finds a run's step and reviewer agents under the new names, including for a run started before this change
      - A labelled run keeps its label in workspace and agent names, as `4077431` built it

- [x] [#4] Reviewer panes get no `R_LOOP_*` environment, so a reviewer cannot tell it runs inside an r-loop step  <!-- fixed: r-loop/phase-4 -->
      - A reviewer pane split beside a step starts with `R_LOOP_RUN`, `R_LOOP_PHASE` and `R_LOOP_STEP` set as a step session's are, plus a variable naming the reviewer
      - A skill that a reviewer runs (such as `/test-app`) can detect from the environment alone that it is inside a live run, and a test proves the variables reach the reviewer's process
      - Step sessions keep exactly the environment they have today

- [x] [#5] Phase 1's gate "failed" with "step committed before review" when the maintainer committed to main during the step  <!-- fixed: r-loop/phase-5 -->
      - When `HEAD` moves during a step that runs in the primary checkout, and the new commits were not made by that step's session, the step is not failed as its own fault. The reason names the commits and says `HEAD` moved from outside the step
      - A step that really commits before review is still failed as today, with today's reason
      - The watchdog can restart a step that failed this way without the phase first being marked blocked

- [x] [#6] A codex implement step cannot run the full test suite: its sandbox forbids local listeners (`bind: operation not permitted`)  <!-- fixed: r-loop/phase-6 -->
      - A codex step session can run the repository's full `go test ./...`, including the `internal/askmcp` and `internal/app` tests that open local TCP listeners, or the implement prompt names exactly which packages it cannot run and why
      - The shipped codex provider block's `flags` carry whatever codex needs for that, and a provider test pins them

- [ ] [#1/2] A finding the author judges "not real" or "out of scope" closes the round with no second look
      - A P1 or P2 finding the step's own session judges `not-real` or `out-of-scope` does not by itself make the round clean: the next round's reviewers, or the watchdog when no round is left, see the dismissal and its evidence and can reopen it
      - An `out-of-scope` verdict names where the work goes instead (another backlog item, or a new one), and the face and `report.md` show every dismissed P1/P2 with its reason
      - A `real` verdict and a P3 dismissal behave as today

- [ ] [#2/2] A late edit by the step's own session is reported as "reviewer modified the tree"
      - When the tree changes during a review round, the failure names who changed it: the step's own session, a reviewer, or unknown, not always the reviewer
      - The step's session cannot change the tree after writing its sentinel without the driver saying so, and the round's reason says which file changed after the sentinel

- [ ] [#3/2] Reviewer agent names lose their `-rv-<name>` part under the 32-character cap
      - A reviewer's agent name always shows which reviewer it is (`-rv-<name>` or a stable short form of it), whatever the label and run token, and still fits the 32-character cap
      - `tech-design.md` ("Names and paths") and the code say the same thing about which part of a name survives the cut, and a test pins it for a codex, a claude and a named (`ui`) reviewer

- [ ] [#4/2] Interactive-only start flags break the nested `codex exec review`
      - The `{args}` a provider's `review` command receives are only the flags that command accepts: flags `codex exec review` rejects are not passed to it
      - A test runs the shipped codex `review` command's argument list through codex's own `exec review` parser, or pins the accepted set, so a flag that breaks it fails a test and not a live run

- [ ] [#5/2] `TestLiveHerdr` fails: herdr still reports an interrupted agent as idle
      - After r-loop interrupts an agent, the herdr adapter waits for, or correctly reads, the state herdr 0.9.0 actually reports, and `R_LOOP_LIVE_HERDR=1 go test ./internal/herdr/ -run TestLiveHerdr` passes against a running herdr server
      - If herdr's reported state after an interrupt is `idle` by design, the adapter and the test say so, and the stall and restart paths that rely on the interrupt still work

- [ ] [#6/2] Every `/test-app` sandbox leaves a trusted-project entry in `~/.codex/config.toml`
      - A codex session r-loop starts in a throwaway directory (a `/test-app` sandbox or its worktrees) leaves no `[projects."…r-loop-sandbox-…"]` entry behind in the maintainer's codex config
      - Entries for real repositories the maintainer trusted are never touched

