package prompts

import (
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/*.md
var embedded embed.FS

var names = map[string]bool{
	"plan": true, "implement": true, "review": true, "review-plan": true, "review-ui": true, "fix": true,
	"milestone": true, "watchdog": true, "gatefix": true, "gate": true, "intake": true,
}

const sentinel = `{{define "outcome"}}
## Reporting the outcome

As your last action, write ` + "`" + `{"outcome":"ok","reason":""}` + "`" + ` to ` + "`" + `{{.Sentinel}}` + "`" + `. When the work cannot be done, write ` + "`" + `{"outcome":"failed","reason":"<why>"}` + "`" + ` there instead, naming the cause. The driver reads only that file: never report completion only in the terminal. Never commit — the driver commits the step's work once its review is done.

Write the sentinel atomically: put the JSON in a temporary file in the same directory, then rename that file onto ` + "`" + `{{.Sentinel}}` + "`" + `. The driver may read the sentinel at any moment, and must never see it half-written.
{{- if .Addendum}}

## Note from the previous attempt:

{{.Addendum}}
{{- end}}
{{end}}
{{define "sentinel"}}
## Reporting the outcome

As your last action, write ` + "`" + `{"outcome":"ok","reason":""}` + "`" + ` to ` + "`" + `{{.Sentinel}}` + "`" + `. When the work cannot be done, write ` + "`" + `{"outcome":"failed","reason":"<why>"}` + "`" + ` there instead, naming the cause. The driver reads only that file: never report completion only in the terminal. Never commit — the driver commits the step's work once its review is done.

Write the sentinel atomically: put the JSON in a temporary file in the same directory, then rename that file onto ` + "`" + `{{.Sentinel}}` + "`" + `. The driver may read the sentinel at any moment, and must never see it half-written.

When you need a decision you cannot take from the repository, call the ` + "`" + `ask_watchdog` + "`" + ` tool with the options and the one you recommend, instead of guessing. It returns at once: end your turn then and do nothing else until the answer arrives as your next message, starting ` + "`" + `r-loop: answer to` + "`" + `. Ask one question at a time. Never ask the user in this pane.
{{- if .Addendum}}

## Note from the previous attempt:

{{.Addendum}}
{{- end}}
{{end}}
{{define "tests"}}
## Writing tests

Use the project's existing test framework, assertion helpers and fixtures; add no test dependency.

- Test behaviour through the real code path. Fake or mock only true I/O boundaries — third-party services, model calls, the clock, the network — never the project's own collaborators. A fake standing in for something that writes state writes that state, so the test asserts on it.
- Each test has given, when and then blocks separated by blank lines, marked ` + "`" + `// given` + "`" + `, ` + "`" + `// when` + "`" + `, ` + "`" + `// then` + "`" + ` in the language's comment syntax.
- One test, one behaviour, named for that behaviour. Add a test rather than extend an existing one; group related tests the way the language does (subtests, nested classes) and use a table or parameterised test for variations of one behaviour.
- Assert only what the test is named for, against hard-coded expected values; never compute the expected value with production code. Name the compared values ` + "`" + `actual…` + "`" + ` and ` + "`" + `expected…` + "`" + `.
- Fixed data only: no random IDs, current time or random amounts.
- Keep a test's data in the test: no shared setup hook for it. Descriptively named helpers that take the values the test depends on as parameters, with defaults for the rest, keep each block to a few lines.
- Inline a value used once or twice rather than name it.
- No loops or conditionals in a test body; use the assertion helpers.
- Never sleep a fixed time to wait: poll for the condition with a deadline.
- A test that checks a call was made also checks what it was called with and the outcome. A test that checks nothing happened checks every side effect that could have.
- No tests for trivial accessors, pure delegation, or framework and library internals; no reflection into private fields.
- Write no test beyond what the obligations need.
{{end}}`

type Renderer struct {
	projectDir string
}

func New(projectDir string) *Renderer {
	return &Renderer{projectDir: projectDir}
}

func (r *Renderer) Render(name string, vars map[string]any) (string, string, error) {
	if !names[name] {
		return "", "", fmt.Errorf("unknown prompt %q", name)
	}
	body, source, err := r.load(name)
	if err != nil {
		return "", "", err
	}
	tmpl, err := template.New(name).Option("missingkey=error").Parse(sentinel)
	if err == nil {
		tmpl, err = tmpl.Parse(body)
	}
	if err != nil {
		return "", "", fmt.Errorf("parse prompt %s (%s): %w", name, source, err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, vars); err != nil {
		return "", "", fmt.Errorf("render prompt %s (%s): %w", name, source, err)
	}
	return out.String(), source, nil
}

func (r *Renderer) load(name string) (string, string, error) {
	path := filepath.Join(r.projectDir, ".r-loop", "prompts", name+".md")
	body, err := os.ReadFile(path)
	if err == nil {
		return string(body), path, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("read prompt override: %w", err)
	}
	body, err = embedded.ReadFile("templates/" + name + ".md")
	if err != nil {
		return "", "", err
	}
	return string(body), "embedded", nil
}
