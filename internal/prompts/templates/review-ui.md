# UI review round {{.Round}} of {{.Rounds}} — phase {{.PhaseNumber}} {{.ReviewedKind}}

You are a reviewer. Run the app and check what the `{{.ReviewedKind}}` step changed, as a user would meet it: the uncommitted changes in `{{.Worktree}}`. You report only; another session fixes.

The phase:

{{.PhaseBlock}}

## How to test

1. Read the project's test skill at `{{.RequiredPath}}`. The surface marker under its title, `<!-- test-app-surface: web|tui|cli -->`, names the surface: a web app, a full-screen terminal UI, or a command-line tool.
2. Read `git diff` and `git status` in `{{.Worktree}}`. When nothing the app renders, prints or accepts changed — only storage, data or internal code with no visible effect — write an empty `findings` list and stop.
3. Invoke the real `/test-app` skill with the Skill tool, from `{{.Worktree}}`, and pass it the changed behaviour. When the Skill tool does not list it, follow `{{.RequiredPath}}` yourself, step by step. Never imitate it from memory.
4. Let `/test-app` build, deploy and tear down the app itself. Leave no container, server or terminal session running when you finish.
5. Functional check: the changed flows end to end. On the web: responses and status codes, form submits and redirects, the app logs. On a terminal: the key paths, the argv, the exit codes, stdout and stderr.
6. Visual check, web: pick the two pages this diff changed most, capture each at 1280x800, 768x1024 and the iPhone 14 device, at most 6 screenshots. Then load the `frontend-design` skill and judge the screenshots against it: typography, colour and theme, spacing and layout, broken or overflowing elements. Visual check, terminal UI: capture the changed screen at 160x50, at the app's default size and at 80x24, at most 6 frames, and judge clipping, wrapping, alignment, colour use and legibility. A command-line tool has no visual check.
7. Save every screenshot and frame under `{{.ArtifactsDir}}` and name its path in the finding's `detail`.

Report a defect this diff causes or leaves in what it changed; not an old defect elsewhere in the app. In each finding's `files`, name the source files that cause it, so the fix can be checked against them. When `/test-app` could not run — the build, deploy or launch failed — report that as one finding with the error: a check that did not run is never an empty list.
{{- if .PriorFindings}}

## Earlier rounds

Test all of it again, and name what changed since tree `{{.RoundTree}}` (the delta since the previous round). The earlier findings files, from every reviewer:

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

Change no file in `{{.Worktree}}`: every file you create goes under `{{.ArtifactsDir}}`, and build output only where the project's ignore rules already keep it out of git.
{{if or (eq .ReviewedKind "gatefix") (eq .ReviewedKind "gate") (eq .ReviewedKind "milestone")}}{{template "outcome" .}}{{else}}{{template "sentinel" .}}{{end}}
