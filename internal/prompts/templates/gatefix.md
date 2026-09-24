# Fix the acceptance gate — phase {{.PhaseNumber}} {{.PhaseTitle}}

The phase's own acceptance command failed on the merged tree:

```
{{.GateCommand}}
```

Its output:

```
{{.GateOutput}}
```

The phase:

{{.PhaseBlock}}

Make that command pass in `{{.Worktree}}`. Do not weaken, skip or delete any test, and do not edit `{{.TodoPath}}`.

When the fix lies outside this phase, write a `failed` sentinel whose reason names the cause.
{{template "outcome" .}}
