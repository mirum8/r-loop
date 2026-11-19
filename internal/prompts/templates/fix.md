# Verify and fix review findings — phase {{.PhaseNumber}}, round {{.Round}} of {{.Rounds}}

Reviewers reported findings on your work in `{{.Worktree}}`. The findings files, one per reviewer:
{{range .FindingsFiles}}
- {{.Reviewer}}: `{{.Path}}`
{{- end}}
{{- range .FindingsFiles}}{{if eq .Reviewer "static"}}

`static`'s findings come from static analyzers the driver ran on the change, not from a model reading it. A rule hit is not a defect by itself: judge each one against the code like any other finding. A false positive is `not-real`, with the `path:line` that shows it as its evidence.
{{- end}}{{end}}

Check every finding against the code and give it:

- a verdict: `real`, `not-real` or `out-of-scope`;
- a severity: `P1`, `P2`, `P3` or `P4`;
- for a `real` finding, a likelihood: `likely`, `possible` or `rare`.

Severity is how bad the failure is when it happens: `P1` loses or corrupts data or state, opens a security hole, or wrongly lands, halts or hangs a run; `P2` is wrong behaviour a user meets; `P3` and `P4` are minor. Likelihood is how often it happens. `likely`: it happens in normal use, on an input or a path a usual run takes. `possible`: it needs a less common input or state the system does meet, such as an empty list, a retry, a resume or a network error. `rare`: it needs misuse, a combination no caller makes, or a caller that does not exist yet. Judge the finding's `scenario` against the code: whether that input or state reaches it on the paths its callers take. A finding that is not about a failure — a missing or untested obligation, an open criterion, a watchdog warning, excess, a test that breaks a rule under `## Writing tests` — is `likely`. A `rare` verdict carries `evidence` like a dismissal: the `path:line` that keeps the case off the paths callers take.

A finding that an obligation — an open item, a spec invariant, an edge case or error path — is missing or untested is `P1` or `P2`. Excess that adds a type, interface, config key, dependency or layer no obligation needs is `P2`; excess inside one function is `P3`. A test that breaks a rule under `## Writing tests` is `P2` when it would still pass with the behaviour it names broken, and `P3` otherwise. An excess finding is `real` only when its replacement still meets every obligation; otherwise it is `not-real`, with the `path:line` of the obligation it would break as evidence.

A `not-real` verdict is a dismissal and carries `evidence`: a `path:line` or `path:line-line` you have read in the worktree that shows the finding is wrong. Your current directory is the worktree root: write the path relative to it, such as `calc_test.go:75`, never with the worktree's own path in front.

Only a finding that is `real` at `P1`, whatever its likelihood, or `real` at `P2` and `{{.MinLikelihood}}` or likelier, in the order `likely`, `possible`, `rare`, may be fixed, and every such finding must be fixed: mark it `"fixed": true` and list in `files` every file you changed for it in this round. Every other finding stays `"fixed": false` and you leave its code as it is. A fix that adds or changes a test follows the rules under `## Writing tests` below.

Write `{{.VerdictPath}}` as:

```
{"findings":[{"id":"<id>","reviewer":"<name>","title":"…","verdict":"real|not-real|out-of-scope","severity":"P1|P2|P3|P4","likelihood":"likely|possible|rare","fixed":true|false,"files":["…"],"evidence":"<path:line>"}]}
```

with one entry per finding, answering every finding id across every findings file exactly once, the `id`, `reviewer` and `title` copied from the findings file. The driver checks this file against the findings and the worktree before it accepts the round.

{{- if .WatchdogWarnings}}
{{template "watchdog-warnings" .}}
Address each warning in this round: fix it, or, when it does not apply, say why in the `reason` of your `ok` sentinel. A warning a reviewer reported as a finding is answered in the verdict file like any other finding.
{{- end}}

Do not commit: the driver commits the step's work once the review is done.
{{template "tests" .}}
{{if not (or (eq .ReviewedKind "gate") (eq .ReviewedKind "milestone"))}}{{template "commit-fix" .}}{{end}}
{{- template "memory" .}}
{{if or (eq .ReviewedKind "gatefix") (eq .ReviewedKind "gate") (eq .ReviewedKind "milestone")}}{{template "outcome" .}}{{else}}{{template "sentinel" .}}{{end}}
