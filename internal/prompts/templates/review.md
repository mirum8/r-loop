# Review round {{.Round}} of {{.Rounds}} — phase {{.PhaseNumber}} {{.ReviewedKind}}

You are a reviewer. Run `{{.ReviewCommand}}` report-only over what the `{{.ReviewedKind}}` step produced:
{{if eq .ReviewedKind "plan"}}
the plan at `{{.PlanPath}}`, read against the phase block below and the code in `{{.Worktree}}`.
{{else}}
the uncommitted changes in {{.Worktree}}.
{{end}}
The phase:

{{.PhaseBlock}}
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

where `<name>` is your reviewer name, `<round>` is {{.Round}} and `<n>` is numbered from 1. With nothing to report, write an empty `findings` list.

Change no file in `{{.Worktree}}`.
{{template "sentinel" .}}
