# Driver — backlog from watching the live fix run of 23 Sep 2026

These are problems seen while run `20260923-153001` worked through
`issues-tui-live-run-2026-09-23.md`. Each item is real work. The evidence and the open design
choices are in `issues-driver-run-2026-09-23-notes.md`. `[#n]` numbers the observations in the
order they were made.

Verified against `r-loop` @ `main` `feaf292`.

- [ ] [#1] The /review skill is not available in this session, so I'll perform the same report-only review directly
      - A codex reviewer actually runs codex's own review command, rather than reading `/review` as prose inside the round prompt and falling back to an ad-hoc review
      - When a reviewer's `review` command cannot run in its session, the round records that as a reviewer failure or a warning the Face shows, never as a clean review
      - The claude reviewer's `/code-review` path is checked the same way, and the plain face and `report.md` name which command each reviewer really ran

- [ ] [#2] Every issues-file item gets a "Files: none understates the cut" warning from the phase check
      - For a backlog item, the phase check does not present `Files: none` and `Risk:` as the plan's claims, because an issues file carries neither by design
      - The watchdog still reports a real mismatch between an item and the code, but not the absence of a `Files:` line on a backlog item
      - A plan-file phase with an explicit `Files:` line is checked exactly as today

- [ ] [#3] Mark test runs' phases, so a sandbox run under `/test-app` can't be mistaken for the real run
      - A run carries a label that goes on every herdr workspace label (`p1 plan`, `◆ watchdog`, `◆ intake`) and the sidebar `rloop` token. It defaults to the repo's directory name, and the sandbox's `.r-loop/config.yaml` sets it to `test`, so a nested run reads `test p9 plan`
      - Herdr agent names (`rloop-p<N>-<kind>`, reviewer and watchdog names) are unique per run, so two runs on one machine never clash over a name, whatever their phase numbers
      - `--dry-run` shows the label and where it came from, like every other key, and an unknown or flow-style value is still rejected

- [ ] [#4] Reviewer panes get no `R_LOOP_*` environment, so a reviewer cannot tell it runs inside an r-loop step
      - A reviewer pane split beside a step starts with `R_LOOP_RUN`, `R_LOOP_PHASE` and `R_LOOP_STEP` set as a step session's are, plus a variable naming the reviewer
      - A skill that a reviewer runs (such as `/test-app`) can detect from the environment alone that it is inside a live run, and a test proves the variables reach the reviewer's process
      - Step sessions keep exactly the environment they have today
