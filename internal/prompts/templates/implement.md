# Implement phase {{.PhaseNumber}} — {{.PhaseTitle}}

Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). The phase:

{{.PhaseBlock}}

A `Resolved first:` list under the phase records decisions the maintainer has already taken: follow each `Resolved:` line and never ask about it again.
{{- if .GroupItems}}

This phase fixes backlog items {{.GroupItems}} with one change. Every member's outcome is an obligation, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done.
{{- end}}

1. Read the plan at `{{.PlanPath}}` first. It is the contract for this step. Where its `## Why this approach` replaces a means the phase block proposes, follow the plan.
2. Write the tests from its `## Tests` section before any production code, and see them fail for the right reason. Write them by the rules under `## Writing tests` below.
3. Implement until those tests pass and the build is green.{{if .ItemGate}} The command under the plan's `## Gate` must pass.{{end}}
4. Never edit `{{.TodoPath}}` or `{{.PlanPath}}`.

When the plan is wrong — it names a file that cannot work, a test that cannot pass, or contradicts the code — do not deviate from it: write a `failed` sentinel whose reason names what is wrong with the plan.
{{template "tests" .}}
{{template "commit" .}}
{{template "sentinel" .}}
