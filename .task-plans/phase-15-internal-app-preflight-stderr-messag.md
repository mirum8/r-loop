status: planned

## Summary

The phase makes three kinds of pre-run text match the design in
`.task-plans/phase-15-internal-app-preflight-stderr-messag.design.md`:

1. **#15, stderr control bytes.** One exported stderr writer, `app.Stderrf`, runs every line through the existing
   `core.Printable` (`internal/core/triage.go:963`). Every stderr write in `internal/app` and `cmd/r-loop/main.go`
   goes through it, and `fail` is a call to it.
2. **#17, plan outside the repo.** One helper in `internal/app`, `planRel(root, todo)`, resolves both paths, takes
   `filepath.Rel`, and refuses `..`-prefixed results with exit 2 and the design's one-line message. It replaces the
   three copies of the EvalSymlinks+Rel code: `todoRel`, `commitHint` and `reconcileLand`. `Wire` calls it before it
   reads the plan, so `repo.Tracked` (git `ls-files`) never sees a `../` path.
3. **#18, banner plurals.** One core helper, `core.NamePhases(ids)`, returns `phase 3` or `phases 3, 4`. It takes the
   singular/plural rule from the existing `core.Plural` (`internal/core/triage.go:1003`), so no second rule exists.
   The triage `Resolve first:` line, the amber preflight line and the TUI feed all call it.

The choices:

- **Where the stderr cleaning lives.** Taken: one writer that every stderr call site uses. Rejected: wrapping
  `env.Stderr` in a filtering `io.Writer` inside `Main`. A wrapper does not reach `cmd/r-loop/main.go` or a direct
  `fail` call, and it would have to keep `\n`/`\t` (`core.Displayable`), so an error with a newline would still split
  across lines.
- **How main.go's Getwd error is cleaned.** Taken: `app.Stderrf`, the same tested writer. Rejected: a separate
  `core.Printable` call in main.go. That would be a second copy of the stderr path. `run` takes `getwd` as a
  parameter so that test 15 can supply the OS error. Rejected: a subprocess with a removed working directory. It
  needs a helper process, and its OS error cannot carry control bytes to prove the cleaning.
- **Where the plan-title stop is cleaned.** Taken: on the stderr path. `fail` reaches it, and `CheckPlan` stays
  unchanged. Rejected: `core.Printable` inside `CheckPlan`. That cleans the text twice, and the notes from the same
  function already reach stdout through `writeWrapped`, which applies `Printable` (`internal/core/triage.go:935`).
- **Where the outside-repo check lives.** Taken: an `internal/app` helper that returns the `ExitError`. Rejected: a
  `gitrepo.Repo` method. The message is app wording with exit code 2, and `gitrepo` has no `ExitError`. The triage
  also asked for `gitrepo` to stay unchanged.
- **When `Wire` checks the location.** Taken: before `plan.Reader.Read`, so the user is told where the plan is before
  any parse error. Rejected: after `Read`. That shows a parse error for a plan that would be refused anyway.
- **Which outside-repo message is shown.** Taken: the `resolves to` form whenever the resolved file differs from the
  path as named. Rejected: telling a symlink apart by testing whether the unresolved path is inside the root. That
  test cannot be done reliably, because `env.Dir` (from Getwd) can be unresolved while `Root()` (git) is resolved.
- **The phase-noun helper.** Taken: `core.NamePhases(ids)` built on `Plural`. Rejected: a new `n == 1` check in each
  caller. That is a third copy beside `internal/face/tui/model.go:312-315`, and the design forbids a second
  pluralising rule.
- **What `todoRel` becomes.** Taken: an unexported field that `Wire` sets once. Rejected: keeping the method and
  calling `planRel` from it. `w.Todo` never changes after `Wire` (no assignment outside `Wire`), so computing it
  again is waste and adds an unreachable error branch.

## Why this approach

The triage proposed the following:

- #15: apply `core.Printable` in `fail`, the usage line, the status errors and main.go, and in `CheckPlan`.
- #17: one `internal/app` helper used in `Wire` before `Tracked`, in `reconcileLand` and in `todoRel`/`commitHint`,
  with `gitrepo` unchanged.
- #18: one small core helper used in both places, plus test and doc updates.

The watchdog's phase check added two requests: the TUI feed should use the same helper, and `Printable` should be
applied at the shared stderr writers.

This plan follows the triage for #17 and #18 and for the stderr sites it names, with these differences:

- **#15 covers every stderr write, through one exported writer.** The design requires that "the reviewer should find
  no stderr line that bypasses it". Today 20 sites write `r-loop: …` with `fmt.Fprintf` directly; the triage names
  only some of them. Examples:
  - `internal/app/status.go:119`: `skipped run %s: %v` carries a store error that names the repo path.
  - `internal/app/intake.go:131`: echoes the user's argv.
  - `internal/app/cleanup.go:68`, `internal/app/wire.go:305,369,377,385,391,421,427`, `internal/app/triage.go:115`,
    `internal/app/unblock.go:153`.

  An error's path is user-controlled: `git` reports `chdir <dir>` with raw bytes (checked: `exec.Command` with
  `Dir` set to a path containing ESC returns `"chdir /tmp/nope\x1b[2J\a: no such file or directory"`), and
  `git rev-parse --show-toplevel` prints a root containing ESC/BEL raw. Routing every site through one function is
  the only way the next message added stays clean.
- **`CheckPlan` is not changed.** `fail` cleans the joined stops (`internal/app/preflight.go:191` →
  `internal/app/wire.go:256`). The notes are cleaned by `writeWrapped` (`internal/core/triage.go:935`). The
  dry-run run-list line is cleaned at `internal/app/preflight.go:36`. A test pins the title on stderr.
- **#18's helper reuses `Plural`.** The design says "Do not invent … a new pluralising helper beside `Plural`".
  `NamePhases` holds no rule of its own: it takes the noun from `Plural(len(ids), "phase")`. This satisfies both the
  design and the watchdog's "one shared helper, no third copy". The TUI's copy at
  `internal/face/tui/model.go:312-315` is replaced by a call to it.

Files outside the phase's natural scope, and why:

- `internal/app/cleanup.go`, `intake.go`, `triage.go`, `unblock.go`, `resume.go`: stderr sites (#15) and the
  `planRel` call sites (#17).
- `internal/face/tui/model.go`: the watchdog's no-third-copy warning (#18).
- `cmd/r-loop/main.go`: the Getwd error and the tap usage lines (#15).
- `docs/task-loop-driver/tech-design.md`: the banner templates, which #18 requires.

## Changes

Build in this order.

1. **`internal/core/triage.go`** (modify). Add, directly after `Plural` (line 1003):

   ```go
   func NamePhases(ids []string) string {
   	_, noun, _ := strings.Cut(Plural(len(ids), "phase"), " ")
   	return noun + " " + joinIDs(ids)
   }
   ```

   - It reuses `Plural` (`internal/core/triage.go:1003`) and `joinIDs` (`internal/core/loop.go:2115`).
   - At line 820, change `d.Entry+" → phases "+joinIDs(d.Phases)` to `d.Entry+" → "+NamePhases(d.Phases)`.
   - Serves: #18 `Resolve first:` line.

2. **`internal/core/triage_test.go`** (modify). Line 509: change the expectation `Resolve first: Pick a DB → phases 7`
   to `Resolve first: Pick a DB → phase 7`. Add the new test from `## Tests`. Serves: #18 triage_test.

3. **`internal/face/tui/model.go`** (modify). Lines 312-316:
   - Delete the `noun` variable and its `if`.
   - Log `"Resolve first open: "+ev.Fields["entry"]+" · skipped "+core.NamePhases(ids)`.
   - The output is unchanged; the existing tests at `internal/face/tui/model_test.go:1329,1332` pin both forms.
   - Serves: watchdog warning (one shared helper).

4. **`internal/app/preflight.go`** (modify):
   - `blockingEntries` (line 122): change `scope = "phase " + strings.Join(hit, ", ")` to
     `scope = core.NamePhases(hit)`.
   - `commitHint` (lines 157-177): delete the EvalSymlinks+Rel block (lines 158-169) and start with
     `rel := w.todoRel`.

     Before this change, `commitHint` returned `""` when `filepath.Rel` failed. That case is now impossible, because
     `Wire` refuses such a plan. The `notes` line and the loop stay as they are.
   - Serves: #18 amber line; #17 (one EvalSymlinks+Rel copy).

5. **`internal/app/wire.go`** (modify):
   - **Add `Stderrf`**:

     ```go
     func Stderrf(out io.Writer, format string, args ...any) {
     	fmt.Fprintln(out, core.Printable(fmt.Sprintf(format, args...)))
     }
     ```

   - **`fail`** (line 318): replace line 324 with `Stderrf(env.Stderr, "r-loop: %s", err)`. `Printable` already folds
     `\n` to a space, so the `strings.ReplaceAll` goes.
   - **Usage line** (line 248): `Stderrf(env.Stderr, "usage: r-loop <todo.md> [flags]: %v", err)`.
   - **Line 305**: `Stderrf(env.Stderr, "r-loop: %s: %s", path, u)`.
   - **Lines 369, 377, 385, 391, 427**: the same format string without the trailing `\n`, through `Stderrf`.
   - **`panicked` (line 421)**: `Stderrf(w.Env.Stderr, "r-loop: %s", reason)` and then
     `fmt.Fprint(w.Env.Stderr, string(stack))`. The output order is unchanged.
   - **Add a `todoRel string` field** to `Wiring`, unexported, next to `LocalTodo` (line 99).
   - **Add `planRel` and `realPath`** below `Wire`:

     ```go
     func planRel(root, todo string) (string, error) {
     	realRoot, realTodo := realPath(root), realPath(todo)
     	rel, err := filepath.Rel(realRoot, realTodo)
     	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
     		return filepath.ToSlash(rel), nil
     	}
     	const fix = "move it into the repository, or run r-loop from the repository that holds it"
     	if realTodo != filepath.Clean(todo) {
     		return "", exit(2, "plan %s resolves to %s, outside the repository %s: %s", todo, realTodo, root, fix)
     	}
     	return "", exit(2, "plan %s is outside the repository %s: %s", todo, root, fix)
     }

     func realPath(path string) string {
     	if r, err := filepath.EvalSymlinks(path); err == nil {
     		return r
     	}
     	dir := filepath.Dir(path)
     	if dir == path {
     		return path
     	}
     	return filepath.Join(realPath(dir), filepath.Base(path))
     }
     ```

     - `realPath` resolves the nearest existing ancestor and attaches the missing suffix. A plan that does not exist
       is still compared against the resolved root, at any depth: a deleted recorded plan on resume, a mistyped name,
       or a missing directory under a symlinked working directory (`env.Dir` = `/tmp/alias` → the repo). Without
       that, an unresolved alias or macOS `/var/...` path compared with a resolved root reads as outside. The
       recursion ends at the filesystem root, where `filepath.Dir(path) == path`.
     - `filepath.Rel` never returns an absolute path for two absolute inputs. Its only error is "cannot be made
       relative", which means not inside, so it is reported with the same message rather than swallowed.
   - **`Wire`** (lines 476-505):
     - After `todo` is made absolute (line 479) and before `plan.Reader{}.Read`, add
       `rel, err := planRel(root, todo)` and `if err != nil { return nil, err }`.
     - Set `todoRel: rel` in the `Wiring` literal.
     - Line 501 becomes `tracked, err := repo.Tracked(w.todoRel)`.
   - Serves: #15 (fail, usage, every stderr line); #17 outside, symlink, inside tracked/ignored.

6. **`internal/app/unblock.go`** (modify):
   - Delete the method `todoRel` (lines 135-148).
   - Line 71: `todo := w.todoRel`.
   - Line 153: `Stderrf(w.Env.Stderr, "r-loop: store: %v", err)`.
   - `path/filepath` stays imported, because line 50 still uses it.
   - Serves: #17, #15.

7. **`internal/app/resume.go`** (modify):
   - `reconcileLand` lines 121-130: replace the root/todo EvalSymlinks, `filepath.Rel` and `ToSlash` block with the
     following, then keep the `repo.Tracked(todoRel)` call that follows:

     ```go
     todoRel, err := planRel(repo.Root(), todoAbs)
     if err != nil {
     	return err
     }
     todoAbs = realPath(todoAbs)
     ```

     The old block also replaced `todoAbs` with its resolved target (lines 123-124). The later reads and writes
     (`tickLocal` at line 146, `os.ReadFile`/`os.WriteFile` at lines 246-266, `todoMatchesOwnTick` at line 212)
     must keep working on the real file. A recorded plan that is a symlink inside the repo is then ticked through
     its target, and the link itself is not replaced: `plan.Reader.Tick` renames a temp file onto its input path.
   - Line 745: `Stderrf(env.Stderr, "usage: r-loop abort")`.
   - Serves: #17 resume, #15.

8. **`internal/app/status.go`** (modify):
   - Line 27: `Stderrf(env.Stderr, "usage: r-loop status [--plain] [<run-id>]")`.
   - Lines 31-34: `return fail(env, err)`.
   - Lines 49-52: `return fail(env, fmt.Errorf("load run %s: %w", id, err))`.
   - Lines 54-57: `return fail(env, err)`.
   - Line 119: `Stderrf(stderr, "r-loop: skipped run %s: %v", name, err)`.
   - `fail` defaults to exit 2, so codes and wording are unchanged except for the cleaning.
   - Serves: #15 status errors.

9. **`internal/app/cleanup.go`** (modify):
   - Line 68: `Stderrf(c.env.Stderr, "r-loop: "+format, args...)`.
   - Line 178: `Stderrf(c.env.Stderr, "r-loop: kept branch %s: it is checked out in %s", b, c.repo.Root())`.
   - Serves: #15.

10. **`internal/app/intake.go`** (modify). Line 131: `Stderrf(in.env.Stderr, "r-loop: resolved: r-loop %s",
    shellJoin(argv))`. Serves: #15.

11. **`internal/app/triage.go`** (modify). Line 115: `Stderrf(w.Env.Stderr, "r-loop: store: %v", err)`. Serves: #15.

12. **`cmd/r-loop/main.go`** (modify):
    - Line 28: `app.Stderrf(stderr, "r-loop: usage: r-loop statusline-tap <file>")`.
    - Line 36: `app.Stderrf(stderr, "r-loop: usage: r-loop hook-tap <file>")`.
    - `run` takes the working-directory lookup as a parameter, so a test can supply the OS error:
      `func run(args []string, stdout, stderr io.Writer, getwd func() (string, error)) int`. `main` calls
      `run(os.Args[1:], os.Stdout, os.Stderr, os.Getwd)`. Line 42 becomes `dir, err := getwd()`.
    - Line 44: `app.Stderrf(stderr, "r-loop: %v", err)`.
    - `fmt` stays imported for line 23.
    - **`cmd/r-loop/main_test.go`**:
      - Pass `os.Getwd` as the new last argument to the two existing `run` calls (lines 12 and 23).
      - Add the Getwd test from `## Tests`.
    - Serves: #15 Getwd and usage lines.

13. **`internal/app/*_test.go`** (modify / add tests). Add the app tests from `## Tests`:
    - `stderr_test.go` (new) holds the #15 tests.
    - `app_test.go` holds the #17 and #18 dry-run tests.
    - `resume_test.go` holds the #17 resume test.
    - `status_test.go` holds the status table test.

14. **`docs/task-loop-driver/tech-design.md`** (modify):
    - Line 697: replace `` `open ## Resolve first: "<name>" (<kind>) blocks phase <n> — <then>`. `` with
      `` `open ## Resolve first: "<name>" (<kind>) blocks phase <n> — <then>` (`blocks phases <n>, <n>` for more than
      one, `blocks every phase` for an entry that blocks all). ``
    - Line 702: `` `Resolve first: <entry> → phases <ids>` `` becomes
      `` `Resolve first: <entry> → phase <id>` (`→ phases <id>, <id>` for more than one) ``.
    - Serves: #18 tech-design template.

## Tests

Every test has `// given`, `// when` and `// then` blocks. New app tests use the existing fixture helpers:
`newFixture` / `newFixtureIn` (`internal/app/app_test.go:61,70`), `f.write`, `f.commit`, `f.main`, `git`,
`f.untrackTodo` (`internal/app/resume_test.go:1890`), `newResumeFixture` (`internal/app/resume_test.go:210`),
`f.appendRunEvent` (`internal/app/resume_test.go:1525`), `ev` (`internal/app/status_test.go:102`) and
`store.New(f.root).Create`.

1. **`TestFailWritesTheErrorWithoutControlBytesAndKeepsItsExitCode`** (`internal/app/stderr_test.go`)
   - Given `var actualStderr bytes.Buffer`.
   - When `actualCode := fail(Env{Stderr: &actualStderr}, exit(3, "a\x1b[2Jb\x07c\nd"))`.
   - Then `actualStderr.String() == "r-loop: a[2Jbc d\n"` and `actualCode == 3`.
   - Covers: #15 "an app test passes an error containing \x1b and \x07 through the stderr error path".

2. **`TestAPlanPathWithControlBytesExits2WithACleanStderrLine`** (`internal/app/stderr_test.go`)
   - Given `f := newFixture(t)`.
   - When `f.main("x\x1b[2Jy\x1b]0;pwn\a.md", "--dry-run", "--plain")`.
   - Then the code is 2 and stderr equals `"r-loop: open " + f.root + "/x[2Jy]0;pwn.md: no such file or directory\n"`.
   - Covers: #15 hostile plan path, exit 2, no 0x1b/0x07.

3. **`TestAPhaseTitleWithControlBytesAndNoChecklistExits2WithTheTitleCleaned`** (`internal/app/stderr_test.go`)
   - Given `f.write("docs/topic/todo.md", "# Plan\n\n### Phase 1 — One\n- [ ] a\n\n### Phase 2 — x\x1b[2Jy\x1b]0;pwn\x07z\n**Done when:** `go test ./...`\n")`
     and `f.commit()`.
   - When `f.main(f.todo, "--dry-run", "--plain")`.
   - Then the code is 2 and stderr equals `"r-loop: Phase 2 — x[2Jy]0;pwnz: no checklist items\n"`.
   - Covers: #15 plan-title stop.

4. **`TestAFlagWithControlBytesPrintsACleanUsageLine`** (`internal/app/stderr_test.go`)
   - Given `f := newFixture(t)`.
   - When `f.main(f.todo, "-x\x1b[2J\x07")`.
   - Then the code is 2 and stderr equals
     `"usage: r-loop <todo.md> [flags]: flag provided but not defined: -x[2J\n"`.
   - Covers: #15 usage line.

5. **`TestStatusErrorsReachStderrWithoutControlBytes`** (`internal/app/status_test.go`)
   - Given a fixture rooted in a hostile directory, built by the helper `newHostileFixture(t)`: it calls
     `os.Mkdir(filepath.Join(<EvalSymlinks(t.TempDir())>, "r\x1b]0;pwn\x07"))` and then `newFixtureIn(t, thatDir)`.
   - The table has one row per error. Each row has a `setup func(f *fixture) []string` that returns the `status` args:
     - **`repo open`**: sets `f.env.Dir = filepath.Join(f.root, "gone")` and returns `{"status"}`.
     - **`load run`**: runs `os.MkdirAll(store.New(f.root).Dir("20261015-120000"), 0o755)`, which leaves the run
       without `meta.json`, and returns `{"status", "20261015-120000"}`.
     - **`plan read`**: creates a run with `store.New(f.root).Create(core.RunMeta{Todo: filepath.Join(f.root,
       "gone.md"), Started: t0})` and returns `{"status", id}`.
   - When `f.main(args...)`.
   - Then, for each row:
     - `actualCode == 2`;
     - `strings.HasSuffix(actualStderr, "\n")`;
     - `strings.IndexFunc(strings.TrimSuffix(actualStderr, "\n"), unicode.IsControl) == -1`;
     - `strings.Contains(actualStderr, "r]0;pwn")`, which proves the hostile root reached the line and was cleaned.
   - Covers: #15 status errors (repo open, load run, plan read), no C0/C1 bytes.

6. **`TestAPlanOutsideTheRepositoryExits2NamingThePlanAndTheRoot`** (`internal/app/app_test.go`)
   - Given `f := newFixture(t)`; `outside := filepath.Join(<EvalSymlinks(t.TempDir())>, "todo.md")`, written with
     `"# Plan\n\n### Phase 1 — One\n- [ ] a\n"`.
   - When `f.main(outside, "--dry-run", "--plain")`.
   - Then the code is 2 and stderr equals
     `"r-loop: plan " + outside + " is outside the repository " + f.root + ": move it into the repository, or run r-loop from the repository that holds it\n"`.
     That exact match excludes `ls-files` and `fatal:`.
   - Covers: #17 outside plan.

7. **`TestASymlinkInTheRepositoryToAPlanOutsideItIsRejected`** (`internal/app/app_test.go`)
   - Given `outside` as in test 6, and `link := filepath.Join(f.root, "docs", "topic", "link.md")` created with
     `os.Symlink(outside, link)`.
   - When `f.main(link, "--dry-run", "--plain")`.
   - Then the code is 2 and stderr equals
     `"r-loop: plan " + link + " resolves to " + outside + ", outside the repository " + f.root + ": move it into the repository, or run r-loop from the repository that holds it\n"`.
   - Covers: #17 symlink.

8. **`TestAPlanInsideTheRepositoryPassesDryRunTrackedOrIgnored`** (`internal/app/app_test.go`)
   - Given a table with two rows. In both, `f.write("docs/topic/todo.md", "# Plan\n\n### Phase 1 — One\n- [ ] a\n")`
     and `f.commit()`. The rows differ in `setup func(f *fixture)`:
     - **`tracked`**: does nothing.
     - **`gitignored`**: calls `f.untrackTodo()`.
   - When `f.main(f.todo, "--dry-run", "--plain")`.
   - Then `actualCode == 0` and `actualStderr == ""`.
   - Covers: #17 inside, tracked and ADR-84 local todo.

9. **`TestResumeOfARunWhosePlanIsOutsideTheRepositoryNamesIt`** (`internal/app/resume_test.go`)
   - Given `f := newResumeFixture(t, noReviewConfig)`; `outside` (as in test 6) written with `resumeTodo`; and
     `id, _ := store.New(f.root).Create(core.RunMeta{Todo: outside, Branch: "main", Started: t0})`.
   - The table has two rows, each with `setup func(f *fixture, id string)`:
     - **`no merge in flight`**: does nothing. This row reaches `Wire`.
     - **`merge intent recorded`**: calls
       `f.appendRunEvent(id, ev(t0, "merge-intent", 1, "land", map[string]string{"phase": "1", "branch": "r-loop/phase-1", "base": "x", "message": "phase 1: one"}))`.
       This row reaches `reconcileLand`.
   - When `f.main("resume", id)`.
   - Then the code is 2 and stderr equals the same line as test 6, with `outside` and `f.root`.
   - Covers: #17 resume, on both routes.

10. **`TestDryRunNamesAResolveFirstEntryThatBlocksTwoPhasesInThePlural`** (`internal/app/app_test.go`)
    - Given `f.write("docs/topic/todo.md", "# Plan\n\n## Resolve first\n- [ ] **Measure it** — how fast?\n      Owner: me. Blocks: Phases 2, 3.\n\n### Phase 1 — One\n- [ ] a\n\n### Phase 2 — Two\n- [ ] b\n\n### Phase 3 — Three\n- [ ] c\n")`
      and `f.commit()`.
    - When `f.main(f.todo, "--dry-run", "--plain")`.
    - Then the code is 0 and stdout contains
      `open ## Resolve first: "Measure it" (unclassified) blocks phases 2, 3 — the watchdog will walk it`.
    - Covers: #18 amber line, two phases.

11. **`TestDryRunNamesAnOpenResolveFirstEntryThatBlocksTheRunList`** (existing, `internal/app/app_test.go:893`, unchanged)
    - Still asserts `blocks phase 2` for one phase.
    - Covers: #18 amber line, one phase.

12. **`TestRenderTriagePlanAndBacklog`** (existing, `internal/core/triage_test.go:473`; its expectation at line 509
    becomes `Resolve first: Pick a DB → phase 7`)
    - Covers: #18 `Resolve first:` single phase; triage_test expectation.

13. **`TestRenderTriageNamesTwoDeferredPhasesInThePlural`** (`internal/core/triage_test.go`)
    - Given `plan := triagePlan()`, `view := triageView(plan, plan.Phases[1:])` and
      `view.Deferrals = []Deferral{{Entry: "Pick a DB", Phases: []string{"3", "4"}}}`.
    - When `_, actualTable := RenderTriage(view, nil)`.
    - Then `actualTable` contains `"Resolve first: Pick a DB → phases 3, 4\n"`.
    - Covers: #18 `Resolve first:` two phases.

14. **`TestAPhaseDeferredByAnOpenResolveFirstEntryIsMarkedAndNamed`** (existing,
    `internal/face/tui/model_test.go:1323`, unchanged)
    - Pins `skipped phases 2, 4` and `skipped phase 3` after the TUI calls `core.NamePhases`.
    - Covers: watchdog warning (TUI uses the shared helper, output unchanged).

15. **`TestAGetwdErrorWithControlBytesPrintsACleanLine`** (`cmd/r-loop/main_test.go`)
    - Given `var actualStderr bytes.Buffer`.
    - When `actualCode := run([]string{"todo.md"}, &bytes.Buffer{}, &actualStderr, func() (string, error) { return "", errors.New("getwd: \x1b[2Jgone\x07") })`.
    - Then `actualCode == 2` and `actualStderr.String() == "r-loop: getwd: [2Jgone\n"`.
    - Covers: #15 Getwd error.

16. **`TestAMissingPlanUnderASymlinkedWorkingDirectoryIsReportedAsMissing`** (`internal/app/app_test.go`)
    - Given `f := newFixture(t)`; `alias := filepath.Join(<EvalSymlinks(t.TempDir())>, "alias")`, created with
      `os.Symlink(f.root, alias)`; and `f.env.Dir = alias`.
    - When `f.main("missing/todo.md", "--dry-run", "--plain")`.
    - Then the code is 2 and stderr equals
      `"r-loop: open " + alias + "/missing/todo.md: no such file or directory\n"`.
      This is the plan-read error, not an outside-repo refusal.
    - Covers: #17 an in-repo plan is never refused as outside: a missing ancestor under a symlinked working
      directory.

17. **`TestResumeTicksTheTargetOfALocalTodoSymlinkAndKeepsTheLink`** (`internal/app/resume_test.go`)
    - Given:
      - `f := newResumeFixture(t, noReviewConfig)` and `f.untrackTodo()`;
      - `os.Symlink("todo.md", filepath.Join(f.root, "docs/topic/alias.md"))`;
      - `f.todo = filepath.Join(f.root, "docs/topic/alias.md")`, so `seedKilledLand` records the link as the run's
        plan;
      - `id, _, base := f.seedKilledLand("1")`, `f.seedMergeIntent(id, base)`, `f.beginLandMerge()`,
        `f.seedLocalLandCommitIntent(id)`, and `git(t, f.root, "commit", "-q", "-m", "phase 1: one")`.

      This is the pattern of `TestResumeTicksALocalTodoForAPhaseThatLandedBeforeTheCrash`
      (`internal/app/resume_test.go:1929`).
    - When `f.resume(newSim())`.
    - Then:
      - `actualTarget, _ := os.ReadFile(filepath.Join(f.root, "docs/topic/todo.md"))` contains `"- [x] a"`;
      - `actualLink, _ := os.Readlink(filepath.Join(f.root, "docs/topic/alias.md"))` equals `"todo.md"`.
    - Covers: #17 regression guard. `reconcileLand` still ticks the resolved local todo (ADR-84) after `planRel`
      replaces its EvalSymlinks+Rel block.

## Left out

- **`core.Printable` inside `CheckPlan`**: the stop is cleaned on stderr by `fail`, and the notes by `writeWrapped`
  (`internal/core/triage.go:935`). No obligation needs a second pass.
- **A filtering `io.Writer` around `env.Stderr`**: it would be a second mechanism beside `Stderrf`. It would not cover
  `cmd/r-loop/main.go` or direct `fail` calls, and it would have to keep newlines.
- **A `gitrepo` method for the location check**: the message and exit code are app concerns, and the triage asked
  for `gitrepo` to stay unchanged.
- **A direct unit test of `NamePhases` or `planRel`**: tests 6-13 pin every form through the real paths.
- **A tech-design line for the outside-repo refusal**: no item asks for it. The message lives in the design file and
  in tests 6-9.
- **Cleaning the panic stack trace**: Go runtime text that carries no user input; only the panic value is user-shaped,
  and it is cleaned.

## Assumptions

- **Watchdog warning #18, the TUI helper.** Resolved: the TUI feed (`internal/face/tui/model.go:312-315`) calls
  `core.NamePhases`, the one shared helper. Its output is unchanged and pinned by test 14.
- **Watchdog warning #15, cleaning at the shared writers.** Resolved: `fail` and every other stderr line go through
  `Stderrf`, which applies `core.Printable`. The plan-title stop reaches stderr only through `fail`, so it is cleaned
  there (test 3). `CheckPlan` itself is not changed, for the reason under `## Left out`. The watchdog's intent, a
  title shown without control bytes, is met.
- **The design's "Do not invent a new pluralising helper beside `Plural`".** `NamePhases` is read as a formatting
  helper, not a pluralising one, because it takes its rule from `Plural`. That meets both the design and the
  watchdog's one-helper request.
- **The Getwd error.** `run` takes `getwd` as a parameter because the working-directory lookup is an OS I/O
  boundary. A process-wide chdir into a removed directory would race the other tests. Test 15 supplies the error.
- **The panic stack.** It is printed raw after the cleaned `r-loop: panic: …` line, because it must stay multi-line.
- **On a system where Getwd returns an unresolved path** (macOS `/var` → `/private/var`), an outside plan named
  through that path shows the `resolves to` form, which is accurate.
- **A plan path equal to the repo root** (`rel == "."`) passes `planRel`, and `plan.Reader.Read` then fails on the
  directory with its own error. That is unchanged behaviour.
- **Exit codes are unchanged everywhere**:
  - the status errors stay 2 (the `fail` default);
  - the outside-repo refusal is 2, as the item requires;
  - the usage lines stay 2.

## Gate

`go test ./internal/core/ -run '^(TestRenderTriagePlanAndBacklog|TestRenderTriageNamesTwoDeferredPhasesInThePlural)$' && go test ./cmd/r-loop/ -run '^TestAGetwdErrorWithControlBytesPrintsACleanLine$' && go test ./internal/face/tui/ -run '^TestAPhaseDeferredByAnOpenResolveFirstEntryIsMarkedAndNamed$' && go test ./internal/app/ -run '^(TestFailWritesTheErrorWithoutControlBytesAndKeepsItsExitCode|TestAPlanPathWithControlBytesExits2WithACleanStderrLine|TestAPhaseTitleWithControlBytesAndNoChecklistExits2WithTheTitleCleaned|TestAFlagWithControlBytesPrintsACleanUsageLine|TestStatusErrorsReachStderrWithoutControlBytes|TestAPlanOutsideTheRepositoryExits2NamingThePlanAndTheRoot|TestASymlinkInTheRepositoryToAPlanOutsideItIsRejected|TestAPlanInsideTheRepositoryPassesDryRunTrackedOrIgnored|TestResumeOfARunWhosePlanIsOutsideTheRepositoryNamesIt|TestDryRunNamesAResolveFirstEntryThatBlocksTwoPhasesInThePlural|TestDryRunNamesAnOpenResolveFirstEntryThatBlocksTheRunList|TestAMissingPlanUnderASymlinkedWorkingDirectoryIsReportedAsMissing|TestResumeTicksTheTargetOfALocalTodoSymlinkAndKeepsTheLink)$'`
