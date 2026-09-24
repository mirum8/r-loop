status: planned

## Summary

One change in `internal/core` fixes backlog items #7 and #8. It also updates the contract in `docs/task-loop-driver/tech-design.md`.

- **#7, gate derivation.** `gateCommand` (`internal/core/land.go:18-29`) stops joining every code span with `&&`. It reads the prose around each span:
  - **Literal span.** A span whose prose since the previous span ends in `prints` or `lists`, optionally followed by `the`, is an expected-output literal. It belongs to the nearest command span before it and is never run. This covers `prints \`X\``, `lists \`X\`` and `prints the \`flags\` line`.
  - **"Prints nothing".** A command span followed by `prints nothing` or `lists nothing` must print nothing.
  - **Checked command.** A command with a literal or a "nothing" check becomes one braced clause. The clause runs the command in its own subshell `( … )` (so an `exit` in it cannot skip the capture) and captures stdout and stderr in `$out` byte for byte, with the exit status in `$st`: `echo ".$?"` is appended inside the command substitution and split off after (`st=${out##*.}; out=${out%.*}`), so trailing newlines survive and newline-only output is not empty. It echoes `$out` so the gate output shows it, then runs its checks:
    - `test -z "$out"` for "nothing" (exit status ignored);
    - for literals, `test "$st" = 0`, then `printf '%s\n' "$out" | grep -qF -e '<literal>'` for each literal.
  - **Plain command.** Every other command stays verbatim. Clauses are joined with ` && `, so a line whose spans are all commands gives exactly today's string.
- **#8, test paths.** `isTestPath` (`internal/core/checks.go:191-199`) now also matches:
  - the file names `*.spec.*`, `test_*.py`, `*Test.java` and `*Test.kt`;
  - any directory segment `test`, `tests` or `__tests__`, at any depth.

  The item gate's red check (`internal/core/land.go:232`) and `foreign-test-edit` (`internal/core/checks.go:211`) both call `isTestPath`. The copy into the red worktree (`land.go:240-243`) already copies every path the rule accepts, so neither caller changes.

Choices:

- **Telling a literal from a command.** Option taken: the prose verb before the span. Rejected:
  - Guessing from the span's content (for example, "is its first word on `PATH`?"). That depends on the machine, and `flags` or `intake:` could be a binary.
  - Rewording the todo.md lines. #7 requires those exact lines to give passing gates, and future lines need a rule.
- **Exit status of a command with a print check.** Option taken: "prints nothing" ignores it; a literal check also requires exit 0. Rejected:
  - Requiring exit 0 for "prints nothing" too. `grep` exits 1 when it prints nothing, so lines 548 and 590 would always be red.
  - Ignoring it for literals too. A command that prints the literal and then fails would pass. Lines 518, 532 and 561 exit 0 on a correct tree (the dry run exits 0 even with an empty `HOME`), so requiring it costs nothing.
- **Which stream is judged.** Option taken: stdout and stderr (`2>&1`). Rejected: stdout only. A `grep` over a missing path writes only to stderr, and "prints nothing" would then pass vacuously.
- **Clause shape.** Option taken: capture into `$out`, echo it, check it, all inside `{ …; }`. Rejected:
  - A bare pipe (`cmd | grep -q`) or `test -z "$(cmd)"`. The gate output would be empty, and the gate-fix step gets `GateOutput` to work from.
  - The same clause without braces. `;` would break the `&&` chain: a red `go test` would be followed by a passing `test -z` of an empty `$out`.
  - A plain `out=$(…)` without the `".$?"` marker. Command substitution strips trailing newlines, so a command printing only `\n` would pass "prints nothing".
- **Test-path rule.** Option taken: fixed file-name patterns plus test directory names at any depth. Rejected: a per-language config key. Nobody asked for one, and the rule has two callers that must agree.
- **Real-line test.** Option taken: derive each gate from the real todo.md line and run it at the repo root. `go test` is shimmed to a no-op; `go run` and `grep` are real. Rejected: asserting the derived strings.
  - Those strings would put line 548's grep pattern into a file under `internal/`, which turns 548's own gate red.
  - Asserting strings would not show that the gate passes on a correct tree.

## Changes

Build order:

1. **`internal/core/land.go` (modify)**, serves #7 c1, c2, c3 and c4.
   - Keep the signature `func gateCommand(doneWhen string) string`. It is called only at `land.go:144`, and its result also becomes the gate fix's `GateCommand` (`land.go:300`), unchanged.
   - Add two package-level regexps next to `codeSpanRe` (`land.go:16`):
     - `var printsBeforeRe = regexp.MustCompile(`(?i)\b(prints|lists)(\s+the)?\s*$`)`
     - `var printsNothingRe = regexp.MustCompile(`(?i)^\s*(prints|lists)\s+nothing\b`)`
   - New body:
     1. `ms := codeSpanRe.FindAllStringSubmatchIndex(doneWhen, -1)`. Keep only spans whose `strings.TrimSpace` text is non-empty, as today.
     2. If none are left, return `strings.TrimSpace(doneWhen)`, unchanged.
     3. Walk the kept spans in order with a local slice of `struct{ cmd string; literals []string; nothing bool }`. For span `i`, `gap` is `doneWhen[prevEnd:start]`, where `prevEnd` is the end of the previous kept span's full match (0 for the first).
        - If the slice is non-empty and `printsBeforeRe.MatchString(gap)`, append the trimmed text to the last clause's `literals`.
        - Otherwise append a new clause with `cmd` = the trimmed text. Set `nothing = printsNothingRe.MatchString(after)`, where `after` is `doneWhen[end:nextStart]` (the next kept span's start, or `len(doneWhen)`).
        - A literal-looking span with no command before it therefore stays a command.
     4. Render each clause:
        - With no literals and `nothing` false, it is `cmd` verbatim.
        - Otherwise build `checks`: `test -z "$out"` when `nothing`; when there are literals, `test "$st" = 0` and then one `printf '%s\n' "$out" | grep -qF -e ` + `shellQuote(lit)` per literal, in order. The clause is:

          ```
          "{ out=$( ( " + cmd + " ) 2>&1; echo \".$?\"); st=${out##*.}; out=${out%.*}; printf '%s' \"$out\"; " + strings.Join(checks, " && ") + "; }"
          ```
     5. Return the rendered clauses joined with `" && "`.

     `shellQuote` is reused from `internal/core/checks.go:201`.
   - Examples of the exact output:
     - "`a` is green and `b x` prints `lit's`." → `a && { out=$( ( b x ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test "$st" = 0 && printf '%s\n' "$out" | grep -qF -e 'lit'\''s'; }`
     - "`a` is green and `b` prints nothing." → `a && { out=$( ( b ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test -z "$out"; }`

2. **`internal/core/checks.go` (modify)**, serves #8 c1, c2, c3 and c4.
   - Keep the signature `func isTestPath(p string) bool`. New body:
     1. `dir, base := path.Split(p)` (`path` is already imported, `checks.go:6`).
     2. Return true if `base` matches any of `"*_test.go", "*.test.*", "*_test.*", "*.spec.*", "test_*.py", "*Test.java", "*Test.kt"` under `path.Match`. This is the existing loop at `checks.go:193-197` with four patterns added.
     3. Otherwise return true if any segment of `strings.Split(dir, "/")` is `test`, `tests` or `__tests__` (a `switch`).
     4. Otherwise return false.
   - The old `strings.HasPrefix(p, "test/")` goes away: the segment rule covers it.
   - `Contest.java` does not match `*Test.java` (`path.Match` is case-sensitive), and `latest.go` matches nothing.
   - No change at the call sites `land.go:232` and `checks.go:211`.

3. **`docs/task-loop-driver/tech-design.md` (modify)**, serves #7 c4 and keeps #8's contract true.
   - **Lines 371-372.** In the Land bullet, replace "the gate command being the `Done when:` line's inline code spans joined with ` && `, or its trimmed text when it has none" with:

     > the gate command being built from the `Done when:` line's inline code spans: a span whose prose since the previous span ends in `prints` or `lists` (optionally followed by `the`) is an expected-output literal of the nearest command span before it and is never run; every other span is a command. A command followed by `prints nothing`/`lists nothing`, or carrying literals, runs as `{ out=$( ( <cmd> ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; <checks>; }`, its stdout+stderr judged: `test -z "$out"` for nothing (exit status ignored, so a silent `grep` with no match passes); for literals, `test "$st" = 0` then `printf '%s\n' "$out" | grep -qF -e '<literal>'` per literal. Clauses are joined with ` && ` (a line of only commands gives its spans joined with ` && `), or the gate is the line's trimmed text when it has no span.

     Keep the following "(the gate fix's `GateCommand` is the same string)".
   - **Line 691.** Replace "changed test files (`isTestPath`)" with:

     > changed test files (`isTestPath`: a file name matching `*_test.*`, `*.test.*`, `*.spec.*`, `test_*.py`, `*Test.java` or `*Test.kt`, or any directory segment `test`, `tests` or `__tests__`; `foreign-test-edit` uses the same rule)

4. **Tests.** Four files: `internal/core/gatecommand_test.go` (create, `package core`), `internal/core/land_test.go`, `internal/core/gate_test.go` and `internal/core/checks_test.go` (modify). See `## Tests`.

## Tests

Write these first. Every one calls only `gateCommand(string) string`, `isTestPath(string) bool` or `LandGate.Land`. All three exist on the base, so the new tests compile there and fail.

**`internal/core/gatecommand_test.go`** (new, `package core`):

- **`TestGateCommandKeepsALineOfOnlyCommands`**, #7 c3. A table asserts exact `gateCommand` output:
  - "`go test ./...` is green." → `go test ./...`
  - "`test -f feature.txt` is green and\n`test -f fix1.txt` finds the fix." → `test -f feature.txt && test -f fix1.txt`
  - "`go build ./cmd/r-loop && go test ./internal/app/...` is green and `go run ./cmd/r-loop x.md --dry-run --plain` prints the banner and run list." → `go build ./cmd/r-loop && go test ./internal/app/... && go run ./cmd/r-loop x.md --dry-run --plain`
  - "`go vet ./...` is green, `make report` prints the summary and `test -f out.txt` exits 0." → `go vet ./... && make report && test -f out.txt`. A verb that is not directly before a span makes no literal.
  - "`grep -n tool a.go b.md` prints the tool and the prompt line." (the shape of line 577) → `grep -n tool a.go b.md`
  - "  make check  " → `make check` (no span).
- **`TestGateCommandWrapsAPrintCheck`**, #7 c1 and c2 (the exact shape, and quoting a literal that holds `'`). A table:
  - "`a` is green and `b x` prints `lit's`." → `a && { out=$( ( b x ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test "$st" = 0 && printf '%s\n' "$out" | grep -qF -e 'lit'\''s'; }`
  - "`a` is green and `b` prints nothing." → `a && { out=$( ( b ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test -z "$out"; }`
  - "`a` lists the `k` line." → `{ out=$( ( a ) 2>&1; echo ".$?"); st=${out##*.}; out=${out%.*}; printf '%s' "$out"; test "$st" = 0 && printf '%s\n' "$out" | grep -qF -e 'k'; }`
- **`TestGateCommandFromTheRealTodoLinesPassesOnACorrectTree`**, #7 c4, and the watchdog's warning about line 561. Setup:
  1. Read `../../docs/task-loop-driver/todo.md` and split it on `"\n"`.
  2. For each `n` in 518, 532, 548, 561, 590 (subtest `t.Run(strconv.Itoa(n))`), fail with the line number if `lines[n-1]` does not start with `**Done when:** `. Otherwise `doneWhen := strings.TrimPrefix(lines[n-1], "**Done when:** ")`.
  3. Build a shim directory `t.TempDir()` holding an executable `go`: `#!/bin/sh\n[ "$1" = test ] && exit 0\nexec '<real go>' "$@"\n`. `<real go>` comes from `exec.LookPath("go")`. This stops the gate re-running the whole suite recursively; `go run` stays real.
  4. Run `exec.Command(realGo, "env", "GOCACHE", "GOMODCACHE", "GOPATH", "GOENV")` and keep its four output lines.
  5. For each line, run `exec.Command("sh", "-c", gateCommand(doneWhen))` with `Dir = "../.."`. `Env` is `os.Environ()` plus `PATH=<shim>:<old PATH>`, `HOME=<t.TempDir()>` (so the maintainer's `~/.config/r-loop/config.yaml` cannot change line 532's `← default`) and the four Go variables.
  6. Require exit 0. On failure, report the command and the combined output.

  On the base all five are red: 518, 532 and 561 run their literal, and 548 and 590 get grep's exit 1. The file must not contain line 548's grep pattern words, since they would make 548's own gate red.

**`internal/core/land_test.go`** (`package core_test`, uses `newLandEnv`, `phaseWork`, `gate`, `phaseOne`, `assertUntouched` from `land_test.go:361-439`):

- **`TestLandGatePrintsNothingPassesWhenTheCommandPrintsNothing`**, #7 c1.
  - Setup: `phaseWork(1, "feature.txt", "new\n")`; Done when is "`test -f feature.txt` is green and `grep -n TODO feature.txt` prints nothing."
  - Expect: `Land` returns nil and `landing.MergeSHA == e.head()`.
- **`TestLandGatePrintsNothingFailsWhenTheCommandPrints`**, #7 c1.
  - Setup: feature.txt holds `"TODO\n"`; same Done when.
  - Expect: `errors.Is(err, core.ErrGate)`, the error contains `1:TODO`, and `assertUntouched(head)`.
- **`TestLandGatePrintsNothingFailsOnAnErrorMessage`**, the stderr edge.
  - Setup: Done when is "`grep -n TODO missing.txt` prints nothing." with feature.txt present.
  - Expect: `ErrGate` and `assertUntouched`.
- **`TestLandGatePrintsNothingFailsOnNewlineOnlyOutput`**, #7 c1 (output of only newline bytes is still output).
  - Setup: `phaseWork(1, "feature.txt", "new\n")`; Done when is "`printf '\n\n'` prints nothing."
  - Expect: `errors.Is(err, core.ErrGate)` and `assertUntouched(head)`.
- **`TestLandGatePrintsALiteralPassesWhenTheOutputContainsItAndNeverRunsIt`**, #7 c2.
  - Setup: feature.txt holds `"touch ran.txt\n"`; Done when is "`cat feature.txt` prints `touch ran.txt`."
  - Expect: `Land` returns nil, and `filepath.Join(e.root, "ran.txt")` does not exist.
- **`TestLandGatePrintsALiteralFailsWhenTheCommandFails`**, #7 c2 (the literal is necessary, not sufficient).
  - Setup: `phaseWork(1, "feature.txt", "new\n")`; Done when is "`sh -c 'printf hello; exit 7'` prints `hello`."
  - Expect: `errors.Is(err, core.ErrGate)`, the error contains `hello`, and `assertUntouched(head)`.
- **`TestLandGatePrintsALiteralFailsWhenTheOutputLacksIt`**, #7 c2.
  - Setup: feature.txt holds `"goodbye\n"`; Done when is "`cat feature.txt` prints `hello`."
  - Expect: `ErrGate`; the error contains `goodbye` and does not contain `not found` (the literal was not run); `assertUntouched`.

**`internal/core/gate_test.go`** (`package core_test`, uses `itemPlan`, `itemWork`, `itemGate` from `gate_test.go:25-49`):

- **`TestItemGateCopiesNonGoTestsIntoTheRedWorktree`**, #8 c1. Subtests for `src/test/java/a/FooTest.java`, `app/FooTest.kt`, `tests/test_foo.py`, `web/foo.spec.ts` and `pkg/__tests__/x.js`. Each subtest:
  1. `newLandEnv(t)` and `marker := filepath.Join(t.TempDir(), "runs")`.
  2. The test file's content is `"echo run >> '" + marker + "'\ntest -f feature.txt\n"`.
  3. `itemWork(1, …)` with three files: `.task-plans/phase-1-first.md` = `strings.Replace(itemPlan, "sh feature_test.sh", "sh "+path, 1)`, the test file, and `feature.txt` = `"new\n"`.
  4. `g, _ := e.itemGate("true")`, then `g.Land(ctx, phaseOne(""))`.

  Expect: nil error (past "no test file"), and the marker reads exactly `"run\nrun\n"`. One line is from the red run, which only happens if the file was copied into `.r-loop/wt/phase-1-red`; the other is from the merged run.

**`internal/core/checks_test.go`** (`package core`):

- **`TestIsTestPathCoversOtherLanguagesAndNestedTestDirs`**, #8 c1, c2 and c3. A table checked with `isTestPath`:
  - True: `src/test/java/a/FooTest.java`, `app/FooTest.kt`, `tests/test_foo.py`, `web/foo.spec.ts`, `pkg/__tests__/x.js`, `module/src/test/java/a/Bar.java`, `sub/tests/helpers.py`, `sub/test/fixtures/data.json`, `a/b_test.go`, `web/app.test.ts`, `lib/thing_test.py`, `test/unit/x.go`.
  - False: `src/main/java/Contest.java`, `latest.go`, `internal/core/land.go`, `contest/main.go`, `docs/testing.md`.
- **`TestForeignTestEditMatchesTheWidenedTestPaths`**, #8 c4. This follows `TestForeignTestEditMatchesEveryTestPattern` (`checks_test.go:246-259`) with paths `src/test/java/a/FooTest.java`, `app/FooTest.kt`, `tests/test_foo.py`, `web/foo.spec.ts`, `pkg/__tests__/x.js`, `module/src/test/java/a/Bar.java`, `sub/tests/helpers.py` and `sub/test/x.go`. Each is `Changed` with `RunOutput: path + "\n"`, and each gives one warning naming the path.
- **`TestForeignTestEditQuietOnProductionLookalikes`**, #8 c3 and c4. `Changed` is `src/main/java/Contest.java` and `latest.go`, and `RunOutput` names both. Expect `noWarning`.

## Left out

- **Several literals on one command joined by "and"** (such as "prints `a` and `b`"). No `Done when:` line in the repository has this shape. The second span stays a command, as today.
- **A literal span with no command before it.** No line has this shape. It stays a command, the same as today's rule.
- **More test naming conventions** (`*Tests.java`, `*IT.java`, `spec/` directories, Go `testdata/`). No obligation names them: `src/test/…` already covers JVM integration tests, and #8 names the five listed paths.
- **A config key for test patterns, or for the output-check verbs.** No story asks for one.
- **Rewording `docs/task-loop-driver/todo.md`.** Line 421 still describes the old `foreign-test-edit` pattern, but it is a ticked history item. Editing that file is also outside this phase.

## Assumptions

- **The watchdog's line-561 warning is resolved, not waived.** 561 is in the real-line test. Under the new rule its `flags` span is a literal (the prose before it ends in "prints the"), matched with `grep -qF` against the grep output `2:flags: …` from `internal/providers/shipped/codex.yaml:2`. Line 577 has no literal span and keeps today's two-command join; `TestGateCommandKeepsALineOfOnlyCommands` pins that shape.
- **"Passes on a correct tree" is judged with `go test` shimmed to a no-op.** Running the real `go test ./...` inside a test of the same suite would recurse. The go-test half of each line is plain `&&` chaining, which `TestGateCommandKeepsALineOfOnlyCommands` and the land tests cover.
- **The real-line test runs with an empty `HOME`.** Line 532's literal is `← default`. On a machine whose `~/.config/r-loop/config.yaml` sets `intake.*`, `go run … --dry-run --plain` prints `← provider ~/.config/…` instead (seen on this machine). The tree is still correct there, so the test isolates the home layer. `GOCACHE`, `GOMODCACHE`, `GOPATH` and `GOENV` are passed through explicitly, so the empty `HOME` does not force a cold build or module download.
- **Output checks judge stdout and stderr together; "prints nothing" ignores the exit status, a literal check requires exit 0.** See Summary, choices 2 and 3.
- **`isTestPath` input is slash-separated and repo-relative.** `Repo.ChangedFiles` and the checks' `changedFiles` produce git paths, so `path` (not `filepath`) is right.

## Gate

`go test ./internal/core/ -count=1 -run '^(TestGateCommandKeepsALineOfOnlyCommands|TestGateCommandWrapsAPrintCheck|TestGateCommandFromTheRealTodoLinesPassesOnACorrectTree|TestLandGatePrintsNothingPassesWhenTheCommandPrintsNothing|TestLandGatePrintsNothingFailsWhenTheCommandPrints|TestLandGatePrintsNothingFailsOnAnErrorMessage|TestLandGatePrintsNothingFailsOnNewlineOnlyOutput|TestLandGatePrintsALiteralPassesWhenTheOutputContainsItAndNeverRunsIt|TestLandGatePrintsALiteralFailsWhenTheOutputLacksIt|TestLandGatePrintsALiteralFailsWhenTheCommandFails|TestItemGateCopiesNonGoTestsIntoTheRedWorktree|TestIsTestPathCoversOtherLanguagesAndNestedTestDirs|TestForeignTestEditMatchesTheWidenedTestPaths|TestForeignTestEditQuietOnProductionLookalikes)$'`
