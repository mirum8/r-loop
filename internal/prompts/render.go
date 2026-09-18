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
	"plan": true, "implement": true, "review": true, "fix": true,
	"milestone": true, "watchdog": true, "gatefix": true,
}

const sentinel = `{{define "sentinel"}}
## Reporting the outcome

As your last action, write ` + "`" + `{"outcome":"ok","reason":"","at":"<RFC3339>"}` + "`" + ` to ` + "`" + `{{.Sentinel}}` + "`" + `, with ` + "`" + `at` + "`" + ` set to the current time. When the work cannot be done, write ` + "`" + `{"outcome":"failed","reason":"<why>","at":"<RFC3339>"}` + "`" + ` there instead, naming the cause. The driver reads only that file: never report completion only in the terminal. Never commit — the driver commits the step's work once its review is done.
{{- if .AskURL}}

When you need a decision you cannot take from the repository, use the ` + "`" + `ask_user` + "`" + ` tool instead of guessing.
{{- end}}
{{- if .Addendum}}

## Note from the previous attempt:

{{.Addendum}}
{{- end}}
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
