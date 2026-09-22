# Verify and fix review findings — phase {{.PhaseNumber}}, round {{.Round}} of {{.Rounds}}

Reviewers reported findings on your work in `{{.Worktree}}`. The findings files, one per reviewer:
{{range .FindingsFiles}}
- {{.Reviewer}}: `{{.Path}}`
{{- end}}

Check every finding against the code and give it:

- a verdict: `real`, `not-real` or `out-of-scope`;
- a severity: `P1`, `P2`, `P3` or `P4`.

A finding that an obligation — an open item, a spec invariant, a reachable edge case or error path — is missing or untested is `P1` or `P2`. Excess that adds a type, interface, config key, dependency or layer no obligation needs is `P2`; excess inside one function is `P3`. An excess finding is `real` only when its replacement still meets every obligation; otherwise it is `not-real`, with the `path:line` of the obligation it would break as evidence.

A `not-real` verdict is a dismissal and carries `evidence`: a `path:line` you have read in the worktree that shows the finding is wrong, with the path relative to `{{.Worktree}}`.

Only a finding that is `real` at `P1` or `P2` may be fixed, and every such finding must be fixed: mark it `"fixed": true` and list in `files` every file you changed for it in this round. Every other finding stays `"fixed": false` and you leave its code as it is.

Write `{{.VerdictPath}}` as:

```
{"findings":[{"id":"<id>","reviewer":"<name>","title":"…","verdict":"real|not-real|out-of-scope","severity":"P1|P2|P3|P4","fixed":true|false,"files":["…"],"evidence":"<path:line>"}]}
```

with one entry per finding, answering every finding id across every findings file exactly once, the `id`, `reviewer` and `title` copied from the findings file. The driver checks this file against the findings and the worktree before it accepts the round.

Do not commit: the driver commits the step's work once the review is done.
{{template "sentinel" .}}
