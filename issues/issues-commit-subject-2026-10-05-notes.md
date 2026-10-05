# Notes — issues-commit-subject-2026-10-05

## [#1] Evidence

- Run `20261005-175851`, phase 47, implement attempt 1. All 16 of the phase's files were changed and
  `internal/analyze/` was written. Then `.r-loop/runs/20261005-175851/phase-47/implement-a1.sentinel` held
  `{"outcome":"ok","commit":"feat(analyze): detect modules, read findings from sarif and pmd reports, add analyze config and preflight"}`,
  108 characters.
- `internal/core/session.go:553` — `SessionManager.subject` calls `ValidSubject` and fails the step with
  `sentinel commit: commit subject is longer than 100 characters` (`internal/core/commit.go:26`).
- The failure raised blocker `b1` (actions retry, switch, block, stop). The watchdog could not retry
  on its own, because `restart` is not allow-listed, so the maintainer had to answer. The retry
  started a fresh session (`-a2`) only to re-check finished work and rewrite one line, and it used one
  of the step's 2 retries.
- `internal/core/commit.go:40` already has `Subject(prefix, text)`, which cuts at a word boundary
  within 100 characters. The milestone report and the `## Resolve first` commits use it.

## [#1] Open choice: how to repair the subject

- **Ask the same session to rewrite it (recommended).** The driver types a one-line correction into
  the step's own pane: the subject is N characters, write the sentinel again with one of 100 or fewer.
  It then waits for the sentinel as it does after a fix round. The agent still writes the subject, as
  ADR-87 requires, and nothing is redone. Cost: one more turn, and a bound on how many times it asks
  (once is enough), after which the step fails as today.
- **Shorten it in the driver.** Cut the subject at a word boundary with `core.Subject`, and record a
  `commit-subject` warning that has both the original and the shortened subject. Cost: no round trip,
  but the cut can drop the end of the description, such as "...add analyze config and" losing
  "preflight". It is closer to the "fallback subject" ADR-87 rejected, though it keeps the agent's
  own words.

Either way, ADR-87's "fails the step on a missing or malformed one" needs an amendment: an
over-long subject is repaired rather than failed. The prompts' 100-character line in
`internal/prompts/render.go:56` stays as it is.

## [#2] Evidence

- Run `20261005-175851`, phase 47, implement review round 1. Blockers `b2` and `b3` came from
  `phase-47/implement-rv-ui`, agent `rloop-b3uys-p47-impl-km1zw-r1-a2`:
  `reviewer ui: herdr: agent … never showed codex's prompt`.
- Both times the pane's visible screen ended in `› Ask Codex to do anything` and codex's footer, so
  codex was ready. The top of the screen was the bottom of the welcome box (`model:` / `directory:`)
  and a two-line tip. The `>_ OpenAI Codex` line had scrolled off. The pane is the third reviewer
  split beside the step, about 12 rows tall. The codex and security reviewer panes in the same round
  were taller and started.
- `internal/herdr/client.go:235` — `awaitCodexPrompt` returns only when the visible screen contains
  `codexBanner` (`>_ OpenAI Codex`, line 230), within `paneBusyBudget` (20s, line 25). A short pane
  never contains it, so retrying cannot help.
- The second retry was run on the maintainer's word; if it fails again, the round needs `skip` or a
  provider switch.

## [#2] Likely fix

Treat codex's input prompt (`›` followed by the placeholder) or its status footer as the ready
signal, either alone or alongside the banner. Or read the agent's recent history rather than only
the visible screen, so the banner is found even after it scrolls. Check whether claude's detection
(`claudeBanner`, `internal/herdr/client.go:232`) has the same weakness in a short pane.
