# Driver — backlog from watching run 20261005-175851

Seen while run `20261005-175851` built Milestone 18. The evidence and the open design choice are in
`issues-commit-subject-2026-10-05-notes.md`.

Verified against `r-loop` @ `main` `190323e`.

- [x] [#1] A commit subject over 100 characters fails a finished implement step and holds the run on a blocker
      - A step whose work is done and whose `ok` sentinel carries a subject that is valid except for its length does not fail, raise a blocker or use up a retry
      - The commit that lands is still a Conventional Commits subject of at most 100 characters, written from the agent's own words, never a generic fallback (ADR-87)
      - The run records that the subject was too long and what was committed instead, and the face shows it as a warning
      - A subject with the wrong shape, a phase or step label, more than one line or no subject at all is repaired by the driver too, never failed (maintainer's decision, ADR-87 amended 2026-10-05)

- [x] [#2] A codex reviewer in a short split pane fails with "never showed codex's prompt" although codex is ready
      - A codex session that has reached its input prompt is detected as ready whatever the pane's height, including a third reviewer split of about 12 rows where the `>_ OpenAI Codex` banner has scrolled off screen
      - Codex's trust dialog is still answered first, and a pane that truly never reaches codex's prompt still fails after the budget, as today
      - A test pins the readiness check against a short screen that holds codex's prompt and footer but not its banner
