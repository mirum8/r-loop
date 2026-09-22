# Plan phase {{.PhaseNumber}} — {{.PhaseTitle}}

You are planning one phase of the implementation plan at `{{.TodoPath}}` (spec and design docs in `{{.SpecDir}}`). Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). Write the phase plan to `{{.PlanPath}}` and touch no other file.

The plan is the implementer's whole brief. Make it decision-complete: someone who reads only the plan and the phase block builds the phase without making a single choice of their own.

The phase:

{{.PhaseBlock}}

A `Resolved first:` list under the phase records decisions the maintainer has already taken: follow each `Resolved:` line and never ask about it again.

Its open items:

{{.Criteria}}
{{- if .PhaseWarnings}}

## The watchdog's phase check warned:

{{.PhaseWarnings}}

Address each of these warnings in the plan: resolve it, or say under `## Assumptions` why it does not apply.
{{- end}}

## 1. Explore

Map the code before deciding anything. Cover three areas:

- **Target** — every file the `Files:` line names, and the code that calls it or that it calls.
- **Patterns** — the closest existing code that does something similar: the types, helpers, error handling and naming this phase should reuse rather than re-create.
- **Tests** — the nearest tests, their fixtures and fakes, and how the package runs them.

If you can run sub-agents, give each area to its own read-only explorer, in parallel, and have each return findings as `path:line` with one line of why, not file dumps. Otherwise work through the three yourself. Either way, then read every file the plan will change yourself. Never assume something is missing without searching for it first.

## 2. Design

List the real choices the phase leaves open: where a piece lives, which existing abstraction it extends, the shape of a new type or signature, how an edge case or error behaves. For each, name at least two options, weigh them against the patterns you found and the spec, and pick one. A choice with only one reasonable answer is not a choice: take it and move on.

## 3. Decide

A fact the repository can answer is looked up, never asked. A choice the repository and the spec cannot settle, and whose options would change what gets built, goes to the `ask_watchdog` tool with your recommendation. Write every other default the plan takes under `## Assumptions`.

## 4. Write

`{{.PlanPath}}` starts with the line `status: planned`, followed by exactly these sections:

- `## Summary` — what the phase builds and the approach, in a few sentences; for each choice from step 2, the option taken and in one line why it beat the other.
- `## Changes` — per file, in build order: create or modify, the types, functions and signatures that change, and the existing code (`path:line`) it reuses or follows.
- `## Tests` — the tests to write first, each by name with the behaviour it pins and the open items it covers, including the edge cases and error paths from step 2. Every open item above is covered.
- `## Assumptions` — each default taken, or `none`.
{{- if .ItemGate}}
- `## Gate` — one shell command in backticks that runs only the tests named in `## Tests`, from the repository root. The driver runs it at land: it must fail on the base code with only the new tests added, and pass once the phase is merged.
{{- end}}

{{- if .ItemGate}}

When the code already does what every open item asks, or the item is not code work — a question, or a premise the code contradicts — write instead a plan that starts with `status: already-done` or `status: not-work`, followed only by `## Evidence`: one line per open item with the `path:line` that shows it. The driver then skips the item and reports it without ticking it.
{{- end}}

## 5. Self-check

Re-read the plan as the implementer would, then fix it until all of this holds: every open item has a test in `## Tests`; only the phase's files are named; every `path:line` cited exists; no line leaves a choice open — no "consider", "if needed", "TBD", "or" between options, or deferred decision.{{if .ItemGate}} `## Gate` runs exactly those tests and nothing else.{{end}}
{{template "sentinel" .}}
