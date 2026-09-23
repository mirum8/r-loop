# Driver — notes on the /test-app run on the pre-run triage change, 23 Sep 2026

Read against `r-loop` @ `main` `492697c`, with the uncommitted triage change in the working tree.
All five leftover findings are work and are in `issues-triage-test-run-2026-09-23.md`. This file
records where the report's description of the cause is not what the code does, and the design
choices the fixes have to make.

The four bugs the run found in the triage change itself are not in the backlog, because they are
already fixed in the working tree, each with a test that failed first:
- control bytes reached the terminal through the triage table and the dry-run run list;
- `ValidateTriage` accepted newlines and control characters in text the watchdog writes;
- a phase skipped by triage kept the queued `·` in the TUI;
- the feed said "verifying 1 phases".

## Questions — need an answer before they can become work

**[#3] Relative to what?** The header path could be relative to the directory the command runs in
(it matches what was typed) or to the repository root (it stays the same wherever resume is started
from). The backlog asks for the second, which makes the header stable. Say if you want the first.

**[#4] Exit code for `--help`.** Neither the README nor the spec defines `--help`. The backlog
assumes the usual convention: output on stdout, exit 0. Exit 2 stays for bad usage.

## Built differently than the report assumes

**[#1] Numbering by place in the file is the documented design, not a parser bug.**
ADR-69 numbers each item "by its place among all items, done or not"
(`docs/task-loop-driver/spec.html:3016`). ADR-78 keeps that for groups (`spec.html:3381`), and
`internal/plan/backlog.go:51` implements it. What actually misleads is the display:
- the triage table prints that place as `#4` (`internal/core/triage.go:713`), the same notation as
  the sender's own `[#6]` tag but with a different meaning;
- a group's title replaces the members' tags with `(items 4, 5)` (`triage.go:116`).

So the fix shows the tag and keeps the ID. Using the tag as the ID is not viable:
- `[#1/2]` is not a valid phase ID;
- `issues/issues-driver-run-2026-09-23.md` uses both `[#1]` and `[#1/2]`;
- IDs would change scheme from one file to the next.

**[#2] The rejection is not saved and picked up later. It is turned into a halt on purpose.**
`Watch.accept` rewrites every rejected watchdog signal, a `warn` included, as
`SignalHalt "watchdog signal rejected: …"` (`internal/core/watch.go:340-345`). It aims the halt at
the step that is held or live. During the remedy window after the codex reviewer failed, that was
phase-1/plan. `haltsWindow` then overwrote the step's real failure reason with
`"watchdog: "+reason` (`internal/core/loop.go:509`). The run exited 5 and no restart or fallback
happened. This behaviour is written down ("a rejected watchdog signal halts the run", tech-design
`561-564`, todo `405-406`) and pinned by tests at `internal/core/watch_test.go:160, 318, 376`.

**[#5] The nesting is codex inside codex, not only the Claude Code sandbox.**
- **How the review command runs.** r-loop never starts `codex exec review` itself. The shipped
  codex block hands that command to the reviewer agent (`internal/providers/shipped/codex.yaml:8`).
  The reviewer, an interactive codex session, runs it with its own shell tool
  (`internal/prompts/templates/review.md:19-23`). So the second codex starts inside the first
  codex's workspace-write sandbox.
- **Conflicting evidence.** The same run passed a codex review once r-loop was started outside the
  Claude Code sandbox. So the cause is not settled, and the first criterion asks for one recorded
  reproduction before choosing a fix.
- **Not the same bug as fixed `[#6]`** in `issues-driver-run`. That one was `bind` refused for local
  listeners, fixed with `sandbox_workspace_write.network_access=true`.
- **Related open items.** It overlaps `[#4/2]` in `issues-driver-run` (interactive flags leaking into
  `{args}` of `codex exec review`) and `[#28]` in `issues-reliability-review` (no preflight check
  for provider binaries). Design them together.
- **Already predicted.** A reviewer in run `20260923-171431` raised this and it was dismissed as out
  of scope.

## Moves architecture or the estimate

**[#2] Changes a documented contract.** The fix separates "a `warn` for a step that already ended"
(harmless: record it, halt nothing) from "a malformed or unknown step" (still a protocol halt). It
rewrites three or four existing tests and the tech-design and todo lines above. It is still going to
be built. Today a harmless late warning turns a failure that could be restarted into exit 5, and it
hides the real cause.

**[#5] May change the reviewer contract.** The fix is one of two things:
- loosen codex's sandbox for reviewer sessions only, which weakens isolation;
- have the driver run the native review command outside the agent and hand the reviewer the output,
  which reverses how the review command runs today.

Either one is a design decision, not a patch.
