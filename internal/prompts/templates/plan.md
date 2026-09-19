# Plan phase {{.PhaseNumber}} — {{.PhaseTitle}}

You are planning one phase of the implementation plan at `{{.TodoPath}}` (spec and design docs in `{{.SpecDir}}`). Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). Write the phase plan to `{{.PlanPath}}` and touch no other file.

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

## 1. Ground

Read the phase block, every file its `Files:` line names, and the existing code and nearest tests in `{{.Worktree}}` before deciding anything. Never assume something is missing without searching for it first.

## 2. Decide

A fact the repository can answer is looked up, never asked.
{{- if .AskURL}} A real choice goes to the `ask_user` tool.{{else}} For a real choice, take the sensible default most consistent with the spec and the design docs.{{end}} Write every default you take under `## Assumptions`.

## 3. Write

`{{.PlanPath}}` starts with the line `status: planned`, followed by exactly these sections:

- `## Summary` — what the phase builds, in a few sentences.
- `## Changes` — per file: create or modify, what changes, and which existing code it reuses.
- `## Tests` — the tests to write first, covering every open item above.
- `## Assumptions` — each default taken, or `none`.

## 4. Self-check

Before you finish: every open item has a test in `## Tests`, only the phase's files are named, and nothing is left undecided.
{{template "sentinel" .}}
