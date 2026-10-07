# Milestone 18 — Static analysis

ADR-89 added this milestone. r-loop now runs local static analyzers on the change a step made and treats their output as one more reviewer, called `static`, in every review round of implement and gatefix. Which analyzers run depends on the languages the change touches. Go gets golangci-lint and govulncheck through `go run`. Maven and Gradle get PMD, and SpotBugs with find-sec-bugs, through the project's own build. Semgrep runs when it is installed. None of this needs configuration, and nothing has to be installed beyond the language toolchain.

| Phase | Landed as | Plan |
|---|---|---|
| 47 — Detection, findings from reports, config and preflight | `94b12de` (work `83b4f8d`, plan `a1708b8`) | `.task-plans/phase-47-detection-findings-from-reports-conf.md` |
| 48 — The toolchains: Go, Maven, Gradle and Semgrep | `03580b4` (work `d13d423`, plan `5c70d93`) | `.task-plans/phase-48-the-toolchains-go-maven-gradle-and-s.md` |
| 49 — The `static` reviewer in every review round | `4c376e7` (work `b3cce1b`, plan `73e6238`) | `.task-plans/phase-49-the-static-reviewer-in-every-review.md` |

---

## Phase 47 — Detection, findings from reports, config and preflight

### What it built

This phase built everything ADR-89 needs except running an analyzer.

- **The `core.Analyzer` port** (`internal/core/ports.go`) is `Analyze(ctx, dir) (Analysis, error)`. `Analysis` carries `Findings []Finding` and `Command`, which names the tools that ran. The core fake (`internal/core/fakes_test.go`) returns scripted results and errors.
- **`analyze.Detect(root)`** (`internal/analyze/detect.go`) walks the tree and finds every module. It skips `.git`, `.r-loop`, `vendor`, `node_modules` and `testdata`.
  - A `go.mod` directory is a `go` module.
  - A `pom.xml` directory is a `maven` module, and a `build.gradle(.kts)` directory is a `gradle` module. Each gets a build root: the outermost `pom.xml` or `settings.gradle(.kts)` directory above it.
  - The runner is `mvnw`/`gradlew` when the build root has one, otherwise `mvn`/`gradle` on `PATH`.
  - `Touched` maps each changed file to its nearest module and returns each touched module with its files (`Touch{Module, Files}`).
- **The changed set** (`internal/analyze/lines.go`) is `git diff -U0 HEAD` plus untracked files. The diff command overrides the user git config settings that would break hunk parsing: `diff.noprefix`, `mnemonicPrefix`, `color.diff`, `diff.relative`, external drivers and inter-hunk context. It also passes `--no-renames`, so a moved file counts as new and every line of it as added. Paths with spaces or non-ASCII characters are unquoted.
- **Report parsing** has two parsers, one per report format:
  - `ParseSARIF` (`internal/analyze/sarif.go`) resolves `originalUriBaseIds`, artifact and rule indices, and message ids with their arguments.
  - `ParsePMD` (`internal/analyze/pmdxml.go`) maps PMD priority to a level.

  Both return neutral `Hit`s. One `Findings(dir, Changes, hits)` function then does the rest:
  - It keeps a hit only when its start line was added or modified, or its file is untracked. govulncheck hits are always kept.
  - It sorts the hits by severity, then by path, line, tool, rule and message.
  - It numbers them `s1…sN`. `Title` is `<tool>/<rule>: <first line>`, and `Detail` is the level, the full message and `path:line`.
  - It keeps at most 50. One final finding then counts the rest by tool and rule.
- **Config**: `analyze.enabled` (default `true`) and `analyze.timeout` (default `15m`), both with provenance. Unknown keys exit 2. Booleans must be exactly `true` or `false`, through a new strict `resolver.boolean` getter. `README.md` documents the keys and says the analyzers need no install.
- **Preflight** (`internal/app/preflight.go`, in `Wiring.checks()`, so resume runs it too) runs detection on the primary tree.
  - When a detected language's toolchain is missing, it exits 2 and names the toolchain: `go`, or the runner plus `java`. Under `--dry-run` this check is skipped.
  - It prints `static: <languages>[, semgrep]` or `static: <languages> (semgrep not installed)`. With no detected language it prints `static: semgrep` or `static: none (semgrep not installed)`.

### Tests that prove it

- `detect_test.go`:
  - Module, build-root and runner detection for Go, multi-module Maven with and without `mvnw`, and Gradle (`TestDetectGivesEachMavenModuleTheOutermostPomAndItsWrapper`, `TestDetectRunsMavenFromPathWithoutAWrapper`, `TestDetectGivesEachGradleModuleTheOutermostSettingsDirectoryAndItsWrapper`, …).
  - The skipped directories (`TestDetectSkipsDirectoriesThatHoldNoProjectModules`).
  - The touch rules, including `go.mod`/`go.sum` (`TestTouchedCountsAGoModulesOwnGoModAndGoSum`) and a change with no source files (`TestTouchedRunsNoToolchainForAChangeWithoutSourceFiles`).
- `lines_test.go` runs a real `git diff -U0` and covers:
  - added and modified lines;
  - a new untracked file, which counts whole;
  - a moved file, which counts whole under its new path;
  - deleted files, which are left out;
  - paths with spaces and non-ASCII names;
  - an added `+++` line, which is not read as a header;
  - the user's diff config and inter-hunk context, which are both ignored (`TestChangedIgnoresTheUsersDiffConfig`, `TestChangedIgnoresTheUsersInterHunkContext`).
- `sarif_test.go` and `pmdxml_test.go` cover:
  - parsing, and rejecting malformed input;
  - the changed-line filter (`TestFindingsKeepOnlyHitsOnChangedLinesOrInUntrackedFiles`);
  - the govulncheck exception (`TestFindingsKeepEveryGovulncheckHit`);
  - the id, title and detail format;
  - severity ordering;
  - the cap of 50, and that exactly 50 adds no summary (`TestFindingsCapAtFiftyAndCountTheRestByToolAndRule`, `TestFindingsAtExactlyFiftyAddNoSummary`).
- `reader_test.go` covers the defaults, provenance, a null value falling through to the machine file, unknown keys, a non-boolean `enabled` and a non-positive `timeout`.
- `app_test.go` covers:
  - the printed `static:` line (`TestPreflightPrintsTheStaticLine`);
  - the exit 2 that names a missing toolchain (`TestPreflightExitsTwoNamingAMissingToolchain`);
  - no toolchain check when analysis is off, or under dry run.

### Assumptions the plan recorded

- Finding ids are exactly `s1…sN`. Phase 49 owns how they meet core's `<reviewer>-r<round>-` id check.
- `ParseSARIF`'s caller passes the tool name (`golangci-lint`, `govulncheck`, `spotbugs`, `semgrep`). The SARIF driver name is not used, because it is unreliable.
- SARIF paths resolve against `originalUriBaseIds`, then against the directory the tool ran in.
- The `static:` line names module kinds (`go`, `maven`, `gradle`). An empty list prints `none`, or `semgrep` alone.
- Detection runs on the primary tree on every preflight and resume. A detection error exits 2.
- A missing toolchain exits 2, as the spec says. Missing provider binaries still exit 127. The README's row for exit code 2 now covers this case.
- A Go module's own `go.mod`/`go.sum` counts as touching it, so that govulncheck runs on a dependency bump. The watchdog settled this as q1, citing `spec.html:3898`.

---

## Phase 48 — The toolchains: Go, Maven, Gradle and Semgrep

### What it built

`analyze.New(timeout)` and `(*Analyzer).Analyze(ctx, dir)` (`internal/analyze/run.go`) chain phase 47's parts into one call: `Changed`, then `Detect`/`Touched`, then each touched toolchain, then `Findings`. The tools run one after another under a single `context.WithTimeout(analyze.timeout)`. Each command runs in its own process group, and a timeout kills that group, so `go run` children and JVMs do not outlive the timeout. `Command` lists the tools that ran.

- **`versions.go` pins** golangci-lint `v2.14.0`, govulncheck `v1.8.0`, maven-pmd-plugin `3.28.0`, spotbugs-maven-plugin `4.10.4.1`, spotbugs-gradle-plugin `6.5.12`, findsecbugs-plugin `1.14.0` and maven-dependency-plugin `3.11.0`. These were the newest releases as of 2026-10-06.
- **Go**: in each touched module, `go run …golangci-lint@v2.14.0 run --issues-exit-code=0 --path-mode=abs --output.sarif.path=…`.
  - It adds `--enable=gosec` unless the module or a parent up to the repository root has a `.golangci.{yml,yaml,toml,json}`.
  - When `go.mod` or `go.sum` changed, it also runs `go run …govulncheck@v1.8.0 -format sarif ./...`.
- **Maven**: one invocation per build root, `-pl <modules> -am`, with `-q -B -DskipTests compile` followed by the `maven-pmd-plugin:…:pmd` and `spotbugs-maven-plugin:…:spotbugs` goals.
  - SARIF output is on, and the find-sec-bugs jar is passed with `spotbugs.pluginList`.
  - The jar is fetched once with `dependency:copy` into `<user cache>/r-loop/analyze`. The download goes to a staging directory and is then renamed into place, so an interrupted download never leaves a jar that looks cached.
  - It reads each module's `target/pmd.xml` and `target/spotbugsSarif.json`.
- **Gradle**: one invocation per build root, `<runner> -q --init-script <init.gradle> :mod:pmdMain :mod:spotbugsMain…`. The root project's tasks have no prefix.
  - The embedded `init.gradle` puts the SpotBugs plugin on the classpath from the plugin portal. For each `java` project, under `afterEvaluate` with a `hasPlugin` guard, it applies `pmd` and SpotBugs with `ignoreFailures = true`, SARIF on and find-sec-bugs.
  - It adds `mavenCentral()` only to a project that has no repositories at the project or settings level.
  - It reads `build/reports/pmd/main.xml` and `build/reports/spotbugs/main.sarif`.
- **Existing configuration is kept**: a build that already configures PMD or SpotBugs keeps its setup, and no build file is written.
- **Semgrep**: when `semgrep` is on `PATH`, `semgrep scan --metrics=off --sarif --output … -- <changed files>`. The config is the repository's `.semgrep.yml` or `.semgrep/` when present, otherwise `p/default`.
- **Robustness**:
  - Stale module reports are deleted before each build, because phase 49 re-runs the analysis in the same worktree.
  - `dir` goes through `EvalSymlinks` first, so a symlinked worktree does not filter out every hit.
  - A module with no compiled main classes needs no report and contributes no hits. The maintainer chose this as watchdog q2.
- **Failures**: an analysis fails with `<tool>: <reason>` and the last 4096 bytes of that tool's output when:
  - a tool exits non-zero;
  - a report is missing or does not parse;
  - the analysis outlives the timeout;
  - the caller cancels the analysis.

### Tests that prove it

`run_test.go` uses stub `go`, `mvn`, `gradle` and `semgrep` executables on `PATH` and covers:

- **Go command lines**: `gosec`, and leaving it to a repository config (`TestAnalyzeRunsGolangciLintWithGosecInEachTouchedGoModule`, `TestAnalyzeLeavesGosecToTheRepositorysGolangciConfig`); govulncheck only on a `go.mod`/`go.sum` change.
- **Maven**: one run per build root with `-pl` (`TestAnalyzeRunsMavenOnceAtTheBuildRootForTheTouchedModules`), the wrapper with the root module as `.`, and the find-sec-bugs jar fetched once (`TestAnalyzeFetchesTheFindSecBugsJarOnceIntoTheUserCache`).
- **Gradle**: one run per build root with the init script, root-project task names, and the pinned versions substituted into the script (`TestAnalyzeHandsGradleTheEmbeddedInitScriptWithThePinnedVersions`).
- **Reports**: read from each touched Maven and Gradle module and from the Go and Semgrep runs, including under a symlinked directory.
- **Edge cases**: no report required from a module without main classes, stale reports ignored, nothing written into the tree except build output (`TestAnalyzeWritesNoFileIntoTheTreeButBuildOutput`).
- **Semgrep**: run only when it is present (`TestAnalyzeSkipsSemgrepWhenItIsNotOnPath`).
- **Nothing to analyze**: nothing runs when nothing changed or when no source file changed, and `Command` names every tool that ran.
- **Each failure and its message**:
  - a non-zero exit, a fetch that writes no jar, a missing report and an unparsable report;
  - the 4096-byte output tail, with trailing whitespace kept;
  - the timeout, and cancellation both during the run and after the last command;
  - unremovable stale reports and an unreadable classes directory;
  - no user cache directory, no temp directory and a missing `dir`.

### Assumptions the plan recorded

- The constructor is `New(timeout time.Duration) *Analyzer`. Adapters take primitives and never import `internal/config`, so phase 49 calls `analyze.New(cfg.Analyze.Timeout)`.
- `Changed`'s git errors are returned unchanged.
- `Command` is `""` when no tool ran.
- Projects ignore `target/` and `build/` in git. A project that tracks its build output would see analysis output as untracked files.
- Gradle project paths follow the directory layout (`app/sub` → `:app:sub`). A remapped `projectDir` fails as a `gradle` failure.
- The pinned golangci-lint and govulncheck need Go ≥ 1.26. This works under the default `GOTOOLCHAIN=auto`. With `GOTOOLCHAIN=local` and an older Go, the analysis fails with Go's own message.
- The unit tests use stubs only, so `go test` needs no JDK. The planner and the implementer checked `init.gradle` against a real Gradle by hand. The behaviour of the Maven goals is taken from the plugin descriptors.
- The watchdog's warning, that phase 47 shipped no `New`/`Analyze`, is resolved by building both in `run.go`.

---

## Phase 49 — The `static` reviewer in every review round

### What it built

- **`ReviewHalf.Analyzer`** (`internal/core/review.go`) is set from a new `SessionManager.Analyzer` field, which `DefaultRunners` copies. A nil analyzer means no `static` reviewer.
  - For a step whose check is `diff` (implement and gatefix), each round starts `Analyze(ctx, worker.Dir)` in a goroutine right after the `review-round` event, before the panes open.
  - The round waits for it right after `WaitAll`, before `join`. If the panes fail to open, the analysis is cancelled and waited for.
  - Plan steps never run it.
- **Findings file and event**:
  - The findings go to `<kind>-findings-static-r<N>.json` with `reviewer: static`. Core prefixes each id as `static-r<N>-sK`, so the ids pass `checkFindingsFile` and stay unique across rounds.
  - A `review-find{round, reviewer: static, state, findings, command: "analyze <Command>"}` event is recorded.
  - `singleRunner` no longer requires pane reviewers, so a row with no other reviewers still runs the round.
- **One more reviewer downstream**: `static`'s file is part of the fix half's `FindingsFiles`, so the verdict must answer its ids. It is also part of the next round's prior findings, and resume rebuilds earlier rounds' paths with `static` among the reviewer ids.
- **Failure**:
  - An `Analyze` error, or a panic in it, raises a `reviewer` blocker on `phase-<N>/<kind>-rv-static`, settled by `settleStatic`. Its actions are retry (analyze again), skip (records `reviewer-skipped{reviewer: static}` and removes an earlier attempt's file), block and stop.
  - With no raiser, the error fails the step the way a reviewer failure does.
  - A findings file that cannot be written also fails the step.
- **`fix.md`** gets a paragraph, rendered only when a `static` file is present. It says these findings come from analyzers, that a rule hit is not a defect by itself, and that a false positive is `not-real`, with the `path:line` that shows it.
- **Wiring**: `internal/app/wire.go` builds `analyze.New(cfg.Analyze.Timeout)` when `analyze.enabled` is set, and nil otherwise.
- **Config** (`internal/config/reader.go`):
  - A configured reviewer named `static` is rejected with exit 2.
  - A `diff` row with rounds and no pane reviewers now needs `review_timeout` when analysis is on.
- **Changes outside the phase's `Files:`**:
  - `runners.go`: the reviewer guard, and passing the analyzer through.
  - `gatecommand_test.go`: it now finds `Done when:` lines by their phase heading instead of fixed line numbers that had drifted. The suite was red before this phase.
  - `reader.go`/`reader_test.go`: the reserved name.
  - `app_test.go`: a fake `semgrep` on `PATH`, so wired tests never fetch rules over the network.

### Tests that prove it

- `review_test.go`, with the fake analyzer, proves:
  - **Findings file and event**: the file is written with round-prefixed ids, and the analysis is recorded as a `static` `review-find`.
  - **Fix half**: the findings reach the fix half, and a verdict that leaves a `static` id unanswered fails the fix (`TestStaticsFindingsFileJoinsTheFixHalf`, `TestAVerdictThatLeavesAStaticFindingUnansweredFailsTheFix`).
  - **Clean rounds**: zero findings leave a clean round clean.
  - **Concurrency**: the analysis runs while the panes work (`TestTheAnalysisRunsWhileTheReviewerPanesWork`), and a failed pane open cancels it before the step fails.
  - **When it runs**: plan steps never run it, a nil analyzer adds no reviewer, and a step with no other reviewers still runs the round, both directly and through `DefaultRunners`.
  - **Later rounds and resume**: the next round's prior findings include `static`'s file, and a resumed round lists earlier `static` files but leaves out one that was never written.
  - **Errors**: an error raises a `static` blocker or, with no raiser, fails the step. Block and stop on that blocker behave as they should, a panicking analyzer fails the step, and an unwritable findings file fails the step.
- `blockers_test.go` proves that retry analyzes the round again and that skip finishes the round without `static`.
- `render_test.go` proves the `fix.md` wording appears with a `static` file and is left out without one.
- `app_test.go` proves the wiring for both settings (`TestAWiredRunReviewsWithTheAnalyzerWhenAnalyzeIsEnabled`, `…WhenAnalyzeIsOff`).
- `reader_test.go` proves that a reviewer named `static` is rejected, and the `review_timeout` rule for reviewer-less `diff` rows.

### Assumptions the plan recorded

- A row with `rounds: 0` has no review rounds, so it never runs `static`.
- `static`'s `review-find` is recorded after the panes' events. With no raiser and a failed pane, the step fails with the pane's reason first.
- A failed analysis, or one with an empty `Command`, is recorded with `command` `analyze`.
- `static` runs for gatefix too, because gatefix's check is `diff`.
- The name `static` is reserved on every row, plan rows included.
- If a project leaves `target/` or `build/` untracked and not ignored, `checkTree` reports the analysis output as a tree change. That belongs to the `analyze` package.
- A retried analysis ignores the resolution's `Addendum`, because `static` has no prompt.
- Watchdog warning 1 (ids, and the `analyze.New` signature) and warning 2 (the already-red `gatecommand_test`) are both resolved in this phase.

---

## What the milestone makes possible

Every implement and gatefix step is now reviewed by deterministic tools as well as by models. A rule hit on a changed line reaches the step session in the same findings format and the same verdict check as any reviewer's finding. Real P1/P2 hits get fixed, and false positives are dismissed with a `path:line` as evidence. All of it happens before the driver commits.

- **No setup**: Go, Maven and Gradle repositories get gosec, govulncheck, PMD, SpotBugs and find-sec-bugs. Semgrep is added when it is installed.
  - Analyzer versions are pinned in the binary.
  - The analyzers are fetched on demand and run through the project's own wrapper and build.
  - A repository's own configuration is kept, and no build file is written.
- **Bounded noise**: only findings on lines the step added or changed reach the review, with the most severe first and at most 50 per round. Dependency vulnerabilities are always reported.
- **Failures surface**: a missing toolchain stops the run at preflight with exit 2 and names the toolchain. An analysis that fails mid-run becomes a `reviewer` blocker the watchdog can retry, skip, block or stop, in keeping with "a gone dependency halts, never falls back".
- **An analysis-only review**: a `diff` row with no reviewer panes still gets an analyzer-only review round each round. The maintainer can set `analyze.enabled: false` to turn it off, and `analyze.timeout` bounds how long it runs.
