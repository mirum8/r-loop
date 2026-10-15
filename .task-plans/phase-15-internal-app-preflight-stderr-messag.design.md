# Design — preflight and stderr messages (#15, #17, #18)

There is no screen here: three kinds of plain text line r-loop prints before a run starts or instead of
one. The design system (`DESIGN.md`) still governs them: amber (`secondary`) means only "waiting for
you", every line is one line, and the voice is the terse `r-loop: <what>: <how to fix>` the binary
already uses.

## Screens

- **stderr error line** — every `r-loop: …` line r-loop writes when it refuses to run: a bad plan path,
  a plan-check stop, the `status` failures (repo open, load run, plan read), the working-directory
  error, and the usage lines. Reached by running `r-loop <plan>`, `r-loop status`, or bad flags.
- **plan-outside-repo refusal** — a new stderr line, reached by `r-loop /elsewhere/todo.md`, by a
  symlink in the repo that points outside it, and by resuming a run whose recorded plan is outside.
- **amber preflight line** — `open ## Resolve first: … blocks …`, printed on stdout during preflight
  and `--dry-run`, in `secondary`.
- **triage table `Resolve first:` line** — in the dry-run triage table and the gated triage the
  watchdog types (`core.RenderTriage`).

## States

| Fragment | Default | Error | Empty / loading / success / no permission |
|---|---|---|---|
| stderr error line | — (only exists as an error) | the message, control bytes removed, newlines folded to spaces, exit code unchanged | n/a: a refusal has no loading or success form; a permission error from git or the filesystem is just another error line, cleaned the same way |
| plan-outside-repo | — | one line naming the plan and the repo root, exit 2; nothing from git (`fatal:`, `ls-files`) reaches the user | n/a. A plan inside the repo, tracked or gitignored (ADR-84 local todo), shows nothing new |
| amber preflight line | one line per open blocking entry: `blocks phase 3`, `blocks phases 3, 4`, or `blocks every phase` | n/a | no open entry → no line (unchanged) |
| `Resolve first:` line | `→ phase 7` for one, `→ phases 3, 4` for more | n/a | no deferral → `Resolve first: none outstanding` (unchanged) |

## Layout

Each is one line; the eye meets the `r-loop:` prefix, then the subject, then the remedy after a colon
or em dash. Nothing is added above or below, and no line gains a prefix, colour or indentation.

```
stderr, plan-check stop with a hostile title (ESC/OSC/BEL dropped):
r-loop: Phase 3 — x[2Jy]0;pwn: no checklist items

stderr, hostile plan path:
r-loop: open /work/proj/x[2Jy]0;pwn.md: no such file or directory

stderr, plan outside the repo:
r-loop: plan /tmp/notes/todo.md is outside the repository /work/proj: move it into the repository, or run r-loop from the repository that holds it

stderr, symlink in the repo that resolves outside:
r-loop: plan /work/proj/docs/todo.md resolves to /tmp/notes/todo.md, outside the repository /work/proj: move it into the repository, or run r-loop from the repository that holds it

stdout, preflight (amber):
open ## Resolve first: "Pick a DB" (decision) blocks phase 3 — the watchdog will walk it
open ## Resolve first: "Pick a DB" (decision) blocks phases 3, 4 — the watchdog will walk it

triage table:
Resolve first: Pick a DB → phase 7
Resolve first: Pick a DB → phases 3, 4
```

## Reuse map

- `internal/core/triage.go:963` — `Printable`: the control-byte rule this phase applies to stderr
  (newline, CR, tab → space; every other C0/C1 control, ESC and BEL included, dropped). The triage
  table already shows plan text this way, so stderr matches it.
- `internal/app/wire.go:318` — the stderr error path (`r-loop: ` prefix, newlines already folded to
  spaces, exit code from the error). The cleaning belongs on this one path, not per message.
- `internal/app/status.go:27`, `:32`, `:50`, `:55`; `cmd/r-loop/main.go:42`; `internal/app/wire.go:248` —
  the lines that print to stderr outside that path today and must come out just as clean.
- `internal/core/plancheck.go:31` — the `Phase <id> — <title>: no checklist items` stop wording, kept.
- `internal/app/wire.go:769` — `… is inside the repository %s: set TMPDIR outside it`: the house shape
  for "path vs repository" refusals; the outside-repo line mirrors it (`repository`, colon, remedy).
- `internal/core/triage.go:1003` — `Plural`: the existing singular/plural rule (1 → singular).
- `internal/app/preflight.go:99` — `amber`, `secondary` with the `NO_COLOR` fallback; unchanged.
- `internal/app/preflight.go:122`, `internal/core/triage.go:820` — the two lines whose wording changes.
- `docs/task-loop-driver/tech-design.md:697` and `:702` — the banner templates to update.

**Do not invent:**
- a second control-byte filter, or one that renders bytes visibly (`^[`, `\x1b`, `?`);
- a colour, prefix (`error:`, `✗`) or second line for the outside-repo refusal;
- a new pluralising helper beside `Plural`, or `phase(s)`;
- any wording change to `every phase`, `none outstanding`, or the `— <then>` tail.

## Responsive

Terminal lines, not a layout. None of these is wrapped by r-loop: stderr lines are one logical line
and the terminal soft-wraps them below their length; the outside-repo line runs long with two absolute
paths and that is accepted (both paths must stay whole and copyable, never truncated). The triage
`Resolve first:` line keeps its existing 100-column wrap. Height does not apply: nothing scrolls or
redraws.

## Accessibility

- No control byte reaches the terminal from user- or file-supplied text, so a path or title can never
  clear the screen, retitle the window, ring the bell or move the cursor; what is shown is the text
  as typed minus those bytes.
- Every message is plain text that reads the same piped, logged, or read aloud: no colour carries
  meaning on stderr. The amber preflight line keeps its words (`open ## Resolve first`, `blocks`) so
  it still reads correctly under `NO_COLOR`.
- `phase 3` / `phases 3, 4` reads grammatically to a screen reader; `phases 3` did not.

## Copy

New:
- `plan <plan> is outside the repository <root>: move it into the repository, or run r-loop from the repository that holds it`
- `plan <plan> resolves to <target>, outside the repository <root>: move it into the repository, or run r-loop from the repository that holds it`

`<plan>` is the plan's absolute path as the user named it (symlink not followed), `<target>` the
resolved file, `<root>` the repository root. Every placeholder is shown with control bytes removed.
Both print with the `r-loop: ` prefix, exit 2; resume prints the same line for the recorded plan.

Changed:
- `open ## Resolve first: "<name>" (<kind>) blocks phase <n> — <then>` for one phase;
  `… blocks phases <n>, <n> — <then>` for more; `blocks every phase` unchanged.
- `Resolve first: <entry> → phase <id>` for one; `Resolve first: <entry> → phases <id>, <id>` for more.
- `tech-design.md:697` and `:702` state both forms.

Unchanged except for removed control bytes: every other stderr message.

## Decisions

- **Drop control bytes, don't render them.** Showing `^[` or `\x1b` would tell the user what was there,
  but it diverges from the triage table, which already drops them (`Printable`), and a visible escape
  in a path invites copy-paste of a path that doesn't exist. Replacing with `?` was turned down for
  the same reason.
- **Clean on the shared stderr path, not per message.** Per-message cleaning misses the next message
  someone adds; the reviewer should find no stderr line that bypasses it.
- **Absolute paths in the outside-repo line.** The relative path as typed (`../notes/todo.md`) was
  turned down: the point of the message is comparing the plan's location to the root, which only
  reads at a glance when both are absolute.
- **Name the symlink and its target.** Naming only the link reads as a false claim ("it's inside!");
  naming only the target hides which file the user passed.
- **Remedy after a colon, mirroring the TMPDIR refusal**, rather than a second hint line: every other
  refusal is one line, and the house voice puts the fix after `:`.
- **`phase 3` / `phases 3, 4` via the existing plural rule**, not `phase(s) 3`, which reads as a
  template leak.

DESIGN-INTENT: Every refusal is one calm, clean `r-loop:` line that names what is wrong in the user's own terms and how to fix it — no git internals, no terminal-hijacking bytes. Banner counts read as written English: one phase is "phase 3", more are "phases 3, 4".
