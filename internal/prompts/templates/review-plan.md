# Review round {{.Round}} of {{.Rounds}} — phase {{.PhaseNumber}} {{.ReviewedKind}}

You are a reviewer. Review, report-only, what the `{{.ReviewedKind}}` step produced:
the plan at `{{.PlanPath}}`, read against the phase block below and the code in `{{.Worktree}}`.
The phase:

{{.PhaseBlock}}
{{- if .GroupItems}}

This phase fixes backlog items {{.GroupItems}} with one change. Every member's criteria are obligations, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done.
{{- end}}

Judge the plan's proportion in both directions, and report each gap as a finding:

- **Missing** — an open item, a spec invariant, or a reachable edge case or error path the plan does not handle or test. Name the input and where it comes from.
- **Excess** — an element no obligation needs: an interface with one implementation, a config key nobody asked for, a helper or generic type for a single call site, a wrapper that only forwards, handling for a state the types already rule out, a hook for a later phase, code that re-creates what the repository already has. Name the element, say why nothing needs it, and give the simpler replacement that still meets every obligation.

A simplification that would drop an obligation is not a finding. Taste is not a finding: report excess only when it adds a concept, not when it is written differently than you would. A cut listed under `## Left out` is challenged only with the obligation it breaks.
{{- if .ItemGate}}

Its open criteria:

{{.Criteria}}

For each criterion, name the test that proves it in the plan's `## Tests`, and report every criterion no test proves as a finding. When the plan says `status: already-done` or `status: not-work`, open each `path:line` under `## Evidence` instead, and report every criterion the cited code does not show.
{{- end}}
{{- if .PriorFindings}}

## Earlier rounds

Review all of it again, and name what changed since tree `{{.RoundTree}}` (the delta since the previous round). The earlier findings files:

{{.PriorFindings}}

and the earlier verdict files:

{{.PriorVerdicts}}

A finding dismissed with evidence is not raised again without new evidence.
{{- end}}

## Findings

Convert what you report into `{{.FindingsPath}}` as:

```
{"reviewer":"<name>","findings":[{"id":"<name>-r<round>-<n>","title":"…","detail":"…","files":["…"]}]}
```

where `<name>` is your reviewer name — the `<name>` in the findings file name `<kind>-findings-<name>-r<round>.json` — `<round>` is {{.Round}} and `<n>` is numbered from 1. Every id is unique within the file and starts with `<name>-r{{.Round}}-`: the step agent answers each id exactly once in its verdict file, and the driver checks the two against each other. `title`, `detail` and `files` are required on every finding. With nothing to report, write an empty `findings` list.

Change no file in `{{.Worktree}}`.
{{template "sentinel" .}}
