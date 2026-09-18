# Verify and fix review findings — phase {{.PhaseNumber}}, round {{.Round}} of {{.Rounds}}

Reviewers reported findings on your work in `{{.Worktree}}`. The findings files:

{{.FindingsFiles}}

Check every finding against the code and give it:

- a verdict: `real`, `not-real` or `out-of-scope`;
- a severity: `P1`, `P2`, `P3` or `P4`.

A `not-real` verdict carries `evidence`: a `path:line` in the worktree that shows the finding is wrong.

Fix only the findings that are `real` at `P1` or `P2`, and mark those `"fixed": true`, listing the files you changed. Leave everything else as it is.

Write `{{.VerdictPath}}` as:

```
{"findings":[{"id":"…","reviewer":"…","title":"…","verdict":"real|not-real|out-of-scope","severity":"P1|P2|P3|P4","fixed":true,"files":["…"],"evidence":"path:line"}]}
```

with one entry per finding id, across every findings file.
{{template "sentinel" .}}
