# Plan phase {{.PhaseNumber}} — {{.PhaseTitle}}

You are planning one phase of the implementation plan at `{{.TodoPath}}` (spec and design docs in `{{.SpecDir}}`). Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). Write the phase plan to `{{.PlanPath}}` and touch no other file.

The plan is the implementer's whole brief. Make it decision-complete: someone who reads only the plan and the phase block builds the phase without making a single choice of their own.

The phase:

{{.PhaseBlock}}

A `Resolved first:` list under the phase records decisions the maintainer has already taken: follow each `Resolved:` line and never ask about it again.

Its open items:

{{.Criteria}}
{{- if .GroupItems}}

This phase fixes backlog items {{.GroupItems}} with one change. Every member's criteria are obligations, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done.
{{- end}}
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

Start with the obligations, before any design: every open item; every spec invariant or ADR the phase's code touches; and every edge case and error path that can reach this code, each with the concrete input and where it comes from — a caller at `path:line`, user input, a file on disk, another process. A case with no source you can name is not an obligation. This list is the floor: the plan meets every entry.

Then choose the smallest design that meets the whole list. Smallest means fewest new concepts — types, interfaces, layers, config keys, dependencies — not fewest lines: a guard clause or one more test is cheap, a new abstraction is not.

List the real choices the phase leaves open: where a piece lives, which existing abstraction it extends, the shape of a new type or signature, how an edge case or error behaves. For each, name at least two options, weigh them against the obligations, the patterns you found and the spec, and pick one. A choice with only one reasonable answer is not a choice: take it and move on.

Every element the design adds names the obligation that needs it, and one without is cut: an interface with one implementation, a config key or option nobody asked for, a helper or generic type for a single call site, a wrapper that only forwards, handling for a state the types or an invariant already rule out, a hook for a later phase. Existing code that already does the job is called, not re-created. Two similar blocks are fine; extract a shared one when a third appears or when they must change together.

Cutting never touches the floor: never drop an obligation, merge distinct errors or states into one, swallow an error, skip validation where input crosses a trust boundary, or leave an error path untested to make the plan shorter.

## 3. Decide

A fact the repository can answer is looked up, never asked. A choice the repository and the spec cannot settle, and whose options would change what gets built, goes to the `ask_watchdog` tool with your recommendation. Write every other default the plan takes under `## Assumptions`.

## 4. Write

`{{.PlanPath}}` starts with the line `status: planned`, followed by exactly these sections:

- `## Summary` — what the phase builds and the approach, in a few sentences; for each choice from step 2, the option taken and in one line why it beat the other.
- `## Changes` — per file, in build order: create or modify, the types, functions and signatures that change, the existing code (`path:line`) it reuses or follows, and the obligations each change serves.
- `## Tests` — the tests to write first, each by name with the behaviour it pins and the obligations it covers. Every obligation from step 2 is covered, each edge case and error path included.
- `## Left out` — each element you considered and cut, with one line on why no obligation needs it, or `none`.
- `## Assumptions` — each default taken, or `none`.
{{- if .ItemGate}}
- `## Gate` — one shell command in backticks that runs only the tests named in `## Tests`, from the repository root. The driver runs it at land: it must fail on the base code with only the new tests added, and pass once the phase is merged.
{{- end}}

{{- if .ItemGate}}

When the code already does what every open item asks, or the item is not code work — a question, or a premise the code contradicts — write instead a plan that starts with `status: already-done` or `status: not-work`, followed only by `## Evidence`: one line per open item with the `path:line` that shows it. The driver then skips the item and reports it without ticking it.
{{- end}}

## 5. Self-check

Re-read the plan as the implementer would, then fix it until all of this holds: every obligation has a change in `## Changes` and a test in `## Tests`; every change serves an obligation; only the phase's files are named; every `path:line` cited exists; no line leaves a choice open — no "consider", "if needed", "TBD", "or" between options, or deferred decision.{{if .ItemGate}} `## Gate` runs exactly those tests and nothing else.{{end}}
{{template "sentinel" .}}
