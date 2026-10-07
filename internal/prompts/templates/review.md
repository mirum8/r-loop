# Review round {{.Round}} of {{.Rounds}} — phase {{.PhaseNumber}} {{.ReviewedKind}}

You are a reviewer. Review, report-only, what the `{{.ReviewedKind}}` step produced:
the uncommitted changes in {{.Worktree}}.
The phase:

{{.PhaseBlock}}
{{- if .GroupItems}}

This phase fixes backlog items {{.GroupItems}} with one change. Every member's outcome is an obligation, `## Gate` runs the tests of every member, and `status: already-done` holds only when every member is done.
{{- end}}

The block's items state outcomes; a means they prescribe is a proposal. A change that reaches an item's outcome another way is not a finding for that reason alone: judge whether it delivers the outcome. Drop a native-review finding that only asks for the means an item proposed.

## Native review
{{if .ReviewRan}}
The driver already ran the native review `{{.ReviewCommand}}`. Its output is in `{{.ArtifactsDir}}/native-review.txt`: read it and base your findings on it, and do not change that file.
{{- else}}
Your first action is to run this command, exactly as written:

    {{.ReviewCommand}}

A command that starts with `/` is a slash command: invoke it as the slash command or skill of that name. Anything else is a shell command: run it in `{{.Worktree}}` with your shell tool. Its raw output must end up in `{{.ArtifactsDir}}/native-review.txt`: inside the folder `{{.ArtifactsDir}}/`, not beside the `.sentinel` file of nearly the same name. A command that writes that file itself leaves it as written; otherwise save the command's full report there verbatim. A slash command that forks into the background reports through a task notification: wait for it and save the text of its `<result>` verbatim; never read its `<output-file>`, which is a transcript, not the report. `(none)` is a valid result: save it as is, and report no findings from it. When the command cannot run — it is not found, is not available in this session, is refused a permission or network access, or exits with an error — never review by hand instead: write a failed sentinel whose reason names the command and the error. The driver fails a reviewer whose `{{.ArtifactsDir}}/native-review.txt` is missing or empty; a `native-review.txt` anywhere else does not count. Base your findings on that output.
{{- end}}
{{- if .ItemGate}}

Its open criteria:

{{.Criteria}}

For each criterion, name the test that proves it, and report every criterion no test proves as a finding. A criterion that prescribes a means — a type, a signature, a mechanism, a file — is proved by a test of the outcome it serves, not of the means. The spec's invariants and ADRs outrank an item's proposed means: never report a change or a plan for avoiding a means they rule out.
{{- end}}
{{- if .PriorFindings}}

## Earlier rounds

Review all of it again, and name what changed since tree `{{.RoundTree}}` (the delta since the previous round). The earlier findings files:

{{.PriorFindings}}

and the earlier verdict files:

{{.PriorVerdicts}}

A finding dismissed with evidence is not raised again without new evidence.
{{- end}}

Report each new or changed test that breaks a rule under `## Writing tests` below as a finding, naming the rule.
{{template "tests" .}}
## Findings

Convert what you report into `{{.FindingsPath}}` as:

```
{"reviewer":"<name>","findings":[{"id":"<name>-r<round>-<n>","title":"…","detail":"…","files":["…"]}]}
```

where `<name>` is your reviewer name — the `<name>` in the findings file name `<kind>-findings-<name>-r<round>.json` — `<round>` is {{.Round}} and `<n>` is numbered from 1. Every id is unique within the file and starts with `<name>-r{{.Round}}-`: the step agent answers each id exactly once in its verdict file, and the driver checks the two against each other. `title`, `detail` and `files` are required on every finding. With nothing to report, write an empty `findings` list.

Change no file in `{{.Worktree}}`. Before you write the sentinel, `{{.ArtifactsDir}}/native-review.txt` must hold the native review's output.
{{if or (eq .ReviewedKind "gatefix") (eq .ReviewedKind "gate") (eq .ReviewedKind "milestone")}}{{template "outcome" .}}{{else}}{{template "sentinel" .}}{{end}}
