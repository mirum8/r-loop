# Implement phase {{.PhaseNumber}} — {{.PhaseTitle}}

Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). The phase:

{{.PhaseBlock}}

A `Resolved first:` list under the phase records decisions the maintainer has already taken: follow each `Resolved:` line and never ask about it again.
{{- if .GroupItems}}

This phase fixes backlog items {{.GroupItems}} with one change. Every member's criteria are obligations, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done.
{{- end}}

1. Read the plan at `{{.PlanPath}}` first. It is the contract for this step.
2. Write the tests from its `## Tests` section before any production code, and see them fail for the right reason.
3. Implement until those tests pass and the build is green.{{if .ItemGate}} The command under the plan's `## Gate` must pass.{{end}}
4. Never edit `{{.TodoPath}}` or `{{.PlanPath}}`.

When the plan is wrong — it names a file that cannot work, a test that cannot pass, or contradicts the code — do not deviate from it: write a `failed` sentinel whose reason names what is wrong with the plan.
{{template "sentinel" .}}
