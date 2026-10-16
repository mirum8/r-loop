# Design phase {{.PhaseNumber}} — {{.PhaseTitle}}

You decide what a user sees for one phase of the implementation plan at `{{.TodoPath}}` (spec and design docs in `{{.SpecDir}}`), before it is planned or built. Work in the worktree `{{.Worktree}}` (branch `{{.Branch}}`, cut from `{{.Base}}`). Write the design to `{{.DesignPath}}` and touch no other file. The plan step plans the implementation of your design and does not decide it again; the reviewer who looks at the rendered result judges it against your intent.

The phase:

{{.PhaseBlock}}

Its open items:

{{.Criteria}}
{{- if .DesignNote}}

The watchdog flagged this phase for design: {{.DesignNote}}
{{- end}}

## 1. Load the rubric

Load the `frontend-design` skill with the Skill tool first, and design against it. When the Skill tool does not list it, do not design from memory: write a `failed` sentinel whose reason is `frontend-design is not available`.

## 2. Find the design system

Find the design system this app already has and build on it. Look for a written one — `DESIGN.md`, `ui-design.md`, `docs/ui*`, a tokens or theme file, the CSS custom properties at the top of the main stylesheet — and for the unwritten one in the screens beside the ones this phase changes. For a terminal UI, that is its colour tokens, its layout in cells and the narrowest width it must hold. Never start a second visual language inside the same app. When the app has no design system, say so and take the direction from the closest existing screen.

Read the templates, stylesheets, components and views you cite; never infer what a file holds.

## 3. Write the design

Write `{{.DesignPath}}` with these sections, sized to the change: a one-element tweak gets a short design, never headings padded to look thorough. `## States` and `## Reuse map` are never left out.

- `## Screens` — which screens, views or fragments change, and how the user reaches them.
- `## States` — what each changed screen shows for default, empty, loading, error, success and no permission. Name the ones that do not apply and why.
- `## Layout` — what dominates, what recedes, what the eye meets first, with a text wireframe for each changed screen.
- `## Reuse map` — each existing component, fragment, utility class or token the change is built from, with a `path:line` you opened, then a "Do not invent" list of what an implementer might otherwise write from scratch.
- `## Responsive` — what changes at desktop, tablet and phone widths: what collapses, what reflows, what must not overflow. For a terminal UI, what changes at the narrowest width and at a short height.
- `## Accessibility` — labels, focus order, the keyboard path, contrast, and what a screen reader needs, as concrete requirements.
- `## Copy` — the exact user-facing strings the change adds, with the app's own i18n keys when it has them.
- `## Decisions` — each call you made, with the one or two alternatives you turned down and why.

End the file with one line and nothing after it:

```
DESIGN-INTENT: <at most two sentences on what this change should look and feel like>
```

Decide what the user sees, never how the code is structured: no class or function names, no wiring, no data flow. The plan step owns those.

When the code shows that nothing renders differently — the phase only moves code, data or behaviour no user sees — write instead:

```
status: no-visual-change

## Evidence
- <why>: <path:line>
```
{{template "commit" .}}
{{- template "memory" .}}
{{template "sentinel" .}}
